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

	// Create a second commit and an existing branch for testing (without checking it out)
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "existing.txt"), []byte("existing content\n"), 0644))
	require.NoError(t, runGit(repoPath, "add", "existing.txt"))
	require.NoError(t, runGit(repoPath, "commit", "-m", "add existing file"))
	// Create the branch existing-branch from master (current HEAD) without checking it out
	require.NoError(t, runGit(repoPath, "branch", "existing-branch"))
	// We are still on master

	ctx := context.Background()

	// Test GitWorktreeListTool (should list at least the main worktree)
	listTool := &GitWorktreeListTool{Cwd: repoPath}
	out, err := listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.Contains(t, out, repoPath) // The main worktree path should appear

	// Test GitWorktreeAddTool with no branch (uses HEAD)
	addTool := &GitWorktreeAddTool{Cwd: repoPath}
	worktreePath1 := filepath.Join(repoPath, ".yottacode", "worktrees", "test-worktree-1")
	out, err = addTool.Execute(ctx, `{"path": "`+worktreePath1+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "added worktree at "+worktreePath1)

	// Test GitWorktreeAddTool with existing branch
	worktreePath2 := filepath.Join(repoPath, ".yottacode", "worktrees", "test-worktree-2")
	out, err = addTool.Execute(ctx, `{"path": "`+worktreePath2+`", "branch": "existing-branch"}`)
	require.NoError(t, err)
	require.Contains(t, out, "added worktree at "+worktreePath2+" (from existing branch existing-branch)")

	// Test GitWorktreeAddTool with new branch (creates and checks out)
	worktreePath3 := filepath.Join(repoPath, ".yottacode", "worktrees", "test-worktree-3")
	out, err = addTool.Execute(ctx, `{"path": "`+worktreePath3+`", "branch": "new-feature"}`)
	require.NoError(t, err)
	require.Contains(t, out, "added worktree at "+worktreePath3+" (created and checked out new branch new-feature)")

	// After adding, list should show multiple worktrees
	out, err = listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.Contains(t, out, worktreePath1)
	require.Contains(t, out, worktreePath2)
	require.Contains(t, out, worktreePath3)

	// Test GitWorktreeLockTool
	lockTool := &GitWorktreeLockTool{Cwd: repoPath}
	out, err = lockTool.Execute(ctx, `{"path": "`+worktreePath1+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "locked worktree at "+worktreePath1)

	// Test GitWorktreeUnlockTool
	unlockTool := &GitWorktreeUnlockTool{Cwd: repoPath}
	out, err = unlockTool.Execute(ctx, `{"path": "`+worktreePath1+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "unlocked worktree at "+worktreePath1)

	// Test GitWorktreePruneTool (should not error, but note: we have a locked worktree so it won't be pruned)
	pruneTool := &GitWorktreePruneTool{Cwd: repoPath}
	out, err = pruneTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.Contains(t, out, "pruned stale worktree data")

	// Test GitWorktreeRemoveTool
	removeTool := &GitWorktreeRemoveTool{Cwd: repoPath}
	out, err = removeTool.Execute(ctx, `{"path": "`+worktreePath1+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "removed worktree at "+worktreePath1)

	// After removal, list should show the remaining worktrees
	out, err = listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.NotContains(t, out, worktreePath1)
	require.Contains(t, out, worktreePath2)
	require.Contains(t, out, worktreePath3)

	// Test GitWorktreeMoveTool
	moveTool := &GitWorktreeMoveTool{Cwd: repoPath}
	worktreePath4 := filepath.Join(repoPath, ".yottacode", "worktrees", "test-worktree-4")
	newWorktreePath := filepath.Join(repoPath, ".yottacode", "worktrees", "renamed-worktree")
	// First add a worktree to move
	out, err = addTool.Execute(ctx, `{"path": "`+worktreePath4+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "added worktree at "+worktreePath4)
	// Then move it
	out, err = moveTool.Execute(ctx, `{"path": "`+worktreePath4+`", "new-path": "`+newWorktreePath+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "moved worktree from "+worktreePath4+" to "+newWorktreePath)
	// Verify it's moved by checking list
	out, err = listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.NotContains(t, out, worktreePath4)
	require.Contains(t, out, newWorktreePath)

	// Test GitWorktreeRepairTool (without path - repair all)
	repairTool := &GitWorktreeRepairTool{Cwd: repoPath}
	out, err = repairTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.Contains(t, out, "repaired all worktrees")
	// Test GitWorktreeRepairTool with path
	out, err = repairTool.Execute(ctx, `{"path": "`+newWorktreePath+`"}`)
	require.NoError(t, err)
	require.Contains(t, out, "repaired worktree at "+newWorktreePath)

	// Clean up: remove the remaining worktrees
	removeTool2 := &GitWorktreeRemoveTool{Cwd: repoPath}
	out, err = removeTool2.Execute(ctx, `{"path": "`+worktreePath2+`"}`)
	require.NoError(t, err)
	removeTool3 := &GitWorktreeRemoveTool{Cwd: repoPath}
	out, err = removeTool3.Execute(ctx, `{"path": "`+worktreePath3+`"}`)
	require.NoError(t, err)
	removeTool4 := &GitWorktreeRemoveTool{Cwd: repoPath}
	out, err = removeTool4.Execute(ctx, `{"path": "`+newWorktreePath+`"}`)
	require.NoError(t, err)

	// Final list should show only the main worktree
	out, err = listTool.Execute(ctx, "{}")
	require.NoError(t, err)
	require.NotContains(t, out, worktreePath2)
	require.NotContains(t, out, worktreePath3)
	require.NotContains(t, out, newWorktreePath)
	require.Contains(t, out, repoPath) // main worktree remains
}

// Helper function to run git commands in a given directory
func runGit(dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := gitOutput(ctx, dir, args...)
	return err
}