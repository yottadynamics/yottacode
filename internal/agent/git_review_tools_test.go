package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestGitDiffStat_WorkingTreeAndRange(t *testing.T) {
	dir := gitInit(t)
	writeRepoFile(t, dir, "a.txt", "one\n")
	gitCommit(t, dir, "first")

	tool := &GitDiffStatTool{Cwd: NewCwdRef(dir)}

	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "(no changes)") {
		t.Errorf("clean tree should report no changes: %q", out)
	}

	writeRepoFile(t, dir, "a.txt", "one\ntwo\n")
	out, err = tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "1 file") {
		t.Errorf("diffstat should summarize the edit: %q", out)
	}

	gitCommit(t, dir, "second")
	out, err = tool.Execute(context.Background(), `{"base":"HEAD~1","head":"HEAD"}`)
	if err != nil {
		t.Fatalf("Execute(range): %v", err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Errorf("range diffstat should include the file: %q", out)
	}

	if _, err := tool.Execute(context.Background(), `{"head":"HEAD"}`); err == nil {
		t.Error("head without base should error")
	}
}

func TestGitDiffStagedAndUnstaged_SplitCorrectly(t *testing.T) {
	dir := gitInit(t)
	writeRepoFile(t, dir, "a.txt", "one\n")
	gitCommit(t, dir, "first")

	staged := &GitDiffStagedTool{Cwd: NewCwdRef(dir)}
	unstaged := &GitDiffUnstagedTool{Cwd: NewCwdRef(dir)}

	// Clean tree: both sides empty, with distinct phrasing.
	out, err := staged.Execute(context.Background(), `{}`)
	if err != nil || !strings.Contains(out, "(nothing staged)") {
		t.Errorf("clean staged = (%q, %v), want nothing-staged marker", out, err)
	}
	out, err = unstaged.Execute(context.Background(), `{}`)
	if err != nil || !strings.Contains(out, "(no unstaged changes)") {
		t.Errorf("clean unstaged = (%q, %v), want no-unstaged marker", out, err)
	}

	// Stage one edit, leave another unstaged: each surface sees only its half.
	writeRepoFile(t, dir, "a.txt", "one\nstaged-line\n")
	c := gitCmd(t, dir, "add", "a.txt")
	_ = c
	writeRepoFile(t, dir, "a.txt", "one\nstaged-line\nunstaged-line\n")

	out, err = staged.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if !strings.Contains(out, "staged-line") || strings.Contains(out, "unstaged-line") {
		t.Errorf("staged diff should contain only the staged half: %q", out)
	}

	out, err = unstaged.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unstaged: %v", err)
	}
	if !strings.Contains(out, "unstaged-line") || strings.Contains(out, "+staged-line") {
		t.Errorf("unstaged diff should contain only the unstaged half: %q", out)
	}
}

func TestGitCommitsBetween_RangeAndLimit(t *testing.T) {
	dir := gitInit(t)
	writeRepoFile(t, dir, "a.txt", "1\n")
	gitCommit(t, dir, "base commit")
	gitCmdOK(t, dir, "switch", "-c", "feature")
	writeRepoFile(t, dir, "a.txt", "2\n")
	gitCommit(t, dir, "feature one")
	writeRepoFile(t, dir, "a.txt", "3\n")
	gitCommit(t, dir, "feature two")

	tool := &GitCommitsBetweenTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"base":"main"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "feature one") || !strings.Contains(out, "feature two") {
		t.Errorf("range should list the branch commits: %q", out)
	}
	if strings.Contains(out, "base commit") {
		t.Errorf("range must exclude commits reachable from base: %q", out)
	}

	out, err = tool.Execute(context.Background(), `{"base":"main","limit":1}`)
	if err != nil {
		t.Fatalf("Execute(limit): %v", err)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("limit=1 should yield exactly one line: %q", out)
	}

	out, err = tool.Execute(context.Background(), `{"base":"HEAD"}`)
	if err != nil || !strings.Contains(out, "(no commits in range)") {
		t.Errorf("empty range = (%q, %v), want no-commits marker", out, err)
	}

	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Error("missing base should error")
	}
}

func TestGitBranchAheadBehind_Counts(t *testing.T) {
	dir := gitInit(t)
	writeRepoFile(t, dir, "a.txt", "1\n")
	gitCommit(t, dir, "base commit")
	gitCmdOK(t, dir, "switch", "-c", "feature")
	writeRepoFile(t, dir, "a.txt", "2\n")
	gitCommit(t, dir, "ahead one")
	writeRepoFile(t, dir, "a.txt", "3\n")
	gitCommit(t, dir, "ahead two")

	tool := &GitBranchAheadBehindTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"base":"main"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "ahead=2 behind=0") {
		t.Errorf("expected ahead=2 behind=0: %q", out)
	}
	if !strings.Contains(out, "merge_base=") {
		t.Errorf("expected merge_base line: %q", out)
	}

	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Error("missing base should error")
	}
}

func TestGitBranchDiff_OneStopSummary(t *testing.T) {
	dir := gitInit(t)
	writeRepoFile(t, dir, "a.txt", "1\n")
	gitCommit(t, dir, "base commit")
	gitCmdOK(t, dir, "switch", "-c", "feature")
	writeRepoFile(t, dir, "a.txt", "2\n")
	writeRepoFile(t, dir, "b.txt", "new\n")
	gitCommit(t, dir, "feature work")

	tool := &GitBranchDiffTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"base":"main"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"## state", "branch=feature", "base=main", "ahead=1 behind=0",
		"## commits", "feature work",
		"## changed-files", "M\ta.txt", "A\tb.txt",
		"## diffstat", "2 files changed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("branch diff missing %q:\n%s", want, out)
		}
	}

	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Error("missing base should error")
	}
}

func TestGitCommitAmend_KeepsOrReplacesMessage(t *testing.T) {
	dir := gitInit(t)
	writeRepoFile(t, dir, "a.txt", "1\n")
	gitCommit(t, dir, "original subject")

	tool := &GitCommitAmendTool{Cwd: NewCwdRef(dir)}

	// Fold a staged change in, keeping the message.
	writeRepoFile(t, dir, "a.txt", "1\n2\n")
	gitCmdOK(t, dir, "add", "a.txt")
	if _, err := tool.Execute(context.Background(), `{}`); err != nil {
		t.Fatalf("amend --no-edit: %v", err)
	}
	subject := gitCmdOK(t, dir, "log", "-1", "--format=%s")
	if strings.TrimSpace(subject) != "original subject" {
		t.Errorf("no-edit amend changed the subject: %q", subject)
	}
	count := gitCmdOK(t, dir, "rev-list", "--count", "HEAD")
	if strings.TrimSpace(count) != "1" {
		t.Errorf("amend should not add a commit; count = %s", count)
	}

	// Replace the message.
	if _, err := tool.Execute(context.Background(), `{"message":"new subject"}`); err != nil {
		t.Fatalf("amend -m: %v", err)
	}
	subject = gitCmdOK(t, dir, "log", "-1", "--format=%s")
	if strings.TrimSpace(subject) != "new subject" {
		t.Errorf("amend -m did not replace the subject: %q", subject)
	}

	// The approval copy must say what it rewrites.
	if p := tool.PreviewCall(`{}`); !strings.Contains(p, "rewrites the last commit") {
		t.Errorf("amend preview must warn about the rewrite: %q", p)
	}
}

func TestGitCommitFixup_TargetsCommit(t *testing.T) {
	dir := gitInit(t)
	writeRepoFile(t, dir, "a.txt", "1\n")
	gitCommit(t, dir, "target subject")
	writeRepoFile(t, dir, "a.txt", "1\nfix\n")
	gitCmdOK(t, dir, "add", "a.txt")

	tool := &GitCommitFixupTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"commit":"HEAD"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "fixup commit") {
		t.Errorf("result should name the fixup commit: %q", out)
	}
	subject := gitCmdOK(t, dir, "log", "-1", "--format=%s")
	if strings.TrimSpace(subject) != "fixup! target subject" {
		t.Errorf("fixup subject = %q, want fixup! target subject", subject)
	}

	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Error("missing commit should error")
	}
}

// The six review surfaces are read-only (auto-execute, parallel-safe);
// the two commit helpers always prompt. Pin the policy so a refactor
// can't silently flip a mutator to auto-exec.
func TestGitReviewTools_ApprovalPolicy(t *testing.T) {
	readOnly := []Tool{
		&GitDiffStatTool{}, &GitDiffStagedTool{}, &GitDiffUnstagedTool{},
		&GitCommitsBetweenTool{}, &GitBranchAheadBehindTool{}, &GitBranchDiffTool{},
	}
	for _, tool := range readOnly {
		if tool.RequiresApproval(`{}`) {
			t.Errorf("%s must be auto-execute (read-only)", tool.Name())
		}
		if ps, ok := tool.(ParallelSafeTool); !ok || !ps.ParallelSafe(`{}`) {
			t.Errorf("%s must be parallel-safe", tool.Name())
		}
	}
	for _, tool := range []Tool{&GitCommitAmendTool{}, &GitCommitFixupTool{}} {
		if !tool.RequiresApproval(`{}`) {
			t.Errorf("%s must require approval", tool.Name())
		}
	}
}

// gitCmd / gitCmdOK run git in the fixture repo; OK fails the test on a
// non-zero exit and returns stdout.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitCmdOK(t, dir, args...)
}

func gitCmdOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}
