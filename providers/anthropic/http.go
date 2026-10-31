package anthropic

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

func failure(code gateway.ErrorCode, op, message string, cause error) *gateway.Error {
	return &gateway.Error{Code: code, Provider: "anthropic", Operation: op, Message: message, Cause: cause}
}

func operationError(op string, err error) error {
	var problem *gateway.Error
	if errors.As(err, &problem) {
		copy := *problem
		copy.Provider, copy.Operation = "anthropic", op
		return &copy
	}
	return err
}

func ioFailure(ctx context.Context, op string, err error) *gateway.Error {
	if ctx.Err() != nil {
		code := gateway.Cancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = gateway.Timeout
		}
		return failure(code, op, "operation context ended", ctx.Err())
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return failure(gateway.Timeout, op, "upstream timed out", err)
	}
	return failure(gateway.UpstreamError, op, "upstream transport failed", err)
}

type wireError struct {
	Type string `json:"type"`
}

func remoteError(op string, remote *wireError) *gateway.Error {
	code := gateway.UpstreamError
	if remote != nil {
		switch remote.Type {
		case "authentication_error", "permission_error":
			code = gateway.AuthenticationFailed
		case "invalid_request_error":
			code = gateway.InvalidRequest
		case "rate_limit_error":
			code = gateway.RateLimited
		case "overloaded_error":
			code = gateway.CapacityUnavailable
		}
	}
	problem := failure(code, op, "upstream returned an error", nil)
	problem.Retryable = code == gateway.RateLimited || code == gateway.CapacityUnavailable
	return problem
}

func (p *Provider) request(ctx context.Context, op, method, path string, body any, streaming bool) (*http.Response, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, failure(gateway.InvalidRequest, op, "request cannot be encoded", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, failure(gateway.InvalidConfiguration, op, "request URL is invalid", nil)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, ioFailure(ctx, op, err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 2048))
		code := gateway.UpstreamError
		switch response.StatusCode {
		case 400, 413, 422:
			code = gateway.InvalidRequest
		case 401, 403:
			code = gateway.AuthenticationFailed
		case 408, 504:
			code = gateway.Timeout
		case 429:
			code = gateway.RateLimited
		case 529:
			code = gateway.CapacityUnavailable
		}
		problem := failure(code, op, "upstream rejected the request", nil)
		problem.UpstreamStatus = response.StatusCode
		problem.Retryable = response.StatusCode == 408 || response.StatusCode == 429 || response.StatusCode >= 500
		problem.RequestID = p.requestID(response.Header.Get("request-id"))
		return nil, problem
	}
	return response, nil
}

func (p *Provider) requestID(id string) string {
	if len(id) > 128 || strings.Contains(id, p.apiKey) {
		return ""
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_.", char)) {
			return ""
		}
	}
	return id
}

func decode(ctx context.Context, op string, r io.Reader, limit int64, target any) (int64, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return int64(len(data)), ioFailure(ctx, op, err)
	}
	if int64(len(data)) > limit {
		return int64(len(data)), failure(gateway.InvalidResponse, op, "response exceeds configured size limit", nil)
	}
	if json.Unmarshal(data, target) != nil {
		return int64(len(data)), failure(gateway.InvalidResponse, op, "upstream returned invalid JSON", nil)
	}
	return int64(len(data)), nil
}

type wireUsage struct {
	Input  *int64 `json:"input_tokens"`
	Output *int64 `json:"output_tokens"`
}

func mergeUsage(current gateway.Usage, remote *wireUsage) (gateway.Usage, error) {
	if remote == nil {
		return current, nil
	}
	for _, count := range []*int64{remote.Input, remote.Output} {
		if count != nil && *count < 0 {
			return current, failure(gateway.InvalidResponse, "", "negative token usage", nil)
		}
	}
	if remote.Input != nil {
		value := *remote.Input
		current.Input = gateway.TokenUsage{Tokens: &value, Source: gateway.UsageReported}
	}
	if remote.Output != nil {
		value := *remote.Output
		current.Output = gateway.TokenUsage{Tokens: &value, Source: gateway.UsageReported}
	}
	return current, nil
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

func finishReason(native string) gateway.FinishReason {
	switch native {
	case "end_turn", "stop_sequence":
		return gateway.FinishStop
	case "max_tokens":
		return gateway.FinishMaxTokens
	case "tool_use":
		return gateway.FinishToolUse
	case "refusal":
		return gateway.FinishContentFilter
	default:
		return gateway.FinishReason(native)
	}
}
