package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// effortEntry is one row in the /effort picker. level is the value
// stored on the Model and passed to the adapter ("" = provider
// default); label/desc are display-only.
type effortEntry struct {
	level string
	label string
	desc  string
}

// effortEntries is the fixed menu. "default" maps to the empty level —
// it injects no reasoning param, so every provider behaves exactly as
// it does with no /effort set. The other three are the uniform surface
// each adapter translates to its own knob.
func effortEntries() []effortEntry {
	return []effortEntry{
		{level: "", label: "default", desc: "provider default — no reasoning override"},
		{level: "low", label: "low", desc: "minimal reasoning, fastest"},
		{level: "medium", label: "medium", desc: "balanced"},
		{level: "high", label: "high", desc: "maximum reasoning, slowest"},
	}
}

// effortPickerState owns the in-flight /effort overlay. Lifetime is one
// /effort invocation: opened by cmdEffort(bare), dropped on Esc/Enter.
type effortPickerState struct {
	entries []effortEntry
	cursor  int
	// current is the effort level active when the picker opened, marked
	// with a · so the user can see "where I am" distinct from the cursor.
	current string
	// note is a non-empty warning when the active model won't honor any
	// non-default level (e.g. a non-reasoning OpenAI model, grok-4, a
	// thinking-incapable Claude/Gemini). Computed once at open time and
	// shown as a footer so the user knows before committing.
	note string
}

// openEffortPicker installs a fresh picker with the cursor on the
// currently-active level, so Enter is a no-op commit and arrowing away
// starts from a known reference point.
func (m *Model) openEffortPicker() {
	entries := effortEntries()
	current := normalizedEffort(m.reasoningEffort)
	cursor := 0
	for i, e := range entries {
		if e.level == current {
			cursor = i
			break
		}
	}
	// Probe whether this model honors effort at all by testing a
	// representative non-default level ("high"). If not, surface why as a
	// footer so the picker doesn't look like it does nothing.
	probe := m.adapterConfig(m.modelName, m.baseURL)
	probe.ReasoningEffort = "high"
	note := adapter.EffortInapplicableReason(probe, m.providerProfile.Provider)

	m.effortPicker = &effortPickerState{entries: entries, cursor: cursor, current: current, note: note}
	m.effortPickerOpen = true
}

// updateEffortPicker handles keystrokes while the picker is foreground.
// Up/Down (and j/k) navigate; Enter commits; Esc closes without change.
func (m Model) updateEffortPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.effortPicker == nil {
		m.effortPickerOpen = false
		return m, nil
	}
	p := m.effortPicker
	switch msg.Type {
	case tea.KeyEsc:
		m.effortPickerOpen = false
		m.effortPicker = nil
		m.openSlashPalette()
		return m, nil
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.entries)-1 {
			p.cursor++
		}
		return m, nil
	case tea.KeyHome:
		p.cursor = 0
		return m, nil
	case tea.KeyEnd:
		p.cursor = len(p.entries) - 1
		return m, nil
	case tea.KeyEnter:
		chosen := p.entries[p.cursor].level
		m.effortPickerOpen = false
		m.effortPicker = nil
		return commitEffortChoice(m, chosen)
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'j':
				if p.cursor < len(p.entries)-1 {
					p.cursor++
				}
			case 'k':
				if p.cursor > 0 {
					p.cursor--
				}
			}
		}
		return m, nil
	}
	return m, nil
}

// commitEffortChoice sets the new level and rebuilds the active adapter
// so the change takes effect on the next turn — the same rebuild flow
// /model uses. Session-only: nothing is written to config.toml, mirroring
// how --reasoning-effort has always been a runtime-only setting.
func commitEffortChoice(m Model, level string) (Model, tea.Cmd) {
	m.reasoningEffort = level
	// Keep the ChatOptions snapshot in sync too: it's the template
	// refreshRouterAdapters (and any future /router rebuild) uses to
	// construct the advisor/implementer adapters, so a stale opts here
	// would re-introduce the same regression the rebuild below fixes.
	m.opts.ReasoningEffort = level
	cfg := m.adapterConfig(m.modelName, m.baseURL)
	ad := adapter.NewWithConfig(cfg)
	m.cfg.Adapter = ad
	// Also update the AgentTool's Adapter so subagents inherit the new provider
	if m.subagentTool != nil {
		m.subagentTool.Adapter = ad
	}
	m.providerProfile = ad.Profile()
	refreshRouterAdapters(&m)

	display := level
	if display == "" {
		display = "default"
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf("[effort] reasoning effort: %s", display)))
	// Warn when the chosen level won't actually do anything on this
	// model, so a no-op isn't mistaken for "applied."
	warnIfEffortNoop(&m, cfg)
	return m, nil
}

// warnIfEffortNoop re-surfaces the "no-op on this model" notice after a
// /model or /provider switch lands the session on a model that silently
// ignores the already-configured reasoning effort. Reasoning effort is a
// session-global setting that /model and /provider switches never touch
// or re-validate, so without this a user who set /effort high and then
// switched models would carry a setting that quietly stopped applying,
// with no indication it had. Mirrors the check commitEffortChoice runs
// when the level is first set.
func warnIfEffortNoop(m *Model, cfg adapter.Config) {
	if reason := adapter.EffortInapplicableReason(cfg, m.providerProfile.Provider); reason != "" {
		m.appendLine(styleMeta.Render("  (no-op on this model — " + reason + ")"))
	}
}

// normalizedEffort lowercases/trims and folds the user-facing aliases
// for "off" onto the empty level the rest of the code uses.
func normalizedEffort(level string) string {
	switch l := strings.ToLower(strings.TrimSpace(level)); l {
	case "default", "off", "none":
		return ""
	default:
		return l
	}
}

// renderEffortPicker draws the single-column overlay: a header plus one
// row per level with its short description. The active-on-open level is
// marked with ·; the cursor row gets the ❯ arrow (pinned to Success so
// it reads identically across themes, matching the theme picker).
func renderEffortPicker(p *effortPickerState) string {
	cursorArrow := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("❯ ")
	var b strings.Builder
	b.WriteString(renderMenuHeader("Reasoning effort",
		"↑↓ navigate · ↵ apply · esc cancel"))
	b.WriteString("\n\n")
	for i, e := range p.entries {
		var marker, label string
		switch {
		case i == p.cursor:
			marker = cursorArrow
			label = stylePaletteSelected.Render(padOrTruncate(e.label, 8))
		case e.level == p.current:
			marker = styleMeta.Render("· ")
			label = stylePaletteItem.Render(padOrTruncate(e.label, 8))
		default:
			marker = "  "
			label = stylePaletteItem.Render(padOrTruncate(e.label, 8))
		}
		b.WriteString(marker)
		b.WriteString(label)
		b.WriteString("  ")
		b.WriteString(styleMeta.Render(e.desc))
		b.WriteByte('\n')
	}
	if p.note != "" {
		b.WriteByte('\n')
		b.WriteString(styleMeta.Render("note: " + p.note))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
