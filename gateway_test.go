package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	gateway "github.com/raulaguila/ai-gateway"
)

// This provider intentionally lives outside package gateway and imports no internals.
type externalChat struct{ caps gateway.Capabilities }

func (p *externalChat) Capabilities(string) gateway.Capabilities { return p.caps }
func (p *externalChat) Chat(_ context.Context, r gateway.ChatRequest) (gateway.ChatResult, error) {
	return gateway.ChatResult{Text: r.Model, ProtocolComplete: true}, nil
}

type externalBase struct{ caps gateway.Capabilities }

func (p externalBase) Capabilities(string) gateway.Capabilities { return p.caps }

type externalStream struct{ externalBase }

func (p externalStream) Stream(_ context.Context, _ gateway.ChatRequest, emit func(gateway.Event) error) (gateway.ChatResult, error) {
	r := gateway.ChatResult{Text: "external", ProtocolComplete: true}
	if err := emit(gateway.Event{Type: gateway.EventTextDelta, Sequence: 42, Text: r.Text}); err != nil {
		return r, err
	}
	return r, emit(gateway.Event{Type: gateway.EventCompleted, Result: &r})
}

func input() gateway.ChatRequest {
	return gateway.ChatRequest{Model: "chat", Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "Olá"}}}
}

func config(p gateway.Provider) gateway.Config {
	return gateway.Config{Deployments: map[string]gateway.Provider{"custom": p}, Models: map[string]gateway.ModelRoute{"chat": {Deployment: "custom", Model: "remote"}}}
}

func TestExternalProviderAliasesAndConcurrentCalls(t *testing.T) {
	p := &externalChat{caps: gateway.Capabilities{Chat: gateway.Supported}}
	cfg := config(p)
	g, err := gateway.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	delete(cfg.Deployments, "custom")
	cfg.Models["chat"] = gateway.ModelRoute{Deployment: "missing", Model: "changed"}
	r := input()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := g.Chat(context.Background(), r)
			if err != nil || result.Text != "remote" || !result.ProtocolComplete {
				t.Errorf("result=%+v err=%v", result, err)
			}
		}()
	}
	wg.Wait()
	if r.Model != "chat" {
		t.Fatal("caller request was mutated")
	}
}

func TestCapabilityGatesAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider gateway.Provider
		allow    bool
		want     gateway.ErrorCode
	}{
		{"unknown denied", &externalChat{}, false, gateway.UnsupportedCapability},
		{"unknown opt in", &externalChat{}, true, ""},
		{"explicit denial", &externalChat{caps: gateway.Capabilities{Chat: gateway.Unsupported}}, true, gateway.UnsupportedCapability},
		{"missing interface", externalBase{}, true, gateway.UnsupportedCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config(tc.provider)
			cfg.AllowUnknownCapabilities = tc.allow
			g, err := gateway.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = g.Chat(context.Background(), input())
			if tc.want == "" && err != nil || tc.want != "" && !gateway.IsCode(err, tc.want) {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
		})
	}
	var typedNil *externalChat
	for _, cfg := range []gateway.Config{
		{}, config(typedNil), config(externalBase{caps: gateway.Capabilities{Chat: gateway.Supported}}),
		{Deployments: map[string]gateway.Provider{"custom": &externalChat{}}, Models: map[string]gateway.ModelRoute{"bad": {Deployment: "missing", Model: "x"}}},
	} {
		if _, err := gateway.New(cfg); !gateway.IsCode(err, gateway.InvalidConfiguration) {
			t.Fatalf("invalid config accepted: %v", err)
		}
	}
}

func TestGatewayDispatchAndContext(t *testing.T) {
	p := externalStream{externalBase{caps: gateway.Capabilities{Streaming: gateway.Supported}}}
	g, err := gateway.New(config(p))
	if err != nil {
		t.Fatal(err)
	}
	var sequence uint64
	result, err := g.Stream(context.Background(), input(), func(e gateway.Event) error {
		sequence++
		if e.Sequence != sequence {
			t.Errorf("sequence=%d want=%d", e.Sequence, sequence)
		}
		return nil
	})
	if err != nil || result.Text != "external" || sequence != 2 {
		t.Fatalf("result=%+v events=%d err=%v", result, sequence, err)
	}
	if _, err = g.Stream(context.Background(), input(), nil); !gateway.IsCode(err, gateway.InvalidRequest) {
		t.Fatal(err)
	}
	if _, err = g.Embed(context.Background(), gateway.EmbedRequest{Model: "chat", Input: []string{"x"}}); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
	if _, err = g.ListModels(context.Background(), "custom"); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
	request := input()
	request.Model = "missing"
	if _, err = g.Chat(context.Background(), request); !gateway.IsCode(err, gateway.ModelNotFound) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.Chat(ctx, input()); !gateway.IsCode(err, gateway.Cancelled) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestToolHistoryAndParameterValidation(t *testing.T) {
	call := gateway.ToolCall{ID: "one", Name: "lookup", Arguments: json.RawMessage(`{"id":1}`)}
	r := input()
	r.Messages = append(r.Messages,
		gateway.Message{Role: gateway.RoleAssistant, ToolCalls: []gateway.ToolCall{call, {ID: "two", Name: "lookup", Arguments: json.RawMessage(`{}`)}}},
		gateway.Message{Role: gateway.RoleTool, ToolCallID: "one", Content: "result one"},
		gateway.Message{Role: gateway.RoleTool, ToolCallID: "two", Content: "result two"},
	)
	zero := 0.0
	r.Temperature, r.TopP = &zero, &zero
	if err := gateway.ValidateChatRequest(r); err != nil {
		t.Fatal(err)
	}
	r.Messages = r.Messages[:len(r.Messages)-1]
	if err := gateway.ValidateChatRequest(r); !gateway.IsCode(err, gateway.InvalidRequest) {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`"x"`), json.RawMessage(`{`)} {
		if gateway.JSONObject(raw) {
			t.Fatalf("accepted nonobject: %s", raw)
		}
	}
}
