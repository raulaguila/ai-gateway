package openai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

type finiteNumber float64

func (n *finiteNumber) UnmarshalJSON(data []byte) error {
	value, err := strconv.ParseFloat(string(data), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return errors.New("invalid vector component")
	}
	*n = finiteNumber(value)
	return nil
}

func (p *Provider) Embed(ctx context.Context, request gateway.EmbedRequest) (result gateway.EmbedResult, err error) {
	const op = "embed"
	defer func() { err = operationError(op, err) }()
	if err := gateway.ValidateEmbedRequest(request); err != nil {
		return result, err
	}
	if p.Capabilities(request.Model).Embeddings == gateway.Unsupported {
		return result, failure(gateway.UnsupportedCapability, op, "model does not support embeddings", nil)
	}
	ctx, cancel, err := p.operationContext(ctx, false)
	if err != nil {
		return result, err
	}
	defer cancel()
	response, err := p.request(ctx, op, http.MethodPost, "/embeddings", map[string]any{"model": request.Model, "input": request.Input, "encoding_format": "float"}, false)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	var remote struct {
		Model string `json:"model"`
		Data  []struct {
			Index     *int           `json:"index"`
			Embedding []finiteNumber `json:"embedding"`
		} `json:"data"`
		Usage *wireUsage      `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if err = p.decode(ctx, op, response.Body, &remote); err != nil {
		return result, err
	}
	if hasRemoteError(remote.Error) {
		return result, failure(gateway.UpstreamError, op, "upstream returned an error", nil)
	}
	result.Usage, err = mergeUsage(result.Usage, remote.Usage)
	if err != nil {
		return result, err
	}
	invalid := func() (gateway.EmbedResult, error) {
		return result, failure(gateway.InvalidResponse, op, "invalid embedding indices, count or dimensions", nil)
	}
	if len(remote.Data) != len(request.Input) {
		return invalid()
	}
	vectors := make([][]float64, len(request.Input))
	dimension := 0
	for _, row := range remote.Data {
		if row.Index == nil || *row.Index < 0 || *row.Index >= len(vectors) || vectors[*row.Index] != nil || len(row.Embedding) == 0 {
			return invalid()
		}
		if dimension == 0 {
			dimension = len(row.Embedding)
		}
		if len(row.Embedding) != dimension {
			return invalid()
		}
		vector := make([]float64, len(row.Embedding))
		for i, value := range row.Embedding {
			vector[i] = float64(value)
		}
		vectors[*row.Index] = vector
	}
	result.Model, result.Vectors = request.Model, vectors
	if remote.Model != "" && remote.Model != request.Model {
		return gateway.EmbedResult{Usage: result.Usage}, failure(gateway.InvalidResponse, op, "upstream changed embedding model identity", nil)
	}
	return result, nil
}

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
	response, err := p.request(ctx, op, http.MethodGet, "/models", nil, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var remote struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Error   json.RawMessage `json:"error"`
		HasMore bool            `json:"has_more"`
	}
	if err = p.decode(ctx, op, response.Body, &remote); err != nil {
		return nil, err
	}
	if hasRemoteError(remote.Error) {
		return nil, failure(gateway.UpstreamError, op, "upstream returned an error", nil)
	}
	if remote.Data == nil || len(remote.Data) > 10000 || remote.HasMore {
		return nil, failure(gateway.InvalidResponse, op, "catalog missing, oversized or requires unsupported pagination", nil)
	}
	models = make([]gateway.Model, 0, len(remote.Data))
	seen := map[string]bool{}
	for _, row := range remote.Data {
		if strings.TrimSpace(row.ID) == "" || seen[row.ID] {
			return nil, failure(gateway.InvalidResponse, op, "catalog contains empty or duplicate model IDs", nil)
		}
		seen[row.ID] = true
		models = append(models, gateway.Model{ID: row.ID, Capabilities: p.Capabilities(row.ID)})
	}
	return models, nil
}
