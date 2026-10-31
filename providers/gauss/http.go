package gauss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
)

func failure(code gateway.ErrorCode, op, message string, cause error) *gateway.Error {
	return &gateway.Error{Code: code, Provider: "gauss", Operation: op, Message: message, Cause: cause}
}

func contextError(err error) *gateway.Error {
	code := gateway.Cancelled
	if errors.Is(err, context.DeadlineExceeded) {
		code = gateway.Timeout
	}
	return failure(code, "", "operation context ended", err)
}

type transportFailure struct{ cause error }

func (e *transportFailure) Error() string { return "Gauss transport operation failed" }
func (e *transportFailure) Unwrap() error { return e.cause }

func (p *Provider) operationError(op string, err error) error {
	if err == nil {
		return nil
	}
	var transport *transportFailure
	if errors.As(err, &transport) {
		err = transport.cause
	}
	var problem *gateway.Error
	if errors.As(err, &problem) {
		copy := *problem
		copy.Provider, copy.Operation = "gauss", op
		copy.RequestID = p.requestID(copy.RequestID)
		return &copy
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		problem := contextError(err)
		problem.Operation = op
		return problem
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return failure(gateway.Timeout, op, "Gauss transport timed out", err)
	}
	return failure(gateway.UpstreamError, op, "Gauss operation failed", err)
}

func (p *Provider) requestID(id string) string {
	if len(id) > 128 || strings.Contains(id, p.openAPIToken) || strings.Contains(id, p.clientToken) {
		return ""
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_.", char)) {
			return ""
		}
	}
	return id
}

type gaussTransport struct {
	provider *Provider
	base     http.RoundTripper
}

func (t *gaussTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *gaussTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	delegated := false
	defer func() {
		if !delegated && request.Body != nil {
			request.Body.Close()
		}
	}()
	p := t.provider
	generation := request.Method == http.MethodPost
	for attempt := 0; attempt < p.maxAttempts; attempt++ {
		if err := p.admit(request.Context(), generation); err != nil {
			return nil, &transportFailure{err}
		}
		copy := request.Clone(request.Context())
		if attempt > 0 && request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, &transportFailure{failure(gateway.InvalidRequest, "", "request cannot be replayed after rate limiting", nil)}
			}
			copy.Body = body
		}
		copy.Header.Del("Authorization")
		copy.Header.Set("x-openapi-token", "Bearer "+p.openAPIToken)
		copy.Header.Set("x-generative-ai-client", p.clientToken)
		if generation {
			routing, _ := request.Context().Value(routingKey{}).(string)
			if routing == "" {
				return nil, &transportFailure{failure(gateway.InvalidConfiguration, "", "Gauss routing ID is missing", nil)}
			}
			copy.Header.Set("x-llm-model-id", routing)
		}
		delegated = true
		response, err := t.base.RoundTrip(copy)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusTooManyRequests {
			if response.StatusCode == http.StatusOK && request.Header.Get("Accept") == "text/event-stream" {
				response.Body = newEventBody(response.Body, p.maxResponseBytes, p.maxEventBytes)
				response.Header = response.Header.Clone()
				response.Header.Set("Content-Type", "text/event-stream")
				response.Header.Del("Content-Length")
				response.ContentLength = -1
			}
			return response, nil
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 2048))
		response.Body.Close()
		if readErr != nil {
			if request.Context().Err() != nil {
				return nil, &transportFailure{contextError(request.Context().Err())}
			}
			return nil, &transportFailure{failure(gateway.UpstreamError, "", "cannot read rate-limit response", nil)}
		}
		delay := retryDelay(response.Header, data, time.Now())
		processQuota.cooldown(p.scope, time.Now().Add(delay))
		if attempt+1 == p.maxAttempts {
			response.Body = io.NopCloser(bytes.NewReader(data))
			response.ContentLength = int64(len(data))
			return response, nil
		}
		if generation && request.GetBody == nil {
			return nil, &transportFailure{failure(gateway.RateLimited, "", "rate-limited request cannot be replayed", nil)}
		}
	}
	return nil, &transportFailure{failure(gateway.RateLimited, "", "Gauss attempt limit exhausted", nil)}
}

func retryDelay(header http.Header, body []byte, now time.Time) time.Duration {
	reference := now
	if serverDate, err := http.ParseTime(header.Get("Date")); err == nil {
		reference = serverDate
	}
	delay, valid := time.Duration(0), false
	if raw := header.Get("Retry-After"); raw != "" {
		if seconds, err := strconv.ParseUint(raw, 10, 64); err == nil {
			seconds = min(seconds, uint64((math.MaxInt64-int64(250*time.Millisecond))/int64(time.Second)))
			delay, valid = time.Duration(seconds)*time.Second, true
		} else if date, err := http.ParseTime(raw); err == nil {
			delay, valid = max(0, date.Sub(reference)), true
		}
	}
	var remote struct {
		Next  json.RawMessage `json:"nextAccessTime"`
		Error *struct {
			Next json.RawMessage `json:"nextAccessTime"`
		} `json:"error"`
		Data *struct {
			Next json.RawMessage `json:"nextAccessTime"`
		} `json:"data"`
	}
	values := make([]json.RawMessage, 0, 4)
	if raw := header.Get("nextAccessTime"); raw != "" {
		encoded, _ := json.Marshal(raw)
		values = append(values, encoded)
	}
	if json.Unmarshal(body, &remote) == nil {
		values = append(values, remote.Next)
		if remote.Error != nil {
			values = append(values, remote.Error.Next)
		}
		if remote.Data != nil {
			values = append(values, remote.Data.Next)
		}
	}
	for _, raw := range values {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			text = string(raw)
		}
		date, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			date, err = time.Parse("2006-Jan-02 15:04:05-0700 UTC", text)
		}
		if err != nil {
			if epoch, parseErr := strconv.ParseInt(text, 10, 64); parseErr == nil && epoch > 0 {
				if epoch >= 1e12 {
					date = time.UnixMilli(epoch)
				} else {
					date = time.Unix(epoch, 0)
				}
				err = nil
			}
		}
		if err == nil {
			delay, valid = max(delay, max(0, date.Sub(reference))), true
		}
	}
	if !valid {
		return time.Minute
	}
	// Clamp before adding slack to avoid overflowing adversarial timestamps.
	return min(delay, time.Duration(math.MaxInt64)-250*time.Millisecond) + 250*time.Millisecond
}
