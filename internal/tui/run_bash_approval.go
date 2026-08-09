package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// renderRunBashApproval parses a run_bash tool call's JSON args and
// produces a multi-line rendering for the approval modal: each
// segment of a compound command on its own line, with risk-based
// color coding (red for destructive, yellow for caution, default for
// none). Returns the rendered body, the number of segments, and ok=
// true when the command was successfully parsed.
//
// cwd (when non-empty) is collapsed to "." inside each segment's text
// so a `cd /long/abs/path && grep ...` reads as `cd . && grep ...`.
// Display-only — the agent still sees the full command.
//
// On JSON parse failure the caller falls back to the original
// PreviewCall string.
func renderRunBashApproval(argsJSON, cwd string) (body string, segments int, ok bool) {
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
	var b strings.Builder
	if len(segs) == 1 {
		s := segs[0]
		line := shortenCwdInText(s.Text, cwd)
		if s.Risk != agent.RiskNone {
			line = renderRiskInline(s) + line
			if s.Reason != "" {
				line += "    " + styleApprovalReason(s.Risk).Render("⚠ "+s.Reason)
			}
		}
		b.WriteString(line)
	} else {
		for i, s := range segs {
			if i > 0 {
				b.WriteString("\n")
			}
			idx := fmt.Sprintf("  %d. ", i+1)
			sep := ""
			if s.Separator != "" {
				sep = styleApprovalSep.Render("(" + s.Separator + ") ")
			}
			text := truncSegment(shortenCwdInText(s.Text, cwd), 100)
			risk := renderRiskInline(s)
			reason := ""
			if s.Reason != "" {
				reason = "    " + styleApprovalReason(s.Risk).Render("⚠ "+s.Reason)
			}
			b.WriteString(idx + sep + risk + text + reason)
		}
	}
	// run_bash bypasses the file-tool path deny lists and has no sandbox, so
	// surface any credential-store or git-hook access the command reaches —
	// these render benign otherwise. Shown in all modes.
	for _, w := range agent.BashSensitivePathHits(a.Command, cwd) {
		b.WriteString("\n" + styleApprovalReason(agent.RiskCaution).Render("⚠ "+w))
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
