package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// planBannerInfo carries the state-derived inputs the plan-mode banner
// needs. Built fresh on every View() call from the live Model so the
// banner reflects the active plan file without caching.
//
// The banner intentionally does NOT show transient agent activity
// (thinking…, running grep, etc.) — the live thinking row above the
// cmdline already surfaces that signal. Doubling it up in the banner
// was noisy. The basename is the more useful at-a-glance info: "which
// plan am I in?".
type planBannerInfo struct {
	PlanFileBasename string // truncated basename of the resolved plan file; "" before slug is set
}

// PlanModeIcon is the glyph that marks plan mode across all surfaces
// (banner, entry log, resume log, exit log, plan-file lines, approval
// card title). Single source of truth so changing the icon is one
// edit. Triangle (matching AutoModeIcon) so the two modes share a
// shape — the color is what distinguishes them: auto is accent, plan
// is warning (orange). Saves the user from learning two glyphs.
const PlanModeIcon = "▸"

// planExitKeysHint is the "how do I leave" copy shared by the entry
// card's footer, the persistent banner, and (re-phrased as re-entry)
// the exit log — one vocabulary across every plan-mode surface.
const planExitKeysHint = "exit with /plan or Shift+Tab"

// renderPlanModeBanner is the one-line indicator rendered immediately
// above the cmdline while plan mode is active. Two states + an
// optional yolo suffix when the yolo overlay is on, with the exit
// keys riding along whenever there's room (mirrors Claude Code's
// persistent "shift+tab to cycle" indicator — mid-session, the banner
// is where the user looks to remember how to leave the mode):
//
//	◈ plan mode · i-want-to-imple…0dae3.md · exit with /plan or Shift+Tab
//	◈ plan mode · exit with /plan or Shift+Tab
//	◈ plan mode · i-want-to-imple…0dae3.md · ⚠ yolo · exit with /plan or Shift+Tab
//
// Lowercase label is intentional — quieter than shouting PLAN MODE,
// matches the surrounding "plan mode active" / "plan resumed"
// entry-log voice. The plan-file basename (truncated) is the
// persistent middle text once the slug is resolved. The exit-keys
// hint is the lowest-priority segment: first to drop on narrow
// terminals.
func renderPlanModeBanner(info planBannerInfo, yoloOn bool, width int) string {
	if width <= 0 {
		width = 80
	}
	label := stylePlanBannerLabel.Render(PlanModeIcon + " plan mode")
	dot := stylePlanBannerSep.Render(" · ")

	mid := planBannerMiddleText(info)
	core := label
	if mid != "" {
		core += dot + stylePlanBannerActivity.Render(mid)
	}
	if yoloOn {
		core += dot + renderYoloTag()
	}
	if withHint := core + dot + stylePlanBannerHint.Render(planExitKeysHint); ansi.StringWidth(withHint) <= width {
		return withHint
	}
	if ansi.StringWidth(core) <= width {
		return core
	}
	// Narrow terminal: drop the mid segment; keep label + (optional)
	// yolo so the warning never disappears.
	tight := label
	if yoloOn {
		tight += dot + renderYoloTag()
	}
	if ansi.StringWidth(tight) <= width {
		return tight
	}
	return label
}

// planBannerMiddleText is the segment after the mode label. Shows the
// plan-file basename once a slug is resolved, or "" before that —
// the empty case lets the banner render as just `◈ plan mode` while
// awaiting the first message, since the entry log right above the
// cmdline already says "describe what you'd like planned in your
// next message" and we'd otherwise repeat that hint on every redraw.
// Never returns transient agent activity — the live thinking row
// owns that signal.
func planBannerMiddleText(info planBannerInfo) string {
	return info.PlanFileBasename
}

// computePlanBannerInfo gathers the live inputs the banner needs from
// the model. The basename is truncated so a long slug (which can be
// 70+ chars after the SHA-256 hash suffix lands) doesn't push the
// banner past the terminal width.
func computePlanBannerInfo(m Model) planBannerInfo {
	info := planBannerInfo{}
	if m.cfg.PlanMode != nil && m.cfg.PlanMode.PlanFile != "" {
		info.PlanFileBasename = compactPlanBasename(m.cfg.PlanMode.PlanFile)
	}
	return info
}

// compactPlanBasename truncates a long plan file basename for the
// banner. Keeps the first ~14 chars of the slug + the 8-char hash
// suffix so two plans sharing an opening phrase stay distinguishable.
// Appends ".md" so the result reads as a filename.
func compactPlanBasename(planPath string) string {
	base := filepath.Base(planPath)
	const maxBasenameWidth = 33 // "first14…last8.md" → 14 + 1 + 8 + 3
	if len(base) <= maxBasenameWidth {
		return base
	}
	slug := strings.TrimSuffix(base, ".md")
	return truncateSlug(slug, maxBasenameWidth-3) + ".md"
}

// Entry-card body hints, one per entry path. The /plan and Shift+Tab
// paths defer the plan file to the user's NEXT message, so the card
// asks for one; the enter_plan_mode tool path derives the file from
// the message that's already in flight, so asking again would be
// wrong.
const (
	planEntryHintUserToggled    = "read-only research; describe what you'd like planned in your next message"
	planEntryHintModelRequested = "read-only research; planning the current request"
)

// renderPlanModeEntryCard renders the on-entry log entry as a
// tool-card-shaped block: header carries the ▸ icon + "plan mode
// active" label, body carries the path-specific hint (see the
// planEntryHint* consts), footer shows how to exit. Reuses
// styleCardGutter / styleCardHeader so the surface reads as part of
// the same visual family as the tool-output cards in scrollback.
func renderPlanModeEntryCard(hint string) string {
	header := styleCardGutter.Render("╭ ") +
		stylePlanBannerLabel.Render(PlanModeIcon+" plan mode active")
	body := styleCardGutter.Render("│ ") +
		stylePlanBannerHint.Render(hint)
	footer := styleCardGutter.Render("╰ ") +
		stylePlanBannerHint.Render(planExitKeysHint)
	return strings.Join([]string{header, body, footer}, "\n")
}

// renderPlanFileCard renders the plan-file-resolved log entry as a
// two-line card: header announces the resolution, footer carries the
// abbreviated path. Emitted from maybeFillPlanFile the first time a
// plan-mode session resolves its slug from the user's opening message.
func renderPlanFileCard(planPath string) string {
	header := styleCardGutter.Render("╭ ") +
		stylePlanBannerLabel.Render(PlanModeIcon+" plan file")
	footer := styleCardGutter.Render("╰ ") +
		stylePlanBannerActivity.Render(abbrevHome(planPath))
	return strings.Join([]string{header, footer}, "\n")
}

// renderPlanApprovalCard renders the decision UI for an exit_plan_mode
// approval. The plan body itself is NOT inside the box — it's emitted
// to scrollback as a quoted block when ApprovalNeeded fires (see
// handleAgentEvent), so this box only carries the four hotkeys.
// Separating body from decision keeps the box small, lets the body
// persist naturally after the user dismisses the modal, and avoids
// truncation when the plan is long.
func renderPlanApprovalCard(width int) string {
	hotkeys := stylePlanApprovalHotkey.Render("[A]") + " " +
		stylePlanApprovalChoice.Render("auto-approval") +
		"\n" +
		stylePlanApprovalHotkey.Render("[M]") + " " +
		stylePlanApprovalChoice.Render("manual approval") +
		"\n\n" +
		stylePlanApprovalHotkey.Render("[L]") + " " +
		stylePlanApprovalChoice.Render("later") +
		"\n" +
		stylePlanApprovalHotkey.Render("[K]") + " " +
		stylePlanApprovalChoice.Render("keep planning")
	return renderPlanDecisionCard(PlanModeIcon+" Approve plan?", "exit_plan_mode", hotkeys, width)
}

// renderEnterPlanApprovalCard renders the decision UI for an
// enter_plan_mode approval — the model requested plan mode and the
// user picks whether the session actually switches. Two choices only;
// deliberately no "always allow" (a derived rule would silently flip
// modes on every future request, which defeats the handshake).
func renderEnterPlanApprovalCard(width int) string {
	hotkeys := stylePlanApprovalHotkey.Render("[Y]") + " " +
		stylePlanApprovalChoice.Render("enter plan mode") +
		"\n" +
		stylePlanApprovalHotkey.Render("[N]") + " " +
		stylePlanApprovalChoice.Render("stay in current mode")
	return renderPlanDecisionCard(PlanModeIcon+" Enter plan mode?", "enter_plan_mode", hotkeys, width)
}

// renderPlanDecisionCard is the shared bordered-box builder behind the
// plan-mode decision cards: title on the top-left of the frame, tool
// name on the top-right, hotkey rows in the body. Kept generic over
// (title, toolName, hotkeys) so enter and exit render identically.
func renderPlanDecisionCard(title, toolName, hotkeys string, width int) string {
	const sideIndent = "  "
	const sideIndentW = 2
	cap := width - 4
	if cap > 120 {
		cap = 120
	}
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
	for _, line := range hardWrap(hotkeys) {
		bodyLines = append(bodyLines, sideIndent+line)
	}
	bodyLines = append(bodyLines, "")

	leftLabel := " " + stylePlanApprovalTitle.Render(title) + " "
	rightLabel := " " + stylePlanApprovalTool.Render(toolName) + " "

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
	if cap > 0 && innerW > cap {
		innerW = cap
	}

	border := lipgloss.NewStyle().Foreground(colorWarning)
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

// Plan-mode banner / approval styles. Bare declarations (no
// initializers) because the palette-colored fields would otherwise
// capture the ZERO VALUE of colorWarning / colorDim / etc. at
// package-init time — Go evaluates var initializers before any
// init() functions run, but the color vars are only populated when
// styles.go's init() calls buildStyles. The captured zero
// AdaptiveColor renders as terminal default fg, which on many
// palettes reads as a stuck bright/yellow color regardless of which
// theme the user picks. Actual style construction lives in
// buildStyles (styles.go) so every ApplyTheme swap rebuilds them
// with the current palette.
//
// stylePlanBannerActivity is the middle segment of the plan-mode
// banner — basename or the "awaiting your message" hint. Plain
// content color so it reads as neutral status text rather than a
// call-to-action; the label on the left is the only saturated
// element on the row.
var (
	stylePlanBannerLabel    lipgloss.Style
	stylePlanBannerHint     lipgloss.Style
	stylePlanBannerSep      lipgloss.Style
	stylePlanBannerActivity lipgloss.Style

	stylePlanApprovalTitle  lipgloss.Style
	stylePlanApprovalTool   lipgloss.Style
	stylePlanApprovalHotkey lipgloss.Style
	stylePlanApprovalChoice lipgloss.Style
)
