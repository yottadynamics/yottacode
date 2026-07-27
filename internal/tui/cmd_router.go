package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
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

// cmdAdvisor is the /advisor handler: configure cache-safe role routing
// between an advisor model and an implementer model. Subcommands:
//
//	/advisor      — open the picker (toggle routing + pick fast/smart models)
//	/advisor on   — enable routing (auto); persists [router].mode = "auto"
//	/advisor off  — disable routing; persists [router].mode = "off"
//
// All changes persist to config.toml and apply live. "manual" mode (route
// only subagents with explicit `model:` frontmatter) stays config-only;
// `/advisor on` always means auto.
func cmdAdvisor(m Model, args []string) (Model, tea.Cmd) {
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
			"[advisor] unknown subcommand %q — use /advisor, /advisor on, or /advisor off", sub)))
		return m, nil
	}
}

// routerOn enables auto routing via the persistent path. Requires a
// configured fast/smart pair; without one it points the user at the
// picker (bare /router) where they can choose models.
func (m Model) routerOn() (Model, tea.Cmd) {
	if m.router == nil {
		m.appendLine(styleAuto.Render(
			"[advisor] no advisor/implementer pair configured — run /advisor to pick models"))
		return m, nil
	}
	return commitRouterMode(m, config.RouterModeAuto)
}

// routerOff disables routing via the persistent path.
func (m Model) routerOff() (Model, tea.Cmd) {
	return commitRouterMode(m, config.RouterModeOff)
}

// applyRoutingOn wires the live session for auto routing: subagents and
// summarization run on the implementer model, while the advisor remains
// available for plan mode and consult_advisor. Mutates the shared AgentTool
// (pointer) and the summarizer in place. No logging, no persistence — callers
// own those. No-op when no advisor/implementer pair is built.
func applyRoutingOn(m *Model) {
	if m.router == nil {
		return
	}
	if m.subagentTool != nil {
		m.subagentTool.RouteAuto = true
		m.subagentTool.ModelResolver = routerResolve(m.router)
		m.subagentTool.ImplementerAdapter = routerImplementer(m.router)
		m.subagentTool.ImplementerModel = routerImplementerModel(m.router)
		m.subagentTool.AdvisorAdapter = routerAdvisor(m.router)
		m.subagentTool.AdvisorModel = routerAdvisorModel(m.router)
		m.subagentTool.FastAdapter = routerImplementer(m.router)
		m.subagentTool.FastModel = routerImplementerModel(m.router)
		m.subagentTool.SmartAdapter = routerAdvisor(m.router)
		m.subagentTool.SmartModel = routerAdvisorModel(m.router)
	}
	if implementer := routerImplementer(m.router); implementer != nil {
		m.summarizerAdapter = implementer
		m.summarizerModel = routerImplementerModel(m.router)
	}
	m.routerMode = config.RouterModeAuto
	syncMainConsultAdvisorTool(m)
}

// applyRoutingOff wires the live session back to the active model for
// every isolated context. Inverse of applyRoutingOn; no logging/persist.
func applyRoutingOff(m *Model) {
	if m.subagentTool != nil {
		m.subagentTool.RouteAuto = false
		m.subagentTool.ModelResolver = nil
		m.subagentTool.ImplementerAdapter = nil
		m.subagentTool.ImplementerModel = ""
		m.subagentTool.AdvisorAdapter = nil
		m.subagentTool.AdvisorModel = ""
		m.subagentTool.FastAdapter = nil
		m.subagentTool.FastModel = ""
		m.subagentTool.SmartAdapter = nil
		m.subagentTool.SmartModel = ""
	}
	// nil, NOT a snapshot of m.cfg.Adapter: chooseSummarizer falls
	// through to the LIVE m.cfg.Adapter when no summarizer is pinned.
	// Snapshotting the adapter here froze compaction onto whatever
	// model was active at /router off — a later /model switch kept
	// summarizing on the old endpoint.
	m.summarizerAdapter = nil
	m.summarizerModel = ""
	m.routerMode = config.RouterModeOff
	syncMainConsultAdvisorTool(m)
}

// syncMainConsultAdvisorTool exposes consult_advisor only to a top-level
// session that is actively driven by the routed implementer model. Subagents
// get their own consult_advisor registration from AgentTool.buildChildRegistry;
// this live registry sync is for the main conversation loop only.
func syncMainConsultAdvisorTool(m *Model) {
	if m == nil || m.cfg.Registry == nil {
		return
	}
	advisor := routerAdvisor(m.router)
	advisorModel := routerAdvisorModel(m.router)
	implementerModel := routerImplementerModel(m.router)
	shouldExpose := routerModeOrOff(m.routerMode) == config.RouterModeAuto &&
		advisor != nil && advisorModel != "" && implementerModel != "" && m.modelName == implementerModel
	if !shouldExpose {
		m.cfg.Registry.Deregister(agent.ConsultAdvisorToolName)
		return
	}
	m.cfg.Registry.Register(&agent.ConsultAdvisorTool{Advisor: advisor, Model: advisorModel})
}
