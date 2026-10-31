// Package anthropic implements the textual Anthropic Messages API.
package anthropic

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

// APIVersion pins the contract described in the project's provider specification.
// In particular, newer versions can remove temperature and top_p parameters.
const APIVersion = "2023-06-01"

type Config struct {
	// BaseURL defaults to https://api.anthropic.com. A final /v1 is accepted.
	BaseURL           string
	APIKey            string
	HTTPClient        *http.Client
	Capabilities      gateway.Capabilities
	ModelCapabilities map[string]gateway.Capabilities
	// DefaultMaxOutputTokens defaults to 4096; each request can override it.
	DefaultMaxOutputTokens        int
	RequestTimeout, StreamTimeout time.Duration
	MaxResponseBytes              int64
	MaxEventBytes                 int
}

type Provider struct {
	baseURL, apiKey               string
	client                        *http.Client
	capabilities                  gateway.Capabilities
	models                        map[string]gateway.Capabilities
	defaultMaxOutputTokens        int
	requestTimeout, streamTimeout time.Duration
	maxResponseBytes              int64
	maxEventBytes                 int
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
	if config.BaseURL == "" {
		config.BaseURL = "https://api.anthropic.com"
	}
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(config.BaseURL, "#") || base.Opaque != "" {
		return bad("base URL must be HTTP(S) without credentials, query or fragment")
	}
	if strings.TrimSpace(config.APIKey) == "" || strings.ContainsAny(config.APIKey, "\r\n") {
		return bad("API key is required and must not contain line breaks")
	}
	if config.DefaultMaxOutputTokens < 0 || config.RequestTimeout < 0 || config.StreamTimeout < 0 || config.MaxResponseBytes < 0 || config.MaxEventBytes < 0 || config.MaxResponseBytes == math.MaxInt64 || config.MaxEventBytes > math.MaxInt-2 {
		return bad("timeouts, token defaults and response limits are invalid")
	}
	if config.DefaultMaxOutputTokens == 0 {
		config.DefaultMaxOutputTokens = 4096
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
		config.MaxEventBytes = min(1<<20, int(min(config.MaxResponseBytes, int64(math.MaxInt-2))))
	}
	if int64(config.MaxEventBytes) > config.MaxResponseBytes {
		return bad("event limit must not exceed response limit")
	}
	if !validCapabilities(config.Capabilities) {
		return bad("invalid capabilities or unsupported embedding declaration")
	}
	models := make(map[string]gateway.Capabilities, len(config.ModelCapabilities))
	for model, caps := range config.ModelCapabilities {
		if strings.TrimSpace(model) == "" || !validCapabilities(caps) {
			return bad("invalid model capabilities")
		}
		caps.Embeddings = gateway.Unsupported
		models[model] = caps
	}
	config.Capabilities.Embeddings = gateway.Unsupported
	client, err := transport.NewClient(config.HTTPClient, false)
	if err != nil {
		return bad(err.Error())
	}
	base.Path = strings.TrimSuffix(strings.TrimRight(base.Path, "/"), "/v1")
	base.RawPath = strings.TrimSuffix(strings.TrimRight(base.RawPath, "/"), "/v1")
	return &Provider{
		baseURL: strings.TrimRight(base.String(), "/"), apiKey: strings.TrimSpace(config.APIKey), client: client,
		capabilities: config.Capabilities, models: models, defaultMaxOutputTokens: config.DefaultMaxOutputTokens,
		requestTimeout: config.RequestTimeout, streamTimeout: config.StreamTimeout,
		maxResponseBytes: config.MaxResponseBytes, maxEventBytes: config.MaxEventBytes,
	}, nil
}

func validCapabilities(c gateway.Capabilities) bool {
	if c.Embeddings == gateway.Supported {
		return false
	}
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
