package gauss

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
)

func TestRollingQuotaAndSharedCooldown(t *testing.T) {
	q := quotaRegistry{states: map[string]*quotaState{}}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if delay := q.reserve("shared", true, now.Add(time.Duration(i)*time.Second)); delay != 0 {
			t.Fatal(delay)
		}
	}
	if delay := q.reserve("shared", true, now.Add(3*time.Second)); delay != 57*time.Second {
		t.Fatal(delay)
	}
	if delay := q.reserve("shared", false, now.Add(3*time.Second)); delay != 0 {
		t.Fatal("catalog consumed quota", delay)
	}
	if delay := q.reserve("other", true, now.Add(3*time.Second)); delay != 0 {
		t.Fatal("scope leaked", delay)
	}
	q.cooldown("shared", now.Add(90*time.Second))
	if delay := q.reserve("shared", false, now.Add(60*time.Second)); delay != 30*time.Second {
		t.Fatal(delay)
	}
	if delay := q.reserve("shared", true, now.Add(90*time.Second)); delay != 0 {
		t.Fatal(delay)
	}
}

func TestThothCatalogFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/thoth-models.json")
	if err != nil {
		t.Fatal(err)
	}
	models, err := decodeModels(data)
	if err != nil || len(models) != 8 {
		t.Fatalf("models=%d err=%v", len(models), err)
	}
	for _, model := range models {
		if model.Name == "" || model.Description == "" || model.ContextWindow == 0 || model.Tools == gateway.Unknown {
			t.Fatalf("Thoth metadata was lost: %+v", model)
		}
		if model.RoutingID == "599" && (model.ServingID != "gausso3-5-awq" || model.Tools != gateway.Supported || model.ContextWindow != 128000) {
			t.Fatalf("routing mismatch: %+v", model)
		}
		if model.RoutingID == "96" && (model.Tools != gateway.Unsupported || model.ContextWindow != 64000) {
			t.Fatalf("capability mismatch: %+v", model)
		}
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if delay := retryDelay(http.Header{}, []byte(`{"nextAccessTime":"2026-Oct-28 12:01:00+0000 UTC"}`), now); delay != 60250*time.Millisecond {
		t.Fatalf("Thoth timestamp delay=%s", delay)
	}
}

func TestConcurrentAdmissionAndIdlePruning(t *testing.T) {
	q := quotaRegistry{states: map[string]*quotaState{}}
	now := time.Now()
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if q.reserve("shared", true, now) == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 3 {
		t.Fatalf("admitted=%d", admitted.Load())
	}
	for i := 0; i < 256; i++ {
		q.states[fmt.Sprint(i)] = &quotaState{lastSeen: now.Add(-2 * time.Minute)}
	}
	q.reserve("new", true, now)
	if len(q.states) != 2 {
		t.Fatalf("idle entries retained: %d", len(q.states))
	}
}

func TestRetryInstructions(t *testing.T) {
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, retry, body string
		want              time.Duration
	}{
		{"fallback", "", `{}`, time.Minute},
		{"seconds", "2", `{}`, 2250 * time.Millisecond},
		{"date", now.Add(3 * time.Second).Format(http.TimeFormat), `{}`, 3250 * time.Millisecond},
		{"next access", "", `{"nextAccessTime":"2026-10-31T10:00:04Z"}`, 4250 * time.Millisecond},
		{"max of instructions", "2", `{"error":{"nextAccessTime":"2026-10-31T10:00:05Z"}}`, 5250 * time.Millisecond},
		{"epoch millis", "", fmt.Sprintf(`{"data":{"nextAccessTime":%d}}`, now.Add(time.Second).UnixMilli()), 1250 * time.Millisecond},
		{"past", "0", `{}`, 250 * time.Millisecond},
		{"invalid", "invalid", `invalid`, time.Minute},
		{"long cooldown", "172800", `{}`, 48*time.Hour + 250*time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("Retry-After", tc.retry)
			h.Set("Date", now.Format(http.TimeFormat))
			if got := retryDelay(h, []byte(tc.body), now.Add(time.Hour)); got != tc.want {
				t.Fatalf("delay=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestReasoningFilterEveryChunkBoundary(t *testing.T) {
	for _, tag := range reasoningTags {
		input := "Antes <" + tag + ">privado</" + tag + "> depois 😀"
		for split := 0; split <= len(input); split++ {
			var filter visibleFilter
			a, err := filter.write(input[:split])
			if err != nil {
				t.Fatal(err)
			}
			b, err := filter.write(input[split:])
			if err != nil {
				t.Fatal(err)
			}
			filter.finish()
			if a+b != "Antes  depois 😀" {
				t.Fatalf("tag=%s split=%d text=%q", tag, split, a+b)
			}
		}
	}
	for _, tc := range []struct{ input, want string }{
		{"a<THINK><analysis>secret</analysis>more</THINK>b", "ab"},
		{"a<think>unterminated", "a"},
		{"a<thi", "a"},
		{"1 < 2 <unknown>normal</unknown>", "1 < 2 <unknown>normal</unknown>"},
	} {
		var filter visibleFilter
		var text strings.Builder
		for i := range tc.input {
			piece, err := filter.write(tc.input[i : i+1])
			if err != nil {
				t.Fatal(err)
			}
			text.WriteString(piece)
		}
		filter.finish()
		if text.String() != tc.want {
			t.Fatalf("text=%q want=%q", text.String(), tc.want)
		}
	}
}

func TestCatalogShapesAndRoutingValidation(t *testing.T) {
	row := `{"modelGuid":"g","modelId":9,"modelServingId":"s","name":{"pt":"Primeiro","es":"Segundo"},"description":"Tool Calling: X\nContext Length: 32k"}`
	for _, body := range []string{"[" + row + "]", `{"data":[` + row + `]}`, `{"models":[` + row + `]}`, `{"data":{"models":[` + row + `]}}`} {
		models, err := decodeModels([]byte(body))
		if err != nil || len(models) != 1 || models[0].GUID != "g" || models[0].RoutingID != "9" || models[0].Name != "Primeiro" || models[0].ContextWindow != 32000 {
			t.Fatalf("models=%+v err=%v", models, err)
		}
	}
	for _, body := range []string{`null`, `{}`, `{"data":null}`, `{"data":[{"modelGuid":"g","modelId":0,"modelServingId":"s"}]}`, `{"data":[],"has_more":true}`, `{"data":[],"error":{"message":"secret"}}`} {
		if _, err := decodeModels([]byte(body)); err == nil {
			t.Fatalf("invalid catalog accepted: %s", body)
		}
	}
}

func TestScopeNormalizationAndNextAccessHeader(t *testing.T) {
	a, err := New(Config{BaseURL: "https://EXAMPLE.com:443/api-chat/openapi/llm/v1/", APIKey: `{"openapi_token":"Bearer token","client_token":"a"}`})
	if err != nil {
		t.Fatal(err)
	}
	defer a.CloseIdleConnections()
	b, err := New(Config{BaseURL: "https://example.com/api-llm", APIKey: `{"openapi_token":"token","client_token":"b"}`})
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseIdleConnections()
	if a.scope != b.scope {
		t.Fatal("equivalent subscriptions did not share scope")
	}
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	headers := http.Header{}
	headers.Set("Date", now.Format(http.TimeFormat))
	headers.Set("nextAccessTime", now.Add(5*time.Second).Format(time.RFC3339))
	if delay := retryDelay(headers, nil, now.Add(time.Hour)); delay != 5250*time.Millisecond {
		t.Fatal(delay)
	}
}
