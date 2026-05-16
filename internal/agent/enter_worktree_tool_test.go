package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/worktree"
	"github.com/stretchr/testify/require"
)

func TestEnterWorktreeCreatesAndCopiesIncludes(t *testing.T) {
	repo := mkRepoForAgent(t)
	// .worktreeinclude with .env pattern; place the .env file.
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte(".env\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=abc\n"), 0o644))

	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	out, err := tool.Execute(context.Background(), `{"name":"feature-x","base":"head"}`)
	require.NoError(t, err)
	require.Contains(t, out, "created worktree")
	require.Contains(t, out, "worktree-feature-x")

	wtDir := worktree.Dir(repo, "feature-x")
	if _, err := os.Stat(wtDir); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}
	// .env should have been copied
	got, err := os.ReadFile(filepath.Join(wtDir, ".env"))
	require.NoError(t, err)
	require.Equal(t, "SECRET=abc\n", string(got))
}

func TestEnterWorktreeAutogeneratesName(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	out, err := tool.Execute(context.Background(), `{"base":"head"}`)
	require.NoError(t, err)
	require.Contains(t, out, "created worktree")
	// Path should be under ~/.yottacode/worktrees/<slug>/.
	require.Contains(t, out, worktree.SlugDir(repo))
}

func TestEnterWorktreeAttachesExisting(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	// First create
	_, err := tool.Execute(context.Background(), `{"name":"reuse-me","base":"head"}`)
	require.NoError(t, err)
	// Second call with same name → attach
	out, err := tool.Execute(context.Background(), `{"name":"reuse-me","base":"head"}`)
	require.NoError(t, err)
	require.Contains(t, out, "attached to existing worktree")
}

func TestEnterWorktreeRejectsBadName(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := tool.Execute(context.Background(), `{"name":".bad","base":"head"}`)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "name must not start"))
}

func TestEnterWorktreeRejectsBadBase(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := tool.Execute(context.Background(), `{"name":"x","base":"wrong"}`)
	require.Error(t, err)
}

// mkRepoForAgent creates a tmp git repo with an initial commit. Kept
// separate from the worktree-package test helper so the agent tests
// don't take a back-import dependency. Also pins HOME to a per-test
// tmpdir so the post-relocation worktrees land in an isolated
// ~/.yottacode/worktrees/ rather than the developer's real home.
func mkRepoForAgent(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		_, err := gitOutput(context.Background(), resolved, args...)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(resolved, "README.md"), []byte("# t\n"), 0o644))
	_, err = gitOutput(context.Background(), resolved, "add", "README.md")
	require.NoError(t, err)
	_, err = gitOutput(context.Background(), resolved, "commit", "-q", "-m", "init")
	require.NoError(t, err)
	return resolved
}
