// Package demo supplies common terminal interaction for the runnable examples.
package demo

import (
	"context"
	"flag"
	"fmt"
	gateway "github.com/raulaguila/ai-gateway"
	"log"
	"os"
	"time"
)

func Required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("set %s before running this example", name)
	}
	return value
}

func Run(g *gateway.Gateway) error {
	stream := flag.Bool("stream", false, "emit text incrementally")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request := gateway.ChatRequest{System: "Responda em português.", MaxOutputTokens: gateway.Ptr(256),
		Messages: []gateway.Message{gateway.User("Explique brevemente o que é um AI Gateway.")}}
	if *stream {
		_, err := g.StreamText(ctx, request, func(text string) error { _, err := fmt.Print(text); return err })
		fmt.Println()
		return err
	}
	result, err := g.Chat(ctx, request)
	if err == nil {
		fmt.Println(result.Text)
	}
	return err
}
