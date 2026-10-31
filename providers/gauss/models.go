package gauss

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
)

func normalizeModel(model Model) (Model, bool) {
	model.GUID, model.RoutingID, model.ServingID = strings.TrimSpace(model.GUID), strings.TrimSpace(model.RoutingID), strings.TrimSpace(model.ServingID)
	number, err := strconv.ParseUint(model.RoutingID, 10, 64)
	if err != nil || number == 0 || model.GUID == "" || model.ServingID == "" || model.ContextWindow < 0 || model.ContextWindow > 2000000 || model.Tools != gateway.Unknown && model.Tools != gateway.Supported && model.Tools != gateway.Unsupported {
		return model, false
	}
	model.RoutingID = strconv.FormatUint(number, 10)
	return model, true
}

// Snapshot returns a copy that callers can persist along with its freshness time.
func (p *Provider) Snapshot() ([]Model, time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	models := make([]Model, 0, len(p.models))
	for _, model := range p.models {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].GUID < models[j].GUID })
	return models, p.updatedAt
}

var toolsLabel = regexp.MustCompile(`(?im)^\s*-?\s*Tool\s+Calling\s*:\s*([OX])\s*$`)
var contextLabel = regexp.MustCompile(`(?im)^\s*-?\s*Context\s+(?:Length|Window)\s*:\s*([0-9]+(?:\.[0-9]+)?\s*[kKmM]?)\s*(?:tokens)?\s*$`)

func contextSize(raw json.RawMessage) int {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		text = string(raw)
	}
	text = strings.ToLower(strings.TrimSpace(text))
	multiplier := 1.0
	if strings.HasSuffix(text, "k") {
		multiplier = 1000
		text = strings.TrimSpace(strings.TrimSuffix(text, "k"))
	}
	if strings.HasSuffix(text, "m") {
		multiplier = 1000000
		text = strings.TrimSpace(strings.TrimSuffix(text, "m"))
	}
	value, err := strconv.ParseFloat(text, 64)
	value *= multiplier
	if err != nil || !(value > 0 && value <= 2000000) || value != float64(int(value)) {
		return 0
	}
	return int(value)
}

func localized(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var translations []json.RawMessage
	if json.Unmarshal(raw, &translations) == nil && translations != nil {
		first := ""
		for _, translation := range translations {
			var entry struct {
				Language string `json:"languageCode"`
				Content  string `json:"content"`
			}
			if json.Unmarshal(translation, &entry) != nil {
				continue
			}
			value := strings.TrimSpace(entry.Content)
			if value == "" {
				continue
			}
			if first == "" {
				first = value
			}
			if strings.EqualFold(entry.Language, "en") {
				return value
			}
		}
		return first
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ""
	}
	first := ""
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return first
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return first
		}
		var candidate string
		if json.Unmarshal(value, &candidate) != nil || strings.TrimSpace(candidate) == "" {
			continue
		}
		candidate = strings.TrimSpace(candidate)
		if first == "" {
			first = candidate
		}
		language := strings.ToLower(key.(string))
		if language == "en" || strings.HasPrefix(language, "en-") || strings.HasPrefix(language, "en_") || language == "english" {
			return candidate
		}
	}
	return first
}

func catalogRows(data []byte, depth int) ([]json.RawMessage, error) {
	if depth > 8 {
		return nil, failure(gateway.InvalidResponse, "list_models", "Gauss catalog nesting exceeds limit", nil)
	}
	var rows []json.RawMessage
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if json.Unmarshal(data, &rows) != nil {
			return nil, failure(gateway.InvalidResponse, "list_models", "invalid Gauss catalog JSON", nil)
		}
	} else {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(data, &envelope) != nil {
			return nil, failure(gateway.InvalidResponse, "list_models", "invalid or paginated Gauss catalog", nil)
		}
		var more bool
		if raw := envelope["has_more"]; raw != nil && (json.Unmarshal(raw, &more) != nil || more) {
			return nil, failure(gateway.InvalidResponse, "list_models", "paginated Gauss catalog is unsupported", nil)
		}
		if raw := envelope["error"]; len(raw) > 0 && string(raw) != "null" {
			return nil, failure(gateway.UpstreamError, "list_models", "Gauss catalog returned an error", nil)
		}
		for _, key := range []string{"models", "data", "result", "llmModels"} {
			if raw := envelope[key]; len(raw) > 0 && string(raw) != "null" {
				return catalogRows(raw, depth+1)
			}
		}
		return nil, failure(gateway.InvalidResponse, "list_models", "Gauss catalog model list is missing", nil)
	}
	return rows, nil
}

func decodeModels(data []byte) ([]Model, error) {
	rows, err := catalogRows(data, 0)
	if err != nil {
		return nil, err
	}
	if rows == nil || len(rows) > 10000 {
		return nil, failure(gateway.InvalidResponse, "list_models", "Gauss catalog is missing or exceeds model limit", nil)
	}
	models := make([]Model, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		var remote struct {
			GUID               string          `json:"modelGuid"`
			Routing            json.RawMessage `json:"modelId"`
			Serving            string          `json:"modelServingId"`
			Name               json.RawMessage `json:"name"`
			ModelName          json.RawMessage `json:"modelName"`
			Description        json.RawMessage `json:"description"`
			ModelDescription   json.RawMessage `json:"modelDescription"`
			ContextWindow      json.RawMessage `json:"contextWindow"`
			ContextLength      json.RawMessage `json:"contextLength"`
			ContextWindowSnake json.RawMessage `json:"context_window"`
			ContextLengthSnake json.RawMessage `json:"context_length"`
			MaxModelLen        json.RawMessage `json:"max_model_len"`
		}
		if json.Unmarshal(row, &remote) != nil {
			continue
		}
		var routing string
		if json.Unmarshal(remote.Routing, &routing) != nil {
			routing = string(remote.Routing)
		}
		model := Model{GUID: remote.GUID, RoutingID: routing, ServingID: remote.Serving}
		model.Name, model.Description = localized(remote.Name), localized(remote.Description)
		if model.Name == "" {
			model.Name = localized(remote.ModelName)
		}
		if model.Description == "" {
			model.Description = localized(remote.ModelDescription)
		}
		if label := toolsLabel.FindStringSubmatch(model.Description); len(label) > 0 {
			model.Tools = gateway.Unsupported
			if strings.EqualFold(label[1], "O") {
				model.Tools = gateway.Supported
			}
		}
		for _, capacity := range []json.RawMessage{remote.ContextWindow, remote.ContextWindowSnake, remote.ContextLength, remote.ContextLengthSnake, remote.MaxModelLen} {
			if model.ContextWindow = contextSize(capacity); model.ContextWindow != 0 {
				break
			}
		}
		if model.ContextWindow == 0 {
			if label := contextLabel.FindStringSubmatch(model.Description); len(label) > 0 {
				raw, _ := json.Marshal(label[1])
				model.ContextWindow = contextSize(raw)
			}
		}
		normalized, ok := normalizeModel(model)
		if !ok || seen[normalized.GUID] {
			continue
		}
		seen[normalized.GUID] = true
		models = append(models, normalized)
	}
	if len(rows) > 0 && len(models) == 0 {
		return nil, failure(gateway.InvalidResponse, "list_models", "Gauss catalog contains no valid model routing", nil)
	}
	return models, nil
}

func (p *Provider) ListModels(ctx context.Context) (models []gateway.Model, err error) {
	const op = "list_models"
	defer func() { err = p.operationError(op, err) }()
	ctx, cancel, err := p.operationContext(ctx, false)
	if err != nil {
		return nil, err
	}
	defer cancel()
	ctx, wait := p.withWait(ctx, nil)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/openapi/llm/v1/models", nil)
	if err != nil {
		return nil, failure(gateway.InvalidConfiguration, op, "catalog URL is invalid", nil)
	}
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		if wait.err != nil {
			return nil, wait.err
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 2048))
		code := gateway.UpstreamError
		switch response.StatusCode {
		case 400, 422:
			code = gateway.InvalidRequest
		case 401, 403:
			code = gateway.AuthenticationFailed
		case 408, 504:
			code = gateway.Timeout
		case 429:
			code = gateway.RateLimited
		case 529:
			code = gateway.CapacityUnavailable
		}
		problem := failure(code, op, "Gauss rejected catalog request", nil)
		problem.UpstreamStatus = response.StatusCode
		problem.RequestID = p.requestID(response.Header.Get("x-request-id"))
		problem.Retryable = response.StatusCode == 408 || response.StatusCode == 429 || response.StatusCode >= 500
		return nil, problem
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, p.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > p.maxResponseBytes {
		return nil, failure(gateway.InvalidResponse, op, "Gauss catalog exceeds response limit", nil)
	}
	native, err := decodeModels(data)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, contextError(ctx.Err())
	}
	snapshot := make(map[string]Model, len(native))
	models = make([]gateway.Model, 0, len(native))
	for _, model := range native {
		snapshot[model.GUID] = model
		models = append(models, gateway.Model{ID: model.GUID, Capabilities: gateway.Capabilities{Chat: gateway.Supported, Streaming: gateway.Supported, Tools: model.Tools, Embeddings: gateway.Unsupported, Catalog: gateway.Supported, Source: "catalog"}})
	}
	p.mu.Lock()
	p.models, p.updatedAt, p.snapshotSource = snapshot, time.Now(), "catalog"
	p.catalogLoaded = true
	p.mu.Unlock()
	return models, nil
}
