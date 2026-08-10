package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// menu_render.go centralizes the picker overlay layout so the model
// and provider pickers share a single visual language: a title block,
// optional description, numbered (or unnumbered) item rows with a
// `❯ ` cursor prefix, an optional `✓ ` checkmark on the
// "currently-active" row, and a muted/italic style for disabled
// rows. The pattern follows Claude Code's "Select model" picker so
// users coming from there have the same affordances available.

// menuDividerWidthCap is the divider width used on any terminal wide enough
// to afford it — unchanged from the historical fixed value, so wide
// terminals render exactly as before.
const menuDividerWidthCap = 72

// menuDividerWidthFloor keeps the divider from shrinking into an unusably
// thin rule on very narrow terminals.
const menuDividerWidthFloor = 20

// menuDividerWidth is the divider width picker overlays fall back to when a
// caller doesn't pass an explicit width to renderMenuHeader. It used to be a
// fixed 72-column constant, which overflowed/wrapped on narrow terminals —
// unlike the status bar and tool cards, which already degrade gracefully
// (see renderStatus's progressive truncation, cardMinUsefulCols). Kept as a
// package var, recomputed on every WindowSizeMsg (see model.go's Update),
// rather than threading m.width through the ~30 renderMenuHeader call sites
// across every picker file — the callers that DO pass an explicit width
// (e.g. cmd_experimental.go, sessions_picker.go) are unaffected.
var menuDividerWidth = menuDividerWidthCap

// computeMenuDividerWidth clamps the picker divider to the terminal width
// (minus the same 4-column margin liveContentWidth uses for chrome), capped
// at menuDividerWidthCap so wide terminals keep the historical look, floored
// at menuDividerWidthFloor so it never collapses to nothing.
func computeMenuDividerWidth(terminalWidth int) int {
	w := terminalWidth - 4
	if w > menuDividerWidthCap {
		return menuDividerWidthCap
	}
	if w < menuDividerWidthFloor {
		return menuDividerWidthFloor
	}
	return w
}

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

	// MaxWidth is the total row budget (cursor + label + check + Desc,
	// in display cells) Desc is truncated (with an ellipsis) to fit
	// within — the row's own picker-content width, e.g. m.popupWidth().
	// Zero means "render Desc as-is, no truncation" — the pre-existing
	// behavior, kept for callers that haven't been updated yet or that
	// already bound their own Desc length before calling in.
	MaxWidth int

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
	if dividerWidth < 1 {
		dividerWidth = 1
	}
	var b strings.Builder
	b.WriteString(styleSplashTitle.Render(title))
	b.WriteString("\n")
	if description != "" {
		// wrapPlain, not a bare Style.Render — lipgloss only wraps when
		// .Width(...) is set on the style, which this one isn't, so an
		// unwrapped long description bled past the divider (and often
		// the terminal's own right edge) on every picker that passed
		// one. dividerWidth is the exact budget every other line in
		// this header is already bound to.
		b.WriteString(styleMeta.Render(wrapPlain(strings.TrimSpace(description), dividerWidth)))
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
	desc := o.Desc
	if o.MaxWidth > 0 {
		// prefix = cursor(2) + label column + gap(1) + check(2). label's
		// own display width when LabelWidth is unset (i.e. rendered
		// as-is, no padding) still has to be measured, not assumed 0.
		labelW := o.LabelWidth
		if labelW <= 0 {
			labelW = ansi.StringWidth(label)
		}
		descBudget := o.MaxWidth - 2 - labelW - 1 - 2
		desc = truncateDisplay(desc, descBudget)
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

// truncateDisplay is truncateLabel's ANSI-aware counterpart, for Desc
// strings that may already carry their own styling (e.g. the MCP server
// list's per-status color, "running" vs "failed"). ansi.Truncate cuts
// by display width rather than rune count and never breaks an escape
// sequence mid-code, so a color applied earlier in the string doesn't
// bleed past the cut or get corrupted. width<1 renders nothing rather
// than a bare ellipsis, matching truncateLabel's own floor.
func truncateDisplay(s string, width int) string {
	if width < 1 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// truncateDisplayMiddle keeps both ends of a long identifier/path visible. That
// is better for filesystem rows than suffix-only truncation because the basename
// is usually the part users recognize, while the prefix still shows scope.
func truncateDisplayMiddle(s string, width int) string {
	if width < 1 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	plain := ansi.Strip(s)
	runes := []rune(plain)
	tailWidth := min(ansi.StringWidth(plain)/2, max(width/2, 1))
	headWidth := width - tailWidth - 1
	if headWidth < 1 {
		headWidth = 1
		tailWidth = width - 2
	}
	head := ansi.Truncate(plain, headWidth, "")
	tail := displaySuffix(runes, tailWidth)
	return head + "…" + tail
}

func displaySuffix(runes []rune, width int) string {
	if width <= 0 {
		return ""
	}
	start := len(runes)
	used := 0
	for start > 0 {
		w := ansi.StringWidth(string(runes[start-1]))
		if w < 1 {
			w = 1
		}
		if used+w > width {
			break
		}
		used += w
		start--
	}
	return string(runes[start:])
}

// wrapPlain breaks s into lines no longer than width, wrapping at word
// boundaries. Naive — fine for short, unstyled description/prose
// strings (picker footnotes, mode descriptions) that don't contain runs
// of non-space punctuation longer than the column. Callers with
// already-ANSI-styled text should wrap before styling, not after.
func wrapPlain(s string, width int) string {
	if width <= 0 || runeLen(s) <= width {
		return s
	}
	var b strings.Builder
	line := 0
	for i, word := range strings.Fields(s) {
		wordW := runeLen(word)
		if i > 0 && line+1+wordW > width {
			b.WriteString("\n")
			line = 0
		} else if i > 0 {
			b.WriteString(" ")
			line++
		}
		b.WriteString(word)
		line += wordW
	}
	return b.String()
}

// runeLen is the display-width proxy used by the menu column math —
// character count rather than byte count.
func runeLen(s string) int { return utf8.RuneCountInString(s) }
