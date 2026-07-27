package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/yottadynamics/yottacode/internal/adapter"
)

// renderStartupBox returns the Codex-style identity card printed once at
// program start. Auto-sizes to the longest line.
//
// version is the semver string (e.g. "0.1.0"); commit is the 7-char short
// SHA the binary was built from, or "" when unknown (go run, tarball
// build, -buildvcs=false). dirty is true when the build had uncommitted
// changes — surfaced as a trailing "*" so users can tell a local hot-fix
// build apart from a clean release artifact at a glance.
//
// tip is an optional dim footer line shown inside the card on fresh
// sessions (caller picks one via pickTip). Empty string omits the
// footer — used on resumed sessions where the user is past the
// onboarding moment.
//
// termWidth is the current terminal width; rows wider than the
// available inner space (termWidth - 4 for border + padding) are
// wrapped with ansi.Wrap so the bordered box stays inside the terminal
// on resize. termWidth <= 0 disables wrapping (early frames before the
// first WindowSizeMsg, test fixtures).
func renderStartupBox(version, commit string, dirty bool, modelName, dir, sessionID, branch, memorySummary string, profile adapter.ProviderProfile, tip string, termWidth int) string {
	leftLabel := " " + styleSplashTitle.Render(">_ YottaCode") + " " + styleSplashLabel.Render(buildLabel(version, commit, dirty))

	items := []startupInfoRow{
		{Key: "model", Value: modelName},
		{Key: "provider", Value: renderProviderSummary(profile)},
		{Key: "directory", Value: abbrevHome(dir)},
	}
	// The id you'd pass to `sessions resume`. Shown up front so it's
	// available mid-session — the exit hint prints it too, but only once
	// you're already leaving, and only for sessions that reached a turn.
	// Sits above the optional tools/context rows so its position is stable
	// whether or not those render.
	if sessionID != "" {
		items = append(items, startupInfoRow{Key: "session", Value: sessionID})
	}
	if tools := renderBuiltinTools(profile); tools != "" {
		items = append(items, startupInfoRow{Key: "tools", Value: tools})
	}
	if ctx := contextLine(branch, memorySummary); ctx != "" {
		items = append(items, startupInfoRow{Key: "context", Value: ctx})
	}
	rows := []string{""}
	rows = append(rows, renderStartupRows(items)...)
	if tip != "" {
		rows = append(rows, "", styleSplashLabel.Render("tip:")+" "+renderInlineCodeSpans(tip, styleSplashLabel))
	}

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

	return renderStartupFrame(leftLabel, rows, termWidth)
}

// renderStartupFrame draws the startup card with title metadata embedded in
// the top border, matching the LSP advisory card's shape while honoring the
// cmdline's full-width frame when the terminal width is known.
func renderStartupFrame(leftLabel string, bodyLines []string, termWidth int) string {
	innerW := 0
	for _, line := range bodyLines {
		if w := ansi.StringWidth(line); w > innerW {
			innerW = w
		}
	}

	// The top border needs at least one rule cell after the embedded label.
	headW := ansi.StringWidth(leftLabel) + 1
	if headW > innerW {
		innerW = headW
	}
	if termWidth > 0 {
		// Outer width is inner content plus both borders and one padding cell on
		// each side. Use the cmdline width when possible, but never clip labels.
		if w := termWidth - 4; w > innerW {
			innerW = w
		}
	}

	border := lipgloss.NewStyle().Foreground(colorDim)
	fill := innerW - ansi.StringWidth(leftLabel)
	if fill < 1 {
		fill = 1
	}
	top := border.Render("┌─") + leftLabel + border.Render(strings.Repeat("─", fill+1)+"┐")

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

// buildLabel composes the version + commit fragment shown in the
// startup card title. Falls back to a bare "vX.Y.Z" when no commit is
// known (go run, tarball build, -buildvcs=false) so we don't render
// confusing empty parens like "v0.1.0 ()".
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

// contextLine joins branch + memory summary into one
// "branch=foo · mem=USER+YOTTA+UMEM" line, or returns "" when nothing
// is set. The agent now manages memory in-band via memory_save /
// memory_forget, so there's no separate background worker state to
// surface here.
func contextLine(branch, memorySummary string) string {
	var parts []string
	if branch != "" {
		parts = append(parts, "branch="+branch)
	}
	if memorySummary != "" {
		parts = append(parts, "mem="+memorySummary)
	}
	return strings.Join(parts, " · ")
}

type startupInfoRow struct {
	Key   string
	Value string
}

func renderStartupRows(rows []startupInfoRow) []string {
	maxKey := 0
	for _, row := range rows {
		if len(row.Key) > maxKey {
			maxKey = len(row.Key)
		}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, labelRow(row.Key, maxKey, row.Value))
	}
	return out
}

// labelRow renders one "label  value" pair. No colon — alignment alone
// communicates the relationship and reads cleaner. The label is
// right-padded to keyWidth so values line up in a consistent column;
// a single trailing space separates the (padded) label from the value.
func labelRow(key string, keyWidth int, value string) string {
	label := fmt.Sprintf("%-*s", keyWidth, key)
	return styleSplashLabel.Render(label) + "  " + value
}

func renderProviderSummary(profile adapter.ProviderProfile) string {
	name := string(profile.Provider)
	if name == "" {
		name = string(adapter.ProviderOpenAICompatible)
	}
	if len(profile.Issues) > 0 {
		name += " !"
	} else if len(profile.Warnings) > 0 {
		name += " ?"
	}
	if profile.UsesResponsesAPI {
		return styleSplashLabel.Render(name + " · responses")
	}
	return styleSplashLabel.Render(name)
}

func renderBuiltinTools(profile adapter.ProviderProfile) string {
	if len(profile.EnabledBuiltinTools) == 0 {
		return ""
	}
	parts := make([]string, 0, len(profile.EnabledBuiltinTools))
	for _, t := range profile.EnabledBuiltinTools {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, " + ")
}
