// Package gateway defines provider-neutral AI operations and explicit model routing.
package gateway

import "context"

// Support describes a model capability, not merely an adapter implementation.
type Support string

const (
	Unknown     Support = ""
	Supported   Support = "supported"
	Unsupported Support = "unsupported"
)

type Capabilities struct {
	Chat       Support
	Streaming  Support
	Tools      Support
	Embeddings Support
	Catalog    Support
	// Source identifies the evidence, for example "configuration" or "test".
	Source string
}

// Provider is the registration contract. Capabilities must not perform network I/O.
// Implementations must be safe for concurrent calls after construction.
type Provider interface {
	Capabilities(model string) Capabilities
}

// ChatRequestValidator optionally checks local provider prerequisites. It must
// perform no I/O, must not mutate the request and must be safe for concurrent use.
// The gateway calls it with the remote model and effective output default.
type ChatRequestValidator interface {
	ValidateChatRequest(ChatRequest) error
}

// TextChatCapabilities explicitly declares text chat and streaming support.
// Use only for models verified by the application; other capabilities stay unknown.
func TextChatCapabilities() Capabilities {
	return Capabilities{Chat: Supported, Streaming: Supported, Source: "configuration"}
}

// ChatWithToolsCapabilities also declares tools for non-streaming chat.
func ChatWithToolsCapabilities() Capabilities {
	c := TextChatCapabilities()
	c.Tools = Supported
	return c
}

type ChatProvider interface {
	Provider
	Chat(context.Context, ChatRequest) (ChatResult, error)
}

type StreamProvider interface {
	Provider
	Stream(context.Context, ChatRequest, func(Event) error) (ChatResult, error)
}

type EmbeddingProvider interface {
	Provider
	Embed(context.Context, EmbedRequest) (EmbedResult, error)
}

type CatalogProvider interface {
	Provider
	ListModels(context.Context) ([]Model, error)
}

type Model struct {
	ID           string
	Capabilities Capabilities
}
