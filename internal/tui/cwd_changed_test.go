package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// When the agent loop emits agent.CwdChanged after enter_worktree
// swaps the session's working directory, the TUI Update handler must
// refresh m.cwd, m.worktree, and the persisted session state so the
// status bar and `sessions list` reflect the new location.
func TestModel_CwdChanged_EntersWorktree(t *testing.T) {
	m := newTestModel(t)
	// Sanity: a fresh test model starts in the main checkout (no
	// worktree chip rendered).
	if m.worktree != "" {
		t.Fatalf("precondition: expected empty worktree, got %q", m.worktree)
	}

	// newTestModel pins HOME via t.TempDir; build a worktree-shaped path
	// under that home so worktree.IsAnyWorktreePath recognizes it.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	newCwd := filepath.Join(home, ".yottacode", "worktrees", "proj-a1b2c3d4", "feature-x")
	m, _ = applyMsg(m, agentEventMsg{ev: agent.CwdChanged{NewCwd: newCwd}})

	if m.cwd != newCwd {
		t.Errorf("m.cwd = %q, want %q", m.cwd, newCwd)
	}
	if m.worktree != "feature-x" {
		t.Errorf("m.worktree = %q, want feature-x", m.worktree)
	}
	if m.sess != nil {
		if m.sess.Cwd != newCwd {
			t.Errorf("m.sess.Cwd = %q, want %q", m.sess.Cwd, newCwd)
		}
		if m.sess.Worktree != "feature-x" {
			t.Errorf("m.sess.Worktree = %q, want feature-x", m.sess.Worktree)
		}
	}
}

// exit_worktree swaps cwd back to the repo root. m.worktree should
// clear (no chip in the status bar) and the session record should
// reflect "back to main checkout."
func TestModel_CwdChanged_ExitsToRepoRoot(t *testing.T) {
	m := newTestModel(t)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	worktreeCwd := filepath.Join(home, ".yottacode", "worktrees", "proj-a1b2c3d4", "feature-x")
	m, _ = applyMsg(m, agentEventMsg{ev: agent.CwdChanged{NewCwd: worktreeCwd}})
	if m.worktree != "feature-x" {
		t.Fatalf("setup: expected m.worktree=feature-x, got %q", m.worktree)
	}

	// Now exit. The repo root is anywhere outside ~/.yottacode/worktrees.
	repoRoot := "/home/me/proj"
	m, _ = applyMsg(m, agentEventMsg{ev: agent.CwdChanged{NewCwd: repoRoot}})

	if m.cwd != repoRoot {
		t.Errorf("m.cwd = %q, want %q", m.cwd, repoRoot)
	}
	if m.worktree != "" {
		t.Errorf("m.worktree should clear on exit, got %q", m.worktree)
	}
	if m.sess != nil {
		if m.sess.Cwd != repoRoot {
			t.Errorf("m.sess.Cwd = %q, want %q", m.sess.Cwd, repoRoot)
		}
		if m.sess.Worktree != "" {
			t.Errorf("m.sess.Worktree should clear on exit, got %q", m.sess.Worktree)
		}
	}
}
