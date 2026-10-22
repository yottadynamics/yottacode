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
	return renderRunBashApprovalWidth(argsJSON, cwd, 0)
}

// defaultApprovalWrapWidth is the line budget used when the terminal width is
// unknown (width <= 0), e.g. test fixtures and very early frames.
const defaultApprovalWrapWidth = 76

// renderRunBashApprovalWidth is renderRunBashApproval with an explicit line
// budget (the box interior minus its left gutter). Content that exceeds it
// wraps with a hanging indent rather than truncating, so nothing the user is
// approving is hidden behind "…".
func renderRunBashApprovalWidth(argsJSON, cwd string, width int) (body string, segments int, ok bool) {
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
	if width <= 0 {
		width = defaultApprovalWrapWidth
	}
	if strings.Contains(strings.TrimSpace(a.Command), "\n") {
		return renderRunBashScript(a.Command, segs, cwd, width), len(segs), true
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
		b.WriteString(hangWrap(styleApprovalCommand.Render("$ "), line, width))
	} else {
		for i, s := range segs {
			if i > 0 {
				b.WriteString("\n")
			}
			idx := fmt.Sprintf("  %d. ", i+1)
			if i == 0 {
				// Keep the shell prompt visible for compound commands too; the
				// numbered rows are still command segments, not plain output.
				idx = styleApprovalCommand.Render("$ ") + idx
			}
			sep := ""
			if s.Separator != "" {
				sep = styleApprovalChoiceDim.Render("(" + s.Separator + ") ")
			}
			text := shortenCwdInText(s.Text, cwd)
			reason := ""
			if s.Reason != "" {
				reason = "    " + styleApprovalReason(s.Risk).Render("⚠ "+s.Reason)
			}
			b.WriteString(hangWrap(idx+sep, renderRiskInline(s)+text+reason, width))
		}
	}
	// Group related sensitive-path hits so the preview explains the
	// risk without turning every matched credential store into a
	// separate approval-looking row.
	for _, w := range groupedSensitiveWarnings(agent.BashSensitivePathHits(a.Command, cwd)) {
		for _, line := range strings.Split(wrapApprovalWarning(w, width-2), "\n") {
			b.WriteString("\n" + styleApprovalReason(agent.RiskCaution).Render("⚠ "+line))
		}
	}
	return b.String(), len(segs), true
}

// hangWrap hard-wraps text to width columns, putting prefix on the first row
// and an equal-width blank hanging indent on continuation rows.
func hangWrap(prefix, text string, width int) string {
	pw := ansi.StringWidth(prefix)
	if width > 0 && pw > width {
		prefix = ansi.Truncate(prefix, width, "")
		pw = ansi.StringWidth(prefix)
	}
	parts := strings.Split(ansi.Hardwrap(text, max(width-pw, 1), true), "\n")
	for i, part := range parts {
		if i == 0 {
			parts[i] = prefix + part
		} else {
			parts[i] = strings.Repeat(" ", pw) + part
		}
	}
	return strings.Join(parts, "\n")
}

// wrapApprovalWarning keeps warning text within the same display budget as
// command segments while preserving every path in the warning.
func wrapApprovalWarning(s string, max int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	if max < 1 {
		max = 1
	}
	var lines []string
	line := ""
	for _, word := range words {
		if ansi.StringWidth(word) > max {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			wrapped := strings.Split(ansi.Hardwrap(word, max, true), "\n")
			lines = append(lines, wrapped[:len(wrapped)-1]...)
			line = wrapped[len(wrapped)-1]
			continue
		}
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
		return styleApprovalReason(agent.RiskDestructive).Render("!! ")
	case agent.RiskCaution:
		return styleApprovalReason(agent.RiskCaution).Render("!  ")
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

// renderRunBashScript renders a multi-line command (script, heredoc, quoted
// multi-line string) as the code the user is approving: original indentation
// intact, a dim line-number gutter, long lines wrapped rather than truncated,
// and the flagged segments summarized once below instead of inline on every
// row. Splitting such commands into one "(;)" segment per line scrambled
// indentation and hid the content behind "…".
func renderRunBashScript(command string, segs []agent.CommandSegment, cwd string, width int) string {
	lines := strings.Split(strings.TrimRight(command, "\n"), "\n")
	gutterW := len(fmt.Sprint(len(lines)))
	// Gutter = number + space + 2-col risk marker.
	scriptWrapWidth := max(width-(gutterW+3), 1)
	flagged := scriptLineFlags(command, lines, segs)

	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		line = shortenCwdInText(strings.ReplaceAll(line, "\t", "    "), cwd)
		marker := styleApprovalChoiceDim.Render("  ")
		if f, ok := flagged[i]; ok {
			marker = styleApprovalReason(f.Risk).Render("! ")
			if f.Risk == agent.RiskDestructive {
				marker = styleApprovalReason(f.Risk).Render("!!")
			}
		}
		num := styleApprovalChoiceDim.Render(fmt.Sprintf("%*d ", gutterW, i+1))
		prefix := num + marker
		if i == 0 {
			// Mark the first row as shell input so a numbered script cannot be
			// mistaken for ordinary approval text.
			prefix = styleApprovalCommand.Render("$ ") + prefix
		}
		if width > 0 && ansi.StringWidth(prefix) > width {
			prefix = ansi.Truncate(prefix, width, "")
		}
		prefixW := ansi.StringWidth(prefix)
		cont := strings.Repeat(" ", min(gutterW+3, max(width, 0)))
		for j, part := range strings.Split(ansi.Hardwrap(line, max(width-prefixW, 1), true), "\n") {
			if j == 0 {
				b.WriteString(prefix + part)
			} else {
				b.WriteString("\n" + cont + part)
			}
		}
	}
	// One footer row per distinct reason, listing every line it applies to.
	type group struct {
		risk  agent.Risk
		lines []int
	}
	var order []string
	groups := map[string]*group{}
	for i := range lines {
		f, ok := flagged[i]
		if !ok || f.Reason == "" {
			continue
		}
		g := groups[f.Reason]
		if g == nil {
			g = &group{risk: f.Risk}
			groups[f.Reason] = g
			order = append(order, f.Reason)
		}
		g.lines = append(g.lines, i+1)
	}
	if len(order) > 0 {
		b.WriteString("\n")
	}
	for _, reason := range order {
		g := groups[reason]
		nums := make([]string, len(g.lines))
		for i, n := range g.lines {
			nums[i] = fmt.Sprint(n)
		}
		label := "line "
		if len(nums) > 1 {
			label = "lines "
		}
		text := "⚠ " + reason + " (" + label + strings.Join(nums, ", ") + ")"
		for _, l := range strings.Split(wrapApprovalWarning(text, scriptWrapWidth), "\n") {
			b.WriteString("\n" + styleApprovalReason(g.risk).Render(l))
		}
	}
	for _, w := range groupedSensitiveWarnings(agent.BashSensitivePathHits(command, cwd)) {
		for _, line := range strings.Split(wrapApprovalWarning(w, scriptWrapWidth), "\n") {
			b.WriteString("\n" + styleApprovalReason(agent.RiskCaution).Render("⚠ "+line))
		}
	}
	return b.String()
}

// scriptLineFlags maps each flagged segment back to the script line it starts
// on by scanning forward through the original command. Segments whose first
// line can't be located are skipped; the highest risk wins when several
// segments start on one line.
func scriptLineFlags(command string, lines []string, segs []agent.CommandSegment) map[int]agent.CommandSegment {
	flags := map[int]agent.CommandSegment{}
	lineStart := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		lineStart[i] = off
		off += len(l) + 1
	}
	pos := 0
	for _, s := range segs {
		first, _, _ := strings.Cut(s.Text, "\n")
		if first == "" {
			continue
		}
		idx := strings.Index(command[pos:], s.Text)
		matchLen := len(s.Text)
		if idx < 0 {
			idx = strings.Index(command[pos:], first)
			matchLen = len(first)
		}
		if idx < 0 {
			continue
		}
		at := pos + idx
		pos = at + matchLen
		if s.Risk == agent.RiskNone {
			continue
		}
		line := 0
		for i := range lineStart {
			if lineStart[i] <= at {
				line = i
			}
		}
		if prev, ok := flags[line]; !ok || s.Risk > prev.Risk {
			flags[line] = s
		}
	}
	return flags
}
