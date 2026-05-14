package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitWorktreeTools(t *testing.T) {
	// Create a temporary directory for the test repo
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoPath, 0755))

	// Initialize a git repository
	require.NoError(t, runGit(repoPath, "init"))
	require.NoError(t, runGit(repoPath, "config", "user.name", "Test User"))
	require.NoError(t, runGit(repoPath, "config", "user.email", "test@example.com"))

	// Create an initial commit so we have a branch to work from
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Test Repo\n"), 0644))
	require.NoError(t, runGit(repoPath, "add", "README.md"))
	require.NoError(t, runGit(repoPath, "commit", "-m", "initial commit"))

	ctx := context.Background()

	// Test GitWorktreeListTool (should list at least the main worktree)
	listTool := &GitWorktreeListTool{Cwd: repoPath}
	out, err := listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.Contains(t, out, repoPath) // The main worktree path should appear

	// Test GitWorktreeAddTool
	addTool := &GitWorktreeAddTool{Cwd: repoPath}
	worktreePath := filepath.Join(repoPath, ".yottacode", "worktrees", "test-worktree")
	out, err = addTool.Execute(ctx, `{"path": "`+worktreePath+`", "branch": "feature-test"}`)
	require.NoError(t, err)
	require.Contains(t, out, "added worktree at "+worktreePath)

	// After adding, list should show two worktrees
	out, err = listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	// Count lines that start with the worktree path (porcelain format: each worktree starts with a line with its path)
	// We'll just check that the worktreePath appears in the output.
	require.Contains(t, out, worktreePath)

	// Test GitWorktreeLockTool
	lockTool := &GitWorktreeLockTool{Cwd: repoPath}
	out, err = lockTool.Execute(ctx, `{"path": "`+worktreePath+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "locked worktree at "+worktreePath)

	// Test GitWorktreeUnlockTool
	unlockTool := &GitWorktreeUnlockTool{Cwd: repoPath}
	out, err = unlockTool.Execute(ctx, `{"path": "`+worktreePath+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "unlocked worktree at "+worktreePath)

	// Test GitWorktreePruneTool (should not error, but note: we have a locked worktree so it won't be pruned)
	pruneTool := &GitWorktreePruneTool{Cwd: repoPath}
	out, err = pruneTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.Contains(t, out, "pruned stale worktree data")

	// Test GitWorktreeRemoveTool
	removeTool := &GitWorktreeRemoveTool{Cwd: repoPath}
	out, err = removeTool.Execute(ctx, `{"path": "`+worktreePath+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "removed worktree at "+worktreePath)

	// After removal, list should show only the main worktree again
	out, err = listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	// The removed worktree path should no longer appear
	require.NotContains(t, out, worktreePath)
}

// Helper function to run git commands in a given directory
func runGit(dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := gitOutput(ctx, dir, args...)
	return err
}