package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/providerops"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

// modelPickerState holds the in-flight Bubbletea overlay for /model.
// Lifetime is one /model invocation: opened by cmdModel, lives on the
// parent Model's modelPicker pointer, dropped on Esc / Enter / outer
// state change. Heap-allocated rather than a value type so the
// loaded-models async message can mutate fields without going
// through Model's value-receiver Update path.
type modelPickerState struct {
	provider config.Provider
	apiKey   string

	// activeModel is the picker's view of the currently-saved
	// default model, used to render the "current default" hint next
	// to each row. Set on open; not mutated by the picker (the
	// actual config write happens after Enter).
	activeModel string

	// entries is the model list for the provider currently being
	// displayed. For curated providers (anthropic/openai/gemini/xai) this
	// comes from the embedded catalog and is populated near-instantly.
	// For openai-compatible / ollama it's the result of a live HTTP
	// fetch.
	entries []catalog.Model

	// loaded and loadErr are set when the async fetch completes.
	// loadErr surfaces in the picker so users see why the list looks
	// short (e.g. "couldn't reach API, run yotta-models refresh").
	loaded  bool
	loadErr error

	// cursor is the highlighted row index into entries.
	cursor int

	// allProviders is a snapshot of cfg.Providers at open time;
	// providerIdx tracks which one is currently being displayed.
	// ←/→ keys cycle through the list. The picker's `provider`
	// field above mirrors allProviders[providerIdx]. Empty when
	// the picker opened on a synthesized session profile (ad-hoc
	// session with no [[providers]] entries) — provider switching
	// is a no-op in that case.
	allProviders []config.Provider
	providerIdx  int

	// Scroll window. Rows from windowTop to windowTop+visibleRows
	// are rendered; the cursor is kept inside that range by
	// clampCursorToWindow after every move. visibleRows is set on
	// open from the parent terminal height (with a minimum of 5).
	windowTop   int
	visibleRows int
}

// modelPickerVisibleRows estimates how many model rows fit between
// the picker's chrome (title, slot indicator, footer, status bar,
// box border) and the bottom of the terminal. Caller passes the
// terminal height; returns at least 5 so a tiny terminal still has
// a usable list.
func modelPickerVisibleRows(termHeight int) int {
	const chrome = 10 // title + blanks + slot row + footer + box + status
	rows := termHeight - chrome
	if rows < 5 {
		return 5
	}
	if rows > 20 {
		// Cap so a tall terminal doesn't render an enormous box —
		// 20 rows covers the long tail (OpenRouter, Together) and
		// scrollback handles the rest.
		return 20
	}
	return rows
}

// pickerListFn is the model-list source used by openModelPicker and
// fetchPickerModels. Real callers use catalog.List; tests inject a
// fake to exercise the picker without a network round-trip.
type pickerListFn func(ctx context.Context, p config.Provider, apiKey string) ([]catalog.Model, error)

// modelPickerLoadedMsg is delivered when the model fetch finishes.
// Carries the list for the caller to install on the picker.
type modelPickerLoadedMsg struct {
	entries []catalog.Model
	err     error
}

// openModelPicker installs a fresh picker on m and returns a cmd that
// kicks off the fetch. The picker shows a "loading…" state until the
// fetch lands. activeProvider is the profile to list models for;
// usually m.providerProfileForModel().
func (m *Model) openModelPicker(activeProvider config.Provider) tea.Cmd {
	apiKey := m.apiKey
	cfg := loadConfigForCommand(*m)

	// Snapshot every configured provider so ←/→ can browse models
	// across the whole list without re-reading the config. Find the
	// index that matches activeProvider so the picker opens on the
	// "expected" provider; fall back to 0 if it's a synthesized
	// session profile not present in [[providers]].
	all := append([]config.Provider(nil), cfg.Providers...)
	idx := 0
	for i, p := range all {
		if p.Name == activeProvider.Name {
			idx = i
			break
		}
	}

	m.modelPicker = &modelPickerState{
		provider:     activeProvider,
		apiKey:       apiKey,
		activeModel:  m.modelName,
		allProviders: all,
		providerIdx:  idx,
		visibleRows:  modelPickerVisibleRows(m.height),
	}
	m.modelPickerOpen = true
	return m.fetchPickerModels(activeProvider, apiKey)
}

// fetchPickerModels returns a tea.Cmd that calls catalog.List and
// delivers the result as a modelPickerLoadedMsg. Used by both the
// initial open and the ←/→ cycle path. Curated providers come back
// near-instantly; live providers may take a couple of seconds.
func (m *Model) fetchPickerModels(p config.Provider, apiKey string) tea.Cmd {
	list := m.pickerList
	if list == nil {
		list = catalog.List
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		entries, err := list(ctx, p, apiKey)
		return modelPickerLoadedMsg{entries: entries, err: err}
	}
}

// cyclePickerProvider switches the picker to the next/previous
// configured provider (delta = +1 / -1). Resets the loading state
// and returns a fetch cmd for the new provider's models. No-op
// when the picker has 0 or 1 configured providers.
func (m *Model) cyclePickerProvider(delta int) tea.Cmd {
	p := m.modelPicker
	if p == nil || len(p.allProviders) < 2 {
		return nil
	}
	p.providerIdx = (p.providerIdx + delta + len(p.allProviders)) % len(p.allProviders)
	p.provider = p.allProviders[p.providerIdx]
	p.entries = nil
	p.loaded = false
	p.loadErr = nil
	p.cursor = 0
	p.windowTop = 0
	// API key for the new provider may differ; pull it fresh from
	// the profile's APIKeyEnv (or fall back to whatever the session
	// already had).
	apiKey := m.apiKey
	if p.provider.APIKeyEnv != "" {
		if v := os.Getenv(p.provider.APIKeyEnv); v != "" {
			apiKey = v
		}
	}
	p.apiKey = apiKey
	return m.fetchPickerModels(p.provider, apiKey)
}

// clampCursorToWindow keeps cursor inside [windowTop, windowTop +
// visibleRows). Called after every cursor change so the visible
// rows always include the highlighted entry.
func (p *modelPickerState) clampCursorToWindow() {
	if p.visibleRows <= 0 {
		return
	}
	if p.cursor < p.windowTop {
		p.windowTop = p.cursor
	}
	if p.cursor >= p.windowTop+p.visibleRows {
		p.windowTop = p.cursor - p.visibleRows + 1
	}
	if p.windowTop < 0 {
		p.windowTop = 0
	}
}

// updateModelPicker handles keystrokes while the picker is the
// foreground modal. Returns the new model + any cmd to spawn (e.g.
// a provider probe after a default-slot switch).
func (m Model) updateModelPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.modelPicker == nil {
		// Defensive: should never happen, but if state drifts close
		// the picker rather than panic.
		m.modelPickerOpen = false
		return m, nil
	}
	p := m.modelPicker
	switch msg.Type {
	case tea.KeyEsc:
		m.modelPickerOpen = false
		m.modelPicker = nil
		m.openSlashPalette()
		return m, nil
	case tea.KeyLeft:
		// Cycle to the previous configured provider — a no-op when
		// the user has 0 or 1 [[providers]] entries.
		return m, m.cyclePickerProvider(-1)
	case tea.KeyRight:
		return m, m.cyclePickerProvider(1)
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		p.clampCursorToWindow()
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.entries)-1 {
			p.cursor++
		}
		p.clampCursorToWindow()
		return m, nil
	case tea.KeyPgUp:
		// Page up by visibleRows. Clamps at 0 so the cursor never
		// wraps off the top of the list.
		p.cursor -= p.visibleRows
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.clampCursorToWindow()
		return m, nil
	case tea.KeyPgDown:
		p.cursor += p.visibleRows
		if p.cursor > len(p.entries)-1 {
			p.cursor = len(p.entries) - 1
		}
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.clampCursorToWindow()
		return m, nil
	case tea.KeyHome:
		p.cursor = 0
		p.clampCursorToWindow()
		return m, nil
	case tea.KeyEnd:
		p.cursor = len(p.entries) - 1
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.clampCursorToWindow()
		return m, nil
	case tea.KeyEnter:
		if !p.loaded || len(p.entries) == 0 {
			return m, nil
		}
		chosenEntry := p.entries[p.cursor]
		chosen := chosenEntry.ID
		// On per-model-key providers (NVIDIA NIM), reject Enter on
		// any model that doesn't have a configured profile. Without
		// the gate the user would commit, the adapter would dispatch
		// to NVIDIA, and the chat call would 404 mid-conversation
		// because the existing key isn't authorized for the chosen
		// model.
		if isPerModelKeyProvider(p.provider) {
			if _, ok := p.configuredModels()[chosen]; !ok {
				m.appendLine(styleAuto.Render(statusLine("model", fmt.Sprintf(
					"%q has no API key configured — run /provider add to register a new profile (NVIDIA keys are minted per-model at build.nvidia.com)",
					chosen))))
				return m, nil
			}
		}
		return m.commitModelChoice(chosen, chosenEntry.ContextWindow)
	}
	return m, nil
}

// handleModelPickerLoaded installs the fetch result onto the open
// picker. Called from the parent Update on modelPickerLoadedMsg.
func (m Model) handleModelPickerLoaded(msg modelPickerLoadedMsg) (Model, tea.Cmd) {
	if m.modelPicker == nil {
		return m, nil
	}
	p := m.modelPicker
	p.entries = msg.entries
	p.loadErr = msg.err
	p.loaded = true
	// Default the cursor to the currently-active model so the user
	// can hit Enter without arrowing if they opened the picker by
	// accident or to confirm.
	for i, e := range p.entries {
		if e.ID == p.activeModel {
			p.cursor = i
			break
		}
	}
	p.clampCursorToWindow()
	return m, nil
}

// commitModelChoice persists the user's selection through providerops,
// closes the picker, and refreshes the active adapter + connection
// probe so the next turn uses the new model.
// window is the chosen model's context window as reported by the
// provider's list-models endpoint (0 when the provider reported none);
// it's persisted onto the active provider so the watermark + summarize
// threshold size against the real window instead of a built-in guess.
func (m Model) commitModelChoice(chosen string, window int) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)

	// Capture the picker's currently-displayed provider profile
	// before we tear the picker state down. We need it to align
	// m.baseURL / m.apiKey with the chosen provider — without it,
	// picking a model after ←/→ cycling rebuilds the adapter against
	// the SESSION's (now-stale) base URL, which is exactly the
	//   POST "http://localhost:11434/v1/chat/completions": 404
	//   model 'nvidia/nemotron-...' not found
	// failure mode users hit when picking an NVIDIA model from a
	// session that opened on Ollama.
	var pickerProv config.Provider
	if m.modelPicker != nil {
		pickerProv = m.modelPicker.provider
	}

	// If the picker is listing models for a provider that exists
	// in [[providers]] but isn't the current active.provider yet,
	// adopt it as active before writing the model. Otherwise
	// SetActiveModel rejects with "no active provider; run `provider
	// use <name>` first" — which is exactly the error users hit
	// before this fix.
	if pickerProv.Name != "" && cfg.Active.Provider != pickerProv.Name {
		if cfg.FindProvider(pickerProv.Name) != nil {
			cfg, _ = providerops.SetActive(cfg, pickerProv.Name)
		}
	}

	updated, err := providerops.SetActiveModel(cfg, chosen)
	if err != nil {
		m.appendLine(styleError.Render(statusLine("model", fmt.Sprintf("%v", err))))
		return m, nil
	}
	// Persist the provider-reported context window (if any) to the
	// file-backed window store, NOT config.toml — writing it into the
	// provider's models list could exclude the default_model and invalidate
	// the config. The store is keyed by model and read by ResolveWindow.
	// No-op when the provider reported no window.
	_, _ = catalog.UpsertWindow(chosen, window)
	if err := config.Validate(updated); err != nil {
		m.appendLine(styleError.Render(statusLine("model", fmt.Sprintf("config invalid: %v", err))))
		return m, nil
	}
	if err := writeConfig(updated); err != nil {
		m.appendLine(styleError.Render(statusLine("model", fmt.Sprintf("write config: %v", err))))
		return m, nil
	}
	// Refresh the in-memory config so the status bar + auto-summarize
	// threshold pick up a just-persisted per-model context_window
	// immediately (otherwise m.fileCfg stays stale until the next
	// session and the window override looks like a no-op live).
	m.fileCfg = updated
	m.modelPickerOpen = false
	m.modelPicker = nil
	// Adopt the picker's provider URL + key into the running session
	// BEFORE rebuilding the adapter. Skipped when the picker opened on
	// a synthesized session profile (no matching [[providers]] entry,
	// BaseURL was empty) — in that case keep the session's existing URL.
	if pickerProv.BaseURL != "" {
		m.baseURL = pickerProv.BaseURL
		m.provider = string(detectKindAsProvider(pickerProv.Kind))
		m.providerLabel = wizard.CatalogIdentity(pickerProv.Name)
		if pickerProv.APIKeyEnv != "" {
			if v := os.Getenv(pickerProv.APIKeyEnv); v != "" {
				m.apiKey = v
			}
		}
	}
	m.modelName = chosen
	m.sess.Model = chosen
	ad := adapter.NewWithConfig(m.adapterConfig(chosen, m.baseURL))
	m.cfg.Adapter = ad
	// Also update the AgentTool's Adapter so subagents inherit the new provider
	if m.subagentTool != nil {
		m.subagentTool.Adapter = ad
	}
	m.providerProfile = ad.Profile()
	m, _ = reloadMemoryNow(m, "")
	m.appendLine(styleAuto.Render(statusLine("model", fmt.Sprintf("default → %s", chosen))))
	cmds := []tea.Cmd{runProviderProbe(m.parentCtx, m.adapterConfig(chosen, m.baseURL), false)}
	// When the picker's list-models row carried no window for this model
	// (NVIDIA NIM, thin proxies), discover it in the background from the
	// live API and persist on return — non-blocking so the picker closes
	// instantly. Gated through shouldProbeWindow so this site matches the
	// startup + provider-switch triggers exactly: only openai-compatible
	// kinds are probeable. Without the kind gate, picking a model on an
	// openai-auth / copilot / ollama provider (whose catalog rows carry no
	// window, so window<=0) would fire an overflow probe — a 300K-token
	// POST /chat/completions — against an endpoint that either speaks a
	// different API shape (the ChatGPT/Copilot backends) or shouldn't be
	// overflow-probed at all. shouldProbeWindow also subsumes the window<=0
	// check: a just-persisted window (window>0, written above) sets the
	// override, so ContextWindowOverride!=0 and the probe is skipped.
	if m.shouldProbeWindow(pickerProv.Kind, chosen) {
		if m.probedModels == nil {
			m.probedModels = map[string]bool{}
		}
		m.probedModels[chosen] = true
		cmds = append(cmds, discoverWindowCmd(m.parentCtx, pickerProv, m.apiKey, chosen))
	}
	return m, tea.Batch(cmds...)
}

// modelWindowProbedMsg carries the result of a background context-window
// discovery (live list-models, then overflow probe) kicked off by a model
// or provider switch. window is 0 when neither source determined a limit
// (left to default_window).
type modelWindowProbedMsg struct {
	provider string
	model    string
	window   int
	detail   string // human-readable: which source answered, or why none did
}

// discoverWindowCmd resolves a model's context window from the provider's
// live API off the UI thread (catalog.DiscoverContextWindow: list-models
// first, overflow probe as fallback) and reports it as a
// modelWindowProbedMsg. No hardcoded model table — the window is read
// from the deployment, which is the only place it's authoritative.
func discoverWindowCmd(ctx context.Context, prov config.Provider, apiKey, model string) tea.Cmd {
	return func() tea.Msg {
		w, detail := catalog.DiscoverContextWindow(ctx, prov, apiKey, model)
		return modelWindowProbedMsg{
			provider: prov.Name,
			model:    model,
			window:   w,
			detail:   detail,
		}
	}
}

// maybeProbeActiveModelWindowCmd returns a background command that
// discovers the active model's context window, or nil when there's
// nothing to do. This is what makes window discovery automatic: a fresh
// session that boots straight into a windowless model (NVIDIA NIM, custom
// OpenAI-compatible proxies) self-corrects from default_window to the
// real, deployment-reported window without the user running any command.
// It's a no-op once the window is known, so it runs at most once per
// model — the result is persisted and read back on the next launch.
//
// Gated (via shouldProbeWindow) to: a per-model context_window override is
// NOT already set, and the active provider is a kind live discovery speaks
// — openai-compatible (list-models / overflow probe) or ollama (/api/show).
// Anthropic/Gemini carry windows in the curated catalog and use different
// APIs, so they're skipped.
func (m Model) maybeProbeActiveModelWindowCmd() tea.Cmd {
	if strings.TrimSpace(m.baseURL) == "" {
		return nil
	}
	prov := m.fileCfg.FindProvider(m.fileCfg.Active.Provider)
	if prov == nil || !m.shouldProbeWindow(prov.Kind, m.modelName) {
		return nil
	}
	return discoverWindowCmd(m.parentCtx, *prov, m.apiKey, m.modelName)
}

// shouldProbeWindow reports whether a model served by a provider of the
// given kind needs background window discovery: a discoverable kind
// (openai-compatible via list-models/overflow probe, or ollama via
// /api/show — see catalog.DiscoverContextWindow), no window override saved
// yet, and not already attempted this session. Shared by the startup,
// picker, and provider-switch trigger sites so they gate identically.
func (m Model) shouldProbeWindow(kind, model string) bool {
	return (kind == "openai-compatible" || kind == "ollama") &&
		strings.TrimSpace(model) != "" &&
		m.fileCfg.ContextWindowOverride(model) == 0 &&
		!m.probedModels[model]
}

// renderModelPicker draws the overlay. Drop-down feel: provider tab
// strip sits below the title when more than one provider is
// configured — the active tab is bracketed + accent-bold so users
// see the ←/→ cycling affordance instantly without reading prose.
// Single-provider opens skip the strip (nothing to switch). Rows
// have no bullet/cursor glyphs (the inverse-color highlight on the
// cursor row carries the vertical navigation cue); the saved-for
// hint is plain "current" text right-aligned. Long lists scroll
// within a fixed window with "▲ N more" / "▼ N more" hints at the
// boundaries.
func renderModelPicker(p *modelPickerState, width int) string {
	_ = width // reserved for future per-row truncation; the inline
	// overlay no longer needs it for centering.
	var b strings.Builder
	b.WriteString(renderMenuHeader("Model", ""))
	if len(p.allProviders) > 1 {
		b.WriteString(renderProviderTabStrip(p))
		b.WriteString("\n")
	} else if len(p.allProviders) == 1 {
		// Single-provider open: no strip needed, but show which
		// provider's catalog is being browsed so the title still
		// orients the user.
		b.WriteString(stylePaletteEmpty.Render("  " + p.provider.Name))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	switch {
	case !p.loaded:
		b.WriteString(stylePaletteEmpty.Render("loading model list…"))
	case len(p.entries) == 0:
		emptyHint := "no models available"
		switch {
		case p.provider.Kind == "openai-auth":
			emptyHint = "no models discovered yet — run `yottacode openai-auth login` to populate"
		case p.provider.Kind == "copilot":
			emptyHint = "no models cached yet — run `yottacode copilot-auth login` to populate"
		case catalog.IsCurated(p.provider):
			emptyHint += " — run `go run ./cmd/yotta-models refresh` to populate the catalog"
		}
		b.WriteString(stylePaletteEmpty.Render(emptyHint))
		if p.loadErr != nil {
			b.WriteString("\n")
			b.WriteString(styleError.Render(truncateErr(p.loadErr.Error(), width-4)))
		}
	default:
		// Window-bounded slice: [windowTop, windowTop+visibleRows).
		// "▲ N above" / "▼ N below" hints fire when content is
		// scrolled off so users know the list extends past what
		// they see.
		visible := p.visibleRows
		if visible <= 0 {
			visible = len(p.entries)
		}
		end := p.windowTop + visible
		if end > len(p.entries) {
			end = len(p.entries)
		}
		if p.windowTop > 0 {
			above := stylePaletteEmpty.Render(fmt.Sprintf("  ▲ %d more above", p.windowTop))
			b.WriteString(above)
			b.WriteString("\n")
		}
		// Per-model-key providers (NVIDIA NIM) grey out models that
		// don't have a configured profile in the user's config.
		// configuredModels is the union of default_model across all
		// per-model-key profiles, so users with multiple NVIDIA
		// profiles see all of THEIR configured models as enabled.
		perModel := isPerModelKeyProvider(p.provider)
		var configured map[string]struct{}
		if perModel {
			configured = p.configuredModels()
		}
		for i := p.windowTop; i < end; i++ {
			enabled := true
			if perModel {
				_, enabled = configured[p.entries[i].ID]
			}
			line := renderPickerRow(p.entries[i], i == p.cursor, p.activeModel, enabled, p.entries[i].Disabled)
			b.WriteString(line)
			b.WriteString("\n")
		}
		if end < len(p.entries) {
			below := stylePaletteEmpty.Render(fmt.Sprintf("  ▼ %d more below", len(p.entries)-end))
			b.WriteString(below)
			b.WriteString("\n")
		}
		if perModel {
			b.WriteString("\n")
			b.WriteString(stylePaletteEmpty.Render(
				"per-model API keys: greyed rows need a separate /provider add"))
		}
		if p.loadErr != nil {
			b.WriteString("\n")
			b.WriteString(stylePaletteEmpty.Render("(live fetch failed: " +
				truncateErr(p.loadErr.Error(), width-40) + ")"))
		}
	}

	b.WriteString("\n")
	footer := "↵ save · ↑↓ navigate · PgUp/PgDn jump · home/end · esc cancel"
	b.WriteString(styleFooter.Render(footer))

	// Inline-overlay shape: no outer rounded box, no horizontal
	// centering. The parent renderInlineOverlay sits this body below
	// the cmdline + status bar + separator rule, so a second border
	// would read as "modal floating on a modal".
	return strings.TrimRight(b.String(), "\n")
}

// renderProviderTabStrip draws the provider switcher as a tab bar:
// active provider in [ name ] brackets with accent-bold styling,
// inactive providers muted, two spaces between tabs. The trailing
// "←/→ switch" hint sits inline so the affordance is impossible to
// miss — replaces the prose "Use ←/→ to switch provider" line that
// users skipped right past. Indented two columns to align with the
// rest of the picker chrome.
func renderProviderTabStrip(p *modelPickerState) string {
	activeStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	tabs := make([]string, 0, len(p.allProviders))
	for i, prov := range p.allProviders {
		if i == p.providerIdx {
			tabs = append(tabs, activeStyle.Render("[ "+prov.Name+" ]"))
		} else {
			tabs = append(tabs, mutedStyle.Render(prov.Name))
		}
	}
	hint := mutedStyle.Render(fmt.Sprintf("←/→ switch (%d/%d)", p.providerIdx+1, len(p.allProviders)))
	return "  " + strings.Join(tabs, "   ") + "    " + hint
}

// renderPickerRow draws one model entry through the shared
// menuItemOpts helper. Layout:
//
//	❯ Claude Sonnet 4.6           thinking · vision · pdf · 200k ctx · current
//	  Claude Haiku 4.5            thinking · 200k ctx
//	  nvidia/llama-3.1-…          —                          (disabled — needs API key)
//
// Long names are truncated to LabelWidth with `…` so the desc column
// lines up regardless of name length. Capability chips show only
// known-true caps (the picker is a glance view; nuanced "—/✗/✓"
// belongs in a future details panel).
func renderPickerRow(e catalog.Model, isCursor bool, activeModel string, enabled bool, planDisabled bool) string {
	var savedFor string
	if !enabled {
		savedFor = " · needs API key"
	} else if planDisabled {
		savedFor = " · upgrade plan"
		enabled = false
	}
	chips := capabilityChips(e.Capabilities)
	var ctx string
	switch {
	case e.ContextWindow >= 1000:
		ctx = fmt.Sprintf("%dk ctx", e.ContextWindow/1000)
	case e.ContextWindow > 0:
		ctx = fmt.Sprintf("%d ctx", e.ContextWindow)
	default:
		ctx = ""
	}
	parts := make([]string, 0, 3)
	if chips != "" {
		parts = append(parts, chips)
	}
	if ctx != "" {
		parts = append(parts, ctx)
	}
	desc := strings.Join(parts, " · ")
	if desc == "" {
		desc = "—"
	}
	desc += savedFor
	return renderMenuItem(menuItemOpts{
		Label:      e.Label(),
		LabelWidth: 38,
		Desc:       desc,
		Cursor:     isCursor,
		Checked:    e.ID == activeModel,
		Disabled:   !enabled,
	})
}

// capabilityChips renders only the known-true capabilities as a
// compact " · "-joined string. Nil (unknown) and false (explicitly
// not supported) values are omitted — the picker is a glance view
// and we want it to scan, not annotate gaps.
func capabilityChips(c catalog.Capabilities) string {
	parts := make([]string, 0, 5)
	if c.Thinking != nil && *c.Thinking {
		parts = append(parts, "thinking")
	}
	if c.Vision != nil && *c.Vision {
		parts = append(parts, "vision")
	}
	if c.PDF != nil && *c.PDF {
		parts = append(parts, "pdf")
	}
	if c.StructuredOutputs != nil && *c.StructuredOutputs {
		parts = append(parts, "json")
	}
	if c.Tools != nil && *c.Tools {
		parts = append(parts, "tools")
	}
	return strings.Join(parts, " · ")
}

// isPerModelKeyProvider returns true for providers that issue API
// keys per model rather than per account/org. NVIDIA NIM at
// integrate.api.nvidia.com is the canonical example: keys minted
// at build.nvidia.com are scoped to a single model, so picking
// any other model with the same key 404s. Detected by URL pattern
// since the catalog already routes NVIDIA through
// kind="openai-compatible". If a future provider adopts the same
// quirk we'll promote this to a dedicated catalog flag.
func isPerModelKeyProvider(p config.Provider) bool {
	return p.Kind == "openai-compatible" && strings.Contains(p.BaseURL, "nvidia.com")
}

// configuredModels returns the set of model names that have a
// configured API key — i.e., they're the default_model of at least
// one per-model-key provider in the user's config. Picker uses
// this to grey out unconfigured models on per-model-key providers
// so users can't pick a model their key won't authorize.
func (p *modelPickerState) configuredModels() map[string]struct{} {
	out := make(map[string]struct{}, len(p.allProviders))
	for _, prov := range p.allProviders {
		if isPerModelKeyProvider(prov) && prov.DefaultModel != "" {
			out[prov.DefaultModel] = struct{}{}
		}
	}
	return out
}

// truncateErr keeps long error strings from blowing up the layout.
func truncateErr(s string, max int) string {
	if max < 8 {
		max = 8
	}
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// providerProfileForModel returns the config.Provider matching the
// session's current base URL — the profile whose models the picker
// should list. Falls back to a synthesized "ad-hoc" provider so the
// picker has *something* to show even when the user is running with
// only --model + --base-url and no [[providers]] entry.
func (m Model) providerProfileForModel() config.Provider {
	cfg := loadConfigForCommand(m)
	for _, p := range cfg.Providers {
		if p.BaseURL == m.baseURL {
			return p
		}
	}
	// Synthesized profile for "ad-hoc" sessions (no [[providers]]
	// in config.toml). Kind is inferred from the running adapter
	// profile so live-fetch picks the right URL/header shape.
	kind := "openai-compatible"
	switch m.providerProfile.Provider {
	case "anthropic":
		kind = "anthropic"
	case "openai":
		kind = "openai"
	case "ollama":
		kind = "ollama"
	}
	return config.Provider{
		Name:    "session",
		Kind:    kind,
		BaseURL: m.baseURL,
	}
}
