package tui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/cost"
	"github.com/yottadynamics/yottacode/internal/session"
)

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
	return m, nil
}

func renderUsagePanel(m Model) string {
	var b strings.Builder
	b.WriteString(styleSplashTitle.Render("Usage"))
	b.WriteString("\n\n")

	b.WriteString(renderSessionUsage(m.sess))
	b.WriteString("\n\n")

	if rl := renderRateLimits(m.providerProfile.Provider); rl != "" {
		b.WriteString(rl)
		b.WriteString("\n\n")
	}

	if rollup := renderTodayRollup(); rollup != "" {
		b.WriteString(rollup)
		b.WriteString("\n\n")
	}

	b.WriteString(renderAccountSection(m))

	b.WriteString("\n\n")
	b.WriteString(styleHint.Render("esc to close"))

	return indentContextReport(strings.TrimRight(b.String(), "\n"), "  ")
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

// perModelEntry is one row of the /usage per-model breakdown (highest
// spender first — matching Claude Code's /usage). Built by combinedModelUsage.
type perModelEntry struct {
	Model string
	Usage adapter.Usage
}

// renderTodayRollup scans every session created since 00:00 local and
// emits a one-block token summary. Skips entirely when only the
// current session is in scope (the per-session block above already
// covers it).
func renderTodayRollup() string {
	startOfDay := time.Now().Truncate(24 * time.Hour)
	summaries, err := session.UsageSince(startOfDay)
	if err != nil || len(summaries) <= 1 {
		return ""
	}
	var totalTokens int64
	for _, s := range summaries {
		// Main thread + this session's subagent spend, so the daily tally
		// matches the per-session block's combined total.
		totalTokens += totalTokensFor(s.TotalUsage) + totalTokensFor(s.SubagentUsage().Total)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-13s%d sessions · %s tokens", "today", len(summaries), formatTokens(int(totalTokens)))
	return b.String()
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
