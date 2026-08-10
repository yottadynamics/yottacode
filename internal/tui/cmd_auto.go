package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/config"
)

// cmdAuto is the slash-command handler for /auto. It jumps directly
// to the implementation stop in the Shift+Tab cycle so users can enter
// or exit auto mode without walking through plan/yolo.
func cmdAuto(m Model, _ []string) (Model, tea.Cmd) {
	return toggleAutoMode(m)
}

// toggleAutoMode is the entry/exit helper used by /auto, the
// Shift+Tab cycle, the plan-card [A] hotkey, and the
// --permission-mode auto startup flag.
// Idempotent at the boundaries. Entering auto turns plan off
// (mutually exclusive modes).
func toggleAutoMode(m Model) (Model, tea.Cmd) {
	state := m.cfg.AutoMode
	if state == nil {
		m.appendLine(styleError.Render("[auto] auto mode is not available in this build"))
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
	m.appendLine(styleAutoBannerHint.Render("  Shift+Tab cycles onward: auto → yolo → normal → plan"))
	// Visual breather between the entry log and the live banner.
	m.appendLine("")

	return m, nil
}

// exitAutoMode is the cleanup half of toggleAutoMode. Also reused by
// the Shift+Tab cycle when moving auto → yolo, and when the user
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

// cycleAgentMode cycles through normal → plan → auto → yolo → normal.
// Each transition reuses existing helpers so the entry/exit log lines
// stay consistent with slash commands and startup flags.
func cycleAgentMode(m Model) (Model, tea.Cmd) {
	autoOn := m.cfg.AutoMode.IsActive()
	planOn := m.cfg.PlanMode.IsActive()
	yoloOn := m.cfg.YoloMode.IsActive()
	switch {
	case yoloOn:
		// Yolo → normal. Yolo is implemented as an overlay, but the
		// cycle treats it as the final always-approve stop.
		exitYoloMode(&m)
		return m, nil
	case !autoOn && !planOn:
		// Normal → plan.
		return togglePlanMode(m)
	case !autoOn && planOn:
		// Plan → auto. toggleAutoMode exits plan first, preserving the
		// same cleanup path used by /auto.
		return toggleAutoMode(m)
	case autoOn && !planOn:
		// Auto → yolo. Yolo is the always-approve stop after bounded
		// auto mode, so clear auto before entering the overlay.
		exitAutoMode(&m)
		return enterYoloMode(m), nil
	default:
		// Defensive: these states shouldn't be on simultaneously, but
		// recover gracefully by clearing every mode/overlay.
		exitAutoMode(&m)
		exitPlanMode(&m)
		exitYoloMode(&m)
		return m, nil
	}
}
