package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestWorktreeExitPrompt_CtrlCInWorktreeOpensPrompt(t *testing.T) {
	m := newTestModel(t)
	m.cwd = testWorktreeCwd(t, "feature-x")
	m.worktree = "feature-x"

	m, cmd := applyMsg(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatalf("Ctrl+C in worktree should open prompt, got cmd %T", cmd)
	}
	if !m.worktreeExitConfirmOpen {
		t.Fatalf("Ctrl+C in worktree should open worktree exit prompt")
	}
	if got := m.worktreeExitConfirmName; got != "feature-x" {
		t.Fatalf("worktreeExitConfirmName = %q, want feature-x", got)
	}
}

func TestWorktreeExitPrompt_EscKeepsSessionOpen(t *testing.T) {
	m := newTestModel(t)
	m.cwd = testWorktreeCwd(t, "feature-x")
	m.worktree = "feature-x"
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	m, cmd := applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("Esc should cancel the worktree exit prompt, got cmd %T", cmd)
	}
	if m.worktreeExitConfirmOpen {
		t.Fatalf("Esc should close the worktree exit prompt")
	}
}

func TestWorktreeExitPrompt_KeepQuitsWithoutRemoving(t *testing.T) {
	m := newTestModel(t)
	m.cwd = testWorktreeCwd(t, "feature-x")
	m.worktree = "feature-x"
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	m, cmd := applyMsg(m, tea.KeyPressMsg{Text: "k"})
	if m.worktreeExitConfirmOpen {
		t.Fatalf("keep should close the worktree exit prompt")
	}
	if cmd == nil {
		t.Fatalf("keep should quit after preserving the worktree")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("keep should return tea.QuitMsg")
	}
	if got := m.worktreeExitCleanup; got != "keep" {
		t.Fatalf("worktreeExitCleanup = %q, want keep", got)
	}
}

func TestWorktreeExitPrompt_RemoveRunsCleanupThenQuits(t *testing.T) {
	m := newTestModel(t)
	m.cwd = testWorktreeCwd(t, "feature-x")
	m.worktree = "feature-x"
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	m, cmd := applyMsg(m, tea.KeyPressMsg{Text: "r"})
	if m.worktreeExitConfirmOpen {
		t.Fatalf("remove should close the worktree exit prompt")
	}
	if cmd == nil {
		t.Fatalf("remove should run cleanup then quit")
	}
	if got := m.worktreeExitCleanup; got != "remove" {
		t.Fatalf("worktreeExitCleanup = %q, want remove", got)
	}
}

func TestWorktreeExitPrompt_MainCheckoutCtrlCStillQuits(t *testing.T) {
	m := newTestModel(t)

	_, cmd := applyMsg(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatalf("Ctrl+C outside a worktree should quit immediately")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl+C outside a worktree should return tea.QuitMsg")
	}
}

func TestWorktreeExitPrompt_RenderUsesLabeledModalChrome(t *testing.T) {
	m := newTestModel(t)
	m.worktreeExitConfirmOpen = true
	m.worktreeExitConfirmName = "feature-x"

	plain := stripANSI(renderWorktreeExitConfirm(m))
	first := strings.SplitN(plain, "\n", 2)[0]
	if !strings.Contains(first, "Exit worktree session?") {
		t.Fatalf("top border should embed title, got %q", first)
	}
	if !strings.Contains(first, "feature-x") {
		t.Fatalf("top border should embed worktree name, got %q", first)
	}
	if strings.Count(plain, "Exit worktree session?") != 1 {
		t.Fatalf("title should render exactly once, got:\n%s", plain)
	}
}

func TestWorktreeExitPrompt_CleanupKeepDoesNotRewriteCwd(t *testing.T) {
	repoRoot, err := cleanupCurrentWorktreeOnExit(context.Background(), testWorktreeCwd(t, "feature-x"), "feature-x", "keep")
	if err != nil {
		t.Fatalf("cleanup keep should not fail: %v", err)
	}
	if repoRoot != "" {
		t.Fatalf("cleanup keep should not rewrite cwd, got %q", repoRoot)
	}
}

func TestWorktreeExitPrompt_ApplyRepoRootClearsSessionWorktree(t *testing.T) {
	m := newTestModel(t)
	cwdRef := agent.NewCwdRef(m.cwd)
	m.cwd = testWorktreeCwd(t, "feature-x")
	m.worktree = "feature-x"
	m.sess.Cwd = m.cwd
	m.sess.Worktree = "feature-x"
	repoRoot := filepath.Join(t.TempDir(), "repo")

	applyWorktreeExitRepoRoot(&m, cwdRef, repoRoot)

	if m.cwd != repoRoot {
		t.Fatalf("model cwd = %q, want repo root %q", m.cwd, repoRoot)
	}
	if m.worktree != "" {
		t.Fatalf("model worktree should clear after removal, got %q", m.worktree)
	}
	if m.sess.Cwd != repoRoot {
		t.Fatalf("session cwd = %q, want repo root %q", m.sess.Cwd, repoRoot)
	}
	if m.sess.Worktree != "" {
		t.Fatalf("session worktree should clear after removal, got %q", m.sess.Worktree)
	}
	if cwdRef.Get() != repoRoot {
		t.Fatalf("cwdRef = %q, want repo root %q", cwdRef.Get(), repoRoot)
	}
}

func testWorktreeCwd(t *testing.T, name string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return filepath.Join(home, ".yottacode", "worktrees", "proj-a1b2c3d4", name)
}
