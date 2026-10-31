package samsung

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

func failure(code gateway.ErrorCode, op, message string, cause error) *gateway.Error {
	return &gateway.Error{Code: code, Provider: "samsung", Operation: op, Message: message, Cause: cause}
}

func operationError(op string, err error) error {
	var problem *gateway.Error
	if errors.As(err, &problem) {
		copy := *problem
		copy.Provider, copy.Operation = "samsung", op
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
	return failure(gateway.UpstreamError, op, "Agent Builder transport failed", err)
}

func (p *Provider) request(ctx context.Context, op, agent string, body wireRequest, stream bool) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, failure(gateway.InvalidRequest, op, "request cannot be encoded", nil)
	}
	endpoint := p.baseURL + "/api/v1/run/" + url.PathEscape(agent) + "?stream=" + strconv.FormatBool(stream)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, failure(gateway.InvalidConfiguration, op, "Agent Builder request URL is invalid", nil)
	}
	request.Header.Set("x-api-key", p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if stream {
		request.Header.Set("Accept", "application/x-ndjson")
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, ioFailure(ctx, op, err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
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
		problem := failure(code, op, "Agent Builder rejected the request", nil)
		problem.UpstreamStatus = response.StatusCode
		problem.Retryable = response.StatusCode == 408 || response.StatusCode == 429 || response.StatusCode >= 500
		problem.RequestID = p.requestID(response.Header.Get("x-request-id"))
		return nil, problem
	}
	return response, nil
}

func (p *Provider) requestID(id string) string {
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
