package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/config"
)

// toggleAutoMode is the entry/exit helper used by the Shift+Tab cycle,
// the plan-card [A] hotkey, and the --permission-mode auto startup
// flag. Mirroring Claude Code, there is no /auto slash command —
// users enter auto only via the keybinding or the startup flag.
// Idempotent at the boundaries. Entering auto turns plan off
// (mutually exclusive modes).
func toggleAutoMode(m Model) (Model, tea.Cmd) {
	state := m.cfg.AutoMode
	if state == nil {
		return m, nil
	}
	if state.IsActive() {
		exitAutoMode(&m)
		return m, nil
	}
	// Entering: clear plan mode if it's on (plan and auto are
	// mutually exclusive — plan = thinking, auto = implementing
	// with safety floor). Yolo is NOT exited — it's an orthogonal
	// overlay flag that can sit alongside any mode.
	if m.cfg.PlanMode.IsActive() {
		exitPlanMode(&m)
	}
	state.Active.Store(true)
	if routerModeOrOff(m.routerMode) != config.RouterModeOff {
		m, _ = m.switchActiveModelToRouterRole("implementer")
	}
	m.appendLine(styleAutoBannerLabel.Render(SysMsg(SysState, "auto mode", "active")) +
		" " + styleAutoBannerHint.Render("— edits auto-allow; run_bash, git_commit, git_checkpoint, rollback still prompt"))
	m.appendLine(styleAutoBannerHint.Render("  Shift+Tab cycles onward: auto → plan → normal"))
	// Visual breather between the entry log and the live banner.
	m.appendLine("")
	return m, nil
}

// exitAutoMode is the cleanup half of toggleAutoMode. Also reused by
// the Shift+Tab cycle when moving auto → plan, and when the user
// explicitly toggles off.
func exitAutoMode(m *Model) {
	state := m.cfg.AutoMode
	if state == nil {
		return
	}
	wasActive := state.Active.Swap(false)
	if !wasActive {
		return
	}
	m.appendLine(styleAutoBannerLabel.Render(SysMsg(SysState, "auto mode", "exited")) +
		" " + styleAutoBannerHint.Render("— re-enter with Shift+Tab"))
}

// cycleAgentMode cycles through normal → auto → plan → normal. The
// three states the Shift+Tab chord traverses. Each transition reuses
// the existing toggle helpers so the entry/exit log lines stay
// consistent with /plan invocations and the startup flags.
func cycleAgentMode(m Model) (Model, tea.Cmd) {
	autoOn := m.cfg.AutoMode.IsActive()
	planOn := m.cfg.PlanMode.IsActive()
	switch {
	case !autoOn && !planOn:
		// Normal → auto.
		return toggleAutoMode(m)
	case autoOn && !planOn:
		// Auto → plan. toggleAutoMode would just turn auto off; we
		// want to chain into plan, so do the transition directly so
		// the entry banners appear in the right order in scrollback.
		exitAutoMode(&m)
		return togglePlanMode(m)
	case !autoOn && planOn:
		// Plan → normal.
		return togglePlanMode(m)
	default:
		// Defensive: both shouldn't be on simultaneously, but recover
		// gracefully by clearing both.
		exitAutoMode(&m)
		exitPlanMode(&m)
		return m, nil
	}
}
