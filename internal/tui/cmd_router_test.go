package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
)

// testRouterAdapters builds a real fast/smart adapter pair the way run.go
// does, with mode left "off" — exercising the build-decoupled-from-mode
// behavior the /router toggle relies on.
func testRouterAdapters(t *testing.T) *cli.RouterAdapters {
	t.Helper()
	cfg := routerTestConfig()
	cfg.Router.Mode = config.RouterModeOff // build is decoupled from mode
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := cli.BuildRouterAdapters(cfg, cli.ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	if ra == nil {
		t.Fatal("expected adapters built for a configured pair in off mode")
	}
	return ra
}

// routerTestConfig is a config with one curated provider that lists both
// router models, so config.Validate accepts fast/smart refs.
func routerTestConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = []config.Provider{{
		Name:         "anthropic",
		Kind:         "anthropic",
		BaseURL:      "https://api.anthropic.com",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-opus-4-6",
		Models: []config.Model{
			{Name: "claude-opus-4-6", Tier: "expensive"},
			{Name: "claude-haiku-4-5", Tier: "cheap"},
		},
	}}
	return cfg
}

func TestRouterModeOrOff(t *testing.T) {
	cases := map[string]string{
		"":         config.RouterModeOff,
		"off":      config.RouterModeOff,
		"manual":   config.RouterModeManual,
		"auto":     config.RouterModeAuto,
		"bogus":    config.RouterModeOff,
		"  auto  ": config.RouterModeAuto,
	}
	for in, want := range cases {
		if got := routerModeOrOff(in); got != want {
			t.Errorf("routerModeOrOff(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRouterSummarizer_AutoOnly locks the bug fix: summarization routes
// to the fast model only in auto. Manual/off return a nil streamer so
// summarizerOrDefault keeps compaction on the active model.
func TestRouterSummarizer_AutoOnly(t *testing.T) {
	ra := testRouterAdapters(t)

	if ad, name := routerSummarizer(ra, false); ad != nil || name != "" {
		t.Errorf("non-auto routerSummarizer = (%v, %q), want (nil, \"\")", ad, name)
	}
	ad, name := routerSummarizer(ra, true)
	if ad == nil {
		t.Error("auto routerSummarizer should return the fast streamer")
	}
	if name != "claude-haiku-4-5" {
		t.Errorf("auto routerSummarizer name = %q, want claude-haiku-4-5", name)
	}
}

// TestRouterModelResolver_GatedByEnabled proves explicit-frontmatter
// routing is off in off mode (nil resolver) and active when enabled.
func TestRouterModelResolver_GatedByEnabled(t *testing.T) {
	ra := testRouterAdapters(t)

	if r := routerModelResolver(ra, false); r != nil {
		t.Error("off mode should yield a nil resolver")
	}
	r := routerModelResolver(ra, true)
	if r == nil {
		t.Fatal("enabled mode should yield a resolver")
	}
	if got := r("claude-haiku-4-5"); got == nil {
		t.Error("resolver should resolve a configured model")
	}
}

func TestApplyRoutingOn_WiresSubagentAndSummarizer(t *testing.T) {
	ra := testRouterAdapters(t)
	main := &scriptedAdapter{}
	m := Model{
		router:       ra,
		subagentTool: &agent.AgentTool{},
		cfg:          agent.LoopConfig{Adapter: main},
		routerMode:   config.RouterModeOff,
	}

	applyRoutingOn(&m)

	if !m.subagentTool.RouteAuto {
		t.Error("applyRoutingOn should set RouteAuto")
	}
	if m.subagentTool.ModelResolver == nil {
		t.Error("applyRoutingOn should install the ModelResolver")
	}
	if m.summarizerAdapter == nil {
		t.Error("applyRoutingOn should route the summarizer to the fast model")
	}
	if m.summarizerModel != "claude-haiku-4-5" {
		t.Errorf("summarizerModel = %q, want claude-haiku-4-5", m.summarizerModel)
	}
	if m.routerMode != config.RouterModeAuto {
		t.Errorf("routerMode = %q, want auto", m.routerMode)
	}
}

func TestApplyRoutingOff_RevertsToActiveModel(t *testing.T) {
	ra := testRouterAdapters(t)
	main := &scriptedAdapter{}
	m := Model{
		router:            ra,
		subagentTool:      &agent.AgentTool{RouteAuto: true, ModelResolver: func(string) agent.Streamer { return nil }},
		summarizerAdapter: ra.Fast,
		summarizerModel:   "claude-haiku-4-5",
		cfg:               agent.LoopConfig{Adapter: main},
		routerMode:        config.RouterModeAuto,
	}

	applyRoutingOff(&m)

	if m.subagentTool.RouteAuto {
		t.Error("applyRoutingOff should clear RouteAuto")
	}
	if m.subagentTool.ModelResolver != nil {
		t.Error("applyRoutingOff should clear the ModelResolver")
	}
	if m.summarizerModel != "" {
		t.Errorf("summarizerModel = %q, want empty", m.summarizerModel)
	}
	if m.routerMode != config.RouterModeOff {
		t.Errorf("routerMode = %q, want off", m.routerMode)
	}
	if m.summarizerAdapter != agentStreamer(main) {
		t.Error("applyRoutingOff should point the summarizer back at the active adapter")
	}
}

// TestApplyRoutingOn_NoPairConfigured: a no-op (and no panic) when no
// fast/smart pair is built.
func TestApplyRoutingOn_NoPairConfigured(t *testing.T) {
	m := Model{routerMode: config.RouterModeOff}
	applyRoutingOn(&m)
	if m.routerMode != config.RouterModeOff {
		t.Errorf("applyRoutingOn with no pair should stay off, got %q", m.routerMode)
	}
}

// TestRouterOn_NoPairHint: /router on without a configured pair prints a
// hint and does not change mode (no disk write attempted).
func TestRouterOn_NoPairHint(t *testing.T) {
	m := Model{routerMode: config.RouterModeOff, transcript: &strings.Builder{}}
	m, _ = m.routerOn()
	if m.routerMode != config.RouterModeOff {
		t.Errorf("routerOn with no pair should stay off, got %q", m.routerMode)
	}
	if !strings.Contains(stripANSI(m.transcript.String()), "no fast/smart pair configured") {
		t.Errorf("expected a 'no pair configured' hint: %q", m.transcript.String())
	}
}

// TestCmdRouter_BareOpensPicker: bare /router opens the picker overlay.
func TestCmdRouter_BareOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdRouter(m, nil)
	if !m.routerPickerOpen {
		t.Error("bare /router should open the router picker")
	}
	if m.routerPicker == nil {
		t.Error("router picker state should be initialized")
	}
}

// TestStatusBar_RendersRoutingChip: an active auto router shows a chip
// naming the smart model it delegates subagents to (the fast model is
// summarization-only, so the chip tracks the subagent target).
func TestStatusBar_RendersRoutingChip(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 160, Height: 24})
	m.routerMode = config.RouterModeAuto
	m.router = &cli.RouterAdapters{FastModel: "claude-haiku-4-5", SmartModel: "claude-opus-4-6"}
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "routing: claude-opus-4-6") {
		t.Errorf("status bar should name the smart model in the routing chip: %q", plain)
	}
}

func TestStatusBar_RoutingChipManual(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 160, Height: 24})
	m.routerMode = config.RouterModeManual
	m.router = &cli.RouterAdapters{FastModel: "claude-haiku-4-5", SmartModel: "claude-opus-4-6"}
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "routing: manual") {
		t.Errorf("manual mode should show 'routing: manual': %q", plain)
	}
}

func TestStatusBar_NoRoutingChipWhenOff(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 160, Height: 24})
	m.routerMode = config.RouterModeOff
	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "routing") {
		t.Errorf("off-mode status bar should not render a routing chip: %q", plain)
	}
}
