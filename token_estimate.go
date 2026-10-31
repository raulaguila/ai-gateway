package gateway

import (
	"context"
	"math"
)

// EstimateInputTokens is the default TokenCounter, with no tokenizer or I/O.
// It estimates each nonempty input field as ceil(UTF-8 bytes / 3), adds 3 tokens
// for the reply, 4 per message (including a nonempty System), and 8 per tool or
// tool call. It includes roles, tool IDs, names, descriptions, schemas, arguments
// and results, but excludes Model and generation parameters (including output).
//
// This model-independent heuristic can overcount or undercount; it is neither
// exact nor a guaranteed upper bound. Provider formatting and remotely added
// context are not known here. Use a model-specific counter when accuracy matters.
// The function does not validate or mutate the request and never updates Usage.
func EstimateInputTokens(ctx context.Context, request ChatRequest) (int, error) {
	if err := checkContext(ctx); err != nil {
		return 0, err
	}
	total := 3
	add := func(overhead int, lengths ...int) error {
		if err := checkContext(ctx); err != nil {
			return err
		}
		for _, length := range lengths {
			tokens := length / 3
			if length%3 != 0 {
				tokens++
			}
			if tokens > math.MaxInt-overhead {
				return &Error{Code: InvalidRequest, Message: "estimated input token count exceeds integer range"}
			}
			overhead += tokens
		}
		if overhead > math.MaxInt-total {
			return &Error{Code: InvalidRequest, Message: "estimated input token count exceeds integer range"}
		}
		total += overhead
		return nil
	}
	if request.System != "" {
		if err := add(4, len(request.System)); err != nil {
			return 0, err
		}
	}
	for _, message := range request.Messages {
		if err := add(4, len(message.Role), len(message.Content), len(message.ToolCallID)); err != nil {
			return 0, err
		}
		for _, call := range message.ToolCalls {
			if err := add(8, len(call.ID), len(call.Name), len(call.Arguments)); err != nil {
				return 0, err
			}
		}
	}
	for _, tool := range request.Tools {
		if err := add(8, len(tool.Name), len(tool.Description), len(tool.Parameters)); err != nil {
			return 0, err
		}
	}
	if err := checkContext(ctx); err != nil {
		return 0, err
	}
	return total, nil
}
