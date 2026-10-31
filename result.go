package gateway

type UsageSource string

const (
	UsageUnknown   UsageSource = ""
	UsageReported  UsageSource = "reported"
	UsageEstimated UsageSource = "estimated"
)

// A nil Tokens value means unknown; a reported zero remains distinguishable.
type TokenUsage struct {
	Tokens *int64
	Source UsageSource
}

type Usage struct {
	Input  TokenUsage
	Output TokenUsage
}

type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishMaxTokens     FinishReason = "max_tokens"
	FinishToolUse       FinishReason = "tool_use"
	FinishContentFilter FinishReason = "content_filter"
)

type ChatResult struct {
	ID string
	// SessionID is observed metadata; adapters do not reuse it as remote memory.
	SessionID          string
	Text               string
	ToolCalls          []ToolCall
	Usage              Usage
	FinishReason       FinishReason
	NativeFinishReason string
	ProtocolComplete   bool
}

type EmbedResult struct {
	Model   string
	Vectors [][]float64
	Usage   Usage
}
