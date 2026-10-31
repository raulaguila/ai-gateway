package gateway

import "time"

type EventType string

const (
	EventTextDelta    EventType = "text_delta"
	EventUsage        EventType = "usage"
	EventCompleted    EventType = "completed"
	EventError        EventType = "error"
	EventProviderWait EventType = "provider_wait"
)

type ProviderWait struct {
	Provider string
	Duration time.Duration
}

// Event payloads belong to the callback; adapters must not mutate them afterward.
// Delivery is synchronous and ordered, with exactly one terminal event after
// stream admission unless the callback itself fails. Pre-admission errors return directly.
// Providers may emit provider_wait before admission; those waits are cancelable.
type Event struct {
	Type     EventType
	Sequence uint64
	Text     string
	Usage    *Usage
	Result   *ChatResult
	Error    *Error
	Wait     *ProviderWait
}
