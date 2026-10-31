package samsung_test

import (
	"context"
	"errors"
	"testing"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/providers/samsung"
)

func TestLocalComponentDiagnostics(t *testing.T) {
	p := provider(t, samsung.Config{BaseURL: "http://127.0.0.1:1"})
	policy := gateway.TokenPolicy{ContextWindow: 100, DefaultMaxOutputTokens: 10, CountInputTokens: func(context.Context, gateway.ChatRequest) (int, error) {
		t.Fatal("readiness check counted tokens")
		return 0, nil
	}}
	_, err := gateway.NewSingle(p, "flow", policy)
	var detail *gateway.Error
	if !errors.As(err, &detail) || detail.Field != "LLMComponentID" || detail.ModelAlias != "flow" || detail.Model != "flow" {
		t.Fatalf("component prerequisite not diagnosed: %+v", err)
	}
	p = provider(t, samsung.Config{BaseURL: "http://127.0.0.1:1", LLMComponentID: "llm"})
	if _, err := gateway.NewSingle(p, "flow", policy); err != nil {
		t.Fatal(err)
	}
}
