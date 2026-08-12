package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderStartupBox returns the launch welcome card shown before the first
// conversation turn. Runtime state (model, branch, memory, session id) lives in
// the cmdline/status bar; this card stays focused on orientation and action.
func renderStartupBox(version, commit string, dirty bool, args ...any) string {
	tip, selected, termWidth := startupBoxArgs(args...)
	innerW := welcomeInnerWidth(termWidth)
	rows := []string{
		"",
		"   " + styleSplashTitle.Render(">_ yottacode") + " " + styleSplashLabel.Render(buildLabel(version, commit, dirty)),
		"",
		"   " + styleSplashInfo.Render("Welcome. What are we building today?"),
		"",
	}
	selected = clampWelcomeCursor(selected)
	for i, item := range welcomeActions() {
		rows = append(rows, welcomeActionRow(item, i == selected, innerW))
	}
	if tip != "" {
		rows = append(rows, "", "   "+styleSplashLabel.Render("Tip  ")+renderInlineCodeSpans(tip, styleSplashLabel))
	}
	rows = append(rows, "")

	if cap := termWidth - 4; cap > 0 {
		wrapped := make([]string, 0, len(rows))
		for _, row := range rows {
			if ansi.StringWidth(row) <= cap {
				wrapped = append(wrapped, row)
				continue
			}
			wrapped = append(wrapped, strings.Split(ansi.Wrap(row, cap, " "), "\n")...)
		}
		rows = wrapped
	}
	return renderStartupFrame(rows, termWidth)
}

func startupBoxArgs(args ...any) (tip string, selected int, termWidth int) {
	if len(args) == 3 {
		tip, _ = args[0].(string)
		selected, _ = args[1].(int)
		termWidth, _ = args[2].(int)
		return tip, selected, termWidth
	}
	// Backward-compatible shape for tests and older call sites:
	// model, dir, sessionID, branch, memorySummary, profile, tip, termWidth.
	if len(args) == 8 {
		tip, _ = args[6].(string)
		termWidth, _ = args[7].(int)
	}
	return tip, 0, termWidth
}

func welcomeInnerWidth(termWidth int) int {
	if termWidth > 0 {
		return max(termWidth-4, 40)
	}
	return 76
}

// renderStartupFrame draws the startup card using the cmdline width when known
// so the welcome card and bottom input frame read as paired chrome.
func renderStartupFrame(bodyLines []string, termWidth int) string {
	innerW := 0
	for _, line := range bodyLines {
		if w := ansi.StringWidth(line); w > innerW {
			innerW = w
		}
	}
	if termWidth > 0 {
		if w := termWidth - 4; w > innerW {
			innerW = w
		}
	}

	border := lipgloss.NewStyle().Foreground(colorDim)
	top := border.Render("┌" + strings.Repeat("─", innerW+2) + "┐")
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
	rows = append(rows, border.Render("└"+strings.Repeat("─", innerW+2)+"┘"))
	return strings.Join(rows, "\n")
}

// buildLabel composes the version + commit fragment shown in the startup card.
// Falls back to a bare "vX.Y.Z" when no commit is known (go run, tarball build,
// -buildvcs=false) so we don't render confusing empty parentheses.
func buildLabel(version, commit string, dirty bool) string {
	if commit == "" {
		return "v" + version
	}
	suffix := commit
	if dirty {
		suffix += "*"
	}
	return "v" + version + " (" + suffix + ")"
}
