package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
)

// routerModelEntry is one selectable model in the picker's fast/smart
// sub-list. ref is the "<provider>:<model>" grammar that fast_model /
// smart_model use; label is display-only (currently the same string).
type routerModelEntry struct {
	ref   string
	label string
}

// Menu rows. The smart slot has a primary row and an optional fallback row
// (the fallback reuses the single-select model list and appends to
// smart_models). The fast slot is summarization-only and self-healing, so
// it has no fallback row — a fast_models chain set in config.toml is still
// honored and preserved on edit, just not surfaced here. Longer chains
// stay editable in config.toml.
const (
	rowRouting = iota
	rowSmartPrimary
	rowFastPrimary
	rowSmartFallback
	routerMenuRows
)

// routerPickerState owns the /router overlay: a menu (routing toggle +
// primary/fallback rows per slot) plus a windowed model sub-list opened
// to fill a row.
type routerPickerState struct {
	cursor int    // menu row (rowRouting … rowSmartFallback)
	mode   string // working copy of [router].mode
	// fastChain/smartChain are the working failover chains (primary first),
	// seeded from config and persisted on each edit. The picker edits the
	// primary (index 0) and the first fallback (index 1); any further
	// entries set in config.toml are preserved.
	fastChain  []string
	smartChain []string
	note       string // transient warning shown under the menu
	// initialSmart is the smart primary at open time. On close, if the
	// user changed it, the active conversation model is switched to match
	// (the smart model is the user's primary capable model).
	initialSmart string

	// selecting names the row being filled: "fast"/"smart" (primary) or
	// "fast-fb"/"smart-fb" (fallback). Empty = the menu is focused.
	selecting   string
	models      []routerModelEntry
	modelCursor int
	windowTop   int
	visibleRows int
}

const routerPickerVisibleRows = 12

func chainPrimary(c []string) string {
	if len(c) > 0 {
		return c[0]
	}
	return ""
}

func chainFallback(c []string) string {
	if len(c) > 1 {
		return c[1]
	}
	return ""
}

func (p *routerPickerState) chain(slot string) []string {
	if slot == "smart" {
		return p.smartChain
	}
	return p.fastChain
}

func (p *routerPickerState) setChain(slot string, c []string) {
	if slot == "smart" {
		p.smartChain = c
	} else {
		p.fastChain = c
	}
}

// routerPickerModels enumerates every model the user can route to as
// "<provider.Name>:<model>" refs: the embedded catalog for curated
// providers plus any explicitly-listed providers.models and default_model.
// Mirrors the membership sources in cli/router.go's providerHasModel.
func routerPickerModels(cfg config.Config) []routerModelEntry {
	seen := map[string]bool{}
	var out []routerModelEntry
	add := func(provName, model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		ref := provName + ":" + model
		if seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, routerModelEntry{ref: ref, label: ref})
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if catalog.IsCurated(*p) {
			for _, mdl := range catalog.Get(p.Kind) {
				add(p.Name, mdl.ID)
			}
		}
		for _, mm := range p.Models {
			add(p.Name, mm.Name)
		}
		add(p.Name, p.DefaultModel)
	}
	return out
}

// openRouterPicker installs the overlay, seeding the working chains from
// the on-disk [router] config and the live mode.
func (m *Model) openRouterPicker() {
	cfg := loadConfigForCommand(*m)
	m.routerPicker = &routerPickerState{
		mode:        routerModeOrOff(m.routerMode),
		fastChain:   cfg.Router.FastChain(),
		smartChain:  cfg.Router.SmartChain(),
		models:      routerPickerModels(cfg),
		visibleRows: routerPickerVisibleRows,
	}
	m.routerPicker.initialSmart = chainPrimary(m.routerPicker.smartChain)
	m.routerPickerOpen = true
}

// updateRouterPicker routes keystrokes to the menu or the model sub-list.
func (m Model) updateRouterPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.routerPicker
	if p == nil {
		m.routerPickerOpen = false
		return m, nil
	}
	if p.selecting != "" {
		return m.updateRouterModelList(msg)
	}

	p.note = ""
	switch msg.Type {
	case tea.KeyEsc:
		return m.closeRouterPicker()
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < routerMenuRows-1 {
			p.cursor++
		}
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		return m.clearFallbackRow()
	case tea.KeyEnter:
		return m.activateRouterMenuRow()
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'j':
				if p.cursor < routerMenuRows-1 {
					p.cursor++
				}
			case 'k':
				if p.cursor > 0 {
					p.cursor--
				}
			case 'd':
				return m.clearFallbackRow()
			}
		}
		return m, nil
	}
	return m, nil
}

// clearFallbackRow drops the fallback(s) of the slot under the cursor when
// it's a fallback row (a no-op elsewhere), leaving just the primary.
func (m Model) clearFallbackRow() (Model, tea.Cmd) {
	p := m.routerPicker
	var slot string
	switch p.cursor {
	case rowSmartFallback:
		slot = "smart"
	default:
		return m, nil
	}
	chain := p.chain(slot)
	if len(chain) <= 1 {
		return m, nil // no fallback to clear
	}
	return commitRouterChain(m, slot, chain[:1])
}

// activateRouterMenuRow handles Enter on a menu row: toggle routing or
// open the model list to fill a primary/fallback slot.
func (m Model) activateRouterMenuRow() (Model, tea.Cmd) {
	p := m.routerPicker
	switch p.cursor {
	case rowRouting:
		// Toggle in place — stay in the picker either way.
		if routerModeOrOff(p.mode) != config.RouterModeOff {
			p.mode = config.RouterModeOff
			p.note = ""
			return commitRouterMode(m, config.RouterModeOff)
		}
		// Turn on. Flip the working state now so the user can pick the
		// models; routing persists + applies once both primaries are set
		// (here, or via completePendingEnable after a pick).
		p.mode = config.RouterModeAuto
		if chainPrimary(p.fastChain) == "" || chainPrimary(p.smartChain) == "" {
			p.note = "select a fast and a smart model below to finish enabling"
			return m, nil
		}
		p.note = ""
		return commitRouterMode(m, config.RouterModeAuto)
	case rowFastPrimary:
		p.openModelList("fast")
	case rowSmartPrimary:
		p.openModelList("smart")
	case rowSmartFallback:
		if chainPrimary(p.smartChain) == "" {
			p.note = "set a smart model first"
			return m, nil
		}
		p.openModelList("smart-fb")
	}
	return m, nil
}

// updateRouterModelList handles keystrokes inside the model sub-list.
// Enter sets the row (persists + applies + returns to the menu); Esc backs
// out without changing anything.
func (m Model) updateRouterModelList(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.routerPicker
	n := len(p.models)
	switch msg.Type {
	case tea.KeyEsc:
		p.selecting = ""
		return m, nil
	case tea.KeyUp:
		if p.modelCursor > 0 {
			p.modelCursor--
		}
		p.clampWindow()
		return m, nil
	case tea.KeyDown:
		if p.modelCursor < n-1 {
			p.modelCursor++
		}
		p.clampWindow()
		return m, nil
	case tea.KeyHome:
		p.modelCursor = 0
		p.clampWindow()
		return m, nil
	case tea.KeyEnd:
		if n > 0 {
			p.modelCursor = n - 1
		}
		p.clampWindow()
		return m, nil
	case tea.KeyEnter:
		if n == 0 {
			return m, nil
		}
		ref := p.models[p.modelCursor].ref
		sel := p.selecting
		p.selecting = ""
		return m.commitRouterEntry(sel, ref)
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'j':
				if p.modelCursor < n-1 {
					p.modelCursor++
				}
				p.clampWindow()
			case 'k':
				if p.modelCursor > 0 {
					p.modelCursor--
				}
				p.clampWindow()
			}
		}
		return m, nil
	}
	return m, nil
}

// openModelList focuses the model sub-list for a primary ("fast"/"smart")
// or fallback ("fast-fb"/"smart-fb") row, with the cursor on that row's
// current value.
func (p *routerPickerState) openModelList(sel string) {
	p.selecting = sel
	slot, isFB := parseSlotSel(sel)
	current := chainPrimary(p.chain(slot))
	if isFB {
		current = chainFallback(p.chain(slot))
	}
	p.modelCursor = 0
	for i, e := range p.models {
		if e.ref == current {
			p.modelCursor = i
			break
		}
	}
	p.windowTop = 0
	p.clampWindow()
}

// parseSlotSel splits a selecting value into the slot ("fast"/"smart") and
// whether it targets the fallback.
func parseSlotSel(sel string) (slot string, isFallback bool) {
	if s, ok := strings.CutSuffix(sel, "-fb"); ok {
		return s, true
	}
	return sel, false
}

// commitRouterEntry applies a model pick to the primary or first-fallback
// position of a slot's chain, preserving any further fallbacks, then
// persists + rebuilds.
func (m Model) commitRouterEntry(sel, ref string) (Model, tea.Cmd) {
	slot, isFB := parseSlotSel(sel)
	chain := append([]string(nil), m.routerPicker.chain(slot)...)
	if isFB {
		if len(chain) == 0 {
			return m, nil // guarded by the menu, but stay safe
		}
		if len(chain) == 1 {
			chain = append(chain, ref)
		} else {
			chain[1] = ref
		}
	} else if len(chain) == 0 {
		chain = []string{ref}
	} else {
		chain[0] = ref
	}
	return commitRouterChain(m, slot, chain)
}

// clampWindow keeps modelCursor inside the visible [windowTop, +visible)
// window, scrolling the window when the cursor moves past either edge.
func (p *routerPickerState) clampWindow() {
	visible := p.visibleRows
	if visible <= 0 {
		return
	}
	if p.modelCursor < p.windowTop {
		p.windowTop = p.modelCursor
	}
	if p.modelCursor >= p.windowTop+visible {
		p.windowTop = p.modelCursor - visible + 1
	}
	if p.windowTop < 0 {
		p.windowTop = 0
	}
}

// commitRouterMode persists [router].mode and applies it live. Shared by
// the /router on|off shortcuts and the picker's Routing toggle.
func commitRouterMode(m Model, mode string) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	cfg.Router.Mode = mode
	if err := config.Validate(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[router] %v", err)))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[router] write config: %v", err)))
		return m, nil
	}
	m.fileCfg = cfg
	if mode == config.RouterModeAuto {
		applyRoutingOn(&m)
		fast, smart := "", ""
		if m.router != nil {
			fast, smart = m.router.FastModel, m.router.SmartModel
		}
		m.appendLine(styleAuto.Render(fmt.Sprintf(
			"[router] on — %s (fast) / %s (smart) · persisted", fast, smart)))
	} else {
		applyRoutingOff(&m)
		m.appendLine(styleAuto.Render(
			"[router] off — subagents and summarization run on the active model · persisted"))
	}
	return m, nil
}

// commitRouterChain persists a slot's full failover chain (writing the
// singular form for one model, the plural fast_models/smart_models for a
// chain), rebuilds the adapters, and re-points live wiring when routing is
// active. ensureProviderModel keeps config.Validate happy for catalog
// models not already in providers.models.
func commitRouterChain(m Model, slot string, chain []string) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	for _, ref := range chain {
		ensureProviderModel(&cfg, ref)
	}
	setRouterChain(&cfg, slot, chain)
	if err := config.Validate(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[router] %v", err)))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[router] write config: %v", err)))
		return m, nil
	}
	m.fileCfg = cfg
	ra, err := cli.BuildRouterAdapters(cfg, m.opts)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[router] rebuild adapters: %v", err)))
		return m, nil
	}
	m.router = ra
	if routerModeOrOff(m.routerMode) == config.RouterModeAuto {
		applyRoutingOn(&m)
	}
	if m.routerPicker != nil {
		m.routerPicker.setChain(slot, chain)
	}
	return m.completePendingEnable()
}

// setRouterChain writes a slot's chain to config in its canonical form: the
// singular field for a single model (clearing the plural), the plural field
// for a multi-model chain (clearing the singular). A slot never has both
// forms set (Validate enforces it).
func setRouterChain(cfg *config.Config, slot string, chain []string) {
	single, plural := "", []string(nil)
	if len(chain) == 1 {
		single = chain[0]
	} else if len(chain) > 1 {
		plural = chain
	}
	if slot == "smart" {
		cfg.Router.SmartModel, cfg.Router.SmartModels = single, plural
	} else {
		cfg.Router.FastModel, cfg.Router.FastModels = single, plural
	}
}

// completePendingEnable turns routing on when the picker's working mode is
// auto (the user toggled On first) and both slots now have a primary but
// routing isn't live yet — so "enable, then pick models" finishes as soon
// as the pair is complete.
func (m Model) completePendingEnable() (Model, tea.Cmd) {
	p := m.routerPicker
	if p == nil {
		return m, nil
	}
	if routerModeOrOff(p.mode) == config.RouterModeAuto &&
		chainPrimary(p.fastChain) != "" && chainPrimary(p.smartChain) != "" &&
		routerModeOrOff(m.routerMode) != config.RouterModeAuto {
		p.note = ""
		return commitRouterMode(m, config.RouterModeAuto)
	}
	return m, nil
}

// closeRouterPicker dismisses the picker and reopens the slash palette
// (matching the other config pickers). If the user changed the smart model
// this session, the active conversation model is switched to it — the
// smart model is the user's primary capable model.
func (m Model) closeRouterPicker() (Model, tea.Cmd) {
	target := routerActiveSwitchTarget(m.routerPicker, m.modelName)
	m.routerPickerOpen = false
	m.routerPicker = nil
	m.openSlashPalette()
	if target != "" {
		return m.switchActiveModelToRef(target)
	}
	return m, nil
}

// routerActiveSwitchTarget returns the smart-model ref the active model
// should switch to on close: the configured smart primary, but only when
// it changed this session and names a model different from the current
// active one. Empty means no switch.
func routerActiveSwitchTarget(p *routerPickerState, activeModel string) string {
	if p == nil {
		return ""
	}
	smart := chainPrimary(p.smartChain)
	if smart == "" || smart == p.initialSmart {
		return ""
	}
	_, model, err := config.ParseCandidate(smart)
	if err != nil || model == "" || model == activeModel {
		return ""
	}
	return smart
}

// switchActiveModelToRef switches the main conversation model + adapter to
// the model named by ref ("<provider>:<model>"), the way /model does —
// rebuilding the adapter against the ref's provider profile. Used when the
// user configures a new smart model in /router.
func (m Model) switchActiveModelToRef(ref string) (Model, tea.Cmd) {
	provName, model, err := config.ParseCandidate(ref)
	if err != nil || model == "" {
		return m, nil
	}
	cfg := loadConfigForCommand(m)
	p := cfg.FindProvider(provName)
	if p == nil {
		return m, nil
	}
	if p.BaseURL != "" {
		m.baseURL = p.BaseURL
	}
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			m.apiKey = v
		}
	}
	ad := adapter.NewWithConfig(m.adapterConfig(model, m.baseURL))
	m.cfg.Adapter = ad
	// Keep the shared AgentTool on the same adapter as the conversation —
	// the same sync every other model/provider switch path does. Without
	// it, subagents spawned after a /router close-switch inherit the
	// STALE adapter (in off/manual modes, and in auto's no-SmartAdapter
	// fallback) and silently run on the previous model/endpoint.
	if m.subagentTool != nil {
		m.subagentTool.Adapter = ad
	}
	m.providerProfile = ad.Profile()
	m.modelName = model
	if m.sess != nil {
		m.sess.Model = model
	}
	m, _ = reloadMemoryNow(m, "")
	m.appendLine(styleAuto.Render(fmt.Sprintf(
		"[router] active model → %s (matching the configured smart model)", model)))
	return m, runProviderProbe(m.parentCtx, m.adapterConfig(model, m.baseURL), false)
}

// ensureProviderModel makes sure the model named in ref
// ("<provider>:<model>") is listed in that provider's providers.models so
// config.Validate (which checks fast/smart refs against that list, not the
// catalog) accepts a model offered from the catalog. No-op for a bare
// "<provider>" ref or a model that's already listed.
func ensureProviderModel(cfg *config.Config, ref string) {
	provName, model, err := config.ParseCandidate(ref)
	if err != nil || model == "" {
		return
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Name != provName {
			continue
		}
		for _, mm := range p.Models {
			if mm.Name == model {
				return
			}
		}
		p.Models = append(p.Models, config.Model{Name: model})
		return
	}
}

// renderRouterPicker draws the menu, or the model sub-list when a row is
// being filled.
func renderRouterPicker(p *routerPickerState) string {
	if p.selecting != "" {
		return renderRouterModelList(p)
	}
	cursorArrow := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("❯ ")
	var b strings.Builder
	b.WriteString(renderMenuHeader("Model routing", "↑↓ navigate · ↵ select · esc cancel"))
	b.WriteString("\n\n")

	routingValue := routerModeLabel(p.mode)
	if routerModeOrOff(p.mode) == config.RouterModeAuto &&
		(chainPrimary(p.fastChain) == "" || chainPrimary(p.smartChain) == "") {
		routingValue = "on — set fast & smart below"
	}
	rows := []struct{ label, value string }{
		{"Routing", routingValue},
		{"Smart model", orNotSet(chainPrimary(p.smartChain))},
		{"Fast model", orNotSet(chainPrimary(p.fastChain))},
		{"Smart fallback", fallbackValue(p.smartChain)},
	}
	for i, r := range rows {
		marker := "  "
		label := stylePaletteItem.Render(padOrTruncate(r.label, 14))
		if i == p.cursor {
			marker = cursorArrow
			label = stylePaletteSelected.Render(padOrTruncate(r.label, 14))
		}
		b.WriteString(marker)
		b.WriteString(label)
		b.WriteString("  ")
		b.WriteString(stylePaletteEmpty.Render(r.value))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(stylePaletteEmpty.Render(
		"↵ set a model (or toggle Routing) · d clears a fallback · persists to config.toml"))
	if p.note != "" {
		b.WriteString("\n")
		b.WriteString(styleError.Render(p.note))
	}
	return strings.TrimRight(b.String(), "\n")
}

// fallbackValue renders a slot's fallback row: the first fallback, "(none)"
// when there is none, plus "(+N more)" when config.toml sets a longer chain.
func fallbackValue(chain []string) string {
	fb := chainFallback(chain)
	if fb == "" {
		return "(none)"
	}
	if len(chain) > 2 {
		return fmt.Sprintf("%s (+%d more)", fb, len(chain)-2)
	}
	return fb
}

// renderRouterModelList draws the windowed model sub-list for the row being
// filled, marking the current value with · and the cursor with ❯.
func renderRouterModelList(p *routerPickerState) string {
	cursorArrow := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("❯ ")
	slot, isFB := parseSlotSel(p.selecting)
	title := "Select " + slot + " model"
	current := chainPrimary(p.chain(slot))
	if isFB {
		title = "Select " + slot + " fallback"
		current = chainFallback(p.chain(slot))
	}
	var b strings.Builder
	b.WriteString(renderMenuHeader(title, "↑↓ navigate · ↵ select · esc back"))
	b.WriteString("\n\n")
	if len(p.models) == 0 {
		b.WriteString(stylePaletteEmpty.Render("no configured models — add a provider first"))
		return b.String()
	}
	visible := p.visibleRows
	if visible <= 0 {
		visible = len(p.models)
	}
	end := min(p.windowTop+visible, len(p.models))
	if p.windowTop > 0 {
		b.WriteString(stylePaletteEmpty.Render(fmt.Sprintf("  ▲ %d more above", p.windowTop)))
		b.WriteByte('\n')
	}
	for i := p.windowTop; i < end; i++ {
		e := p.models[i]
		marker := "  "
		label := stylePaletteItem.Render(e.label)
		switch {
		case i == p.modelCursor:
			marker = cursorArrow
			label = stylePaletteSelected.Render(e.label)
		case e.ref == current:
			marker = stylePaletteEmpty.Render("· ")
		}
		b.WriteString(marker)
		b.WriteString(label)
		b.WriteByte('\n')
	}
	if end < len(p.models) {
		b.WriteString(stylePaletteEmpty.Render(fmt.Sprintf("  ▼ %d more below", len(p.models)-end)))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// routerModeLabel renders the working mode for the Routing row.
func routerModeLabel(mode string) string {
	switch routerModeOrOff(mode) {
	case config.RouterModeAuto:
		return "on (auto)"
	case config.RouterModeManual:
		return "on (manual)"
	default:
		return "off"
	}
}

// orNotSet renders a model ref, or a dim placeholder when unset.
func orNotSet(ref string) string {
	if strings.TrimSpace(ref) == "" {
		return "(not set)"
	}
	return ref
}
