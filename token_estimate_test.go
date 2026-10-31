package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
)

func estimatedInput() gateway.ChatRequest {
	return gateway.ChatRequest{
		Model: "chat", System: "abcd",
		Messages: []gateway.Message{
			{Role: gateway.RoleUser, Content: "Olá"},
			{Role: gateway.RoleAssistant, ToolCalls: []gateway.ToolCall{{ID: "one", Name: "lookup", Arguments: json.RawMessage(`{}`)}}},
			{Role: gateway.RoleTool, ToolCallID: "one", Content: "done"},
		},
		Tools: []gateway.Tool{{Name: "lookup", Description: "buscar", Parameters: json.RawMessage(`{}`)}},
	}
}

func TestEstimateInputTokensTextAndStructure(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    gateway.ChatRequest
		want int
	}{
		{"empty", gateway.ChatRequest{}, 3},
		{"system", gateway.ChatRequest{System: "abcd"}, 9},
		{"ASCII", gateway.ChatRequest{Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "hello"}}}, 11},
		{"Portuguese", input(), 11},
		{"CJK", gateway.ChatRequest{Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "こんにちは"}}}, 14},
		{"emoji", gateway.ChatRequest{Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "😀"}}}, 11},
		{"history", gateway.ChatRequest{Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "Olá"}, {Role: gateway.RoleUser, Content: "Olá"}}}, 19},
		{"tool schema", gateway.ChatRequest{Tools: []gateway.Tool{{Name: "lookup", Description: "buscar", Parameters: json.RawMessage(`{}`)}}}, 16},
		{"full tool roundtrip", estimatedInput(), 58},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gateway.EstimateInputTokens(context.Background(), tc.r)
			if err != nil || got != tc.want {
				t.Fatalf("estimated=%d want=%d err=%v", got, tc.want, err)
			}
		})
	}
	for _, tc := range []struct{ bytes, want int }{{0, 9}, {1, 10}, {2, 10}, {3, 10}, {4, 11}, {6, 11}, {7, 12}} {
		r := gateway.ChatRequest{Messages: []gateway.Message{{Role: gateway.RoleUser, Content: strings.Repeat("x", tc.bytes)}}}
		if got, err := gateway.EstimateInputTokens(context.Background(), r); err != nil || got != tc.want {
			t.Fatalf("bytes=%d estimated=%d want=%d err=%v", tc.bytes, got, tc.want, err)
		}
	}
}

func TestEstimateInputTokensPreservesRequestAndIgnoresGenerationParameters(t *testing.T) {
	r := estimatedInput()
	r.Model = strings.Repeat("model", 100)
	temperature, topP, output := 0.5, 0.9, 100000
	r.Temperature, r.TopP, r.MaxOutputTokens = &temperature, &topP, &output
	before, _ := json.Marshal(r)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := gateway.EstimateInputTokens(context.Background(), r); err != nil || got != 58 {
				t.Errorf("estimated=%d err=%v", got, err)
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("estimator mutated request")
	}
}

func TestEstimateInputTokensContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	for _, tc := range []struct {
		ctx   context.Context
		code  gateway.ErrorCode
		cause error
	}{
		{nil, gateway.InvalidRequest, nil},
		{cancelled, gateway.Cancelled, context.Canceled},
		{expired, gateway.Timeout, context.DeadlineExceeded},
	} {
		n, err := gateway.EstimateInputTokens(tc.ctx, estimatedInput())
		if n != 0 || !gateway.IsCode(err, tc.code) || tc.cause != nil && !errors.Is(err, tc.cause) {
			t.Fatalf("estimated=%d err=%v", n, err)
		}
	}
}

func TestTokenPolicyDefaultEstimate(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct{ window, margin, percent int }{
			{20, 0, 0}, {21, 0, 0}, {22, 0, 0}, {25, 5, 0}, {26, 5, 0}, {27, 5, 0},
			{22, 0, 10}, {23, 0, 10}, {24, 0, 10}, {27, 5, 10}, {28, 5, 10}, {29, 5, 10},
		} {
			window := tc.window
			calls, events := 0, 0
			policy := &gateway.TokenPolicy{ContextWindow: window, SafetyMarginTokens: tc.margin, SafetyMarginPercent: tc.percent, DefaultMaxOutputTokens: 10}
			g, err := gateway.New(budgetConfig(budgetProvider{accept: func(r gateway.ChatRequest) {
				calls++
				if r.MaxOutputTokens == nil || *r.MaxOutputTokens != 10 || r.Messages[0].Content != "Olá" {
					t.Errorf("request changed: %+v", r)
				}
			}}, policy))
			if err != nil {
				t.Fatal(err)
			}
			var result gateway.ChatResult
			if stream {
				result, err = g.Stream(context.Background(), input(), func(gateway.Event) error { events++; return nil })
			} else {
				result, err = g.Chat(context.Background(), input())
			}
			percentageMargin := 0
			if tc.percent != 0 {
				percentageMargin = 2 // ceil(11 input tokens * 10 / 100)
			}
			if window-tc.margin-percentageMargin == 20 {
				if !gateway.IsCode(err, gateway.ContextExceeded) || !strings.Contains(err.Error(), "estimated input tokens 11") || !strings.Contains(err.Error(), "safety margin") || calls != 0 || events != 0 {
					t.Fatalf("stream=%t calls=%d events=%d err=%v", stream, calls, events, err)
				}
			} else if err != nil || calls != 1 || !result.ProtocolComplete {
				t.Fatalf("window=%d stream=%t calls=%d result=%+v err=%v", window, stream, calls, result, err)
			}
			if result.Usage.Input.Tokens != nil || result.Usage.Input.Source != gateway.UsageUnknown {
				t.Fatal("input estimate must not become reported usage")
			}
		}
	}
}

func TestTokenPolicyCustomCounterOverridesEstimate(t *testing.T) {
	counted, dispatched := false, false
	policy := &gateway.TokenPolicy{ContextWindow: 20, DefaultMaxOutputTokens: 10, CountInputTokens: func(context.Context, gateway.ChatRequest) (int, error) {
		counted = true
		return 1, nil
	}}
	g, err := gateway.New(budgetConfig(budgetProvider{accept: func(gateway.ChatRequest) { dispatched = true }}, policy))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.Chat(context.Background(), input()); err != nil || !counted || !dispatched {
		t.Fatalf("counter=%t dispatched=%t err=%v", counted, dispatched, err)
	}
}
