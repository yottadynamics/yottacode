package agent

import "context"

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
// This is safe because while a tool's Execute is running, the parent
// loop is itself blocked waiting for Execute to return — it is NOT
// reading from decisions, so the runner can read freely. Once Execute
// returns the parent loop resumes ownership of the channel.
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
