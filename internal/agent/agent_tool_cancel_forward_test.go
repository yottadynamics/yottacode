package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestAgentTool_ForegroundCancelDuringApprovalForward_FinalizesTranscriptAndStats
// covers a resource-cleanup gap in runChild's approval-forwarding switch:
// when the PARENT's ctx goes Done while a child's ApprovalNeeded (or
// PathTrustElevationNeeded) is being relayed to the parent's modal, the
// six early "return \"\", true, subagents.TaskCanceled, 0" bailouts used
// to skip the same finalization the normal exit path performs at the
// bottom of runChild: t.Tasks.SetToolCalls, transcript.writeOutcome, and
// transcript.close(). That leaked the transcript's *os.File and froze
// the task's stats at zero even when the child had already completed
// real tool calls before the cancel landed.
//
// The child here makes one real tool call (read_file, no approval) before
// a second call (run_bash, approval-required) gets forwarded to the
// parent; the parent cancels instead of answering, right when it sees
// the forwarded ApprovalNeeded.
//
// This also doubles as the regression test for a genuine data race
// `go test -race` (already run in CI, .github/workflows/go.yml) caught
// here: at the same instant the parent cancels, the child's own Turn
// goroutine independently reacts to the identical ctx.Done() by
// repairing ITS OWN history (orphan-marking the tool_use it was
// mid-approval on) — concurrently with cancelExit, on the parent-facing
// goroutine, which used to call contextwindow.EstimateTokens(history)
// on that same slice with no synchronization between the two. Fixed by
// having cancelExit not read history at all (see its comment in
// agent_tool.go); run this test with -race to verify.
func TestAgentTool_ForegroundCancelDuringApprovalForward_FinalizesTranscriptAndStats(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "read_file", ArgsJSON: `{}`})},
		{sseDone("", adapter.ToolCall{ID: "2", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
	}}
	cfg := subagents.AgentConfig{Name: "fg", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, false)

	parentEvents := make(chan Event, 64)
	parentDecisions := make(chan Decision, 1)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithParentDecisions(WithParentEvents(ctx, parentEvents), parentDecisions)

	// Simulated parent UI: cancel the moment the second tool call's
	// approval request is forwarded, instead of answering it — models
	// the parent's own turn ending (Esc, a mid-session provider/model
	// switch tearing down channels, session teardown) while a nested
	// subagent is mid-approval.
	go func() {
		for ev := range parentEvents {
			if _, ok := ev.(ApprovalNeeded); ok {
				cancel()
				return
			}
		}
	}()

	args := mustJSON(t, agentArgs{SubagentType: "fg", Prompt: "p"})
	out, err := tool.Execute(ctx, args)
	close(parentEvents)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "" {
		t.Errorf("cancelled-mid-forward result should be empty; got %q", out)
	}

	tasks := tool.Tasks.List()
	if len(tasks) != 1 {
		t.Fatalf("expected exactly one task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.Status != subagents.TaskCanceled {
		t.Errorf("task status = %v, want TaskCanceled", task.Status)
	}
	// Before the fix this was always 0: the early return skipped
	// SetToolCalls entirely, discarding the read_file call that had
	// already completed.
	if task.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1 (read_file completed before the cancel; run_bash never started)", task.ToolCalls)
	}

	// The transcript file must be closed (no leaked *os.File) and carry
	// a terminal outcome line — before the fix, the ctx.Done() bailout
	// inside the approval-forwarding switch skipped both.
	data, rerr := os.ReadFile(task.TranscriptPath)
	if rerr != nil {
		t.Fatalf("reading transcript: %v", rerr)
	}
	if !strings.Contains(string(data), "**Outcome:**") {
		t.Errorf("transcript missing outcome marker; got:\n%s", data)
	}
}
