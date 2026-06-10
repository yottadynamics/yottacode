package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/config"
)

// routerModeOrOff normalizes a configured [router].mode to one of
// "off"/"manual"/"auto", treating blank/unknown as "off".
func routerModeOrOff(mode string) string {
	switch strings.TrimSpace(mode) {
	case config.RouterModeManual:
		return config.RouterModeManual
	case config.RouterModeAuto:
		return config.RouterModeAuto
	default:
		return config.RouterModeOff
	}
}

// cmdRouter is the /router handler: configure cache-safe task routing
// between a fast and a smart model. Subcommands:
//
//	/router      — open the picker (toggle routing + pick fast/smart models)
//	/router on   — enable routing (auto); persists [router].mode = "auto"
//	/router off  — disable routing; persists [router].mode = "off"
//
// All changes persist to config.toml and apply live. "manual" mode (route
// only subagents with explicit `model:` frontmatter) stays config-only;
// `/router on` always means auto.
func cmdRouter(m Model, args []string) (Model, tea.Cmd) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case "":
		m.openRouterPicker()
		return m, nil
	case "on", "auto":
		return m.routerOn()
	case "off":
		return m.routerOff()
	default:
		m.appendLine(styleAuto.Render(fmt.Sprintf(
			"[router] unknown subcommand %q — use /router, /router on, or /router off", sub)))
		return m, nil
	}
}

// routerOn enables auto routing via the persistent path. Requires a
// configured fast/smart pair; without one it points the user at the
// picker (bare /router) where they can choose models.
func (m Model) routerOn() (Model, tea.Cmd) {
	if m.router == nil {
		m.appendLine(styleAuto.Render(
			"[router] no fast/smart pair configured — run /router to pick models"))
		return m, nil
	}
	return commitRouterMode(m, config.RouterModeAuto)
}

// routerOff disables routing via the persistent path.
func (m Model) routerOff() (Model, tea.Cmd) {
	return commitRouterMode(m, config.RouterModeOff)
}

// applyRoutingOn wires the live session for auto routing: read-only
// subagents and summarization run on the fast model, other delegated work
// on the smart model. Mutates the shared AgentTool (pointer) and the
// summarizer in place. No logging, no persistence — callers own those.
// No-op when no fast/smart pair is built.
func applyRoutingOn(m *Model) {
	if m.router == nil {
		return
	}
	if m.subagentTool != nil {
		m.subagentTool.RouteAuto = true
		m.subagentTool.ModelResolver = routerResolve(m.router)
	}
	if fast := routerFast(m.router); fast != nil {
		m.summarizerAdapter = fast
		m.summarizerModel = routerFastModel(m.router)
	}
	m.routerMode = config.RouterModeAuto
}

// applyRoutingOff wires the live session back to the active model for
// every isolated context. Inverse of applyRoutingOn; no logging/persist.
func applyRoutingOff(m *Model) {
	if m.subagentTool != nil {
		m.subagentTool.RouteAuto = false
		m.subagentTool.ModelResolver = nil
	}
	// nil, NOT a snapshot of m.cfg.Adapter: chooseSummarizer falls
	// through to the LIVE m.cfg.Adapter when no summarizer is pinned.
	// Snapshotting the adapter here froze compaction onto whatever
	// model was active at /router off — a later /model switch kept
	// summarizing on the old endpoint.
	m.summarizerAdapter = nil
	m.summarizerModel = ""
	m.routerMode = config.RouterModeOff
}
