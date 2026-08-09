package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderApprovalModal lays out the approval prompt per the Phase 5
// design: the command being approved is the visual focus (Content,
// the only bright thing), the tool name lives in the header on the
// right (Dim), and hotkeys are scannable as a grid with brackets
// first (`[Y] yes  [N] no`, `[A] always — adds Bash(go *)`).
//
//	┌─ Approval needed ───────────────────────── run_bash ─┐
//	│                                                      │
//	│   $ go run hello.go                                  │
//	│                                                      │
//	│   [Y] yes              [N] no                        │
//	│   [A] always — adds Bash(go *)                       │
//	│   [D] never  — adds Bash(curl *)                     │
//	│                                                      │
//	└──────────────────────────────────────────────────────┘
//
// The detail about `permissions.local.json` no longer appears in the
// prompt itself — after the user picks `[A]` we emit a toast
// (`✓ Added Bash(go *) to permissions.local.json`) into scrollback.
// Keeps the prompt focused on the immediate decision.
func renderApprovalModal(m Model) string {
	body := approvalBodyFor(m)
	hotkeys := approvalHotkeyGrid(m.approvalAllowAlwaysOK, m.approvalDerivedRule, m.approvalDenyAlwaysOK, m.approvalDerivedDenyRule)

	// Expand tabs to spaces so width measurement matches what the
	// terminal actually renders. ansi.StringWidth treats `\t` as
	// 0-width, but the terminal expands it to the next tab stop —
	// that mismatch pushes the right border out on tab-indented lines
	// (notably Go source in a write_file approval).
	body = strings.ReplaceAll(body, "\t", "    ")

	// capW == 0 means we don't know the terminal width yet (test
	// fixtures, very early frames) — hardWrapLabeled/renderLabeledBox
	// both fall back to the original unconstrained behavior in that
	// case. Wrapping matters because a single body line wider than the
	// box (very long markdown in a write_file approval, a 200-char URL
	// in a run_bash approval) would otherwise bleed past the right
	// border — the row-padding loop in renderLabeledBox only pads,
	// never trims.
	capW := capLabeledBoxWidth(m.width)

	bodyLines := []string{""}
	for _, line := range hardWrapLabeled(body, capW) {
		bodyLines = append(bodyLines, labeledBoxIndent+line)
	}
	bodyLines = append(bodyLines, "")
	for _, line := range hardWrapLabeled(hotkeys, capW) {
		bodyLines = append(bodyLines, labeledBoxIndent+line)
	}
	bodyLines = append(bodyLines, "")

	leftLabel := " " + styleApprovalTitle.Render("Approval needed") + " "
	rightLabel := " " + styleApprovalTool.Render(m.approvalTool) + " "

	return renderLabeledBox(leftLabel, rightLabel, bodyLines, capW, colorWarning)
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
func approvalHotkeyGrid(allowAlways bool, derivedRule string, denyAlways bool, denyRule string) string {
	primary := strings.Join([]string{
		styleApprovalHotkey.Render("[Y]") + " " + styleApprovalChoice.Render("yes"),
		strings.Repeat(" ", 6),
		styleApprovalHotkey.Render("[N]") + " " + styleApprovalChoice.Render("no"),
	}, "")
	lines := []string{primary}
	if allowAlways {
		lines = append(lines, styleApprovalHotkey.Render("[A]")+" "+
			styleApprovalChoiceDim.Render("always — adds "+derivedRule))
	}
	if denyAlways {
		lines = append(lines, styleApprovalHotkey.Render("[D]")+" "+
			styleApprovalChoiceDim.Render("never — adds "+denyRule))
	}
	return strings.Join(lines, "\n")
}

// approvalToast formats the post-decision confirmation that lands in
// scrollback after the user picks `[A]`. Keeps the modal itself
// focused on the immediate decision and gives the user a clear
// receipt of what got persisted.
func approvalToast(rule string) string {
	return styleApprovalToast.Render(fmt.Sprintf("✓ Added %s to permissions.local.json", rule))
}

// approvalDenyToast is approvalToast's mirror for the "[D] never" path —
// the receipt for a persisted block rule.
func approvalDenyToast(rule string) string {
	return styleApprovalToast.Render(fmt.Sprintf("✓ Blocked %s — saved to permissions.local.json", rule))
}

// Approval-modal styles. Bare declarations — see plan_mode_render.go's
// note: initializers would capture the zero AdaptiveColor at package
// init, freezing the modal at default-fg regardless of theme. Actual
// construction lives in buildStyles (styles.go).
//
// Design: title sits left in Warning (the box is the caution color);
// tool name sits right in Dim so the eye lands on the command body.
var (
	styleApprovalTitle     lipgloss.Style
	styleApprovalTool      lipgloss.Style
	styleApprovalCommand   lipgloss.Style
	styleApprovalHotkey    lipgloss.Style
	styleApprovalChoice    lipgloss.Style
	styleApprovalChoiceDim lipgloss.Style
	styleApprovalToast     lipgloss.Style
)
