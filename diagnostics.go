package gateway

import (
	"context"
	"errors"
	"sort"
)

func annotateModel(err error, alias string, route ModelRoute, operation string) error {
	if err == nil {
		return nil
	}
	var detail *Error
	_ = errors.As(annotate(err, route.Deployment, operation), &detail)
	detail.ModelAlias, detail.Model = alias, route.Model
	return detail
}

func prepareChatRequest(p Provider, request ChatRequest, policy *TokenPolicy) (ChatRequest, error) {
	if request.MaxOutputTokens == nil && policy != nil && policy.DefaultMaxOutputTokens > 0 {
		request.MaxOutputTokens = Ptr(policy.DefaultMaxOutputTokens)
	}
	if err := ValidateChatRequest(request); err != nil {
		return request, err
	}
	if validator, ok := p.(ChatRequestValidator); ok {
		if err := validator.ValidateChatRequest(request); err != nil {
			return request, err
		}
	}
	return request, nil
}

// CheckConfiguration reports local readiness issues in alias order. It does not
// call catalogs, token counters or generation APIs. It checks whether at least
// one inference operation is available per alias, not remote connectivity.
// New remains usable for catalog-only initialization; this check requires aliases.
func (g *Gateway) CheckConfiguration(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return annotate(err, "", "check_configuration")
	}
	if len(g.models) == 0 {
		return &Error{Code: InvalidConfiguration, Operation: "check_configuration", Field: "Models", Message: "no model aliases are configured; add Config.Models before generating"}
	}
	aliases := make([]string, 0, len(g.models))
	for alias := range g.models {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	var issues []error
	for _, alias := range aliases {
		if err := checkContext(ctx); err != nil {
			return annotate(err, "", "check_configuration")
		}
		route := g.models[alias]
		p := g.deployments[route.Deployment]
		caps := p.Capabilities(route.Model)
		_, chat := p.(ChatProvider)
		_, stream := p.(StreamProvider)
		_, embed := p.(EmbeddingProvider)
		chatErr := g.require(caps.Chat, chat, "Chat")
		streamErr := g.require(caps.Streaming, stream, "Streaming")
		embedErr := g.require(caps.Embeddings, embed, "Embeddings")
		var err error
		if (chat || stream) && (embedErr != nil || chatErr == nil || streamErr == nil) {
			r := ChatRequest{Model: route.Model, Messages: []Message{User("configuration check")}}
			if route.TokenPolicy != nil {
				r.MaxOutputTokens = Ptr(max(1, route.TokenPolicy.DefaultMaxOutputTokens))
			}
			_, err = prepareChatRequest(p, r, route.TokenPolicy)
		}
		if err == nil && chatErr != nil && streamErr != nil && embedErr != nil {
			switch {
			case chat:
				err = chatErr
			case stream:
				err = streamErr
			case embed:
				err = embedErr
			default:
				err = &Error{Code: UnsupportedCapability, Message: "provider implements no inference operation"}
			}
		}
		if err != nil {
			issues = append(issues, annotateModel(err, alias, route, "check_configuration"))
		}
	}
	return errors.Join(issues...)
}

// EstimateBudget runs the same local request and token-policy checks as chat,
// without generation. On ContextExceeded it returns the available budget and
// the error. A TokenPolicy is required; custom counters retain their own I/O policy.
func (g *Gateway) EstimateBudget(ctx context.Context, request ChatRequest) (budget TokenBudget, err error) {
	p, route, err := g.resolve(ctx, request.Model, "estimate_budget")
	if err != nil {
		return budget, err
	}
	alias := g.modelAlias(request.Model)
	defer func() { err = annotateModel(err, alias, route, "estimate_budget") }()
	if route.TokenPolicy == nil {
		return budget, &Error{Code: InvalidConfiguration, Field: "TokenPolicy", Message: "configure ModelRoute.TokenPolicy before estimating a budget"}
	}
	request.Model = route.Model
	request, err = prepareChatRequest(p, request, route.TokenPolicy)
	if err != nil {
		return budget, err
	}
	_, calculated, err := applyTokenPolicy(ctx, request, route.TokenPolicy)
	if calculated != nil {
		budget = *calculated
	}
	return budget, err
}
