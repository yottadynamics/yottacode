package tui

import (
	"strings"
	"testing"
)

func TestGitImplementIssueDirective_NamesReadAndCreateTools(t *testing.T) {
	d := gitImplementIssueDirective(42)
	for _, want := range []string{"issue_read", "pr_create", "git_commit_apply", "git_push", "git_stage_files"} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing tool name %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitImplementIssueDirective_InlinesIssueNumber(t *testing.T) {
	d := gitImplementIssueDirective(42)
	// The issue number must splice into the issue_read call,
	// the branch name template, the commit Refs, and the Closes
	// line. If any one of these drifts the flow breaks silently.
	for _, want := range []string{
		"number=42",
		"fix/issue-42",
		"Refs #42",
		"Closes #42",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitImplementIssueDirective_GatesOnIssueDetailSufficiency(t *testing.T) {
	// The issue-read checkpoint is the load-bearing safety gate: the
	// command should not plan or edit until the issue has enough detail.
	d := gitImplementIssueDirective(42)
	for _, want := range []string{
		"Step 2 — validate issue detail sufficiency",
		"missing expected vs actual behavior",
		"missing reproduction steps",
		"missing acceptance criteria",
		"Do NOT research code",
		"write tests, or edit files until the issue is clear enough",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing issue-detail gate %q\nfull:\n%s", want, d)
		}
	}
	if strings.Contains(d, "load-bearing safety gate of this command") {
		t.Errorf("directive must not describe plan mode as the command's load-bearing gate\nfull:\n%s", d)
	}
}

func TestGitImplementIssueDirective_PlanModeIsOptionalFallback(t *testing.T) {
	d := gitImplementIssueDirective(42)
	if strings.Contains(d, "CALL exit_plan_mode") || strings.Contains(d, "Do NOT skip this step") {
		t.Errorf("directive must not require exit_plan_mode as the only checkpoint\nfull:\n%s", d)
	}
	for _, want := range []string{"If exit_plan_mode", "If exit_plan_mode is not available", "normal scrollback message"} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing plan-mode fallback wording %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitImplementIssueDirective_PinsReproStepForBugs(t *testing.T) {
	// Step 3.5 (label-conditional reproduce) is the design decision
	// we explicitly added to the spec. Pin it so the next directive
	// edit doesn't drop the regression-test discipline.
	d := gitImplementIssueDirective(42)
	for _, want := range []string{
		"Step 3.5",
		`"bug"`,
		"reproduces the buggy behavior",
		"regression test",
		"tests-per-capability",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing reproduction anchor %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitImplementIssueDirective_PinsHardProhibitions(t *testing.T) {
	d := gitImplementIssueDirective(42)
	for _, want := range []string{
		"Do NOT close the issue manually",
		"Do NOT auto-iterate on test failures",
		"Do NOT force-push",
		"Do NOT amend",
		"Do NOT merge the PR",
		"Do NOT assign reviewers",
		"Do NOT fabricate file:line refs",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing hard prohibition %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitImplementIssueDirective_BranchesOnStateFlags(t *testing.T) {
	// The state-flag branching is the same load-bearing pattern as
	// the other /git-* directives. Pin the three branches so a
	// refactor of the flag names is caught here.
	d := gitImplementIssueDirective(42)
	for _, want := range []string{
		"github_unavailable=true",
		"not_found=true",
		"state=CLOSED",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing state-flag branch %q\nfull:\n%s", want, d)
		}
	}
}

func TestGitImplementIssueDirective_DraftPRWithClosesKeyword(t *testing.T) {
	d := gitImplementIssueDirective(42)
	if !strings.Contains(d, "draft=true") {
		t.Errorf("directive must require draft=true for the PR")
	}
	if !strings.Contains(d, "Closes #42") {
		t.Errorf("directive must include Closes #42 in the PR body")
	}
	if !strings.Contains(d, "human-review gate") {
		t.Errorf("directive must frame the draft PR as the human-review gate")
	}
}

func TestSlash_GitImplementIssueCommandRegistered(t *testing.T) {
	if findSlash("git-implement-issue") == nil {
		t.Errorf("expected /git-implement-issue in the slash registry")
	}
	// Spec'd under /git-implement-issue. The roadmap doc lives at
	// git-fix-issue.md (older name); the older name must NOT have
	// also been registered, otherwise the help palette shows two
	// near-duplicate entries.
	if findSlash("git-fix-issue") != nil {
		t.Errorf("/git-fix-issue should not be registered; only /git-implement-issue")
	}
	if findSlash("implement-issue") != nil {
		t.Errorf("unprefixed /implement-issue should not be registered; use /git-implement-issue")
	}
}

func TestSlash_GitImplementIssueBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdGitImplementIssue(m, []string{"42"})
	if cmd != nil {
		t.Errorf("cmdGitImplementIssue should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}

func TestSlash_GitImplementIssueRejectsMissingArg(t *testing.T) {
	m := newTestModel(t)
	out, cmd := cmdGitImplementIssue(m, nil)
	if cmd != nil {
		t.Errorf("cmdGitImplementIssue must not start a turn without an issue number")
	}
	if !strings.Contains(out.transcript.String(), "usage:") {
		t.Errorf("expected usage hint; got: %q", out.transcript.String())
	}
}

func TestSlash_GitImplementIssueRejectsNonNumericArg(t *testing.T) {
	m := newTestModel(t)
	out, cmd := cmdGitImplementIssue(m, []string{"not-a-number"})
	if cmd != nil {
		t.Errorf("cmdGitImplementIssue must not start a turn with non-numeric arg")
	}
	if !strings.Contains(out.transcript.String(), "positive integer") {
		t.Errorf("expected positive-integer rejection; got: %q", out.transcript.String())
	}
}

func TestSlash_GitImplementIssueRejectsZeroOrNegative(t *testing.T) {
	cases := []string{"0", "-1", "-42"}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			m := newTestModel(t)
			out, cmd := cmdGitImplementIssue(m, []string{tc})
			if cmd != nil {
				t.Errorf("cmdGitImplementIssue must not accept %q as an issue number", tc)
			}
			if !strings.Contains(out.transcript.String(), "positive integer") {
				t.Errorf("expected rejection; got: %q", out.transcript.String())
			}
		})
	}
}
