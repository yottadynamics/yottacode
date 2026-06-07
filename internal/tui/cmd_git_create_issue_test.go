package tui

import (
	"strings"
	"testing"
)

func TestGitCreateIssueDirective_NamesReadAndCreateTools(t *testing.T) {
	d := gitCreateIssueDirective("")
	for _, want := range []string{"gh_issue_context", "gh_issue_create"} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing tool name %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitCreateIssueDirective_InlinesTitleArgument(t *testing.T) {
	d := gitCreateIssueDirective("Test issue title")
	if !strings.Contains(d, "Test issue title") {
		t.Errorf("directive missing inlined title\nfull:\n%s", d)
	}
}

func TestGitCreateIssueDirective_PinsHardProhibitions(t *testing.T) {
	d := gitCreateIssueDirective("")
	for _, want := range []string{
		"Do NOT run gh issue edit",
		"Do NOT auto-assign labels",
		"Do NOT invent checklist items",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing hard prohibition %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitCreateIssueDirective_BranchesOnStateFlags(t *testing.T) {
	d := gitCreateIssueDirective("")
	for _, want := range []string{
		"gh_available=false",
		"validation error",
		"gh_unavailable",
		"gh_error",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing state-flag branch %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitCreateIssueDirective_TemplateHandling(t *testing.T) {
	d := gitCreateIssueDirective("")
	for _, want := range []string{
		"template.content",
		"template.choices",
		"default skeleton",
		"Summary",
		"Details",
		"Checklist",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing template handling %q\nfull:\n%s", want, d)
		}
	}
}

func TestSlash_GitCreateIssueCommandRegistered(t *testing.T) {
	if findSlash("git-create-issue") == nil {
		t.Errorf("expected /git-create-issue in the slash registry")
	}
}

func TestSlash_GitCreateIssueBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdGitCreateIssue(m, []string{"test"})
	if cmd != nil {
		t.Errorf("cmdGitCreateIssue should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}

func TestSlash_GitCreateIssueAcceptsTitleArg(t *testing.T) {
	m := newTestModel(t)
	// We can't fully test the turn start without the agent (empty registry),
	// but we can verify the arg is parsed without triggering "usage" error
	out, _ := cmdGitCreateIssue(m, []string{"my test issue"})
	// Should not show usage error
	if strings.Contains(out.transcript.String(), "usage:") {
		t.Errorf("should not show usage error for valid title arg")
	}
}

func TestSlash_GitCreateIssueJoinsMultiWordTitle(t *testing.T) {
	// runSlash tokenizes the input line on whitespace, so
	// "/git-create-issue Fix crash on resize" reaches the handler as
	// four args. Regression: the title is their joined form — an
	// earlier version read args[0] and silently truncated the title
	// to its first word.
	got := issueTitleFromArgs([]string{"Fix", "crash", "on", "resize"})
	if got != "Fix crash on resize" {
		t.Errorf("issueTitleFromArgs = %q; want %q", got, "Fix crash on resize")
	}
	if issueTitleFromArgs(nil) != "" {
		t.Errorf("nil args should produce an empty title, got %q", issueTitleFromArgs(nil))
	}
}
