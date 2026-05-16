package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/worktree"
	"github.com/stretchr/testify/require"
)

func TestWorktreeStatusSingleClean(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"st-clean","base":"head"}`)
	require.NoError(t, err)

	st := &WorktreeStatusTool{Cwd: NewCwdRef(repo)}
	out, err := st.Execute(context.Background(), `{"name":"st-clean"}`)
	require.NoError(t, err)
	require.Contains(t, out, "st-clean")
	require.Contains(t, out, "branch=worktree-st-clean")
	require.Contains(t, out, "clean")
}

func TestWorktreeStatusSingleDirty(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"st-dirty","base":"head"}`)
	require.NoError(t, err)
	wtDir := worktree.Dir(repo, "st-dirty")
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "scratch.txt"), []byte("x\n"), 0o644))

	st := &WorktreeStatusTool{Cwd: NewCwdRef(repo)}
	out, err := st.Execute(context.Background(), `{"name":"st-dirty"}`)
	require.NoError(t, err)
	require.Contains(t, out, "dirty")
	require.Contains(t, out, "untracked files")
}

func TestWorktreeStatusAll(t *testing.T) {
	repo := mkRepoForAgent(t)
	enter := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := enter.Execute(context.Background(), `{"name":"st-a","base":"head"}`)
	require.NoError(t, err)
	_, err = enter.Execute(context.Background(), `{"name":"st-b","base":"head"}`)
	require.NoError(t, err)

	st := &WorktreeStatusTool{Cwd: NewCwdRef(repo)}
	out, err := st.Execute(context.Background(), `{}`)
	require.NoError(t, err)
	require.Contains(t, out, "st-a")
	require.Contains(t, out, "st-b")
}

func TestWorktreeStatusEmpty(t *testing.T) {
	repo := mkRepoForAgent(t)
	st := &WorktreeStatusTool{Cwd: NewCwdRef(repo)}
	out, err := st.Execute(context.Background(), `{}`)
	require.NoError(t, err)
	require.Contains(t, out, "no yottacode worktrees")
}
