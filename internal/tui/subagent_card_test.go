package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// TestAppendDispatchWakeMetadata_ErroredCommittedWorkerNotRecommendedForIntegrate
// is the regression for the "errored worker's branch still gets recommended"
// bug: a background dispatch worker can be BOTH Committed (its branch has an
// earlier, legitimate commit) AND Errored (e.g. it left out-of-scope changes
// uncommitted). Before the fix, the wake-turn message this function builds —
// injected straight into the model's next turn — told the model to call
// integrate with that worker's branch regardless of Errored.
func TestAppendDispatchWakeMetadata_ErroredCommittedWorkerNotRecommendedForIntegrate(t *testing.T) {
	w := agent.SubagentBackgroundDone{
		TaskID:    "task1234",
		Branch:    "worktree-dispatch-x-1",
		BatchID:   "batch-1",
		Committed: true,
		CommitSHA: "deadbeef",
		CommitErr: "out-of-scope changes left uncommitted: rogue.go",
		Errored:   true,
	}
	var b strings.Builder
	appendDispatchWakeMetadata(&b, w)
	out := b.String()

	if strings.Contains(out, "Next: when every worker") {
		t.Errorf("an errored worker must not be recommended for integrate:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("expected the errored-but-committed worker to be flagged as failed:\n%s", out)
	}
}

// TestAppendDispatchWakeMetadata_CleanCommittedWorkerStillRecommended pins
// the non-errored path: a genuinely clean, committed worker must still get
// the "call integrate" instruction — the fix must not over-correct into
// suppressing it for every committed worker.
func TestAppendDispatchWakeMetadata_CleanCommittedWorkerStillRecommended(t *testing.T) {
	w := agent.SubagentBackgroundDone{
		TaskID:    "task5678",
		Branch:    "worktree-dispatch-x-2",
		BatchID:   "batch-1",
		Committed: true,
		CommitSHA: "cafebabe",
	}
	var b strings.Builder
	appendDispatchWakeMetadata(&b, w)
	out := b.String()

	if !strings.Contains(out, "Next: when every worker") {
		t.Errorf("a clean committed worker should still be recommended for integrate:\n%s", out)
	}
}

// TestRenderSubagentBackgroundDone_ErroredCommittedShowsFailed pins the
// live-dock/banner counterpart of the same fix: an errored-but-committed
// worker's badge must read as a failure, not a plain "committed" success.
func TestRenderSubagentBackgroundDone_ErroredCommittedShowsFailed(t *testing.T) {
	e := agent.SubagentBackgroundDone{
		TaskID:    "task1234",
		AgentType: "writer",
		Branch:    "worktree-dispatch-x-1",
		Committed: true,
		CommitSHA: "deadbeef",
		Errored:   true,
	}
	out := renderSubagentBackgroundDone(e)
	if !strings.Contains(out, "FAILED") {
		t.Errorf("expected the badge to flag FAILED for an errored-but-committed worker, got:\n%s", out)
	}
}
