package gateway_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/providers/openai"
)

func ExampleNewSingle() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Olá"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	p, err := openai.New(openai.Config{BaseURL: server.URL, ModelCapabilities: map[string]gateway.Capabilities{"local": gateway.TextChatCapabilities()}})
	if err != nil {
		panic(err)
	}
	defer p.CloseIdleConnections()
	client, err := gateway.NewSingle(p, "local", gateway.TokenPolicy{ContextWindow: 100, DefaultMaxOutputTokens: 10, SafetyMarginTokens: 5, SafetyMarginPercent: 10})
	if err != nil {
		panic(err)
	}
	request := gateway.ChatRequest{Messages: []gateway.Message{gateway.User("Olá")}}
	budget, err := client.EstimateBudget(context.Background(), request)
	if err != nil {
		panic(err)
	}
	fmt.Printf("input=%d output=%d total=%d\n", *budget.InputTokens, budget.ReservedOutputTokens, *budget.TotalTokens)
	result, err := client.Chat(context.Background(), request)
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Text)
	// Output:
	// input=11 output=10 total=28
	// Olá
}

func ExampleGateway_StreamText() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Olá\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	p, err := openai.New(openai.Config{BaseURL: server.URL, ModelCapabilities: map[string]gateway.Capabilities{"local": gateway.TextChatCapabilities()}})
	if err != nil {
		panic(err)
	}
	defer p.CloseIdleConnections()
	client, err := gateway.NewSingle(p, "local")
	if err != nil {
		panic(err)
	}
	result, err := client.StreamText(context.Background(), gateway.ChatRequest{Messages: []gateway.Message{gateway.User("Olá")}, MaxOutputTokens: gateway.Ptr(10)}, func(text string) error {
		fmt.Println(text)
		return nil
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.ProtocolComplete)
	// Output:
	// Olá
	// true
}
