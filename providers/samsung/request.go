package samsung

import (
	"encoding/json"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

type wireRequest struct {
	InputType       string         `json:"input_type"`
	OutputType      string         `json:"output_type"`
	InputValue      string         `json:"input_value"`
	ComponentInputs map[string]any `json:"component_inputs,omitempty"`
}

func (p *Provider) prepare(request gateway.ChatRequest) (string, wireRequest, error) {
	var err error
	request, err = p.validateRequest(request)
	if err != nil {
		return "", wireRequest{}, err
	}
	return p.buildRequest(request)
}

// ValidateChatRequest checks component prerequisites without serialization or I/O.
func (p *Provider) ValidateChatRequest(request gateway.ChatRequest) error {
	_, err := p.validateRequest(request)
	return err
}

func (p *Provider) validateRequest(request gateway.ChatRequest) (gateway.ChatRequest, error) {
	if request.Model == "" {
		request.Model = p.agentKey
	}
	if !validAgent(request.Model) {
		return request, failure(gateway.InvalidRequest, "", "agent key must be a single nonempty path segment", nil)
	}
	if err := gateway.ValidateChatRequest(request); err != nil {
		return request, err
	}
	if len(request.Tools) > 0 {
		return request, failure(gateway.UnsupportedCapability, "", "Agent Builder does not support normalized tools", nil)
	}
	for _, message := range request.Messages {
		if len(message.ToolCalls) > 0 || message.Role == gateway.RoleTool {
			return request, failure(gateway.UnsupportedCapability, "", "Agent Builder does not support tool history", nil)
		}
	}
	if p.llmID == "" && (request.Temperature != nil || request.TopP != nil || request.MaxOutputTokens != nil) {
		return request, &gateway.Error{Code: gateway.UnsupportedCapability, Field: "LLMComponentID", Message: "generation parameters and token policies require LLMComponentID; configure the flow's LLM component ID"}
	}
	if !p.sendHistory {
		found := false
		for _, message := range request.Messages {
			found = found || message.Role == gateway.RoleUser
		}
		if !found {
			return request, failure(gateway.InvalidRequest, "", "history-disabled generation requires a user message", nil)
		}
	}
	return request, nil
}

func (p *Provider) buildRequest(request gateway.ChatRequest) (string, wireRequest, error) {
	messages := request.Messages
	if !p.sendHistory {
		messages = nil
		for i := len(request.Messages) - 1; i >= 0; i-- {
			if request.Messages[i].Role == gateway.RoleUser {
				messages = request.Messages[i : i+1]
				break
			}
		}
	}
	var dialogue strings.Builder
	for i, message := range messages {
		if i > 0 {
			dialogue.WriteString("\n\n")
		}
		dialogue.WriteString(string(message.Role))
		dialogue.WriteString(": ")
		dialogue.WriteString(message.Content)
	}
	input := dialogue.String()
	if system := strings.TrimSpace(request.System); system != "" {
		input = system + "\n\n---\n\n" + input
	}
	body := wireRequest{InputType: p.inputType, OutputType: p.outputType, InputValue: input, ComponentInputs: map[string]any{}}
	if p.chatInputID != "" {
		component := map[string]any{"session_id": "", "should_store_message": false}
		if p.userName != "" {
			component["sender_name"] = p.userName
		}
		body.ComponentInputs[p.chatInputID] = component
	}
	if p.chatOutputID != "" {
		body.ComponentInputs[p.chatOutputID] = map[string]any{"session_id": "", "should_store_message": false, "sender_name": "AI"}
	}
	if p.memoryID != "" {
		body.ComponentInputs[p.memoryID] = map[string]any{"n_messages": 0, "order": "Ascending"}
	}
	if p.llmID != "" {
		parameters := map[string]any{"temperature": 0.3, "top_p": 0.95, "extra_body": map[string]any{"repetition_penalty": 1.05}}
		if request.Temperature != nil {
			parameters["temperature"] = *request.Temperature
		}
		if request.TopP != nil {
			parameters["top_p"] = *request.TopP
		}
		if request.MaxOutputTokens != nil {
			parameters["max_tokens"] = *request.MaxOutputTokens
		}
		encoded, err := json.Marshal(parameters)
		if err != nil {
			return "", wireRequest{}, failure(gateway.InvalidRequest, "", "generation parameters cannot be encoded", nil)
		}
		body.ComponentInputs[p.llmID] = map[string]any{"parameters": string(encoded)}
	}
	return request.Model, body, nil
}
