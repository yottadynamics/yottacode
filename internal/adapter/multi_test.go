package adapter

import (
	"context"
	"errors"
	"testing"
	"time"
)

// scriptedStreamer replays a fixed list of StreamEvents on each
// ChatStream call and is the test workhorse for MultiStreamer.
type scriptedStreamer struct {
	events []StreamEvent
	// hold, when set, blocks the goroutine until the channel closes.
	// Used by the cancellation test to keep an attempt "in flight".
	hold <-chan struct{}
}

func (s *scriptedStreamer) ChatStream(ctx context.Context, _ []Message, _ []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, len(s.events)+1)
	go func() {
		defer close(out)
		for _, ev := range s.events {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
		if s.hold != nil {
			select {
			case <-ctx.Done():
			case <-s.hold:
			}
		}
	}()
	return out
}

func collect(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("timed out collecting events; got %d so far: %+v", len(out), out)
		}
	}
}

func eventKinds(events []StreamEvent) []StreamEventKind {
	out := make([]StreamEventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

// TestMultiStreamer_SuccessOnFirstCandidate confirms the happy path:
// one candidate returns EventDone, no fallback events, no further
// candidates consulted.
func TestMultiStreamer_SuccessOnFirstCandidate(t *testing.T) {
	first := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventTokenDelta, Token: "hello"},
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "hello"}},
	}}
	second := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventErr, Err: errors.New("should not be reached")},
	}}

	router, err := NewMultiStreamer(
		[]Candidate{{Streamer: first, Label: "a"}, {Streamer: second, Label: "b"}},
		FallbackChain{},
	)
	if err != nil {
		t.Fatalf("NewMultiStreamer: %v", err)
	}

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	want := []StreamEventKind{EventTokenDelta, EventDone}
	if !equalKinds(eventKinds(got), want) {
		t.Errorf("kinds = %v, want %v (full events: %+v)", eventKinds(got), want, got)
	}
}

// TestMultiStreamer_EarlyFailureFallsThrough confirms that an EventErr
// before any tokens triggers fallback to the next candidate, with a
// visible EventFallback emitted between them.
func TestMultiStreamer_EarlyFailureFallsThrough(t *testing.T) {
	first := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventErr, Err: errors.New("rate limited")},
	}}
	second := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventTokenDelta, Token: "world"},
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "world"}},
	}}

	router, _ := NewMultiStreamer(
		[]Candidate{
			{Streamer: first, Label: "primary"},
			{Streamer: second, Label: "secondary"},
		},
		FallbackChain{},
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	wantKinds := []StreamEventKind{EventFallback, EventTokenDelta, EventDone}
	if !equalKinds(eventKinds(got), wantKinds) {
		t.Fatalf("kinds = %v, want %v (full events: %+v)", eventKinds(got), wantKinds, got)
	}

	fb := got[0]
	if fb.FallbackFrom != "primary" || fb.FallbackTo != "secondary" {
		t.Errorf("fallback labels = %q→%q, want primary→secondary", fb.FallbackFrom, fb.FallbackTo)
	}
	if fb.FallbackPolicy != "fallback-chain" {
		t.Errorf("fallback policy = %q, want fallback-chain", fb.FallbackPolicy)
	}
	if fb.FallbackReason != "rate limited" {
		t.Errorf("fallback reason = %q, want %q", fb.FallbackReason, "rate limited")
	}
}

// TestMultiStreamer_MidStreamFailureIsTerminal confirms that once a
// candidate has streamed any tokens, a subsequent EventErr surfaces as
// terminal — silent restart on a different candidate would corrupt the
// user-visible reply.
func TestMultiStreamer_MidStreamFailureIsTerminal(t *testing.T) {
	first := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventTokenDelta, Token: "partial answer"},
		{Kind: EventErr, Err: errors.New("connection reset")},
	}}
	second := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "should not be reached"}},
	}}

	router, _ := NewMultiStreamer(
		[]Candidate{
			{Streamer: first, Label: "a"},
			{Streamer: second, Label: "b"},
		},
		FallbackChain{},
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	wantKinds := []StreamEventKind{EventTokenDelta, EventErr}
	if !equalKinds(eventKinds(got), wantKinds) {
		t.Errorf("kinds = %v, want %v (full events: %+v)", eventKinds(got), wantKinds, got)
	}
}

// TestMultiStreamer_ReasoningCountsAsEmittedTokens confirms that an
// EventReasoning before EventErr is treated like EventTokenDelta —
// the user has already seen "thinking" tokens, falling back would be
// confusing.
func TestMultiStreamer_ReasoningCountsAsEmittedTokens(t *testing.T) {
	first := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventReasoning, Token: "let me think..."},
		{Kind: EventErr, Err: errors.New("upstream 500")},
	}}
	second := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "should not be reached"}},
	}}

	router, _ := NewMultiStreamer(
		[]Candidate{
			{Streamer: first, Label: "a"},
			{Streamer: second, Label: "b"},
		},
		FallbackChain{},
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	wantKinds := []StreamEventKind{EventReasoning, EventErr}
	if !equalKinds(eventKinds(got), wantKinds) {
		t.Errorf("kinds = %v, want %v (full events: %+v)", eventKinds(got), wantKinds, got)
	}
}

// TestMultiStreamer_AllCandidatesFail confirms that when every
// candidate errors early, the last candidate's error bubbles up as
// terminal — there is nothing left to fall through to.
func TestMultiStreamer_AllCandidatesFail(t *testing.T) {
	first := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventErr, Err: errors.New("first failed")},
	}}
	second := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventErr, Err: errors.New("second failed")},
	}}

	router, _ := NewMultiStreamer(
		[]Candidate{
			{Streamer: first, Label: "a"},
			{Streamer: second, Label: "b"},
		},
		FallbackChain{},
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	wantKinds := []StreamEventKind{EventFallback, EventErr}
	if !equalKinds(eventKinds(got), wantKinds) {
		t.Fatalf("kinds = %v, want %v (full events: %+v)", eventKinds(got), wantKinds, got)
	}
	if got[1].Err == nil || got[1].Err.Error() != "second failed" {
		t.Errorf("terminal error = %v, want %q", got[1].Err, "second failed")
	}
}

// TestMultiStreamer_StreamEndsWithoutDone covers a misbehaving
// upstream that closes its channel without ever emitting EventDone or
// EventErr. Not last → fall through. Last → synthesize a terminal
// error so the agent loop does not hang waiting for a Done that never
// arrives.
func TestMultiStreamer_StreamEndsWithoutDoneFallsThrough(t *testing.T) {
	first := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventTokenDelta, Token: ""}, // no-op delta, but no Done
	}}
	second := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "ok"}},
	}}
	// First candidate's events do emit a token, so a fallback is
	// already disqualified by the mid-stream rule. Use a candidate
	// with NO events to test the genuine "closed without anything"
	// case.
	first = &scriptedStreamer{events: nil}

	router, _ := NewMultiStreamer(
		[]Candidate{
			{Streamer: first, Label: "a"},
			{Streamer: second, Label: "b"},
		},
		FallbackChain{},
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	wantKinds := []StreamEventKind{EventFallback, EventDone}
	if !equalKinds(eventKinds(got), wantKinds) {
		t.Errorf("kinds = %v, want %v (full events: %+v)", eventKinds(got), wantKinds, got)
	}
}

func TestMultiStreamer_StreamEndsWithoutDoneOnLastCandidateIsTerminal(t *testing.T) {
	first := &scriptedStreamer{events: nil}
	router, _ := NewMultiStreamer(
		[]Candidate{{Streamer: first, Label: "only"}},
		FallbackChain{},
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	wantKinds := []StreamEventKind{EventErr}
	if !equalKinds(eventKinds(got), wantKinds) {
		t.Errorf("kinds = %v, want %v (full events: %+v)", eventKinds(got), wantKinds, got)
	}
}

// TestMultiStreamer_CtxCancelMidStream confirms that canceling the
// caller's context aborts an in-flight attempt cleanly rather than
// hanging — the per-attempt context is derived from the caller's, so
// cancellation propagates.
func TestMultiStreamer_CtxCancelMidStream(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)

	first := &scriptedStreamer{
		events: []StreamEvent{
			{Kind: EventTokenDelta, Token: "thinking..."},
		},
		hold: hold,
	}
	router, _ := NewMultiStreamer(
		[]Candidate{{Streamer: first, Label: "slow"}},
		FallbackChain{},
	)

	ctx, cancel := context.WithCancel(context.Background())
	ch := router.ChatStream(ctx, nil, nil)

	// Drain the first event so we know the goroutine started.
	select {
	case ev := <-ch:
		if ev.Kind != EventTokenDelta {
			t.Fatalf("first event = %v, want EventTokenDelta", ev.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive first event in time")
	}

	cancel()

	// The router goroutine should now exit cleanly. Drain until the
	// channel closes; if it doesn't close within the timeout, we're
	// hanging.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("router did not close output channel after ctx cancel")
		}
	}
}

// TestNewMultiStreamer_ConstructorValidation confirms we fail loud at
// construction rather than at first request when configuration is
// obviously broken.
func TestNewMultiStreamer_ConstructorValidation(t *testing.T) {
	t.Run("no candidates", func(t *testing.T) {
		if _, err := NewMultiStreamer(nil, FallbackChain{}); err == nil {
			t.Error("expected error for empty candidates, got nil")
		}
	})
	t.Run("nil policy", func(t *testing.T) {
		if _, err := NewMultiStreamer([]Candidate{{Streamer: &scriptedStreamer{}}}, nil); err == nil {
			t.Error("expected error for nil policy, got nil")
		}
	})
	t.Run("nil streamer in candidate", func(t *testing.T) {
		_, err := NewMultiStreamer([]Candidate{{Streamer: nil, Label: "broken"}}, FallbackChain{})
		if err == nil {
			t.Error("expected error for nil streamer, got nil")
		}
	})
}

// TestMultiStreamer_SatisfiesClientInterface confirms MultiStreamer can
// stand in for adapter.Client wherever the TUI/oneshot expect one. The
// representative Profile is the first candidate's — capability gating
// across heterogeneous providers is a Phase 2 concern.
func TestMultiStreamer_SatisfiesClientInterface(t *testing.T) {
	first := &scriptedStreamer{}
	second := &scriptedStreamer{}
	router, err := NewMultiStreamer(
		[]Candidate{
			{Streamer: first, Label: "primary", Profile: ProviderProfile{Provider: ProviderAnthropic}},
			{Streamer: second, Label: "fallback", Profile: ProviderProfile{Provider: ProviderOpenAI}},
		},
		FallbackChain{},
	)
	if err != nil {
		t.Fatalf("NewMultiStreamer: %v", err)
	}

	var _ Client = router // compile-time check
	if got := router.Profile().Provider; got != ProviderAnthropic {
		t.Errorf("Profile().Provider = %q, want %q (first candidate's)", got, ProviderAnthropic)
	}
	if got := router.PolicyName(); got != "fallback-chain" {
		t.Errorf("PolicyName() = %q, want fallback-chain", got)
	}
	if got := len(router.Candidates()); got != 2 {
		t.Errorf("Candidates() len = %d, want 2", got)
	}
}

// TestMultiStreamer_PolicyOrderIsApplied confirms CheapFirst actually
// reorders candidates before dispatch — the cheapest candidate is
// tried first regardless of declaration order.
func TestMultiStreamer_PolicyOrderIsApplied(t *testing.T) {
	expensive := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "expensive answer"}},
	}}
	cheap := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "cheap answer"}},
	}}

	router, _ := NewMultiStreamer(
		[]Candidate{
			{Streamer: expensive, Label: "expensive", Tier: TierExpensive},
			{Streamer: cheap, Label: "cheap", Tier: TierCheap},
		},
		CheapFirst{},
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	if len(got) != 1 || got[0].Kind != EventDone {
		t.Fatalf("expected single EventDone, got %+v", got)
	}
	if got[0].Final == nil || got[0].Final.Content != "cheap answer" {
		t.Errorf("got Final.Content = %q, want %q (CheapFirst should have run cheap first)",
			got[0].Final.Content, "cheap answer")
	}
}

// TestMultiStreamer_HealthDemotesDegradedCandidate confirms the
// router reorders candidates when one has been failing repeatedly:
// after enough early failures within the window, the next request
// puts the degraded candidate at the back.
func TestMultiStreamer_HealthDemotesDegradedCandidate(t *testing.T) {
	failer := newAlwaysFailingStreamer(errors.New("rate limited"))
	winner := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "ok"}},
	}}

	router, err := NewMultiStreamer(
		[]Candidate{
			{Streamer: failer, Label: "primary"},
			{Streamer: winner, Label: "backup"},
		},
		FallbackChain{},
		WithHealth(HealthOptions{Window: time.Minute, Threshold: 2}),
	)
	if err != nil {
		t.Fatalf("NewMultiStreamer: %v", err)
	}

	// First two requests: primary fails, backup succeeds. Each request
	// records one failure for primary; after the second, primary is
	// at threshold → degraded.
	for i := 0; i < 2; i++ {
		got := collect(t, router.ChatStream(context.Background(), nil, nil))
		// First event is EventFallback, second is EventDone from backup.
		if len(got) != 2 || got[0].Kind != EventFallback || got[1].Kind != EventDone {
			t.Fatalf("turn %d: kinds = %v, want [EventFallback EventDone]",
				i, eventKinds(got))
		}
		// Reset the winner for the next request — scriptedStreamer
		// emits the same events on every ChatStream call so this works
		// without state.
	}

	// Reset the winner's state by giving it fresh events. Then the
	// router orders backup ahead of primary (primary now degraded).
	winner.events = []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "fresh"}},
	}
	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	// With backup-first, no fallback should occur on this turn.
	if len(got) != 1 || got[0].Kind != EventDone {
		t.Errorf("after degradation, kinds = %v, want [EventDone] (backup should be tried first now)",
			eventKinds(got))
	}
}

// TestMultiStreamer_HealthRestoresAfterSuccess confirms that a single
// successful turn clears a candidate's failure history — recovery is
// fast, not gated on the window sliding.
func TestMultiStreamer_HealthRestoresAfterSuccess(t *testing.T) {
	flaky := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventErr, Err: errors.New("transient 500")},
	}}
	router, _ := NewMultiStreamer(
		[]Candidate{{Streamer: flaky, Label: "flaky"}},
		FallbackChain{},
		WithHealth(HealthOptions{Window: time.Minute, Threshold: 2}),
	)

	// Two failures → degraded.
	for i := 0; i < 2; i++ {
		_ = collect(t, router.ChatStream(context.Background(), nil, nil))
	}
	if !router.health.degraded("flaky") {
		t.Fatal("expected flaky to be degraded after 2 failures")
	}

	// Now make it succeed.
	flaky.events = []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "back"}},
	}
	_ = collect(t, router.ChatStream(context.Background(), nil, nil))

	if router.health.degraded("flaky") {
		t.Error("recordSuccess should clear degraded state on next request")
	}
}

// TestMultiStreamer_HealthRecordsMidStreamFailures confirms that even
// a candidate that streamed tokens before failing is recorded as
// failed — the request did not succeed regardless of how far it got.
func TestMultiStreamer_HealthRecordsMidStreamFailures(t *testing.T) {
	mid := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventTokenDelta, Token: "partial"},
		{Kind: EventErr, Err: errors.New("connection reset")},
	}}
	router, _ := NewMultiStreamer(
		[]Candidate{{Streamer: mid, Label: "midfail"}},
		FallbackChain{},
		WithHealth(HealthOptions{Window: time.Minute, Threshold: 1}),
	)

	got := collect(t, router.ChatStream(context.Background(), nil, nil))
	// The event sequence is unchanged from the no-health-tracking
	// case: tokens flow through, then a terminal EventErr. The new
	// behavior is that the failure was *recorded*, not that the
	// stream shape changed.
	wantKinds := []StreamEventKind{EventTokenDelta, EventErr}
	if !equalKinds(eventKinds(got), wantKinds) {
		t.Errorf("kinds = %v, want %v", eventKinds(got), wantKinds)
	}
	if !router.health.degraded("midfail") {
		t.Error("mid-stream failure should still record into health tracker")
	}
}

// TestMultiStreamer_HealthDisabledByDefault confirms the original
// two-arg constructor produces a router with no observation — exactly
// the same behavior the package exposed before this change.
func TestMultiStreamer_HealthDisabledByDefault(t *testing.T) {
	failer := newAlwaysFailingStreamer(errors.New("nope"))
	winner := &scriptedStreamer{events: []StreamEvent{
		{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "ok"}},
	}}
	router, _ := NewMultiStreamer(
		[]Candidate{
			{Streamer: failer, Label: "primary"},
			{Streamer: winner, Label: "backup"},
		},
		FallbackChain{},
	)
	if router.health.enabled() {
		t.Error("default constructor should not enable health observation")
	}

	// Drive a few turns. Without observation, primary stays at the
	// front, fallback fires every turn.
	for i := 0; i < 3; i++ {
		winner.events = []StreamEvent{
			{Kind: EventDone, Final: &Message{Role: RoleAssistant, Content: "ok"}},
		}
		got := collect(t, router.ChatStream(context.Background(), nil, nil))
		if len(got) == 0 || got[0].Kind != EventFallback {
			t.Errorf("turn %d without health: first event = %v, want EventFallback "+
				"(no demotion = primary still tried first)",
				i, got[0].Kind)
		}
	}
}

// newAlwaysFailingStreamer returns a streamer whose ChatStream always
// emits a single EventErr. Used as the persistently-broken candidate
// in health-demotion tests; the existing scriptedStreamer drains its
// events on the first call, which would not exercise the per-request
// recording loop.
func newAlwaysFailingStreamer(err error) Streamer {
	return alwaysFailing{err: err}
}

type alwaysFailing struct{ err error }

func (a alwaysFailing) ChatStream(ctx context.Context, _ []Message, _ []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 1)
	go func() {
		defer close(out)
		select {
		case out <- StreamEvent{Kind: EventErr, Err: a.err}:
		case <-ctx.Done():
		}
	}()
	return out
}

func equalKinds(got, want []StreamEventKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
