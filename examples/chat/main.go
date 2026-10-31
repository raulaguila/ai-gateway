package main

import (
	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/examples/internal/demo"
	"github.com/raulaguila/ai-gateway/providers/openai"
	"log"
	"os"
)

func main() {
	base := os.Getenv("GATEWAY_BASE_URL")
	if base == "" {
		base = "http://localhost:11434/v1"
	}
	model := demo.Required("GATEWAY_MODEL")
	p, err := openai.New(openai.Config{BaseURL: base, APIKey: os.Getenv("GATEWAY_API_KEY"),
		ModelCapabilities: map[string]gateway.Capabilities{model: gateway.TextChatCapabilities()},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer p.CloseIdleConnections()
	g, err := gateway.NewSingle(p, model)
	if err != nil {
		log.Fatal(err)
	}
	if err := demo.Run(g); err != nil {
		log.Fatal(err)
	}
}
