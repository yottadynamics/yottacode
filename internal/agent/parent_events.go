package agent

import (
	"context"
	"sync"
)

// parentEventsKey is the context key the loop uses to make the parent's
// events channel available to tool implementations that need to emit
// side-channel events alongside their normal tool result string.
// The only consumer today is AgentTool, which forwards subagent-progress
// events to the parent's UI without polluting the parent model's
// adapter context.
type parentEventsKey struct{}

// WithParentEvents attaches the parent loop's events channel to ctx so
// downstream Tool.Execute calls can pull it via ParentEvents(ctx). The
// channel is send-only; tools may push Subagent* / progress events
// onto it without coordinating with the loop.
//
// Tools that don't need this seam should ignore the helper — the
// normal tool-result return value is still the primary output path.
func WithParentEvents(ctx context.Context, events chan<- Event) context.Context {
	if events == nil {
		return ctx
	}
	return context.WithValue(ctx, parentEventsKey{}, events)
}

// ParentEvents recovers the channel attached by WithParentEvents, or
// nil when no parent loop is on the stack (tests, oneshot paths that
// haven't wired it in). Always check for nil before sending.
func ParentEvents(ctx context.Context) chan<- Event {
	ch, _ := ctx.Value(parentEventsKey{}).(chan<- Event)
	return ch
}

// parentDecisionsKey is the context key the loop uses to expose the
// parent's approval-decisions channel to Tool implementations. The
// only consumer today is AgentTool: when a foreground subagent's
// child loop emits ApprovalNeeded, the runner forwards it onto the
// parent's events channel and waits on the parent's decisions
// channel for the user's verdict, then routes the answer to the
// child's own decisions channel.
//
// On the serial execution path this is safe because while a tool's
// Execute is running, the parent loop is itself blocked waiting for
// Execute to return — it is NOT reading from decisions, so the runner
// can read freely. On the PARALLEL path that invariant does not hold:
// several Execute calls run at once, and more than one may want to read
// decisions (a forwarded subagent approval, a permission Ask prompt, a
// path-trust elevation). The approval gate (WithApprovalGate) serializes
// those round-trips so the single decisions channel + single TUI modal
// still serve one request at a time. See lockApprovalGate.
type parentDecisionsKey struct{}

// WithParentDecisions attaches the parent loop's decisions channel
// to ctx. Tools that don't need to forward approvals should ignore
// this seam — the channel is receive-only and may be nil.
func WithParentDecisions(ctx context.Context, decisions <-chan Decision) context.Context {
	if decisions == nil {
		return ctx
	}
	return context.WithValue(ctx, parentDecisionsKey{}, decisions)
}

// ParentDecisions recovers the channel attached by WithParentDecisions,
// or nil when no parent loop is on the stack.
func ParentDecisions(ctx context.Context) <-chan Decision {
	ch, _ := ctx.Value(parentDecisionsKey{}).(<-chan Decision)
	return ch
}

// questionnaireAnswersKey is the context key exposing the per-turn
// LoopConfig.QuestionAnswers channel to AskUserQuestionTool.Execute.
// Mirrors the decisions seam: the tool emits UserQuestionsNeeded on
// the parent events channel and blocks here for the TUI's structured
// reply. Nil (oneshot, subagent child loops, tests) means no
// interactive user is attached — the tool returns an instructive
// error instead of hanging.
type questionnaireAnswersKey struct{}

// WithQuestionnaireAnswers attaches the questionnaire reply channel to
// ctx. Tools other than ask_user_question should ignore this seam.
func WithQuestionnaireAnswers(ctx context.Context, answers <-chan QuestionnaireAnswers) context.Context {
	if answers == nil {
		return ctx
	}
	return context.WithValue(ctx, questionnaireAnswersKey{}, answers)
}

// QuestionnaireAnswersChan recovers the channel attached by
// WithQuestionnaireAnswers, or nil when no interactive consumer is on
// the stack.
func QuestionnaireAnswersChan(ctx context.Context) <-chan QuestionnaireAnswers {
	ch, _ := ctx.Value(questionnaireAnswersKey{}).(<-chan QuestionnaireAnswers)
	return ch
}

// approvalGateKey is the context key carrying the per-parallel-batch
// mutex that serializes user-interaction round-trips.
type approvalGateKey struct{}

// WithApprovalGate attaches a mutex that serializes request→decision
// round-trips across tools running concurrently in one parallel batch.
// The parent's decisions channel and the TUI's single approval modal can
// each serve only one request at a time; without this lock two parallel
// workers reading decisions would misroute the user's answer (authorize
// the wrong call) or deadlock (one worker waits forever for a second
// decision the user never gives). A nil gate — the serial path, where
// there is no contention — is left unattached and locking becomes a
// no-op.
func WithApprovalGate(ctx context.Context, gate *sync.Mutex) context.Context {
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, approvalGateKey{}, gate)
}

// lockApprovalGate acquires the approval gate from ctx (if one is
// attached) and returns a function that releases it. The returned
// unlock func is always safe to call exactly once, whether or not a gate
// was present, so callers can wrap a single round-trip uniformly:
//
//	unlock := lockApprovalGate(ctx)
//	... emit ApprovalNeeded; receive the decision ...
//	unlock()
//
// Hold the lock only for the duration of one request→decision exchange,
// never across a tool's full Execute — that would serialize the whole
// parallel batch instead of just its user-interaction points.
func lockApprovalGate(ctx context.Context) func() {
	g, _ := ctx.Value(approvalGateKey{}).(*sync.Mutex)
	if g == nil {
		return func() {}
	}
	g.Lock()
	var once sync.Once
	return func() { once.Do(g.Unlock) }
}

// withoutApprovalGate returns a ctx with any approval gate DETACHED, so code
// running under it treats lockApprovalGate as a no-op.
//
// A subagent's own loop must run under such a ctx. The parent batch's gate may
// only be held by the parent-side forwarder (the runChild drain loop) during a
// forwarded approval round-trip. If the child's own loop also acquired the same
// gate, it would lock the gate and then block waiting for its decision — while
// the drain loop, the only thing that can feed that decision, blocks trying to
// lock the same gate. Deadlock. Detaching the gate from the child loop leaves
// only the forwarder holding it, which is exactly the serialization the gate is
// for.
func withoutApprovalGate(ctx context.Context) context.Context {
	if ctx.Value(approvalGateKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, approvalGateKey{}, (*sync.Mutex)(nil))
}
