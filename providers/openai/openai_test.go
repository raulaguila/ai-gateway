package openai_test

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
	"github.com/raulaguila/ai-gateway/providers/openai"
)

const chatJSON = `{"id":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"Olá"},"finish_reason":"stop"}]}`
const delta = "data: {\"id\":\"s\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Olá\"},\"finish_reason\":null}]}\n\n"
const finish = "data: {\"id\":\"s\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
const done = "data: [DONE]\n\n"

func request() gateway.ChatRequest {
	return gateway.ChatRequest{Model: "remote", Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "Oi"}}}
}

func provider(t *testing.T, cfg openai.Config) *openai.Provider {
	t.Helper()
	p, err := openai.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.CloseIdleConnections)
	return p
}

func server(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

func TestChatWireParametersAndTools(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/prefix/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if string(body["model"]) != `"remote"` || string(body["stream"]) != "false" || body["stream_options"] != nil {
					t.Errorf("body=%s", body)
				}
				if explicit {
					if string(body["temperature"]) != "0" || string(body["top_p"]) != "0" || string(body["max_completion_tokens"]) != "32" || body["max_tokens"] != nil {
						t.Errorf("parameters=%s", body)
					}
				} else if body["temperature"] != nil || body["top_p"] != nil || body["max_completion_tokens"] != nil {
					t.Errorf("absent parameters transmitted")
				}
				var messages []map[string]json.RawMessage
				_ = json.Unmarshal(body["messages"], &messages)
				if len(messages) != 2 || string(messages[0]["role"]) != `"system"` {
					t.Errorf("messages=%s", body["messages"])
				}
				io.WriteString(w, chatJSON)
			})
			p := provider(t, openai.Config{BaseURL: s.URL + "/prefix/v1///", APIKey: "test-secret", MaxTokensField: "max_completion_tokens"})
			r := request()
			r.System = "system"
			if explicit {
				zero, max := 0.0, 32
				r.Temperature, r.TopP, r.MaxOutputTokens = &zero, &zero, &max
			}
			result, err := p.Chat(context.Background(), r)
			if err != nil || result.Text != "Olá" || !result.ProtocolComplete || result.Usage.Input.Tokens != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	t.Run("tool round trip", func(t *testing.T) {
		var calls atomic.Int32
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if calls.Add(1) == 1 {
				if body["tools"] == nil || string(body["tool_choice"]) != `"auto"` {
					t.Error("tools not serialized")
				}
				io.WriteString(w, `{"choices":[{"index":0,"message":{"content":null,"tool_calls":[{"id":"call-a","type":"function","function":{"name":"lookup","arguments":"{\"id\":1}"}},{"id":"call-b","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":0,"completion_tokens":3}}`)
			} else {
				var messages []struct {
					Role string `json:"role"`
					ID   string `json:"tool_call_id"`
				}
				_ = json.Unmarshal(body["messages"], &messages)
				if len(messages) != 4 || messages[2].ID != "call-a" || messages[3].ID != "call-b" {
					t.Errorf("history=%s", body["messages"])
				}
				io.WriteString(w, chatJSON)
			}
		})
		p := provider(t, openai.Config{BaseURL: s.URL})
		r := request()
		r.Tools = []gateway.Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}
		result, err := p.Chat(context.Background(), r)
		if err != nil || len(result.ToolCalls) != 2 || result.FinishReason != gateway.FinishToolUse || result.Usage.Input.Tokens == nil || *result.Usage.Input.Tokens != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		r.Messages = append(r.Messages, gateway.Message{Role: gateway.RoleAssistant, ToolCalls: result.ToolCalls}, gateway.Message{Role: gateway.RoleTool, ToolCallID: "call-a", Content: "a"}, gateway.Message{Role: gateway.RoleTool, ToolCallID: "call-b", Content: "b"})
		if _, err := p.Chat(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStreamingTerminalsAndFraming(t *testing.T) {
	multiline := "data: {\"id\":\"s\",\"choices\":[\ndata: {\"index\":0,\"delta\":{\"content\":\"Olá\"},\"finish_reason\":null}]}\n\n"
	usage := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"
	for _, tc := range []struct {
		name, frames string
		want         gateway.ErrorCode
		text         string
		usage        bool
	}{
		{"multiline and late usage", ": comment\n\n" + multiline + finish + usage + done, "", "Olá", true},
		{"finish then EOF", delta + finish, "", "Olá", false},
		{"done marker", delta + done, "", "Olá", false},
		{"premature EOF", delta, gateway.StreamIncomplete, "Olá", false},
		{"unterminated final event", delta + "data: [DONE]\n", gateway.StreamIncomplete, "Olá", false},
		{"invalid JSON", delta + "data: invalid\n\n", gateway.InvalidResponse, "Olá", false},
		{"late invalid JSON", delta + finish + "data: invalid\n\n", gateway.InvalidResponse, "Olá", false},
		{"remote error", delta + "event: error\ndata: {\"secret\":\"do not expose\"}\n\n", gateway.UpstreamError, "Olá", false},
		{"no choices", done, gateway.InvalidResponse, "", false},
		{"unsolicited tool", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{}]}}]}\n\n", gateway.UnsupportedCapability, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["stream_options"] == nil {
					t.Error("stream usage flag missing")
				}
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				io.WriteString(w, tc.frames)
			})
			p := provider(t, openai.Config{BaseURL: s.URL, IncludeStreamUsage: true})
			var sequence uint64
			var text string
			var terminals int
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
				if e.Usage != nil && e.Usage.Input.Tokens != nil {
					*e.Usage.Input.Tokens = 999
				} // Consumer owns snapshots.
				if e.Error != nil {
					e.Error.Code = "mutated"
				}
				return nil
			})
			if tc.want == "" && err != nil || tc.want != "" && !gateway.IsCode(err, tc.want) {
				t.Fatalf("err=%v want=%s", err, tc.want)
			}
			if result.Text != tc.text || text != tc.text || terminals != 1 || result.ProtocolComplete != (err == nil) {
				t.Fatalf("result=%+v text=%s terminals=%d", result, text, terminals)
			}
			if tc.usage && (result.Usage.Input.Tokens == nil || *result.Usage.Input.Tokens != 5) {
				t.Fatalf("usage=%+v", result.Usage)
			}
			if err != nil && strings.Contains(err.Error(), "do not expose") {
				t.Fatal("remote body leaked")
			}
		})
	}
}

func TestStreamCallbackAndCancellation(t *testing.T) {
	for _, callbackFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(callbackFailure), func(t *testing.T) {
			upstreamCancelled := make(chan struct{})
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, delta)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(upstreamCancelled)
				case <-time.After(2 * time.Second):
				}
			})
			p := provider(t, openai.Config{BaseURL: s.URL})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("consumer stopped")
			events := 0
			result, err := p.Stream(ctx, request(), func(e gateway.Event) error {
				events++
				if e.Type == gateway.EventTextDelta {
					if callbackFailure {
						return sentinel
					}
					cancel()
				}
				return nil
			})
			if callbackFailure {
				if !errors.Is(err, sentinel) || !gateway.IsCode(err, gateway.CallbackFailed) || events != 1 {
					t.Fatalf("events=%d err=%v", events, err)
				}
			} else if !errors.Is(err, context.Canceled) || !gateway.IsCode(err, gateway.Cancelled) || events != 2 {
				t.Fatalf("events=%d err=%v", events, err)
			}
			if result.Text != "Olá" || result.ProtocolComplete {
				t.Fatalf("partial=%+v", result)
			}
			select {
			case <-upstreamCancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream was not cancelled")
			}
		})
	}
}

func TestEmbeddingsValidationAndOrdering(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		valid          bool
	}{
		{"reordered", `{"model":"remote","data":[{"index":1,"embedding":[3,4]},{"index":0,"embedding":[1,2]}],"usage":{"prompt_tokens":2}}`, true},
		{"duplicate", `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`, false},
		{"out of range", `{"data":[{"index":0,"embedding":[1]},{"index":2,"embedding":[2]}]}`, false},
		{"missing index", `{"data":[{"embedding":[1]},{"index":1,"embedding":[2]}]}`, false},
		{"dimension change", `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[2,3]}]}`, false},
		{"empty vector", `{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[2]}]}`, false},
		{"null component", `{"data":[{"index":0,"embedding":[null]},{"index":1,"embedding":[2]}]}`, false},
		{"incomplete", `{"data":[{"index":0,"embedding":[1]}]}`, false},
		{"overflow", `{"data":[{"index":0,"embedding":[1e999]},{"index":1,"embedding":[2]}]}`, false},
		{"NaN", `{"data":[{"index":0,"embedding":[NaN]},{"index":1,"embedding":[2]}]}`, false},
		{"infinity", `{"data":[{"index":0,"embedding":[Infinity]},{"index":1,"embedding":[2]}]}`, false},
		{"wrong model", `{"model":"different","data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[2]}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected request")
				}
				io.WriteString(w, tc.response)
			})
			p := provider(t, openai.Config{BaseURL: s.URL + "/v1"})
			result, err := p.Embed(context.Background(), gateway.EmbedRequest{Model: "remote", Input: []string{"a", "b"}})
			if tc.valid {
				if err != nil || result.Vectors[0][0] != 1 || result.Vectors[1][0] != 3 || result.Usage.Output.Tokens != nil {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if !gateway.IsCode(err, gateway.InvalidResponse) || len(result.Vectors) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestHTTPFailuresAreSanitizedAndNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   gateway.ErrorCode
	}{
		{400, gateway.InvalidRequest}, {401, gateway.AuthenticationFailed}, {403, gateway.AuthenticationFailed},
		{404, gateway.UpstreamError}, {408, gateway.Timeout}, {429, gateway.RateLimited}, {500, gateway.UpstreamError},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var count atomic.Int32
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("X-Request-Id", "safe-id")
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":"secret-value and private prompt"}`)
			})
			p := provider(t, openai.Config{BaseURL: s.URL, APIKey: "secret-value"})
			_, err := p.Chat(context.Background(), request())
			var problem *gateway.Error
			if !gateway.IsCode(err, tc.want) || !errors.As(err, &problem) || problem.UpstreamStatus != tc.status || problem.RequestID != "safe-id" || count.Load() != 1 {
				t.Fatalf("error=%v count=%d", err, count.Load())
			}
			if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "private prompt") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestTLSIsolationAllOperations(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"remote"}]}`)
		case "/v1/embeddings":
			io.WriteString(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
		case "/v1/chat/completions":
			var body struct {
				Stream bool `json:"stream"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, delta+finish+done)
			} else {
				io.WriteString(w, chatJSON)
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	secure := provider(t, openai.Config{BaseURL: s.URL + "/v1"})
	insecure := provider(t, openai.Config{BaseURL: s.URL + "/v1", InsecureTLS: true})
	for _, op := range []struct {
		name string
		run  func(*openai.Provider) error
	}{
		{"chat", func(p *openai.Provider) error { _, err := p.Chat(context.Background(), request()); return err }},
		{"stream", func(p *openai.Provider) error {
			_, err := p.Stream(context.Background(), request(), func(gateway.Event) error { return nil })
			return err
		}},
		{"embed", func(p *openai.Provider) error {
			_, err := p.Embed(context.Background(), gateway.EmbedRequest{Model: "remote", Input: []string{"a"}})
			return err
		}},
		{"catalog", func(p *openai.Provider) error { _, err := p.ListModels(context.Background()); return err }},
	} {
		t.Run(op.name, func(t *testing.T) {
			if err := op.run(secure); err == nil {
				t.Fatal("untrusted certificate accepted by default")
			}
			if err := op.run(insecure); err != nil {
				t.Fatal(err)
			}
			if err := op.run(secure); err == nil {
				t.Fatal("insecure setting leaked to another provider")
			}
		})
	}
	client := s.Client()
	original := client.Transport.(*http.Transport)
	custom := provider(t, openai.Config{BaseURL: s.URL + "/v1", HTTPClient: client, InsecureTLS: true})
	if _, err := custom.Chat(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if original.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("caller transport was mutated")
	}
}

func TestTimeoutAndRedirect(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		})
		p := provider(t, openai.Config{BaseURL: s.URL, RequestTimeout: 25 * time.Millisecond})
		_, err := p.Chat(context.Background(), request())
		if !gateway.IsCode(err, gateway.Timeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("redirect blocked", func(t *testing.T) {
		var reached atomic.Int32
		target := server(t, func(w http.ResponseWriter, r *http.Request) { reached.Add(1); io.WriteString(w, chatJSON) })
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		})
		p := provider(t, openai.Config{BaseURL: s.URL, APIKey: "secret"})
		if _, err := p.Chat(context.Background(), request()); err == nil || reached.Load() != 0 {
			t.Fatalf("redirect followed: err=%v", err)
		}
	})
}

func TestInvalidResponsesAndBounds(t *testing.T) {
	for _, body := range []string{
		`{`, `null`, `{"choices":[]}`, chatJSON + `{}`,
		`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`,
		`{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1}}`,
		`{"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"[]"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			p := provider(t, openai.Config{BaseURL: s.URL})
			result, err := p.Chat(context.Background(), request())
			if !gateway.IsCode(err, gateway.InvalidResponse) || result.ProtocolComplete {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint("size ", streaming), func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, r *http.Request) {
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: "+strings.Repeat("x", 200)+"\n\n")
				} else {
					io.WriteString(w, strings.Repeat("x", 200))
				}
			})
			p := provider(t, openai.Config{BaseURL: s.URL, MaxResponseBytes: 128, MaxEventBytes: 64})
			var err error
			if streaming {
				_, err = p.Stream(context.Background(), request(), func(gateway.Event) error { return nil })
			} else {
				_, err = p.Chat(context.Background(), request())
			}
			if !gateway.IsCode(err, gateway.InvalidResponse) {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigurationCatalogAndGatewayIntegration(t *testing.T) {
	for _, base := range []string{"", "ftp://example.com", "http://user:password@example.com", "http://example.com?api-version=x", "http://example.com#fragment"} {
		if _, err := openai.New(openai.Config{BaseURL: base}); !gateway.IsCode(err, gateway.InvalidConfiguration) {
			t.Fatalf("accepted invalid base %q: %v", base, err)
		}
	}
	caps := gateway.Capabilities{Chat: gateway.Supported, Streaming: gateway.Supported, Embeddings: gateway.Supported, Catalog: gateway.Supported, Tools: gateway.Unsupported, Source: "test"}
	modelCaps := map[string]gateway.Capabilities{"remote": caps}
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"remote"},{"id":"unknown"}]}`)
		case "/v1/chat/completions":
			var body struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model != "remote" {
				t.Error("alias was not resolved")
			}
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, delta+done)
			} else {
				io.WriteString(w, chatJSON)
			}
		case "/v1/embeddings":
			io.WriteString(w, `{"model":"remote","data":[{"index":0,"embedding":[1]}]}`)
		default:
			t.Error("unexpected operation")
		}
	})
	p := provider(t, openai.Config{BaseURL: s.URL + "/v1", Capabilities: gateway.Capabilities{Catalog: gateway.Supported}, ModelCapabilities: modelCaps})
	delete(modelCaps, "remote")
	g, err := gateway.New(gateway.Config{Deployments: map[string]gateway.Provider{"local": p}, Models: map[string]gateway.ModelRoute{"chat": {Deployment: "local", Model: "remote"}}})
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
	if _, err := g.Embed(context.Background(), gateway.EmbedRequest{Model: "chat", Input: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	models, err := g.ListModels(context.Background(), "local")
	if err != nil || len(models) != 2 || models[0].Capabilities.Chat != gateway.Supported || models[1].Capabilities.Chat != gateway.Unknown {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	r.Tools = []gateway.Tool{{Name: "f", Parameters: json.RawMessage(`{}`)}}
	if _, err := g.Chat(context.Background(), r); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
	if _, err := p.Stream(context.Background(), r, func(gateway.Event) error { return nil }); !gateway.IsCode(err, gateway.UnsupportedCapability) {
		t.Fatal(err)
	}
}
