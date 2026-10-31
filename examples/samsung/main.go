package main

import (
	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/examples/internal/demo"
	"github.com/raulaguila/ai-gateway/providers/samsung"
	"log"
	"os"
)

func main() {
	flow := demo.Required("GATEWAY_MODEL")
	p, err := samsung.New(samsung.Config{BaseURL: demo.Required("GATEWAY_BASE_URL"), APIKey: os.Getenv("GATEWAY_API_KEY"),
		AgentKey: flow, LLMComponentID: demo.Required("SAMSUNG_LLM_COMPONENT_ID"), ChatInputID: os.Getenv("SAMSUNG_CHAT_INPUT_ID"),
		ChatOutputID: os.Getenv("SAMSUNG_CHAT_OUTPUT_ID"), MemoryComponentID: os.Getenv("SAMSUNG_MEMORY_COMPONENT_ID"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer p.CloseIdleConnections()
	g, err := gateway.NewSingle(p, flow)
	if err != nil {
		log.Fatal(err)
	}
	if err := demo.Run(g); err != nil {
		log.Fatal(err)
	}
}
