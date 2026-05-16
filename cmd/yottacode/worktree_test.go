package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/trust"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

func TestEnsureWorktreeNoOpWhenEmpty(t *testing.T) {
	repo := mkRepoCmd(t)
	t.Chdir(repo)

	opts := &cli.ChatOptions{} // Worktree == ""
	require.NoError(t, ensureWorktree(context.Background(), opts))
	require.Equal(t, "", opts.Worktree)

	// cwd should not have moved
	got, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, repo, got)
}

func TestEnsureWorktreeRefusesUntrusted(t *testing.T) {
	repo := mkRepoCmd(t)
	t.Chdir(repo)
	// Point trust store at an empty file in tmpdir so repo isn't trusted.
	t.Setenv("HOME", t.TempDir())

	opts := &cli.ChatOptions{Worktree: "feature-untrusted"}
	err := ensureWorktree(context.Background(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "workspace trust not accepted")
}

func TestEnsureWorktreeCreatesAndChdirs(t *testing.T) {
	repo := mkRepoCmd(t)
	t.Chdir(repo)
	// Trust the repo.
	trustRepoInHome(t, repo)

	opts := &cli.ChatOptions{Worktree: "feature-cli"}
	require.NoError(t, ensureWorktree(context.Background(), opts))
	require.Equal(t, "feature-cli", opts.Worktree)

	got, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, worktree.Dir(repo, "feature-cli"), got)
}

func TestEnsureWorktreeAutoGenerates(t *testing.T) {
	repo := mkRepoCmd(t)
	t.Chdir(repo)
	trustRepoInHome(t, repo)

	opts := &cli.ChatOptions{Worktree: cli.WorktreeAutoGenerate}
	require.NoError(t, ensureWorktree(context.Background(), opts))
	require.NotEqual(t, "", opts.Worktree)
	require.NotEqual(t, cli.WorktreeAutoGenerate, opts.Worktree)
}

func TestEnsureWorktreeAttachesExisting(t *testing.T) {
	repo := mkRepoCmd(t)
	t.Chdir(repo)
	trustRepoInHome(t, repo)

	// First call creates
	opts1 := &cli.ChatOptions{Worktree: "feature-attach"}
	require.NoError(t, ensureWorktree(context.Background(), opts1))

	// Chdir back to repo so second call starts where the first did
	require.NoError(t, os.Chdir(repo))

	// Second call attaches (no error)
	opts2 := &cli.ChatOptions{Worktree: "feature-attach"}
	require.NoError(t, ensureWorktree(context.Background(), opts2))
}

func TestWorktreeListCmd(t *testing.T) {
	repo := mkRepoCmd(t)
	t.Chdir(repo)
	trustRepoInHome(t, repo)

	// Create one worktree via ensureWorktree
	opts := &cli.ChatOptions{Worktree: "in-list"}
	require.NoError(t, ensureWorktree(context.Background(), opts))
	require.NoError(t, os.Chdir(repo))

	cmd := newWorktreeListCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))
	require.Contains(t, buf.String(), "in-list")
	require.Contains(t, buf.String(), "branch=worktree-in-list")
}

func TestWorktreeRemoveCmd(t *testing.T) {
	repo := mkRepoCmd(t)
	t.Chdir(repo)
	trustRepoInHome(t, repo)

	opts := &cli.ChatOptions{Worktree: "to-remove"}
	require.NoError(t, ensureWorktree(context.Background(), opts))
	require.NoError(t, os.Chdir(repo))

	cmd := newWorktreeRemoveCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, []string{"to-remove"}))
	require.Contains(t, buf.String(), "removed worktree to-remove")

	if _, err := os.Stat(filepath.Join(repo, ".yottacode", "worktrees", "to-remove")); !os.IsNotExist(err) {
		t.Fatalf("expected worktree removed, stat=%v", err)
	}
}

// trustRepoInHome points HOME at a tmp dir and writes a
// trusted-roots.json that includes repo. Used by tests that need
// ensureWorktree's trust gate to pass.
func trustRepoInHome(t *testing.T, repo string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := &trust.Store{Version: trust.Version}
	_, err := store.Add(repo)
	require.NoError(t, err)
	storePath := filepath.Join(home, ".yottacode", "trusted-roots.json")
	require.NoError(t, trust.Save(storePath, store))
}

// mkRepoCmd creates a fresh git repo with an initial commit for the
// cmd/yottacode tests. Mirrors the helper in internal/worktree but
// stays local so we don't depend on the test export.
func mkRepoCmd(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		_, err := runGit(context.Background(), resolved, args...)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(resolved, "README.md"), []byte("# t\n"), 0o644))
	_, err = runGit(context.Background(), resolved, "add", "README.md")
	require.NoError(t, err)
	_, err = runGit(context.Background(), resolved, "commit", "-q", "-m", "init")
	require.NoError(t, err)
	return resolved
}
