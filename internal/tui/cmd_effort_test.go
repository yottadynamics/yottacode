package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
)

// /effort is registered so the palette and /help surface it.
func TestEffort_Registered(t *testing.T) {
	if findSlash("effort") == nil {
		t.Fatal("/effort is not registered in allSlash")
	}
}

func TestEffort_ShortcutSetsLevel(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/effort high")
	if m.reasoningEffort != "high" {
		t.Errorf("reasoningEffort = %q, want high", m.reasoningEffort)
	}
	// The adapter must be rebuilt so the new effort takes effect — a stale
	// adapter would silently keep the old setting.
	if m.cfg.Adapter == nil {
		t.Error("adapter should be rebuilt on /effort")
	}
}

func TestEffort_ShortcutDefaultClears(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/effort high")
	m, _ = m.runSlash("/effort default")
	if m.reasoningEffort != "" {
		t.Errorf("reasoningEffort = %q, want empty (provider default)", m.reasoningEffort)
	}
	// "off" and "none" are aliases for the same clear.
	m, _ = m.runSlash("/effort high")
	m, _ = m.runSlash("/effort off")
	if m.reasoningEffort != "" {
		t.Errorf("'off' should clear effort, got %q", m.reasoningEffort)
	}
}

func TestEffort_RejectsUnknownLevel(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/effort high")
	m, _ = m.runSlash("/effort turbo")
	if m.reasoningEffort != "high" {
		t.Errorf("an invalid level must not change the effort; got %q", m.reasoningEffort)
	}
	if !strings.Contains(m.transcript.String(), "unknown effort") {
		t.Error("expected an error message for an unknown level")
	}
}

func TestEffort_WarnsWhenNoOpOnModel(t *testing.T) {
	// newTestModel resolves to an openai-compatible endpoint, which
	// doesn't apply reasoning effort — setting a level should warn that
	// it's a no-op rather than silently doing nothing.
	m := newTestModel(t)
	m, _ = m.runSlash("/effort high")
	if !strings.Contains(m.transcript.String(), "no-op on this model") {
		t.Errorf("expected a no-op warning, transcript:\n%s", m.transcript.String())
	}
}

func TestEffort_PickerCarriesNoteForNoOpModel(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/effort")
	if m.effortPicker == nil || m.effortPicker.note == "" {
		t.Error("picker should carry a note when the active model can't use effort")
	}
}

func TestEffort_BareOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/effort")
	if !m.effortPickerOpen || m.effortPicker == nil {
		t.Fatal("/effort (bare) should open the picker")
	}
	// Cursor lands on the active level (default → "" → first row).
	if got := m.effortPicker.entries[m.effortPicker.cursor].level; got != "" {
		t.Errorf("picker opened on %q, want the active level \"\"", got)
	}
}

func TestEffort_PickerEnterCommits(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/effort")
	// Jump to the last row (high) and commit.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.effortPickerOpen {
		t.Error("Enter should close the picker")
	}
	if m.reasoningEffort != "high" {
		t.Errorf("reasoningEffort = %q, want high after committing the last row", m.reasoningEffort)
	}
}

func TestEffort_PickerEscIsNonDestructive(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/effort medium") // start from a known level
	m, _ = m.runSlash("/effort")        // open picker
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.effortPickerOpen {
		t.Error("Esc should close the picker")
	}
	if m.reasoningEffort != "medium" {
		t.Errorf("Esc must not change the effort; got %q, want medium", m.reasoningEffort)
	}
}

// commitEffortChoice must rebuild the router's advisor/implementer
// adapters, not just the main one. BuildRouterAdapters bakes reasoning
// effort into each adapter at construction time from m.opts, and m.opts
// is a startup snapshot /effort never used to update — without the
// rebuild in refreshRouterAdapters, /auto or /plan (or routed subagent
// dispatch) would silently regress back to the pre-/effort setting the
// next time either role adapter is used.
func TestCommitEffortChoice_RebuildsRouterAdapters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	seed := config.Default()
	seed.Providers = []config.Provider{{
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
	if err := config.Save(seed, ""); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	m := Model{
		parentCtx:    context.Background(),
		subagentTool: &agent.AgentTool{},
		cfg:          agent.LoopConfig{Adapter: &scriptedAdapter{}},
		routerMode:   config.RouterModeOff,
		transcript:   &strings.Builder{},
		routerPicker: &routerPickerState{},
		modelName:    "claude-opus-4-6",
		baseURL:      "https://api.anthropic.com",
		provider:     "anthropic",
	}
	m, _ = commitRouterChain(m, "fast", []string{"anthropic:claude-haiku-4-5"})
	m, _ = commitRouterChain(m, "smart", []string{"anthropic:claude-opus-4-6"})
	if m.router == nil {
		t.Fatalf("setup: commitRouterChain should build m.router")
	}
	staleImplementer := m.router.Implementer
	staleAdvisor := m.router.Advisor

	m, _ = commitEffortChoice(m, "high")

	if m.opts.ReasoningEffort != "high" {
		t.Errorf("m.opts.ReasoningEffort = %q, want high — router rebuilds read effort from opts", m.opts.ReasoningEffort)
	}
	if m.router.Implementer == staleImplementer {
		t.Error("commitEffortChoice should rebuild the router's implementer adapter with the new effort, not keep the stale one")
	}
	if m.router.Advisor == staleAdvisor {
		t.Error("commitEffortChoice should rebuild the router's advisor adapter with the new effort, not keep the stale one")
	}
}

func TestNormalizedEffort(t *testing.T) {
	cases := map[string]string{
		"high": "high", "LOW": "low", " medium ": "medium",
		"default": "", "off": "", "none": "", "": "",
	}
	for in, want := range cases {
		if got := normalizedEffort(in); got != want {
			t.Errorf("normalizedEffort(%q) = %q, want %q", in, got, want)
		}
	}
}
