package tui

import "charm.land/lipgloss/v2"

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
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(0, 1).
		Render(body)
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
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	x := max((m.width-bw)/2, 0)
	y := max((m.height-bh)/2, 0)
	bg := lipgloss.NewLayer(background).Z(0)
	fg := lipgloss.NewLayer(box).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(bg, fg).Render()
}
