package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
		// Keep the command preview below the modal's narrowest practical
		// width. The outer approval renderer cannot safely recover from a
		// styled line that already extends beyond the box.
		b.WriteString(truncSegment(line, 72))
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
			text := truncSegment(shortenCwdInText(s.Text, cwd), 72)
			risk := renderRiskInline(s)
			reason := ""
			if s.Reason != "" {
				reason = "    " + styleApprovalReason(s.Risk).Render("⚠ "+s.Reason)
			}
			b.WriteString(idx + sep + risk + truncSegment(text+reason, 72))
		}
	}
	// Group related sensitive-path hits so the preview explains the
	// risk without turning every matched credential store into a
	// separate approval-looking row.
	for _, w := range groupedSensitiveWarnings(agent.BashSensitivePathHits(a.Command, cwd)) {
		for _, line := range strings.Split(wrapApprovalWarning(w, 72), "\n") {
			b.WriteString("\n" + styleApprovalReason(agent.RiskCaution).Render("⚠ "+line))
		}
	}
	return b.String(), len(segs), true
}

// truncSegment keeps the visible command preview within max terminal columns
// without splitting a rune or a wide (CJK/emoji) cell.
func truncSegment(s string, max int) string {
	if ansi.StringWidth(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return ansi.Truncate(s, max, "…")
}

// wrapApprovalWarning keeps warning text within the same display budget as
// command segments while preserving every path in the warning.
func wrapApprovalWarning(s string, max int) string {
	words := strings.Fields(s)
	if len(words) == 0 || max <= 0 {
		return s
	}
	var lines []string
	line := ""
	for _, word := range words {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if ansi.StringWidth(candidate) > max && line != "" {
			lines = append(lines, line)
			line = word
		} else {
			line = candidate
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// recursive command reaches several credential stores at once.
func groupedSensitiveWarnings(hits []string) []string {
	var reads []string
	var other []string
	seen := map[string]bool{}
	for _, hit := range hits {
		if strings.HasPrefix(hit, "reads ") {
			path := strings.TrimPrefix(hit, "reads ")
			if !seen[path] {
				seen[path] = true
				reads = append(reads, path)
			}
			continue
		}
		if !seen[hit] {
			seen[hit] = true
			other = append(other, hit)
		}
	}
	out := make([]string, 0, 2)
	if len(reads) > 0 {
		out = append(out, "reads "+strings.Join(reads, ", "))
	}
	return append(out, other...)
}

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
