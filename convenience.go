package gateway

import "context"

// Ptr preserves explicit values (including zero) for optional request fields.
func Ptr[T any](value T) *T { return &value }

func User(text string) Message { return Message{Role: RoleUser, Content: text} }

// NewSingle registers one provider/model and makes Model optional in requests.
// At most one token policy may be supplied. It checks local readiness without I/O.
// Model capabilities must still be declared by the provider configuration.
func NewSingle(provider Provider, model string, policies ...TokenPolicy) (*Gateway, error) {
	if len(policies) > 1 {
		return nil, &Error{Code: InvalidConfiguration, Field: "TokenPolicy", Message: "NewSingle accepts at most one token policy"}
	}
	route := ModelRoute{Deployment: "single", Model: model}
	if len(policies) == 1 {
		route.TokenPolicy = &policies[0]
	}
	g, err := New(Config{Deployments: map[string]Provider{"single": provider}, Models: map[string]ModelRoute{model: route}})
	if err != nil {
		return nil, err
	}
	g.defaultAlias = model
	if err := g.CheckConfiguration(context.Background()); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Gateway) modelAlias(model string) string {
	if model == "" {
		return g.defaultAlias
	}
	return model
}

// StreamText delivers text deltas synchronously and returns the complete result.
// It preserves Stream's errors, partial result, cancellation and backpressure.
func (g *Gateway) StreamText(ctx context.Context, request ChatRequest, emit func(string) error) (ChatResult, error) {
	if emit == nil {
		return g.Stream(ctx, request, nil)
	}
	return g.Stream(ctx, request, func(event Event) error {
		if event.Type == EventTextDelta {
			return emit(event.Text)
		}
		return nil
	})
}
