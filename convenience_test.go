package gateway_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	gateway "github.com/raulaguila/ai-gateway"
)

func TestSingleGatewayAndHelpers(t *testing.T) {
	zero := gateway.Ptr(0.0)
	if *zero != 0 || gateway.User("hello").Role != gateway.RoleUser {
		t.Fatal("helpers changed explicit values")
	}
	caps := gateway.TextChatCapabilities()
	if caps.Chat != gateway.Supported || caps.Streaming != gateway.Supported || caps.Tools != gateway.Unknown || caps.Embeddings != gateway.Unknown || caps.Catalog != gateway.Unknown || gateway.ChatWithToolsCapabilities().Tools != gateway.Supported {
		t.Fatal("profiles must not promote unrelated capabilities")
	}
	// Declare only chat: a profile for chat+stream would contradict this provider.
	p := &externalChat{caps: gateway.Capabilities{Chat: gateway.Supported}}
	g, err := gateway.NewSingle(p, "remote")
	if err != nil {
		t.Fatal(err)
	}
	r := gateway.ChatRequest{Messages: []gateway.Message{gateway.User("hello")}}
	result, err := g.Chat(context.Background(), r)
	if err != nil || result.Text != "remote" || r.Model != "" {
		t.Fatalf("result=%+v request=%+v err=%v", result, r, err)
	}
	r.Model = "remote"
	if _, err := g.Chat(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Model = "different"
	if _, err := g.Chat(context.Background(), r); !gateway.IsCode(err, gateway.ModelNotFound) {
		t.Fatal(err)
	}
	if _, err := gateway.NewSingle(p, "remote", gateway.TokenPolicy{}, gateway.TokenPolicy{}); !gateway.IsCode(err, gateway.InvalidConfiguration) {
		t.Fatal("multiple policies accepted", err)
	}
	if _, err := gateway.NewSingle(&externalChat{}, "remote"); !gateway.IsCode(err, gateway.UnsupportedCapability) || !strings.Contains(err.Error(), "declare verified") {
		t.Fatal("missing capabilities were not actionable", err)
	}
}

func TestStreamTextPreservesResultAndFailure(t *testing.T) {
	p := externalStream{externalBase{caps: gateway.Capabilities{Streaming: gateway.Supported}}}
	g, err := gateway.NewSingle(p, "remote")
	if err != nil {
		t.Fatal(err)
	}
	r := gateway.ChatRequest{Messages: []gateway.Message{gateway.User("hello")}}
	var text strings.Builder
	result, err := g.StreamText(context.Background(), r, func(delta string) error { text.WriteString(delta); return nil })
	if err != nil || text.String() != "external" || result.Text != text.String() || !result.ProtocolComplete {
		t.Fatalf("text=%s result=%+v err=%v", text.String(), result, err)
	}
	sentinel := errors.New("stop consumer")
	_, err = g.StreamText(context.Background(), r, func(string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var detail *gateway.Error
	if !errors.As(err, &detail) || detail.ModelAlias != "remote" || detail.Model != "remote" {
		t.Fatalf("routing details missing: %+v", err)
	}
	if _, err = g.StreamText(context.Background(), r, nil); !gateway.IsCode(err, gateway.InvalidRequest) {
		t.Fatal(err)
	}
}

func TestBudgetPreviewMatchesDispatchWithoutGeneration(t *testing.T) {
	counts, generations := 0, 0
	policy := gateway.TokenPolicy{ContextWindow: 27, DefaultMaxOutputTokens: 10, SafetyMarginTokens: 5, SafetyMarginPercent: 10, CountInputTokens: func(context.Context, gateway.ChatRequest) (int, error) {
		counts++
		return 11, nil
	}}
	g, err := gateway.NewSingle(budgetProvider{accept: func(gateway.ChatRequest) { generations++ }}, "remote", policy)
	if err != nil || counts != 0 {
		t.Fatalf("construction invoked counter: %d %v", counts, err)
	}
	r := gateway.ChatRequest{Messages: []gateway.Message{gateway.User("hello")}}
	budget, err := g.EstimateBudget(context.Background(), r)
	if !gateway.IsCode(err, gateway.ContextExceeded) || budget.TotalTokens == nil || *budget.TotalTokens != 28 || generations != 0 || counts != 1 {
		t.Fatalf("budget=%+v generations=%d counts=%d err=%v", budget, generations, counts, err)
	}
	_, err = g.Chat(context.Background(), r)
	var detail *gateway.Error
	if !errors.As(err, &detail) || !reflect.DeepEqual(*detail.TokenBudget, budget) || detail.ModelAlias != "remote" || counts != 2 || generations != 0 {
		t.Fatalf("preview and runtime differ: %+v", err)
	}
	policy.ContextWindow = 28
	g, err = gateway.NewSingle(budgetProvider{accept: func(gateway.ChatRequest) { generations++ }}, "remote", policy)
	if err != nil {
		t.Fatal(err)
	}
	budget, err = g.EstimateBudget(context.Background(), r)
	if err != nil || *budget.TotalTokens != 28 || generations != 0 || r.MaxOutputTokens != nil || r.Model != "" {
		t.Fatalf("preview mutated input or generated: %+v %v", r, err)
	}
	if _, err = g.Chat(context.Background(), r); err != nil || generations != 1 {
		t.Fatal(err)
	}
	plain, err := gateway.NewSingle(&externalChat{caps: gateway.Capabilities{Chat: gateway.Supported}}, "remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plain.EstimateBudget(context.Background(), r); !gateway.IsCode(err, gateway.InvalidConfiguration) {
		t.Fatal("missing policy accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.EstimateBudget(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type localValidator struct{ externalChat }

func (p localValidator) ValidateChatRequest(gateway.ChatRequest) error {
	return &gateway.Error{Code: gateway.InvalidConfiguration, Field: "LocalPrerequisite", Message: "configure the local prerequisite before generating"}
}

func TestConfigurationDiagnosticsAreLocalAndOrdered(t *testing.T) {
	p := &externalChat{}
	cfg := config(p)
	cfg.Models["a"] = cfg.Models["chat"]
	g, err := gateway.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = g.CheckConfiguration(context.Background())
	var detail *gateway.Error
	if !errors.As(err, &detail) || detail.ModelAlias != "a" || detail.Field != "Capabilities.Chat" {
		t.Fatalf("unordered or unhelpful diagnostics: %+v", err)
	}
	g, err = gateway.New(gateway.Config{Deployments: map[string]gateway.Provider{"test": p}})
	if err != nil {
		t.Fatal("catalog-only New must remain valid", err)
	}
	if err := g.CheckConfiguration(context.Background()); !gateway.IsCode(err, gateway.InvalidConfiguration) {
		t.Fatal(err)
	}
	g, err = gateway.New(config(&localValidator{}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Chat(context.Background(), input())
	if !errors.As(err, &detail) || detail.Field != "LocalPrerequisite" || detail.ModelAlias != "chat" || detail.Model != "remote" {
		t.Fatalf("provider prerequisite hidden by capability gate: %+v", err)
	}
}

type embeddingWithChatValidation struct{ localValidator }

func (p embeddingWithChatValidation) Capabilities(string) gateway.Capabilities {
	return gateway.Capabilities{Chat: gateway.Unsupported, Streaming: gateway.Unsupported, Embeddings: gateway.Supported}
}

func (p embeddingWithChatValidation) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResult, error) {
	return gateway.EmbedResult{Vectors: [][]float64{{1}}}, nil
}

func TestSingleEmbeddingDoesNotRequireChatPrerequisites(t *testing.T) {
	g, err := gateway.NewSingle(embeddingWithChatValidation{}, "embedding")
	if err != nil {
		t.Fatal("chat-only prerequisites rejected embedding model", err)
	}
	result, err := g.Embed(context.Background(), gateway.EmbedRequest{Input: []string{"hello"}})
	if err != nil || len(result.Vectors) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
