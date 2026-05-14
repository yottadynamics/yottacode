package tui

import (
	"strings"
	"testing"
)

// TestGitReviewPRDirective_NamesCompositeTool locks the contract:
// the procedural directive must reference gh_pr_review_context by
// name. The reliability work lives in that tool; if the prompt
// stops naming it, we silently regress to whatever the model picks
// (likely raw bash + gh CLI).
func TestGitReviewPRDirective_NamesCompositeTool(t *testing.T) {
	got := gitReviewPRDirective("")
	if !strings.Contains(got, "gh_pr_review_context") {
		t.Errorf("gitReviewPRDirective must name gh_pr_review_context; got:\n%s", got)
	}
}

// TestGitReviewPRDirective_InlinesRefArgument mirrors the
// create-pr analogue: when ref is supplied, the prompt embeds it
// in the tool call rather than asking the model to thread it.
func TestGitReviewPRDirective_InlinesRefArgument(t *testing.T) {
	got := gitReviewPRDirective("17")
	if !strings.Contains(got, `ref="17"`) {
		t.Errorf("expected ref=\"17\" inlined; got:\n%s", got)
	}
	noRef := gitReviewPRDirective("")
	if strings.Contains(noRef, `ref="`) {
		t.Errorf("expected no ref= when called bare; got:\n%s", noRef)
	}
}

// TestGitReviewPRDirective_PinsHardProhibitions pins the bright
// lines that distinguish a review (read-only, scrollback output)
// from a fix-up pass. The legacy /check:review markdown directive
// also forbade these but in prose; pinning them in test means an
// accidental relaxation requires a deliberate test edit.
func TestGitReviewPRDirective_PinsHardProhibitions(t *testing.T) {
	got := gitReviewPRDirective("")
	mustContain := []string{
		"Do NOT post the review back to GitHub",  // no auto-post
		"Do NOT propose code edits",              // review, not fix-up
		"Do NOT fabricate file:line refs",        // no hallucinated locations
		"STOP",                                   // explicit stop on not_found/unavailable
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("gitReviewPRDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestGitReviewPRDirective_PinsRubricStructure guards the
// review's structural contract: the model must emit Blockers /
// Suggestions / Nits sections in that order so reviews remain
// scannable. A prompt drift that drops a section would degrade
// the review format silently — pinning fences it.
func TestGitReviewPRDirective_PinsRubricStructure(t *testing.T) {
	got := gitReviewPRDirective("")
	sections := []string{
		"### Failing checks",
		"### Blockers",
		"### Suggestions",
		"### Nits",
	}
	prev := 0
	for _, s := range sections {
		idx := strings.Index(got, s)
		if idx < 0 {
			t.Errorf("missing section %q in directive", s)
			continue
		}
		if idx < prev {
			t.Errorf("section %q appears before its predecessor — order broken", s)
		}
		prev = idx
	}
}

// TestSlash_GitReviewPRCommandRegistered verifies the new built-in
// is in the slash registry under the git- family slug. The
// no-unprefixed-shadow guard mirrors the pattern used for
// /git-commit and /git-create-pr.
func TestSlash_GitReviewPRCommandRegistered(t *testing.T) {
	if findSlash("git-review-pr") == nil {
		t.Errorf("expected /git-review-pr in the slash registry")
	}
	if findSlash("review-pr") != nil {
		t.Errorf("legacy /review-pr slug should not be registered; use /git-review-pr")
	}
}

// TestSlash_GitReviewPRBailsWhenTurnActive guards the turn-active
// early return. Same pattern as the other /git-* tests.
func TestSlash_GitReviewPRBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdGitReviewPR(m, nil)
	if cmd != nil {
		t.Errorf("cmdGitReviewPR should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}
