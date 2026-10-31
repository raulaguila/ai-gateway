package gateway

import "errors"

type ErrorCode string

const (
	InvalidConfiguration  ErrorCode = "invalid_configuration"
	InvalidRequest        ErrorCode = "invalid_request"
	AuthenticationFailed  ErrorCode = "authentication_failed"
	ModelNotFound         ErrorCode = "model_not_found"
	UnsupportedCapability ErrorCode = "unsupported_capability"
	ContextExceeded       ErrorCode = "context_exceeded"
	RateLimited           ErrorCode = "rate_limited"
	CapacityUnavailable   ErrorCode = "capacity_unavailable"
	Timeout               ErrorCode = "timeout"
	Cancelled             ErrorCode = "cancelled"
	UpstreamError         ErrorCode = "upstream_error"
	InvalidResponse       ErrorCode = "invalid_response"
	StreamIncomplete      ErrorCode = "stream_incomplete"
	CallbackFailed        ErrorCode = "callback_failed"
)

// Error prints only its sanitized message. Cause remains available to errors.Is/As.
type Error struct {
	Code           ErrorCode
	Provider       string
	Operation      string
	UpstreamStatus int
	Retryable      bool
	RequestID      string
	Message        string
	Cause          error
	// ModelAlias and Model identify local routing without exposing configuration.
	ModelAlias string
	Model      string
	// Field identifies a configuration field when there is an actionable correction.
	Field string
	// TokenBudget is populated by local token-policy context rejections.
	// It is nil for upstream errors and operations without a calculated budget.
	TokenBudget *TokenBudget
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func IsCode(err error, code ErrorCode) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

func annotate(err error, deployment, operation string) error {
	if err == nil {
		return nil
	}
	var target *Error
	if errors.As(err, &target) {
		copy := *target
		copy.Provider, copy.Operation = deployment, operation
		return &copy
	}
	return &Error{Code: UpstreamError, Provider: deployment, Operation: operation, Message: "provider operation failed", Cause: err}
}
