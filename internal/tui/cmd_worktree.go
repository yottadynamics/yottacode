package tui

import tea "charm.land/bubbletea/v2"

// cmdWorktree starts an agent turn that uses the existing enter_worktree tool
// rather than duplicating worktree creation in the TUI. Keeping this as a normal
// turn preserves tool approval, cwd-change events, and worktree cleanup behavior.
func cmdWorktree(m Model, _ []string) (Model, tea.Cmd) {
	out, cmd := m.startTurnWithDisplay("Create a new yottacode-managed worktree for this repository using the enter_worktree tool. Use a fresh generated name and the default fresh base.", "/worktree")
	return out.(Model), cmd
}
