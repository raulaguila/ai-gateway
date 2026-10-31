package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

type wireBlock struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   *string         `json:"content,omitempty"`
}

type wireMessage struct {
	Role    gateway.Role `json:"role"`
	Content []wireBlock  `json:"content"`
}

type wireResponse struct {
	Type       string       `json:"type"`
	ID         string       `json:"id"`
	Role       gateway.Role `json:"role"`
	Content    []wireBlock  `json:"content"`
	StopReason string       `json:"stop_reason"`
	Usage      *wireUsage   `json:"usage"`
	Error      *wireError   `json:"error"`
}

func (p *Provider) chatBody(r gateway.ChatRequest, stream bool) (map[string]any, error) {
	if err := gateway.ValidateChatRequest(r); err != nil {
		return nil, err
	}
	caps := p.Capabilities(r.Model)
	if !stream && caps.Chat == gateway.Unsupported || stream && caps.Streaming == gateway.Unsupported {
		return nil, failure(gateway.UnsupportedCapability, "", "model does not support this operation", nil)
	}
	if r.Temperature != nil && *r.Temperature > 1 {
		return nil, failure(gateway.InvalidRequest, "", "Anthropic temperature must be between zero and one", nil)
	}
	system := make([]string, 0)
	if r.System != "" {
		system = append(system, r.System)
	}
	messages := make([]wireMessage, 0, len(r.Messages))
	hasTools := len(r.Tools) > 0
	for _, m := range r.Messages {
		if m.Role == gateway.RoleSystem {
			if len(messages) > 0 {
				return nil, failure(gateway.UnsupportedCapability, "", "mid-conversation system messages are not supported by this API profile", nil)
			}
			if m.Content != "" {
				system = append(system, m.Content)
			}
			continue
		}
		role := m.Role
		blocks := make([]wireBlock, 0, 1+len(m.ToolCalls))
		content := m.Content
		if role == gateway.RoleTool {
			role = gateway.RoleUser
			blocks = append(blocks, wireBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: &content})
			hasTools = true
		} else {
			if content != "" {
				blocks = append(blocks, wireBlock{Type: "text", Text: &content})
			}
			for _, call := range m.ToolCalls {
				blocks = append(blocks, wireBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: call.Arguments})
				hasTools = true
			}
		}
		if len(blocks) == 0 {
			return nil, failure(gateway.InvalidRequest, "", "messages must contain text or tool blocks", nil)
		}
		if len(messages) > 0 && messages[len(messages)-1].Role == role {
			messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, blocks...)
		} else {
			messages = append(messages, wireMessage{Role: role, Content: blocks})
		}
	}
	if len(messages) == 0 {
		return nil, failure(gateway.InvalidRequest, "", "at least one user or assistant message is required", nil)
	}
	if hasTools && (stream || caps.Tools == gateway.Unsupported) {
		return nil, failure(gateway.UnsupportedCapability, "", "tools require supported non-streaming chat", nil)
	}
	maxTokens := p.defaultMaxOutputTokens
	if r.MaxOutputTokens != nil {
		maxTokens = *r.MaxOutputTokens
	}
	body := map[string]any{"model": r.Model, "messages": messages, "max_tokens": maxTokens, "stream": stream}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, 0, len(r.Tools))
		for _, tool := range r.Tools {
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters})
		}
		body["tools"] = tools
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
	response, err := p.request(ctx, op, http.MethodPost, "/v1/messages", body, false)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	var remote wireResponse
	if _, err = decode(ctx, op, response.Body, p.maxResponseBytes, &remote); err != nil {
		return result, err
	}
	if remote.Error != nil || remote.Type == "error" {
		return result, remoteError(op, remote.Error)
	}
	result.ID = remote.ID
	result.Usage, err = mergeUsage(result.Usage, remote.Usage)
	if err != nil {
		return result, err
	}
	if remote.Type != "message" || strings.TrimSpace(remote.ID) == "" || remote.Role != gateway.RoleAssistant || remote.StopReason == "" {
		return result, failure(gateway.InvalidResponse, op, "expected a complete assistant message", nil)
	}
	result.NativeFinishReason, result.FinishReason = remote.StopReason, finishReason(remote.StopReason)
	var text strings.Builder
	ids := map[string]bool{}
	for _, block := range remote.Content {
		switch block.Type {
		case "text":
			if block.Text == nil {
				return result, failure(gateway.InvalidResponse, op, "text block is missing its text", nil)
			}
			text.WriteString(*block.Text)
		case "tool_use":
			if strings.TrimSpace(block.ID) == "" || ids[block.ID] || strings.TrimSpace(block.Name) == "" || !gateway.JSONObject(block.Input) {
				return result, failure(gateway.InvalidResponse, op, "invalid tool call returned by upstream", nil)
			}
			ids[block.ID] = true
			result.ToolCalls = append(result.ToolCalls, gateway.ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
		case "thinking", "redacted_thinking": // Reasoning is never exposed as visible answer text.
		default:
			return result, failure(gateway.UnsupportedCapability, op, "response contains unsupported content blocks", nil)
		}
		result.Text = text.String()
	}
	if len(result.ToolCalls) > 0 && result.FinishReason != gateway.FinishContentFilter {
		result.FinishReason = gateway.FinishToolUse
	}
	if result.FinishReason == gateway.FinishToolUse && len(result.ToolCalls) == 0 {
		return result, failure(gateway.InvalidResponse, op, "tool finish reason without tool calls", nil)
	}
	if result.Text == "" && len(result.ToolCalls) == 0 && result.FinishReason != gateway.FinishMaxTokens && result.FinishReason != gateway.FinishContentFilter {
		return result, failure(gateway.InvalidResponse, op, "upstream returned no visible content", nil)
	}
	result.ProtocolComplete = true
	return result, nil
}
