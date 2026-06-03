package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitListBranches returns short branch names matching a glob.
func gitListBranches(t *testing.T, repoRoot, glob string) []string {
	t.Helper()
	out, err := gitOutput(context.Background(), repoRoot, "branch", "--list", glob, "--format=%(refname:short)")
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	return nonEmptyLines(out)
}

// gitCommitFileOnBranch creates `branch` off `base`, writes `content` to
// `file`, commits, and returns to `base`. Identity comes from the repo
// config (set by dispatchTestRepo).
func gitCommitFileOnBranch(t *testing.T, repoRoot, branch, file, content, base string) {
	t.Helper()
	ctx := context.Background()
	for _, args := range [][]string{{"checkout", "-q", "-b", branch, base}} {
		if _, err := gitOutput(ctx, repoRoot, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", branch}, {"checkout", "-q", base}} {
		if _, err := gitOutput(ctx, repoRoot, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}

func TestIntegrate_Disabled(t *testing.T) {
	it := &IntegrateTool{Cwd: NewCwdRef(t.TempDir()), Enabled: false}
	out, _ := it.Execute(context.Background(), `{"branches":["x"]}`)
	if !strings.Contains(out, "experimental") {
		t.Errorf("expected experimental gate, got %q", out)
	}
}

func TestIntegrate_NoBranches(t *testing.T) {
	it := &IntegrateTool{Cwd: NewCwdRef(t.TempDir()), Enabled: true}
	out, _ := it.Execute(context.Background(), `{"branches":[]}`)
	if !strings.Contains(out, "at least one branch") {
		t.Errorf("got %q", out)
	}
}

// TestIntegrate_CleanMerge runs the full dispatch→integrate happy path: two
// disjoint write subtasks produce two branches, which integrate merges into
// one integration branch carrying both files.
func TestIntegrate_CleanMerge(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	_, err := d.Execute(context.Background(), `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	branches := gitListBranches(t, repoRoot, "worktree-dispatch-*")
	if len(branches) != 2 {
		t.Fatalf("expected 2 dispatch branches, got %v", branches)
	}

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	argsJSON := `{"branches":["` + branches[0] + `","` + branches[1] + `"]}`
	out, err := it.Execute(context.Background(), argsJSON)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "Integrated 2 branch") {
		t.Fatalf("expected clean integration, got:\n%s", out)
	}

	integ := gitListBranches(t, repoRoot, "dispatch-integration-*")
	if len(integ) != 1 {
		t.Fatalf("expected 1 integration branch, got %v", integ)
	}
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		content, e := gitOutput(context.Background(), repoRoot, "show", integ[0]+":"+f)
		if e != nil {
			t.Errorf("integration branch missing %s: %v", f, e)
			continue
		}
		if strings.TrimSpace(content) != "hello from "+f {
			t.Errorf("%s content = %q", f, content)
		}
	}
}

// TestIntegrate_Conflict forces two branches editing the same file and
// asserts integrate stops and reports the conflicted file + the integration
// branch to resume with.
func TestIntegrate_Conflict(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	gitCommitFileOnBranch(t, repoRoot, "feat-x", "base.txt", "x change\n", "main")
	gitCommitFileOnBranch(t, repoRoot, "feat-y", "base.txt", "y change\n", "main")

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(context.Background(), `{"branches":["feat-x","feat-y"],"integration_branch":"dispatch-integration-test"}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "CONFLICT") {
		t.Fatalf("expected conflict report, got:\n%s", out)
	}
	if !strings.Contains(out, "base.txt") {
		t.Errorf("conflict report should name base.txt:\n%s", out)
	}
	if !strings.Contains(out, "dispatch-integration-test") {
		t.Errorf("conflict report should name the integration branch to resume:\n%s", out)
	}
	// feat-x merged cleanly before the conflict.
	if !strings.Contains(out, "feat-x") {
		t.Errorf("expected feat-x reported as merged:\n%s", out)
	}
}
