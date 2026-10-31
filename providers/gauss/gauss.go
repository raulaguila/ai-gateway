// Package gauss implements Samsung Gauss routing, authentication and admission.
package gauss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/internal/transport"
	"github.com/raulaguila/ai-gateway/providers/openai"
)

const DefaultBaseURL = "https://genai-openapi.sec.samsung.net/lahq/trial/api-llm"

type Credentials struct {
	OpenAPIToken string `json:"openapi_token"`
	ClientToken  string `json:"client_token"`
}

// Model keeps Gauss's three distinct identities. Only GUID is used by callers.
type Model struct {
	GUID, RoutingID, ServingID string
	Name, Description          string
	ContextWindow              int
	Tools                      gateway.Support
}

type Config struct {
	BaseURL string
	// Credentials is preferred. Do not combine it with the legacy JSON APIKey.
	Credentials Credentials
	// APIKey accepts legacy JSON containing openapi_token and client_token.
	APIKey     string
	HTTPClient *http.Client
	// Models bootstraps a caller-owned snapshot; inference never discovers implicitly.
	Models                        []Model
	CatalogUpdatedAt              time.Time
	CatalogTTL                    time.Duration
	RequestTimeout, StreamTimeout time.Duration
	MaxResponseBytes              int64
	MaxEventBytes                 int
	// MaxAttempts defaults to 3. Only HTTP 429 before a response body is retried.
	MaxAttempts int
	// FailIfBusy denies generations immediately when the local quota is occupied.
	FailIfBusy bool
	// OnWait is synchronous and may be called concurrently by different operations.
	OnWait func(context.Context, time.Duration) error
}

type Provider struct {
	baseURL, openAPIToken, clientToken, scope string
	client                                    *http.Client
	inner                                     *openai.Provider
	mu                                        sync.RWMutex
	models                                    map[string]Model
	updatedAt                                 time.Time
	snapshotSource                            string
	catalogLoaded                             bool
	catalogTTL, requestTimeout, streamTimeout time.Duration
	maxResponseBytes                          int64
	maxEventBytes                             int
	maxAttempts                               int
	failIfBusy                                bool
	onWait                                    func(context.Context, time.Duration) error
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
		config.BaseURL = DefaultBaseURL
	}
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(config.BaseURL, "#") || base.Opaque != "" {
		return bad("base URL must be HTTP(S) without credentials, query or fragment")
	}
	normalizePath := func(path string) string {
		path = strings.TrimSuffix(strings.TrimRight(path, "/"), "/openapi/llm/v1")
		if strings.HasSuffix(path, "/api-chat") {
			path = strings.TrimSuffix(path, "/api-chat") + "/api-llm"
		}
		return path
	}
	base.Path, base.RawPath = normalizePath(base.Path), normalizePath(base.RawPath)
	base.Host = strings.ToLower(base.Host)
	if base.Scheme == "https" && base.Port() == "443" || base.Scheme == "http" && base.Port() == "80" {
		host := base.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		base.Host = host
	}
	credentials := config.Credentials
	if credentials != (Credentials{}) && config.APIKey != "" {
		return bad("use Credentials or the legacy APIKey JSON, not both")
	}
	if credentials == (Credentials{}) && json.Unmarshal([]byte(config.APIKey), &credentials) != nil {
		return bad("Gauss credentials must be JSON with two single-line tokens")
	}
	if strings.ContainsAny(credentials.OpenAPIToken+credentials.ClientToken, "\r\n") {
		return bad("Gauss credentials must contain two single-line tokens")
	}
	credentials.OpenAPIToken, credentials.ClientToken = strings.TrimSpace(credentials.OpenAPIToken), strings.TrimSpace(credentials.ClientToken)
	if len(credentials.OpenAPIToken) >= 7 && strings.EqualFold(credentials.OpenAPIToken[:7], "Bearer ") {
		credentials.OpenAPIToken = strings.TrimSpace(credentials.OpenAPIToken[7:])
	}
	if credentials.OpenAPIToken == "" || credentials.ClientToken == "" || strings.EqualFold(credentials.OpenAPIToken, "Bearer") {
		return bad("both Gauss tokens are required")
	}
	if config.CatalogTTL < 0 || config.RequestTimeout < 0 || config.StreamTimeout < 0 || config.MaxAttempts < 0 || config.MaxResponseBytes < 0 || config.MaxResponseBytes == math.MaxInt64 || config.MaxEventBytes < 0 || config.MaxEventBytes > math.MaxInt-2 {
		return bad("timeouts, attempt count and response limits are invalid")
	}
	if config.CatalogTTL == 0 {
		config.CatalogTTL = 10 * time.Minute
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 2 * time.Minute
	}
	if config.StreamTimeout == 0 {
		config.StreamTimeout = 30 * time.Minute
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 3
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 8 << 20
	}
	if config.MaxEventBytes == 0 {
		config.MaxEventBytes = int(min(1<<20, config.MaxResponseBytes))
	}
	if int64(config.MaxEventBytes) > config.MaxResponseBytes {
		return bad("event limit must not exceed response limit")
	}
	if len(config.Models) > 10000 {
		return bad("snapshot exceeds model limit")
	}
	models := make(map[string]Model, len(config.Models))
	for _, model := range config.Models {
		normalized, ok := normalizeModel(model)
		if !ok {
			return bad("snapshot models require valid GUID, routing ID, serving ID and capabilities")
		}
		if _, exists := models[normalized.GUID]; exists {
			return bad("snapshot contains duplicate GUIDs")
		}
		models[normalized.GUID] = normalized
	}
	if config.CatalogUpdatedAt.IsZero() {
		config.CatalogUpdatedAt = time.Now()
	}
	if config.CatalogUpdatedAt.After(time.Now().Add(time.Minute)) {
		return bad("snapshot timestamp is in the future")
	}
	client, err := transport.NewClient(config.HTTPClient, false)
	if err != nil {
		return bad(err.Error())
	}
	p := &Provider{
		baseURL: strings.TrimRight(base.String(), "/"), openAPIToken: credentials.OpenAPIToken, clientToken: credentials.ClientToken,
		models: models, updatedAt: config.CatalogUpdatedAt, snapshotSource: "configuration", catalogTTL: config.CatalogTTL,
		catalogLoaded:  config.Models != nil,
		requestTimeout: config.RequestTimeout, streamTimeout: config.StreamTimeout, maxResponseBytes: config.MaxResponseBytes, maxEventBytes: config.MaxEventBytes,
		maxAttempts: config.MaxAttempts, failIfBusy: config.FailIfBusy, onWait: config.OnWait,
	}
	hash := sha256.Sum256([]byte(p.baseURL + "\x00" + p.openAPIToken))
	p.scope = hex.EncodeToString(hash[:])
	client.Transport = &gaussTransport{provider: p, base: client.Transport}
	p.client = client
	p.inner, err = openai.New(openai.Config{BaseURL: p.baseURL + "/openapi/llm/v1", HTTPClient: client,
		RequestTimeout: config.RequestTimeout, StreamTimeout: config.StreamTimeout,
		MaxResponseBytes: config.MaxResponseBytes, MaxEventBytes: config.MaxEventBytes})
	if err != nil {
		return nil, p.operationError("configure", err)
	}
	return p, nil
}

func (p *Provider) Capabilities(guid string) gateway.Capabilities {
	p.mu.RLock()
	defer p.mu.RUnlock()
	caps := gateway.Capabilities{Embeddings: gateway.Unsupported, Catalog: gateway.Supported, Source: p.snapshotSource}
	if model, ok := p.models[guid]; ok {
		caps.Chat, caps.Streaming, caps.Tools = gateway.Supported, gateway.Supported, model.Tools
	}
	return caps
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
