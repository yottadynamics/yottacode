package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
)

// stubPickerList returns a fixed list of models so the picker tests
// don't hit the network. Returned in a closure so each test can
// declare what its provider should "have."
func stubPickerList(models []catalog.Model, err error) pickerListFn {
	return func(ctx context.Context, p config.Provider, apiKey string) ([]catalog.Model, error) {
		return models, err
	}
}

// /model with no args opens the picker, attaches the picker state,
// and returns a cmd that performs the async fetch.
func TestModelPicker_OpensViaSlashCommand(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	if !m.modelPickerOpen {
		t.Fatalf("/model should open the picker")
	}
	if m.modelPicker == nil {
		t.Fatalf("picker state should be attached")
	}
}

// Esc closes the picker without persisting.
func TestModelPicker_EscClosesWithoutChange(t *testing.T) {
	m := newTestModel(t)
	originalModel := m.modelName
	m, _ = typeAndEnter(t, m, "/model")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.modelPickerOpen {
		t.Errorf("Esc should close the picker")
	}
	if m.modelPicker != nil {
		t.Errorf("picker state should be detached after Esc")
	}
	if m.modelName != originalModel {
		t.Errorf("Esc should not change modelName; got %q want %q", m.modelName, originalModel)
	}
}

// Up/Down move the cursor through the loaded entries. We bypass the
// async fetch by directly delivering a modelPickerLoadedMsg.
func TestModelPicker_ArrowsNavigateLoadedList(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	loaded := modelPickerLoadedMsg{
		entries: []catalog.Model{
			{ID: "a", Provider: "session"}, {ID: "b", Provider: "session"}, {ID: "c", Provider: "session"},
		},
	}
	m, _ = applyMsg(m, loaded)
	if m.modelPicker.cursor != 0 {
		t.Fatalf("cursor should start at 0, got %d", m.modelPicker.cursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.modelPicker.cursor != 1 {
		t.Errorf("Down should advance cursor; got %d", m.modelPicker.cursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown}) // bounded at last
	if m.modelPicker.cursor != 2 {
		t.Errorf("Down should clamp at last entry; got %d", m.modelPicker.cursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.modelPicker.cursor != 1 {
		t.Errorf("Up should retreat cursor; got %d", m.modelPicker.cursor)
	}
}

// On load, the cursor snaps to whichever entry matches the running
// active model — so the user can hit Enter to confirm without
// arrowing.
func TestModelPicker_LoadDefaultsCursorToActiveModel(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "b"
	m, _ = typeAndEnter(t, m, "/model")
	loaded := modelPickerLoadedMsg{
		entries: []catalog.Model{
			{ID: "a", Provider: "session"}, {ID: "b", Provider: "session"}, {ID: "c", Provider: "session"},
		},
	}
	m, _ = applyMsg(m, loaded)
	if m.modelPicker.cursor != 1 {
		t.Errorf("cursor should land on active model 'b' (index 1); got %d", m.modelPicker.cursor)
	}
}

// When browsing across providers, the highlighted/current row should be
// scoped to the provider being displayed. This is especially visible for
// Vertex: a session may be running vertex-gemini while the user cycles to
// vertex-claude to pick a Claude model, and the Claude tab should land on
// its own default_model instead of searching for the Gemini active model.
func TestModelPicker_CycledProviderUsesOwnDefaultModel(t *testing.T) {
	m := newTestModel(t)
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[active]
provider      = "vertex-gemini"
default_model = "google/gemini-2.5-pro"

[[providers]]
name = "vertex-gemini"
kind = "vertex"
base_url = "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi"
default_model = "google/gemini-2.5-pro"

[[providers]]
name = "vertex-claude"
kind = "vertex-anthropic"
base_url = "https://aiplatform.googleapis.com/v1/projects/p/locations/global"
default_model = "claude-sonnet-4-5@20250929"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	gemini := *cfg.FindProvider("vertex-gemini")
	m.modelName = "google/gemini-2.5-pro"
	m.pickerList = func(ctx context.Context, p config.Provider, apiKey string) ([]catalog.Model, error) {
		switch p.Name {
		case "vertex-gemini":
			return []catalog.Model{
				{ID: "google/gemini-2.5-flash", Provider: p.Kind},
				{ID: "google/gemini-2.5-pro", Provider: p.Kind},
			}, nil
		case "vertex-claude":
			return []catalog.Model{
				{ID: "claude-opus-4-8@default", Provider: p.Kind},
				{ID: "claude-sonnet-4-5@20250929", Provider: p.Kind},
			}, nil
		default:
			return nil, fmt.Errorf("unexpected provider %q", p.Name)
		}
	}

	cmd := m.openModelPicker(gemini)
	m, _ = applyMsg(m, cmd())
	if got := m.modelPicker.entries[m.modelPicker.cursor].ID; got != "google/gemini-2.5-pro" {
		t.Fatalf("active provider cursor = %q, want Gemini active model", got)
	}

	m, fetchCmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyRight})
	if fetchCmd == nil {
		t.Fatal("provider cycle returned no fetch command")
	}
	m, _ = applyMsg(m, fetchCmd())
	if got := m.modelPicker.provider.Name; got != "vertex-claude" {
		t.Fatalf("provider = %q, want vertex-claude", got)
	}
	if got := m.modelPicker.entries[m.modelPicker.cursor].ID; got != "claude-sonnet-4-5@20250929" {
		t.Fatalf("cycled provider cursor = %q, want the Claude provider default_model", got)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "✓ —") {
		t.Errorf("view should mark the Claude provider default row; got:\n%s", view)
	}
}

// picker banner. The banner is just "Model" — the previous "Model —
// <provider>" form was redundant once the provider tab strip below
// the title carries the same information visually.
func TestModelPicker_ViewIncludesBanner(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	got := m.View()
	if !strings.Contains(stripANSI(got), "Model") {
		t.Errorf("View should include 'Model' banner; got:\n%s", got)
	}
}

// The picker is the proactive surface for the cache-reset fact — shown
// unconditionally in the header before the user commits to a switch,
// unlike the reactive scrollback warning (warnIfCacheReset) which only
// fires after a real mid-session switch. Both surfaces matter: the
// header primes users browsing the list, the scrollback line still
// covers the /model <name> power-user shortcut, which never renders
// this view at all.
func TestModelPicker_ViewIncludesCacheResetNote(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	got := stripANSI(m.View())
	if !strings.Contains(got, "resets the provider's prompt cache") {
		t.Errorf("picker header should proactively note the cache-reset fact; got:\n%s", got)
	}
}

// Live fetch error doesn't panic the picker — we surface it inline
// and the caller can still see whatever catalog entries we have.
// Tested by providing an empty catalog and an error fetcher: picker
// renders the error string in muted text alongside the loading-done
// state.
func TestModelPicker_LoadErrorIsVisibleInView(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	m, _ = applyMsg(m, modelPickerLoadedMsg{
		entries: nil,
		err:     errors.New("network down"),
	})
	got := stripANSI(m.View())
	if !strings.Contains(got, "network down") {
		t.Errorf("View should surface fetch error; got:\n%s", got)
	}
}

// Selecting a model from a provider that exists in [[providers]]
// but isn't currently active.provider must auto-adopt that
// provider as active. Before this fix, picking a model from the
// listed provider failed with "[model] no active provider; run
// `provider use <name>` first" — confusing because the user IS
// picking from a provider they have configured.
func TestModelPicker_SelectingModelAdoptsProviderAsActive(t *testing.T) {
	m := newTestModel(t)
	// Seed config with an ollama provider but no [active] block.
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "qwen3.5:9b"
  [[providers.models]]
  name = "qwen3.5:9b"
  tier = "cheap"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Open picker for the ollama provider directly so the test
	// doesn't depend on /model's provider-resolution heuristic.
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	prov := *cfg.FindProvider("ollama")
	m.pickerList = stubPickerList(
		[]catalog.Model{{ID: "qwen3.5:9b", Provider: "session"}}, nil,
	)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	// Press Enter on the lone model. Without the auto-adopt fix
	// this errors with "no active provider"; with the fix the
	// picker closes and Active.Provider is set to "ollama".
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelPickerOpen {
		t.Errorf("Enter should close picker after committing; got open")
	}
	saved, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if saved.Active.Provider != "ollama" {
		t.Errorf("Active.Provider should auto-adopt to ollama; got %q", saved.Active.Provider)
	}
	if saved.Active.DefaultModel != "qwen3.5:9b" {
		t.Errorf("Active.DefaultModel = %q, want qwen3.5:9b", saved.Active.DefaultModel)
	}
}

// TestModelPicker_WarnsOnMidSessionCacheReset mirrors
// TestModelShortcut_WarnsOnMidSessionCacheReset for the picker's Enter
// commit path (commitModelChoice), so the warning isn't only wired
// into the /model <name> shortcut. newTestModel's session starts on
// "test-model"; picking a different model here is a real switch.
func TestModelPicker_WarnsOnMidSessionCacheReset(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello"},
	)
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[[providers]]
name = "picker-test"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "other-model"
  [[providers.models]]
  name = "other-model"
  tier = "cheap"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	prov := *cfg.FindProvider("picker-test")
	m.pickerList = stubPickerList(
		[]catalog.Model{{ID: "other-model", Provider: "session"}}, nil,
	)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	pre := m.transcript.String()
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	post := m.transcript.String()[len(pre):]
	if !strings.Contains(post, "prompt cache resets") {
		t.Errorf("picker-driven mid-session model switch should warn about the cache reset; new transcript:\n%s", post)
	}
}

// Long lists must stay within the visibleRows window. The cursor
// stays in the visible window; "▼ N more below" appears below the
// visible slice. PgDn jumps a page; Home/End reach the boundaries.
func TestModelPicker_ScrollWindowKeepsCursorVisible(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	// Pretend the picker has 50 entries and only 8 visible rows.
	entries := make([]catalog.Model, 50)
	for i := range entries {
		entries[i] = catalog.Model{ID: fmt.Sprintf("m-%02d", i), Provider: "session"}
	}
	m, _ = applyMsg(m, modelPickerLoadedMsg{entries: entries})
	m.modelPicker.visibleRows = 8 // override whatever the terminal heuristic chose

	// Down past the bottom of the window scrolls.
	for i := 0; i < 10; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.modelPicker.cursor != 10 {
		t.Errorf("cursor should be at 10; got %d", m.modelPicker.cursor)
	}
	if m.modelPicker.windowTop == 0 {
		t.Errorf("windowTop should have advanced past 0; got %d", m.modelPicker.windowTop)
	}
	if cursor, top, vr := m.modelPicker.cursor, m.modelPicker.windowTop, m.modelPicker.visibleRows; cursor < top || cursor >= top+vr {
		t.Errorf("cursor %d out of visible window [%d, %d)", cursor, top, top+vr)
	}

	// PgDn jumps a window.
	before := m.modelPicker.cursor
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.modelPicker.cursor != before+m.modelPicker.visibleRows {
		t.Errorf("PgDn should advance by visibleRows (%d); cursor went %d → %d",
			m.modelPicker.visibleRows, before, m.modelPicker.cursor)
	}

	// End jumps to the last entry.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.modelPicker.cursor != len(entries)-1 {
		t.Errorf("End should land on last entry; got %d", m.modelPicker.cursor)
	}

	// Home jumps back to 0 and resets the window.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyHome})
	if m.modelPicker.cursor != 0 || m.modelPicker.windowTop != 0 {
		t.Errorf("Home should reset cursor + windowTop; got cursor=%d windowTop=%d",
			m.modelPicker.cursor, m.modelPicker.windowTop)
	}
}

// View() honors the scroll window — it renders the "▲/▼ N more"
// hint when content sits off-screen, and it does NOT include the
// names of entries past the bottom of the window.
func TestModelPicker_ViewHidesOffWindowEntriesAndShowsHints(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	entries := make([]catalog.Model, 30)
	for i := range entries {
		entries[i] = catalog.Model{ID: fmt.Sprintf("m-%02d", i), Provider: "session"}
	}
	m, _ = applyMsg(m, modelPickerLoadedMsg{entries: entries})
	m.modelPicker.visibleRows = 5
	m.modelPicker.windowTop = 0
	m.modelPicker.cursor = 0

	got := stripANSI(m.View())
	// First five visible.
	for i := 0; i < 5; i++ {
		want := fmt.Sprintf("m-%02d", i)
		if !strings.Contains(got, want) {
			t.Errorf("View should include %q (in-window); didn't", want)
		}
	}
	// Sixth and beyond hidden.
	if strings.Contains(got, "m-05") {
		t.Errorf("View should NOT include m-05 (off-window); did")
	}
	if !strings.Contains(got, "more below") {
		t.Errorf("View should show 'more below' hint when content is off-screen")
	}
}

// ←/→ cycle through configured providers, each switch resets the
// cursor and emits a fresh fetch cmd.
func TestModelPicker_LeftRightCyclesProviders(t *testing.T) {
	m := newTestModel(t)
	// Seed two providers.
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[active]
provider      = "anthropic"
default_model = "claude-sonnet-4-6"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "llama3.1:8b"
  [[providers.models]]
  name = "llama3.1:8b"
  tier = "cheap"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	prov := *cfg.FindProvider("anthropic")
	m.pickerList = stubPickerList(
		[]catalog.Model{{ID: "from-fake", Provider: "session"}}, nil,
	)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	if m.modelPicker.providerIdx != 0 {
		t.Fatalf("setup: expected providerIdx=0 (anthropic); got %d", m.modelPicker.providerIdx)
	}
	if len(m.modelPicker.allProviders) != 2 {
		t.Fatalf("expected 2 providers in picker; got %d", len(m.modelPicker.allProviders))
	}
	// Right cycles to ollama.
	m, fetchCmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.modelPicker.provider.Name != "ollama" {
		t.Errorf("Right should cycle to ollama; provider = %q", m.modelPicker.provider.Name)
	}
	if fetchCmd == nil {
		t.Errorf("Right should return a fetch cmd for the new provider")
	}
	// Left cycles back to anthropic.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.modelPicker.provider.Name != "anthropic" {
		t.Errorf("Left should cycle back to anthropic; provider = %q", m.modelPicker.provider.Name)
	}
}

// View renders the provider tab strip when 2+ providers are
// configured and hides it otherwise. The strip carries both the
// switch affordance (←/→) and the visual cue (active provider in
// brackets) so users see the cycling option instantly without
// reading a prose hint.
func TestModelPicker_ViewProviderCycleHint(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	// Inject two providers into the picker state directly so we
	// don't need a full config setup.
	m.modelPicker.allProviders = []config.Provider{
		{Name: "a", Kind: "anthropic", BaseURL: "x"},
		{Name: "b", Kind: "openai", BaseURL: "y"},
	}
	got := stripANSI(m.View())
	if !strings.Contains(got, "[ a ]") {
		t.Errorf("View should bracket the active provider; got:\n%s", got)
	}
	if !strings.Contains(got, "←/→ switch") {
		t.Errorf("View should show ←/→ switch hint when >=2 providers; got:\n%s", got)
	}
	// Reduce to one provider — strip should disappear.
	m.modelPicker.allProviders = m.modelPicker.allProviders[:1]
	got = stripANSI(m.View())
	if strings.Contains(got, "[ a ]") {
		t.Errorf("View should not bracket-tab a single-provider open; got:\n%s", got)
	}
	if strings.Contains(got, "←/→ switch") {
		t.Errorf("View should hide ←/→ switch hint with 1 provider; got:\n%s", got)
	}
}

// Per-model-key providers (NVIDIA NIM) grey out models that aren't
// the default_model of any configured profile. Enter on a greyed
// row must be a no-op — the picker stays open and a hint lands in
// the transcript pointing at /provider add. Without this gate the
// adapter would commit, dispatch to NVIDIA, and the next chat
// call would 404 because the existing key isn't authorized for
// the chosen model.
func TestModelPicker_PerModelKeyProviderRejectsUnconfiguredModel(t *testing.T) {
	m := newTestModel(t)
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[active]
provider      = "nvidia-nim"
default_model = "nvidia/nemotron-3-super-120b-a12b"

[[providers]]
name = "nvidia-nim"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/nemotron-3-super-120b-a12b"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	prov := *cfg.FindProvider("nvidia-nim")
	// Live fetch returns three models. Only the configured one is
	// selectable; the other two should be greyed.
	m.pickerList = stubPickerList(
		[]catalog.Model{
			{ID: "nvidia/nemotron-3-super-120b-a12b", Provider: "session"},
			{ID: "meta/llama-3.1-405b-instruct", Provider: "session"},
			{ID: "mistralai/mixtral-8x22b-instruct", Provider: "session"},
		},
		nil,
	)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())

	// Cursor lands on the active (configured) model — Enter
	// commits cleanly.
	if got := m.modelPicker.entries[m.modelPicker.cursor].ID; got != "nvidia/nemotron-3-super-120b-a12b" {
		t.Fatalf("cursor should default to active model; got %q", got)
	}

	// Move to the unconfigured llama model and Enter.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.modelPicker.entries[m.modelPicker.cursor].ID; got != "meta/llama-3.1-405b-instruct" {
		t.Fatalf("cursor should be on llama; got %q", got)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.modelPickerOpen {
		t.Errorf("Enter on unconfigured per-model-key row should NOT close picker")
	}
	got := m.transcript.String()
	if !strings.Contains(got, "no API key configured") {
		t.Errorf("expected hint in transcript; got:\n%s", got)
	}
	if !strings.Contains(got, "/provider add") {
		t.Errorf("hint should point at /provider add; got:\n%s", got)
	}
}

// View's per-model-key footnote appears when the picker is on a
// per-model-key provider, and is hidden for cloud (Anthropic /
// OpenAI) and free-form non-NVIDIA (Ollama) profiles.
func TestModelPicker_PerModelKeyHintAppearsForNvidiaOnly(t *testing.T) {
	m := newTestModel(t)
	m.pickerList = stubPickerList(
		[]catalog.Model{{ID: "x", Provider: "session"}}, nil,
	)

	// 1) NVIDIA — hint should appear.
	nvidia := config.Provider{
		Name:    "nvidia-nim",
		Kind:    "openai-compatible",
		BaseURL: "https://integrate.api.nvidia.com/v1",
	}
	cmd := m.openModelPicker(nvidia)
	m, _ = applyMsg(m, cmd())
	if !strings.Contains(stripANSI(m.View()), "per-model API keys") {
		t.Errorf("NVIDIA picker should show the per-model-key hint")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})

	// 2) Anthropic — no hint.
	anthropic := config.Provider{
		Name:    "anthropic",
		Kind:    "anthropic",
		BaseURL: "https://api.anthropic.com",
	}
	cmd = m.openModelPicker(anthropic)
	m, _ = applyMsg(m, cmd())
	if strings.Contains(stripANSI(m.View()), "per-model API keys") {
		t.Errorf("Anthropic picker should NOT show per-model-key hint")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})

	// 3) Ollama — no hint either (free-form, but local).
	ollama := config.Provider{
		Name:    "ollama",
		Kind:    "ollama",
		BaseURL: "http://localhost:11434/v1",
	}
	cmd = m.openModelPicker(ollama)
	m, _ = applyMsg(m, cmd())
	if strings.Contains(stripANSI(m.View()), "per-model API keys") {
		t.Errorf("Ollama picker should NOT show per-model-key hint")
	}
}

// Multiple NVIDIA profiles widen the "configured" set so users
// who've added several nvidia-X providers (one per model + key) see
// all of them as selectable.
func TestModelPicker_PerModelKeyMultipleProfilesEnableAllConfigured(t *testing.T) {
	m := newTestModel(t)
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[[providers]]
name = "nvidia-nemotron"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_NEMOTRON_KEY"
default_model = "nvidia/nemotron-3-super-120b-a12b"

[[providers]]
name = "nvidia-llama"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_LLAMA_KEY"
default_model = "meta/llama-3.1-405b-instruct"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	prov := *cfg.FindProvider("nvidia-nemotron")
	m.pickerList = stubPickerList(
		[]catalog.Model{
			{ID: "nvidia/nemotron-3-super-120b-a12b", Provider: "session"},
			{ID: "meta/llama-3.1-405b-instruct", Provider: "session"},
			{ID: "mistralai/mixtral-8x22b-instruct", Provider: "session"},
		},
		nil,
	)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	got := m.modelPicker.configuredModels()
	if _, ok := got["nvidia/nemotron-3-super-120b-a12b"]; !ok {
		t.Errorf("nemotron should be configured (matches nvidia-nemotron profile)")
	}
	if _, ok := got["meta/llama-3.1-405b-instruct"]; !ok {
		t.Errorf("llama should be configured (matches nvidia-llama profile via different key)")
	}
	if _, ok := got["mistralai/mixtral-8x22b-instruct"]; ok {
		t.Errorf("mixtral has no profile; should NOT be in configured set")
	}
}

// The new menu rendering uses a `❯` cursor prefix and a `✓`
// checkmark on the current-default row (Claude Code "Select model"
// pattern). Verify both glyphs appear in the rendered View.
func TestModelPicker_RendersClaudeCodeStyleCursorAndCheck(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "claude-sonnet-4-6"
	m.pickerList = stubPickerList(
		[]catalog.Model{
			{ID: "claude-sonnet-4-6", Provider: "session"},
			{ID: "claude-haiku-4-5", Provider: "session"},
		},
		nil,
	)
	prov := config.Provider{Name: "anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com"}
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	got := stripANSI(m.View())
	if !strings.Contains(got, "❯") {
		t.Errorf("View should include ❯ cursor; got:\n%s", got)
	}
	if !strings.Contains(got, "✓") {
		t.Errorf("View should include ✓ on current-default row; got:\n%s", got)
	}
}

// Long model names (NVIDIA-style) get truncated with `…` so the
// description column always starts at the same offset. The
// alignment fix.
func TestModelPicker_LongNamesTruncateForAlignment(t *testing.T) {
	m := newTestModel(t)
	long := "nvidia/llama-3.1-nemotron-safety-guard-8b-v3-extra-suffix"
	m.pickerList = stubPickerList(
		[]catalog.Model{{ID: long, Provider: "session"}}, nil,
	)
	prov := config.Provider{Name: "nvidia", Kind: "openai-compatible", BaseURL: "https://integrate.api.nvidia.com/v1"}
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	got := stripANSI(m.View())
	// The full long name shouldn't appear verbatim — it should be
	// truncated with a trailing ellipsis.
	if strings.Contains(got, long) {
		t.Errorf("long name should be truncated with …; rendered raw:\n%s", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("View should contain `…` truncation glyph for long name; got:\n%s", got)
	}
}

// Picking a model from a different provider than the running
// session must align m.baseURL / m.apiKey with that provider's
// values. Without this, the adapter rebuild fires the next chat
// call at the SESSION's (now-stale) URL, which surfaces as
//
//	POST "http://localhost:11434/v1/chat/completions": 404
//	model 'nvidia/nemotron-...' not found
//
// when the session opened on Ollama and the user cycled the
// picker to NVIDIA before picking.
func TestModelPicker_SelectingFromDifferentProviderUpdatesBaseURL(t *testing.T) {
	m := newTestModel(t)
	// Session starts on Ollama.
	m.baseURL = "http://localhost:11434/v1"
	m.apiKey = ""

	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[active]
provider      = "ollama"
default_model = "qwen3.5:9b"

[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "qwen3.5:9b"
  [[providers.models]]
  name = "qwen3.5:9b"
  tier = "cheap"

[[providers]]
name = "nvidia-nim"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/nemotron"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("NVIDIA_API_KEY", "sk-nvidia-test")

	// Open picker for NVIDIA directly (simulating ←/→ having
	// landed on it).
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	prov := *cfg.FindProvider("nvidia-nim")
	m.pickerList = stubPickerList(
		[]catalog.Model{{ID: "nvidia/nemotron", Provider: "session"}}, nil,
	)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.modelPickerOpen {
		t.Fatalf("Enter should close picker")
	}
	if m.baseURL != "https://integrate.api.nvidia.com/v1" {
		t.Errorf("baseURL should switch to NVIDIA's; got %q", m.baseURL)
	}
	if m.apiKey != "sk-nvidia-test" {
		t.Errorf("apiKey should adopt NVIDIA's env value; got %q", m.apiKey)
	}
	if m.modelName != "nvidia/nemotron" {
		t.Errorf("modelName = %q, want nvidia/nemotron", m.modelName)
	}
}

// After /provider use X (which adopts X's default_model) followed by a
// model-picker pick of Y on the same provider, the live adapter must
// carry Y — not X's default_model from config.toml. Regression guard:
// without the adapter rebuild in commitModelChoice (model_picker.go:375),
// the next chat turn would silently send the provider's default while
// the TUI status bar shows the picked model — the worst kind of UI/state
// drift. Asserts on the model baked into m.cfg.Adapter (via Model()
// exposed by chatAdapter) rather than just m.modelName, since the bug
// class lives in the gap between those two.
func TestModelPicker_PickAfterProviderUseRebuildsAdapterWithPickedModel(t *testing.T) {
	type modelAccessor interface {
		Model() string
	}

	m := newTestModel(t)
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `
[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "qwen3.5:9b"
  [[providers.models]]
  name = "qwen3.5:9b"
  tier = "cheap"
  [[providers.models]]
  name = "llama3:8b"
  tier = "cheap"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Step 1: /provider use ollama. The slash command rebuilds the
	// adapter with the provider's default_model (qwen3.5:9b).
	m, _ = typeAndEnter(t, m, "/provider use ollama")
	if m.modelName != "qwen3.5:9b" {
		t.Fatalf("after /provider use, modelName = %q, want qwen3.5:9b (provider default)", m.modelName)
	}
	got, ok := m.cfg.Adapter.(modelAccessor)
	if !ok {
		t.Fatalf("after /provider use, adapter %T does not expose Model()", m.cfg.Adapter)
	}
	if got.Model() != "qwen3.5:9b" {
		t.Fatalf("after /provider use, adapter.Model() = %q, want qwen3.5:9b", got.Model())
	}

	// Step 2: open the picker for the running ollama provider and pick
	// llama3:8b — a model that is NOT the provider's default. This is
	// the case the regression is about: the user's TUI selection must
	// override the config-default model on the live adapter.
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	prov := *cfg.FindProvider("ollama")
	m.pickerList = stubPickerList(
		[]catalog.Model{
			{ID: "qwen3.5:9b", Provider: "session"},
			{ID: "llama3:8b", Provider: "session"},
		}, nil,
	)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	// The picker auto-cursors to the active model (qwen3.5:9b at
	// index 0). Move down to llama3:8b (index 1) and confirm.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.modelPickerOpen {
		t.Fatalf("Enter should close the picker after committing")
	}
	if m.modelName != "llama3:8b" {
		t.Errorf("after pick, modelName = %q, want llama3:8b", m.modelName)
	}
	got, ok = m.cfg.Adapter.(modelAccessor)
	if !ok {
		t.Fatalf("after pick, adapter %T does not expose Model()", m.cfg.Adapter)
	}
	if got.Model() != "llama3:8b" {
		t.Errorf("after pick, adapter.Model() = %q, want llama3:8b — picker must override provider default on the live adapter", got.Model())
	}
	// Session model must also reflect the pick, so a per-turn save
	// records the actually-used model (not the config default).
	if m.sess.Model != "llama3:8b" {
		t.Errorf("after pick, sess.Model = %q, want llama3:8b", m.sess.Model)
	}
}

// Picking a model in the overlay is the default /model UX (the bare
// invocation), so the no-op re-check needs to hold here too, not just
// on the /model <name> and /provider use shortcuts. reasoningEffort is
// a session-global field commitModelChoice never clears or
// re-validates, so a level that applied cleanly on the old model must
// be re-flagged as a no-op the moment the picked model can't use it.
func TestModelPicker_PickWarnsWhenEffortBecomesNoop(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "anthropic-test"
kind = "anthropic"
base_url = "https://anthropic.example/v1"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "openai-test"
kind = "openai"
base_url = "https://openai.example/v1"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`)
	m, _ = typeAndEnter(t, m, "/provider use anthropic-test")
	m, _ = typeAndEnter(t, m, "/effort high")
	if strings.Contains(m.transcript.String(), "no-op on this model") {
		t.Fatalf("effort should apply cleanly on claude-sonnet-4-6; transcript:\n%s", m.transcript.String())
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	prov := *cfg.FindProvider("openai-test")
	m.pickerList = stubPickerList([]catalog.Model{{ID: "gpt-4o", Provider: "session"}}, nil)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())

	pre := m.transcript.String()
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelPickerOpen {
		t.Fatalf("Enter should close the picker after committing")
	}
	post := m.transcript.String()[len(pre):]
	if !strings.Contains(post, "no-op on this model") {
		t.Errorf("picking gpt-4o (non-reasoning) should re-surface the no-op warning; new transcript:\n%s", post)
	}
}

// The pickerList stub is the source of truth — when it returns the
// list, those entries land in the picker. This replaces the old
// merge-with-static-catalog test (we removed that merge — config-side
// p.Models is no longer surfaced in the picker; the embedded catalog
// or live fetch is canonical).
func TestModelPicker_PickerListResultsAreDisplayed(t *testing.T) {
	m := newTestModel(t)
	m.pickerList = stubPickerList(
		[]catalog.Model{
			{ID: "first", Provider: "session"},
			{ID: "second", Provider: "session"},
		},
		nil,
	)
	prov := config.Provider{
		Name:    "x",
		Kind:    "openai",
		BaseURL: "https://example.com/v1",
	}
	cmd := m.openModelPicker(prov)
	if cmd == nil {
		t.Fatal("openModelPicker should return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(modelPickerLoadedMsg); !ok {
		t.Fatalf("cmd should produce modelPickerLoadedMsg; got %T", msg)
	}
	m, _ = applyMsg(m, msg)
	if len(m.modelPicker.entries) != 2 {
		t.Errorf("picker should show 2 entries; got %d", len(m.modelPicker.entries))
	}
	if m.modelPicker.entries[0].ID != "first" || m.modelPicker.entries[1].ID != "second" {
		t.Errorf("entries out of order: %+v", m.modelPicker.entries)
	}
}
