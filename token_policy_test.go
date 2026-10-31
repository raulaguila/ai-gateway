package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gateway "github.com/raulaguila/ai-gateway"
)

type budgetProvider struct {
	accept func(gateway.ChatRequest)
}

func (p budgetProvider) Capabilities(string) gateway.Capabilities {
	return gateway.Capabilities{Chat: gateway.Supported, Streaming: gateway.Supported, Tools: gateway.Supported}
}

func (p budgetProvider) Chat(_ context.Context, r gateway.ChatRequest) (gateway.ChatResult, error) {
	p.accept(r)
	return gateway.ChatResult{Text: "ok", ProtocolComplete: true}, nil
}

func (p budgetProvider) Stream(ctx context.Context, r gateway.ChatRequest, emit func(gateway.Event) error) (gateway.ChatResult, error) {
	result, err := p.Chat(ctx, r)
	if err == nil {
		err = emit(gateway.Event{Type: gateway.EventCompleted, Result: &result})
	}
	return result, err
}

func budgetConfig(p gateway.Provider, policy *gateway.TokenPolicy) gateway.Config {
	cfg := config(p)
	route := cfg.Models["chat"]
	route.TokenPolicy = policy
	cfg.Models["chat"] = route
	return cfg
}

func TestTokenPolicyBoundaries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name           string
			window, tokens int
			output         int
			margin         int
			percent        int
			disabled       bool
			want           gateway.ErrorCode
		}{
			{name: "disabled", tokens: math.MaxInt, output: 30, disabled: true},
			{name: "below", window: 100, tokens: 60, output: 30},
			{name: "exact", window: 100, tokens: 70, output: 30},
			{name: "zero input", window: 100, output: 100},
			{name: "one over", window: 100, tokens: 71, output: 30, want: gateway.ContextExceeded},
			{name: "input alone over", window: 100, tokens: 101, output: 1, want: gateway.ContextExceeded},
			{name: "output alone over", window: 100, output: 101, want: gateway.ContextExceeded},
			{name: "addition overflow", window: math.MaxInt, tokens: math.MaxInt, output: 1, want: gateway.ContextExceeded},
			{name: "margin below", window: 100, tokens: 59, output: 30, margin: 10},
			{name: "margin exact", window: 100, tokens: 60, output: 30, margin: 10},
			{name: "margin one over", window: 100, tokens: 61, output: 30, margin: 10, want: gateway.ContextExceeded},
			{name: "output plus margin over", window: 100, output: 91, margin: 10, want: gateway.ContextExceeded},
			{name: "margin overflow", window: math.MaxInt, tokens: math.MaxInt, output: 1, margin: 1, want: gateway.ContextExceeded},
			{name: "output margin overflow", window: math.MaxInt, output: math.MaxInt, margin: 1, want: gateway.ContextExceeded},
			{name: "largest valid margin", window: math.MaxInt, output: 1, margin: math.MaxInt - 1},
			{name: "percent exact", window: 100, tokens: 60, output: 34, percent: 10},
			{name: "percent one over", window: 99, tokens: 60, output: 34, percent: 10, want: gateway.ContextExceeded},
			{name: "percent rounds up exact", window: 100, tokens: 61, output: 32, percent: 10},
			{name: "percent rounds up over", window: 99, tokens: 61, output: 32, percent: 10, want: gateway.ContextExceeded},
			{name: "small percentage reserves one", window: 10, tokens: 1, output: 9, percent: 1, want: gateway.ContextExceeded},
			{name: "zero input percentage", window: 100, output: 100, percent: math.MaxInt},
			{name: "combined margins exact", window: 100, tokens: 60, output: 24, margin: 10, percent: 10},
			{name: "combined margins over", window: 99, tokens: 60, output: 24, margin: 10, percent: 10, want: gateway.ContextExceeded},
			{name: "percentage above 100", window: 100, tokens: 10, output: 70, percent: 200},
			{name: "large exact percentage", window: math.MaxInt, tokens: math.MaxInt / 2, output: 1, percent: 100},
			{name: "large percentage one over", window: math.MaxInt, tokens: math.MaxInt/2 + 1, output: 1, percent: 100, want: gateway.ContextExceeded},
			{name: "large percentage small input", window: math.MaxInt, tokens: 1, output: 1, percent: math.MaxInt},
			{name: "percentage overflow", window: math.MaxInt, tokens: math.MaxInt, output: 1, percent: math.MaxInt, want: gateway.ContextExceeded},
			{name: "margin quotient overflow", window: math.MaxInt, tokens: 101, output: 1, percent: math.MaxInt, want: gateway.ContextExceeded},
			{name: "max margin quotient", window: math.MaxInt, tokens: 100, output: 1, percent: math.MaxInt, want: gateway.ContextExceeded},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				calls, events, counts := 0, 0, 0
				policy := &gateway.TokenPolicy{ContextWindow: tc.window, SafetyMarginTokens: tc.margin, SafetyMarginPercent: tc.percent, CountInputTokens: func(context.Context, gateway.ChatRequest) (int, error) {
					counts++
					return tc.tokens, nil
				}}
				if tc.disabled {
					policy = nil
				}
				g, err := gateway.New(budgetConfig(budgetProvider{accept: func(gateway.ChatRequest) { calls++ }}, policy))
				if err != nil {
					t.Fatal(err)
				}
				r := input()
				r.MaxOutputTokens = &tc.output
				var result gateway.ChatResult
				op := "chat"
				if stream {
					op = "stream"
					result, err = g.Stream(context.Background(), r, func(gateway.Event) error { events++; return nil })
				} else {
					result, err = g.Chat(context.Background(), r)
				}
				if tc.want != "" {
					var detail *gateway.Error
					if !errors.As(err, &detail) || detail.Code != tc.want || detail.Provider != "custom" || detail.Operation != op || detail.Retryable || calls != 0 || events != 0 || result.ProtocolComplete {
						t.Fatalf("result=%+v calls=%d events=%d error=%+v", result, calls, events, err)
					}
				} else if err != nil || calls != 1 || !result.ProtocolComplete || stream && events != 1 {
					t.Fatalf("result=%+v calls=%d events=%d error=%v", result, calls, events, err)
				}
				if tc.disabled && counts != 0 {
					t.Fatal("disabled policy invoked counter")
				}
			})
		}
	}
}

func TestTokenPolicyPreservesFullInputAndResolvesOutputBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/explicit=%t", stream, explicit), func(t *testing.T) {
				r := input()
				r.System = "instruções de sistema"
				r.Messages = append(r.Messages,
					gateway.Message{Role: gateway.RoleAssistant, ToolCalls: []gateway.ToolCall{{ID: "call1", Name: "lookup", Arguments: json.RawMessage(`{"city":"Manaus"}`)}}},
					gateway.Message{Role: gateway.RoleTool, ToolCallID: "call1", Content: "resultado extenso"},
				)
				r.Tools = []gateway.Tool{{Name: "lookup", Description: "buscar", Parameters: json.RawMessage(`{"type":"object"}`)}}
				zero := 0.0
				r.Temperature, r.TopP = &zero, &zero
				output := 20
				if explicit {
					output = 30
					r.MaxOutputTokens = &output
				}
				before, _ := json.Marshal(r)
				want := r
				want.Model, want.MaxOutputTokens = "remote", &output
				check := func(got gateway.ChatRequest) {
					if !reflect.DeepEqual(got, want) {
						t.Errorf("input was changed: got=%+v want=%+v", got, want)
					}
				}
				counted := false
				policy := &gateway.TokenPolicy{ContextWindow: 117, SafetyMarginTokens: 10, SafetyMarginPercent: 10, DefaultMaxOutputTokens: 20, CountInputTokens: func(_ context.Context, got gateway.ChatRequest) (int, error) {
					check(got)
					counted = true
					return 70, nil
				}}
				g, err := gateway.New(budgetConfig(budgetProvider{accept: check}, policy))
				if err != nil {
					t.Fatal(err)
				}
				if stream {
					_, err = g.Stream(context.Background(), r, func(gateway.Event) error { return nil })
				} else {
					_, err = g.Chat(context.Background(), r)
				}
				after, _ := json.Marshal(r)
				if err != nil || !counted || string(before) != string(after) {
					t.Fatalf("counter=%t request changed=%t error=%v", counted, string(before) != string(after), err)
				}
			})
		}
	}
}

func TestTokenPolicyInvalidConfiguration(t *testing.T) {
	counter := func(context.Context, gateway.ChatRequest) (int, error) { return 1, nil }
	for _, policy := range []gateway.TokenPolicy{
		{}, {ContextWindow: -1, CountInputTokens: counter},
		{ContextWindow: 0, CountInputTokens: counter},
		{ContextWindow: 100, DefaultMaxOutputTokens: -1, CountInputTokens: counter},
		{ContextWindow: 100, DefaultMaxOutputTokens: 101, CountInputTokens: counter},
		{ContextWindow: 100, SafetyMarginTokens: -1},
		{ContextWindow: 100, SafetyMarginTokens: 100},
		{ContextWindow: 100, SafetyMarginTokens: math.MaxInt},
		{ContextWindow: 100, SafetyMarginTokens: 10, DefaultMaxOutputTokens: 91},
		{ContextWindow: math.MaxInt, SafetyMarginTokens: 1, DefaultMaxOutputTokens: math.MaxInt},
		{ContextWindow: 100, SafetyMarginPercent: -1},
	} {
		if _, err := gateway.New(budgetConfig(budgetProvider{}, &policy)); !gateway.IsCode(err, gateway.InvalidConfiguration) {
			t.Fatalf("accepted policy %+v: %v", policy, err)
		}
	}
}

func TestTokenPolicyCountingFailures(t *testing.T) {
	secret := errors.New("private prompt must not appear in error text")
	for _, tc := range []struct {
		name   string
		tokens int
		err    error
		want   gateway.ErrorCode
	}{
		{"negative count", -1, nil, gateway.InvalidRequest},
		{"counter error", 0, secret, gateway.InvalidRequest},
		{"cancelled", 0, fmt.Errorf("count: %w", context.Canceled), gateway.Cancelled},
		{"timeout", 0, fmt.Errorf("count: %w", context.DeadlineExceeded), gateway.Timeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &gateway.TokenPolicy{ContextWindow: 100, DefaultMaxOutputTokens: 10, CountInputTokens: func(context.Context, gateway.ChatRequest) (int, error) { return tc.tokens, tc.err }}
			g, err := gateway.New(budgetConfig(budgetProvider{accept: func(gateway.ChatRequest) { t.Fatal("dispatched after counter failure") }}, policy))
			if err != nil {
				t.Fatal(err)
			}
			_, err = g.Chat(context.Background(), input())
			if !gateway.IsCode(err, tc.want) || tc.err != nil && !errors.Is(err, tc.err) || strings.Contains(err.Error(), secret.Error()) {
				t.Fatal(err)
			}
			var detail *gateway.Error
			if !errors.As(err, &detail) || detail.TokenBudget != nil {
				t.Fatal("counter failure must not expose a fabricated token budget")
			}
		})
	}
}

func TestTokenPolicyRequiresOutputAndHonorsCancellation(t *testing.T) {
	counts := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	policy := &gateway.TokenPolicy{ContextWindow: 100, CountInputTokens: func(got context.Context, _ gateway.ChatRequest) (int, error) {
		counts++
		if got != ctx {
			t.Fatal("counter did not receive operation context")
		}
		cancel()
		return 1, nil
	}}
	g, err := gateway.New(budgetConfig(budgetProvider{accept: func(gateway.ChatRequest) { t.Fatal("unexpected dispatch") }}, policy))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.Chat(ctx, input()); !gateway.IsCode(err, gateway.InvalidRequest) || counts != 0 {
		t.Fatalf("missing output: count=%d err=%v", counts, err)
	}
	r := input()
	output := 10
	r.MaxOutputTokens = &output
	if _, err = g.Chat(ctx, r); !gateway.IsCode(err, gateway.Cancelled) || !errors.Is(err, context.Canceled) || counts != 1 {
		t.Fatalf("cancel during count: count=%d err=%v", counts, err)
	}
	if _, err = g.Chat(ctx, r); !gateway.IsCode(err, gateway.Cancelled) || counts != 1 {
		t.Fatalf("cancel before count: count=%d err=%v", counts, err)
	}
}

func TestTokenPolicySnapshotAndConcurrentCalls(t *testing.T) {
	var calls atomic.Int64
	policy := &gateway.TokenPolicy{ContextWindow: 100, DefaultMaxOutputTokens: 10, SafetyMarginTokens: 2, SafetyMarginPercent: 10, CountInputTokens: func(context.Context, gateway.ChatRequest) (int, error) { return 80, nil }}
	cfg := budgetConfig(budgetProvider{accept: func(r gateway.ChatRequest) {
		if r.MaxOutputTokens == nil || *r.MaxOutputTokens != 10 {
			t.Errorf("unexpected budget: %+v", r)
		}
		calls.Add(1)
	}}, policy)
	// A second alias on the same deployment can use an independent policy.
	cfg.Models["small"] = gateway.ModelRoute{Deployment: "custom", Model: "remote", TokenPolicy: &gateway.TokenPolicy{ContextWindow: 50, DefaultMaxOutputTokens: 10, CountInputTokens: policy.CountInputTokens}}
	g, err := gateway.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	*policy = gateway.TokenPolicy{}
	delete(cfg.Models, "chat")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Chat(context.Background(), input()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	r := input()
	r.Model = "small"
	if _, err := g.Chat(context.Background(), r); !gateway.IsCode(err, gateway.ContextExceeded) || calls.Load() != 20 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}
