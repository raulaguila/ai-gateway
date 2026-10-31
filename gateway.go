package gateway

import (
	"context"
	"reflect"
	"strings"
)

type ModelRoute struct {
	Deployment string
	Model      string
	// TokenPolicy is optional and applies only to Chat and Stream through Gateway.
	TokenPolicy *TokenPolicy
}

type Config struct {
	Deployments map[string]Provider
	Models      map[string]ModelRoute
	// Unknown capabilities fail closed unless the caller explicitly opts in.
	AllowUnknownCapabilities bool
}

// Gateway copies routing maps on construction. Providers retain ownership of
// their own configuration and must implement the concurrency contract.
type Gateway struct {
	deployments  map[string]Provider
	models       map[string]ModelRoute
	allowUnknown bool
	defaultAlias string
}

func New(config Config) (*Gateway, error) {
	g := &Gateway{deployments: map[string]Provider{}, models: map[string]ModelRoute{}, allowUnknown: config.AllowUnknownCapabilities}
	bad := func(message string) (*Gateway, error) {
		return nil, &Error{Code: InvalidConfiguration, Message: message}
	}
	if len(config.Deployments) == 0 {
		return bad("at least one deployment is required")
	}
	for name, provider := range config.Deployments {
		if strings.TrimSpace(name) == "" || nilProvider(provider) {
			return bad("deployment names and providers must be nonempty")
		}
		if err := validateCapabilities(provider, ""); err != nil {
			return nil, annotate(err, name, "configure")
		}
		g.deployments[name] = provider
	}
	for alias, route := range config.Models {
		if strings.TrimSpace(alias) == "" || strings.TrimSpace(route.Model) == "" || g.deployments[route.Deployment] == nil {
			return nil, &Error{Code: InvalidConfiguration, ModelAlias: alias, Model: route.Model, Provider: route.Deployment, Field: "Models", Message: "model alias must reference a configured deployment and nonempty remote model"}
		}
		if err := validateCapabilities(g.deployments[route.Deployment], route.Model); err != nil {
			return nil, annotateModel(err, alias, route, "configure")
		}
		if route.TokenPolicy != nil {
			policy := *route.TokenPolicy
			if err := policy.validate(); err != nil {
				return nil, annotateModel(err, alias, route, "configure")
			}
			route.TokenPolicy = &policy
		}
		g.models[alias] = route
	}
	return g, nil
}

func nilProvider(p Provider) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func validateCapabilities(p Provider, model string) error {
	c := p.Capabilities(model)
	_, chat := p.(ChatProvider)
	_, stream := p.(StreamProvider)
	_, embed := p.(EmbeddingProvider)
	_, catalog := p.(CatalogProvider)
	for _, item := range []struct {
		state       Support
		implemented bool
	}{
		{c.Chat, chat}, {c.Streaming, stream}, {c.Embeddings, embed}, {c.Catalog, catalog}, {c.Tools, chat || stream},
	} {
		if item.state != Unknown && item.state != Supported && item.state != Unsupported || item.state == Supported && !item.implemented {
			return &Error{Code: InvalidConfiguration, Message: "capability declarations contradict provider interfaces"}
		}
	}
	return nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return &Error{Code: InvalidRequest, Message: "context is required"}
	}
	if err := ctx.Err(); err != nil {
		code := Cancelled
		if err == context.DeadlineExceeded {
			code = Timeout
		}
		return &Error{Code: code, Message: "operation context ended", Cause: err}
	}
	return nil
}

func (g *Gateway) resolve(ctx context.Context, alias, op string) (Provider, ModelRoute, error) {
	alias = g.modelAlias(alias)
	if err := checkContext(ctx); err != nil {
		return nil, ModelRoute{}, annotate(err, "", op)
	}
	route, ok := g.models[alias]
	if !ok {
		return nil, route, &Error{Code: ModelNotFound, Operation: op, ModelAlias: alias, Field: "Models", Message: "model alias is not configured; add it to Config.Models"}
	}
	return g.deployments[route.Deployment], route, nil
}

func (g *Gateway) require(state Support, implemented bool, capability string) error {
	message := ""
	if !implemented {
		message = "provider does not implement " + capability
	} else if state == Unsupported {
		message = "model explicitly declares " + capability + " unsupported"
	} else if state == Unknown && !g.allowUnknown {
		message = capability + " capability is unknown; declare verified model capabilities in provider configuration"
	}
	if message != "" {
		return &Error{Code: UnsupportedCapability, Field: "Capabilities." + capability, Message: message}
	}
	return nil
}

func (g *Gateway) Chat(ctx context.Context, request ChatRequest) (result ChatResult, err error) {
	p, route, err := g.resolve(ctx, request.Model, "chat")
	if err != nil {
		return ChatResult{}, err
	}
	alias := g.modelAlias(request.Model)
	defer func() { err = annotateModel(err, alias, route, "chat") }()
	request.Model = route.Model
	if request, err = prepareChatRequest(p, request, route.TokenPolicy); err != nil {
		return ChatResult{}, annotate(err, route.Deployment, "chat")
	}
	chat, ok := p.(ChatProvider)
	caps := p.Capabilities(route.Model)
	err = g.require(caps.Chat, ok, "Chat")
	if err == nil && usesTools(request) {
		err = g.require(caps.Tools, true, "Tools")
	}
	if err != nil {
		return ChatResult{}, annotate(err, route.Deployment, "chat")
	}
	request, _, err = applyTokenPolicy(ctx, request, route.TokenPolicy)
	if err != nil {
		return ChatResult{}, annotate(err, route.Deployment, "chat")
	}
	result, err = chat.Chat(ctx, request)
	return result, annotate(err, route.Deployment, "chat")
}

func (g *Gateway) Stream(ctx context.Context, request ChatRequest, emit func(Event) error) (result ChatResult, err error) {
	p, route, err := g.resolve(ctx, request.Model, "stream")
	if err != nil {
		return ChatResult{}, err
	}
	alias := g.modelAlias(request.Model)
	defer func() { err = annotateModel(err, alias, route, "stream") }()
	if emit == nil {
		return ChatResult{}, &Error{Code: InvalidRequest, Message: "stream callback is required"}
	}
	request.Model = route.Model
	if request, err = prepareChatRequest(p, request, route.TokenPolicy); err != nil {
		return ChatResult{}, annotate(err, route.Deployment, "stream")
	}
	stream, ok := p.(StreamProvider)
	caps := p.Capabilities(route.Model)
	err = g.require(caps.Streaming, ok, "Streaming")
	if err == nil && usesTools(request) {
		err = g.require(caps.Tools, true, "Tools")
	}
	if err != nil {
		return ChatResult{}, annotate(err, route.Deployment, "stream")
	}
	request, _, err = applyTokenPolicy(ctx, request, route.TokenPolicy)
	if err != nil {
		return ChatResult{}, annotate(err, route.Deployment, "stream")
	}
	var sequence uint64
	result, err = stream.Stream(ctx, request, func(event Event) error {
		sequence++
		event.Sequence = sequence
		if event.Wait != nil {
			copy := *event.Wait
			copy.Provider = route.Deployment
			event.Wait = &copy
		}
		if event.Error != nil {
			copy := *event.Error
			copy.Provider, copy.Operation = route.Deployment, "stream"
			copy.ModelAlias, copy.Model = alias, route.Model
			event.Error = &copy
		}
		return emit(event)
	})
	return result, annotate(err, route.Deployment, "stream")
}

func (g *Gateway) Embed(ctx context.Context, request EmbedRequest) (result EmbedResult, err error) {
	p, route, err := g.resolve(ctx, request.Model, "embed")
	if err != nil {
		return EmbedResult{}, err
	}
	alias := g.modelAlias(request.Model)
	defer func() { err = annotateModel(err, alias, route, "embed") }()
	request.Model = route.Model
	if err = ValidateEmbedRequest(request); err != nil {
		return EmbedResult{}, annotate(err, route.Deployment, "embed")
	}
	embed, ok := p.(EmbeddingProvider)
	if err = g.require(p.Capabilities(route.Model).Embeddings, ok, "Embeddings"); err != nil {
		return EmbedResult{}, annotate(err, route.Deployment, "embed")
	}
	result, err = embed.Embed(ctx, request)
	return result, annotate(err, route.Deployment, "embed")
}

func (g *Gateway) ListModels(ctx context.Context, deployment string) ([]Model, error) {
	if err := checkContext(ctx); err != nil {
		return nil, annotate(err, deployment, "list_models")
	}
	p, ok := g.deployments[deployment]
	if !ok {
		return nil, &Error{Code: InvalidConfiguration, Operation: "list_models", Message: "deployment is not configured"}
	}
	catalog, ok := p.(CatalogProvider)
	if err := g.require(p.Capabilities("").Catalog, ok, "Catalog"); err != nil {
		return nil, annotate(err, deployment, "list_models")
	}
	models, err := catalog.ListModels(ctx)
	return models, annotate(err, deployment, "list_models")
}
