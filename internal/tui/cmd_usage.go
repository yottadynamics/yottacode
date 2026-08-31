package tui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/cost"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// retainedContextWarnTokens is the approximate size where one retained
// transcript message is large enough to noticeably inflate every later turn.
// The estimate uses the same 4-chars/token heuristic as ToolStats.
const retainedContextWarnTokens = 8_000

// cmdUsage renders per-session token usage in a Claude-style per-model
// breakdown, plus live rate limits and a provider-aware account block.
// It deliberately shows NO dollar estimate: providers don't expose a
// per-model pricing API, so any computed cost would rest on a
// hand-maintained price table we can't keep accurate. The account
// block links each provider's billing dashboard instead — that's the
// authoritative source for spend.
//
//   - subscription auth (openai-auth, copilot): plan + reset window
//     where available. For openai-auth we additionally fire a
//     best-effort backend probe to surface plan info proactively (the
//     /backend-api/me endpoint isn't documented; we degrade silently
//     when it changes shape).
//   - pay-per-use cloud (anthropic, openai, gemini, xai,
//     openai-compatible): session + today rollup + billing-dashboard
//     link.
//   - free / local (ollama, NIM): token counts only.
//
// Renders into an inline overlay above the cmdline (like the
// cheatsheet) rather than appending to chat scrollback: token tallies
// are transient inspection, not part of the conversation, so they
// shouldn't bloat the history the model re-reads. The body is rendered
// once here so the per-frame redraw doesn't re-fire the openai-auth
// backend probe; any key dismisses it.
//
// Read-only; safe mid-turn (PreservesTurn=true).
func cmdUsage(m Model, _ []string) (Model, tea.Cmd) {
	// Snapshot the live subagent registry onto the session so /usage folds
	// in this session's subagent spend (session.AddUsage only ever tallies
	// the main thread). Export is lock-guarded and returns a copy.
	if m.subagentTasks != nil && m.sess != nil {
		m.sess.SubagentTasks = m.subagentTasks.Export()
	}
	m.usagePanel = renderUsagePanel(m)
	m.usageOpen = true
	m.usageScrollOffset = 0
	return m, nil
}

// usageScrollReserve reserves one row for the scroll-position hint line
// windowedUsagePanel appends whenever it truncates — without this the hint
// itself would push the rendered box one row past what usageVisibleLines
// promised.
const usageScrollReserve = 1

// usageVisibleLines returns how many lines of usagePanel fit in the popup
// at the current terminal height. popupBox adds exactly 2 border rows
// (RoundedBorder, no vertical padding — popup.go:42-49) and popupOrigin
// clamps the box to the terminal rather than growing past it
// (popup.go:87-90), so content taller than this would otherwise get
// silently clipped instead of scrolling. Floors at 1 rather than a larger
// arbitrary minimum: below roughly 4 terminal rows there's no room for a
// bordered popup with any content regardless of this floor (2 border rows
// + the hint alone already exceed that), a limit inherent to popupBox
// itself and shared by every popup in this package, not something this
// function can route around.
func (m Model) usageVisibleLines() int {
	n := m.height - 2 - usageScrollReserve
	if n < 1 {
		n = 1
	}
	return n
}

// usageFullFitLines is the "does this need scrolling at all" budget —
// just the 2 popupBox border rows, no hint reserve. Checked before
// usageVisibleLines' smaller budget: content that fits here needs no hint,
// so reserving a line for one would truncate a line of real content that
// didn't need to be cut.
func (m Model) usageFullFitLines() int {
	n := m.height - 2
	if n < 1 {
		n = 1
	}
	return n
}

// usageMaxScrollOffset is the highest offset that still shows a full final
// page — scrolling can't go further than "the last line is at the bottom."
func (m Model) usageMaxScrollOffset() int {
	lines := strings.Count(m.usagePanel, "\n") + 1
	if lines <= m.usageFullFitLines() {
		return 0
	}
	return lines - m.usageVisibleLines()
}

// windowedUsagePanel returns the visible slice of usagePanel for the
// current usageScrollOffset, with a scroll-position hint appended when the
// content doesn't fit on one screen. Needed because popupBox has no height
// clamp of its own — without this, the tool/subagent/session detail added
// to /usage would overflow the terminal instead of scrolling.
func (m Model) windowedUsagePanel() string {
	if m.usagePanel == "" {
		return m.usagePanel
	}
	allLines := strings.Split(m.usagePanel, "\n")
	total := len(allLines)
	if total <= m.usageFullFitLines() {
		// Fits with room to spare for the border alone — no hint needed,
		// so don't spend the smaller hint-reserving budget truncating a
		// line that didn't need to be cut.
		return m.usagePanel
	}
	visible := m.usageVisibleLines()
	offset := min(max(m.usageScrollOffset, 0), total-visible)
	end := min(total, offset+visible)
	shown := strings.Join(allLines[offset:end], "\n")
	hint := fmt.Sprintf("── %d-%d of %d lines · wheel/click ↑↓ · PgUp/PgDn ──", offset+1, end, total)
	return shown + "\n" + styleHint.Render(hint)
}

func renderUsagePanel(m Model) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Usage", "Tokens by model, tool, and subagent, plus cache, compaction, efficiency, daily rollup, rate limits, and account links.", m.popupWidth()))
	b.WriteString("\n")

	b.WriteString(renderSessionUsage(m.sess))
	b.WriteString("\n\n")

	if tools := renderToolStats(m.sess); tools != "" {
		b.WriteString(tools)
		b.WriteString("\n\n")
	}

	if retained := renderRetainedContext(m.sess); retained != "" {
		b.WriteString(retained)
		b.WriteString("\n\n")
	}

	if efficiency := renderEfficiencySection(m.sess); efficiency != "" {
		b.WriteString(efficiency)
		b.WriteString("\n\n")
	}

	if agents := renderSubagentDetail(m.sess); agents != "" {
		b.WriteString(agents)
		b.WriteString("\n\n")
	}

	if rl := renderRateLimits(m.providerProfile.Provider); rl != "" {
		b.WriteString(rl)
		b.WriteString("\n\n")
	}

	if rollup := renderTodayRollup(m.sess.ID); rollup != "" {
		b.WriteString(rollup)
		b.WriteString("\n\n")
	}

	b.WriteString(renderAccountSection(m))

	b.WriteString("\n\n")
	b.WriteString(styleHint.Render("esc to close"))

	return strings.TrimRight(b.String(), "\n")
}

// renderSessionUsage writes the "session" block: a per-model token
// breakdown and a single session total. Subagent spend is folded into both —
// each model row and the total reflect everything this session spent on that
// model (main thread + any subagents that ran on it), so /usage answers
// "what did this whole conversation cost." Per-turn and per-task granularity
// live inline instead (the "Thought for …" footer and the subagent cards).
// Token counts are provider-reported and exact. No dollar figure — cost
// would require a price table we can't keep accurate (see the
// billing-dashboard link in the account block). Sessions with no usage yet
// get a short placeholder.
func renderSessionUsage(s *session.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-13s%s  (%s)\n", "session", formatSessionUsageTime(s.Created), s.ID)

	sub := s.SubagentUsage()
	if s.TotalUsage.IsZero() && sub.Total.IsZero() {
		b.WriteByte('\n')
		b.WriteString(styleEmpty.Render("no token data yet — records after the next assistant turn"))
		return b.String()
	}

	models := combinedModelUsage(s, sub)
	if metrics := renderSessionMetrics(s, len(models)); metrics != "" {
		b.WriteByte('\n')
		b.WriteString(metrics)
		b.WriteByte('\n')
	}
	if peak := renderPeakTurn(s); peak != "" {
		b.WriteString(peak)
		b.WriteByte('\n')
	}
	if comp := renderCompactionSummary(s); comp != "" {
		b.WriteString(comp)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	modelWidth := 0
	for _, m := range models {
		modelWidth = max(modelWidth, len(m.Model))
	}

	for _, m := range models {
		showModelTotal := len(models) > 1 || usageHasDetailRows(m.Usage)
		b.WriteString(formatModelUsageBlock(m.Model, m.Usage, modelWidth, showModelTotal))
		b.WriteByte('\n')
	}

	var total adapter.Usage
	total.Add(&s.TotalUsage)
	total.Add(&sub.Total)
	fmt.Fprintf(&b, "%-13s  %s tokens\n", "session total", formatInt(totalTokensFor(total)))

	return strings.TrimRight(b.String(), "\n")
}

// renderSessionMetrics prints lightweight current-session shape data beside
// the token ledger. These counts come from the session and subagent index only;
// they avoid provider calls and stay cheap enough for an always-on /usage row.
func renderSessionMetrics(s *session.Session, modelCount int) string {
	if s == nil {
		return ""
	}
	turns, tools := 0, 0
	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleAssistant {
			continue
		}
		turns++
		tools += len(msg.ToolCalls)
	}
	parts := make([]string, 0, 4)
	if turns > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", turns, usagePluralize("turn", turns)))
	}
	if tools > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", tools, usagePluralize("tool", tools)))
	}
	if n := len(s.SubagentTasks); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, usagePluralize("subagent", n)))
	}
	if modelCount > 1 {
		parts = append(parts, fmt.Sprintf("%d models", modelCount))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("%-13s%s", "metrics", strings.Join(parts, " · "))
}

func usagePluralize(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// formatSessionUsageTime renders the session timestamp in local time for the
// terminal while keeping the full session id beside it as the durable handle.
func formatSessionUsageTime(t time.Time) string {
	if t.IsZero() {
		return "unknown time"
	}
	local := t.Local()
	label := local.Format("3:04 PM")
	now := time.Now()
	if local.Year() == now.Year() && local.YearDay() == now.YearDay() {
		return label + " today"
	}
	return label + " " + local.Format("2006-01-02")
}

// combinedModelUsage merges the main-thread per-model breakdown with the
// subagent rollup (each subagent attributed to its own model, inherited runs
// to the session's headline model), sorted highest-spender-first. Folding
// them here keeps the rows and the session total consistent — the rows sum
// to the total.
func combinedModelUsage(s *session.Session, sub session.SubagentUsageRollup) []perModelEntry {
	merged := map[string]adapter.Usage{}
	add := func(model string, u adapter.Usage) {
		prev := merged[model]
		prev.Add(&u)
		merged[model] = prev
	}
	for model, u := range s.ModelUsage {
		add(model, u)
	}
	// Fallback: a main-thread total with no per-model breakdown (sessions
	// created before ModelUsage landed) attributes to the headline model.
	if len(s.ModelUsage) == 0 && !s.TotalUsage.IsZero() {
		add(s.Model, s.TotalUsage)
	}
	for model, u := range sub.ByModel {
		add(model, u)
	}
	out := make([]perModelEntry, 0, len(merged))
	for model, u := range merged {
		out = append(out, perModelEntry{Model: model, Usage: u})
	}
	sort.Slice(out, func(i, j int) bool {
		return totalTokensFor(out[i].Usage) > totalTokensFor(out[j].Usage)
	})
	return out
}

// formatModelUsageBlock renders a per-model ledger. Input/output are always
// shown; cache and reasoning rows appear only when non-zero so ordinary turns
// stay compact while cache-heavy sessions remain explainable. The total uses
// the same non-reasoning basis as the footer and session total because several
// providers report reasoning as a detail of output tokens rather than an
// additive bucket.
func formatModelUsageBlock(model string, u adapter.Usage, modelWidth int, showTotal bool) string {
	labelWidth := max(modelWidth, len("session total"))
	metricWidth := 11
	valueWidth := maxUsageValueWidth(u)

	rate := cacheHitRate(u)
	var rateStr string
	if rate >= 0 {
		rateStr = fmt.Sprintf("%.0f%%", rate*100)
		// The percentage string ("100%") can be wider than every token
		// count in a session with small totals but a high hit rate — fold
		// it into the shared column width so the row (and the divider
		// line below, which reuses this same width) stays aligned.
		valueWidth = max(valueWidth, len(rateStr))
	}

	rows := []usageMetricRow{
		{"input", u.InputTokens},
		{"output", u.OutputTokens},
	}
	if u.CacheReadTokens > 0 {
		rows = append(rows, usageMetricRow{"cache read", u.CacheReadTokens})
	}
	if u.CacheCreationTokens > 0 {
		rows = append(rows, usageMetricRow{"cache write", u.CacheCreationTokens})
	}
	if u.ReasoningTokens > 0 {
		rows = append(rows, usageMetricRow{"reasoning", u.ReasoningTokens})
	}

	var b strings.Builder
	for i, row := range rows {
		label := ""
		if i == 0 {
			label = model
		}
		fmt.Fprintf(&b, "%-*s  %-*s %*s\n", labelWidth, label, metricWidth, row.label, valueWidth, formatInt(row.value))
	}
	if rate >= 0 {
		fmt.Fprintf(&b, "%-*s  %-*s %*s\n", labelWidth, "", metricWidth, "cache hit", valueWidth, rateStr)
	}
	if showTotal {
		fmt.Fprintf(&b, "%-*s  %s\n", labelWidth, "", strings.Repeat("─", metricWidth+1+valueWidth))
		fmt.Fprintf(&b, "%-*s  %-*s %*s\n", labelWidth, "", metricWidth, "total", valueWidth, formatInt(totalTokensFor(u)))
	}
	return strings.TrimRight(b.String(), "\n")
}

type usageMetricRow struct {
	label string
	value int64
}

func maxUsageValueWidth(u adapter.Usage) int {
	width := len(formatInt(totalTokensFor(u)))
	for _, v := range []int64{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens, u.ReasoningTokens} {
		if v > 0 {
			width = max(width, len(formatInt(v)))
		}
	}
	return width
}

func usageHasDetailRows(u adapter.Usage) bool {
	return u.CacheReadTokens > 0 || u.CacheCreationTokens > 0 || u.ReasoningTokens > 0
}

// cacheHitRate returns the fraction of this turn's prompt-side tokens
// actually served from cache: CacheReadTokens / (CacheReadTokens +
// InputTokens + CacheCreationTokens) — the same prompt-token basis
// totalTokensFor uses elsewhere in this file, so a cache *write* (content
// being cached for the first time this turn, not yet a hit) correctly
// counts as a miss rather than being left out of the denominator. -1 only
// when there's no cache activity at all (both CacheReadTokens and
// CacheCreationTokens are zero) — nothing to report. Whenever caching IS in
// play but CacheReadTokens is exactly 0 (a broken cache prefix: every turn
// re-caches from scratch instead of hitting), this reports 0% rather than
// hiding the row, since that's precisely the signal the feature exists to
// surface.
func cacheHitRate(u adapter.Usage) float64 {
	if u.CacheReadTokens <= 0 && u.CacheCreationTokens <= 0 {
		return -1
	}
	total := u.CacheReadTokens + u.InputTokens + u.CacheCreationTokens
	if total <= 0 {
		return -1
	}
	return float64(u.CacheReadTokens) / float64(total)
}

// renderPeakTurn finds the assistant turn with the largest token total and
// surfaces its input/output/cache split, so one outlier turn is visible and
// explainable instead of averaged away inside the session total.
func renderPeakTurn(s *session.Session) string {
	if s == nil {
		return ""
	}
	turn, bestTurn := 0, 0
	var bestUsage adapter.Usage
	var bestTokens int64
	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleAssistant {
			continue
		}
		turn++
		if msg.Usage == nil {
			continue
		}
		if tok := totalTokensFor(*msg.Usage); tok > bestTokens {
			bestTokens, bestTurn, bestUsage = tok, turn, *msg.Usage
		}
	}
	if bestTurn == 0 {
		return ""
	}
	parts := []string{
		fmt.Sprintf("input %s", formatInt(bestUsage.InputTokens)),
		fmt.Sprintf("output %s", formatInt(bestUsage.OutputTokens)),
	}
	if bestUsage.CacheReadTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache read %s", formatInt(bestUsage.CacheReadTokens)))
	}
	if bestUsage.CacheCreationTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache write %s", formatInt(bestUsage.CacheCreationTokens)))
	}
	if bestUsage.ReasoningTokens > 0 {
		parts = append(parts, fmt.Sprintf("reasoning %s", formatInt(bestUsage.ReasoningTokens)))
	}
	return fmt.Sprintf("%-13sturn %d · %s tokens (%s)", "largest turn", bestTurn, formatInt(bestTokens), strings.Join(parts, " · "))
}

// renderRetainedContext lists the largest messages currently retained in the
// transcript. These are the blobs most likely to inflate every later provider
// input, which is the missing causal link when /usage flags a token outlier.
func renderRetainedContext(s *session.Session) string {
	if s == nil || len(s.Messages) == 0 {
		return ""
	}
	callNames := map[string]string{}
	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID != "" && call.Name != "" {
				callNames[call.ID] = call.Name
			}
		}
	}
	type retainedRow struct {
		label  string
		tokens int64
	}
	rows := make([]retainedRow, 0, len(s.Messages))
	for _, msg := range s.Messages {
		tokens := int64((len(msg.Content) + 3) / 4)
		if tokens == 0 {
			continue
		}
		label := string(msg.Role)
		if msg.Role == adapter.RoleTool {
			name := callNames[msg.ToolCallID]
			if name == "" {
				name = "unknown"
			}
			label = "tool:" + name
		}
		rows = append(rows, retainedRow{label: label, tokens: tokens})
	}
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].tokens != rows[j].tokens {
			return rows[i].tokens > rows[j].tokens
		}
		return rows[i].label < rows[j].label
	})
	if len(rows) > 5 {
		rows = rows[:5]
	}
	labelWidth, valueWidth := 0, 0
	for _, r := range rows {
		labelWidth = max(labelWidth, len(r.label))
		valueWidth = max(valueWidth, len(formatInt(r.tokens)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-13stop retained context\n", "context")
	for _, r := range rows {
		line := fmt.Sprintf("%-13s%-*s  %*s tokens", "", labelWidth, r.label, valueWidth, formatInt(r.tokens))
		if r.tokens >= retainedContextWarnTokens {
			line = styleNoticeWarn.Render(line + "  ⚠ retained")
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderCompactionSummary surfaces how many times this session's own
// history was compacted (auto or manual /summarize) and how many tokens
// that reclaimed — spend from re-summarization that would otherwise be
// invisible, folded silently into ordinary turn usage.
func renderCompactionSummary(s *session.Session) string {
	if s == nil || len(s.CompactionEvents) == 0 {
		return ""
	}
	var reclaimed int
	for _, e := range s.CompactionEvents {
		if d := e.Before - e.After; d > 0 {
			reclaimed += d
		}
	}
	return fmt.Sprintf("%-13scompacted %dx · reclaimed ~%s tokens", "compaction", len(s.CompactionEvents), formatTokens(reclaimed))
}

// renderToolStats renders the main-thread per-tool-call breakdown: how many
// times each tool ran, its approximate combined output size, and how many
// calls errored. Main-thread only — subagent tool calls stay a single count
// on subagents.Task.ToolCalls, not broken out by name (see the ToolStats
// doc comment on session.Session). Rows sort by output tokens descending —
// the tool most likely to be inflating input tokens on every later turn.
func renderToolStats(s *session.Session) string {
	if s == nil || len(s.ToolStats) == 0 {
		return ""
	}
	type toolRow struct {
		name string
		session.ToolStat
	}
	rows := make([]toolRow, 0, len(s.ToolStats))
	for name, stat := range s.ToolStats {
		rows = append(rows, toolRow{name, stat})
	}
	// name is the tiebreaker (map keys are unique) so two tools tied on
	// OutputTokens render in the same order every time /usage opens —
	// ranging over s.ToolStats above visits them in Go's randomized map
	// order, and sort.Slice isn't stable, so without it a tie's relative
	// order would vary run to run.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].OutputTokens != rows[j].OutputTokens {
			return rows[i].OutputTokens > rows[j].OutputTokens
		}
		return rows[i].name < rows[j].name
	})

	nameWidth, valueWidth := 0, 0
	for _, r := range rows {
		nameWidth = max(nameWidth, len(r.name))
		valueWidth = max(valueWidth, len(formatInt(r.OutputTokens)))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-13s%d distinct\n", "tools", len(rows))
	for _, r := range rows {
		line := fmt.Sprintf("%-13s%-*s  %4d %-5s  %*s tokens", "", nameWidth, r.name, r.Count, usagePluralize("call", r.Count), valueWidth, formatInt(r.OutputTokens))
		if r.Errors > 0 {
			line += fmt.Sprintf("  %d %s", r.Errors, usagePluralize("error", r.Errors))
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// lowSignalInputTokens and lowSignalOutputTokens bound a "low-signal" turn:
// a lot of input spent — mostly retained history, not new content — for
// next to no new output. That's the shape of a turn that pays the
// session's full fixed cost (system prompt, memory, tool schemas, prior
// history) but visibly makes little progress.
const (
	lowSignalInputTokens  = 10_000
	lowSignalOutputTokens = 50
)

// maxEfficiencyDetailRows caps how many repeated-tool-call or
// repeated-failure rows the efficiency section lists, matching the top-5
// convention renderRetainedContext already uses.
const maxEfficiencyDetailRows = 5

// renderEfficiencySection composes the /usage "efficiency" block: average
// tokens spent per assistant turn and how many were low-signal, plus (when
// present) exact-duplicate tool calls and tool calls that tripped the
// in-loop repeated-failure guard. Everything here is derived from data the
// session already persists (per-turn adapter.Usage, ToolCalls with their
// ArgsJSON, and tool-result content) — no new tracking or storage.
// Self-hides entirely when the session has no assistant turns yet.
func renderEfficiencySection(s *session.Session) string {
	summary := renderEfficiencySummary(s)
	if summary == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(summary)
	b.WriteByte('\n')
	for _, row := range repeatedToolCallRows(s) {
		fmt.Fprintf(&b, "%-13s%s\n", "", row)
	}
	for _, row := range repeatedToolFailureRows(s) {
		fmt.Fprintf(&b, "%-13s%s\n", "", row)
	}
	if waste := renderWasteEstimate(s); waste != "" {
		fmt.Fprintf(&b, "%-13s%s\n", "", waste)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderEfficiencySummary computes the average total tokens spent per
// assistant turn and counts low-signal turns (see lowSignalInputTokens /
// lowSignalOutputTokens), from the per-turn Usage already attached to each
// RoleAssistant message. Returns "" when the session has no turn with
// recorded usage yet.
func renderEfficiencySummary(s *session.Session) string {
	if s == nil {
		return ""
	}
	var turns, lowSignal int
	var totalTokens int64
	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleAssistant || msg.Usage == nil {
			continue
		}
		turns++
		totalTokens += totalTokensFor(*msg.Usage)
		if msg.Usage.InputTokens > lowSignalInputTokens && msg.Usage.OutputTokens < lowSignalOutputTokens {
			lowSignal++
		}
	}
	if turns == 0 {
		return ""
	}
	line := fmt.Sprintf("%-13savg %s tokens/turn", "efficiency", formatTokens(int(totalTokens/int64(turns))))
	if lowSignal > 0 {
		line += fmt.Sprintf(" · %d low-signal %s", lowSignal, usagePluralize("turn", lowSignal))
	}
	return line
}

// repeatedToolCallRows finds tool calls issued more than once in this
// session with the exact same arguments where no world-mutating tool ran
// in between — genuine "spinning" duplicates, not verification-loop
// re-calls (e.g. run_tests after each edit). Uses a mutation epoch that
// advances whenever a non-read-only tool is seen; a duplicate only
// counts when its epoch matches the previous occurrence's epoch.
// Sorted by repeat count descending, capped at maxEfficiencyDetailRows.
func repeatedToolCallRows(s *session.Session) []string {
	if s == nil {
		return nil
	}
	type callKey struct{ name, args string }
	// Per-key state: the epoch when we last saw this key, and how many
	// idle (same-epoch) duplicates we've accumulated.
	type keyState struct {
		lastEpoch int
		idle      int // duplicate occurrences with no intervening mutation
	}
	state := map[callKey]*keyState{}
	var order []callKey
	epoch := 0

	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			// Advance the mutation epoch for non-read-only tools.
			if !agent.IsReadOnlyTool(call.Name) {
				epoch++
			}
			k := callKey{call.Name, call.ArgsJSON}
			st, exists := state[k]
			if !exists {
				st = &keyState{lastEpoch: epoch}
				state[k] = st
				order = append(order, k)
				continue
			}
			// Same key seen again — idle duplicate only if epoch hasn't
			// advanced since the last occurrence of this exact key.
			if epoch == st.lastEpoch {
				st.idle++
			}
			st.lastEpoch = epoch
		}
	}
	type repeatRow struct {
		callKey
		count int
	}
	var rows []repeatRow
	for _, k := range order {
		if state[k].idle > 0 {
			rows = append(rows, repeatRow{k, state[k].idle + 1})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})
	if len(rows) > maxEfficiencyDetailRows {
		rows = rows[:maxEfficiencyDetailRows]
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("repeated call  %s(%s) × %d", r.name, truncateForRender(r.args, 40), r.count))
	}
	return out
}

// repeatedToolFailureRows counts, per tool, how many times this session's
// in-loop repeated-failure guard (agent.RepeatedToolFailureMarker, injected
// by applyRepeatedToolFailureGuard) fired — i.e. the same tool failed the
// same way enough times in a row that the loop injected strategy guidance
// rather than letting it retry blind. Read retroactively from persisted
// tool-result content; the guard itself already ran live during the turn.
func repeatedToolFailureRows(s *session.Session) []string {
	if s == nil {
		return nil
	}
	callNames := map[string]string{}
	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID != "" && call.Name != "" {
				callNames[call.ID] = call.Name
			}
		}
	}
	counts := map[string]int{}
	var order []string
	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleTool || !strings.Contains(msg.Content, agent.RepeatedToolFailureMarker) {
			continue
		}
		name := callNames[msg.ToolCallID]
		if name == "" {
			name = "unknown"
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}
	if len(order) == 0 {
		return nil
	}
	sort.Slice(order, func(i, j int) bool {
		if counts[order[i]] != counts[order[j]] {
			return counts[order[i]] > counts[order[j]]
		}
		return order[i] < order[j]
	})
	if len(order) > maxEfficiencyDetailRows {
		order = order[:maxEfficiencyDetailRows]
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, fmt.Sprintf("strategy guidance fired  %s (%d×)", name, counts[name]))
	}
	return out
}

// renderWasteEstimate sums estimated tokens for two unambiguous categories
// of waste: the result of every idle-duplicate tool call past its first
// occurrence (same args, no intervening mutation — the same epoch logic
// repeatedToolCallRows uses), and every tool-result message where the
// in-loop repeated-failure guard fired (agent.RepeatedToolFailureMarker).
// This is a floor, not a full accounting: it deliberately does NOT price
// low-signal turns or ordinary retained context, since a turn or a
// retained blob can be expensive without being unambiguously wasted —
// only these two categories are.
func renderWasteEstimate(s *session.Session) string {
	if s == nil {
		return ""
	}
	// Pre-index tool result sizes by call ID.
	resultTokens := map[string]int64{}
	for _, msg := range s.Messages {
		if msg.Role == adapter.RoleTool {
			resultTokens[msg.ToolCallID] = int64((len(msg.Content) + 3) / 4)
		}
	}

	// Walk messages with the same mutation-epoch logic as
	// repeatedToolCallRows: only charge waste for duplicates that
	// occurred without an intervening world-mutating tool call.
	var waste int64
	type callKey struct{ name, args string }
	type keyState struct {
		lastEpoch int
	}
	state := map[callKey]*keyState{}
	epoch := 0

	for _, msg := range s.Messages {
		if msg.Role != adapter.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if !agent.IsReadOnlyTool(call.Name) {
				epoch++
			}
			k := callKey{call.Name, call.ArgsJSON}
			st, exists := state[k]
			if !exists {
				state[k] = &keyState{lastEpoch: epoch}
				continue
			}
			// Idle duplicate: same key, epoch hasn't advanced.
			if epoch == st.lastEpoch {
				waste += resultTokens[call.ID]
			}
			st.lastEpoch = epoch
		}
	}

	// Add tokens from repeated-failure guard firings (separate signal).
	for _, msg := range s.Messages {
		if msg.Role == adapter.RoleTool && strings.Contains(msg.Content, agent.RepeatedToolFailureMarker) {
			waste += int64((len(msg.Content) + 3) / 4)
		}
	}
	if waste == 0 {
		return ""
	}
	return fmt.Sprintf("waste estimate  ~%s tokens (repeated calls + failed-retry guidance)", formatTokens(int(waste)))
}

// renderSubagentDetail lists each subagent task this session ran, one row
// per task: agent type, duration, tokens, tool-call count, and status.
// Complements the per-model subagent rollup already folded into the
// session block above by making individual runs visible — a single
// bloated subagent otherwise hides inside the combined model total.
func renderSubagentDetail(s *session.Session) string {
	if s == nil || len(s.SubagentTasks) == 0 {
		return ""
	}
	tasks := append([]subagents.TaskRecord(nil), s.SubagentTasks...)
	sort.Slice(tasks, func(i, j int) bool {
		return taskTotalTokens(tasks[i]) > taskTotalTokens(tasks[j])
	})
	agentWidth, valueWidth, toolsWidth := 0, 0, 1
	for _, t := range tasks {
		agentWidth = max(agentWidth, len(t.AgentType))
		valueWidth = max(valueWidth, len(formatInt(taskTotalTokens(t))))
		toolsWidth = max(toolsWidth, len(strconv.Itoa(t.ToolCalls)))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-13s%d %s\n", "subagents", len(tasks), usagePluralize("task", len(tasks)))
	for _, t := range tasks {
		dur := "running"
		if !t.Finished.IsZero() {
			dur = formatDuration(t.Finished.Sub(t.Started))
		}
		tok := taskTotalTokens(t)
		idLabel := t.ID
		if len(idLabel) > 8 {
			idLabel = idLabel[:8]
		}
		line := fmt.Sprintf("%-13s%-8s  %-*s  %8s  %*s tokens  %*d %-5s  %s", "", idLabel, agentWidth, t.AgentType, dur, valueWidth, formatInt(tok), toolsWidth, t.ToolCalls, usagePluralize("tool", t.ToolCalls), t.Status.String())
		if t.CompactionCount > 0 {
			line += fmt.Sprintf("  compacted %dx", t.CompactionCount)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// taskTotalTokens returns the exact provider-reported total for a
// persisted subagent task record, falling back to the ~4-char/token
// estimate — the same basis subagents.Task.UsageTokens uses for the live
// Task, duplicated here because TaskRecord (the persisted form) doesn't
// carry that method.
func taskTotalTokens(t subagents.TaskRecord) int64 {
	if !t.Usage.IsZero() {
		return t.Usage.InputTokens + t.Usage.OutputTokens + t.Usage.CacheReadTokens + t.Usage.CacheCreationTokens
	}
	return int64(t.TokensUsed)
}

// perModelEntry is one row of the /usage per-model breakdown (highest
// spender first — matching Claude Code's /usage). Built by combinedModelUsage.
type perModelEntry struct {
	Model string
	Usage adapter.Usage
}

// localMidnight returns 00:00 in t's own location. Deliberately NOT
// t.Truncate(24*time.Hour): Truncate rounds against the UTC-referenced zero
// time, so for any non-UTC caller it silently snaps to the most recent UTC
// midnight rather than the caller's own — a bug renderTodayRollup used to
// have, made consequential once it started listing individual, outlier-
// ranked sessions instead of only a hidden aggregate.
func localMidnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// renderTodayRollup scans every session created since 00:00 local and
// emits a token summary followed by one row per session, largest first —
// so a token spike on a given day is traceable to a specific session
// instead of only visible as a daily total. Skips entirely when only the
// current session is in scope (the per-session block above already
// covers it). currentID marks this session's own row in the list.
func renderTodayRollup(currentID string) string {
	summaries, err := session.UsageSince(localMidnight(time.Now()))
	if err != nil || len(summaries) <= 1 {
		return ""
	}
	type dayRow struct {
		session.SessionUsageSummary
		tokens int64
	}
	rows := make([]dayRow, 0, len(summaries))
	var totalTokens int64
	for _, s := range summaries {
		// Main thread + this session's subagent spend, so the daily tally
		// matches the per-session block's combined total.
		tok := totalTokensFor(s.TotalUsage) + totalTokensFor(s.SubagentUsage().Total)
		rows = append(rows, dayRow{s, tok})
		totalTokens += tok
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].tokens > rows[j].tokens })

	var b strings.Builder
	fmt.Fprintf(&b, "%-13s%d sessions · %s tokens\n", "today", len(rows), formatTokens(int(totalTokens)))
	for _, r := range rows {
		label := formatSessionUsageTime(r.Created)
		if r.ID == currentID {
			label += " (current)"
		}
		// The short id lets /inspect <id> target a past session spotted
		// here — without it there's no way to reference a row other than
		// the current one.
		idLabel := r.ID
		if len(idLabel) > 8 {
			idLabel = idLabel[:8]
		}
		line := fmt.Sprintf("%-13s%-24s%s tokens  %s", "", label, formatTokens(int(r.tokens)), styleHint.Render(idLabel))
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderRateLimits surfaces the live per-minute quota headroom parsed
// from the last API response's rate-limit headers (OpenAI / Anthropic /
// xAI return these on every 200 — no admin key, no extra request). The
// figure is "remaining / limit" with a humanized reset countdown.
// Returns "" when no snapshot has been observed this run (the provider
// doesn't emit these headers, or no turn has completed yet), so the
// block self-hides rather than printing an empty header.
func renderRateLimits(provider adapter.Provider) string {
	snap := adapter.LastRateLimit(provider)
	if snap == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("rate limits  live, from last response\n")
	if snap.HasTokens {
		b.WriteString("  ")
		b.WriteString(formatRateLimitLine("tokens", snap.TokensRemaining, snap.TokensLimit, snap.TokensReset))
		b.WriteByte('\n')
	}
	if snap.HasRequests {
		b.WriteString("  ")
		b.WriteString(formatRateLimitLine("requests", snap.RequestsRemaining, snap.RequestsLimit, snap.RequestsReset))
		b.WriteByte('\n')
	}
	return b.String()
}

// formatRateLimitLine renders one quota row: "<label>  <remaining> /
// <limit> remaining · resets in <dur>". The limit clause is dropped
// when the provider didn't send a limit header (limit == 0); the reset
// clause is dropped when no reset timestamp was parsed.
func formatRateLimitLine(label string, remaining, limit int64, reset time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s  %s", label, formatInt(remaining))
	if limit > 0 {
		fmt.Fprintf(&b, " / %s", formatInt(limit))
	}
	b.WriteString(" remaining")
	if !reset.IsZero() {
		fmt.Fprintf(&b, " · resets in %s", humanizeUsageDuration(time.Until(reset)))
	}
	return b.String()
}

// renderAccountSection prints the provider-aware account block:
//
//   - subscription auth: plan / reset window + signed-in email, all
//     from the provider's own API (openai-auth's /backend-api/me probe)
//   - pay-per-use cloud: "pay-per-use API key" line
//   - free / local: a one-liner explaining no cost is tracked
//
// Account identity comes from each provider's own API, never from
// config — and only openai-auth currently exposes one (email + plan).
// API-key providers don't surface the holder's name/email on the
// inference key, so none is shown for them. For every billable provider
// it links the billing dashboard.
func renderAccountSection(m Model) string {
	var b strings.Builder

	provider := m.providerProfile.Provider
	switch {
	case provider == adapter.ProviderOpenAIAuth:
		b.WriteString(renderOpenAIAuthAccount(m.parentCtx))
	case provider == adapter.ProviderCopilot:
		fmt.Fprintf(&b, "%-13s%s\n", "account", "copilot (github subscription)")
		fmt.Fprintf(&b, "%-13s%s\n", "", "token counts only; no public quota endpoint")
	case m.providerProfile.SupportsUsageReporting:
		// Pay-per-use cloud.
		fmt.Fprintf(&b, "%-13s%s (pay-per-use API key)\n", "account", provider)
	default:
		fmt.Fprintf(&b, "%-13s%s\n", "account", "free / local provider")
		fmt.Fprintf(&b, "%-13s%s\n", "", "no per-request cost tracked")
	}

	if url := cost.BillingDashboardURL(provider); url != "" {
		fmt.Fprintf(&b, "%-13sbilling → %s\n", "", shortUsageURL(url))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderOpenAIAuthAccount handles the ChatGPT-subscription branch.
// Fires a best-effort backend probe (cached for 5 min); falls back
// to the 429 memo when the probe yields nothing. Both signals can
// degrade independently, so the function builds the line from
// whatever it has.
func renderOpenAIAuthAccount(ctx context.Context) string {
	var b strings.Builder
	acct := adapter.ProbeOpenAIAuthAccount(ctx)
	memo := adapter.LastOpenAIAuthRateLimit()
	label := "openai-auth (chatgpt subscription)"

	// Plan name: the probe is authoritative when it answered, the memo's
	// header/body value is the fallback.
	plan := ""
	if acct != nil {
		plan = acct.Plan
	}
	if plan == "" && memo != nil {
		plan = memo.PlanType
	}
	if plan != "" {
		fmt.Fprintf(&b, "%-13sopenai-auth (chatgpt %s plan)\n", "account", plan)
	} else {
		fmt.Fprintf(&b, "%-13s%s\n", "account", label)
	}

	rows := quotaWindowRows(memo)
	for _, row := range rows {
		fmt.Fprintf(&b, "%-13s%s\n", "", row)
	}
	if len(rows) == 0 {
		fmt.Fprintf(&b, "%-13s%s\n", "", "quota shown after the first turn")
	}
	if acct != nil && acct.Email != "" {
		fmt.Fprintf(&b, "%-13ssigned in as %s\n", "", acct.Email)
	}
	fmt.Fprintf(&b, "%-13s✓ no per-request cost — subscription\n", "")
	return b.String()
}

// quotaWindowRows renders one line per Codex quota window that still
// carries usable information, most-constraining facts first. Returns
// nothing when the memo is empty or every window has lapsed, which is
// the renderer's cue to print the "not observed yet" fallback.
//
// A window whose reset instant has already passed is dropped rather than
// shown: the quota it described has since refilled, so displaying it
// asserts a limit the user is no longer under. (Before this, a lapsed
// window rendered as a permanent "resets in now", because
// humanizeUsageDuration collapses every non-positive duration to "now".)
func quotaWindowRows(memo *adapter.OpenAIAuthRateLimit) []string {
	if memo == nil {
		return nil
	}
	now := time.Now()
	var rows []string
	for _, w := range []struct {
		family string
		win    adapter.OpenAIAuthWindow
	}{
		{"primary", memo.Primary},
		{"secondary", memo.Secondary},
	} {
		if !w.win.Has {
			continue
		}
		// Lapsed: the window reset while we weren't looking.
		if !w.win.ResetsAt.IsZero() && !w.win.ResetsAt.After(now) {
			continue
		}
		label := adapter.FormatQuotaWindowLabel(w.win.WindowMinutes)
		if label == "" {
			label = w.family
		}

		var parts []string
		if w.win.HasPercent {
			parts = append(parts, strconv.FormatFloat(w.win.UsedPercent, 'f', -1, 64)+"% used")
		}
		if !w.win.ResetsAt.IsZero() {
			parts = append(parts, "resets in "+humanizeUsageDuration(time.Until(w.win.ResetsAt)))
		}
		if len(parts) == 0 {
			continue
		}
		rows = append(rows, fmt.Sprintf("%-13s%s", label, strings.Join(parts, " · ")))
	}
	return rows
}

func shortUsageURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host + u.EscapedPath()
}

// totalTokensFor is the per-model sort key for the breakdown table:
// total tokens (input + output + cache writes + cache reads).
func totalTokensFor(u adapter.Usage) int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

// renderTurnFooter composes the quiet end-of-turn receipt — how long the
// turn took, plus this turn's exact token total when the provider reported
// usage. The token clause is dropped when usage is zero (providers that
// don't report it) so the line stays "◦ thought · 12s". Uses
// the same total-tokens basis as /usage and the subagent cards, so every
// surface counts tokens the same way. Compact k/M formatting (formatTokens)
// keeps it glanceable, unlike /usage's exact comma counts.
func renderTurnFooter(elapsed time.Duration, turnUsage adapter.Usage) string {
	details := []string{}
	if tok := int(totalTokensFor(turnUsage)); tok > 0 {
		details = append(details, formatTokens(tok)+" tokens")
	}
	return SysMsgAligned(SysThought, "thought", formatDuration(elapsed), details...)
}

// formatInt renders an int64 with thousands separators. /usage uses
// raw counts (no k/M suffixes) so tallies stay precise — Claude
// Code's /usage uses suffixes (e.g. "22.5m cache read") which is
// nice visually but obscures the exact number. We prefer accuracy.
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > 3 {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// humanizeUsageDuration mirrors the openai_auth adapter's humanizer.
// Kept here so the /usage renderer doesn't pull an adapter-internal
// helper across package boundaries.
func humanizeUsageDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	}
	if d < 24*time.Hour {
		d = d.Round(time.Minute)
		h := int(d / time.Hour)
		mn := int((d % time.Hour) / time.Minute)
		if mn == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, mn)
	}
	d = d.Round(time.Hour)
	days := int(d / (24 * time.Hour))
	h := int((d % (24 * time.Hour)) / time.Hour)
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}
