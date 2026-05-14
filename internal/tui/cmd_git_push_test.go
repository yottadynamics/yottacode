package tui

import (
	"strings"
	"testing"
)

// TestGitPushDirective_NamesCompositeTool — same contract guard as
// the rest of the /git-* family: the directive must name the
// underlying tool by literal string so a prompt-edit that drops
// the reference would silently regress to model-orchestrated push.
func TestGitPushDirective_NamesCompositeTool(t *testing.T) {
	got := gitPushDirective()
	if !strings.Contains(got, "git_push") {
		t.Errorf("gitPushDirective must name git_push; got:\n%s", got)
	}
}

// TestGitPushDirective_PinsHardProhibitions pins the safety rails
// that distinguish /git-push from a free-form `git push` via the
// unified git tool. No --force, no other-branch push. The tool
// itself enforces the first; the directive locks in the second.
func TestGitPushDirective_PinsHardProhibitions(t *testing.T) {
	got := gitPushDirective()
	mustContain := []string{
		"--force",                   // no force-push
		"--force-with-lease",        // no soft force either
		"only ever pushes HEAD",     // no other-branch push via unified git
		"Do NOT auto-retry",         // no auto-retry on error
		"STOP",                      // explicit stop on detached/error
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("gitPushDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestGitPushDirective_PRURLBranches pins the two-shape output:
// pushed-with-PR surfaces the URL, pushed-without-PR surfaces the
// "use /git-create-pr" hint. The hint is the discoverability
// payoff for this command — losing it loses the cross-family
// pointer that ties /git-push to /git-create-pr.
func TestGitPushDirective_PRURLBranches(t *testing.T) {
	got := gitPushDirective()
	mustContain := []string{
		"PR updated:",         // populated-URL branch
		"/git-create-pr",      // no-PR-yet hint
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("gitPushDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestSlash_GitPushCommandRegistered verifies the new built-in is
// in the slash registry under the git- family slug. Pinning the
// no-unprefixed-shadow guard is the family convention.
func TestSlash_GitPushCommandRegistered(t *testing.T) {
	if findSlash("git-push") == nil {
		t.Errorf("expected /git-push in the slash registry")
	}
	if findSlash("push") != nil {
		t.Errorf("unprefixed /push slug should not be registered; use /git-push")
	}
}

// TestSlash_GitPushBailsWhenTurnActive guards the turn-active
// early return. Pure UI logic but worth pinning so a future
// refactor doesn't drop the guard.
func TestSlash_GitPushBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdGitPush(m, nil)
	if cmd != nil {
		t.Errorf("cmdGitPush should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}
