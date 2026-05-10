package tui

import (
	"fmt"
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	m.contextTokens = contextwindow.EstimateTokens(m.sess.Messages)
}

// updateContextUsage recomputes context-window usage after each turn,
// emits a one-time warning notice if the warn_threshold was just
// crossed, and returns a tea.Cmd that runs auto-summarization if the
// auto_threshold was crossed. The Model is mutated to remember the
// last percentage observed so we don't spam the same notice.
//
// Returns a non-nil tea.Cmd when auto-summarization should fire — the
// caller must sequence it after any other commands queued for this
// tick (typically session-save and recall-index).
func (m *Model) updateContextUsage() tea.Cmd {
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
		m.lastWatermarkPct = pct
		return m.startAutoSummarize(pct)
	}

	if warnThr < 1.0 && pct >= warnThr {
		// Fire if we've never warned, or if we've crossed another
		// 5% step since the last notice.
		if m.lastWatermarkPct == 0 || pct >= m.lastWatermarkPct+watermarkStep {
			m.lastWatermarkPct = pct
			m.appendLine(styleWatermark.Render(
				fmt.Sprintf("· context at %d%% — consider /summarize", int(pct*100))))
		}
	} else if warnThr < 1.0 && pct < warnThr {
		// Below threshold (after /summarize, /clear, or a /sessions
		// Resume): reset so the next crossing fires fresh.
		m.lastWatermarkPct = 0
	}
	return nil
}

// contextWindow returns the resolved capacity for the current model,
// honoring both yottacode's known-model table and the user's
// context.default_window override.
func (m Model) contextWindow() int {
	return contextwindow.WindowFor(m.modelName, m.fileCfg.Context.DefaultWindow)
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
// tiers paint the BAR (and the percentage) with: Dim under
// warn_threshold, Warning amber once it crosses, Error red once it
// crosses auto_threshold. The `ctx` label stays Dim regardless
// (it's a label, not a value). Threshold knobs come from
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

	color := colorDim
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

	label := lipgloss.NewStyle().Foreground(colorDim).Render("ctx ")
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
