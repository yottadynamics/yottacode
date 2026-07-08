package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// Subagent styles use the canonical palette from styles.go so the
// surface reads as part of yottacode's existing visual language
// (sessions picker, plans picker, tool cards). Bare declarations —
// initializers would capture the zero AdaptiveColor at package
// init; the actual styles are built inside buildStyles (styles.go)
// so theme swaps via ApplyTheme rebuild them correctly.
var (
	styleSubagentLabel       lipgloss.Style
	styleSubagentMeta        lipgloss.Style
	styleSubagentActivity    lipgloss.Style
	styleSubagentOK          lipgloss.Style
	styleSubagentErr         lipgloss.Style
	styleSubagentRunning     lipgloss.Style
	styleSubagentTableHeader lipgloss.Style
)

// padRight pads s with trailing spaces to width n. Used for the
// /subagents table so colored cells still align under their headers —
// lipgloss styles include ANSI escape codes that throw off fmt's
// width-counting (it counts bytes, not visible cells), so we
// pad BEFORE wrapping with the style.
func padRight(s string, n int) string {
	w := ansi.StringWidth(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
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
	header := tag + " " + mode + " " + idTag
	if e.Branch != "" {
		// Dispatch worktree task: show the branch it commits to.
		header += " " + styleSubagentMeta.Render("· "+shortBranch(e.Branch))
	}
	header += "  " + prompt
	return header + "\n" + transcript
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
	stats := formatSubagentStats(e.Duration, e.ToolCalls, e.TokensUsed, e.Model)
	line := styleSubagentMeta.Render("  └ ") + status + " " +
		styleSubagentMeta.Render(stats)
	if e.Branch != "" {
		line += styleSubagentMeta.Render(" · " + shortBranch(e.Branch))
	}
	return line
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
	stats := formatSubagentStats(e.Duration, e.ToolCalls, e.TokensUsed, e.Model)
	header := styleSubagentLabel.Render("◉ "+e.AgentType) + " " +
		styleSubagentMeta.Render("· "+e.TaskID[:8]+"  ") +
		status + " " + styleSubagentMeta.Render(stats)
	if e.Branch != "" {
		header += styleSubagentMeta.Render(" · " + shortBranch(e.Branch))
		// Commit state, so the async banner doesn't imply integrate-ready
		// work on an empty/rejected branch. A committed worker shows its
		// short SHA; one that produced nothing committable shows the reason.
		switch {
		case e.Committed:
			header += styleSubagentMeta.Render(" · committed " + shortCommit(e.CommitSHA))
		case e.CommitErr != "":
			header += styleSubagentErr.Render(" · not committed: " + e.CommitErr)
		case e.Reclaimed:
			// The branch named above no longer exists — the worker produced
			// nothing, so its empty worktree+branch were removed on finish.
			header += styleSubagentMeta.Render(" · no changes — worktree reclaimed")
		}
	}
	footer := styleSubagentMeta.Render("    /subagents — open the picker, then Enter on task " + e.TaskID[:8])
	return header + "\n" + footer
}

// shortCommit renders an 8-char commit SHA for the dock banner, matching
// the dispatch tool's shortSHA so the two surfaces agree.
func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// formatSubagentStats composes the duration / tool-count / tokens
// receipt rendered inline on every subagent completion card. Tokens
// are omitted when zero (we don't yet propagate real per-turn token
// deltas from the adapter — when that lands, this renderer will
// surface them automatically). Tool count is always shown so the
// user can see how much investigation the child did.
func formatSubagentStats(duration time.Duration, toolCalls, tokens int, model string) string {
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
	// Surface the routed model so the user can see cache-safe routing in
	// action (e.g. an Explore subagent that ran on the fast model). Empty
	// when the child inherited the parent's model — no chip then.
	if model != "" {
		parts = append(parts, "on "+model)
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

// truncateForRender returns at most n chars with an ellipsis when
// truncated. Used for one-line previews in scrollback cards.
func truncateForRender(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}
