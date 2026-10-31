package gauss

import (
	"context"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
)

type CatalogStatus struct {
	Loaded     bool
	Stale      bool
	ModelCount int
	UpdatedAt  time.Time
	ExpiresAt  time.Time
}

// CatalogStatus inspects the local snapshot without making a request.
func (p *Provider) CatalogStatus() CatalogStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	status := CatalogStatus{Loaded: p.catalogLoaded, Stale: true, ModelCount: len(p.models)}
	if p.catalogLoaded {
		status.UpdatedAt = p.updatedAt
		status.ExpiresAt = p.updatedAt.Add(p.catalogTTL)
		status.Stale = time.Now().After(status.ExpiresAt)
	}
	return status
}

// RefreshModels explicitly fetches and replaces the routing snapshot. A failed
// refresh preserves the existing snapshot. No generation is performed.
func (p *Provider) RefreshModels(ctx context.Context) ([]gateway.Model, error) {
	return p.ListModels(ctx)
}

func (p *Provider) resolveModel(guid string) (Model, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.catalogLoaded || p.updatedAt.IsZero() || time.Since(p.updatedAt) > p.catalogTTL {
		return Model{}, &gateway.Error{Code: gateway.InvalidConfiguration, Field: "Models", Message: "Gauss routing snapshot is missing or stale; call RefreshModels (or ListModels) before generating"}
	}
	model, ok := p.models[guid]
	if !ok {
		return Model{}, &gateway.Error{Code: gateway.InvalidConfiguration, Field: "Model", Message: "Gauss GUID is absent from the snapshot; select a GUID from RefreshModels (or ListModels)"}
	}
	return model, nil
}

// ValidateChatRequest checks local routing before the gateway's capability gate.
func (p *Provider) ValidateChatRequest(request gateway.ChatRequest) error {
	if err := gateway.ValidateChatRequest(request); err != nil {
		return err
	}
	_, err := p.resolveModel(request.Model)
	return err
}
