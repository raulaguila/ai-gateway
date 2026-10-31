package gauss

import (
	"context"
	"sync"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
)

type quotaState struct {
	used               []time.Time
	cooldown, lastSeen time.Time
}

// All instances consult the registry on every attempt, including after a wait.
type quotaRegistry struct {
	mu     sync.Mutex
	states map[string]*quotaState
}

var processQuota = quotaRegistry{states: make(map[string]*quotaState)}

func (q *quotaRegistry) reserve(scope string, generation bool, now time.Time) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.states) >= 256 {
		for key, state := range q.states {
			if state.lastSeen.Before(now.Add(-time.Minute)) && !state.cooldown.After(now) {
				delete(q.states, key)
			}
		}
	}
	state := q.states[scope]
	if state == nil {
		state = &quotaState{}
		q.states[scope] = state
	}
	state.lastSeen = now
	first := 0
	for first < len(state.used) && !state.used[first].After(now.Add(-time.Minute)) {
		first++
	}
	state.used = state.used[first:]
	until := state.cooldown
	if generation && len(state.used) >= 3 && state.used[0].Add(time.Minute).After(until) {
		until = state.used[0].Add(time.Minute)
	}
	if until.After(now) {
		return until.Sub(now)
	}
	if generation {
		state.used = append(state.used, now)
	}
	return 0
}

func (q *quotaRegistry) cooldown(scope string, until time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	state := q.states[scope]
	if state == nil {
		state = &quotaState{}
		q.states[scope] = state
	}
	if until.After(state.cooldown) {
		state.cooldown = until
	}
	state.lastSeen = time.Now()
}

func (p *Provider) admit(ctx context.Context, generation bool) error {
	for {
		if ctx.Err() != nil {
			return contextError(ctx.Err())
		}
		delay := processQuota.reserve(p.scope, generation, time.Now())
		if delay == 0 {
			return nil
		}
		if generation && p.failIfBusy {
			return failure(gateway.CapacityUnavailable, "", "Gauss generation quota is busy", nil)
		}
		if notify, _ := ctx.Value(waitKey{}).(func(time.Duration) error); notify != nil {
			if err := notify(delay); err != nil {
				return err
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return contextError(ctx.Err())
		case <-timer.C:
		}
	}
}

type waitKey struct{}
type routingKey struct{}

type waitState struct{ err error }

func (p *Provider) withWait(ctx context.Context, emit func(gateway.Event) error) (context.Context, *waitState) {
	state := &waitState{}
	return context.WithValue(ctx, waitKey{}, func(delay time.Duration) error {
		if p.onWait != nil {
			if err := p.onWait(ctx, delay); err != nil {
				state.err = failure(gateway.CallbackFailed, "", "wait callback failed", err)
				return state.err
			}
		}
		if emit != nil {
			state.err = emit(gateway.Event{Type: gateway.EventProviderWait, Wait: &gateway.ProviderWait{Provider: "gauss", Duration: delay}})
			return state.err
		}
		return nil
	}), state
}
