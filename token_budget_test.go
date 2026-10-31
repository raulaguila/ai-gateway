package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	gateway "github.com/raulaguila/ai-gateway"
)

func budgetInt(n int) *int { return &n }

func TestContextErrorTokenBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   gateway.TokenPolicy
		explicit *int
		count    *int
		want     gateway.TokenBudget
	}{
		{
			name:   "default estimate and combined margins",
			policy: gateway.TokenPolicy{ContextWindow: 27, DefaultMaxOutputTokens: 10, SafetyMarginTokens: 5, SafetyMarginPercent: 10},
			want:   gateway.TokenBudget{ContextWindow: 27, InputTokens: budgetInt(11), InputEstimated: true, ReservedOutputTokens: 10, SafetyMarginTokens: 5, SafetyMarginPercent: 10, PercentageMarginTokens: budgetInt(2), TotalTokens: budgetInt(28)},
		},
		{
			name:     "custom counter and explicit output",
			policy:   gateway.TokenPolicy{ContextWindow: 99, DefaultMaxOutputTokens: 10, SafetyMarginPercent: 10},
			explicit: budgetInt(32), count: budgetInt(61),
			want: gateway.TokenBudget{ContextWindow: 99, InputTokens: budgetInt(61), ReservedOutputTokens: 32, SafetyMarginPercent: 10, PercentageMarginTokens: budgetInt(7), TotalTokens: budgetInt(100)},
		},
		{
			name:   "zero margins are known zero",
			policy: gateway.TokenPolicy{ContextWindow: 20, DefaultMaxOutputTokens: 10},
			want:   gateway.TokenBudget{ContextWindow: 20, InputTokens: budgetInt(11), InputEstimated: true, ReservedOutputTokens: 10, PercentageMarginTokens: budgetInt(0), TotalTokens: budgetInt(21)},
		},
		{
			name:     "early rejection skips default input count",
			policy:   gateway.TokenPolicy{ContextWindow: 100, DefaultMaxOutputTokens: 10, SafetyMarginTokens: 10, SafetyMarginPercent: 10},
			explicit: budgetInt(91),
			want:     gateway.TokenBudget{ContextWindow: 100, InputEstimated: true, ReservedOutputTokens: 91, SafetyMarginTokens: 10, SafetyMarginPercent: 10},
		},
		{
			name:     "early rejection skips custom input count",
			policy:   gateway.TokenPolicy{ContextWindow: 100, DefaultMaxOutputTokens: 10, SafetyMarginTokens: 10},
			explicit: budgetInt(91), count: budgetInt(50),
			want: gateway.TokenBudget{ContextWindow: 100, ReservedOutputTokens: 91, SafetyMarginTokens: 10},
		},
		{
			name:     "early output plus margin overflow",
			policy:   gateway.TokenPolicy{ContextWindow: math.MaxInt, SafetyMarginTokens: 1},
			explicit: budgetInt(math.MaxInt),
			want:     gateway.TokenBudget{ContextWindow: math.MaxInt, InputEstimated: true, ReservedOutputTokens: math.MaxInt, SafetyMarginTokens: 1, Overflow: true},
		},
		{
			name:   "known components but total overflow",
			policy: gateway.TokenPolicy{ContextWindow: math.MaxInt, DefaultMaxOutputTokens: 1},
			count:  budgetInt(math.MaxInt),
			want:   gateway.TokenBudget{ContextWindow: math.MaxInt, InputTokens: budgetInt(math.MaxInt), ReservedOutputTokens: 1, PercentageMarginTokens: budgetInt(0), Overflow: true},
		},
		{
			name:   "percentage margin overflow",
			policy: gateway.TokenPolicy{ContextWindow: math.MaxInt, DefaultMaxOutputTokens: 1, SafetyMarginPercent: math.MaxInt},
			count:  budgetInt(math.MaxInt),
			want:   gateway.TokenBudget{ContextWindow: math.MaxInt, InputTokens: budgetInt(math.MaxInt), ReservedOutputTokens: 1, SafetyMarginPercent: math.MaxInt, Overflow: true},
		},
		{
			name:   "percentage known but total overflow",
			policy: gateway.TokenPolicy{ContextWindow: math.MaxInt, DefaultMaxOutputTokens: 1, SafetyMarginPercent: 100},
			count:  budgetInt(math.MaxInt/2 + 1),
			want:   gateway.TokenBudget{ContextWindow: math.MaxInt, InputTokens: budgetInt(math.MaxInt/2 + 1), ReservedOutputTokens: 1, SafetyMarginPercent: 100, PercentageMarginTokens: budgetInt(math.MaxInt/2 + 1), Overflow: true},
		},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				policy := tc.policy
				counts := 0
				if tc.count != nil {
					policy.CountInputTokens = func(context.Context, gateway.ChatRequest) (int, error) {
						counts++
						return *tc.count, nil
					}
				}
				g, err := gateway.New(budgetConfig(budgetProvider{accept: func(gateway.ChatRequest) { t.Fatal("unexpected dispatch") }}, &policy))
				if err != nil {
					t.Fatal(err)
				}
				r := input()
				r.MaxOutputTokens = tc.explicit
				op := "chat"
				if stream {
					op = "stream"
					_, err = g.Stream(context.Background(), r, func(gateway.Event) error { t.Fatal("unexpected event"); return nil })
				} else {
					_, err = g.Chat(context.Background(), r)
				}
				var detail *gateway.Error
				if !errors.As(fmt.Errorf("application: %w", err), &detail) || detail.Code != gateway.ContextExceeded || detail.Provider != "custom" || detail.Operation != op || detail.TokenBudget == nil {
					t.Fatalf("missing context error details: %+v", err)
				}
				if !reflect.DeepEqual(*detail.TokenBudget, tc.want) {
					t.Fatalf("budget=%+v want=%+v", detail.TokenBudget, tc.want)
				}
				if tc.count != nil && tc.want.InputTokens == nil && counts != 0 {
					t.Fatal("early rejection unnecessarily invoked counter")
				}
			})
		}
	}
}

func TestTokenBudgetIndependentErrorsAndNoPromptContent(t *testing.T) {
	policy := &gateway.TokenPolicy{ContextWindow: 10, DefaultMaxOutputTokens: 5, SafetyMarginTokens: 1, SafetyMarginPercent: 10, CountInputTokens: func(context.Context, gateway.ChatRequest) (int, error) { return 10, nil }}
	g, err := gateway.New(budgetConfig(budgetProvider{}, policy))
	if err != nil {
		t.Fatal(err)
	}
	r := input()
	r.System = "private system prompt"
	r.Messages[0].Content = "private conversation"
	_, err = g.Chat(context.Background(), r)
	var first *gateway.Error
	if !errors.As(err, &first) || first.TokenBudget == nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), r.System) || strings.Contains(string(encoded), r.Messages[0].Content) {
		t.Fatalf("error exposed prompt: %s err=%v", encoded, err)
	}
	first.TokenBudget.ContextWindow = -1
	*first.TokenBudget.InputTokens = -1
	*first.TokenBudget.PercentageMarginTokens = -1
	*first.TokenBudget.TotalTokens = -1
	_, err = g.Chat(context.Background(), r)
	var second *gateway.Error
	if !errors.As(err, &second) || second.TokenBudget == nil {
		t.Fatal(err)
	}
	want := gateway.TokenBudget{ContextWindow: 10, InputTokens: budgetInt(10), ReservedOutputTokens: 5, SafetyMarginTokens: 1, SafetyMarginPercent: 10, PercentageMarginTokens: budgetInt(1), TotalTokens: budgetInt(17)}
	if !reflect.DeepEqual(*second.TokenBudget, want) {
		t.Fatalf("prior error mutation changed budget: %+v", second.TokenBudget)
	}
}
