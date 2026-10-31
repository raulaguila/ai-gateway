package main

import (
	"context"
	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/examples/internal/demo"
	"github.com/raulaguila/ai-gateway/providers/gauss"
	"log"
	"os"
	"time"
)

func main() {
	guid := demo.Required("GATEWAY_MODEL")
	p, err := gauss.New(gauss.Config{BaseURL: os.Getenv("GATEWAY_BASE_URL"),
		Credentials: gauss.Credentials{OpenAPIToken: demo.Required("GAUSS_OPENAPI_TOKEN"), ClientToken: demo.Required("GAUSS_CLIENT_TOKEN")},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer p.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := p.RefreshModels(ctx); err != nil {
		log.Fatal(err)
	}
	g, err := gateway.NewSingle(p, guid)
	if err != nil {
		log.Fatal(err)
	}
	if err := demo.Run(g); err != nil {
		log.Fatal(err)
	}
}
