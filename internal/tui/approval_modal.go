package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// renderApprovalModal lays out the approval prompt per the Phase 5
// design: the command being approved is the visual focus (Content,
// the only bright thing), the tool name lives in the header on the
// right (Dim), and hotkeys are scannable as a grid with brackets
// first (`[Y] yes  [N] no`, `[A] always — adds Bash(go *)`).
//
//	╭─ Approval needed ───────────────────────── run_bash ─╮
//	│                                                      │
//	│   $ go run hello.go                                  │
//	│                                                      │
//	│   [Y] yes              [N] no                        │
//	│   [A] always — adds Bash(go *)                       │
//	│                                                      │
//	╰──────────────────────────────────────────────────────╯
//
// The detail about `permissions.local.json` no longer appears in the
// prompt itself — after the user picks `[A]` we emit a toast
// (`✓ Added Bash(go *) to permissions.local.json`) into scrollback.
// Keeps the prompt focused on the immediate decision.
func renderApprovalModal(m Model) string {
	body := approvalBodyFor(m)
	hotkeys := approvalHotkeyGrid(m.approvalAllowAlwaysOK, m.approvalDerivedRule)

	// Expand tabs to spaces so width measurement matches what the
	// terminal actually renders. ansi.StringWidth treats `\t` as
	// 0-width, but the terminal expands it to the next tab stop —
	// that mismatch pushes the right border out on tab-indented lines
	// (notably Go source in a write_file approval).
	body = strings.ReplaceAll(body, "\t", "    ")

	const sideIndent = "  "
	const sideIndentW = 2

	// Determine the cap (max permitted innerW) up-front so we can wrap
	// long body lines to fit. Cap == 0 means we don't know the
	// terminal width yet (test fixtures, very early frames) — fall
	// back to the original unconstrained behavior.
	cap := m.width - 4
	if cap > 120 {
		cap = 120
	}

	// hardWrap applies cap-aware soft wrapping to a multi-line block.
	// Without it, a single body line wider than the box (very long
	// markdown in a write_file approval, a 200-char URL in a run_bash
	// approval) bleeds past the right border because the row-padding
	// loop below only pads, never trims.
	hardWrap := func(s string) []string {
		lines := strings.Split(s, "\n")
		if cap <= sideIndentW {
			return lines
		}
		wrapW := cap - sideIndentW
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

	bodyLines := []string{""}
	for _, line := range hardWrap(body) {
		bodyLines = append(bodyLines, sideIndent+line)
	}
	bodyLines = append(bodyLines, "")
	for _, line := range hardWrap(hotkeys) {
		bodyLines = append(bodyLines, sideIndent+line)
	}
	bodyLines = append(bodyLines, "")

	leftLabel := " " + styleApprovalTitle.Render("Approval needed") + " "
	rightLabel := " " + styleApprovalTool.Render(m.approvalTool) + " "

	innerW := 0
	for _, line := range bodyLines {
		if w := ansi.StringWidth(line); w > innerW {
			innerW = w
		}
	}
	headW := ansi.StringWidth(leftLabel) + ansi.StringWidth(rightLabel) + 2
	if headW > innerW {
		innerW = headW
	}
	// Final cap pass — header expansion above can push innerW past
	// the cap on a small terminal with a long tool name; clamp again
	// so the box still fits.
	if cap > 0 && innerW > cap {
		innerW = cap
	}

	border := lipgloss.NewStyle().Foreground(colorWarning)
	// Top width = "╭─" + leftLabel + ─×fill + rightLabel + "─╮" =
	// leftW + rightW + fill + 4. Body/bottom rows are innerW + 4. So
	// fill = innerW - leftW - rightW keeps the top border flush with
	// the right edge of the box.
	fill := innerW - ansi.StringWidth(leftLabel) - ansi.StringWidth(rightLabel)
	if fill < 1 {
		fill = 1
	}
	top := border.Render("╭─") + leftLabel +
		border.Render(strings.Repeat("─", fill)) +
		rightLabel + border.Render("─╮")

	sideL := border.Render("│ ")
	sideR := border.Render(" │")
	rows := []string{top}
	for _, line := range bodyLines {
		pad := innerW - ansi.StringWidth(line)
		if pad < 0 {
			pad = 0
		}
		rows = append(rows, sideL+line+strings.Repeat(" ", pad)+sideR)
	}
	rows = append(rows, border.Render("╰"+strings.Repeat("─", innerW+2)+"╯"))
	return strings.Join(rows, "\n")
}

// approvalBodyFor returns the focused, Content-styled body for the
// modal — the actual command / diff / file content the user is being
// asked to approve. Per-tool customizers handle edit_file (diff),
// write_file (rendered file), and run_bash (parsed compound command);
// other tools fall back to the raw preview.
func approvalBodyFor(m Model) string {
	switch m.approvalTool {
	case "edit_file":
		if diff, ok := renderEditDiff(m.approvalArgs); ok {
			return diff
		}
	case "write_file":
		// Body (file contents) was emitted to scrollback at
		// ApprovalNeeded time so it persists past the modal and
		// doesn't cram the box. Here we render only the destination
		// path + size summary — the user already saw the body above.
		if rendered, ok := renderWriteFileApprovalSummary(m.approvalArgs); ok {
			return rendered
		}
	case "run_bash":
		if rendered, _, ok := renderRunBashApproval(m.approvalArgs, m.cwd); ok {
			return styleApprovalCommand.Render("$ ") + rendered
		}
	}
	return styleApprovalCommand.Render(m.approvalPreview)
}

// approvalHotkeyGrid composes the [Y] yes / [N] no row, with the
// optional [A] always hint on a second line when always-allow is
// available for this tool. Brackets-first formatting is faster to
// scan than `[y]es`-style mid-word brackets.
func approvalHotkeyGrid(allowAlways bool, derivedRule string) string {
	primary := strings.Join([]string{
		styleApprovalHotkey.Render("[Y]") + " " + styleApprovalChoice.Render("yes"),
		strings.Repeat(" ", 12),
		styleApprovalHotkey.Render("[N]") + " " + styleApprovalChoice.Render("no"),
	}, "")
	if !allowAlways {
		return primary
	}
	always := styleApprovalHotkey.Render("[A]") + " " +
		styleApprovalChoiceDim.Render("always — adds "+derivedRule)
	return primary + "\n" + always
}

// approvalToast formats the post-decision confirmation that lands in
// scrollback after the user picks `[A]`. Keeps the modal itself
// focused on the immediate decision and gives the user a clear
// receipt of what got persisted.
func approvalToast(rule string) string {
	return styleApprovalToast.Render(fmt.Sprintf("✓ Added %s to permissions.local.json", rule))
}

var (
	// Header label styles. Title sits left in Warning (the box is the
	// caution color); tool name sits right in Dim so the eye lands on
	// the command body.
	styleApprovalTitle     = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	styleApprovalTool      = lipgloss.NewStyle().Foreground(colorDim)
	styleApprovalCommand   = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleApprovalHotkey    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleApprovalChoice    = lipgloss.NewStyle().Foreground(colorContent)
	styleApprovalChoiceDim = lipgloss.NewStyle().Foreground(colorDim)
	styleApprovalToast     = lipgloss.NewStyle().Foreground(colorSuccess)
)
