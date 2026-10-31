package gateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// TokenCounter counts the complete model input: system, messages, tool schemas,
// tool calls/results and protocol overhead. Model is the resolved remote ID and
// MaxOutputTokens is the effective output budget. Implementations must not mutate
// the request, must honor ctx and must be safe for concurrent calls. An estimate
// may be used, but its accuracy remains the consumer's responsibility.
type TokenCounter func(ctx context.Context, request ChatRequest) (int, error)

// TokenPolicy validates input tokens plus reserved output and safety margin
// against ContextWindow.
// It never truncates or summarizes input and does not discover model limits.
type TokenPolicy struct {
	ContextWindow int
	// SafetyMarginTokens reserves additional context without changing the request
	// or Usage. Defaults to zero and applies to estimated and custom counters.
	// Must be nonnegative and smaller than ContextWindow.
	SafetyMarginTokens int
	// SafetyMarginPercent reserves ceil(input tokens * percent / 100) in addition
	// to SafetyMarginTokens. Uses whole percentages (10 means 10%), must be
	// nonnegative and defaults to zero. Applies to estimated and custom counters.
	SafetyMarginPercent int
	// DefaultMaxOutputTokens is sent to the provider when the request omits it.
	// Zero requires an explicit MaxOutputTokens on every request.
	DefaultMaxOutputTokens int
	// CountInputTokens defaults to EstimateInputTokens when nil. The default is
	// a heuristic, not an exact token count or a guaranteed upper bound.
	CountInputTokens TokenCounter
}

func (p TokenPolicy) validate() error {
	if p.ContextWindow <= 0 || p.SafetyMarginTokens < 0 || p.SafetyMarginTokens >= p.ContextWindow || p.DefaultMaxOutputTokens < 0 || p.DefaultMaxOutputTokens > p.ContextWindow-p.SafetyMarginTokens {
		return &Error{Code: InvalidConfiguration, Message: "token policy requires a positive context window, a nonnegative safety margin smaller than the window and a default output budget that fits the remaining context"}
	}
	if p.SafetyMarginPercent < 0 {
		return &Error{Code: InvalidConfiguration, Message: "token safety margin percentage must be nonnegative"}
	}
	return nil
}

func applyTokenPolicy(ctx context.Context, request ChatRequest, policy *TokenPolicy) (ChatRequest, *TokenBudget, error) {
	if policy == nil {
		return request, nil, nil
	}
	output := policy.DefaultMaxOutputTokens
	if request.MaxOutputTokens != nil {
		output = *request.MaxOutputTokens
	}
	if output == 0 {
		return request, nil, &Error{Code: InvalidRequest, Field: "MaxOutputTokens", Message: "max output tokens is required by the token policy"}
	}
	// Copy the value so policy defaults never change the caller's request.
	request.MaxOutputTokens = &output
	if output > policy.ContextWindow-policy.SafetyMarginTokens {
		budget := tokenBudget(policy, output, nil)
		return request, budget, &Error{Code: ContextExceeded, TokenBudget: budget, Message: fmt.Sprintf("output budget %d plus safety margin %d exceed context window %d", output, policy.SafetyMarginTokens, policy.ContextWindow)}
	}
	counter := policy.CountInputTokens
	if counter == nil {
		counter = EstimateInputTokens
	}
	input, err := counter(ctx, request)
	if contextErr := checkContext(ctx); contextErr != nil {
		return request, nil, contextErr
	}
	if err != nil {
		code := InvalidRequest
		if errors.Is(err, context.Canceled) {
			code = Cancelled
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = Timeout
		}
		return request, nil, &Error{Code: code, Message: "input token counting failed", Cause: err}
	}
	if input < 0 {
		return request, nil, &Error{Code: InvalidRequest, Message: "input token count must be nonnegative"}
	}
	budget := tokenBudget(policy, output, &input)
	if budget.Overflow || *budget.TotalTokens > policy.ContextWindow {
		label := "input tokens"
		if policy.CountInputTokens == nil {
			label = "estimated input tokens"
		}
		return request, budget, &Error{Code: ContextExceeded, TokenBudget: budget, Message: fmt.Sprintf("%s %d plus output budget %d plus safety margin %d and %d%% of input exceed context window %d", label, input, output, policy.SafetyMarginTokens, policy.SafetyMarginPercent, policy.ContextWindow)}
	}
	return request, budget, nil
}

// Counts and percentages are nonnegative; ceil(input*percent/100) must fit int.
func percentageTokenMargin(input, percent int) (int, bool) {
	hi, lo := bits.Mul64(uint64(input), uint64(percent))
	if hi >= 100 {
		return 0, false
	}
	quotient, remainder := bits.Div64(hi, lo, 100)
	if quotient > uint64(math.MaxInt) || quotient == uint64(math.MaxInt) && remainder != 0 {
		return 0, false
	}
	if remainder != 0 {
		quotient++
	}
	return int(quotient), true
}
