package gateway

import (
	"encoding/json"
	"math"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Model is an alias when used through Gateway and a remote ID on a provider.
type ChatRequest struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []Tool
	Temperature     *float64
	TopP            *float64
	MaxOutputTokens *int
}

type EmbedRequest struct {
	Model string
	Input []string
}

// ValidateChatRequest is also available to adapters implemented in other modules.
func ValidateChatRequest(r ChatRequest) error {
	bad := func(message string) error { return &Error{Code: InvalidRequest, Message: message} }
	if strings.TrimSpace(r.Model) == "" || len(r.Messages) == 0 {
		return bad("model and messages are required")
	}
	if r.Temperature != nil && (math.IsNaN(*r.Temperature) || math.IsInf(*r.Temperature, 0) || *r.Temperature < 0) {
		return bad("temperature must be finite and nonnegative")
	}
	if r.TopP != nil && (math.IsNaN(*r.TopP) || *r.TopP < 0 || *r.TopP > 1) {
		return bad("top_p must be between zero and one")
	}
	if r.MaxOutputTokens != nil && *r.MaxOutputTokens <= 0 {
		return bad("max output tokens must be positive")
	}
	tools := make(map[string]bool, len(r.Tools))
	for _, tool := range r.Tools {
		if strings.TrimSpace(tool.Name) == "" || tools[tool.Name] || !JSONObject(tool.Parameters) {
			return bad("tools require unique names and JSON object schemas")
		}
		tools[tool.Name] = true
	}
	seen, pending := map[string]bool{}, map[string]bool{}
	for _, message := range r.Messages {
		switch message.Role {
		case RoleSystem, RoleUser, RoleAssistant:
			if len(pending) != 0 {
				return bad("tool calls must be followed by their results")
			}
		case RoleTool:
			if !pending[message.ToolCallID] {
				return bad("tool result must reference a pending call")
			}
			delete(pending, message.ToolCallID)
		default:
			return bad("invalid message role")
		}
		if message.Role != RoleTool && message.ToolCallID != "" {
			return bad("tool_call_id is only valid on tool results")
		}
		if len(message.ToolCalls) != 0 && message.Role != RoleAssistant {
			return bad("tool calls require an assistant message")
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || seen[call.ID] || strings.TrimSpace(call.Name) == "" || !JSONObject(call.Arguments) {
				return bad("tool calls require unique IDs, names and JSON object arguments")
			}
			seen[call.ID], pending[call.ID] = true, true
		}
	}
	if len(pending) != 0 {
		return bad("tool calls must have results before another generation")
	}
	return nil
}

func ValidateEmbedRequest(r EmbedRequest) error {
	if strings.TrimSpace(r.Model) == "" || len(r.Input) == 0 {
		return &Error{Code: InvalidRequest, Message: "model and embedding input are required"}
	}
	for _, input := range r.Input {
		if strings.TrimSpace(input) == "" {
			return &Error{Code: InvalidRequest, Message: "embedding input must not be blank"}
		}
	}
	return nil
}

// JSONObject distinguishes an object from null, arrays and scalar JSON values.
func JSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func usesTools(r ChatRequest) bool {
	if len(r.Tools) > 0 {
		return true
	}
	for _, m := range r.Messages {
		if len(m.ToolCalls) > 0 || m.Role == RoleTool {
			return true
		}
	}
	return false
}
