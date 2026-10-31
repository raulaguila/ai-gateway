package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

func failure(code gateway.ErrorCode, operation, message string, cause error) *gateway.Error {
	return &gateway.Error{Code: code, Provider: "openai_compatible", Operation: operation, Message: message, Cause: cause}
}

func operationError(operation string, err error) error {
	var problem *gateway.Error
	if errors.As(err, &problem) {
		copy := *problem
		copy.Provider, copy.Operation = "openai_compatible", operation
		return &copy
	}
	return err
}

func ioFailure(ctx context.Context, operation string, err error) *gateway.Error {
	if ctx.Err() != nil {
		code := gateway.Cancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = gateway.Timeout
		}
		return failure(code, operation, "operation context ended", ctx.Err())
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return failure(gateway.Timeout, operation, "upstream timed out", err)
	}
	return failure(gateway.UpstreamError, operation, "upstream transport failed", err)
}

func (p *Provider) request(ctx context.Context, operation, method, path string, body any, streaming bool) (*http.Response, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, failure(gateway.InvalidRequest, operation, "request cannot be encoded", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, failure(gateway.InvalidConfiguration, operation, "request URL is invalid", nil)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, ioFailure(ctx, operation, err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		// Error bodies may reflect prompts or secrets; never include them in errors.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 2048))
		code := gateway.UpstreamError
		switch response.StatusCode {
		case 400, 422:
			code = gateway.InvalidRequest
		case 401, 403:
			code = gateway.AuthenticationFailed
		case 408, 504:
			code = gateway.Timeout
		case 429:
			code = gateway.RateLimited
		}
		problem := failure(code, operation, "upstream rejected the request", nil)
		problem.UpstreamStatus = response.StatusCode
		problem.Retryable = response.StatusCode == 408 || response.StatusCode == 429 || response.StatusCode >= 500
		problem.RequestID = p.requestID(response)
		return nil, problem
	}
	return response, nil
}

func (p *Provider) requestID(response *http.Response) string {
	id := response.Header.Get("X-Request-Id")
	if len(id) > 128 || p.apiKey != "" && strings.Contains(id, p.apiKey) {
		return ""
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_.", char)) {
			return ""
		}
	}
	return id
}

func (p *Provider) decode(ctx context.Context, operation string, r io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, p.maxResponseBytes+1))
	if err != nil {
		return ioFailure(ctx, operation, err)
	}
	if int64(len(data)) > p.maxResponseBytes {
		return failure(gateway.InvalidResponse, operation, "response exceeds configured size limit", nil)
	}
	if json.Unmarshal(data, target) != nil {
		return failure(gateway.InvalidResponse, operation, "upstream returned invalid JSON", nil)
	}
	return nil
}

type wireUsage struct {
	Prompt     *int64 `json:"prompt_tokens"`
	Completion *int64 `json:"completion_tokens"`
}

func mergeUsage(current gateway.Usage, remote *wireUsage) (gateway.Usage, error) {
	if remote == nil {
		return current, nil
	}
	for _, count := range []*int64{remote.Prompt, remote.Completion} {
		if count != nil && *count < 0 {
			return current, failure(gateway.InvalidResponse, "", "negative token usage", nil)
		}
	}
	if remote.Prompt != nil {
		value := *remote.Prompt
		current.Input = gateway.TokenUsage{Tokens: &value, Source: gateway.UsageReported}
	}
	if remote.Completion != nil {
		value := *remote.Completion
		current.Output = gateway.TokenUsage{Tokens: &value, Source: gateway.UsageReported}
	}
	return current, nil
}

func finishReason(native string) gateway.FinishReason {
	switch native {
	case "stop":
		return gateway.FinishStop
	case "length":
		return gateway.FinishMaxTokens
	case "tool_calls":
		return gateway.FinishToolUse
	case "content_filter":
		return gateway.FinishContentFilter
	default:
		return gateway.FinishReason(native)
	}
}
