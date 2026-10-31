package gauss

import (
	"context"
	"errors"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

func (p *Provider) prepare(request gateway.ChatRequest, stream bool) (gateway.ChatRequest, Model, error) {
	if err := gateway.ValidateChatRequest(request); err != nil {
		return request, Model{}, err
	}
	model, err := p.resolveModel(request.Model)
	if err != nil {
		return request, Model{}, err
	}
	hasTools := len(request.Tools) > 0
	for _, message := range request.Messages {
		hasTools = hasTools || len(message.ToolCalls) > 0 || message.Role == gateway.RoleTool
	}
	if hasTools && (stream || model.Tools == gateway.Unsupported) {
		return request, model, failure(gateway.UnsupportedCapability, "", "tools require supported non-streaming chat", nil)
	}
	request.Model = model.ServingID
	return request, model, nil
}

func emptyVisible(result gateway.ChatResult) bool {
	return strings.TrimSpace(result.Text) == "" && len(result.ToolCalls) == 0 && result.FinishReason != gateway.FinishMaxTokens && result.FinishReason != gateway.FinishContentFilter
}

func (p *Provider) Chat(ctx context.Context, request gateway.ChatRequest) (result gateway.ChatResult, err error) {
	const op = "chat"
	defer func() { err = p.operationError(op, err) }()
	request, model, err := p.prepare(request, false)
	if err != nil {
		return result, err
	}
	ctx, cancel, err := p.operationContext(ctx, false)
	if err != nil {
		return result, err
	}
	defer cancel()
	ctx, wait := p.withWait(ctx, nil)
	ctx = context.WithValue(ctx, routingKey{}, model.RoutingID)
	result, err = p.inner.Chat(ctx, request)
	if wait.err != nil {
		err = wait.err
	}
	var filter visibleFilter
	visible, filterErr := filter.write(result.Text)
	filter.finish()
	result.Text = visible
	if filterErr != nil {
		result.ProtocolComplete = false
		return result, filterErr
	}
	if err == nil && emptyVisible(result) {
		result.ProtocolComplete = false
		return result, failure(gateway.InvalidResponse, op, "Gauss returned no visible content", nil)
	}
	return result, err
}

func (p *Provider) Stream(ctx context.Context, request gateway.ChatRequest, callback func(gateway.Event) error) (result gateway.ChatResult, err error) {
	const op = "stream"
	defer func() { err = p.operationError(op, err) }()
	if callback == nil {
		return result, failure(gateway.InvalidRequest, op, "stream callback is required", nil)
	}
	request, model, err := p.prepare(request, true)
	if err != nil {
		return result, err
	}
	ctx, cancel, err := p.operationContext(ctx, true)
	if err != nil {
		return result, err
	}
	defer cancel()
	var sequence uint64
	var callbackError error
	emit := func(event gateway.Event) error {
		sequence++
		event.Sequence = sequence
		if callbackErr := callback(event); callbackErr != nil {
			cancel()
			callbackError = failure(gateway.CallbackFailed, op, "stream callback failed", callbackErr)
			return callbackError
		}
		return nil
	}
	ctx, wait := p.withWait(ctx, emit)
	ctx = context.WithValue(ctx, routingKey{}, model.RoutingID)
	var visible strings.Builder
	var filter visibleFilter
	var filteringError error
	result, err = p.inner.Stream(ctx, request, func(event gateway.Event) error {
		switch event.Type {
		case gateway.EventTextDelta:
			text, filterErr := filter.write(event.Text)
			visible.WriteString(text)
			if filterErr != nil {
				filteringError = filterErr
				return filterErr
			}
			if text == "" {
				return nil
			}
			event.Text = text
		case gateway.EventCompleted:
			filter.finish()
			snapshot := *event.Result
			snapshot.Text = visible.String()
			if emptyVisible(snapshot) {
				filteringError = failure(gateway.InvalidResponse, op, "Gauss returned no visible content", nil)
				return filteringError
			}
			event.Result = &snapshot
		case gateway.EventError:
			var problem *gateway.Error
			if errors.As(p.operationError(op, event.Error), &problem) {
				event.Error = problem
			}
		}
		return emit(event)
	})
	filter.finish()
	result.Text = visible.String()
	if callbackError != nil {
		return result, callbackError
	}
	if wait.err != nil {
		return result, wait.err
	}
	if filteringError != nil {
		result.ProtocolComplete = false
		var problem *gateway.Error
		_ = errors.As(p.operationError(op, filteringError), &problem)
		copy := *problem
		if callbackErr := emit(gateway.Event{Type: gateway.EventError, Error: &copy}); callbackErr != nil {
			return result, callbackErr
		}
		return result, problem
	}
	return result, err
}
