package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/session"
)

func TestRouterPickerModels_EnumeratesProviderModels(t *testing.T) {
	models := routerPickerModels(routerTestConfig())

	want := map[string]bool{
		"anthropic:claude-opus-4-6":  false,
		"anthropic:claude-haiku-4-5": false,
	}
	seen := map[string]int{}
	for _, e := range models {
		seen[e.ref]++
		if e.ref != e.label {
			t.Errorf("label should equal ref: %q vs %q", e.label, e.ref)
		}
		if _, ok := want[e.ref]; ok {
			want[e.ref] = true
		}
	}
	for ref, found := range want {
		if !found {
			t.Errorf("enumeration missing %q", ref)
		}
	}
	for ref, n := range seen {
		if n > 1 {
			t.Errorf("ref %q enumerated %d times (should dedup)", ref, n)
		}
	}
}

func TestEnsureProviderModel(t *testing.T) {
	cfg := routerTestConfig()
	before := len(cfg.Providers[0].Models)

	ensureProviderModel(&cfg, "anthropic:claude-haiku-4-5") // already listed
	if len(cfg.Providers[0].Models) != before {
		t.Errorf("ensureProviderModel duplicated an existing model")
	}

	ensureProviderModel(&cfg, "anthropic:claude-sonnet-4-6") // not listed
	found := false
	for _, mm := range cfg.Providers[0].Models {
		if mm.Name == "claude-sonnet-4-6" {
			found = true
		}
	}
	if !found {
		t.Error("ensureProviderModel should append a missing model")
	}

	// Bare provider ref and unknown provider must be safe no-ops.
	ensureProviderModel(&cfg, "anthropic")
	ensureProviderModel(&cfg, "ghost:model")
}

// TestCommitRouter_AddsModelsThenTurnsOn exercises the whole picker
// persistence flow against a curated provider with an EMPTY
// providers.models: pick fast, pick smart, turn on. ensureProviderModel
// must add the chosen models so the turn-on (mode=auto) validates.
func TestCommitRouter_AddsModelsThenTurnsOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	seed := config.Default()
	seed.Providers = []config.Provider{{
		Name:         "anthropic",
		Kind:         "anthropic",
		BaseURL:      "https://api.anthropic.com",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-opus-4-6",
		// Only the default is listed; the fast model (haiku) is absent on
		// purpose, so ensureProviderModel must add it for turn-on to
		// validate.
		Models: []config.Model{{Name: "claude-opus-4-6", Tier: "expensive"}},
	}}
	if err := config.Save(seed, ""); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	m := Model{
		subagentTool: &agent.AgentTool{},
		cfg:          agent.LoopConfig{Adapter: &scriptedAdapter{}},
		routerMode:   config.RouterModeOff,
		transcript:   &strings.Builder{},
		routerPicker: &routerPickerState{},
	}

	m, _ = commitRouterChain(m, "fast", []string{"anthropic:claude-haiku-4-5"})
	m, _ = commitRouterChain(m, "smart", []string{"anthropic:claude-opus-4-6"})
	if m.router == nil {
		t.Fatalf("commitRouterChain should rebuild m.router; transcript=%q", m.transcript.String())
	}
	if chainPrimary(m.routerPicker.fastChain) != "anthropic:claude-haiku-4-5" {
		t.Errorf("picker working chain not synced: fast=%v", m.routerPicker.fastChain)
	}

	m, _ = commitRouterMode(m, config.RouterModeAuto)
	if m.routerMode != config.RouterModeAuto {
		t.Fatalf("turn-on failed; mode=%q transcript=%q", m.routerMode, m.transcript.String())
	}
	if !m.subagentTool.RouteAuto {
		t.Error("turn-on should wire RouteAuto on the subagent tool")
	}

	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Router.Mode != config.RouterModeAuto ||
		reloaded.Router.ImplementerModel != "anthropic:claude-haiku-4-5" ||
		reloaded.Router.AdvisorModel != "anthropic:claude-opus-4-6" {
		t.Errorf("router config not persisted: %+v", reloaded.Router)
	}
	if err := config.Validate(reloaded); err != nil {
		t.Errorf("persisted config should validate: %v", err)
	}
	added := false
	for _, mm := range reloaded.Providers[0].Models {
		if mm.Name == "claude-haiku-4-5" {
			added = true
		}
	}
	if !added {
		t.Error("ensureProviderModel should have persisted claude-haiku-4-5 into providers.models")
	}
}

func TestUpdateRouterPicker_MenuToModelListAndBack(t *testing.T) {
	m := Model{
		routerPickerOpen: true,
		routerPicker: &routerPickerState{
			cursor:      rowFastPrimary,
			models:      []routerModelEntry{{ref: "anthropic:claude-haiku-4-5", label: "anthropic:claude-haiku-4-5"}},
			visibleRows: 12,
		},
	}

	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter})
	if m.routerPicker.selecting != "fast" {
		t.Fatalf("Enter on Fast should open the sub-list, got selecting=%q", m.routerPicker.selecting)
	}
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEsc})
	if m.routerPicker.selecting != "" {
		t.Errorf("Esc in the sub-list should return to the menu, got selecting=%q", m.routerPicker.selecting)
	}
}

func TestUpdateRouterPicker_EscClosesMenu(t *testing.T) {
	m := newTestModel(t)
	m.openRouterPicker()
	if !m.routerPickerOpen {
		t.Fatal("openRouterPicker should set routerPickerOpen")
	}
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEsc})
	if m.routerPickerOpen {
		t.Error("Esc on the menu should close the picker")
	}
}

// TestRouterPicker_ToggleOnWithoutModels: enabling before models are set
// stays in the picker, flips the working mode to auto (pending), and
// prompts to pick models — it does not exit or persist auto.
func TestRouterPicker_ToggleOnWithoutModels(t *testing.T) {
	m := Model{
		routerPickerOpen: true,
		routerPicker:     &routerPickerState{cursor: 0, mode: config.RouterModeOff},
		transcript:       &strings.Builder{},
	}
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.routerPickerOpen {
		t.Error("toggling on without models should keep the picker open")
	}
	if m.routerPicker.mode != config.RouterModeAuto {
		t.Errorf("working mode should be auto (pending), got %q", m.routerPicker.mode)
	}
	if m.routerPicker.note == "" {
		t.Error("expected a note prompting to set models first")
	}
	if m.routerMode == config.RouterModeAuto {
		t.Error("routing must not actually be live until models are set")
	}
}

// TestRouterPicker_EnableThenPickModels drives the "enable, then specify
// models below" flow end-to-end: toggle On (pending), pick fast, pick
// smart — routing turns on as soon as the pair is complete, all without
// leaving the picker.
func TestRouterPicker_EnableThenPickModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	seed := config.Default()
	seed.Providers = []config.Provider{{
		Name:         "anthropic",
		Kind:         "anthropic",
		BaseURL:      "https://api.anthropic.com",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-opus-4-6",
		Models:       []config.Model{{Name: "claude-opus-4-6", Tier: "expensive"}},
	}}
	if err := config.Save(seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	models := []routerModelEntry{
		{ref: "anthropic:claude-haiku-4-5", label: "anthropic:claude-haiku-4-5"},
		{ref: "anthropic:claude-opus-4-6", label: "anthropic:claude-opus-4-6"},
	}
	m := Model{
		routerPickerOpen: true,
		subagentTool:     &agent.AgentTool{},
		cfg:              agent.LoopConfig{Adapter: &scriptedAdapter{}},
		routerMode:       config.RouterModeOff,
		transcript:       &strings.Builder{},
		routerPicker:     &routerPickerState{cursor: 0, mode: config.RouterModeOff, models: models, visibleRows: 12},
	}

	// Toggle On — pending (no models yet).
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter})
	if m.routerMode == config.RouterModeAuto {
		t.Fatal("should not be live before models are set")
	}

	// Pick Fast: cursor to the Fast model row, Enter (sub-list), select first model.
	m.routerPicker.cursor = rowFastPrimary
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter}) // open fast sub-list
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter}) // select models[0] = haiku
	if m.routerMode == config.RouterModeAuto {
		t.Fatal("still pending after only the fast model is set")
	}

	// Pick Smart: cursor to the Smart model row, open sub-list, move to opus, select.
	m.routerPicker.cursor = rowSmartPrimary
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter}) // open smart sub-list
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyDown})  // to models[1] = opus
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter}) // select

	if !m.routerPickerOpen {
		t.Error("picker should stay open throughout enable + model selection")
	}
	if m.routerMode != config.RouterModeAuto {
		t.Fatalf("routing should be live once both models are set; mode=%q transcript=%q",
			m.routerMode, m.transcript.String())
	}
	if !m.subagentTool.RouteAuto {
		t.Error("completing the pair should wire RouteAuto")
	}
	reloaded, _ := config.LoadDefault()
	if reloaded.Router.Mode != config.RouterModeAuto {
		t.Errorf("auto mode should be persisted, got %q", reloaded.Router.Mode)
	}
}

func TestSetRouterChain(t *testing.T) {
	// Multi-element chain → plural form; the singular is cleared.
	cfg := config.Config{}
	cfg.Router.SmartModel = "stale"
	setRouterChain(&cfg, "smart", []string{"primary", "backup"})
	if cfg.Router.AdvisorModel != "" {
		t.Errorf("legacy singular should be cleared for a chain, got %q", cfg.Router.AdvisorModel)
	}
	if len(cfg.Router.AdvisorModels) != 2 || cfg.Router.AdvisorModels[0] != "primary" || cfg.Router.AdvisorModels[1] != "backup" {
		t.Errorf("plural = %v, want [primary backup]", cfg.Router.AdvisorModels)
	}

	// Single-element chain → singular form; the plural is cleared.
	cfg2 := config.Config{}
	cfg2.Router.FastModels = []string{"stale1", "stale2"}
	setRouterChain(&cfg2, "fast", []string{"only"})
	if cfg2.Router.ImplementerModel != "only" {
		t.Errorf("singular = %q, want only", cfg2.Router.ImplementerModel)
	}
	if len(cfg2.Router.ImplementerModels) != 0 {
		t.Errorf("plural should be cleared, got %v", cfg2.Router.FastModels)
	}
}

func TestFallbackValue(t *testing.T) {
	if got := fallbackValue([]string{"a"}); got != "(none)" {
		t.Errorf("no fallback → %q, want (none)", got)
	}
	if got := fallbackValue(nil); got != "(none)" {
		t.Errorf("empty chain → %q, want (none)", got)
	}
	if got := fallbackValue([]string{"a", "b"}); got != "b" {
		t.Errorf("one fallback → %q, want b", got)
	}
	if got := fallbackValue([]string{"a", "b", "c", "d"}); got != "b (+2 more)" {
		t.Errorf("longer chain → %q, want 'b (+2 more)'", got)
	}
}

// TestRouterPicker_ShowsFallbackRow: opening the picker on a slot with a
// configured chain shows the fallback row populated (and the other slot's
// fallback row as "(none)").
func TestRouterPicker_ShowsFallbackRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	seed := config.Default()
	seed.Providers = []config.Provider{{
		Name:         "anthropic",
		Kind:         "anthropic",
		BaseURL:      "https://api.anthropic.com",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-opus-4-6",
		Models:       []config.Model{{Name: "claude-opus-4-6"}, {Name: "claude-haiku-4-5"}},
	}}
	seed.Router.Mode = config.RouterModeAuto
	seed.Router.FastModel = "anthropic:claude-haiku-4-5"
	seed.Router.SmartModels = []string{"anthropic:claude-opus-4-6", "anthropic:claude-haiku-4-5"}
	if err := config.Save(seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := Model{routerMode: config.RouterModeAuto}
	m.openRouterPicker()
	if len(m.routerPicker.smartChain) != 2 {
		t.Errorf("smartChain = %v, want 2 entries", m.routerPicker.smartChain)
	}
	if len(m.routerPicker.fastChain) != 1 {
		t.Errorf("fastChain = %v, want 1 entry", m.routerPicker.fastChain)
	}
	plain := stripANSI(renderRouterPicker(m.routerPicker))
	if !strings.Contains(plain, "Advisor fb") || !strings.Contains(plain, "anthropic:claude-haiku-4-5") {
		t.Errorf("expected the smart fallback row with its model: %q", plain)
	}
	if strings.Contains(plain, "Fast fallback") {
		t.Errorf("the fast slot is summarization-only — it should have no fallback row: %q", plain)
	}
}

// TestRouterPicker_AddAndClearSmartFallback: pick a smart fallback (chain
// becomes 2, persisted as smart_models), then clear it with 'd' (back to the
// singular smart_model).
func TestRouterPicker_AddAndClearSmartFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	seed := config.Default()
	seed.Providers = []config.Provider{{
		Name:         "anthropic",
		Kind:         "anthropic",
		BaseURL:      "https://api.anthropic.com",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-opus-4-6",
		Models:       []config.Model{{Name: "claude-opus-4-6"}, {Name: "claude-haiku-4-5"}},
	}}
	seed.Router.Mode = config.RouterModeAuto
	seed.Router.FastModel = "anthropic:claude-haiku-4-5"
	seed.Router.SmartModel = "anthropic:claude-opus-4-6"
	if err := config.Save(seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	models := []routerModelEntry{
		{ref: "anthropic:claude-haiku-4-5", label: "anthropic:claude-haiku-4-5"},
		{ref: "anthropic:claude-opus-4-6", label: "anthropic:claude-opus-4-6"},
	}
	m := Model{
		routerPickerOpen: true,
		subagentTool:     &agent.AgentTool{},
		cfg:              agent.LoopConfig{Adapter: &scriptedAdapter{}},
		routerMode:       config.RouterModeAuto,
		transcript:       &strings.Builder{},
		routerPicker: &routerPickerState{
			mode:        config.RouterModeAuto,
			fastChain:   []string{"anthropic:claude-haiku-4-5"},
			smartChain:  []string{"anthropic:claude-opus-4-6"},
			models:      models,
			visibleRows: 12,
		},
	}

	// Add a smart fallback: cursor to the Smart fallback row, open list, pick haiku.
	m.routerPicker.cursor = rowSmartFallback
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter}) // open smart-fb list
	if m.routerPicker.selecting != "smart-fb" {
		t.Fatalf("expected the smart-fb sub-list, got %q", m.routerPicker.selecting)
	}
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyEnter}) // select first (haiku) as the fallback

	if len(m.routerPicker.smartChain) != 2 || m.routerPicker.smartChain[1] != "anthropic:claude-haiku-4-5" {
		t.Fatalf("smart chain should have the fallback appended: %v", m.routerPicker.smartChain)
	}
	reloaded, _ := config.LoadDefault()
	if len(reloaded.Router.AdvisorModels) != 2 || reloaded.Router.AdvisorModels[1] != "anthropic:claude-haiku-4-5" {
		t.Errorf("advisor_models not persisted as a chain: %v (single=%q)", reloaded.Router.AdvisorModels, reloaded.Router.AdvisorModel)
	}
	if reloaded.Router.AdvisorModel != "" {
		t.Errorf("singular advisor_model should be cleared when a chain is set, got %q", reloaded.Router.AdvisorModel)
	}

	// Clear it with 'd' on the Smart fallback row.
	m.routerPicker.cursor = rowSmartFallback
	m, _ = m.updateRouterPicker(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if len(m.routerPicker.smartChain) != 1 {
		t.Errorf("clearing the fallback should leave just the primary: %v", m.routerPicker.smartChain)
	}
	reloaded2, _ := config.LoadDefault()
	if reloaded2.Router.AdvisorModel != "anthropic:claude-opus-4-6" || len(reloaded2.Router.AdvisorModels) != 0 {
		t.Errorf("after clear, expected the singular advisor_model: single=%q plural=%v",
			reloaded2.Router.AdvisorModel, reloaded2.Router.AdvisorModels)
	}
}

func TestRouterActiveSwitchTarget(t *testing.T) {
	// Smart changed this session and differs from the active model → switch.
	p := &routerPickerState{smartChain: []string{"anthropic:claude-opus-4-6"}, initialSmart: ""}
	if got := routerActiveSwitchTarget(p, "claude-haiku-4-5"); got != "anthropic:claude-opus-4-6" {
		t.Errorf("changed smart should switch the active model, got %q", got)
	}
	// Unchanged this session (initial == current) → no switch.
	p2 := &routerPickerState{smartChain: []string{"anthropic:claude-opus-4-6"}, initialSmart: "anthropic:claude-opus-4-6"}
	if got := routerActiveSwitchTarget(p2, "claude-haiku-4-5"); got != "" {
		t.Errorf("unchanged smart should not switch, got %q", got)
	}
	// Changed but the smart model is already the active one → no switch.
	p3 := &routerPickerState{smartChain: []string{"anthropic:claude-opus-4-6"}, initialSmart: ""}
	if got := routerActiveSwitchTarget(p3, "claude-opus-4-6"); got != "" {
		t.Errorf("smart already active should not switch, got %q", got)
	}
	// No smart configured → no switch.
	if got := routerActiveSwitchTarget(&routerPickerState{}, "x"); got != "" {
		t.Errorf("no smart should not switch, got %q", got)
	}
}

// TestSwitchActiveModelToRef: configuring a new smart model switches the
// active conversation model to it.
func TestSwitchActiveModelToRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	seed := config.Default()
	seed.Providers = []config.Provider{{
		Name:         "anthropic",
		Kind:         "anthropic",
		BaseURL:      "https://api.anthropic.com",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-opus-4-6",
		Models:       []config.Model{{Name: "claude-opus-4-6"}, {Name: "claude-haiku-4-5"}},
	}}
	if err := config.Save(seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sess, err := session.New("claude-haiku-4-5", "/cwd")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	staleAdapter := &scriptedAdapter{}
	m := Model{
		parentCtx:    context.Background(),
		modelName:    "claude-haiku-4-5",
		baseURL:      "https://api.anthropic.com",
		provider:     "anthropic",
		cfg:          agent.LoopConfig{Adapter: staleAdapter},
		subagentTool: &agent.AgentTool{Adapter: staleAdapter},
		sess:         sess,
		transcript:   &strings.Builder{},
	}

	m, _ = m.switchActiveModelToRef("anthropic:claude-opus-4-6")
	if m.modelName != "claude-opus-4-6" {
		t.Errorf("active model = %q, want claude-opus-4-6", m.modelName)
	}
	if m.sess.Model != "claude-opus-4-6" {
		t.Errorf("session model = %q, want claude-opus-4-6", m.sess.Model)
	}
	// The shared AgentTool must follow the conversation adapter — every
	// other model/provider switch path syncs it; without this, subagents
	// spawned after a /router close-switch run on the stale adapter.
	if m.subagentTool.Adapter == agent.Streamer(staleAdapter) {
		t.Error("switchActiveModelToRef must sync subagentTool.Adapter off the stale adapter")
	}
	if m.subagentTool.Adapter != agent.Streamer(m.cfg.Adapter) {
		t.Error("subagentTool.Adapter and cfg.Adapter must point at the same rebuilt adapter")
	}
}

// reasoningEffort is a session-global field switchActiveModelToRef never
// clears or re-validates. Configuring a new smart model in /router that
// happens to be a non-reasoning model must re-flag an already-set effort
// level as a no-op — the same check every other model/provider switch
// path runs — otherwise a level that applied cleanly on the old model
// silently stops applying with nothing telling the user.
func TestSwitchActiveModelToRef_WarnsWhenEffortBecomesNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")
	seed := config.Default()
	seed.Providers = []config.Provider{
		{
			Name:         "anthropic",
			Kind:         "anthropic",
			BaseURL:      "https://api.anthropic.com",
			APIKeyEnv:    "ANTHROPIC_API_KEY",
			DefaultModel: "claude-opus-4-6",
			Models:       []config.Model{{Name: "claude-opus-4-6"}},
		},
		{
			Name:         "openai",
			Kind:         "openai",
			BaseURL:      "https://api.openai.com/v1",
			APIKeyEnv:    "OPENAI_API_KEY",
			DefaultModel: "gpt-4o",
			Models:       []config.Model{{Name: "gpt-4o"}},
		},
	}
	if err := config.Save(seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sess, err := session.New("claude-opus-4-6", "/cwd")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	m := Model{
		parentCtx:       context.Background(),
		modelName:       "claude-opus-4-6",
		baseURL:         "https://api.anthropic.com",
		provider:        "anthropic",
		reasoningEffort: "high",
		cfg:             agent.LoopConfig{Adapter: &scriptedAdapter{}},
		subagentTool:    &agent.AgentTool{Adapter: &scriptedAdapter{}},
		sess:            sess,
		transcript:      &strings.Builder{},
	}

	m, _ = m.switchActiveModelToRef("anthropic:claude-opus-4-6")
	if strings.Contains(m.transcript.String(), "no-op on this model") {
		t.Fatalf("effort should apply cleanly on claude-opus-4-6; transcript:\n%s", m.transcript.String())
	}

	pre := m.transcript.String()
	m, _ = m.switchActiveModelToRef("openai:gpt-4o")
	post := m.transcript.String()[len(pre):]
	if !strings.Contains(post, "no-op on this model") {
		t.Errorf("switching to gpt-4o (non-reasoning) should re-surface the no-op warning; new transcript:\n%s", post)
	}
}

func TestAnyOverlayOpen_RouterPicker(t *testing.T) {
	m := Model{}
	if m.anyOverlayOpen() {
		t.Fatal("no overlay should be open on a zero Model")
	}
	m.routerPickerOpen = true
	if !m.anyOverlayOpen() {
		t.Error("routerPickerOpen should count as an open overlay")
	}
}
