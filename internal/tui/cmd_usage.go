package tui

import (
	"context"
	"fmt"
	"sort"
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
// Renders into an inline overlay below the cmdline (like the
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
	b.WriteString(styleAssistantHeader.Render("usage"))
	b.WriteByte('\n')

	b.WriteString(renderSessionUsage(m.sess))
	b.WriteByte('\n')

	if rl := renderRateLimits(m.providerProfile.Provider); rl != "" {
		b.WriteString(rl)
		b.WriteByte('\n')
	}

	if rollup := renderTodayRollup(); rollup != "" {
		b.WriteString(rollup)
		b.WriteByte('\n')
	}

	b.WriteString(renderAccountSection(m))

	b.WriteByte('\n')
	b.WriteString(styleFooter.Render("press any key to close"))

	return b.String()
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
	fmt.Fprintf(&b, "session  %s\n", s.ID)

	sub := s.SubagentUsage()
	if s.TotalUsage.IsZero() && sub.Total.IsZero() {
		b.WriteString("  no token data yet — records after the next assistant turn\n")
		return b.String()
	}

	models := combinedModelUsage(s, sub)
	modelWidth := 0
	for _, m := range models {
		modelWidth = max(modelWidth, len(m.Model))
	}

	b.WriteString("  usage by model:\n")
	for _, m := range models {
		b.WriteString("    ")
		b.WriteString(formatModelUsageLine(m.Model, m.Usage, modelWidth))
		b.WriteByte('\n')
	}

	var total adapter.Usage
	total.Add(&s.TotalUsage)
	total.Add(&sub.Total)
	fmt.Fprintf(&b, "  total tokens  %s\n", formatInt(totalTokensFor(total)))

	return b.String()
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

// formatModelUsageLine produces a single "model: N input, N output,
// N cache read, N cache write, N reasoning" token line. Columns are
// suppressed when zero so a vanilla turn doesn't render "0 cache read,
// 0 cache write".
func formatModelUsageLine(model string, u adapter.Usage, modelWidth int) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%s input", formatInt(u.InputTokens)))
	parts = append(parts, fmt.Sprintf("%s output", formatInt(u.OutputTokens)))
	if u.CacheReadTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s cache read", formatInt(u.CacheReadTokens)))
	}
	if u.CacheCreationTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s cache write", formatInt(u.CacheCreationTokens)))
	}
	if u.ReasoningTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s reasoning", formatInt(u.ReasoningTokens)))
	}
	// Pad the "model:" label as a unit so the colon hugs each model name
	// and the token columns still line up under one another.
	label := model + ":"
	return fmt.Sprintf("%-*s  %s", modelWidth+1, label, strings.Join(parts, ", "))
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
	b.WriteString("today\n")
	fmt.Fprintf(&b, "  %d sessions · %s total tokens\n", len(summaries), formatInt(totalTokens))
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
	b.WriteString("rate limits  (live, from last response)\n")
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
	b.WriteString("account\n")

	provider := m.providerProfile.Provider
	switch {
	case provider == adapter.ProviderOpenAIAuth:
		b.WriteString(renderOpenAIAuthAccount(m.parentCtx))
	case provider == adapter.ProviderCopilot:
		b.WriteString("  copilot (github subscription) — token counts only; no public quota endpoint\n")
	case m.providerProfile.SupportsUsageReporting:
		// Pay-per-use cloud.
		fmt.Fprintf(&b, "  provider: %s (pay-per-use API key)\n", provider)
	default:
		b.WriteString("  free / local provider — no per-request cost tracked\n")
	}

	if url := cost.BillingDashboardURL(provider); url != "" {
		fmt.Fprintf(&b, "  billing dashboard: %s — the source of truth for spend\n", url)
	}
	return b.String()
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

	switch {
	case acct != nil && acct.Plan != "" && memo != nil && !memo.ResetsAt.IsZero():
		fmt.Fprintf(&b, "  openai-auth (chatgpt %s plan) — resets in %s\n",
			acct.Plan, humanizeUsageDuration(time.Until(memo.ResetsAt)))
	case acct != nil && acct.Plan != "":
		fmt.Fprintf(&b, "  openai-auth (chatgpt %s plan)\n", acct.Plan)
	case memo != nil && memo.PlanType != "" && !memo.ResetsAt.IsZero():
		fmt.Fprintf(&b, "  openai-auth (chatgpt %s plan) — resets in %s\n",
			memo.PlanType, humanizeUsageDuration(time.Until(memo.ResetsAt)))
	case memo != nil && memo.PlanType != "":
		fmt.Fprintf(&b, "  openai-auth (chatgpt %s plan)\n", memo.PlanType)
	case memo != nil && !memo.ResetsAt.IsZero():
		fmt.Fprintf(&b, "  openai-auth (chatgpt subscription) — resets in %s\n",
			humanizeUsageDuration(time.Until(memo.ResetsAt)))
	default:
		b.WriteString("  openai-auth (chatgpt subscription) — token counts only; quota visible only after a 429\n")
	}
	if acct != nil && acct.Email != "" {
		fmt.Fprintf(&b, "  signed in as: %s\n", acct.Email)
	}
	b.WriteString("  no per-request cost (subscription)\n")
	return b.String()
}

// totalTokensFor is the per-model sort key for the breakdown table:
// total tokens (input + output + cache writes + cache reads).
func totalTokensFor(u adapter.Usage) int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

// renderTurnFooter composes the quiet end-of-turn receipt — how long the
// turn took, plus this turn's exact token total when the provider reported
// usage. The token clause is dropped when usage is zero (providers that
// don't report it) so the line stays "› Thought for 12s" unchanged. Uses
// the same total-tokens basis as /usage and the subagent cards, so every
// surface counts tokens the same way. Compact k/M formatting (formatTokens)
// keeps it glanceable, unlike /usage's exact comma counts.
func renderTurnFooter(elapsed time.Duration, turnUsage adapter.Usage) string {
	s := "› Thought for " + formatDuration(elapsed)
	if tok := int(totalTokensFor(turnUsage)); tok > 0 {
		s += " · " + formatTokens(tok) + " tokens"
	}
	return s
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
