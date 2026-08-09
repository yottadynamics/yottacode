package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
)

// labeledBoxCap is the shared upper bound for the labeled-box family —
// the approval modal, plan-mode decision cards, and the LSP advisory
// card all draw "┌─ Title ──────── context ─┐" frames and previously
// hand-rolled their own copy of this same measure/wrap/pad math. No
// terminal is wide enough that one of these boxes should stretch past
// this regardless of live width.
const labeledBoxCap = 120

// labeledBoxIndent is the left padding callers prepend to each wrapped
// body line before framing; labeledBoxIndentW is its width.
const labeledBoxIndent = "  "
const labeledBoxIndentW = 2

// capLabeledBoxWidth derives the wrap cap for a labeled box from the
// live terminal width. Returns 0 ("uncapped — fall back to
// unconstrained rendering") when termWidth is unknown, e.g. a frame
// rendered before the first WindowSizeMsg; otherwise min(termWidth-4,
// labeledBoxCap).
func capLabeledBoxWidth(termWidth int) int {
	if termWidth <= 0 {
		return 0
	}
	return min(termWidth-4, labeledBoxCap)
}

// hardWrapLabeled applies cap-aware wrapping to a (possibly multi-line,
// possibly already ANSI-styled) text block. capW is the box's total
// interior budget; wrapping targets capW-labeledBoxIndentW since every
// caller prepends labeledBoxIndent to each returned line before
// framing. capW<=0 or capW<=labeledBoxIndentW means "don't wrap" —
// used for the early-frame / unknown-width case.
func hardWrapLabeled(s string, capW int) []string {
	lines := strings.Split(s, "\n")
	if capW <= labeledBoxIndentW {
		return lines
	}
	wrapW := capW - labeledBoxIndentW
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if ansi.StringWidth(line) <= wrapW {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(line, wrapW, true), "\n")...)
	}
	return out
}

// renderLabeledBox draws a bordered box with a label in the left of
// the top border and a second label on the right, e.g.:
//
//	┌─ Approval needed ───────────────────────── run_bash ─┐
//	│                                                       │
//	│   $ go run hello.go                                   │
//	│                                                       │
//	└───────────────────────────────────────────────────────┘
//
// leftLabel/rightLabel arrive pre-styled and pre-bracketed with their
// own leading/trailing space. bodyLines are the interior rows, also
// pre-styled/pre-indented — this function only measures, pads, and
// frames them; wrapping long lines is the caller's job via
// hardWrapLabeled before calling in. capW re-clamps the measured inner
// width so a very long label (long tool name, long language count)
// can't stretch the box past the same budget the body was wrapped to;
// pass 0 to skip clamping.
func renderLabeledBox(leftLabel, rightLabel string, bodyLines []string, capW int, borderColor compat.AdaptiveColor) string {
	innerW := 0
	for _, line := range bodyLines {
		if w := ansi.StringWidth(line); w > innerW {
			innerW = w
		}
	}
	innerW = max(innerW, ansi.StringWidth(leftLabel)+ansi.StringWidth(rightLabel)+2)
	if capW > 0 && innerW > capW {
		innerW = capW
	}

	border := lipgloss.NewStyle().Foreground(borderColor)
	// Top width = "┌─" + leftLabel + ─×fill + rightLabel + "─┐" =
	// leftW + rightW + fill + 4. Body/bottom rows are innerW + 4. So
	// fill = innerW - leftW - rightW keeps the top border flush with
	// the right edge of the box.
	fill := max(innerW-ansi.StringWidth(leftLabel)-ansi.StringWidth(rightLabel), 1)
	top := border.Render("┌─") + leftLabel +
		border.Render(strings.Repeat("─", fill)) +
		rightLabel + border.Render("─┐")

	sideL := border.Render("│ ")
	sideR := border.Render(" │")
	rows := []string{top}
	for _, line := range bodyLines {
		pad := max(innerW-ansi.StringWidth(line), 0)
		rows = append(rows, sideL+line+strings.Repeat(" ", pad)+sideR)
	}
	rows = append(rows, border.Render("└"+strings.Repeat("─", innerW+2)+"┘"))
	return strings.Join(rows, "\n")
}
