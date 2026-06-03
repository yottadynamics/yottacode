package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// gitMergeTestRepo creates a throwaway git repo with an initial commit on
// the default branch and returns its path. Commits use -c identity so the
// test doesn't depend on the developer's global git config.
func gitMergeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.email=t@t", "-c", "user.name=t",
			"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
		if _, err := gitOutput(ctx, dir, full...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "base")
	return dir
}

func gitMergeCommitFile(t *testing.T, dir, branch, file, content, base string) {
	t.Helper()
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
		if _, err := gitOutput(ctx, dir, full...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("checkout", "-b", branch, base)
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", branch+" edits "+file)
	run("checkout", base)
}

// TestGitMerge_CleanDisjoint is the partition happy path: two branches that
// touched different files merge into the base with no conflict.
func TestGitMerge_CleanDisjoint(t *testing.T) {
	dir := gitMergeTestRepo(t)
	gitMergeCommitFile(t, dir, "a", "a.txt", "from a\n", "main")
	gitMergeCommitFile(t, dir, "b", "b.txt", "from b\n", "main")

	for _, br := range []string{"a", "b"} {
		conflicts, err := gitMerge(context.Background(), dir, br)
		if err != nil {
			t.Fatalf("merge %s: %v (conflicts=%v)", br, err, conflicts)
		}
		if len(conflicts) != 0 {
			t.Fatalf("merge %s: unexpected conflicts %v", br, conflicts)
		}
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s present after merges: %v", f, err)
		}
	}
}

// TestGitMerge_Conflict surfaces a same-file collision: two branches editing
// the same line. The second merge must report the conflicted path.
func TestGitMerge_Conflict(t *testing.T) {
	dir := gitMergeTestRepo(t)
	gitMergeCommitFile(t, dir, "x", "shared.txt", "x version\n", "main")
	gitMergeCommitFile(t, dir, "y", "shared.txt", "y version\n", "main")

	if conflicts, err := gitMerge(context.Background(), dir, "x"); err != nil || len(conflicts) != 0 {
		t.Fatalf("first merge should be clean: err=%v conflicts=%v", err, conflicts)
	}
	conflicts, err := gitMerge(context.Background(), dir, "y")
	if err == nil {
		t.Fatal("expected conflict error on second merge")
	}
	found := false
	for _, c := range conflicts {
		if c == "shared.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected shared.txt in conflicts, got %v", conflicts)
	}
}

func TestGitWorktreeDirty(t *testing.T) {
	dir := gitMergeTestRepo(t)
	if gitWorktreeDirty(context.Background(), dir) {
		t.Error("clean repo reported dirty")
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitWorktreeDirty(context.Background(), dir) {
		t.Error("repo with untracked file reported clean")
	}
}
