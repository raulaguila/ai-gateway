package anthropic_test

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
	"github.com/raulaguila/ai-gateway/providers/anthropic"
)

const chatJSON = `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"Olá"},{"type":"text","text":"!"}],"stop_reason":"end_turn"}`

func request() gateway.ChatRequest {
	return gateway.ChatRequest{Model: "remote", Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "Oi"}}}
}

func server(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

func provider(t *testing.T, cfg anthropic.Config) *anthropic.Provider {
	t.Helper()
	if cfg.APIKey == "" {
		cfg.APIKey = "test-secret"
	}
	p, err := anthropic.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.CloseIdleConnections)
	return p
}

func headers(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("x-api-key") != "test-secret" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
		t.Error("incorrect Anthropic authentication or version headers")
	}
}

func frame(kind, body string) string { return "event: " + kind + "\ndata: " + body + "\n\n" }

var start = frame("message_start", `{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`)
var blockStart = frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
var delta = frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Olá"}}`)
var blockStop = frame("content_block_stop", `{"type":"content_block_stop","index":0}`)
var finish = frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`)
var stop = frame("message_stop", `{"type":"message_stop"}`)

func TestChatParametersAndNormalizedBase(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1///", "/prefix/v1"} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprint(suffix, explicit), func(t *testing.T) {
				s := server(t, func(w http.ResponseWriter, r *http.Request) {
					headers(t, r)
					wantPath := "/v1/messages"
					if strings.HasPrefix(suffix, "/prefix") {
						wantPath = "/prefix/v1/messages"
					}
					if r.URL.Path != wantPath || r.Method != "POST" {
						t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					}
					var body struct {
						Model       string   `json:"model"`
						System      string   `json:"system"`
						MaxTokens   int      `json:"max_tokens"`
						Stream      bool     `json:"stream"`
						Temperature *float64 `json:"temperature"`
						TopP        *float64 `json:"top_p"`
						Messages    []struct {
							Role string `json:"role"`
						} `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if body.Model != "remote" || body.Stream || body.System != "global\n\nleading" || len(body.Messages) != 1 || body.Messages[0].Role != "user" {
						t.Errorf("body=%+v", body)
					}
					if explicit {
						if body.Temperature == nil || *body.Temperature != 0 || body.TopP == nil || *body.TopP != 0 || body.MaxTokens != 13 {
							t.Errorf("explicit parameters=%+v", body)
						}
					} else if body.Temperature != nil || body.TopP != nil || body.MaxTokens != 4096 {
						t.Errorf("default parameters=%+v", body)
					}
					io.WriteString(w, chatJSON)
				})
				p := provider(t, anthropic.Config{BaseURL: s.URL + suffix})
				r := request()
				r.System = "global"
				r.Messages = append([]gateway.Message{{Role: gateway.RoleSystem, Content: "leading"}}, r.Messages...)
				if explicit {
					zero, max := 0.0, 13
					r.Temperature, r.TopP, r.MaxOutputTokens = &zero, &zero, &max
				}
				result, err := p.Chat(context.Background(), r)
				if err != nil || result.Text != "Olá!" || result.FinishReason != gateway.FinishStop || result.NativeFinishReason != "end_turn" || !result.ProtocolComplete || result.Usage.Input.Tokens != nil {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			})
		}
	}
}

func TestToolRoundTrip(t *testing.T) {
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		headers(t, r)
		var body struct {
			Tools []struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"input_schema"`
			} `json:"tools"`
			Messages []struct {
				Role    string                       `json:"role"`
				Content []map[string]json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Tools) != 1 || body.Tools[0].Name != "lookup" || !gateway.JSONObject(body.Tools[0].Schema) {
			t.Error("invalid tool definition")
		}
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"type":"message","id":"tools","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Consultando"},{"type":"tool_use","id":"one","name":"lookup","input":{"id":1}},{"type":"tool_use","id":"two","name":"lookup","input":{}}],"usage":{"input_tokens":0,"output_tokens":3}}`)
		} else {
			if len(body.Messages) != 3 || body.Messages[1].Role != "assistant" || len(body.Messages[1].Content) != 3 || body.Messages[2].Role != "user" || len(body.Messages[2].Content) != 2 {
				t.Errorf("incorrect grouped tool history: %+v", body.Messages)
			}
			results := body.Messages[len(body.Messages)-1].Content
			if string(results[0]["tool_use_id"]) != `"one"` || string(results[1]["tool_use_id"]) != `"two"` || string(results[1]["content"]) != `""` {
				t.Error("tool results lost IDs or empty output")
			}
			io.WriteString(w, chatJSON)
		}
	})
	p := provider(t, anthropic.Config{BaseURL: s.URL})
	r := request()
	r.Tools = []gateway.Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}
	result, err := p.Chat(context.Background(), r)
	if err != nil || len(result.ToolCalls) != 2 || result.FinishReason != gateway.FinishToolUse || *result.Usage.Input.Tokens != 0 || result.Usage.Input.Source != gateway.UsageReported {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	r.Messages = append(r.Messages, gateway.Message{Role: gateway.RoleAssistant, Content: result.Text, ToolCalls: result.ToolCalls}, gateway.Message{Role: gateway.RoleTool, ToolCallID: "one", Content: "found"}, gateway.Message{Role: gateway.RoleTool, ToolCallID: "two"})
	if _, err := p.Chat(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestChatInvalidAndExceptionalResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       gateway.ErrorCode
		reason     gateway.FinishReason
	}{
		{"null", `null`, gateway.InvalidResponse, ""},
		{"JSON", `{`, gateway.InvalidResponse, ""},
		{"no finish", strings.Replace(chatJSON, `"end_turn"`, `null`, 1), gateway.InvalidResponse, ""},
		{"no content", strings.Replace(chatJSON, `[{"type":"text","text":"Olá"},{"type":"text","text":"!"}]`, `[]`, 1), gateway.InvalidResponse, ""},
		{"tool argument array", `{"id":"m","type":"message","role":"assistant","content":[{"type":"tool_use","id":"one","name":"f","input":[]}],"stop_reason":"tool_use"}`, gateway.InvalidResponse, ""},
		{"duplicate tool ID", `{"id":"m","type":"message","role":"assistant","content":[{"type":"tool_use","id":"one","name":"f","input":{}},{"type":"tool_use","id":"one","name":"f","input":{}}],"stop_reason":"tool_use"}`, gateway.InvalidResponse, ""},
		{"negative usage", strings.TrimSuffix(chatJSON, "}") + `,"usage":{"input_tokens":-1}}`, gateway.InvalidResponse, ""},
		{"remote error", `{"type":"error","error":{"type":"overloaded_error","message":"test-secret"}}`, gateway.CapacityUnavailable, ""},
		{"refusal", `{"id":"m","type":"message","role":"assistant","content":[],"stop_reason":"refusal"}`, "", gateway.FinishContentFilter},
		{"max tokens", `{"id":"m","type":"message","role":"assistant","content":[],"stop_reason":"max_tokens"}`, "", gateway.FinishMaxTokens},
		{"unknown reason", strings.Replace(chatJSON, "end_turn", "future_reason", 1), "", "future_reason"},
		{"reasoning hidden", `{"id":"m","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"visible"}],"stop_reason":"stop_sequence"}`, "", gateway.FinishStop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) })
			p := provider(t, anthropic.Config{BaseURL: s.URL})
			result, err := p.Chat(context.Background(), request())
			if tc.code == "" {
				if err != nil || !result.ProtocolComplete || result.FinishReason != tc.reason || strings.Contains(result.Text, "private") {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if !gateway.IsCode(err, tc.code) || result.ProtocolComplete {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestStreamFramingUsageAndTerminals(t *testing.T) {
	thinking := frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"private"}}`) + frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private"}}`) + blockStop
	for _, tc := range []struct {
		name, frames, text string
		code               gateway.ErrorCode
		output             int64
	}{
		{"complete", start + blockStart + delta + blockStop + finish + stop, "Olá", "", 4},
		{"multiline and unknown event", start + blockStart + ": ping\n\n" + frame("future", `{"type":"future"}`) + strings.Replace(delta, `,"index"`, ",\ndata: \"index\"", 1) + blockStop + finish + stop, "Olá", "", 4},
		{"cumulative usage", start + blockStart + delta + blockStop + finish + frame("message_delta", `{"type":"message_delta","usage":{"output_tokens":6}}`) + stop, "Olá", "", 6},
		{"hidden thinking", start + thinking + strings.ReplaceAll(blockStart+delta+blockStop, `"index":0`, `"index":1`) + finish + stop, "Olá", "", 4},
		{"no message_stop", start + blockStart + delta + blockStop + finish, "Olá", gateway.StreamIncomplete, 4},
		{"EOF partial", start + blockStart + delta, "Olá", gateway.StreamIncomplete, 1},
		{"foreign DONE", start + blockStart + delta + "data: [DONE]\n\n", "Olá", gateway.InvalidResponse, 1},
		{"open block", start + blockStart + delta + finish + stop, "Olá", gateway.InvalidResponse, 1},
		{"missing start", blockStart + delta + blockStop + finish + stop, "", gateway.InvalidResponse, 0},
		{"duplicate start", start + start, "", gateway.InvalidResponse, 1},
		{"wrong index", start + blockStart + strings.Replace(delta, `"index":0`, `"index":1`, 1), "", gateway.InvalidResponse, 1},
		{"missing delta type", start + blockStart + frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"text":"lost"}}`), "", gateway.InvalidResponse, 1},
		{"event mismatch", start + strings.Replace(blockStart, "event: content_block_start", "event: message_delta", 1), "", gateway.InvalidResponse, 1},
		{"malformed JSON", start + blockStart + delta + "data: invalid\n\n", "Olá", gateway.InvalidResponse, 1},
		{"remote overload", start + blockStart + delta + frame("error", `{"type":"error","error":{"type":"overloaded_error","message":"test-secret private"}}`), "Olá", gateway.CapacityUnavailable, 1},
		{"negative usage", strings.Replace(start, `"input_tokens":10`, `"input_tokens":-1`, 1), "", gateway.InvalidResponse, 0},
		{"unsolicited tool", start + frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"one","name":"f","input":{}}}`), "", gateway.UnsupportedCapability, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				headers(t, r)
				var body struct {
					Stream bool `json:"stream"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if !body.Stream {
					t.Error("stream flag missing")
				}
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				io.WriteString(w, tc.frames)
			})
			p := provider(t, anthropic.Config{BaseURL: s.URL})
			var sequence uint64
			var text string
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
				if e.Error != nil {
					if e.Error.Provider != "anthropic" {
						t.Error("missing provider")
					}
					e.Error.Code = "mutated"
				}
				if e.Usage != nil && e.Usage.Output.Tokens != nil {
					*e.Usage.Output.Tokens = 999
				}
				return nil
			})
			if tc.code == "" && err != nil || tc.code != "" && !gateway.IsCode(err, tc.code) {
				t.Fatalf("err=%v want=%s", err, tc.code)
			}
			if text != tc.text || result.Text != tc.text || terminals != 1 || result.ProtocolComplete != (err == nil) {
				t.Fatalf("text=%q result=%+v terminals=%d", text, result, terminals)
			}
			if result.Usage.Output.Tokens != nil && *result.Usage.Output.Tokens != tc.output {
				t.Errorf("usage=%d want=%d", *result.Usage.Output.Tokens, tc.output)
			}
			if err != nil && (strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "private")) {
				t.Fatal("error leaked remote body")
			}
		})
	}
}

func TestCatalogPaginationAndBounds(t *testing.T) {
	t.Run("pagination with encoded cursor", func(t *testing.T) {
		var calls atomic.Int32
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			headers(t, r)
			if r.URL.Path != "/prefix/v1/models" || r.Method != "GET" || r.URL.Query().Get("limit") != "1000" {
				t.Error("incorrect catalog request")
			}
			if calls.Add(1) == 1 {
				if r.URL.Query().Get("after_id") != "" {
					t.Error("initial cursor present")
				}
				io.WriteString(w, `{"data":[{"id":"model/+?"}],"has_more":true,"last_id":"model/+?"}`)
			} else {
				if r.URL.Query().Get("after_id") != "model/+?" {
					t.Error("cursor not encoded correctly")
				}
				io.WriteString(w, `{"data":[{"id":"remote"}],"has_more":false}`)
			}
		})
		p := provider(t, anthropic.Config{BaseURL: s.URL + "/prefix/v1"})
		models, err := p.ListModels(context.Background())
		if err != nil || len(models) != 2 || calls.Load() != 2 || models[1].Capabilities.Chat != gateway.Unknown || models[1].Capabilities.Embeddings != gateway.Unsupported {
			t.Fatalf("models=%+v err=%v", models, err)
		}
	})
	for _, tc := range []struct{ name, body string }{
		{"missing data", `{}`}, {"null data", `{"data":null}`},
		{"empty ID", `{"data":[{"id":""}]}`},
		{"duplicates", `{"data":[{"id":"a"},{"id":"a"}]}`},
		{"empty page with more", `{"data":[],"has_more":true,"last_id":"a"}`},
		{"missing cursor", `{"data":[{"id":"a"}],"has_more":true}`},
		{"wrong cursor", `{"data":[{"id":"a"}],"has_more":true,"last_id":"b"}`},
		{"loop", `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			s := server(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); io.WriteString(w, tc.body) })
			p := provider(t, anthropic.Config{BaseURL: s.URL})
			models, err := p.ListModels(context.Background())
			if !gateway.IsCode(err, gateway.InvalidResponse) || models != nil || count.Load() > 2 {
				t.Fatalf("models=%+v err=%v requests=%d", models, err, count.Load())
			}
		})
	}
	t.Run("total bytes across pages", func(t *testing.T) {
		var count atomic.Int32
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			index := count.Add(1)
			fmt.Fprintf(w, `{"data":[{"id":"%d"}],"has_more":true,"last_id":"%d"}`, index, index)
		})
		p := provider(t, anthropic.Config{BaseURL: s.URL, MaxResponseBytes: 80})
		if models, err := p.ListModels(context.Background()); !gateway.IsCode(err, gateway.InvalidResponse) || models != nil || count.Load() != 2 {
			t.Fatalf("models=%+v err=%v requests=%d", models, err, count.Load())
		}
	})
}

func TestCancellationAndCallbackFailure(t *testing.T) {
	for _, callbackFailure := range []bool{true, false} {
		t.Run(fmt.Sprint(callbackFailure), func(t *testing.T) {
			cancelled := make(chan struct{})
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, start+blockStart+delta)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(cancelled)
				case <-time.After(2 * time.Second):
				}
			})
			p := provider(t, anthropic.Config{BaseURL: s.URL})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("consumer stopped")
			terminals := 0
			result, err := p.Stream(ctx, request(), func(e gateway.Event) error {
				if e.Type == gateway.EventError || e.Type == gateway.EventCompleted {
					terminals++
				}
				if e.Type == gateway.EventTextDelta {
					if callbackFailure {
						return sentinel
					}
					cancel()
				}
				return nil
			})
			if callbackFailure {
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
				t.Fatal("upstream cancellation was not propagated")
			}
		})
	}
}

func TestHTTPFailuresTimeoutsAndTLS(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   gateway.ErrorCode
	}{
		{400, gateway.InvalidRequest}, {401, gateway.AuthenticationFailed}, {403, gateway.AuthenticationFailed},
		{429, gateway.RateLimited}, {500, gateway.UpstreamError}, {529, gateway.CapacityUnavailable},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var count atomic.Int32
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("request-id", "safe-id")
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"message":"test-secret private"}`)
			})
			p := provider(t, anthropic.Config{BaseURL: s.URL})
			_, err := p.Chat(context.Background(), request())
			var problem *gateway.Error
			if !gateway.IsCode(err, tc.code) || !errors.As(err, &problem) || problem.UpstreamStatus != tc.status || problem.RequestID != "safe-id" || problem.Provider != "anthropic" || problem.Operation != "chat" || count.Load() != 1 || strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "private") {
				t.Fatalf("err=%+v requests=%d", err, count.Load())
			}
		})
	}
	t.Run("request and streaming timeout", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		})
		p := provider(t, anthropic.Config{BaseURL: s.URL, RequestTimeout: 20 * time.Millisecond, StreamTimeout: 20 * time.Millisecond})
		_, chatErr := p.Chat(context.Background(), request())
		_, streamErr := p.Stream(context.Background(), request(), func(gateway.Event) error { t.Error("pre-admission failure emitted an event"); return nil })
		for _, err := range []error{chatErr, streamErr} {
			if !gateway.IsCode(err, gateway.Timeout) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		}
	})
	t.Run("TLS and isolated client", func(t *testing.T) {
		s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { headers(t, r); io.WriteString(w, chatJSON) }))
		s.Config.ErrorLog = log.New(io.Discard, "", 0)
		s.StartTLS()
		t.Cleanup(s.Close)
		secure := provider(t, anthropic.Config{BaseURL: s.URL})
		if _, err := secure.Chat(context.Background(), request()); err == nil {
			t.Fatal("untrusted certificate accepted")
		}
		client := s.Client()
		original := client.Transport
		trusted := provider(t, anthropic.Config{BaseURL: s.URL, HTTPClient: client})
		if _, err := trusted.Chat(context.Background(), request()); err != nil {
			t.Fatal(err)
		}
		if client.Transport != original || client.CheckRedirect != nil {
			t.Fatal("caller client mutated")
		}
	})
	t.Run("redirect blocked", func(t *testing.T) {
		var reached atomic.Int32
		target := server(t, func(w http.ResponseWriter, r *http.Request) { reached.Add(1) })
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		})
		p := provider(t, anthropic.Config{BaseURL: s.URL})
		if _, err := p.Chat(context.Background(), request()); err == nil || reached.Load() != 0 {
			t.Fatalf("redirect followed: %v", err)
		}
	})
}

func TestCapabilitiesValidationAndGateway(t *testing.T) {
	for _, cfg := range []anthropic.Config{
		{}, {APIKey: "a\nb"}, {APIKey: "key", BaseURL: "ftp://example.com"}, {APIKey: "key", BaseURL: "https://user:secret@example.com"},
		{APIKey: "key", BaseURL: "https://example.com?version=x"}, {APIKey: "key", BaseURL: "https://example.com#fragment"},
		{APIKey: "key", Capabilities: gateway.Capabilities{Embeddings: gateway.Supported}}, {APIKey: "key", MaxResponseBytes: -1},
	} {
		if _, err := anthropic.New(cfg); !gateway.IsCode(err, gateway.InvalidConfiguration) {
			t.Fatalf("invalid config accepted: %v", err)
		}
	}
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"remote"}]}`)
			return
		}
		var body struct {
			Model     string `json:"model"`
			Stream    bool   `json:"stream"`
			MaxTokens int    `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "remote" || body.MaxTokens != 12 {
			t.Error("alias or custom token default lost")
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, start+blockStart+delta+blockStop+finish+stop)
		} else {
			io.WriteString(w, chatJSON)
		}
	})
	caps := map[string]gateway.Capabilities{"remote": {Chat: gateway.Supported, Streaming: gateway.Supported, Tools: gateway.Supported}}
	p := provider(t, anthropic.Config{BaseURL: s.URL, DefaultMaxOutputTokens: 12, Capabilities: gateway.Capabilities{Catalog: gateway.Supported}, ModelCapabilities: caps})
	delete(caps, "remote")
	g, err := gateway.New(gateway.Config{Deployments: map[string]gateway.Provider{"claude": p}, Models: map[string]gateway.ModelRoute{"chat": {Deployment: "claude", Model: "remote"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := request()
	r.Model = "chat"
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Chat(context.Background(), r); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := g.Stream(context.Background(), r, func(gateway.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ListModels(context.Background(), "claude"); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := g.Embed(context.Background(), gateway.EmbedRequest{Model: "chat", Input: []string{"x"}}); !gateway.IsCode(err, gateway.UnsupportedCapability) || calls.Load() != before {
		t.Fatalf("embedding not rejected locally: %v", err)
	}
	r.Tools = []gateway.Tool{{Name: "f", Parameters: json.RawMessage(`{}`)}}
	if _, err := g.Stream(context.Background(), r, func(gateway.Event) error { return nil }); !gateway.IsCode(err, gateway.UnsupportedCapability) || calls.Load() != before {
		t.Fatal(err)
	}
	r = request()
	r.Messages = append(r.Messages, gateway.Message{Role: gateway.RoleSystem, Content: "late"})
	if _, err := p.Chat(context.Background(), r); !gateway.IsCode(err, gateway.UnsupportedCapability) || calls.Load() != before {
		t.Fatal(err)
	}
}

func TestResponseAndEventSizeLimits(t *testing.T) {
	for _, op := range []string{"chat", "stream", "list_models"} {
		t.Run(op, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				if op == "stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: "+strings.Repeat("x", 200)+"\n\n")
				} else {
					io.WriteString(w, strings.Repeat("x", 200))
				}
			})
			p := provider(t, anthropic.Config{BaseURL: s.URL, MaxResponseBytes: 128, MaxEventBytes: 64})
			var err error
			switch op {
			case "chat":
				_, err = p.Chat(context.Background(), request())
			case "stream":
				_, err = p.Stream(context.Background(), request(), func(gateway.Event) error { return nil })
			case "list_models":
				_, err = p.ListModels(context.Background())
			}
			var problem *gateway.Error
			if !gateway.IsCode(err, gateway.InvalidResponse) || !errors.As(err, &problem) || problem.Provider != "anthropic" || problem.Operation != op {
				t.Fatalf("err=%+v", err)
			}
		})
	}
	t.Run("aggregate stream bytes", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, strings.Repeat(frame("ping", `{"type":"ping"}`), 30))
		})
		p := provider(t, anthropic.Config{BaseURL: s.URL, MaxResponseBytes: 512, MaxEventBytes: 64})
		if _, err := p.Stream(context.Background(), request(), func(gateway.Event) error { return nil }); !gateway.IsCode(err, gateway.InvalidResponse) {
			t.Fatal(err)
		}
	})
}
