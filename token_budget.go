package gateway

import "math"

// TokenBudget describes a local preflight budget, not provider-reported Usage.
// It contains counts only, never prompts, credentials or model output.
type TokenBudget struct {
	ContextWindow int
	// InputTokens is nil when output plus fixed margin already exceeds the window
	// and the counter was skipped. A non-nil zero is a counted zero.
	InputTokens *int
	// InputEstimated identifies the default heuristic. False means a custom
	// counter was configured; it does not guarantee that counter is exact.
	InputEstimated       bool
	ReservedOutputTokens int
	SafetyMarginTokens   int
	SafetyMarginPercent  int
	// PercentageMarginTokens is ceil(input * percent / 100). Nil means the input
	// was not counted or the margin exceeded the int range.
	PercentageMarginTokens *int
	// TotalTokens includes input, reserved output and both margins. Nil means
	// input was not counted or a component/sum exceeded the int range.
	TotalTokens *int
	// Overflow distinguishes integer overflow from a skipped input count.
	Overflow bool
}

func tokenBudget(policy *TokenPolicy, output int, input *int) *TokenBudget {
	budget := &TokenBudget{
		ContextWindow: policy.ContextWindow, InputTokens: input,
		InputEstimated:       policy.CountInputTokens == nil,
		ReservedOutputTokens: output, SafetyMarginTokens: policy.SafetyMarginTokens,
		SafetyMarginPercent: policy.SafetyMarginPercent,
	}
	if input != nil {
		margin, fits := percentageTokenMargin(*input, policy.SafetyMarginPercent)
		if !fits {
			budget.Overflow = true
			return budget
		}
		budget.PercentageMarginTokens = &margin
	}
	total := output
	if policy.SafetyMarginTokens > math.MaxInt-total {
		budget.Overflow = true
		return budget
	}
	total += policy.SafetyMarginTokens
	if input == nil {
		return budget
	}
	for _, tokens := range []int{*input, *budget.PercentageMarginTokens} {
		if tokens > math.MaxInt-total {
			budget.Overflow = true
			return budget
		}
		total += tokens
	}
	budget.TotalTokens = &total
	return budget
}
