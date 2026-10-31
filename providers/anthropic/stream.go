package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/internal/sse"
)

var errMessageStop = errors.New("Anthropic message_stop")

func (p *Provider) Stream(ctx context.Context, request gateway.ChatRequest, callback func(gateway.Event) error) (result gateway.ChatResult, err error) {
	const op = "stream"
	defer func() { err = operationError(op, err) }()
	if callback == nil {
		return result, failure(gateway.InvalidRequest, op, "stream callback is required", nil)
	}
	body, err := p.chatBody(request, true)
	if err != nil {
		return result, err
	}
	ctx, cancel, err := p.operationContext(ctx, true)
	if err != nil {
		return result, err
	}
	defer cancel()
	response, err := p.request(ctx, op, http.MethodPost, "/v1/messages", body, true)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return result, failure(gateway.InvalidResponse, op, "expected an SSE response", nil)
	}
	var sequence uint64
	callbackFailed := false
	emit := func(event gateway.Event) error {
		sequence++
		event.Sequence = sequence
		if err := callback(event); err != nil {
			callbackFailed = true
			cancel()
			return failure(gateway.CallbackFailed, op, "stream callback failed", err)
		}
		return nil
	}
	reader := &io.LimitedReader{R: response.Body, N: p.maxResponseBytes + 1}
	started, deltaSeen := false, false
	active := ""
	nextIndex := 0
	var text strings.Builder
	invalid := func() error {
		return failure(gateway.InvalidResponse, op, "invalid Anthropic stream event order or payload", nil)
	}
	appendText := func(value string) error {
		if value == "" {
			return nil
		}
		text.WriteString(value)
		return emit(gateway.Event{Type: gateway.EventTextDelta, Text: value})
	}
	applyUsage := func(remote *wireUsage) error {
		if remote == nil {
			return nil
		}
		usage, err := mergeUsage(result.Usage, remote)
		if err != nil {
			return err
		}
		result.Usage = usage
		snapshot := copyUsage(usage)
		return emit(gateway.Event{Type: gateway.EventUsage, Usage: &snapshot})
	}
	err = sse.Read(reader, p.maxEventBytes, func(kind, data string) error {
		if err := ctx.Err(); err != nil {
			return ioFailure(ctx, op, err)
		}
		var event struct {
			Type         string        `json:"type"`
			Index        *int          `json:"index"`
			Message      *wireResponse `json:"message"`
			ContentBlock *wireBlock    `json:"content_block"`
			Delta        struct {
				Type       string  `json:"type"`
				Text       *string `json:"text"`
				StopReason string  `json:"stop_reason"`
			} `json:"delta"`
			Usage *wireUsage `json:"usage"`
			Error *wireError `json:"error"`
		}
		if json.Unmarshal([]byte(data), &event) != nil || event.Type == "" || kind != "" && kind != event.Type {
			return invalid()
		}
		if event.Type == "error" || event.Error != nil {
			return remoteError(op, event.Error)
		}
		switch event.Type {
		case "message_start":
			m := event.Message
			if started || m == nil || m.Type != "message" || strings.TrimSpace(m.ID) == "" || m.Role != gateway.RoleAssistant || len(m.Content) != 0 || m.StopReason != "" {
				return invalid()
			}
			started = true
			result.ID = m.ID
			return applyUsage(m.Usage)
		case "content_block_start":
			if !started || deltaSeen || active != "" || event.Index == nil || *event.Index != nextIndex || event.ContentBlock == nil {
				return invalid()
			}
			block := event.ContentBlock
			active = block.Type
			switch block.Type {
			case "text":
				if block.Text == nil {
					return invalid()
				}
				return appendText(*block.Text)
			case "thinking", "redacted_thinking":
				return nil
			default:
				return failure(gateway.UnsupportedCapability, op, "stream contains unsupported content blocks", nil)
			}
		case "content_block_delta":
			if !started || active == "" || event.Index == nil || *event.Index != nextIndex || event.Delta.Type == "" {
				return invalid()
			}
			switch event.Delta.Type {
			case "text_delta":
				if active != "text" || event.Delta.Text == nil {
					return invalid()
				}
				return appendText(*event.Delta.Text)
			case "thinking_delta", "signature_delta":
				if active != "thinking" {
					return invalid()
				}
			case "input_json_delta":
				return failure(gateway.UnsupportedCapability, op, "tool streaming is not implemented", nil)
			}
		case "content_block_stop":
			if !started || active == "" || event.Index == nil || *event.Index != nextIndex {
				return invalid()
			}
			active = ""
			nextIndex++
		case "message_delta":
			if !started || active != "" {
				return invalid()
			}
			deltaSeen = true
			if reason := event.Delta.StopReason; reason != "" {
				if result.NativeFinishReason != "" && result.NativeFinishReason != reason {
					return invalid()
				}
				result.NativeFinishReason, result.FinishReason = reason, finishReason(reason)
				if result.FinishReason == gateway.FinishToolUse {
					return failure(gateway.UnsupportedCapability, op, "tool streaming is not implemented", nil)
				}
			}
			return applyUsage(event.Usage)
		case "message_stop":
			if !started || active != "" || !deltaSeen || result.NativeFinishReason == "" {
				return invalid()
			}
			return errMessageStop
			// Unknown event types and pings are ignored for protocol forward compatibility.
		}
		return nil
	})
	result.Text = text.String()
	if callbackFailed {
		return result, err
	}
	if errors.Is(err, errMessageStop) {
		err = nil
	} else if err == nil {
		err = failure(gateway.StreamIncomplete, op, "stream ended before message_stop", nil)
	}
	if reader.N <= 0 {
		err = failure(gateway.InvalidResponse, op, "stream exceeds configured response limit", nil)
	}
	if ctx.Err() != nil {
		err = ioFailure(ctx, op, ctx.Err())
	}
	if err == nil && result.Text == "" && result.FinishReason != gateway.FinishMaxTokens && result.FinishReason != gateway.FinishContentFilter {
		err = failure(gateway.InvalidResponse, op, "stream returned no visible content", nil)
	}
	if err != nil {
		var problem *gateway.Error
		if !errors.As(err, &problem) {
			problem = ioFailure(ctx, op, err)
		} else {
			copy := *problem
			problem = &copy
		}
		problem.Provider, problem.Operation = "anthropic", op
		problem.RequestID = p.requestID(response.Header.Get("request-id"))
		eventProblem := *problem
		if callbackErr := emit(gateway.Event{Type: gateway.EventError, Error: &eventProblem}); callbackErr != nil {
			return result, callbackErr
		}
		return result, problem
	}
	result.ProtocolComplete = true
	snapshot := result
	snapshot.Usage = copyUsage(result.Usage)
	if err := emit(gateway.Event{Type: gateway.EventCompleted, Result: &snapshot}); err != nil {
		return result, err
	}
	return result, nil
}
