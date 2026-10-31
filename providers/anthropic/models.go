package anthropic

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

func (p *Provider) ListModels(ctx context.Context) (models []gateway.Model, err error) {
	const op = "list_models"
	defer func() { err = operationError(op, err) }()
	if p.Capabilities("").Catalog == gateway.Unsupported {
		return nil, failure(gateway.UnsupportedCapability, op, "deployment does not support model discovery", nil)
	}
	ctx, cancel, err := p.operationContext(ctx, false)
	if err != nil {
		return nil, err
	}
	defer cancel()
	models = make([]gateway.Model, 0)
	seen := map[string]bool{}
	cursor := ""
	remaining := p.maxResponseBytes
	for page := 0; page < 100; page++ {
		if remaining <= 0 {
			break
		}
		query := url.Values{"limit": {"1000"}}
		if cursor != "" {
			query.Set("after_id", cursor)
		}
		response, err := p.request(ctx, op, http.MethodGet, "/v1/models?"+query.Encode(), nil, false)
		if err != nil {
			return nil, err
		}
		var remote struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool       `json:"has_more"`
			LastID  string     `json:"last_id"`
			Type    string     `json:"type"`
			Error   *wireError `json:"error"`
		}
		count, err := decode(ctx, op, response.Body, remaining, &remote)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		remaining -= count
		if remote.Error != nil || remote.Type == "error" {
			return nil, remoteError(op, remote.Error)
		}
		if remote.Data == nil || len(models)+len(remote.Data) > 10000 {
			return nil, failure(gateway.InvalidResponse, op, "catalog is missing or exceeds model limit", nil)
		}
		for _, row := range remote.Data {
			if strings.TrimSpace(row.ID) == "" || seen[row.ID] {
				return nil, failure(gateway.InvalidResponse, op, "catalog contains empty or duplicate model IDs", nil)
			}
			seen[row.ID] = true
			models = append(models, gateway.Model{ID: row.ID, Capabilities: p.Capabilities(row.ID)})
		}
		if !remote.HasMore {
			return models, nil
		}
		if len(remote.Data) == 0 || remote.LastID != remote.Data[len(remote.Data)-1].ID || remote.LastID == cursor {
			return nil, failure(gateway.InvalidResponse, op, "catalog pagination did not advance", nil)
		}
		cursor = remote.LastID
	}
	return nil, failure(gateway.InvalidResponse, op, "catalog exceeds byte or page limits", nil)
}
