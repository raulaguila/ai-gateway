package openai

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

var errStreamDone = errors.New("stream terminal marker")

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
	response, err := p.request(ctx, op, http.MethodPost, "/chat/completions", body, true)
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
	sawChoice, finished := false, false
	var text strings.Builder
	err = sse.Read(reader, p.maxEventBytes, func(kind, data string) error {
		if err := ctx.Err(); err != nil {
			return ioFailure(ctx, op, err)
		}
		if kind == "error" {
			return failure(gateway.UpstreamError, op, "upstream stream returned an error", nil)
		}
		if data == "[DONE]" {
			return errStreamDone
		}
		var chunk struct {
			ID      string `json:"id"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content      *string           `json:"content"`
					ToolCalls    []json.RawMessage `json:"tool_calls"`
					FunctionCall json.RawMessage   `json:"function_call"`
					Refusal      string            `json:"refusal"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *wireUsage      `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return failure(gateway.InvalidResponse, op, "invalid JSON in SSE event", nil)
		}
		if hasRemoteError(chunk.Error) {
			return failure(gateway.UpstreamError, op, "upstream stream returned an error", nil)
		}
		if chunk.ID != "" {
			if result.ID != "" && result.ID != chunk.ID {
				return failure(gateway.InvalidResponse, op, "stream changed response identity", nil)
			}
			result.ID = chunk.ID
		}
		if len(chunk.Choices) > 1 {
			return failure(gateway.InvalidResponse, op, "expected at most one stream choice", nil)
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 || finished {
				return failure(gateway.InvalidResponse, op, "unexpected choice after stream finish", nil)
			}
			sawChoice = true
			if len(choice.Delta.ToolCalls) > 0 || hasRemoteError(choice.Delta.FunctionCall) || choice.FinishReason == "tool_calls" || choice.FinishReason == "function_call" {
				return failure(gateway.UnsupportedCapability, op, "tool streaming is not implemented", nil)
			}
			if choice.Delta.Refusal != "" {
				result.FinishReason = gateway.FinishContentFilter
			}
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				text.WriteString(*choice.Delta.Content)
				if err := emit(gateway.Event{Type: gateway.EventTextDelta, Text: *choice.Delta.Content}); err != nil {
					return err
				}
			}
			if choice.FinishReason != "" {
				finished = true
				result.NativeFinishReason = choice.FinishReason
				if result.FinishReason != gateway.FinishContentFilter {
					result.FinishReason = finishReason(choice.FinishReason)
				}
			}
		}
		if chunk.Usage != nil {
			usage, err := mergeUsage(result.Usage, chunk.Usage)
			if err != nil {
				return err
			}
			result.Usage = usage
			snapshot := copyUsage(usage)
			if err := emit(gateway.Event{Type: gateway.EventUsage, Usage: &snapshot}); err != nil {
				return err
			}
		}
		return nil
	})
	result.Text = text.String()
	if callbackFailed {
		return result, err
	}
	if reader.N <= 0 {
		err = failure(gateway.InvalidResponse, op, "stream exceeds configured response limit", nil)
	}
	if ctx.Err() != nil {
		err = ioFailure(ctx, op, ctx.Err())
	}
	if err == nil && !finished {
		err = failure(gateway.StreamIncomplete, op, "stream ended without a terminal marker", nil)
	}
	if errors.Is(err, errStreamDone) {
		err = nil
	}
	if err == nil && (!sawChoice || result.Text == "" && result.FinishReason != gateway.FinishContentFilter && result.FinishReason != gateway.FinishMaxTokens) {
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
		problem.Provider, problem.Operation = "openai_compatible", op
		eventProblem := *problem
		if callbackErr := emit(gateway.Event{Type: gateway.EventError, Error: &eventProblem}); callbackErr != nil {
			return result, callbackErr
		}
		return result, problem
	}
	if result.FinishReason == "" {
		result.FinishReason = gateway.FinishStop
	}
	result.ProtocolComplete = true
	snapshot := result
	snapshot.Usage = copyUsage(result.Usage)
	if err := emit(gateway.Event{Type: gateway.EventCompleted, Result: &snapshot}); err != nil {
		return result, err
	}
	return result, nil
}

func copyUsage(usage gateway.Usage) gateway.Usage {
	if usage.Input.Tokens != nil {
		count := *usage.Input.Tokens
		usage.Input.Tokens = &count
	}
	if usage.Output.Tokens != nil {
		count := *usage.Output.Tokens
		usage.Output.Tokens = &count
	}
	return usage
}
