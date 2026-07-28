package tui

import (
	"strings"
	"testing"
)

// TestGitCommitDirective_NamesBothCompositeTools locks the contract
// that the procedural /git-commit prompt references the two Layer-1
// tools by name. The reliability win comes from the model invoking
// these specific tools — not the legacy bash heredoc — so a future
// prompt edit that drops a reference would silently regress us to
// the legacy shape without this guard.
func TestGitCommitDirective_NamesBothCompositeTools(t *testing.T) {
	got := gitCommitDirective()
	for _, tool := range []string{"git_commit_context", "git_commit_apply"} {
		if !strings.Contains(got, tool) {
			t.Errorf("gitCommitDirective must name %s; got:\n%s", tool, got)
		}
	}
}

// TestGitCommitDirective_PinsHardProhibitions guards the bright
// lines that distinguish procedural /git-commit from a model-driven
// flow. We can't enforce these at the Go level (the agent loop
// ultimately decides), but pinning the wording in the prompt is the
// only place we can — anyone deleting these lines will need to
// update the test, which forces a deliberate decision.
func TestGitCommitDirective_PinsHardProhibitions(t *testing.T) {
	got := gitCommitDirective()
	mustContain := []string{
		"git commit --amend",          // amend prohibition
		"git_stage_files",             // stage prohibition + reference
		"do NOT call git_stage_files", // explicit on the early-exit branch
		"STOP",                        // hook-error stop directive
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("gitCommitDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestSlash_GitCommitCommandRegistered verifies /git-commit lives in
// the built-in slash registry under the git- prefixed slug (palette
// discoverability: typing /git filters to every git-related
// built-in). Stale registrations are a real bug class — pinning
// both the slug and the helper-function wiring catches them.
func TestSlash_GitCommitCommandRegistered(t *testing.T) {
	if findSlash("git-commit") == nil {
		t.Errorf("expected /git-commit in the slash registry")
	}
	// Guard against accidental re-introduction of the unprefixed
	// /commit slug — discoverability via the git- prefix is the
	// reason we renamed, and adding both would defeat the palette
	// filter behavior.
	if findSlash("commit") != nil {
		t.Errorf("legacy /commit slug should not be registered; use /git-commit")
	}
}

// TestSlash_GitCommitBailsWhenTurnActive guards the turn-active
// early return. Pure UI logic; pinning it prevents a future
// refactor from quietly dropping the guard and queueing /git-commit
// invocations while another turn is mid-stream.
func TestSlash_GitCommitBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdGitCommit(m, nil)
	if cmd != nil {
		t.Errorf("cmdGitCommit should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "turn already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}
