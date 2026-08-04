package tui

import (
	"strings"
	"testing"
)

// TestGitUpdatePRDirective_NamesBothCompositeTools — same
// contract guard as the rest of the /git-* family. The directive
// reuses pr_review_context for the gathering step and adds
// pr_update for the mutation. Both names must appear.
func TestGitUpdatePRDirective_NamesBothCompositeTools(t *testing.T) {
	got := gitUpdatePRDirective("")
	for _, tool := range []string{"pr_review_context", "pr_update"} {
		if !strings.Contains(got, tool) {
			t.Errorf("gitUpdatePRDirective must name %s; got:\n%s", tool, got)
		}
	}
}

// TestGitUpdatePRDirective_InlinesRefArgument mirrors the
// create-pr / review-pr analogues: when ref is supplied, the
// prompt embeds it directly in the tool call.
func TestGitUpdatePRDirective_InlinesRefArgument(t *testing.T) {
	got := gitUpdatePRDirective("17")
	if !strings.Contains(got, `ref="17"`) {
		t.Errorf("expected ref=\"17\" inlined; got:\n%s", got)
	}
	noRef := gitUpdatePRDirective("")
	if strings.Contains(noRef, `ref="`) {
		t.Errorf("expected no ref= when called bare; got:\n%s", noRef)
	}
}

// TestGitUpdatePRDirective_PinsTitleChurnGuard is the load-bearing
// brief for this command: the model must keep the existing title
// when commits remain consistent with it, and only rewrite for
// material scope changes. Without this guard, the model edits
// titles on every invocation (cosmetic rewording), which churns
// the PR's GitHub history with noise. Pinning the wording means
// any future prompt edit that drops it requires a deliberate
// test change.
func TestGitUpdatePRDirective_PinsTitleChurnGuard(t *testing.T) {
	got := gitUpdatePRDirective("")
	mustContain := []string{
		"keep the existing title verbatim",      // default-keep stance
		"scope materially changed",              // bar for rewriting
		"rewording, capitalization fixes",       // explicit no-cosmetic list
		"adds noise to the PR's GitHub history", // why it matters
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("gitUpdatePRDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestGitUpdatePRDirective_PinsHardProhibitions pins the bright
// lines that distinguish /git-update-pr from a free-form
// `gh pr edit`. Title+body only — no labels, reviewers, base,
// draft, milestone, projects. No close/reopen/merge.
func TestGitUpdatePRDirective_PinsHardProhibitions(t *testing.T) {
	got := gitUpdatePRDirective("")
	mustContain := []string{
		"Do NOT touch fields other than title and body", // scope guard
		"Do NOT close, reopen, or merge the PR",         // lifecycle guard
		"Do NOT rewrite the title for cosmetic reasons", // churn guard
		"STOP", // explicit stops on each branch
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("gitUpdatePRDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestSlash_GitUpdatePRCommandRegistered verifies the new
// built-in is in the slash registry. The no-unprefixed-shadow
// guard mirrors the pattern used elsewhere in the family.
func TestSlash_GitUpdatePRCommandRegistered(t *testing.T) {
	if findSlash("git-update-pr") == nil {
		t.Errorf("expected /git-update-pr in the slash registry")
	}
	if findSlash("update-pr") != nil {
		t.Errorf("unprefixed /update-pr should not be registered; use /git-update-pr")
	}
}

// TestSlash_GitUpdatePRBailsWhenTurnActive guards the
// turn-active early return. Pure UI logic; pinning prevents
// silent drop in a future refactor.
func TestSlash_GitUpdatePRBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdGitUpdatePR(m, nil)
	if cmd != nil {
		t.Errorf("cmdGitUpdatePR should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "turn already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}
