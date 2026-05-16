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

// End-to-end: after enter_worktree, a subsequent write_file with a
// relative path lands inside the worktree — not the main repo. Then
// exit_worktree returns the session cwd to the repo root and the
// next write lands there. This is the regression test for the v1
// gap where enter_worktree only returned an absolute path; the agent
// had to thread absolute paths thereafter. With CwdRef in place, the
// relative-path flow Just Works.
func TestCwdSwap_EnterAndExit(t *testing.T) {
	repo := mkRepoForAgent(t)
	defer mustChdir(t, repo)
	mustChdir(t, repo)

	cwdRef := NewCwdRef(repo)

	enter := &EnterWorktreeTool{Cwd: cwdRef}
	// WriteOpts.Cwd shares the same CwdRef — that's how the production
	// wiring works in tui/run.go and oneshot/oneshot.go. Sharing means
	// the write validator follows enter_worktree / exit_worktree swaps
	// without needing a separate refresh path.
	write := &WriteFileTool{Cwd: cwdRef, WriteOpts: WritePathOptions{Cwd: cwdRef}}
	exit := &ExitWorktreeTool{Cwd: cwdRef}

	// 1. Enter the worktree — should swap cwd via cwdRef + os.Chdir.
	_, err := enter.Execute(context.Background(), `{"name":"swap-x","base":"head"}`)
	require.NoError(t, err)

	wtDir := worktree.Dir(repo, "swap-x")
	require.Equal(t, wtDir, cwdRef.Get(), "cwdRef should now point at the worktree")
	gotCwd, _ := os.Getwd()
	gotCwdResolved, _ := filepath.EvalSymlinks(gotCwd)
	require.Equal(t, wtDir, gotCwdResolved, "process cwd should also be the worktree")

	// 2. write_file with a relative path lands INSIDE the worktree, not
	//    the original repo. WriteOpts.Cwd is intentionally still the
	//    original repo (cwd-confinement perimeter is unchanged) — but
	//    the worktree dir sits under that perimeter, so the write is
	//    allowed by ValidateWritePath.
	_, err = write.Execute(context.Background(), `{"path":"hello.txt","content":"in worktree\n"}`)
	require.NoError(t, err)
	// The file should live inside the worktree, not the repo root.
	if _, err := os.Stat(filepath.Join(wtDir, "hello.txt")); err != nil {
		t.Fatalf("expected hello.txt in worktree (%s); stat=%v", wtDir, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "hello.txt")); err == nil {
		t.Fatalf("hello.txt should NOT have landed in the main repo (%s)", repo)
	}

	// 3. exit_worktree (cleanup=keep so we can still inspect after) —
	//    cwd should return to the repo root.
	out, err := exit.Execute(context.Background(), `{"cleanup":"keep"}`)
	require.NoError(t, err)
	require.Contains(t, out, "kept worktree swap-x")
	require.Equal(t, repo, cwdRef.Get(), "cwdRef should be back at repo root")
	gotCwd, _ = os.Getwd()
	gotCwdResolved, _ = filepath.EvalSymlinks(gotCwd)
	require.Equal(t, repo, gotCwdResolved, "process cwd should be back at repo root")

	// 4. A subsequent write with a relative path lands in the repo,
	//    not the worktree.
	_, err = write.Execute(context.Background(), `{"path":"after.txt","content":"in repo\n"}`)
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(repo, "after.txt")); err != nil {
		t.Fatalf("expected after.txt in repo root after exit; stat=%v", err)
	}
}

// Confirms enter_worktree's success message mentions the cwd swap (not
// the v1 wording that asked the agent to use absolute paths).
func TestEnterWorktreeReportsCwdSwap(t *testing.T) {
	repo := mkRepoForAgent(t)
	defer mustChdir(t, repo)
	mustChdir(t, repo)

	tool := &EnterWorktreeTool{Cwd: NewCwdRef(repo)}
	out, err := tool.Execute(context.Background(), `{"name":"msg-test","base":"head"}`)
	require.NoError(t, err)
	require.True(t,
		strings.Contains(out, "session cwd is now this directory"),
		"expected the result to mention the cwd swap, got: %s", out,
	)
}

func mustChdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
}
