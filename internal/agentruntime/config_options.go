package agentruntime

import (
	"fmt"
	"slices"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/cli"
)

// EffortLevels is the closed set RebuildAdapterForEffort accepts, mirroring
// internal/tui/effort_picker.go's effortEntries — "" means provider
// default (no reasoning override injected).
var EffortLevels = []string{"", "low", "medium", "high"}

// IsValidEffortLevel reports whether level is one of EffortLevels.
func IsValidEffortLevel(level string) bool {
	return slices.Contains(EffortLevels, level)
}

// RebuildAdapterForEffort updates rt.ChatOptions.ReasoningEffort and
// reconstructs every adapter that bakes reasoning effort in at
// construction time — rt.Adapter and, when a router pair is configured
// for this session, rt.RouterAdapters — using exactly Build's own
// adapter-selection logic (RoutingEnabled → the freshly-rebuilt advisor
// adapter; otherwise a plain adapter.NewWithConfig), so a mid-session
// effort change can never silently knock a routed session off its
// advisor model the way an independent "just call NewWithConfig" rebuild
// could. Session-only: nothing is persisted to config.toml. No preflight
// re-probe — matches internal/tui/effort_picker.go's commitEffortChoice,
// which also skips it (the provider connection already worked at session
// start).
func RebuildAdapterForEffort(rt *Runtime, level string) error {
	if !IsValidEffortLevel(level) {
		return fmt.Errorf("invalid reasoning effort %q — use default, low, medium, or high", level)
	}
	rt.ChatOptions.ReasoningEffort = level

	routerAdapters, err := cli.BuildRouterAdapters(rt.FileCfg, rt.ChatOptions)
	if err != nil {
		// Non-fatal, mirrors Build's own tolerance for a stale/unresolved
		// pair: keep going on a plain adapter rather than losing an
		// otherwise-working session over a rebuild hiccup.
		routerAdapters = nil
	}

	var ad adapter.Client
	if rt.FileCfg.Router.RoutingEnabled() && routerAdapters != nil && routerAdapters.Advisor != nil {
		ad = routerAdapters.Advisor
		if routerAdapters.AdvisorModel != "" {
			rt.ChatOptions.Model = routerAdapters.AdvisorModel
			rt.Model = routerAdapters.AdvisorModel
		}
	} else {
		ad = adapter.NewWithConfig(adapterConfig(rt.ChatOptions, rt.FileCfg))
	}
	rt.Adapter = ad
	rt.Cfg.Adapter = ad
	if rt.AgentTool != nil {
		rt.AgentTool.Adapter = ad
	}

	if routerAdapters != nil {
		rt.RouterAdapters = routerAdapters
		if rt.RoutingAuto {
			// Session already had subagent/summarizer routing turned on —
			// re-point that wiring at the freshly rebuilt pair instead of
			// leaving it on the stale one, mirroring
			// internal/tui/cmd_router.go's refreshRouterAdapters (only
			// re-applies when the session is currently in auto mode).
			_ = SetAdvisorRouting(rt, true)
		}
	}
	return nil
}

// SetAdvisorRouting toggles whether subagent dispatch and summarization
// route through rt.RouterAdapters' implementer/advisor pair. Mirrors
// internal/tui/cmd_router.go's applyRoutingOn/applyRoutingOff exactly —
// session-only (nothing persisted to config.toml), and deliberately does
// NOT touch rt.Adapter/rt.Cfg.Adapter: which model serves the main
// conversation is decided once, at Build time, by
// fileCfg.Router.RoutingEnabled() (see RebuildAdapterForEffort), not by
// this toggle. Returns an error when enabling is requested but no
// advisor/implementer pair is configured for this session — mirrors the
// TUI's own "/advisor on" guidance to configure one first.
func SetAdvisorRouting(rt *Runtime, enabled bool) error {
	if enabled && rt.RouterAdapters == nil {
		return fmt.Errorf("no advisor/implementer pair configured for this session")
	}

	implementer := routerImplementer(rt.RouterAdapters)
	implementerModel := routerImplementerModel(rt.RouterAdapters)
	advisor := routerAdvisor(rt.RouterAdapters)
	advisorModel := routerAdvisorModel(rt.RouterAdapters)

	if rt.AgentTool != nil {
		if enabled {
			rt.AgentTool.RouteAuto = true
			rt.AgentTool.ModelResolver = routerModelResolver(rt.RouterAdapters, true)
			rt.AgentTool.ImplementerAdapter = implementer
			rt.AgentTool.ImplementerModel = implementerModel
			rt.AgentTool.AdvisorAdapter = advisor
			rt.AgentTool.AdvisorModel = advisorModel
			rt.AgentTool.FastAdapter = implementer
			rt.AgentTool.FastModel = implementerModel
			rt.AgentTool.SmartAdapter = advisor
			rt.AgentTool.SmartModel = advisorModel
		} else {
			rt.AgentTool.RouteAuto = false
			rt.AgentTool.ModelResolver = nil
			rt.AgentTool.ImplementerAdapter = nil
			rt.AgentTool.ImplementerModel = ""
			rt.AgentTool.AdvisorAdapter = nil
			rt.AgentTool.AdvisorModel = ""
			rt.AgentTool.FastAdapter = nil
			rt.AgentTool.FastModel = ""
			rt.AgentTool.SmartAdapter = nil
			rt.AgentTool.SmartModel = ""
		}
	}
	if rt.Cfg.Compaction != nil {
		if enabled {
			rt.Cfg.Compaction.Summarizer = implementer
		} else {
			rt.Cfg.Compaction.Summarizer = nil
		}
	}
	rt.RoutingAuto = enabled
	return nil
}
