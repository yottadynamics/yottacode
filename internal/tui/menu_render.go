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

	// Desc is the right-column description text.
	Desc string

	// DescWidth caps Desc before rendering. Zero uses the shared default cap so
	// long right-side text never grows an overlay past the terminal edge.
	DescWidth int

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

// renderMenuHeader draws the picker's title + optional description
// block, followed by a divider. Title is bold/branded; description is muted.
// Caller appends a blank line + items below.
func renderMenuHeader(title, description string) string {
	var b strings.Builder
	b.WriteString(styleSplashTitle.Render(title))
	b.WriteString("\n")
	if description != "" {
		b.WriteString(styleMeta.Render(truncateLabel(strings.TrimSpace(description), 88)))
		b.WriteString("\n")
	}
	b.WriteString(styleOverlayRule.Render(strings.Repeat("─", 120)))
	b.WriteString("\n")
	return b.String()
}

// renderMenuItem returns one formatted picker row. The layout:
//
//	❯ claude-sonnet-4-6        ✓ balanced · ctx=200k
//	  claude-haiku-4-5            cheap · ctx=200k
//
// Cursor and check markers reserve their column whether or not
// they're present, so unchecked rows align with checked ones and
// non-cursor rows align with cursor rows. Active rows use Accent
// (cyan) so they match the input frame and the user-message bar.
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
	desc := o.Desc
	descWidth := o.DescWidth
	if descWidth > 0 {
		desc = shortenMiddle(desc, descWidth)
	}
	label := o.Label
	if o.LabelWidth > 0 {
		label = truncateLabel(label, o.LabelWidth)
		label = fmt.Sprintf("%-*s", o.LabelWidth, label)
	}
	body := cursor + label + " " + check + desc
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
