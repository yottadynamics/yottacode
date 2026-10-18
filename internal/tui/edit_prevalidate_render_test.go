package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
)

// A call rejected before the approval prompt reaches the TUI as ToolStart
// followed directly by an errored ToolResult, with no ApprovalNeeded between.
// The card must close out as a failure and the model must not be left waiting
// on an approval or showing a running tool.
func TestModel_PrevalidatedApplyDiffRendersAsFailedCardWithoutApproval(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()

	args := `{"diff":"*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch"}`
	msg := "error: apply_diff: apply_patch-style patches are not accepted here — use a unified diff, e.g. \"--- a/PATH\\n+++ b/PATH\\n@@ -12,3 +12,3 @@\\n context\\n-old\\n+new\\n context\\n\" (no \"*** Begin Patch\" envelope)"

	m, _ = applyMsg(m, agentEventMsg{ev: agent.AssistantMessage{Message: adapter.Message{
		ToolCalls: []adapter.ToolCall{{ID: "c1", Name: "apply_diff", ArgsJSON: args}},
	}}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolCallID: "c1", ToolName: "apply_diff", Preview: "apply_diff\n*** Begin Patch", ArgsJSON: args}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolCallID: "c1", ToolName: "apply_diff", Output: msg, Errored: true}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	if m.awaitingApproval {
		t.Fatal("a prevalidated rejection must never leave the model awaiting approval")
	}
	plain := stripANSI(m.transcript.String())
	t.Logf("rendered:\n%s", plain)
	if !strings.Contains(plain, "apply_patch-style") && !strings.Contains(plain, "unified diff") && !strings.Contains(plain, "malformed") {
		t.Errorf("failed card should surface the rejection reason: %q", plain)
	}
}

// A relocated edit_anchored success carries a note line; the card must still
// render as a success.
func TestModel_RelocatedEditAnchoredResultRenders(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()

	args := `{"path":"x.go","operations":[{"op":"replace_range","start_anchor":"3#aaaaaaaa","end_anchor":"3#aaaaaaaa","new_text":"y"}]}`
	out := "edited /tmp/x.go: 1 anchored operation(s)\nnote: stale anchor(s) relocated by matching their text (original line → current line): 3→5. Re-read before further edits to this file"
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolCallID: "c1", ToolName: "edit_anchored", Preview: "edit_anchored(x.go, 1 ops)", ArgsJSON: args}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolCallID: "c1", ToolName: "edit_anchored", Output: out}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	plain := stripANSI(m.transcript.String())
	t.Logf("rendered:\n%s", plain)
	if strings.Contains(plain, "error") {
		t.Errorf("relocated success must not render as an error: %q", plain)
	}
}
