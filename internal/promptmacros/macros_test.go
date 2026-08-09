package promptmacros

import (
	"strings"
	"testing"
)

func TestAll_ReturnsAllNineMacros(t *testing.T) {
	want := []string{
		"git-commit", "git-push", "git-create-pr", "git-update-pr",
		"git-create-issue", "git-review-pr", "code-review",
		"git-implement-issue", "init",
	}
	all := All()
	if len(all) != len(want) {
		t.Fatalf("All() returned %d macros, want %d", len(all), len(want))
	}
	for i, name := range want {
		if all[i].Name != name {
			t.Errorf("All()[%d].Name = %q, want %q", i, all[i].Name, name)
		}
		if all[i].Build == nil {
			t.Errorf("All()[%d] (%s) has a nil Build func", i, all[i].Name)
		}
		if all[i].Description == "" {
			t.Errorf("All()[%d] (%s) has an empty Description", i, all[i].Name)
		}
	}
}

func TestAll_ReturnsACopy(t *testing.T) {
	a := All()
	a[0].Name = "mutated"
	b := All()
	if b[0].Name == "mutated" {
		t.Error("All() must return a fresh copy — mutating the result leaked into the registry")
	}
}

func TestGet_KnownAndUnknown(t *testing.T) {
	if _, ok := Get("git-commit"); !ok {
		t.Error("Get(\"git-commit\") should be found")
	}
	if _, ok := Get("does-not-exist"); ok {
		t.Error("Get(\"does-not-exist\") should not be found")
	}
}

func TestMustGet_PanicsOnUnknown(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustGet on an unknown name should panic")
		}
	}()
	MustGet("does-not-exist")
}

func TestMustGet_ReturnsKnown(t *testing.T) {
	if m := MustGet("init"); m.Name != "init" {
		t.Errorf("MustGet(\"init\").Name = %q, want \"init\"", m.Name)
	}
}

func TestBuild_GitCommit_IgnoresArgs(t *testing.T) {
	m := MustGet("git-commit")
	got, err := m.Build("/repo", []string{"unexpected", "args"})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != GitCommitDirective() {
		t.Error("git-commit Build output must match GitCommitDirective()")
	}
}

func TestBuild_GitPush_IgnoresArgs(t *testing.T) {
	m := MustGet("git-push")
	got, err := m.Build("/repo", nil)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != GitPushDirective() {
		t.Error("git-push Build output must match GitPushDirective()")
	}
}

func TestBuild_GitCreatePR_TrimsAndSplicesBase(t *testing.T) {
	m := MustGet("git-create-pr")
	got, err := m.Build("/repo", []string{"  develop  "})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != GitCreatePRDirective("develop") {
		t.Error("git-create-pr Build must trim and pass the base arg through")
	}
	gotNoArg, err := m.Build("/repo", nil)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if gotNoArg != GitCreatePRDirective("") {
		t.Error("git-create-pr Build with no args must match the empty-base directive")
	}
}

func TestBuild_GitCreatePR_QuotesBase(t *testing.T) {
	got := GitCreatePRDirective("bad\"\nextra")
	if !strings.Contains(got, `base="bad\"\nextra"`) {
		t.Fatalf("base argument was not safely quoted in directive:\n%s", got)
	}
	if strings.Contains(got, "base=\"bad\"\nextra\".") {
		t.Fatal("base argument was interpolated without escaping")
	}
}

func TestBuild_GitUpdatePR_TrimsAndSplicesRef(t *testing.T) {
	m := MustGet("git-update-pr")
	got, err := m.Build("/repo", []string{"17"})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != GitUpdatePRDirective("17") {
		t.Error("git-update-pr Build must pass the ref arg through")
	}
}

func TestBuild_GitUpdatePR_QuotesRef(t *testing.T) {
	got := GitUpdatePRDirective("bad\"\nextra")
	if !strings.Contains(got, `ref="bad\"\nextra"`) {
		t.Fatalf("ref argument was not safely quoted in directive:\n%s", got)
	}
	if strings.Contains(got, "ref=\"bad\"\nextra\".") {
		t.Fatal("ref argument was interpolated without escaping")
	}
}

func TestBuild_GitCreateIssue_JoinsMultiWordTitle(t *testing.T) {
	m := MustGet("git-create-issue")
	got, err := m.Build("/repo", []string{"Fix", "crash", "on", "resize"})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != GitCreateIssueDirective("Fix crash on resize") {
		t.Error("git-create-issue Build must join args into the title")
	}
}

func TestBuild_GitReviewPR_TrimsAndSplicesRef(t *testing.T) {
	m := MustGet("git-review-pr")
	got, err := m.Build("/repo", []string{" feature/x "})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != GitReviewPRDirective("feature/x") {
		t.Error("git-review-pr Build must trim and pass the ref arg through")
	}
}

func TestBuild_GitReviewPR_QuotesRef(t *testing.T) {
	got := GitReviewPRDirective("bad\"\nextra")
	if !strings.Contains(got, `ref="bad\"\nextra"`) {
		t.Fatalf("ref argument was not safely quoted in directive:\n%s", got)
	}
	if strings.Contains(got, "ref=\"bad\"\nextra\".") {
		t.Fatal("ref argument was interpolated without escaping")
	}
}

func TestBuild_CodeReview_DefaultsToMedium(t *testing.T) {
	m := MustGet("code-review")
	got, err := m.Build("/repo", nil)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != CodeReviewDirective("medium") {
		t.Error("code-review Build with no args must default to medium effort")
	}
}

func TestBuild_CodeReview_UnknownEffortErrors(t *testing.T) {
	m := MustGet("code-review")
	if _, err := m.Build("/repo", []string{"bogus"}); err == nil {
		t.Fatal("code-review Build with an unrecognized effort must return the parse notice as an error")
	} else if !strings.Contains(err.Error(), "unknown effort bogus") {
		t.Fatalf("code-review Build error = %v, want unknown-effort notice", err)
	}
}

func TestBuild_GitImplementIssue_ValidatesArgs(t *testing.T) {
	m := MustGet("git-implement-issue")

	if _, err := m.Build("/repo", nil); err == nil {
		t.Error("git-implement-issue Build with no args must return an error")
	}
	if _, err := m.Build("/repo", []string{"not-a-number"}); err == nil {
		t.Error("git-implement-issue Build with a non-numeric arg must return an error")
	}
	for _, bad := range []string{"0", "-1", "-42"} {
		if _, err := m.Build("/repo", []string{bad}); err == nil {
			t.Errorf("git-implement-issue Build(%q) must return an error", bad)
		}
	}

	got, err := m.Build("/repo", []string{"42"})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != GitImplementIssueDirective(42) {
		t.Error("git-implement-issue Build(42) must match GitImplementIssueDirective(42)")
	}
}

func TestBuild_Init_UsesCwd(t *testing.T) {
	m := MustGet("init")
	dir := t.TempDir()
	got, err := m.Build(dir, nil)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got != BuildInitPrompt(dir) {
		t.Error("init Build must match BuildInitPrompt(cwd)")
	}
}
