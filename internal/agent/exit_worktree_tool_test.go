package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/worktree"
	"github.com/stretchr/testify/require"
)

func TestExitWorktreeAutoCleanRemoves(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"clean-x","base":"head"}`)
	require.NoError(t, err)

	exit := &ExitWorktreeTool{Cwd: NewCwdRef(repo)}
	out, err := exit.Execute(context.Background(), `{"name":"clean-x","cleanup":"auto"}`)
	require.NoError(t, err)
	require.Contains(t, out, "removed clean worktree clean-x")
	// Worktree dir should be gone
	wtDir := worktree.Dir(repo, "clean-x")
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed, err=%v", err)
	}
}

func TestExitWorktreeAutoDirtyKeeps(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"dirty-x","base":"head"}`)
	require.NoError(t, err)
	wtDir := worktree.Dir(repo, "dirty-x")
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "scratch.txt"), []byte("wip\n"), 0o644))

	exit := &ExitWorktreeTool{Cwd: NewCwdRef(repo)}
	out, err := exit.Execute(context.Background(), `{"name":"dirty-x","cleanup":"auto"}`)
	require.NoError(t, err)
	require.Contains(t, out, "kept worktree dirty-x")
	require.Contains(t, out, "untracked files")
	// Worktree still exists
	if _, err := os.Stat(wtDir); err != nil {
		t.Fatalf("expected worktree dir to remain on dirty auto, err=%v", err)
	}
}

func TestExitWorktreeForceRemovesDirty(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"force-x","base":"head"}`)
	require.NoError(t, err)
	wtDir := worktree.Dir(repo, "force-x")
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "scratch.txt"), []byte("wip\n"), 0o644))

	exit := &ExitWorktreeTool{Cwd: NewCwdRef(repo)}
	out, err := exit.Execute(context.Background(), `{"name":"force-x","cleanup":"remove"}`)
	require.NoError(t, err)
	require.Contains(t, out, "removed worktree force-x")
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed after force, err=%v", err)
	}
}

func TestExitWorktreeKeep(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"hold-x","base":"head"}`)
	require.NoError(t, err)

	exit := &ExitWorktreeTool{Cwd: NewCwdRef(repo)}
	out, err := exit.Execute(context.Background(), `{"name":"hold-x","cleanup":"keep"}`)
	require.NoError(t, err)
	require.Contains(t, out, "kept worktree hold-x")
	wtDir := worktree.Dir(repo, "hold-x")
	if _, err := os.Stat(wtDir); err != nil {
		t.Fatalf("expected worktree dir to remain on cleanup=keep, err=%v", err)
	}
}

func TestExitWorktreeMissingNameFromMainRepo(t *testing.T) {
	repo := mkRepoForAgent(t)
	exit := &ExitWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := exit.Execute(context.Background(), `{}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not under <repo>/.yottacode/worktrees/")
}

func TestExitWorktreeInfersFromCwd(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"infer-x","base":"head"}`)
	require.NoError(t, err)
	wtDir := worktree.Dir(repo, "infer-x")

	exit := &ExitWorktreeTool{Cwd: NewCwdRef(wtDir)}
	out, err := exit.Execute(context.Background(), `{"cleanup":"auto"}`)
	require.NoError(t, err)
	require.Contains(t, out, "removed clean worktree infer-x")
}

func TestExitWorktreeBadCleanup(t *testing.T) {
	repo := mkRepoForAgent(t)
	exit := &ExitWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := exit.Execute(context.Background(), `{"name":"x","cleanup":"yeet"}`)
	require.Error(t, err)
}
