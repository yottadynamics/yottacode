package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// menu_render.go centralizes the picker overlay layout so the model
// and provider pickers share a single visual language: a title block,
// optional description, numbered (or unnumbered) item rows with a
// `❯ ` cursor prefix, an optional `✓ ` checkmark on the
// "currently-active" row, and a muted/italic style for disabled
// rows. The pattern follows Claude Code's "Select model" picker so
// users coming from there have the same affordances available.

const (
	// menuDividerWidth spans ordinary submenu boxes without wrapping on narrow terminals.
	menuDividerWidth = 72
)

// menuItemOpts customizes one row in a picker. Build a slice of
// these and pass them through renderMenuItem to get consistent
// alignment + cursor/check rendering.
type menuItemOpts struct {
	// Number is the 1-based position shown before the label.
	// Zero means "no number" — useful for long lists where
	// numbers would be noise rather than affordance.
	Number int

	// Label is the short identifier in the left column.
	Label string

	// LabelWidth is the column width to pad/truncate Label to.
	// Zero means "render as-is, no padding/truncation."
	LabelWidth int

	// Desc is the right-column description text. Truncated
	// implicitly by terminal wrap.
	Desc string

	// Cursor adds the `❯ ` prefix and bolds the row in the
	// brand color.
	Cursor bool

	// Checked adds a `✓ ` marker between the label and the
	// description, used to mark the row that's currently
	// in-effect (active provider, current default model, etc.).
	Checked bool

	// Disabled renders the row in a muted/italic style so users
	// see it's there but can tell it isn't selectable. Cursor
	// rows on a disabled item add an underline so the navigation
	// cue stays visible.
	Disabled bool
}

// renderMenuHeader draws the picker's top divider, title, optional description,
// and matching bottom divider. The rules visually separate submenu chrome from
// surrounding terminal content and rows while keeping all slash-command submenus
// on one shared layout path. Callers that know the live overlay width pass it so
// the header rules align with the prompt separator below the menu.
func renderMenuHeader(title, description string, widths ...int) string {
	dividerWidth := menuDividerWidth
	if len(widths) > 0 && widths[0] > 0 {
		dividerWidth = widths[0]
	}
	var b strings.Builder
	b.WriteString(renderMenuDivider(dividerWidth))
	b.WriteString("\n")
	b.WriteString(styleSplashTitle.Render(title))
	b.WriteString("\n")
	if description != "" {
		// Wrap description at ~80 cols for readability when the
		// terminal is wide. Lipgloss handles this for us via the
		// styled writer; we just trim trailing whitespace.
		b.WriteString(styleMeta.Render(strings.TrimSpace(description)))
		b.WriteString("\n")
	}
	b.WriteString(renderMenuDivider(dividerWidth))
	b.WriteString("\n")
	return b.String()
}

// renderMenuDivider returns a single horizontal rule sized for the current
// menu body. Width is in display cells, not bytes; callers pass a conservative
// content width so the line reaches toward the box edge without forcing wraps.
func renderMenuDivider(width int) string {
	if width < 1 {
		width = 1
	}
	return styleOverlayRule.Render(strings.Repeat("─", width))
}

// renderMenuItem returns one formatted picker row. The layout:
//
//	❯ claude-sonnet-4-6        ✓ balanced · ctx=200k
//	  claude-haiku-4-5            cheap · ctx=200k
//
// Cursor and check markers reserve their column whether or not
// they're present, so unchecked rows align with checked ones and
// non-cursor rows align with cursor rows. Active rows use the brand
// green (colorSuccess), not Accent — matching the input frame and the
// user-message bar's chevron, which never renders Accent either
// (maintainer call: blue/cyan reads wrong on prompt-adjacent chrome).
//
// The `Number` field on menuItemOpts is preserved for back-compat
// but no longer rendered — arrow-key navigation is enough, and the
// numbers were redundant noise. If number-key shortcuts come back,
// render the number right-aligned in Dim (per the Phase 5 spec).
func renderMenuItem(o menuItemOpts) string {
	cursor := "  "
	if o.Cursor {
		cursor = "❯ "
	}
	check := "  "
	if o.Checked {
		check = "✓ "
	}
	label := o.Label
	if o.LabelWidth > 0 {
		label = truncateLabel(label, o.LabelWidth)
		label = fmt.Sprintf("%-*s", o.LabelWidth, label)
	}
	body := cursor + label + " " + check + o.Desc
	switch {
	case o.Disabled && o.Cursor:
		// Cursor on a disabled row: muted + italic + underline so
		// users can still tell where their cursor sits even though
		// the row is inactive. Selecting it is a no-op handled by
		// the parent dispatcher.
		return lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Underline(true).Render(body)
	case o.Disabled:
		return styleEmpty.Render(body)
	case o.Cursor:
		return lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render(body)
	default:
		return stylePaletteItem.Render(body)
	}
}

// truncateLabel cuts a string to max characters with a trailing `…`
// when it overflows. Used for model names that exceed the column
// budget (e.g. "nvidia/llama-3.1-nemotron-safety-guard-8b-v3" is
// 46 chars and would otherwise push the description column out of
// alignment).
// Counts runes, not bytes: labels and session gists are arbitrary user text,
// and slicing mid-rune emits invalid UTF-8 that renders as garbage.
func truncateLabel(s string, max int) string {
	if max < 1 {
		return ""
	}
	if runeLen(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

// runeLen is the display-width proxy used by the menu column math —
// character count rather than byte count.
func runeLen(s string) int { return utf8.RuneCountInString(s) }
