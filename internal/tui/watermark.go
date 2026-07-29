package tui

import (
	"fmt"
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// watermarkStep is the percentage gap between repeated warning notices
// once the warn threshold is first crossed. Without this, the user
// would see one notice and then nothing until auto-summarization
// kicks in — which can be 20%+ of the window away.
const watermarkStep = 0.05

// refreshContextTokens recomputes the token count without firing any
// notices or auto-summarization. Cheap enough to call on every
// IterationStart so the status-bar percentage tracks per-round
// growth (assistant message + tool results appended during the
// prior round-trip). The full updateContextUsage stays bound to
// turnDone since mid-turn auto-summarize would cancel the active
// turn and a warn notice mid-stream would land in the middle of
// the model's reply.
func (m *Model) refreshContextTokens() {
	// Read the history under the lock: this fires on every IterationStart,
	// concurrently with the agent goroutine appending to the same slice.
	m.contextTokens = m.estimatedContextTokens(m.lockedMessages())
}

func (m *Model) refreshTurnCompactionConfig() {
	if m.cfg.Compaction == nil {
		return
	}
	window := m.contextWindow()
	if window <= 0 {
		m.cfg.Compaction.Window = 0
		return
	}
	m.cfg.Compaction.Window = window
	m.cfg.Compaction.Threshold = m.fileCfg.Context.CompactionThreshold
	m.cfg.Compaction.TargetRatio = contextCompactionTargetRatio(m.fileCfg.Context.CompactionTargetRatio)
	if m.cfg.Compaction.Threshold >= 1.0 {
		// Threshold 1.0 disables preemptive compaction but keeps the
		// Window populated so provider-overflow recovery can still force a
		// single compaction attempt.
		return
	}
	msgs := m.lockedMessages()
	systemTokens, _ := contextwindow.SplitMessages(msgs)
	schemaTokens := registrySchemaTokens(m.cfg.Registry)
	retainBudget := int(contextCompactionTargetRatio(m.fileCfg.Context.CompactionTargetRatio) * float64(window))
	if window-systemTokens-schemaTokens-retainBudget <= 0 {
		m.cfg.Compaction.Window = 0
	}
}

func contextCompactionTargetRatio(configured float64) float64 {
	if configured <= 0 {
		return 0.35
	}
	return configured
}

// estimatedContextTokens approximates the full next-request size: the
// message history plus the tool schemas advertised on every call.
// EstimateTokens counts only the message slice, but the tool schemas
// (easily several thousand tokens once MCP servers attach) ride alongside
// it on the wire. Routing the status bar, the warn/auto thresholds, and
// the non-convergence check through one estimate keeps them honest about
// what the provider actually receives — matching the same overhead the
// agent loop's compaction trigger now counts.
func (m Model) estimatedContextTokens(msgs []adapter.Message) int {
	return contextwindow.EstimateTokens(msgs) + registrySchemaTokens(m.cfg.Registry)
}

// lockedMessages reads sess.Messages under histMu so a concurrent agent
// append (which reassigns the slice header on the Turn goroutine) can't be
// observed mid-write. The returned header is safe to range afterward: an
// append either writes only beyond the snapshot's length or reallocates to
// a fresh array, and the agent never rewrites existing elements' contents.
// Use it for any read of sess.Messages on the Update goroutine that can run
// while a turn is active.
func (m Model) lockedMessages() []adapter.Message {
	m.histMu.Lock()
	defer m.histMu.Unlock()
	return m.sess.Messages
}

// resumeWatermarkCheckMsg fires once from Init when the program starts
// with a resumed (non-empty) session: the transcript may already sit
// past the auto threshold, and the watermark check is otherwise bound
// to turn ends — without this, the first send of a too-full resumed
// session fails with the provider's context-overflow error before
// anything heals it.
type resumeWatermarkCheckMsg struct{}

// registrySchemaTokens estimates the token cost of the registry's
// advertised tool schemas, or 0 when no registry is wired.
func registrySchemaTokens(reg *agent.Registry) int {
	if reg == nil {
		return 0
	}
	return contextwindow.EstimateToolSchemas(reg.AsAdapterTools())
}

// updateContextUsage recomputes context-window usage after each turn,
// emits a one-time warning notice if the warn_threshold was just
// crossed, and returns a tea.Cmd that runs auto-summarization if the
// auto_threshold was crossed. The Model is mutated to remember the
// last percentage observed so we don't spam the same notice.
//
// Returns a non-nil tea.Cmd when auto-summarization should fire — the
// caller must sequence it after any other commands queued for this
// tick (typically session-save and recall-index). allowAuto=false
// suppresses the auto branch for callers about to start a queued turn;
// the returned Cmd must then never be needed. Suppression has to
// happen here rather than by discarding the returned Cmd:
// startAutoSummarize prints its banner and flips m.summarizing as side
// effects BEFORE the Cmd runs, so a discarded Cmd leaves the UI
// announcing a summarization that never starts and wedges the
// m.summarizing gate shut for the rest of the session.
func (m *Model) updateContextUsage(allowAuto bool) tea.Cmd {
	m.refreshContextTokens()
	tokens := m.contextTokens

	window := m.contextWindow()
	if window <= 0 {
		return nil
	}
	pct := float64(tokens) / float64(window)

	warnThr := m.fileCfg.Context.WarnThreshold
	autoThr := m.fileCfg.Context.AutoThreshold

	// Auto-summarization beats the warning notice — there's no point
	// showing "consider /summarize" right before we run /summarize for
	// the user. Auto only fires while we're under the threshold and
	// haven't already auto-summarized at this fill level (tracked via
	// lastWatermarkPct).
	if autoThr < 1.0 && pct >= autoThr && m.lastWatermarkPct < autoThr {
		// Suppress a re-attempt when a prior summarize already failed to
		// converge at ~this fill against ~this window: re-running burns
		// minutes to land at the same irreducible floor. Unlike the old
		// behavior (which latched lastWatermarkPct high and disabled auto
		// for the rest of the session), this re-arms once the situation
		// changes — fill grew past the stuck point by a step, or the
		// window changed. Return before the warn branch so it can't record
		// a ≥ auto_threshold fill and re-close the gate through the back
		// door.
		if autoSuppressedByNonConvergence(pct, window, m.nonConvergentAt, m.nonConvergentWindow) {
			return nil
		}
		if !allowAuto {
			// A queued user message is about to start a fresh turn:
			// compressing now would yank context out from under it.
			// Return without touching the watermark (the warn branch
			// below would record pct ≥ auto_threshold and close the
			// auto gate) so the check re-arms at the queued turn's end.
			return nil
		}
		m.lastWatermarkPct = pct
		return m.startAutoSummarize(pct)
	}

	if warnThr < 1.0 && pct >= warnThr {
		// Fire if we've never warned, or if we've crossed another
		// 5% step since the last notice.
		if m.lastWatermarkPct == 0 || pct >= m.lastWatermarkPct+watermarkStep {
			// First crossing also arms the pre-compaction memory
			// reminder for the next turn: auto-summarization is now on
			// the horizon, and anything durable the model hasn't saved
			// will be compacted away with the older turns. Warn-level
			// only — once the AUTO threshold branch above fires, the
			// details are already being summarized and a reminder
			// would arrive too late.
			if m.lastWatermarkPct == 0 && !m.memoryNudgePending {
				m.memoryNudgePending = true
				m.appendLine(styleAuto.Render(SysMsgAligned(SysQueue, "memory", "reminder armed", "save durable memories next turn")))
			}
			m.lastWatermarkPct = pct
			m.appendLine(styleAuto.Render(SysMsgAligned(SysWarning, "context", fmt.Sprintf("at %d%%", int(pct*100)), "consider /summarize")))
		}
	} else if warnThr < 1.0 && pct < warnThr {
		// Below threshold (after /summarize, /clear, or a /sessions
		// Resume): reset so the next crossing fires fresh. A still-armed
		// reminder disarms with it — post-shrink, the context it was
		// protecting has already been summarized or discarded. A recorded
		// non-convergence is stale too: we've dropped well under the line.
		m.lastWatermarkPct = 0
		m.memoryNudgePending = false
		m.nonConvergentAt = 0
		m.nonConvergentWindow = 0
	}
	return nil
}

// contextWindow returns the resolved capacity for the current model,
// honoring yottacode's known-model table, the serving provider's kind
// (a namesake model behind a different backend can have a smaller real
// window), and the user's context.default_window override.
func (m Model) contextWindow() int {
	return catalog.ResolveWindowForProvider(
		m.fileCfg.ProviderKindForModel(m.modelName),
		m.modelName,
		m.fileCfg.ContextWindowOverride(m.modelName),
		m.fileCfg.Context.DefaultWindow,
	)
}

// summaryConverged reports whether a just-finished summarization brought
// the history back under the auto-summarize threshold. When it returns
// false the caller records the non-convergence (nonConvergentAt) rather
// than re-arming the auto gate outright — otherwise auto-summarize re-fires
// every turn against an irreducible floor (system prompt + the summary
// itself + the retained tail that already exceeds the window), a
// multi-minute compression each turn that never gets under the line. The
// record is per (fill, window), so it suppresses only pointless re-runs at
// the same fill and releases once the situation changes. A non-positive
// window or a disabled threshold (<=0 or >=1.0) means "no meaningful line
// to loop on," so it reports converged.
func summaryConverged(tokens, window int, autoThreshold float64) bool {
	if window <= 0 || autoThreshold <= 0 || autoThreshold >= 1.0 {
		return true
	}
	return float64(tokens) < autoThreshold*float64(window)
}

// autoSuppressedByNonConvergence reports whether auto-summarize should be
// held back because a prior summarize already failed to converge at ~this
// fill against ~this window. It is deliberately NOT a permanent latch (the
// bug it replaces): it releases once fill grows a step past the recorded
// stuck point — meaning the history kept growing and a fresh summary now
// has different material to work with — or once the window changes, which
// a /model switch or a drift pin can do and which makes the old verdict
// stale. nonConvergentAt <= 0 means "nothing on record."
func autoSuppressedByNonConvergence(pct float64, window int, nonConvergentAt float64, nonConvergentWindow int) bool {
	if nonConvergentAt <= 0 || window != nonConvergentWindow {
		return false
	}
	return pct < nonConvergentAt+watermarkStep
}

// dominantContextBucket names the largest of three coarse context
// consumers — the system prompt (incl. injected memory/skills text), the
// advertised tool schemas (incl. MCP), and the conversation history — so a
// non-convergence notice can point the user at what to trim instead of
// just reporting a percentage. It reuses the same primitives as /context
// (SplitMessages + registrySchemaTokens) rather than the full per-file
// breakdown, which is enough to choose the actionable advice. Returns the
// bucket label and its estimated tokens.
func (m Model) dominantContextBucket() (string, int) {
	sysTok, convoTok := contextwindow.SplitMessages(m.lockedMessages())
	schemaTok := registrySchemaTokens(m.cfg.Registry)
	name, tokens := "system prompt", sysTok
	if schemaTok > tokens {
		name, tokens = "tool schemas", schemaTok
	}
	if convoTok > tokens {
		name, tokens = "retained conversation", convoTok
	}
	return name, tokens
}

// ctxBarWidth is the cell count of the visual fill bar in the ctx
// segment. Six is wide enough to read at a glance and narrow enough
// to fit on terminals as small as ~50 columns when paired with the
// model + provider tags.
const ctxBarWidth = 6

// renderContextBar formats the `ctx ████░░ 4.3K / 128K (28%)` status
// segment. The bar is six cells of `█`/`░` — a full-block + light-
// shade pair renders reliably across monospace fonts. Earlier
// versions used `▓` which was inconsistent.
//
// Returns "" when the model's context window is unknown — the
// percentage would be misleading without a denominator. Threshold
// tiers paint the BAR (and the percentage) with: Content under
// warn_threshold, Warning amber once it crosses, Error red once it
// crosses auto_threshold. The `ctx` label renders in Content too so
// the whole status bar reads bright. Threshold knobs come from
// m.fileCfg.Context — same source the auto-summarize watermark
// reads, so the visual signal moves in lockstep with behavior.
func (m Model) renderContextBar() string {
	window := m.contextWindow()
	if window <= 0 {
		return ""
	}
	pctFloat := float64(m.contextTokens) / float64(window)
	pct := int(math.Round(pctFloat * 100))
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	color := colorContent
	switch {
	case m.fileCfg.Context.AutoThreshold < 1.0 && pctFloat >= m.fileCfg.Context.AutoThreshold:
		color = colorError
	case m.fileCfg.Context.WarnThreshold < 1.0 && pctFloat >= m.fileCfg.Context.WarnThreshold:
		color = colorWarning
	}

	filled := int(math.Round(pctFloat * float64(ctxBarWidth)))
	if filled < 0 {
		filled = 0
	}
	if filled > ctxBarWidth {
		filled = ctxBarWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", ctxBarWidth-filled)

	label := lipgloss.NewStyle().Foreground(colorContent).Render("ctx ")
	graph := lipgloss.NewStyle().Foreground(color).Render(
		bar + " " + formatTokens(m.contextTokens) + " / " + formatTokens(window) +
			fmt.Sprintf(" (%d%%)", pct))
	return label + graph
}

// formatTokens shrinks a raw token count to a status-bar-friendly
// width. Below 1K we show the exact number; between 1K and 10K we
// keep one decimal so the value still moves visibly turn-to-turn
// (1.2K → 1.3K); past 10K the decimal becomes noise so we drop it.
func formatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	case n < 1000000:
		return fmt.Sprintf("%dK", n/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
}
