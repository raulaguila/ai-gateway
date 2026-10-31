package samsung_test

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/providers/samsung"
)

func server(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}
func provider(t *testing.T, cfg samsung.Config) *samsung.Provider {
	t.Helper()
	p, err := samsung.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.CloseIdleConnections)
	return p
}
func request() gateway.ChatRequest {
	return gateway.ChatRequest{Model: "agent", Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "hi"}}}
}

// These envelopes reproduce the shapes in Thoth's samsung_stream_test.go.
func answer(text, session string) string {
	raw, _ := json.Marshal(map[string]any{"session_id": session, "outputs": []any{map[string]any{"outputs": []any{map[string]any{"results": map[string]any{"message": map[string]any{"text": text}}}}}}})
	return string(raw)
}
func end(text, session string) string {
	return `{"event":"end","data":{"result":` + answer(text, session) + `}}`
}
func token(text string) string {
	raw, _ := json.Marshal(map[string]any{"event": "token", "data": map[string]any{"chunk": text}})
	return string(raw)
}

func TestThothBufferedPayloadAndComponents(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/prefix/api/v1/run/agent" || r.URL.Query().Get("stream") != "false" || r.Header.Get("x-api-key") != "test-key" || r.Header.Get("Accept") != "application/json" {
					t.Error("incorrect buffered request")
				}
				var body struct {
					InputType  string                                `json:"input_type"`
					OutputType string                                `json:"output_type"`
					InputValue string                                `json:"input_value"`
					Session    json.RawMessage                       `json:"session_id"`
					Components map[string]map[string]json.RawMessage `json:"component_inputs"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.InputType != "chat" || body.OutputType != "chat" || body.InputValue != "sys\n\n---\n\nuser: código ALFA-739\n\nassistant: anotado\n\nuser: qual código?" || body.Session != nil {
					t.Errorf("body=%+v", body)
				}
				if len(body.Components) != 4 || string(body.Components["in"]["session_id"]) != `""` || string(body.Components["in"]["should_store_message"]) != "false" || string(body.Components["in"]["sender_name"]) != `"Raul"` || string(body.Components["out"]["sender_name"]) != `"AI"` || string(body.Components["out"]["session_id"]) != `""` || string(body.Components["out"]["should_store_message"]) != "false" || string(body.Components["mem"]["n_messages"]) != "0" || string(body.Components["mem"]["order"]) != `"Ascending"` {
					t.Errorf("components=%s", body.Components)
				}
				var encoded string
				_ = json.Unmarshal(body.Components["llm"]["parameters"], &encoded)
				var parameters map[string]any
				if err := json.Unmarshal([]byte(encoded), &parameters); err != nil {
					t.Error("parameters are not a JSON string")
					return
				}
				if explicit {
					if parameters["temperature"] != float64(0) || parameters["top_p"] != float64(0) || parameters["max_tokens"] != float64(128) {
						t.Errorf("explicit parameters=%v", parameters)
					}
				} else if parameters["temperature"] != 0.3 || parameters["top_p"] != 0.95 || parameters["max_tokens"] != nil {
					t.Errorf("default parameters=%v", parameters)
				}
				if parameters["extra_body"].(map[string]any)["repetition_penalty"] != 1.05 {
					t.Error("repetition penalty missing")
				}
				io.WriteString(w, answer("final answer", "session-2"))
			})
			p := provider(t, samsung.Config{BaseURL: s.URL + "/prefix///", AgentKey: "fallback", APIKey: "test-key", ChatInputID: "in", ChatOutputID: "out", MemoryComponentID: "mem", LLMComponentID: "llm", UserName: "Raul"})
			r := request()
			r.System = " sys "
			r.Messages = []gateway.Message{{Role: gateway.RoleUser, Content: "código ALFA-739"}, {Role: gateway.RoleAssistant, Content: "anotado"}, {Role: gateway.RoleUser, Content: "qual código?"}}
			if explicit {
				zero, max := 0.0, 128
				r.Temperature, r.TopP, r.MaxOutputTokens = &zero, &zero, &max
			}
			result, err := p.Chat(context.Background(), r)
			if err != nil || result.Text != "final answer" || result.SessionID != "session-2" || !result.ProtocolComplete || result.FinishReason != gateway.FinishStop || result.Usage.Input.Tokens != nil || result.Usage.Output.Tokens != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestHistorySelectionLocalCatalogAndAgentFallback(t *testing.T) {
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/run/configured" {
			t.Error(r.URL.Path)
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if string(body["input_value"]) != `"sys\n\n---\n\nuser: live"` || body["component_inputs"] != nil || body["session_id"] != nil {
			t.Errorf("body=%s", body)
		}
		io.WriteString(w, answer("answer", "observed"))
	})
	history := false
	p := provider(t, samsung.Config{BaseURL: s.URL, AgentKey: "configured", SendHistory: &history})
	history = true
	models, err := p.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "configured" || calls.Load() != 0 {
		t.Fatalf("models=%v err=%v calls=%d", models, err, calls.Load())
	}
	r := request()
	r.Model = ""
	r.System = "sys"
	r.Messages = []gateway.Message{{Role: gateway.RoleUser, Content: "old"}, {Role: gateway.RoleAssistant, Content: "old answer"}, {Role: gateway.RoleUser, Content: "live"}, {Role: gateway.RoleAssistant, Content: "after"}}
	for i := 0; i < 2; i++ {
		if _, err := p.Chat(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	noDefault := provider(t, samsung.Config{BaseURL: s.URL})
	empty, err := noDefault.ListModels(context.Background())
	if err != nil || len(empty) != 0 || calls.Load() != 2 {
		t.Fatalf("models=%v err=%v", empty, err)
	}
}

func TestStreamThothShapesAndTerminalConsistency(t *testing.T) {
	for _, tc := range []struct {
		name, body, text, session string
		code                      gateway.ErrorCode
	}{
		{"prefix completion", token("hello") + "\n" + end("hello world", "session-1") + "\n", "hello world", "session-1", ""},
		{"end only no newline", end("ok", "session-end"), "ok", "session-end", ""},
		{"session event", `{"event":"add_message","data":{"session_id":"session-add","text":"not answer"}}` + "\n" + token("answer") + "\n" + `{"event":"end"}`, "answer", "session-add", ""},
		{"identical final", token("answer") + "\n" + end("answer", "s"), "answer", "s", ""},
		{"unknown event", `{"event":"build_start","data":{"text":"not answer"}}` + "\n" + end("ok", ""), "ok", "", ""},
		{"EOF partial", token("hello") + "\n", "hello", "", gateway.StreamIncomplete},
		{"divergent final", token("hello") + "\n" + end("different", "s"), "hello", "s", gateway.InvalidResponse},
		{"shortened final", token("hello world") + "\n" + end("hello", "s"), "hello world", "s", gateway.InvalidResponse},
		{"empty final", end(" ", "s"), "", "s", gateway.InvalidResponse},
		{"invalid JSON", token("partial") + "\n" + `{test-key private}`, "partial", "", gateway.InvalidResponse},
		{"invalid token", `{"event":"token","data":{"chunk":1}}`, "", "", gateway.InvalidResponse},
		{"invalid end", token("partial") + "\n" + `{"event":"end","data":{"result":"test-key"}}`, "partial", "", gateway.InvalidResponse},
		{"remote error", token("partial") + "\n" + `{"event":"error","data":{"message":"test-key private"}}`, "partial", "", gateway.UpstreamError},
		{"message error", `{"event":"add_message","data":{"error":true,"text":"test-key private"}}`, "", "", gateway.UpstreamError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("stream") != "true" || r.Header.Get("Accept") != "application/x-ndjson" {
					t.Error("incorrect stream request")
				}
				io.WriteString(w, tc.body)
			})
			p := provider(t, samsung.Config{BaseURL: s.URL, APIKey: "test-key"})
			var text string
			var sequence uint64
			terminals := 0
			result, err := p.Stream(context.Background(), request(), func(e gateway.Event) error {
				sequence++
				if e.Sequence != sequence {
					t.Errorf("sequence=%d want=%d", e.Sequence, sequence)
				}
				if e.Type == gateway.EventTextDelta {
					text += e.Text
				}
				if e.Type == gateway.EventError || e.Type == gateway.EventCompleted {
					terminals++
				}
				if e.Result != nil {
					if e.Result.Text != tc.text || e.Result.SessionID != tc.session {
						t.Errorf("completion=%+v", e.Result)
					}
					e.Result.SessionID = "mutated"
				}
				if e.Error != nil {
					if e.Error.Provider != "samsung" {
						t.Error("wrong provider")
					}
					e.Error.Code = "mutated"
				}
				return nil
			})
			if tc.code == "" && err != nil || tc.code != "" && !gateway.IsCode(err, tc.code) {
				t.Fatalf("err=%v want=%s", err, tc.code)
			}
			if result.Text != tc.text || text != tc.text || result.SessionID != tc.session || terminals != 1 || result.ProtocolComplete != (err == nil) || result.Usage.Input.Tokens != nil || result.Usage.Output.Tokens != nil {
				t.Fatalf("result=%+v text=%q terminals=%d", result, text, terminals)
			}
			if err != nil && (strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "private")) {
				t.Fatal("remote body leaked")
			}
		})
	}
}

func TestValidationAndGateway(t *testing.T) {
	for _, cfg := range []samsung.Config{{}, {BaseURL: "ftp://example.com"}, {BaseURL: "https://user:secret@example.com"}, {BaseURL: "https://example.com?key=secret"}, {BaseURL: "https://example.com", APIKey: "bad\nkey"}, {BaseURL: "https://example.com", AgentKey: "../escape"}, {BaseURL: "https://example.com", ChatInputID: "same", MemoryComponentID: "same"}, {BaseURL: "https://example.com", MaxLineBytes: -1}} {
		if _, err := samsung.New(cfg); !gateway.IsCode(err, gateway.InvalidConfiguration) {
			t.Fatalf("invalid configuration accepted: %v", err)
		}
	}
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/run/agent" {
			t.Error("alias not resolved")
		}
		if r.URL.Query().Get("stream") == "true" {
			io.WriteString(w, end("stream", "session"))
		} else {
			io.WriteString(w, answer("chat", "session"))
		}
	})
	p := provider(t, samsung.Config{BaseURL: s.URL, AgentKey: "agent"})
	g, err := gateway.New(gateway.Config{Deployments: map[string]gateway.Provider{"builder": p}, Models: map[string]gateway.ModelRoute{"chat": {Deployment: "builder", Model: "agent"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := request()
	r.Model = "chat"
	if _, err := g.Chat(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Stream(context.Background(), r, func(gateway.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	r.Tools = []gateway.Tool{{Name: "f", Parameters: json.RawMessage(`{}`)}}
	if _, err := g.Chat(context.Background(), r); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), r); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
	if _, err := g.Embed(context.Background(), gateway.EmbedRequest{Model: "chat", Input: []string{"x"}}); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
	r = request()
	zero := 0.0
	r.Temperature = &zero
	if _, err := p.Chat(context.Background(), r); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
	r = request()
	r.Model = "../escape"
	if _, err := p.Chat(context.Background(), r); !gateway.IsCode(err, gateway.InvalidRequest) {
		t.Fatal(err)
	}
	if _, err := p.Stream(context.Background(), request(), nil); !gateway.IsCode(err, gateway.InvalidRequest) {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("invalid/unsupported operations called network")
	}
}

func TestCallbacksCancellationAndConcurrentSessions(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			cancelled := make(chan struct{})
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				io.WriteString(w, token("partial")+"\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(cancelled)
				case <-time.After(2 * time.Second):
				}
			})
			p := provider(t, samsung.Config{BaseURL: s.URL})
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
			if result.Text != "partial" || result.ProtocolComplete {
				t.Fatalf("partial=%+v", result)
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream not cancelled")
			}
		})
	}
	t.Run("independent sessions", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input   string          `json:"input_value"`
				Session json.RawMessage `json:"session_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Session != nil {
				t.Error("remote session reused")
			}
			io.WriteString(w, answer(body.Input, "session:"+body.Input))
		})
		p := provider(t, samsung.Config{BaseURL: s.URL})
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := request()
				r.Messages[0].Content = fmt.Sprint(i)
				result, err := p.Chat(context.Background(), r)
				if err != nil || result.SessionID != "session:user: "+fmt.Sprint(i) {
					t.Errorf("result=%+v err=%v", result, err)
				}
			}(i)
		}
		wg.Wait()
	})
}

func TestHTTPFailuresTimeoutTLSAndLimits(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("x-request-id", "test-key")
				w.WriteHeader(status)
				io.WriteString(w, "test-key private prompt")
			})
			p := provider(t, samsung.Config{BaseURL: s.URL, APIKey: "test-key"})
			_, err := p.Chat(context.Background(), request())
			var problem *gateway.Error
			if !errors.As(err, &problem) || problem.UpstreamStatus != status || problem.Provider != "samsung" || problem.Operation != "chat" || problem.RequestID != "" || calls.Load() != 1 || strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "private") {
				t.Fatalf("err=%+v calls=%d", err, calls.Load())
			}
		})
	}
	t.Run("deadline", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		})
		p := provider(t, samsung.Config{BaseURL: s.URL, RequestTimeout: 20 * time.Millisecond, StreamTimeout: 20 * time.Millisecond})
		_, chatErr := p.Chat(context.Background(), request())
		_, streamErr := p.Stream(context.Background(), request(), func(gateway.Event) error { t.Error("pre-admission event"); return nil })
		for _, err := range []error{chatErr, streamErr} {
			if !gateway.IsCode(err, gateway.Timeout) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		}
	})
	t.Run("TLS", func(t *testing.T) {
		s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("stream") == "true" {
				io.WriteString(w, end("ok", "session"))
			} else {
				io.WriteString(w, answer("ok", "session"))
			}
		}))
		s.Config.ErrorLog = log.New(io.Discard, "", 0)
		s.StartTLS()
		t.Cleanup(s.Close)
		secure := provider(t, samsung.Config{BaseURL: s.URL})
		if _, err := secure.Chat(context.Background(), request()); err == nil {
			t.Fatal("untrusted certificate accepted")
		}
		trusted := provider(t, samsung.Config{BaseURL: s.URL, HTTPClient: s.Client()})
		if _, err := trusted.Chat(context.Background(), request()); err != nil {
			t.Fatal(err)
		}
		if _, err := trusted.Stream(context.Background(), request(), func(gateway.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	})
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("limits ", stream), func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", 200)) })
			p := provider(t, samsung.Config{BaseURL: s.URL, MaxResponseBytes: 128, MaxLineBytes: 64})
			var err error
			if stream {
				_, err = p.Stream(context.Background(), request(), func(gateway.Event) error { return nil })
			} else {
				_, err = p.Chat(context.Background(), request())
			}
			if !gateway.IsCode(err, gateway.InvalidResponse) {
				t.Fatal(err)
			}
		})
	}
	for _, body := range []string{`null`, `{}`, `not-json`, answer(" ", "s"), answer("ok", "s") + `{}`} {
		t.Run(body, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			p := provider(t, samsung.Config{BaseURL: s.URL})
			result, err := p.Chat(context.Background(), request())
			if !gateway.IsCode(err, gateway.InvalidResponse) || result.ProtocolComplete {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
