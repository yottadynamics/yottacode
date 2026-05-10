package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Tool-output cards: one shape for every tool call, regardless of
// kind. Header carries the invocation + duration; body carries up to
// `cardBodyLineCap` rendered lines from the tool output (with a
// trailing "…" notice when truncated); footer summarizes the result
// — entry count, bytes written, exit code, etc.
//
// The card replaces the older two-line `▸ start` / `↳ summary` shape.
// Once the user learns this shape they can scan a long conversation
// by skimming card headers.
//
//	╭ list_dir(.)                                                 9ms
//	│ bin/
//	│ pkg/
//	│ src/
//	╰ 3 entries
//
// Per-tool customizers (toolBodyLines, toolFooter) extract a
// readable body + footer summary from the raw tool output. Falls
// back to a generic "first N lines + N total lines" shape for
// unknown tools so a new tool drops in cleanly without code changes.

const (
	cardBodyLineCap   = 10  // max body lines before we truncate with "…"
	cardMaxWidthCap   = 120 // hard cap; finer cap is terminalWidth - 4
	cardMinUsefulCols = 40  // width below which we don't bother padding the duration
)

// renderToolCard renders a complete tool-call card from the tool's
// invocation preview, output, and error state. termWidth is the
// current terminal width (m.width) — the card auto-fits.
//
// argsJSON is the raw tool-call arguments (forwarded from
// agent.ToolStart). edit_file uses it to render a proper old/new diff
// body; other tools ignore it and fall back to the standard text-body
// shape.
func renderToolCard(toolName, preview, argsJSON, output string, errored bool, termWidth int) string {
	width := cardMaxWidthCap
	if termWidth > 0 && termWidth-4 < width {
		width = termWidth - 4
	}
	if width < cardMinUsefulCols {
		width = cardMinUsefulCols
	}

	header := renderCardHeader(preview)
	footer := toolFooter(toolName, output, errored)

	out := []string{header}
	// edit_file gets a structured diff body with bg-tinted +/- lines.
	// We bypass the generic text-body path because the body lines carry
	// their own (highlighted) styling — they shouldn't be wrapped in
	// styleCardBody, which would override the syntax colors.
	if !errored && toolName == "edit_file" {
		if rows, ok := editFileDiffRows(argsJSON, width); ok {
			out = append(out, rows...)
			out = append(out, styleCardGutter.Render("╰ ")+footer)
			return strings.Join(out, "\n")
		}
	}
	body := toolBodyLines(toolName, output, errored)
	if len(body) > cardBodyLineCap {
		visible := body[:cardBodyLineCap]
		hidden := len(body) - cardBodyLineCap
		for _, line := range visible {
			out = append(out, styleCardGutter.Render("│ ")+styleCardBody.Render(line))
		}
		out = append(out, styleCardGutter.Render("│ ")+styleCardMeta.Render(fmt.Sprintf("…%d more line(s)", hidden)))
	} else {
		for _, line := range body {
			out = append(out, styleCardGutter.Render("│ ")+styleCardBody.Render(line))
		}
	}
	out = append(out, styleCardGutter.Render("╰ ")+footer)
	return strings.Join(out, "\n")
}

// renderCardHeader composes "╭ <preview>". The trailing duration tag
// that used to right-align here was removed — fast tool calls
// dominate (sub-second), and the live thinking row already shows
// elapsed time during long ones, so the per-card timestamp was just
// chrome.
func renderCardHeader(preview string) string {
	return styleCardGutter.Render("╭ ") + styleCardHeader.Render(preview)
}

// toolBodyLines extracts the displayable body for a given tool. Returns
// a slice of plain strings (no per-line gutter applied — the caller
// adds the `│ ` prefix). Each tool gets a tailored shape:
//
//   - list_dir: drop the f/d/l marker column; append `/` to directories
//   - list_project_structure: drop marker/size/mtime; append `/` to dirs
//   - run_bash: stdout-only, with stderr appended below an "── stderr ──" hint when present
//   - read_file: empty body — the model needs the content; the user
//     just needs to see "we read this file" — the footer's line/byte
//     count is enough at-a-glance signal. Dumping the first 10 lines
//     was visual noise that pushed real progress out of view.
//   - write_file: empty body — the footer "wrote N bytes" tells the whole story
//   - edit_file / git: raw output (often a unified diff or status text)
//   - glob / grep: raw output (one match per line)
//   - default: raw output trimmed of trailing whitespace
//
// Errored output is shown as-is so the user sees the error message.
func toolBodyLines(toolName, output string, errored bool) []string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return nil
	}
	if errored {
		return strings.Split(output, "\n")
	}
	switch toolName {
	case "list_dir":
		return listDirBody(output)
	case "list_project_structure":
		return listProjectStructureBody(output)
	case "run_bash":
		return runBashBody(output)
	case "read_file", "write_file":
		// Footer carries the relevant summary; body adds noise.
		return nil
	}
	return strings.Split(output, "\n")
}

// toolFooter produces the styled summary line at the card's bottom-edge.
// Format depends on tool: list_dir = "N entries", write_file =
// "wrote N bytes to path", run_bash = "exit N" (green if 0, red
// otherwise), default = a one-line truncated preview of output.
func toolFooter(toolName, output string, errored bool) string {
	if errored {
		summary := summarizeToolOutput(output)
		return styleCardErrFooter.Render("✗ " + summary)
	}
	switch toolName {
	case "list_dir":
		return styleCardMeta.Render(listDirFooter(output))
	case "list_project_structure":
		return styleCardMeta.Render(listDirFooter(output))
	case "run_bash":
		return runBashFooter(output)
	case "read_file":
		return styleCardMeta.Render(readFileFooter(output))
	case "write_file":
		return styleCardMeta.Render(strings.TrimSpace(output))
	case "edit_file":
		// edit_file's output is "edited <path>: N replacement(s)" — the
		// whole story belongs in the footer (the diff body above already
		// carries the visual change).
		return styleCardMeta.Render(strings.TrimSpace(output))
	case "glob":
		return styleCardMeta.Render(genericMatchFooter(output, "match"))
	case "grep":
		return styleCardMeta.Render(genericMatchFooter(output, "match"))
	}
	return styleCardMeta.Render("done")
}

// listDirBody parses the f/d/l\tname format the list_dir tool emits
// and renders one entry per line with a trailing slash on directories.
// Permissions, sizes, and timestamps are dropped — the model has the
// raw output; the user just needs to skim what's there.
func listDirBody(out string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// Truncation marker the tool itself emits — keep it visible.
		if strings.HasPrefix(line, "…") {
			entries = append(entries, line)
			continue
		}
		marker, name, ok := strings.Cut(line, "\t")
		if !ok {
			entries = append(entries, line)
			continue
		}
		if marker == "d" {
			name += "/"
		}
		entries = append(entries, name)
	}
	return entries
}

// listProjectStructureBody parses the marker\tsize\tmtime\trelpath
// format the list_project_structure tool emits and renders one
// relpath per line, with a trailing slash on directories. The size
// and mtime columns are dropped for the same reason listDirBody
// drops them — the model already has the raw output; the user is
// just skimming what's there. Relpaths from depth ≥ 2 already
// contain `/` separators, so a tree listing reads as a sorted file
// inventory rather than a parsed-and-redrawn tree.
func listProjectStructureBody(out string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// Truncation marker the tool itself emits — keep it visible.
		if strings.HasPrefix(line, "…") {
			entries = append(entries, line)
			continue
		}
		// Format: marker \t size \t mtime \t relpath. SplitN with n=4
		// keeps any literal tab inside a relpath intact (defensive —
		// regular paths don't contain tabs, but trusting the input
		// shape is cheap).
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			entries = append(entries, line)
			continue
		}
		name := parts[3]
		if parts[0] == "d" {
			name += "/"
		}
		entries = append(entries, name)
	}
	return entries
}

// readFileFooter summarizes the read in a single dim line — line count
// and byte count, with a "(truncated)" marker when the tool's output
// hit the 512 KiB read cap. The body itself is empty (see toolBodyLines)
// so this footer is the only signal the user gets that the read
// happened and how much it returned.
func readFileFooter(out string) string {
	truncated := strings.HasSuffix(out, "\n…[truncated]")
	if truncated {
		out = strings.TrimSuffix(out, "\n…[truncated]")
	}
	bytes := len(out)
	lines := strings.Count(out, "\n")
	if bytes > 0 && !strings.HasSuffix(out, "\n") {
		lines++
	}
	noun := "lines"
	if lines == 1 {
		noun = "line"
	}
	body := fmt.Sprintf("%d %s · %d bytes", lines, noun, bytes)
	if truncated {
		body += " (truncated)"
	}
	return body
}

// listDirFooter counts entries (excluding the truncation marker) and
// returns "N entries".
func listDirFooter(out string) string {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "…") {
			continue
		}
		n++
	}
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

// runBashBody returns just the stdout portion of run_bash output. The
// tool emits "exit=N\n--- stdout ---\n...\n--- stderr ---\n..."; we
// strip the framing and append a "── stderr ──" hint plus the stderr
// body when present (so the user sees both streams).
func runBashBody(out string) []string {
	exit, stdout, stderr := parseRunBashOutput(out)
	_ = exit
	var lines []string
	if stdout != "" {
		lines = append(lines, strings.Split(stdout, "\n")...)
	}
	if stderr != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "── stderr ──")
		lines = append(lines, strings.Split(stderr, "\n")...)
	}
	return lines
}

// runBashFooter returns "exit N" colored Success when N=0, Error
// otherwise.
func runBashFooter(out string) string {
	exit, _, _ := parseRunBashOutput(out)
	tag := fmt.Sprintf("exit %d", exit)
	if exit == 0 {
		return styleCardOKFooter.Render(tag)
	}
	return styleCardErrFooter.Render(tag)
}

// parseRunBashOutput pulls the exit code, stdout, and stderr out of
// run_bash's combined "exit=N\n--- stdout ---\n…\n--- stderr ---\n…"
// envelope. Best-effort: if the format doesn't match, returns zero
// exit and the full output as stdout so we still render something
// useful.
//
// The separators look for the marker without requiring a trailing
// newline so an empty trailing section (e.g., empty stderr) still
// parses cleanly after the surrounding TrimRight.
func parseRunBashOutput(out string) (exit int, stdout, stderr string) {
	out = strings.TrimRight(out, "\n")
	const stdoutSep = "\n--- stdout ---"
	const stderrSep = "\n--- stderr ---"
	exitPart, rest, ok := strings.Cut(out, stdoutSep)
	if !ok {
		return 0, out, ""
	}
	if _, err := fmt.Sscanf(exitPart, "exit=%d", &exit); err != nil {
		exit = 0
	}
	stdout, stderr, _ = strings.Cut(rest, stderrSep)
	stdout = strings.TrimPrefix(stdout, "\n")
	stderr = strings.TrimPrefix(stderr, "\n")
	return exit, strings.TrimRight(stdout, "\n"), strings.TrimRight(stderr, "\n")
}

// genericMatchFooter counts non-empty lines and returns "N <noun>(s)".
// Used by glob/grep where each line is one match.
func genericMatchFooter(out, noun string) string {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// Card-specific styles. The gutter (╭ │ ╰) renders Muted (decorative);
// the header preview is Content (the value the user cares about);
// metadata (duration, footer) renders Dim by default with state
// colors for OK / Error footers.
var (
	styleCardGutter    = lipgloss.NewStyle().Foreground(colorRule)
	styleCardHeader    = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleCardBody      = lipgloss.NewStyle().Foreground(colorContent)
	styleCardMeta      = lipgloss.NewStyle().Foreground(colorDim)
	styleCardOKFooter  = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleCardErrFooter = lipgloss.NewStyle().Foreground(colorError).Bold(true)
)
