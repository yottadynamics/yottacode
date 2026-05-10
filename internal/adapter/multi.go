package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MultiStreamer dispatches a ChatStream call across an ordered set of
// candidate Streamers, falling through on early failures according to
// the configured Policy. The whole point of yottacode being multi-
// provider — a single provider degradation should not interrupt the
// user's turn if another configured provider can serve it.
//
// Fallback is intentionally conservative:
//
//   - A failure (EventErr) emitted before any user-visible token has
//     streamed is "early" and triggers fallback to the next candidate.
//   - A failure emitted after one or more EventTokenDelta or
//     EventReasoning events is "mid-stream" and bubbles up as a
//     terminal error. Silently restarting on a different provider
//     mid-reply would corrupt the conversation log and confuse the
//     user — we'd rather surface the error and let the agent loop
//     retry the whole turn.
//   - When the last candidate fails, its error bubbles up regardless
//     of timing — there is nothing left to fall through to.
//
// MultiStreamer emits EventFallback between the failed attempt and the
// next attempt, carrying enough metadata for the TUI to render a loud
// "fell through from X to Y because Z" line. Silent fallback is the
// failure mode this whole design is built to avoid.
type MultiStreamer struct {
	candidates []Candidate
	policy     Policy
	health     *healthTracker
}

// MultiStreamerOption tunes router behavior at construction time.
// Variadic to keep the original two-arg NewMultiStreamer signature
// backward compatible with callers (and tests) that don't care about
// the new knobs.
type MultiStreamerOption func(*MultiStreamer)

// WithHealth attaches a sliding-window health tracker. Zero values for
// Window or Threshold disable tracking — useful when a caller wants to
// pass an "unset" config struct without conditionally choosing between
// two constructors. Defaults are documented on HealthOptions.
func WithHealth(opts HealthOptions) MultiStreamerOption {
	return func(m *MultiStreamer) {
		m.health = newHealthTracker(opts)
	}
}

// withClock injects a clock for tests; deliberately unexported so
// callers stay on time.Now in production.
func withClock(now func() time.Time) MultiStreamerOption {
	return func(m *MultiStreamer) {
		if m.health != nil {
			m.health.now = now
		}
	}
}

// Profile satisfies adapter.Client by returning the first candidate's
// profile. The router does not synthesize a merged profile — capability
// gating across heterogeneous providers is a Phase 2 concern. For step
// 1.5 the convention is: list candidates in capability-aligned order;
// the first one is the representative for system-prompt composition.
func (m *MultiStreamer) Profile() ProviderProfile {
	if len(m.candidates) == 0 {
		return ProviderProfile{}
	}
	return m.candidates[0].Profile
}

// Candidates returns a copy of the configured candidate list. Useful
// for diagnostics and `/router list`-style introspection commands.
func (m *MultiStreamer) Candidates() []Candidate {
	out := make([]Candidate, len(m.candidates))
	copy(out, m.candidates)
	return out
}

// PolicyName returns the name of the active policy. Mirrors what the
// MultiStreamer would emit on EventFallback.FallbackPolicy.
func (m *MultiStreamer) PolicyName() string { return m.policy.Name() }

// NewMultiStreamer constructs a router over the supplied candidates.
// Returns an error if no candidates were supplied or no policy was set
// — both are programmer errors, not runtime conditions, so we fail
// loud at construction rather than at request time. Variadic options
// configure health observation and (in tests) the clock.
func NewMultiStreamer(candidates []Candidate, policy Policy, opts ...MultiStreamerOption) (*MultiStreamer, error) {
	if len(candidates) == 0 {
		return nil, errors.New("adapter: MultiStreamer requires at least one candidate")
	}
	if policy == nil {
		return nil, errors.New("adapter: MultiStreamer requires a non-nil Policy")
	}
	for i, c := range candidates {
		if c.Streamer == nil {
			return nil, fmt.Errorf("adapter: candidate %d has nil Streamer", i)
		}
	}
	m := &MultiStreamer{candidates: candidates, policy: policy}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// ChatStream applies the policy to order the candidates, then drives
// them in sequence until one succeeds, all fail, or ctx is canceled.
// When health observation is enabled, recently-degraded candidates are
// stable-demoted to the back of the order so this request prefers
// candidates that have been working. The returned channel is closed
// when the request finally terminates.
func (m *MultiStreamer) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		ordered := m.policy.Order(m.candidates)
		ordered = m.health.orderByHealth(ordered)
		if len(ordered) == 0 {
			sendEvent(ctx, out, StreamEvent{
				Kind: EventErr,
				Err:  errors.New("adapter: policy returned no candidates"),
			})
			return
		}
		for i, cand := range ordered {
			isLast := i == len(ordered)-1
			done, succeeded, fallbackErr := m.attempt(ctx, cand, messages, tools, out, isLast)
			if m.health != nil {
				if succeeded {
					m.health.recordSuccess(cand.Label)
				} else if fallbackErr != nil || done {
					// Record the failure for either an early
					// fallback (fallbackErr != nil, !done) or a
					// terminal error on this candidate
					// (done && !succeeded).
					m.health.recordFailure(cand.Label)
				}
			}
			if done {
				return
			}
			next := ordered[i+1]
			ev := StreamEvent{
				Kind:           EventFallback,
				FallbackFrom:   cand.Label,
				FallbackTo:     next.Label,
				FallbackPolicy: m.policy.Name(),
			}
			if fallbackErr != nil {
				ev.FallbackReason = fallbackErr.Error()
			}
			if !sendEvent(ctx, out, ev) {
				return
			}
		}
	}()
	return out
}

// attempt streams a single candidate to completion. Returns:
//
//	done=true succeeded=true   EventDone forwarded; healthy outcome.
//	done=true succeeded=false  Terminal error forwarded — either a
//	                            mid-stream failure (tokens already
//	                            streamed; cannot retry without
//	                            corrupting the reply) or a failure on
//	                            the last candidate. Caller must stop
//	                            iterating.
//	done=false succeeded=false Early failure on a non-last candidate.
//	                            fallbackErr is the failure reason, to
//	                            be embedded in the EventFallback the
//	                            caller emits.
//
// The succeeded flag is the health tracker's signal: a successful
// turn clears the candidate's failure history; a failure (early or
// terminal) records one.
func (m *MultiStreamer) attempt(
	ctx context.Context,
	cand Candidate,
	messages []Message,
	tools []Tool,
	out chan<- StreamEvent,
	isLast bool,
) (done bool, succeeded bool, fallbackErr error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sub := cand.Streamer.ChatStream(attemptCtx, messages, tools)
	tokensEmitted := false

	for ev := range sub {
		switch ev.Kind {
		case EventTokenDelta, EventReasoning:
			tokensEmitted = true
			if !sendEvent(ctx, out, ev) {
				return true, false, nil
			}
		case EventDone:
			sendEvent(ctx, out, ev)
			return true, true, nil
		case EventErr:
			if tokensEmitted || isLast {
				sendEvent(ctx, out, ev)
				return true, false, nil
			}
			err := ev.Err
			if err == nil {
				err = errors.New("unspecified stream error")
			}
			return false, false, err
		default:
			if !sendEvent(ctx, out, ev) {
				return true, false, nil
			}
		}
	}

	// Upstream channel closed without EventDone or EventErr. Treat as
	// an unexpected stream end — terminal on the last candidate, a
	// fallback-eligible failure otherwise.
	err := errors.New("adapter: candidate stream ended without EventDone or EventErr")
	if isLast {
		sendEvent(ctx, out, StreamEvent{Kind: EventErr, Err: err})
		return true, false, nil
	}
	return false, false, err
}

// sendEvent forwards ev unless ctx is canceled first. Returns false
// when the send was abandoned, signaling the caller to stop producing.
func sendEvent(ctx context.Context, out chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
