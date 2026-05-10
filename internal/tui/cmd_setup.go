package tui

import (
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/config"
)

// setupDoneMsg fires when the suspended `yottacode setup` subprocess
// returns. The TUI uses it to re-load config.toml so any new
// providers / active selection immediately take effect.
type setupDoneMsg struct {
	err error
}

// cmdSetup handles `/setup` — suspends the TUI, spawns
// `<self> setup` as a subprocess, and reloads config when it returns.
//
// Spawning a subprocess (rather than nesting a tea.Program inside the
// chat program) keeps the two Bubbletea programs from fighting over
// the same TTY. tea.ExecProcess hands the terminal off cleanly and
// restores it when the child exits.
func cmdSetup(m Model, _ []string) (Model, tea.Cmd) {
	self, err := os.Executable()
	if err != nil {
		m.appendLine(styleError.Render("[setup] cannot resolve own binary: " + err.Error()))
		return m, nil
	}
	cmd := exec.Command(self, "setup")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return setupDoneMsg{err: err}
	})
}

// handleSetupDone is called from the model's main Update switch. We
// reload config.toml and patch the in-memory file config so any new
// tunables take effect on the next turn. We do NOT swap the active
// adapter mid-session — the user can do that with /provider use or
// by quitting and relaunching, since changing the adapter mid-flight
// would require rebuilding the system prompt and the connection
// probe; that's a clear restart-worthy event.
func handleSetupDone(m Model, msg setupDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render("[setup] " + msg.err.Error()))
		return m, nil
	}
	cfgPath, err := config.DefaultPath()
	if err != nil {
		m.appendLine(styleError.Render("[setup] " + err.Error()))
		return m, nil
	}
	newCfg, err := config.Load(cfgPath)
	if err != nil {
		m.appendLine(styleError.Render("[setup] config reload: " + err.Error()))
		return m, nil
	}
	m.fileCfg = newCfg
	m.appendLine(styleAuto.Render("[setup] config reloaded — /provider use <name> to switch, or restart to pick up the new active provider"))
	return m, nil
}
