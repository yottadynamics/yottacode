package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectStateClean(t *testing.T) {
	repo := mkRepo(t)
	ctx := context.Background()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feature-clean")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-feature-clean", wt)

	s, err := DetectState(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Clean() {
		t.Fatalf("expected clean, got %+v", s)
	}
}

func TestDetectStateUncommitted(t *testing.T) {
	repo := mkRepo(t)
	ctx := context.Background()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feature-mod")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-feature-mod", wt)
	// Modify a tracked file
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("# modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := DetectState(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasUncommitted {
		t.Fatalf("expected uncommitted, got %+v", s)
	}
	if s.Clean() {
		t.Fatalf("expected dirty, got clean")
	}
}

func TestDetectStateUntracked(t *testing.T) {
	repo := mkRepo(t)
	ctx := context.Background()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feature-new")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-feature-new", wt)
	if err := os.WriteFile(filepath.Join(wt, "newfile.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := DetectState(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasUntracked {
		t.Fatalf("expected untracked, got %+v", s)
	}
}

func TestRemoveCleanWorktreeAndBranch(t *testing.T) {
	repo := mkRepo(t)
	ctx := context.Background()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feature-rm")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-feature-rm", wt)

	if err := Remove(ctx, repo, wt, false); err != nil {
		t.Fatal(err)
	}
	// Worktree gone
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed, stat err = %v", err)
	}
	// Branch gone
	if _, err := gitOut(ctx, repo, "rev-parse", "--verify", "worktree-feature-rm"); err == nil {
		t.Fatalf("expected branch deleted")
	}
}

func TestRemoveDirtyRequiresForce(t *testing.T) {
	repo := mkRepo(t)
	ctx := context.Background()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feature-dirty")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-feature-dirty", wt)
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("# dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without force, git refuses
	if err := Remove(ctx, repo, wt, false); err == nil {
		t.Fatalf("expected remove without force to fail on dirty tree")
	}
	// With force, removes
	if err := Remove(ctx, repo, wt, true); err != nil {
		t.Fatalf("force remove failed: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed after force, stat err = %v", err)
	}
}

// Removing the last worktree under a slug dir should also remove the
// empty slug dir AND its .origin metadata, so deleting your only
// worktree returns ~/.yottacode/worktrees/ to a clean state. A
// non-empty slug dir (sibling worktree present) is left alone.
func TestRemoveCleansEmptySlugDir(t *testing.T) {
	repo := mkRepo(t)
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	// One worktree + an .origin file (as enter_worktree would have
	// produced). Slug dir is its parent.
	wt := Dir(repo, "solo")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-solo", wt)
	slugDir := filepath.Dir(wt)
	if err := WriteOrigin(slugDir, repo); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}
	if _, err := os.Stat(slugDir); err != nil {
		t.Fatalf("slug dir not created: %v", err)
	}
	if _, err := os.Stat(OriginPath(slugDir)); err != nil {
		t.Fatalf(".origin not created: %v", err)
	}

	if err := Remove(ctx, repo, wt, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(slugDir); !os.IsNotExist(err) {
		t.Fatalf("slug dir should be removed when last worktree goes; stat err = %v", err)
	}
}

func TestRemoveLeavesSlugDirWhenSiblingExists(t *testing.T) {
	repo := mkRepo(t)
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	wt1 := Dir(repo, "alpha")
	wt2 := Dir(repo, "beta")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-alpha", wt1)
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-beta", wt2)
	slugDir := filepath.Dir(wt1) // same as Dir(wt2)

	if err := Remove(ctx, repo, wt1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(slugDir); err != nil {
		t.Fatalf("slug dir should survive while sibling worktree remains: %v", err)
	}
	if _, err := os.Stat(wt2); err != nil {
		t.Fatalf("sibling worktree must still exist: %v", err)
	}
}

func TestStateReasons(t *testing.T) {
	s := State{HasUncommitted: true, HasUntracked: true, HasUnpushed: true}
	r := s.Reasons()
	if len(r) != 3 {
		t.Fatalf("expected 3 reasons, got %v", r)
	}
}
