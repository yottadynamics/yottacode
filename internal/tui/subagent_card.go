package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// Subagent styles use the canonical palette from styles.go so the
// surface reads as part of yottacode's existing visual language
// (sessions picker, plans picker, tool cards). Earlier versions used
// loud direct ANSI numbers (bright purple, neon green, vivid red);
// those have been swapped for the adaptive Accent/Success/Error/Dim
// tokens that respect the user's terminal theme.
var (
	styleSubagentLabel       = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSubagentMeta        = lipgloss.NewStyle().Foreground(colorMuted)
	styleSubagentActivity    = lipgloss.NewStyle().Foreground(colorContent)
	styleSubagentOK          = lipgloss.NewStyle().Foreground(colorSuccess)
	styleSubagentErr         = lipgloss.NewStyle().Foreground(colorError)
	styleSubagentRunning     = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSubagentCanceled    = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleSubagentTableHeader = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
)

// statusStyleFor returns the per-row color for a task's status —
// brighter for running so eye catches the live work, dim for
// terminal states, red for errors. Each branch renders the literal
// status word so callers can compose the styled label inline.
func statusStyleFor(s subagents.TaskStatus) lipgloss.Style {
	switch s {
	case subagents.TaskRunning:
		return styleSubagentRunning
	case subagents.TaskCompleted:
		return styleSubagentOK
	case subagents.TaskErrored, subagents.TaskIterCapped:
		return styleSubagentErr
	case subagents.TaskCanceled:
		return styleSubagentCanceled
	default:
		return styleSubagentMeta
	}
}

// padRight pads s with trailing spaces to width n. Used for the
// /subagents table so colored cells still align under their headers —
// lipgloss styles include ANSI escape codes that throw off fmt's
// width-counting (it counts bytes, not visible cells), so we
// pad BEFORE wrapping with the style.
func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// wrapWithHangingIndent word-wraps text so each line fits within
// lineWidth visible chars, and prefixes every continuation line
// (lines 2..N) with `indent` spaces so they hang neatly under the
// first line's start column. Used by /subagents types to keep long
// agent descriptions readable when they exceed the terminal width.
//
// Pure word-wrap on whitespace boundaries; words longer than
// lineWidth get their own line uncut (better than mid-word chops
// that fight syntax highlighting). Returns "" for empty input.
func wrapWithHangingIndent(text string, lineWidth, indent int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if lineWidth < 10 {
		lineWidth = 10
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > lineWidth {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	lines = append(lines, cur)
	if len(lines) == 1 {
		return lines[0]
	}
	pad := strings.Repeat(" ", indent)
	for i := 1; i < len(lines); i++ {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// relativeAge formats a start time as a compact "Ns ago" / "Nm ago" /
// "Nh ago" string, used in the STARTED column so the user can see
// recency without needing to do arithmetic on wall-clock timestamps.
// Times in the future (clock skew, tests) read as "now".
func relativeAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		return "now"
	}
	s := int(d.Seconds())
	switch {
	case s < 1:
		return "now"
	case s < 60:
		return fmt.Sprintf("%ds ago", s)
	case s < 3600:
		return fmt.Sprintf("%dm ago", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh ago", s/3600)
	default:
		return fmt.Sprintf("%dd ago", s/86400)
	}
}

// renderSubagentStart renders the SubagentStart event as a short
// scrollback header. The transcript path on disk is verbose
// (per-project slug + full agent name + 16-char task id) so we
// shorten it to "subagents/<basename>" — the full path lives in the
// transcript file's header itself for users who need it.
//
// Mode is always explicit: foreground runs get `[fg]`, background
// runs get `[bg]`. Earlier versions only marked background; absence
// as signal turned out to be confusing for users scanning
// scrollback.
//
// The prompt is truncated short (60 chars) so the header stays on
// one terminal line. A long prompt would wrap and visually compete
// with the tick rows below — the full prompt is in the transcript
// file's header for users who need the verbatim form.
//
// Indentation: both the transcript line and the activity ticks
// (rendered separately by renderSubagentProgress) use a 2-space
// indent so the card reads as one aligned block.
func renderSubagentStart(e agent.SubagentStart) string {
	modeBadge := "[fg]"
	if e.Background {
		modeBadge = "[bg]"
	}
	// ▸ (U+25B8 Black Right-Pointing Small Triangle) is the dedicated
	// "small" version of ▶ (U+25B6) — same right-pointing-play
	// semantic, less visual weight. Cards feel less shouty without
	// losing the icon entirely.
	tag := styleSubagentLabel.Render("▸ " + e.AgentType)
	mode := styleSubagentMeta.Render(modeBadge)
	idTag := styleSubagentMeta.Render("· " + e.TaskID[:8])
	prompt := styleSubagentActivity.Render(truncateForRender(e.Prompt, 60))
	transcript := styleSubagentMeta.Render("  " + shortTranscriptPath(e.TranscriptPath))
	return tag + " " + mode + " " + idTag + "  " + prompt + "\n" + transcript
}

// renderSubagentProgress is a one-line activity tick. The header
// already established the agent type; the tick stays minimal — just
// indent + tree bullet + the activity. Coalesced duplicates already
// land as the synthetic "  …repeated ×N" form from the runner, which
// we surface verbatim (no extra bullet so it reads as a continuation).
func renderSubagentProgress(e agent.SubagentProgress) string {
	if strings.HasPrefix(strings.TrimLeft(e.Activity, " "), "…") {
		return styleSubagentMeta.Render("  " + e.Activity)
	}
	return styleSubagentMeta.Render("  ├ " + e.Activity)
}

// renderSubagentDone marks the end of a foreground subagent. Result
// content lands in the model's context (as the tool result string) and
// is also surfaced inline so the user sees what the child concluded.
// Stats summary (duration · tool count · tokens) lives on the same
// line so the user gets a glanceable receipt without running
// /subagents list.
func renderSubagentDone(e agent.SubagentDone) string {
	status := styleSubagentOK.Render("done")
	if e.Errored {
		status = styleSubagentErr.Render("errored")
	}
	stats := formatSubagentStats(e.Duration, e.ToolCalls, e.TokensUsed)
	return styleSubagentMeta.Render("  └ ") + status + " " +
		styleSubagentMeta.Render(stats)
}

// renderSubagentBackgroundDone is the asynchronous completion banner
// for a background task. Rendered with the prominent "◉" badge so the
// user notices it landed (foreground completions have the parent
// model's next reply for context; background runs land without that
// surrounding signal). Stats summary on the header line matches the
// foreground done card.
func renderSubagentBackgroundDone(e agent.SubagentBackgroundDone) string {
	status := styleSubagentOK.Render("done")
	if e.Errored {
		status = styleSubagentErr.Render("errored")
	}
	stats := formatSubagentStats(e.Duration, e.ToolCalls, e.TokensUsed)
	header := styleSubagentLabel.Render("◉ "+e.AgentType) + " " +
		styleSubagentMeta.Render("· "+e.TaskID[:8]+"  ") +
		status + " " + styleSubagentMeta.Render(stats)
	footer := styleSubagentMeta.Render("    /subagents — open the picker, then Enter on task " + e.TaskID[:8])
	return header + "\n" + footer
}

// formatSubagentStats composes the duration / tool-count / tokens
// receipt rendered inline on every subagent completion card. Tokens
// are omitted when zero (we don't yet propagate real per-turn token
// deltas from the adapter — when that lands, this renderer will
// surface them automatically). Tool count is always shown so the
// user can see how much investigation the child did.
func formatSubagentStats(duration time.Duration, toolCalls, tokens int) string {
	parts := []string{"in " + formatDuration(duration)}
	switch toolCalls {
	case 0:
		// Don't render "0 tools" — usually means the child errored
		// before its first tool call; the status word already says
		// "errored".
	case 1:
		parts = append(parts, "1 tool")
	default:
		parts = append(parts, fmt.Sprintf("%d tools", toolCalls))
	}
	if tokens > 0 {
		parts = append(parts, formatTokens(tokens)+" tokens")
	}
	return strings.Join(parts, " · ")
}


// shortTranscriptPath collapses an absolute transcript path to a
// terminal-friendly form. We render relative to the user's home dir
// when possible ("~/…/subagents/<basename>"), so the line stays under
// ~80 cols regardless of project slug length.
func shortTranscriptPath(abs string) string {
	if abs == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(abs, home) {
		return "transcript: ~" + abs[len(home):]
	}
	return "transcript: " + filepath.Base(abs)
}

// renderSubagentList renders the table the /subagents command prints.
// Designed to be terminal-friendly — fixed-width columns, sorted
// newest-first by Registry.List, with column headers, colored
// per-row status, and per-row counts (activities executed, tokens
// when known). The Prompt column dynamically expands to consume the
// remaining width so a wide terminal shows more of the question.
func renderSubagentList(tasks []subagents.Task) string {
	if len(tasks) == 0 {
		return styleSubagentMeta.Render(
			"no subagent tasks this session — call the Agent tool to spawn one " +
				"(see /subagents types for the available subagent_type values)")
	}

	const (
		idW     = 8
		statusW = 8
		typeW   = 16
		modeW   = 4 // "fg" / "bg" + slack
		ageW    = 9
		durW    = 7
		actsW   = 5
	)
	// Padding between columns: 8 single-space gaps (one per pair of
	// adjacent cells, since we now have 8 columns).
	const fixedW = idW + statusW + typeW + modeW + ageW + durW + actsW + 8
	// 100 is a reasonable fallback; the model is rendered via
	// appendLine which doesn't get a width plumbed in (the live
	// footer owns terminal width tracking, not card renderers).
	// The width-aware list could be a follow-up — for now we pick
	// a generous wrap target that fits modern terminals.
	promptW := 120 - fixedW
	if promptW < 20 {
		promptW = 20
	}

	var b strings.Builder
	// Header line: bolded title with summary counts so the user
	// gets a glanceable status before scanning the table.
	var running, done, errored, canceled, capped int
	for _, t := range tasks {
		switch t.Status {
		case subagents.TaskRunning:
			running++
		case subagents.TaskCompleted:
			done++
		case subagents.TaskErrored:
			errored++
		case subagents.TaskCanceled:
			canceled++
		case subagents.TaskIterCapped:
			capped++
		}
	}
	summary := []string{
		fmt.Sprintf("%d total", len(tasks)),
	}
	if running > 0 {
		summary = append(summary, styleSubagentLabel.Render(fmt.Sprintf("%d running", running)))
	}
	if done > 0 {
		summary = append(summary, styleSubagentOK.Render(fmt.Sprintf("%d done", done)))
	}
	if errored > 0 {
		summary = append(summary, styleSubagentErr.Render(fmt.Sprintf("%d errored", errored)))
	}
	if canceled > 0 {
		summary = append(summary, styleSubagentMeta.Render(fmt.Sprintf("%d canceled", canceled)))
	}
	if capped > 0 {
		summary = append(summary, styleSubagentErr.Render(fmt.Sprintf("%d iter-cap", capped)))
	}
	b.WriteString(styleSubagentLabel.Render("Subagents") + "  " +
		styleSubagentMeta.Render("("+strings.Join(summary, " · ")+")") + "\n\n")

	// Column header row: bold + uppercase so it reads as a label,
	// not data. Width strings match the row format string below.
	header := fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s",
		idW, "ID",
		statusW, "STATUS",
		typeW, "AGENT",
		modeW, "MODE",
		ageW, "STARTED",
		durW, "DURATION",
		actsW, "ACTS",
		"PROMPT")
	b.WriteString(styleSubagentTableHeader.Render(header) + "\n")
	b.WriteString(styleSubagentMeta.Render("  "+strings.Repeat("─", len(header)-2)) + "\n")

	// Body rows.
	for _, t := range tasks {
		statusCell := statusStyleFor(t.Status).Render(padRight(t.Status.String(), statusW))
		typeCell := styleSubagentActivity.Render(padRight(truncateForRender(t.AgentType, typeW), typeW))
		mode := "fg"
		if t.Background {
			mode = "bg"
		}
		modeCell := styleSubagentMeta.Render(padRight(mode, modeW))
		idCell := styleSubagentLabel.Render(t.ID[:idW])
		ageCell := styleSubagentMeta.Render(padRight(relativeAge(t.Started), ageW))
		durCell := styleSubagentMeta.Render(padRight(formatDuration(t.Duration()), durW))
		actsCell := styleSubagentMeta.Render(padRight(fmt.Sprintf("%d", len(t.Activities)), actsW))
		promptCell := styleSubagentActivity.Render(truncateForRender(t.Prompt, promptW))

		b.WriteString(fmt.Sprintf("  %s  %s  %s  %s  %s  %s  %s  %s\n",
			idCell, statusCell, typeCell, modeCell, ageCell, durCell, actsCell, promptCell))
	}

	// Footer: command hints for next actions, dimmed so the table
	// itself stays the focal point.
	b.WriteString("\n")
	b.WriteString(styleSubagentMeta.Render("  /subagents — open picker  · /subagents stop <id>  · /subagents types"))
	return b.String()
}

// truncateForRender returns at most n chars with an ellipsis when
// truncated. Used for one-line previews in scrollback cards.
func truncateForRender(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

