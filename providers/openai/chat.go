package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role       gateway.Role   `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Refusal    string         `json:"refusal,omitempty"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

func (p *Provider) chatBody(r gateway.ChatRequest, stream bool) (map[string]any, error) {
	if err := gateway.ValidateChatRequest(r); err != nil {
		return nil, err
	}
	caps := p.Capabilities(r.Model)
	if !stream && caps.Chat == gateway.Unsupported || stream && caps.Streaming == gateway.Unsupported {
		return nil, failure(gateway.UnsupportedCapability, "", "model does not support this operation", nil)
	}
	hasTools := len(r.Tools) > 0
	messages := make([]wireMessage, 0, len(r.Messages)+1)
	if r.System != "" {
		messages = append(messages, wireMessage{Role: gateway.RoleSystem, Content: &r.System})
	}
	for _, m := range r.Messages {
		content := m.Content
		message := wireMessage{Role: m.Role, Content: &content, ToolCallID: m.ToolCallID}
		for _, call := range m.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, wireToolCall{ID: call.ID, Type: "function", Function: wireFunction{Name: call.Name, Arguments: string(call.Arguments)}})
		}
		if len(m.ToolCalls) > 0 && m.Content == "" {
			message.Content = nil
		}
		messages = append(messages, message)
		hasTools = hasTools || len(m.ToolCalls) > 0 || m.Role == gateway.RoleTool
	}
	if hasTools && (stream || caps.Tools == gateway.Unsupported) {
		return nil, failure(gateway.UnsupportedCapability, "", "tools require supported non-streaming chat", nil)
	}
	body := map[string]any{"model": r.Model, "messages": messages, "stream": stream}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	if r.MaxOutputTokens != nil {
		body[p.maxTokensField] = *r.MaxOutputTokens
	}
	if stream && p.includeStreamUsage {
		body["stream_options"] = map[string]bool{"include_usage": true}
	}
	if len(r.Tools) > 0 {
		tools := make([]wireTool, 0, len(r.Tools))
		for _, tool := range r.Tools {
			tools = append(tools, wireTool{Type: "function", Function: wireFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
		}
		body["tools"], body["tool_choice"] = tools, "auto"
	}
	return body, nil
}

func (p *Provider) Chat(ctx context.Context, request gateway.ChatRequest) (result gateway.ChatResult, err error) {
	const op = "chat"
	defer func() { err = operationError(op, err) }()
	body, err := p.chatBody(request, false)
	if err != nil {
		return result, err
	}
	ctx, cancel, err := p.operationContext(ctx, false)
	if err != nil {
		return result, err
	}
	defer cancel()
	response, err := p.request(ctx, op, http.MethodPost, "/chat/completions", body, false)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	var remote struct {
		ID      string `json:"id"`
		Choices []struct {
			Index        int         `json:"index"`
			Message      wireMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage *wireUsage      `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if err := p.decode(ctx, op, response.Body, &remote); err != nil {
		return result, err
	}
	if hasRemoteError(remote.Error) {
		return result, failure(gateway.UpstreamError, op, "upstream returned an error", nil)
	}
	result.ID = remote.ID
	result.Usage, err = mergeUsage(result.Usage, remote.Usage)
	if err != nil {
		return result, err
	}
	if len(remote.Choices) != 1 || remote.Choices[0].Index != 0 || remote.Choices[0].FinishReason == "" {
		return result, failure(gateway.InvalidResponse, op, "expected one complete chat choice", nil)
	}
	choice := remote.Choices[0]
	result.NativeFinishReason, result.FinishReason = choice.FinishReason, finishReason(choice.FinishReason)
	if choice.Message.Content != nil {
		result.Text = *choice.Message.Content
	}
	if choice.Message.Refusal != "" {
		result.FinishReason = gateway.FinishContentFilter
	}
	ids := map[string]bool{}
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" || strings.TrimSpace(call.ID) == "" || ids[call.ID] || strings.TrimSpace(call.Function.Name) == "" || !gateway.JSONObject(json.RawMessage(call.Function.Arguments)) {
			return result, failure(gateway.InvalidResponse, op, "invalid tool call returned by upstream", nil)
		}
		ids[call.ID] = true
		result.ToolCalls = append(result.ToolCalls, gateway.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments)})
	}
	if len(result.ToolCalls) > 0 && result.FinishReason != gateway.FinishContentFilter {
		result.FinishReason = gateway.FinishToolUse
	}
	if len(result.ToolCalls) == 0 && result.FinishReason == gateway.FinishToolUse {
		return result, failure(gateway.InvalidResponse, op, "tool finish reason without tool calls", nil)
	}
	if result.Text == "" && len(result.ToolCalls) == 0 && result.FinishReason != gateway.FinishContentFilter && result.FinishReason != gateway.FinishMaxTokens {
		return result, failure(gateway.InvalidResponse, op, "upstream returned no visible content", nil)
	}
	result.ProtocolComplete = true
	return result, nil
}

func hasRemoteError(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }
