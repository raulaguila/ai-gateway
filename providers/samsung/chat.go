package samsung

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

type wireResult struct {
	SessionID string `json:"session_id"`
	Outputs   []struct {
		Outputs []struct {
			Results struct {
				Message struct {
					Text *string `json:"text"`
				} `json:"message"`
			} `json:"results"`
		} `json:"outputs"`
	} `json:"outputs"`
}

func extractText(result *wireResult) string {
	for _, output := range result.Outputs {
		for _, nested := range output.Outputs {
			if text := nested.Results.Message.Text; text != nil && strings.TrimSpace(*text) != "" {
				return *text
			}
		}
	}
	return ""
}

func (p *Provider) Chat(ctx context.Context, request gateway.ChatRequest) (result gateway.ChatResult, err error) {
	const op = "chat"
	defer func() { err = operationError(op, err) }()
	agent, body, err := p.prepare(request)
	if err != nil {
		return result, err
	}
	ctx, cancel, err := p.operationContext(ctx, false)
	if err != nil {
		return result, err
	}
	defer cancel()
	response, err := p.request(ctx, op, agent, body, false)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, p.maxResponseBytes+1))
	if err != nil {
		return result, ioFailure(ctx, op, err)
	}
	if int64(len(data)) > p.maxResponseBytes {
		return result, failure(gateway.InvalidResponse, op, "Agent Builder response exceeds configured size limit", nil)
	}
	var remote wireResult
	if json.Unmarshal(data, &remote) != nil {
		return result, failure(gateway.InvalidResponse, op, "invalid Agent Builder JSON response", nil)
	}
	result.Text, result.SessionID = extractText(&remote), remote.SessionID
	if result.Text == "" {
		return result, failure(gateway.InvalidResponse, op, "Agent Builder returned no answer text", nil)
	}
	if ctx.Err() != nil {
		return result, ioFailure(ctx, op, ctx.Err())
	}
	result.FinishReason, result.ProtocolComplete = gateway.FinishStop, true
	return result, nil
}
