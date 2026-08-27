package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yottadynamics/yottacode/internal/worktree"
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

// TestEnterWorktreeRejectsWhenSandboxActive: a container-backed Sandbox
// only has the session's original cwd bind-mounted, so swapping CwdRef to
// a worktree path would point every subsequent run_bash exec at a
// directory the container can't see. enter_worktree must refuse loudly
// rather than let that happen silently.
func TestEnterWorktreeRejectsWhenSandboxActive(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo), Sandbox: &spySandbox{label: "[podman]"}}
	_, err := tool.Execute(context.Background(), `{"name":"x","base":"head"}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "podman command sandbox is active")
	// No worktree should have been created — the refusal must happen
	// before any side effect.
	if _, statErr := os.Stat(worktree.Dir(repo, "x")); !os.IsNotExist(statErr) {
		t.Errorf("worktree should not exist after a refused enter_worktree, stat err = %v", statErr)
	}
}

type lazySandboxNoLiveProfiles struct{ spySandbox }

func (s lazySandboxNoLiveProfiles) LiveProfiles() []SandboxProfile { return nil }

type lazySandboxWithLiveProfiles struct{ spySandbox }

func (s lazySandboxWithLiveProfiles) LiveProfiles() []SandboxProfile {
	return []SandboxProfile{SandboxProfileDefault}
}

func TestEnterWorktreeAllowsLazySandboxBeforeProfileCreated(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo), Sandbox: &lazySandboxNoLiveProfiles{}}
	_, err := tool.Execute(context.Background(), `{"name":"x","base":"head"}`)
	require.NoError(t, err)
}

func TestEnterWorktreeRejectsLazySandboxWithLiveProfile(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo), Sandbox: &lazySandboxWithLiveProfiles{}}
	_, err := tool.Execute(context.Background(), `{"name":"x","base":"head"}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "podman command sandbox is active")
}

// TestEnterWorktreeAllowsWhenSandboxNil confirms the common (no sandbox)
// case is unaffected — a nil Sandbox field behaves exactly as before this
// guard existed.
func TestEnterWorktreeAllowsWhenSandboxNil(t *testing.T) {
	repo := mkRepoForAgent(t)
	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	_, err := tool.Execute(context.Background(), `{"name":"x","base":"head"}`)
	require.NoError(t, err)
}

// mkRepoForAgent creates a tmp git repo with an initial commit. Kept
// separate from the worktree-package test helper so the agent tests
// don't take a back-import dependency. Also pins HOME to a per-test
// tmpdir so the post-relocation worktrees land in an isolated
// ~/.yottacode/worktrees/ rather than the developer's real home.
func mkRepoForAgent(t *testing.T) string {
	t.Helper()
	// HOME is resolved through EvalSymlinks so paths built from $HOME
	// (worktree.Dir, SlugDir) match what os.Getwd reports after chdir.
	// macOS's /var → /private/var symlink would otherwise leave the
	// expected path in one form and the actual in the other.
	homeResolved, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", homeResolved)
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
