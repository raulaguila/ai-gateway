// Package openai implements the textual Chat Completions compatibility profile.
package openai

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/internal/transport"
)

type Config struct {
	// BaseURL includes any required prefix, such as http://localhost:11434/v1.
	BaseURL string
	APIKey  string
	// InsecureTLS affects only this provider's HTTP transport.
	InsecureTLS bool
	HTTPClient  *http.Client
	// Capabilities are explicit evidence supplied by the caller. Defaults are unknown.
	Capabilities      gateway.Capabilities
	ModelCapabilities map[string]gateway.Capabilities
	// MaxTokensField is max_tokens (default) or max_completion_tokens.
	MaxTokensField string
	// IncludeStreamUsage is opt-in because not every compatible server accepts it.
	IncludeStreamUsage bool
	RequestTimeout     time.Duration
	StreamTimeout      time.Duration
	MaxResponseBytes   int64
	MaxEventBytes      int
}

type Provider struct {
	baseURL                       string
	apiKey                        string
	client                        *http.Client
	capabilities                  gateway.Capabilities
	models                        map[string]gateway.Capabilities
	maxTokensField                string
	includeStreamUsage            bool
	requestTimeout, streamTimeout time.Duration
	maxResponseBytes              int64
	maxEventBytes                 int
}

var (
	_ gateway.ChatProvider      = (*Provider)(nil)
	_ gateway.StreamProvider    = (*Provider)(nil)
	_ gateway.EmbeddingProvider = (*Provider)(nil)
	_ gateway.CatalogProvider   = (*Provider)(nil)
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
	if config.MaxTokensField == "" {
		config.MaxTokensField = "max_tokens"
	}
	if config.MaxTokensField != "max_tokens" && config.MaxTokensField != "max_completion_tokens" {
		return bad("invalid max tokens field")
	}
	if config.RequestTimeout < 0 || config.StreamTimeout < 0 || config.MaxResponseBytes < 0 || config.MaxEventBytes < 0 {
		return bad("timeouts and response limits must be positive")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = time.Minute
	}
	if config.StreamTimeout == 0 {
		config.StreamTimeout = 30 * time.Minute
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 8 << 20
	}
	if config.MaxEventBytes == 0 {
		config.MaxEventBytes = 1 << 20
	}
	if int64(config.MaxEventBytes) > config.MaxResponseBytes {
		return bad("event limit must not exceed response limit")
	}
	if !validCapabilities(config.Capabilities) {
		return bad("invalid capability declaration")
	}
	models := make(map[string]gateway.Capabilities, len(config.ModelCapabilities))
	for name, caps := range config.ModelCapabilities {
		if strings.TrimSpace(name) == "" || !validCapabilities(caps) {
			return bad("invalid model capability declaration")
		}
		models[name] = caps
	}
	client, err := transport.NewClient(config.HTTPClient, config.InsecureTLS)
	if err != nil {
		return bad(err.Error())
	}
	return &Provider{
		baseURL: strings.TrimRight(base.String(), "/"), apiKey: strings.TrimSpace(config.APIKey), client: client,
		capabilities: config.Capabilities, models: models, maxTokensField: config.MaxTokensField,
		includeStreamUsage: config.IncludeStreamUsage, requestTimeout: config.RequestTimeout,
		streamTimeout: config.StreamTimeout, maxResponseBytes: config.MaxResponseBytes, maxEventBytes: config.MaxEventBytes,
	}, nil
}

func validCapabilities(c gateway.Capabilities) bool {
	for _, state := range []gateway.Support{c.Chat, c.Streaming, c.Tools, c.Embeddings, c.Catalog} {
		if state != gateway.Unknown && state != gateway.Supported && state != gateway.Unsupported {
			return false
		}
	}
	return true
}

func (p *Provider) Capabilities(model string) gateway.Capabilities {
	if caps, ok := p.models[model]; ok {
		return caps
	}
	return p.capabilities
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
