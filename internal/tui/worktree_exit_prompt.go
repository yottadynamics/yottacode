package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// requestImmediateExit preserves Ctrl+C's fast-exit semantics except when the
// session is currently inside a yottacode-managed worktree. In that case the
// user gets one explicit keep/remove choice so managed worktrees do not linger
// just because the fastest exit gesture was used.
func requestImmediateExit(m Model) (Model, tea.Cmd) {
	name := m.worktree
	if name == "" {
		name = worktreeNameFromPath(m.cwd)
	}
	if name == "" {
		return m, tea.Quit
	}
	m.worktreeExitConfirmOpen = true
	m.worktreeExitConfirmName = name
	m.worktreeExitCleanup = ""
	m.worktreeExitGraceful = false
	return m, nil
}

// requestWorktreeAwareGracefulExit runs the normal graceful-exit path after the
// worktree keep/remove decision. That preserves final-memory behavior for /quit
// and Ctrl+D without letting the process leave a worktree unintentionally.
func requestWorktreeAwareGracefulExit(m Model) (tea.Model, tea.Cmd) {
	out, _ := requestImmediateExit(m)
	if out.worktreeExitConfirmOpen {
		out.worktreeExitGraceful = true
		return out, nil
	}
	return maybeStartExitSaveTurn(out)
}

// renderWorktreeExitConfirm uses the same labeled-box chrome as approval
// prompts so the title is embedded in the top border instead of duplicated in
// the body. The modal is intentionally compact because Ctrl+C is often a
// panic/cleanup gesture.
func renderWorktreeExitConfirm(m Model) string {
	name := m.worktreeExitConfirmName
	if name == "" {
		name = m.worktree
	}
	capW := capLabeledBoxWidth(m.width)
	body := "This yottacode session is running inside worktree " + styleInlineCommand.Render(name) + ".\n\n" +
		"Choose what to do before yottacode exits:\n\n" +
		"[R] Remove worktree and delete its worktree-* branch\n" +
		"[K] Keep worktree in place\n\n" +
		"Esc cancels exit"
	wrapped := hardWrapLabeled(body, capW)
	bodyLines := make([]string, 0, len(wrapped)+2)
	bodyLines = append(bodyLines, strings.Repeat(" ", approvalModalTargetInnerWidth(capW)))
	for _, line := range wrapped {
		bodyLines = append(bodyLines, labeledBoxIndent+line)
	}
	bodyLines = append(bodyLines, "")
	leftLabel := " " + styleApprovalTitle.Render("Exit worktree session?") + " "
	rightLabel := " " + styleApprovalTool.Render(name) + " "
	return renderLabeledBox(leftLabel, rightLabel, bodyLines, capW, colorWarning)
}

func (m Model) updateWorktreeExitConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "r", "R":
		return m.confirmWorktreeExit("remove")
	case "k", "K", "enter":
		return m.confirmWorktreeExit("keep")
	case "esc":
		m.worktreeExitConfirmOpen = false
		m.worktreeExitConfirmName = ""
		m.worktreeExitCleanup = ""
		return m, nil
	}
	return m, nil
}

func (m Model) confirmWorktreeExit(cleanup string) (Model, tea.Cmd) {
	m.worktreeExitConfirmOpen = false
	m.worktreeExitCleanup = cleanup
	if m.worktreeExitGraceful {
		m.worktreeExitGraceful = false
		out, cmd := maybeStartExitSaveTurn(m)
		return out.(Model), cmd
	}
	return m, tea.Quit
}

func cleanupCurrentWorktreeOnExit(ctx context.Context, cwd, name, cleanup string) (string, error) {
	if name == "" || cleanup == "" || cleanup == "keep" {
		return "", nil
	}
	if cleanup != "remove" {
		return "", fmt.Errorf("worktree exit cleanup: unknown cleanup %q", cleanup)
	}
	repoRoot, err := worktree.ResolveRepoRoot(ctx, cwd)
	if err != nil {
		return "", fmt.Errorf("worktree exit cleanup: resolve repo root: %w", err)
	}
	// The TUI process may still have its cwd inside the worktree the user chose
	// to delete. Move the process back to the main checkout before invoking git.
	if err := os.Chdir(repoRoot); err != nil {
		return "", fmt.Errorf("worktree exit cleanup: chdir %s: %w", repoRoot, err)
	}
	wtDir := worktree.Dir(repoRoot, name)
	if err := worktree.Remove(ctx, repoRoot, wtDir, true); err != nil {
		return "", fmt.Errorf("worktree exit cleanup: remove %s: %w", name, err)
	}
	return repoRoot, nil
}

func cleanupCurrentWorktreeOnExitWithTimeout(cwd, name, cleanup string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return cleanupCurrentWorktreeOnExit(ctx, cwd, name, cleanup)
}

func applyWorktreeExitRepoRoot(m *Model, cwdRef *agent.CwdRef, repoRoot string) {
	if repoRoot == "" || m == nil {
		return
	}
	// Removing the worktree invalidates the session's old cwd. Persist the
	// originating repo root instead so resuming the same session does not point
	// tools and status chips back at a deleted ~/.yottacode/worktrees path.
	m.cwd = repoRoot
	m.worktree = ""
	if m.sess != nil {
		m.sess.Cwd = repoRoot
		m.sess.Worktree = ""
	}
	if cwdRef != nil {
		cwdRef.Set(repoRoot)
	}
}
