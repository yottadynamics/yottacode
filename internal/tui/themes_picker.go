package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

// themePickerState owns the in-flight /theme overlay. Lifetime is
// one /theme invocation: opened by cmdThemes(bare), lives on the
// parent Model's themePicker pointer, dropped on Esc / Enter. The
// picker live-applies the highlighted palette as the user navigates
// — that's the preview surface (the whole TUI repaints with the
// candidate theme on every cursor move). Esc restores the theme
// that was active at open time so navigating away is non-destructive.
type themePickerState struct {
	// entries is the sorted list of registered theme names. Captured
	// at open time so a future palette registered mid-session
	// doesn't shift indexes under the cursor (unlikely, but cheap).
	entries []string

	// cursor is the highlighted row.
	cursor int

	// originalTheme is what was active when the picker opened. Used
	// by Esc to revert the live preview without committing.
	originalTheme string
}

// openThemePicker installs a fresh picker on m and live-applies the
// theme under the initial cursor so the right pane shows a preview
// immediately, without waiting for the first arrow press. The
// cursor lands on the currently-active theme so Enter is a no-op
// commit and arrowing away starts at a known reference point.
func (m *Model) openThemePicker() {
	entries := themes.Names()
	active := ActiveTheme()
	cursor := 0
	for i, n := range entries {
		if n == active {
			cursor = i
			break
		}
	}
	m.themePicker = &themePickerState{
		entries:       entries,
		cursor:        cursor,
		originalTheme: active,
	}
	m.themePickerOpen = true
	// Already on the active theme — no-op apply for clarity. The
	// cursor-move handlers re-apply on every step from here.
	ApplyTheme(entries[cursor])
	*m = refreshComponentStyles(*m)
}

// updateThemePicker handles keystrokes while the picker is foreground.
// Up/Down (and j/k) navigate; Enter commits + persists; Esc reverts
// to the original theme and closes.
func (m Model) updateThemePicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.themePicker == nil {
		m.themePickerOpen = false
		return m, nil
	}
	p := m.themePicker
	switch msg.Code {
	case tea.KeyEsc:
		// Revert the live-applied preview before tearing down. Skip
		// the write — Esc is the "I changed my mind" path, so the
		// on-disk theme should be whatever it was before /theme
		// opened.
		ApplyTheme(p.originalTheme)
		m = refreshComponentStyles(m)
		m.themePickerOpen = false
		m.themePicker = nil
		m.openSlashPalette()
		return m, nil
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
			m = applyHighlightedTheme(m)
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.entries)-1 {
			p.cursor++
			m = applyHighlightedTheme(m)
		}
		return m, nil
	case tea.KeyHome:
		p.cursor = 0
		m = applyHighlightedTheme(m)
		return m, nil
	case tea.KeyEnd:
		p.cursor = len(p.entries) - 1
		m = applyHighlightedTheme(m)
		return m, nil
	case tea.KeyEnter:
		// Commit the currently-applied theme. ApplyTheme is already
		// pointed at p.entries[p.cursor] from the last navigation
		// step (or from openThemePicker on entry), so we only need
		// to persist + close.
		chosen := p.entries[p.cursor]
		m.themePickerOpen = false
		m.themePicker = nil
		return commitThemeChoice(m, chosen)
	default:
		// Vim-style j/k for users who use them in pickers like
		// /sessions and /memory. Single-rune match — multi-char
		// filtering isn't worth the complexity for a six-row list.
		if r := []rune(msg.Text); len(r) == 1 {
			switch r[0] {
			case 'j':
				if p.cursor < len(p.entries)-1 {
					p.cursor++
					m = applyHighlightedTheme(m)
				}
			case 'k':
				if p.cursor > 0 {
					p.cursor--
					m = applyHighlightedTheme(m)
				}
			}
		}
		return m, nil
	}
}

// applyHighlightedTheme is the post-navigation hook: switch the
// global palette to the entry under the cursor, then refresh the
// styles that sub-components captured at construction time. Called
// from every cursor-moving branch in updateThemePicker.
func applyHighlightedTheme(m Model) Model {
	p := m.themePicker
	if p == nil || len(p.entries) == 0 {
		return m
	}
	ApplyTheme(p.entries[p.cursor])
	return refreshComponentStyles(m)
}

// commitThemeChoice persists the user's selection to config.toml and
// reports the result. Mirrors commitModelChoice's flow: load → mutate
// → validate → write → log. A write failure leaves the theme applied
// in-memory so the session keeps the new look even when the disk path
// is broken.
func commitThemeChoice(m Model, name string) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	cfg.Theme.Name = name
	if err := config.Validate(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[themes] validation: %v", err)))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[themes] write config: %v", err)))
		return m, nil
	}
	m.fileCfg = cfg
	m.appendLine(styleAuto.Render(fmt.Sprintf("[themes] switched to %q (persisted)", name)))
	return m, nil
}

// renderThemePicker draws the two-pane overlay: theme list on the
// left, preview block on the right. The preview is rendered using
// the *currently-applied* palette (which the cursor-move handlers
// keep in sync with p.entries[p.cursor]) so a glance at the right
// pane shows what the highlighted theme actually looks like.
//
// Width budget: left pane is a fixed 24 cols (long enough for
// "high-contrast" + a 2-col cursor indicator). The right pane takes
// whatever's left, with a 2-col separator gutter between them. On a
// terminal narrower than ~50 cols the two-pane layout degrades to a
// stacked list-only render — the preview just isn't useful at that
// size.
func renderThemePicker(p *themePickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	if width < 50 {
		return renderThemePickerNarrow(p, h)
	}
	// leftWidth covers the longest theme name ("high-contrast" = 13)
	// + the 2-col cursor marker + breathing room. 20 cols keeps the
	// list compact so the code preview on the right gets as much
	// horizontal room as the terminal can spare — the code lines in
	// renderThemePreviewPane run to ~56 cols and we don't want them
	// wrapping on a standard 80-col terminal.
	const leftWidth = 20
	const gutterWidth = 2
	rightWidth := width - leftWidth - gutterWidth - 2 // -2 for outer padding
	if rightWidth < 30 {
		rightWidth = 30
	}

	header := renderMenuHeader("Themes", "↑↓ navigate · ↵ apply + persist · esc cancel", width)
	// The list and preview panes sit side by side on the same rows —
	// h.span (column-bounded, like a tab strip) keeps a click in the
	// preview pane from resolving to a list row just because it shares
	// that row's Y. h.row (whole-row) would be wrong here. rowOffset
	// accounts for the header + blank line written ahead of the body.
	rowOffset := strings.Count(header+"\n\n", "\n")
	left := renderThemeList(p, leftWidth, h, rowOffset)
	right := renderThemePreviewPane(p, rightWidth)

	gutter := lipgloss.NewStyle().Width(gutterWidth).Render(" ")
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, gutter, right)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	b.WriteString(body)
	return strings.TrimRight(b.String(), "\n")
}

// renderThemePickerNarrow is the fallback rendering for terminals
// too thin to host the preview pane (under 50 cols). Keeps the list
// usable without packing two columns into space that can't hold
// them. The hint at the bottom points users at /theme preview if
// they want to see a sample anyway (one row at a time).
func renderThemePickerNarrow(p *themePickerState, h *pickerHits) string {
	header := renderMenuHeader("Themes", "↑↓ navigate · ↵ apply · esc cancel", 46)
	rowOffset := strings.Count(header+"\n", "\n")
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(renderThemeList(p, 24, h, rowOffset))
	b.WriteString("\n")
	b.WriteString(styleHint.Render("  (widen terminal for live preview)"))
	return strings.TrimRight(b.String(), "\n")
}

// renderThemeList renders the left pane: cursor indicator + theme
// name padded to a fixed width so the right pane's left edge stays
// stable as the cursor moves across rows of different name length.
// The active-in-config marker (·) sits before the cursor cell so
// the user sees both "what's persisted" and "what's highlighted"
// without conflict.
//
// The cursor arrow is painted in the palette's Success role (green
// across every theme) rather than Accent — Accent shifts per theme
// (cyan in terminal, blue in dimmed, lavender in catppuccin, …),
// which made the arrow inconsistent across palettes. Pinning it to
// Success keeps the "you are here" cue visually identical regardless
// of the theme being previewed.
func renderThemeList(p *themePickerState, width int, h *pickerHits, rowOffset int) string {
	cursorArrow := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("❯ ")
	// Every rendered line is exactly 2 (marker) + (width-3) (padOrTruncate)
	// = width-1 display cells wide — the actual rendered pane width,
	// used to bound clicks to this pane and not the preview beside it.
	paneWidth := width - 1
	var b strings.Builder
	for i, name := range p.entries {
		h.span(rowOffset+i, 0, paneWidth, i)
		var marker, body string
		switch {
		case i == p.cursor:
			marker = cursorArrow
			body = stylePaletteSelected.Render(padOrTruncate(name, width-3))
		case name == p.originalTheme:
			// Marker for the theme that's active on disk — helps
			// the user spot "where I am now" without it stealing
			// focus from the cursor.
			marker = styleMeta.Render("· ")
			body = stylePaletteItem.Render(padOrTruncate(name, width-3))
		default:
			marker = "  "
			body = stylePaletteItem.Render(padOrTruncate(name, width-3))
		}
		b.WriteString(marker)
		b.WriteString(body)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// padOrTruncate returns s padded to width with trailing spaces, or
// truncated with `…` when s is already too long. Local to the
// picker because the existing padRight helper (in subagent_card.go)
// pads but never truncates — different contract.
func padOrTruncate(s string, width int) string {
	if width <= 0 {
		return s
	}
	if len(s) > width {
		if width <= 1 {
			return s[:width]
		}
		return s[:width-1] + "…"
	}
	return s + strings.Repeat(" ", width-len(s))
}

// renderThemePreviewPane renders the right pane: a code-forward
// showcase that exercises every role the user is actually about
// to see in their session. The code block is the centerpiece — it's
// the surface most users spend the most time reading — so it gets
// the most vertical space, with role samples (chat headers, inline
// tokens, state markers, diff lines) framing it.
//
// Pulls the active palette's description from the registry so the
// header matches the highlighted theme even when the user's just
// navigated and the palette swap hasn't finished propagating
// through every render path.
func renderThemePreviewPane(p *themePickerState, width int) string {
	name := p.entries[p.cursor]
	pal, _ := themes.Get(name)

	var b strings.Builder

	// --- Header --------------------------------------------------
	b.WriteString(styleAssistantHeader.Render(name))
	b.WriteString("\n")
	b.WriteString(stylePaletteItem.Render(wrapPlain(pal.Description, width)))
	b.WriteString("\n\n")

	// --- Inline tokens row ---------------------------------------
	// One-line proof that the palette renders each inline role
	// distinctly — headings, code spans, paths, commands.
	b.WriteString(styleAssistantHeading.Render("Inline tokens"))
	b.WriteString("\n")
	b.WriteString("  " + styleListMarker.Render("•") + " " +
		styleInlineCode.Render("toolDeltaBoundary()") +
		styleAssistantBody.Render(" in ") +
		styleInlinePath.Render("internal/agent/stream.go"))
	b.WriteString("\n")
	b.WriteString("  " + styleListMarker.Render("•") + " run " +
		styleInlineCommand.Render("/git-commit") +
		styleAssistantBody.Render(" to ship"))
	b.WriteString("\n\n")

	// --- Code block ----------------------------------------------
	// Centerpiece — the user's day-to-day surface. A 14-line Go
	// sample with comments, a function, a struct, control flow,
	// and a string literal so every chroma token category lands.
	b.WriteString(styleAssistantHeading.Render("Code block · ") +
		styleInlineCode.Render(activeHighlightStyleName()))
	b.WriteString("\n")
	sample := "" +
		"// coalesceToolDeltas folds adjacent SSE tool-call frames\n" +
		"// into a single ToolCall so the transcript renders one card\n" +
		"// per tool invocation, not one per delta chunk.\n" +
		"func coalesceToolDeltas(in <-chan Frame) <-chan ToolCall {\n" +
		"    out := make(chan ToolCall)\n" +
		"    go func() {\n" +
		"        defer close(out)\n" +
		"        var acc ToolCall\n" +
		"        for f := range in {\n" +
		"            if f.Role != RoleTool && acc.ID != \"\" {\n" +
		"                out <- acc\n" +
		"                acc = ToolCall{}\n" +
		"            }\n" +
		"            acc.MergeFrom(f)\n" +
		"        }\n" +
		"    }()\n" +
		"    return out\n" +
		"}"
	highlighted := strings.TrimRight(HighlightFromPath(sample, "preview.go"), "\n")
	for _, line := range strings.Split(highlighted, "\n") {
		b.WriteString(styleCodeBlock.Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// --- Diff fragment -------------------------------------------
	b.WriteString(styleAssistantHeading.Render("Diff"))
	b.WriteString("\n")
	b.WriteString("  " + styleDiffDel.Render("- log.Printf(\"tool %s emitted\", name)"))
	b.WriteString("\n")
	b.WriteString("  " + styleDiffAdd.Render("+ slog.Info(\"tool emitted\", \"name\", name)"))
	b.WriteString("\n\n")

	// --- State markers + thinking --------------------------------
	b.WriteString(styleAssistantHeading.Render("Status"))
	b.WriteString("\n")
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(colorSuccess).Render("● ok "))
	b.WriteString(lipgloss.NewStyle().Foreground(colorWarning).Render("● warn "))
	b.WriteString(lipgloss.NewStyle().Foreground(colorError).Render("● err "))
	b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render("● dim "))
	b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Render("● accent"))
	b.WriteString("\n")
	b.WriteString("  " + styleToolCall.Render("▸ run_bash(\"go test ./...\")"))
	b.WriteString("\n")
	b.WriteString("  " + styleToolMeta.Render("(streamed 2.3s · 42 lines)"))
	b.WriteString("\n")
	b.WriteString("  " + styleThinking.Render("thinking · re-checking the coalesce boundary…"))
	return strings.TrimRight(b.String(), "\n")
}

