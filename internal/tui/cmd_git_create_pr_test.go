package tui

import (
	"strings"
	"testing"
)

// TestGitCreatePRDirective_NamesBothCompositeTools is the analogue
// of TestGitCommitDirective_NamesBothCompositeTools — the procedural
// directive must reference both halves of the workflow (context +
// create) by name. A prompt edit that drops a reference would
// silently regress us toward model-orchestrated PR creation without
// this guard.
func TestGitCreatePRDirective_NamesBothCompositeTools(t *testing.T) {
	got := gitCreatePRDirective("")
	for _, tool := range []string{"gh_pr_context", "gh_pr_create"} {
		if !strings.Contains(got, tool) {
			t.Errorf("gitCreatePRDirective must name %s; got:\n%s", tool, got)
		}
	}
}

// TestGitCreatePRDirective_InlinesBaseArgument locks the
// splice-injection path: when /git-create-pr is invoked with an
// explicit base, the prompt embeds that base in the gh_pr_context
// call instead of asking the model to thread it as prose. Threading
// via prose was a stumbling block in the legacy directive ($1
// substitution depended on the model honoring the placeholder).
func TestGitCreatePRDirective_InlinesBaseArgument(t *testing.T) {
	got := gitCreatePRDirective("develop")
	if !strings.Contains(got, `base="develop"`) {
		t.Errorf("expected base=\"develop\" inlined into the gh_pr_context call; got:\n%s", got)
	}

	// Without a base, no `base=` line — the resolver does its
	// origin/HEAD → fallback chain instead.
	noBase := gitCreatePRDirective("")
	if strings.Contains(noBase, `base="`) {
		t.Errorf("expected no base= when called bare; got:\n%s", noBase)
	}
}

// TestGitCreatePRDirective_PinsHardProhibitions pins the bright
// lines distinguishing procedural /git-create-pr from a model-driven
// flow. Same rationale as the /git-commit analogue: can't enforce
// these in Go, can only pin the prompt wording.
func TestGitCreatePRDirective_PinsHardProhibitions(t *testing.T) {
	got := gitCreatePRDirective("")
	mustContain := []string{
		"gh pr edit",  // edit prohibition
		"gh pr merge", // merge prohibition
		"STOP",        // explicit stop directives on each branch
		"empty PR",    // ahead_count=0 stop
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("gitCreatePRDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestSlash_GitCreatePRCommandRegistered verifies /git-create-pr is
// in the built-in slash registry alongside /git-commit. The `git-`
// prefix gives palette discoverability — typing /git filters to
// every git-related built-in.
func TestSlash_GitCreatePRCommandRegistered(t *testing.T) {
	if findSlash("git-create-pr") == nil {
		t.Errorf("expected /git-create-pr in the slash registry")
	}
	// Guard against accidental re-introduction of the unprefixed
	// /create-pr slug. The git- prefix is load-bearing for the
	// palette filter behavior.
	if findSlash("create-pr") != nil {
		t.Errorf("legacy /create-pr slug should not be registered; use /git-create-pr")
	}
}

// TestSlash_GitCreatePRBailsWhenTurnActive guards the turn-active
// early return. Pure UI logic but worth pinning so a future
// refactor doesn't drop the guard and queue /git-create-pr
// invocations mid-stream.
func TestSlash_GitCreatePRBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdGitCreatePR(m, nil)
	if cmd != nil {
		t.Errorf("cmdGitCreatePR should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}
