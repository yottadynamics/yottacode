package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

const applyPatchStyleArgs = `{"diff":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}`

func TestApplyDiffPrevalidateRejectsApplyPatchStyle(t *testing.T) {
	tmp := t.TempDir()
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	err := tool.PrevalidateArgs(applyPatchStyleArgs)
	if err == nil || !strings.Contains(err.Error(), "apply_patch-style patches are not accepted") {
		t.Fatalf("err = %v", err)
	}
	// The remedy must be self-contained: a concrete unified-diff example.
	if !strings.Contains(err.Error(), "--- a/PATH") || !strings.Contains(err.Error(), "@@ -12,3 +12,3 @@") {
		t.Errorf("error lacks a unified-diff example: %v", err)
	}
	// ClassifyEditFailure must keep recognising it so TUI cards don't regress.
	if got := ClassifyEditFailure("apply_diff", "error: "+err.Error()); got != EditFailureMalformed {
		t.Errorf("classified as %v, want EditFailureMalformed", got)
	}
}

func TestApplyDiffPrevalidateAcceptsValidAndLeavesOtherErrorsToExecute(t *testing.T) {
	tmp := t.TempDir()
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	valid := `{"diff":"--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"}`
	if err := tool.PrevalidateArgs(valid); err != nil {
		t.Fatalf("valid diff rejected: %v", err)
	}
	for _, args := range []string{`not json`, `{"diff":"  "}`} {
		if err := tool.PrevalidateArgs(args); err != nil {
			t.Errorf("PrevalidateArgs(%q) = %v; Execute owns this error", args, err)
		}
	}
}

// The loop must bounce a structurally invalid patch BEFORE prompting: with a
// nil decide func, runTurnSync fails the test if ApprovalNeeded ever fires.
func TestLoopPrevalidatesBeforeApprovalPrompt(t *testing.T) {
	tmp := t.TempDir()
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "apply_diff", ArgsJSON: applyPatchStyleArgs})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Cwd: NewCwdRef(tmp), MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	got := findToolResult(t, hist, "c1")
	if !strings.HasPrefix(got.Content, "error: apply_diff:") || !strings.Contains(got.Content, "apply_patch-style") {
		t.Errorf("tool result = %q", got.Content)
	}
	var sawErrored bool
	for _, ev := range events {
		if tr, ok := ev.(ToolResult); ok && tr.Errored {
			sawErrored = true
		}
	}
	if !sawErrored {
		t.Error("no errored ToolResult event emitted")
	}
}
