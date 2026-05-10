package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// renderRunBashApproval parses a run_bash tool call's JSON args and
// produces a multi-line rendering for the approval modal: each
// segment of a compound command on its own line, with risk-based
// color coding (red for destructive, yellow for caution, default for
// none). Returns the rendered body, the number of segments, and ok=
// true when the command was successfully parsed.
//
// On JSON parse failure the caller falls back to the original
// PreviewCall string.
func renderRunBashApproval(argsJSON string) (body string, segments int, ok bool) {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Command == "" {
		return "", 0, false
	}
	segs := agent.SplitCommand(a.Command)
	if len(segs) == 0 {
		return "", 0, false
	}
	if len(segs) == 1 {
		// Single command — keep the modal compact, mirror PreviewCall
		// shape but include risk reason if any.
		s := segs[0]
		line := s.Text
		if s.Risk != agent.RiskNone {
			line = renderRiskInline(s) + line
			if s.Reason != "" {
				line += "    " + styleApprovalReason(s.Risk).Render("⚠ "+s.Reason)
			}
		}
		return line, 1, true
	}
	// Compound: number each segment, show separator before all but the
	// first, color-code by risk. Width-cap each line so a runaway
	// segment doesn't blow the modal.
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			b.WriteString("\n")
		}
		idx := fmt.Sprintf("  %d. ", i+1)
		sep := ""
		if s.Separator != "" {
			sep = styleApprovalSep.Render("("+s.Separator+") ")
		}
		text := truncSegment(s.Text, 100)
		risk := renderRiskInline(s)
		reason := ""
		if s.Reason != "" {
			reason = "    " + styleApprovalReason(s.Risk).Render("⚠ "+s.Reason)
		}
		b.WriteString(idx + sep + risk + text + reason)
	}
	return b.String(), len(segs), true
}

func truncSegment(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// renderRiskInline returns a leading marker for a segment based on its
// risk level. Empty for RiskNone so safe segments don't get noisy.
func renderRiskInline(s agent.CommandSegment) string {
	switch s.Risk {
	case agent.RiskDestructive:
		return styleApprovalReason(agent.RiskDestructive).Render("🚨 ")
	case agent.RiskCaution:
		return styleApprovalReason(agent.RiskCaution).Render("⚠ ")
	default:
		return ""
	}
}

func styleApprovalReason(risk agent.Risk) lipgloss.Style {
	switch risk {
	case agent.RiskDestructive:
		return lipgloss.NewStyle().Foreground(colorErr).Bold(true)
	case agent.RiskCaution:
		return lipgloss.NewStyle().Foreground(colorWarn)
	default:
		return lipgloss.NewStyle()
	}
}

var styleApprovalSep = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
