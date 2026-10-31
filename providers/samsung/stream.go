package samsung

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

func (p *Provider) Stream(ctx context.Context, request gateway.ChatRequest, callback func(gateway.Event) error) (result gateway.ChatResult, err error) {
	const op = "stream"
	defer func() { err = operationError(op, err) }()
	if callback == nil {
		return result, failure(gateway.InvalidRequest, op, "stream callback is required", nil)
	}
	agent, body, err := p.prepare(request)
	if err != nil {
		return result, err
	}
	ctx, cancel, err := p.operationContext(ctx, true)
	if err != nil {
		return result, err
	}
	defer cancel()
	response, err := p.request(ctx, op, agent, body, true)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	var sequence uint64
	callbackFailed := false
	emit := func(event gateway.Event) error {
		sequence++
		event.Sequence = sequence
		if callbackErr := callback(event); callbackErr != nil {
			callbackFailed = true
			cancel()
			return failure(gateway.CallbackFailed, op, "stream callback failed", callbackErr)
		}
		return nil
	}
	reader := &io.LimitedReader{R: response.Body, N: p.maxResponseBytes + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, min(4096, p.maxLineBytes+2)), p.maxLineBytes+2)
	var text strings.Builder
	appendText := func(chunk string) error {
		if chunk == "" {
			return nil
		}
		text.WriteString(chunk)
		return emit(gateway.Event{Type: gateway.EventTextDelta, Text: chunk})
	}
	ended := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			err = ioFailure(ctx, op, ctx.Err())
			break
		}
		line := scanner.Bytes()
		if len(line) > p.maxLineBytes {
			err = failure(gateway.InvalidResponse, op, "NDJSON line exceeds configured limit", nil)
			break
		}
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &event) != nil || event.Event == "" {
			err = failure(gateway.InvalidResponse, op, "invalid Agent Builder NDJSON event", nil)
			break
		}
		if event.Event == "error" || len(event.Error) > 0 && string(event.Error) != "null" && string(event.Error) != "false" {
			err = failure(gateway.UpstreamError, op, "Agent Builder stream returned an error", nil)
			break
		}
		switch event.Event {
		case "token":
			var data struct {
				Chunk *string `json:"chunk"`
			}
			if json.Unmarshal(event.Data, &data) != nil || data.Chunk == nil {
				err = failure(gateway.InvalidResponse, op, "invalid Agent Builder token event", nil)
			} else {
				err = appendText(*data.Chunk)
			}
		case "add_message":
			var data struct {
				SessionID string `json:"session_id"`
				Error     bool   `json:"error"`
			}
			if json.Unmarshal(event.Data, &data) != nil {
				err = failure(gateway.InvalidResponse, op, "invalid Agent Builder message event", nil)
			} else if data.Error {
				err = failure(gateway.UpstreamError, op, "Agent Builder message reported an error", nil)
			} else if data.SessionID != "" {
				result.SessionID = data.SessionID
			}
		case "end":
			var data struct {
				Result *wireResult `json:"result"`
			}
			if len(event.Data) > 0 && json.Unmarshal(event.Data, &data) != nil {
				err = failure(gateway.InvalidResponse, op, "invalid Agent Builder end event", nil)
				break
			}
			if data.Result != nil {
				if data.Result.SessionID != "" {
					result.SessionID = data.Result.SessionID
				}
				if final := extractText(data.Result); final != "" {
					if !strings.HasPrefix(final, text.String()) {
						err = failure(gateway.InvalidResponse, op, "Agent Builder final text diverges from delivered deltas", nil)
						break
					}
					err = appendText(final[text.Len():])
				}
			}
			if err == nil {
				ended = true
			}
			// Unknown event names are ignored for protocol forward compatibility.
		}
		if err != nil || ended {
			break
		}
	}
	result.Text = text.String()
	if callbackFailed {
		return result, err
	}
	if err == nil && scanner.Err() != nil {
		if errors.Is(scanner.Err(), bufio.ErrTooLong) {
			err = failure(gateway.InvalidResponse, op, "NDJSON line exceeds configured limit", nil)
		} else {
			err = ioFailure(ctx, op, scanner.Err())
		}
	}
	if reader.N <= 0 {
		err = failure(gateway.InvalidResponse, op, "stream exceeds configured response limit", nil)
	}
	if ctx.Err() != nil {
		err = ioFailure(ctx, op, ctx.Err())
	}
	if err == nil && !ended {
		err = failure(gateway.StreamIncomplete, op, "Agent Builder stream ended without end event", nil)
	}
	if err == nil && strings.TrimSpace(result.Text) == "" {
		err = failure(gateway.InvalidResponse, op, "Agent Builder returned no answer text", nil)
	}
	if err != nil {
		var problem *gateway.Error
		if !errors.As(err, &problem) {
			problem = ioFailure(ctx, op, err)
		} else {
			copy := *problem
			problem = &copy
		}
		problem.Provider, problem.Operation = "samsung", op
		problem.RequestID = p.requestID(response.Header.Get("x-request-id"))
		copy := *problem
		if callbackErr := emit(gateway.Event{Type: gateway.EventError, Error: &copy}); callbackErr != nil {
			return result, callbackErr
		}
		return result, problem
	}
	result.FinishReason, result.ProtocolComplete = gateway.FinishStop, true
	snapshot := result
	if err := emit(gateway.Event{Type: gateway.EventCompleted, Result: &snapshot}); err != nil {
		return result, err
	}
	return result, nil
}
