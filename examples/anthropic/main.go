package main

import (
	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/examples/internal/demo"
	"github.com/raulaguila/ai-gateway/providers/anthropic"
	"log"
	"os"
)

func main() {
	model := demo.Required("GATEWAY_MODEL")
	p, err := anthropic.New(anthropic.Config{APIKey: demo.Required("GATEWAY_API_KEY"), BaseURL: os.Getenv("GATEWAY_BASE_URL"),
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
