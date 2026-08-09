package tui

import (
	tea "charm.land/bubbletea/v2"
)

// cmdSandbox is the /sandbox handler: opens the picker for choosing how
// run_bash executes — inside a podman sandbox (with this session's auto
// mode, or with regular approval prompts) or directly on the host (no
// sandbox, today's default). See sandbox_picker.go.
func cmdSandbox(m Model, args []string) (Model, tea.Cmd) {
	return m.openSandboxPicker()
}
