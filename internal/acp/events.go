package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// toolCallTracker correlates agent.ToolStart/agent.ToolResult/
// agent.ApprovalNeeded events — none of which carry a call id — with the
// ACP ToolCallId the client needs to pair a permission request, its
// `tool_call`, and its later `tool_call_update`. Scoped to one Prompt
// call (one turn), not the session.
//
// Two-stage by design: idFor is keyed by toolName+argsJSON so an
// ApprovalNeeded and the ToolStart that follows it (same call) resolve
// to the same id; once ToolStart actually claims that id, it moves into
// a per-toolName FIFO queue for result() to pop from, because
// agent.ToolResult carries only ToolName — not ArgsJSON — so that's all
// there is to correlate on for the result side.
//
// Limitation, accepted deliberately: two genuinely concurrent calls to
// the same tool with the same exact args (rare) can have their
// start/result pairing swapped in the client's tool-call UI. This is
// cosmetic only — the actual tool result content still reaches the
// model via the ordinary adapter.Message flow, entirely untouched by
// this tracker.
type toolCallTracker struct {
	mu      sync.Mutex
	nextID  uint64
	pending map[string]coderacp.ToolCallId   // keyed by toolName+"\x00"+argsJSON, cleared once ToolStart claims it
	queued  map[string][]coderacp.ToolCallId // keyed by toolName, FIFO for result()
}

func newToolCallTracker() *toolCallTracker {
	return &toolCallTracker{
		pending: make(map[string]coderacp.ToolCallId),
		queued:  make(map[string][]coderacp.ToolCallId),
	}
}

func toolCallKey(toolName, argsJSON string) string { return toolName + "\x00" + argsJSON }

// idFor returns the id for a pending (not-yet-started) call, creating
// one if this is the first time this exact toolName+argsJSON has been
// seen. Used by both the ApprovalNeeded handler and ToolStart (whichever
// fires first for a given call wins the allocation; the other reuses it).
func (t *toolCallTracker) idFor(toolName, argsJSON string) coderacp.ToolCallId {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := toolCallKey(toolName, argsJSON)
	if id, ok := t.pending[key]; ok {
		return id
	}
	t.nextID++
	id := coderacp.ToolCallId(fmt.Sprintf("call_%d", t.nextID))
	t.pending[key] = id
	return id
}

// claimForStart resolves the id for this call (allocating one if
// ToolStart is firing without a preceding ApprovalNeeded) and moves it
// into the per-toolName result queue.
func (t *toolCallTracker) claimForStart(toolName, argsJSON string) coderacp.ToolCallId {
	id := t.idFor(toolName, argsJSON)
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, toolCallKey(toolName, argsJSON))
	t.queued[toolName] = append(t.queued[toolName], id)
	return id
}

// result pops the oldest queued id for toolName, matching
// agent.ToolResult's shape (ToolName only — no ArgsJSON to key on).
func (t *toolCallTracker) result(toolName string) (coderacp.ToolCallId, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	q := t.queued[toolName]
	if len(q) == 0 {
		return "", false
	}
	id := q[0]
	t.queued[toolName] = q[1:]
	return id, true
}

// emitUpdate translates one yottacode agent event into zero or one ACP
// session/update notification. ApprovalNeeded and
// PathTrustElevationNeeded never reach here — they need a
// decisions-channel round trip (see acpSession.requestPermission /
// requestPathElevation in permissions.go), not a fire-and-forget update.
func emitUpdate(ctx context.Context, conn *coderacp.AgentSideConnection, sessionID string, tracker *toolCallTracker, ev agent.Event) error {
	sid := coderacp.SessionId(sessionID)
	send := func(u coderacp.SessionUpdate) error {
		return conn.SessionUpdate(ctx, coderacp.SessionNotification{SessionId: sid, Update: u})
	}
	diag := func(format string, args ...any) error {
		return send(coderacp.UpdateAgentMessageText(fmt.Sprintf(format, args...)))
	}

	switch e := ev.(type) {
	case agent.ContentToken:
		return send(coderacp.UpdateAgentMessageText(e.Text))
	case agent.ReasoningToken:
		return send(coderacp.UpdateAgentThoughtText(e.Text))

	case agent.ToolStart:
		id := tracker.claimForStart(e.ToolName, e.ArgsJSON)
		return send(coderacp.StartToolCall(id, e.ToolName,
			coderacp.WithStartStatus(coderacp.ToolCallStatusInProgress),
			coderacp.WithStartRawInput(json.RawMessage(e.ArgsJSON)),
		))
	case agent.ToolResult:
		id, ok := tracker.result(e.ToolName)
		if !ok {
			return nil // no matching start — drop rather than send a bogus update
		}
		status := coderacp.ToolCallStatusCompleted
		if e.Errored {
			status = coderacp.ToolCallStatusFailed
		}
		return send(coderacp.UpdateToolCall(id,
			coderacp.WithUpdateStatus(status),
			coderacp.WithUpdateContent([]coderacp.ToolCallContent{coderacp.ToolContent(coderacp.TextBlock(e.Output))}),
		))

	case agent.TodoUpdate:
		entries := make([]coderacp.PlanEntry, len(e.Todos))
		for i, td := range e.Todos {
			entries[i] = coderacp.PlanEntry{
				Content:  td.Content,
				Priority: coderacp.PlanEntryPriorityMedium, // agent.Todo carries no priority
				Status:   todoStatusToACP(td.Status),
			}
		}
		return send(coderacp.UpdatePlan(entries...))

	case agent.ApprovalAuto:
		// No distinct wire representation: the ToolStart that
		// immediately follows carries the tool_call notification.
		return nil

	case agent.SubagentStart:
		label := "foreground"
		if e.Background {
			label = "background"
		}
		return diag("[subagent:%s] start (%s) — %s\n", e.AgentType, label, truncateOneLine(e.Prompt, 120))
	case agent.SubagentProgress:
		return diag("[subagent:%s] %s\n", e.AgentType, e.Activity)
	case agent.SubagentDone:
		tag := "done"
		if e.Errored {
			tag = "errored"
		}
		return diag("[subagent:%s] %s in %s\n", e.AgentType, tag, e.Duration)
	case agent.SubagentBackgroundDone:
		return diag("[subagent:%s] background %s done\n", e.AgentType, e.TaskID)

	case agent.ErrorEvent:
		return diag("[error] %s\n", e.Err.Error())
	case agent.Fallback:
		if e.Reason != "" {
			return diag("[fallback] %s → %s [%s]: %s\n", e.From, e.To, e.Policy, e.Reason)
		}
		return diag("[fallback] %s → %s [%s]\n", e.From, e.To, e.Policy)
	case agent.IterCap:
		return diag("[agent] hit %d/%d iterations\n", e.Max, e.Max)
	case agent.ProviderToolCall:
		if e.Detail != "" {
			return diag("[provider-tool] %s %s: %s\n", e.ToolName, e.Phase, e.Detail)
		}
		return diag("[provider-tool] %s %s\n", e.ToolName, e.Phase)

	// Diagnostic/internal event kinds beyond the original roadmap
	// mapping table — no ACP counterpart in v1, dropped silently rather
	// than spamming agent_message_chunk noise for every token-budget
	// tick.
	case agent.IterationStart, agent.IterationContinue, agent.ContextUsage,
		agent.StreamProgress, agent.ContextCompacted, agent.CwdChanged,
		agent.CheckpointInfo, agent.UserMessageAppended, agent.AssistantMessage:
		return nil

	// TurnDone/TurnInterrupted map to the Prompt response itself (see
	// acpSession.prompt), not to a session/update.
	case agent.TurnDone, agent.TurnInterrupted:
		return nil

	default:
		return nil
	}
}

func todoStatusToACP(s agent.TodoStatus) coderacp.PlanEntryStatus {
	switch s {
	case agent.TodoInProgress:
		return coderacp.PlanEntryStatusInProgress
	case agent.TodoCompleted, agent.TodoSkipped:
		// TodoSkipped has no ACP equivalent; "completed" is the closer
		// fit (off the active list) than "pending" (still to do).
		return coderacp.PlanEntryStatusCompleted
	default:
		return coderacp.PlanEntryStatusPending
	}
}

// truncateOneLine returns at most max chars of s, collapsing any
// newlines to spaces so a multi-line subagent prompt renders as a
// single diagnostic line. Duplicated from internal/oneshot (private,
// trivial, and this repo's convention is small private duplicates over
// a shared testutil-style package — see internal/oneshot/oneshot_test.go's
// own comment on the same trade-off).
func truncateOneLine(s string, max int) string {
	clean := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			clean = append(clean, ' ')
			continue
		}
		clean = append(clean, r)
	}
	if len(clean) <= max {
		return string(clean)
	}
	return string(clean[:max]) + "…"
}
