// Package samsung implements Samsung Agent Builder's Langflow run protocol.
package samsung

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/internal/transport"
)

type Config struct {
	BaseURL, APIKey                                              string
	AgentKey                                                     string
	ChatInputID, ChatOutputID, LLMComponentID, MemoryComponentID string
	InputType, OutputType                                        string
	UserName                                                     string
	// SendHistory defaults to true. False serializes only the last user message.
	SendHistory                   *bool
	HTTPClient                    *http.Client
	RequestTimeout, StreamTimeout time.Duration
	MaxResponseBytes              int64
	MaxLineBytes                  int
}

type Provider struct {
	baseURL, apiKey, agentKey                  string
	chatInputID, chatOutputID, llmID, memoryID string
	inputType, outputType, userName            string
	sendHistory                                bool
	client                                     *http.Client
	requestTimeout, streamTimeout              time.Duration
	maxResponseBytes                           int64
	maxLineBytes                               int
}

var (
	_ gateway.ChatProvider    = (*Provider)(nil)
	_ gateway.StreamProvider  = (*Provider)(nil)
	_ gateway.CatalogProvider = (*Provider)(nil)
)

func New(config Config) (*Provider, error) {
	bad := func(message string) (*Provider, error) {
		return nil, failure(gateway.InvalidConfiguration, "configure", message, nil)
	}
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(config.BaseURL, "#") || base.Opaque != "" {
		return bad("base URL must be HTTP(S) without credentials, query or fragment")
	}
	if strings.ContainsAny(config.APIKey, "\r\n") {
		return bad("API key must not contain line breaks")
	}
	config.AgentKey = strings.TrimSpace(config.AgentKey)
	if config.AgentKey != "" && !validAgent(config.AgentKey) {
		return bad("agent key must be a single nonempty path segment")
	}
	ids := []*string{&config.ChatInputID, &config.ChatOutputID, &config.LLMComponentID, &config.MemoryComponentID}
	seen := map[string]bool{}
	for _, id := range ids {
		*id = strings.TrimSpace(*id)
		if *id == "" {
			continue
		}
		if seen[*id] {
			return bad("component IDs must be distinct")
		}
		seen[*id] = true
	}
	if config.RequestTimeout < 0 || config.StreamTimeout < 0 || config.MaxResponseBytes < 0 || config.MaxResponseBytes == math.MaxInt64 || config.MaxLineBytes < 0 || config.MaxLineBytes > math.MaxInt-2 {
		return bad("timeouts and response limits are invalid")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 5 * time.Minute
	}
	if config.StreamTimeout == 0 {
		config.StreamTimeout = 30 * time.Minute
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 8 << 20
	}
	if config.MaxLineBytes == 0 {
		config.MaxLineBytes = int(min(4<<20, config.MaxResponseBytes))
	}
	if int64(config.MaxLineBytes) > config.MaxResponseBytes {
		return bad("line limit must not exceed response limit")
	}
	client, err := transport.NewClient(config.HTTPClient, false)
	if err != nil {
		return bad(err.Error())
	}
	history := true
	if config.SendHistory != nil {
		history = *config.SendHistory
	}
	input, output := strings.TrimSpace(config.InputType), strings.TrimSpace(config.OutputType)
	if input == "" {
		input = "chat"
	}
	if output == "" {
		output = "chat"
	}
	return &Provider{baseURL: strings.TrimRight(base.String(), "/"), apiKey: strings.TrimSpace(config.APIKey), agentKey: config.AgentKey,
		chatInputID: config.ChatInputID, chatOutputID: config.ChatOutputID, llmID: config.LLMComponentID, memoryID: config.MemoryComponentID,
		inputType: input, outputType: output, userName: config.UserName, sendHistory: history, client: client,
		requestTimeout: config.RequestTimeout, streamTimeout: config.StreamTimeout, maxResponseBytes: config.MaxResponseBytes, maxLineBytes: config.MaxLineBytes}, nil
}

func validAgent(id string) bool {
	return strings.TrimSpace(id) != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\?#%\r\n")
}

func (p *Provider) Capabilities(model string) gateway.Capabilities {
	caps := gateway.Capabilities{Tools: gateway.Unsupported, Embeddings: gateway.Unsupported, Catalog: gateway.Supported, Source: "configuration"}
	if model == "" {
		model = p.agentKey
	}
	if validAgent(model) {
		caps.Chat, caps.Streaming = gateway.Supported, gateway.Supported
	}
	return caps
}

// ListModels is local configuration, not a connectivity or credential check.
func (p *Provider) ListModels(ctx context.Context) ([]gateway.Model, error) {
	if ctx == nil {
		return nil, failure(gateway.InvalidRequest, "list_models", "context is required", nil)
	}
	if ctx.Err() != nil {
		return nil, ioFailure(ctx, "list_models", ctx.Err())
	}
	if p.agentKey == "" {
		return []gateway.Model{}, nil
	}
	return []gateway.Model{{ID: p.agentKey, Capabilities: p.Capabilities(p.agentKey)}}, nil
}

func (p *Provider) operationContext(ctx context.Context, stream bool) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, failure(gateway.InvalidRequest, "", "context is required", nil)
	}
	timeout := p.requestTimeout
	if stream {
		timeout = p.streamTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	return bounded, cancel, nil
}

func (p *Provider) CloseIdleConnections() { p.client.CloseIdleConnections() }
