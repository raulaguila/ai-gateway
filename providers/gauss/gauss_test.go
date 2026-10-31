package gauss_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/providers/gauss"
)

const credentials = `{"openapi_token":"Bearer openapi-secret","client_token":"client-secret"}`
const chatJSON = `{"id":"chat","choices":[{"index":0,"message":{"role":"assistant","content":"<think>privado</think>Olá"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`
const catalogJSON = `{"data":[{"modelGuid":"guid-1","modelId":"0007","modelServingId":"serving-1","name":{"pt":"Nome","en":"English"},"description":{"pt":"outro","en":"Tool Calling: O\nContext Window: 64k"}},{"modelGuid":"guid-2","modelId":8,"modelServingId":"serving-2","description":"Tool Calling: X","contextLength":8192},{"modelGuid":"guid-1","modelId":99,"modelServingId":"duplicate"},{"modelGuid":"broken","modelId":0,"modelServingId":"bad"}]}`

func server(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}
func provider(t *testing.T, cfg gauss.Config) *gauss.Provider {
	t.Helper()
	if cfg.APIKey == "" {
		cfg.APIKey = credentials
	}
	if cfg.Models == nil {
		cfg.Models = []gauss.Model{{GUID: "guid-1", RoutingID: "0007", ServingID: "serving-1", Tools: gateway.Supported}}
	}
	p, err := gauss.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.CloseIdleConnections)
	return p
}
func request() gateway.ChatRequest {
	return gateway.ChatRequest{Model: "guid-1", Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "Oi"}}}
}
func headers(t *testing.T, r *http.Request, generation bool) {
	t.Helper()
	if r.Header.Get("x-openapi-token") != "Bearer openapi-secret" || r.Header.Get("x-generative-ai-client") != "client-secret" || r.Header.Get("Authorization") != "" {
		t.Error("incorrect Gauss authentication headers")
	}
	if generation && r.Header.Get("x-llm-model-id") != "7" {
		t.Errorf("routing=%q", r.Header.Get("x-llm-model-id"))
	}
}
func event(payload string) string { return "data: " + payload + "\n\n" }
func delta(text string) string {
	raw, _ := json.Marshal(map[string]any{"id": "s", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}})
	return event(string(raw))
}

var finish = event(`{"id":"s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)

const done = "data: [DONE]\n\n"

func TestCatalogRoutesAndPreservesSnapshotOnFailure(t *testing.T) {
	var fail atomic.Bool
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			headers(t, r, false)
			if fail.Load() {
				w.WriteHeader(500)
				io.WriteString(w, "openapi-secret client-secret")
				return
			}
			io.WriteString(w, catalogJSON)
			return
		}
		headers(t, r, true)
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if string(body["model"]) != `"serving-1"` {
			t.Error("GUID sent instead of serving ID")
		}
		io.WriteString(w, chatJSON)
	})
	p := provider(t, gauss.Config{BaseURL: s.URL, Models: []gauss.Model{}})
	if _, err := p.Chat(context.Background(), request()); !gateway.IsCode(err, gateway.InvalidConfiguration) {
		t.Fatal(err)
	}
	models, err := p.ListModels(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "guid-1" || models[1].Capabilities.Tools != gateway.Unsupported {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	native, stamp := p.Snapshot()
	if stamp.IsZero() || len(native) != 2 || native[0].RoutingID != "7" || native[0].ServingID != "serving-1" || native[0].Name != "English" || native[0].ContextWindow != 64000 {
		t.Fatalf("snapshot=%+v", native)
	}
	native[0].ServingID = "mutated"
	result, err := p.Chat(context.Background(), request())
	if err != nil || result.Text != "Olá" || !result.ProtocolComplete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	fail.Store(true)
	if _, err := p.ListModels(context.Background()); !gateway.IsCode(err, gateway.UpstreamError) {
		t.Fatal(err)
	}
	kept, keptTime := p.Snapshot()
	if !stamp.Equal(keptTime) || kept[0].ServingID != "serving-1" {
		t.Fatal("failed refresh discarded snapshot")
	}
	if _, err := p.Chat(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
}

func TestBaseNormalizationParametersAndTools(t *testing.T) {
	for _, suffix := range []string{"/api-llm", "/api-llm/", "/api-chat", "/api-chat/openapi/llm/v1/", "/api-llm/openapi/llm/v1"} {
		t.Run(suffix, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				headers(t, r, true)
				if r.URL.Path != "/mount/api-llm/openapi/llm/v1/chat/completions" {
					t.Error(r.URL.Path)
				}
				var body map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				if string(body["model"]) != `"serving-1"` || string(body["temperature"]) != "0" || string(body["top_p"]) != "0" || string(body["max_tokens"]) != "12" || body["stream_options"] != nil {
					t.Errorf("unexpected body %s", body)
				}
				io.WriteString(w, chatJSON)
			})
			p := provider(t, gauss.Config{BaseURL: s.URL + "/mount" + suffix})
			r := request()
			zero, max := 0.0, 12
			r.Temperature, r.TopP, r.MaxOutputTokens = &zero, &zero, &max
			if _, err := p.Chat(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("tool round trip", func(t *testing.T) {
		var calls atomic.Int32
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["tools"] == nil || string(body["tool_choice"]) != `"auto"` {
				t.Error("tools not serialized")
			}
			if calls.Add(1) == 1 {
				io.WriteString(w, `{"choices":[{"index":0,"message":{"content":null,"tool_calls":[{"id":"one","type":"function","function":{"name":"lookup","arguments":"{}"}},{"id":"two","type":"function","function":{"name":"lookup","arguments":"{\"id\":1}"}}]},"finish_reason":"tool_calls"}]}`)
			} else {
				io.WriteString(w, chatJSON)
			}
		})
		p := provider(t, gauss.Config{BaseURL: s.URL})
		r := request()
		r.Tools = []gateway.Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}
		result, err := p.Chat(context.Background(), r)
		if err != nil || len(result.ToolCalls) != 2 || result.FinishReason != gateway.FinishToolUse {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		r.Messages = append(r.Messages, gateway.Message{Role: gateway.RoleAssistant, ToolCalls: result.ToolCalls}, gateway.Message{Role: gateway.RoleTool, ToolCallID: "one", Content: "a"}, gateway.Message{Role: gateway.RoleTool, ToolCallID: "two", Content: "b"})
		if _, err := p.Chat(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStreamVisibleTextAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, frames, text string
		code               gateway.ErrorCode
	}{
		{"split tags", delta("Antes <thi") + delta("nk>secret</th") + delta("ink><analysis>private</analysis>depois") + finish + event(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":9}}`) + done, "Antes depois", ""},
		{"only hidden", delta("<reasoning>secret</reasoning>") + finish + done, "", gateway.InvalidResponse},
		{"partial", delta("Olá<thinking>secret"), "Olá", gateway.StreamIncomplete},
		{"malformed", delta("Olá") + "data: openapi-secret client-secret\n\n", "Olá", gateway.InvalidResponse},
		{"remote error", delta("Olá") + "event: error\ndata: {\"error\":\"openapi-secret\"}\n\n", "Olá", gateway.UpstreamError},
		{"Thoth line framing", strings.ReplaceAll(delta("Olá")+finish+done, "\n\n", "\n"), "Olá", ""},
		{"multiline JSON", "data: {\"choices\":[\ndata: {\"index\":0,\"delta\":{\"content\":\"Olá\"}}\ndata: ]}\n\n" + finish + done, "Olá", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				headers(t, r, true)
				var body map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["stream_options"] != nil || string(body["stream"]) != "true" {
					t.Error("incorrect Gauss streaming payload")
				}
				if tc.name != "Thoth line framing" {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				io.WriteString(w, tc.frames)
			})
			p := provider(t, gauss.Config{BaseURL: s.URL})
			var text string
			var sequence uint64
			terminals := 0
			result, err := p.Stream(context.Background(), request(), func(e gateway.Event) error {
				sequence++
				if sequence != e.Sequence {
					t.Errorf("sequence=%d want=%d", e.Sequence, sequence)
				}
				if e.Type == gateway.EventTextDelta {
					text += e.Text
				}
				if e.Type == gateway.EventCompleted || e.Type == gateway.EventError {
					terminals++
				}
				if e.Result != nil && e.Result.Text != tc.text {
					t.Errorf("raw completion exposed: %q", e.Result.Text)
				}
				if e.Error != nil && e.Error.Provider != "gauss" {
					t.Error("wrong provider")
				}
				return nil
			})
			if tc.code == "" && err != nil || tc.code != "" && !gateway.IsCode(err, tc.code) {
				t.Fatalf("err=%v want=%s", err, tc.code)
			}
			if result.Text != tc.text || text != tc.text || terminals != 1 || calls.Load() != 1 || result.ProtocolComplete != (err == nil) {
				t.Fatalf("result=%+v text=%q terminals=%d calls=%d", result, text, terminals, calls.Load())
			}
			if err != nil && (strings.Contains(err.Error(), "openapi-secret") || strings.Contains(err.Error(), "client-secret")) {
				t.Fatal("credentials leaked")
			}
		})
	}
}

func TestQuotaSharedAcrossInstancesAndCatalogBypass(t *testing.T) {
	var generations atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, catalogJSON)
			return
		}
		generations.Add(1)
		io.WriteString(w, chatJSON)
	})
	p := provider(t, gauss.Config{BaseURL: s.URL, FailIfBusy: true})
	other := provider(t, gauss.Config{BaseURL: s.URL + "/openapi/llm/v1/", FailIfBusy: true})
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		target := p
		if i == 1 {
			target = other
		}
		if _, err := target.Chat(context.Background(), request()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := other.Chat(context.Background(), request()); !gateway.IsCode(err, gateway.CapacityUnavailable) || generations.Load() != 3 {
		t.Fatalf("shared quota bypassed: %v calls=%d", err, generations.Load())
	}
	if _, err := other.ListModels(context.Background()); err != nil {
		t.Fatal("catalog consumed generation quota", err)
	}
	var waits atomic.Int32
	waiting := provider(t, gauss.Config{BaseURL: s.URL, OnWait: func(context.Context, time.Duration) error { waits.Add(1); return nil }})
	g, err := gateway.New(gateway.Config{Deployments: map[string]gateway.Provider{"corporate": waiting}, Models: map[string]gateway.ModelRoute{"chat": {Deployment: "corporate", Model: "guid-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := request()
	r.Model = "chat"
	waitEvents := 0
	_, err = g.Stream(ctx, r, func(e gateway.Event) error {
		if e.Type != gateway.EventProviderWait || e.Wait.Provider != "corporate" || e.Wait.Duration <= 0 {
			t.Errorf("incorrect pre-admission event: %+v", e)
		}
		waitEvents++
		return nil
	})
	if !gateway.IsCode(err, gateway.Timeout) || !errors.Is(err, context.DeadlineExceeded) || waitEvents == 0 || waits.Load() == 0 || generations.Load() != 3 {
		t.Fatalf("wait not cancelled: %v waits=%d calls=%d", err, waitEvents, generations.Load())
	}
	sentinel := errors.New("wait consumer stopped")
	_, err = waiting.Stream(context.Background(), request(), func(gateway.Event) error { return sentinel })
	if !gateway.IsCode(err, gateway.CallbackFailed) || !errors.Is(err, sentinel) {
		t.Fatalf("wait callback cause lost: %v", err)
	}
}

func TestRateLimitRetriesAndCooldown(t *testing.T) {
	for _, op := range []string{"chat", "stream", "catalog"} {
		t.Run(op, func(t *testing.T) {
			var calls atomic.Int32
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				headers(t, r, r.Method == http.MethodPost)
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(429)
					io.WriteString(w, `{"error":"openapi-secret client-secret"}`)
					return
				}
				if op == "catalog" {
					io.WriteString(w, catalogJSON)
				} else if op == "stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, delta("Olá")+finish+done)
				} else {
					io.WriteString(w, chatJSON)
				}
			})
			var waits atomic.Int32
			p := provider(t, gauss.Config{BaseURL: s.URL, OnWait: func(_ context.Context, delay time.Duration) error {
				if delay <= 0 {
					t.Error("invalid delay")
				}
				waits.Add(1)
				return nil
			}})
			var err error
			switch op {
			case "chat":
				_, err = p.Chat(context.Background(), request())
			case "stream":
				_, err = p.Stream(context.Background(), request(), func(gateway.Event) error { return nil })
			case "catalog":
				_, err = p.ListModels(context.Background())
			}
			if err != nil || calls.Load() != 2 || waits.Load() == 0 {
				t.Fatalf("err=%v calls=%d waits=%d", err, calls.Load(), waits.Load())
			}
		})
	}
	t.Run("attempt limit and catalog cooldown", func(t *testing.T) {
		var calls atomic.Int32
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			io.WriteString(w, "openapi-secret")
		})
		p := provider(t, gauss.Config{BaseURL: s.URL, MaxAttempts: 2})
		if _, err := p.Chat(context.Background(), request()); !gateway.IsCode(err, gateway.RateLimited) || calls.Load() != 2 {
			t.Fatalf("err=%v calls=%d", err, calls.Load())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := p.ListModels(ctx); !gateway.IsCode(err, gateway.Timeout) || calls.Load() != 2 {
			t.Fatalf("catalog ignored cooldown: %v calls=%d", err, calls.Load())
		}
	})
}

func TestStreamCallbackFailureAndCancellation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			cancelled := make(chan struct{})
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, delta("Olá<think>hidden"))
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(cancelled)
				case <-time.After(2 * time.Second):
				}
			})
			p := provider(t, gauss.Config{BaseURL: s.URL})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("consumer stopped")
			terminals := 0
			result, err := p.Stream(ctx, request(), func(e gateway.Event) error {
				if e.Type == gateway.EventTextDelta {
					if fail {
						return sentinel
					}
					cancel()
				}
				if e.Type == gateway.EventError || e.Type == gateway.EventCompleted {
					terminals++
				}
				return nil
			})
			if fail {
				if !gateway.IsCode(err, gateway.CallbackFailed) || !errors.Is(err, sentinel) || terminals != 0 {
					t.Fatalf("err=%v terminals=%d", err, terminals)
				}
			} else if !gateway.IsCode(err, gateway.Cancelled) || !errors.Is(err, context.Canceled) || terminals != 1 {
				t.Fatalf("err=%v terminals=%d", err, terminals)
			}
			if result.Text != "Olá" || result.ProtocolComplete {
				t.Fatalf("partial=%+v", result)
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream not cancelled")
			}
		})
	}
}

func TestConfigurationStaleModelsAndErrors(t *testing.T) {
	for _, cfg := range []gauss.Config{
		{}, {APIKey: `{"openapi_token":"a"}`}, {APIKey: `{"openapi_token":"Bearer ","client_token":"b"}`}, {APIKey: `{"openapi_token":"a\nb","client_token":"b"}`},
		{APIKey: credentials, BaseURL: "https://user:password@example.com"}, {APIKey: credentials, BaseURL: "https://example.com?q=1"}, {APIKey: credentials, MaxAttempts: -1},
		{APIKey: credentials, Models: []gauss.Model{{GUID: "g", RoutingID: "bad", ServingID: "s"}}},
	} {
		if _, err := gauss.New(cfg); !gateway.IsCode(err, gateway.InvalidConfiguration) {
			t.Fatalf("invalid config accepted: %v", err)
		}
	}
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("x-request-id", "client-secret")
		w.WriteHeader(401)
		io.WriteString(w, "openapi-secret client-secret")
	})
	p := provider(t, gauss.Config{BaseURL: s.URL, CatalogUpdatedAt: time.Now().Add(-time.Hour)})
	if _, err := p.Chat(context.Background(), request()); !gateway.IsCode(err, gateway.InvalidConfiguration) || calls.Load() != 0 {
		t.Fatal("stale snapshot called network", err)
	}
	p = provider(t, gauss.Config{BaseURL: s.URL})
	_, err := p.Chat(context.Background(), request())
	var problem *gateway.Error
	if !gateway.IsCode(err, gateway.AuthenticationFailed) || !errors.As(err, &problem) || problem.RequestID != "" || problem.Provider != "gauss" || problem.Operation != "chat" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error=%+v", err)
	}
	g, err := gateway.New(gateway.Config{Deployments: map[string]gateway.Provider{"gauss": p}, Models: map[string]gateway.ModelRoute{"chat": {Deployment: "gauss", Model: "guid-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := g.Embed(context.Background(), gateway.EmbedRequest{Model: "chat", Input: []string{"x"}}); !gateway.IsCode(err, gateway.UnsupportedCapability) || calls.Load() != before {
		t.Fatal(err)
	}
	r := request()
	r.Tools = []gateway.Tool{{Name: "f", Parameters: json.RawMessage(`{}`)}}
	if _, err := p.Stream(context.Background(), r, func(gateway.Event) error { return nil }); !gateway.IsCode(err, gateway.UnsupportedCapability) || calls.Load() != before {
		t.Fatal(err)
	}
	noTools := provider(t, gauss.Config{BaseURL: s.URL, Models: []gauss.Model{{GUID: "guid-1", RoutingID: "7", ServingID: "serving-1", Tools: gateway.Unsupported}}})
	if _, err := noTools.Chat(context.Background(), r); !gateway.IsCode(err, gateway.UnsupportedCapability) || calls.Load() != before {
		t.Fatal(err)
	}
}

func TestTLSAndResponseBounds(t *testing.T) {
	t.Run("TLS", func(t *testing.T) {
		s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headers(t, r, r.Method == http.MethodPost)
			if strings.HasSuffix(r.URL.Path, "/models") {
				io.WriteString(w, catalogJSON)
			} else {
				io.WriteString(w, chatJSON)
			}
		}))
		s.Config.ErrorLog = log.New(io.Discard, "", 0)
		s.StartTLS()
		t.Cleanup(s.Close)
		secure := provider(t, gauss.Config{BaseURL: s.URL})
		if _, err := secure.Chat(context.Background(), request()); err == nil {
			t.Fatal("untrusted certificate accepted")
		}
		trusted := provider(t, gauss.Config{BaseURL: s.URL, HTTPClient: s.Client()})
		if _, err := trusted.Chat(context.Background(), request()); err != nil {
			t.Fatal(err)
		}
		if _, err := trusted.ListModels(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	for _, op := range []string{"chat", "stream", "catalog"} {
		t.Run(op, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				if op == "stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: "+strings.Repeat("x", 200)+"\n\n")
				} else {
					io.WriteString(w, strings.Repeat("x", 200))
				}
			})
			p := provider(t, gauss.Config{BaseURL: s.URL, MaxResponseBytes: 128, MaxEventBytes: 64})
			var err error
			switch op {
			case "chat":
				_, err = p.Chat(context.Background(), request())
			case "stream":
				_, err = p.Stream(context.Background(), request(), func(gateway.Event) error { return nil })
			case "catalog":
				_, err = p.ListModels(context.Background())
			}
			if !gateway.IsCode(err, gateway.InvalidResponse) {
				t.Fatal(err)
			}
		})
	}
}
