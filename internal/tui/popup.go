package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// popupMaxWidth caps a centered popup's content width well short of the
// terminal's full width, unlike the old renderInlineOverlay's
// inlineOverlayWidth (terminal width minus a 1-cell gutter, since that
// filled the whole footer) — a floating card should read as a card on a
// wide terminal, not stretch edge to edge.
const popupMaxWidth = 100

// popupWidth is the content width budget for a centered popup body.
func (m Model) popupWidth() int {
	w := m.width - 8 // border + padding on both sides, plus a margin
	if w > popupMaxWidth {
		w = popupMaxWidth
	}
	if w < 20 && m.width-4 > w {
		w = m.width - 4
	}
	if w < 1 {
		w = 1
	}
	return w
}

// popupBox wraps a borderless picker/panel body (render*Picker output,
// the cheatsheet, /usage, /help, ...) in the shared rounded popup
// border. Deliberately different from renderInputFrame's sharp corners
// (model.go) and from the labeled-box family's sharp corners
// (labeled_box.go, startup.go, used by approval/path-trust/plan-approval
// decision cards) — floating windows read as "lifted" cards distinct
// from the structural, always-there cmdline chrome. Decision cards that
// already self-border via renderLabeledBox are NOT passed through this —
// they're already a complete framed box; wrapping them again would read
// as "a modal floating on a modal."
func popupBox(body string) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(0, 1).
		Render(body)
	return addPopupCloseGlyph(box)
}

// addPopupCloseGlyph paints a small close affordance into the popup border. The
// mouse handler treats the same top-right cells as an Esc click, so users who
// discover mouse scrolling/clicking also get an obvious way to dismiss panels.
func addPopupCloseGlyph(box string) string {
	lines := strings.Split(box, "\n")
	if len(lines) == 0 {
		return box
	}
	width := lipgloss.Width(lines[0])
	if width < 6 {
		return box
	}
	plainTop := ansi.Strip(lines[0])
	if runeLen(plainTop) != width {
		return box
	}
	left, right := "╭", "╮"
	switch {
	case strings.HasPrefix(plainTop, "╭") && strings.HasSuffix(plainTop, "╮"):
		// Keep the shared rounded-popup chrome for picker/menu surfaces.
	case strings.HasPrefix(plainTop, "┌") && strings.HasSuffix(plainTop, "┐"):
		left, right = "┌", "┐"
	default:
		return box
	}
	// Rebuild the top border instead of splicing into ANSI-styled bytes. The
	// remaining border/body lines keep their original lipgloss styling.
	closeGlyph := lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render("×")
	lines[0] = left + strings.Repeat("─", width-4) + " " + closeGlyph + right
	return strings.Join(lines, "\n")
}

// popupOrigin returns the screen (x,y) composePopup places box at — the
// single formula both the compositor and every mouse click handler
// (mouse.go) use, so a click handler's idea of "where the popup is" can
// never drift from what was actually drawn.
func (m Model) popupOrigin(box string) (x, y int) {
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	return max((m.width-bw)/2, 0), max((m.height-bh)/2, 0)
}

// composePopup centers an already-framed box (either via popupBox for
// plain bodies, or a self-bordered renderLabeledBox render for decision
// modals) over background, using lipgloss's layer compositor.
// Layer.Draw clears its own bounds before painting, so the popup box
// hard-occludes whatever background content sits beneath it at that
// position — the compositing primitive true floating windows need, in
// place of the old renderInlineOverlay's "replace the whole footer"
// approach.
func (m Model) composePopup(background, box string) string {
	x, y := m.popupOrigin(box)
	bg := lipgloss.NewLayer(background).Z(0)
	fg := lipgloss.NewLayer(box).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(bg, fg).Render()
}
