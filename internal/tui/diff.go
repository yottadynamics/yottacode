package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// editArgs mirrors agent.EditFileTool's expected arguments. We re-declare
// them here because reaching into the agent package just for a struct shape
// would be an awkward dependency.
type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// intralineSpan returns the rune offsets [delStart, delEnd) in oldLine
// and [addStart, addEnd) in newLine of the differing middle once the
// common prefix and suffix are stripped. ok=false when the lines are
// identical or when the change covers (nearly) the whole line — in
// both cases span emphasis adds nothing over the line color.
func intralineSpan(oldLine, newLine string) (delStart, delEnd, addStart, addEnd int, ok bool) {
	if oldLine == newLine {
		return 0, 0, 0, 0, false
	}
	or, nr := []rune(oldLine), []rune(newLine)
	p := 0
	for p < len(or) && p < len(nr) && or[p] == nr[p] {
		p++
	}
	s := 0
	for s < len(or)-p && s < len(nr)-p && or[len(or)-1-s] == nr[len(nr)-1-s] {
		s++
	}
	delStart, delEnd = p, len(or)-s
	addStart, addEnd = p, len(nr)-s
	// A fully (or nearly fully) rewritten line should read as a plain
	// -/+ pair — reversing 90% of a line is louder than reversing none.
	longer := max(len(or), len(nr))
	changed := max(delEnd-delStart, addEnd-addStart)
	if longer == 0 || float64(changed)/float64(longer) > 0.8 {
		return 0, 0, 0, 0, false
	}
	return delStart, delEnd, addStart, addEnd, true
}

// renderSpanRow styles one side of a paired replacement: unchanged
// prefix/suffix in the base state color, the changed span in the
// reverse-video emphasis style.
func renderSpanRow(line string, start, end int, base, emph lipgloss.Style) string {
	rs := []rune(line)
	out := base.Render(string(rs[:start]))
	if end > start {
		out += emph.Render(string(rs[start:end]))
	}
	return out + base.Render(string(rs[end:]))
}

// intralineDiffLines renders the old/new blocks as styled -/+ content
// lines (no marker, no gutter — callers add those) with per-line
// intraline emphasis on the changed span. Pairing applies only when
// the blocks have the same line count — the common case for targeted
// edit_file replacements (line i of old pairs with line i of new).
// ok=false otherwise; callers fall back to the chroma-highlighted
// block path, which handles asymmetric changes better than a bad
// pairing would.
//
// Paired lines trade syntax highlighting for change precision: content
// renders in the +/- state color (the convention every diff viewer
// uses) so the reversed span is the only loud thing on the row.
func intralineDiffLines(oldStr, newStr string) (delRows, addRows []string, ok bool) {
	oldLines := strings.Split(strings.TrimRight(oldStr, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(newStr, "\n"), "\n")
	if len(oldLines) != len(newLines) {
		return nil, nil, false
	}
	for i := range oldLines {
		ds, de, as, ae, spanOK := intralineSpan(oldLines[i], newLines[i])
		if spanOK {
			delRows = append(delRows, renderSpanRow(oldLines[i], ds, de, styleDiffDelBody, styleDiffDelEmph))
			addRows = append(addRows, renderSpanRow(newLines[i], as, ae, styleDiffAddBody, styleDiffAddEmph))
		} else {
			delRows = append(delRows, styleDiffDelBody.Render(oldLines[i]))
			addRows = append(addRows, styleDiffAddBody.Render(newLines[i]))
		}
	}
	return delRows, addRows, true
}

// renderEditDiff produces a colored before/after view for an edit_file
// approval prompt. Each old line is labeled `-`, each new line `+`.
// Same-line-count replacements get intraline emphasis (the changed
// span in reverse video) so a one-token edit is spottable at a glance;
// asymmetric changes run each block through the syntax highlighter
// using the file's extension as a language hint instead.
//
// Returns ("", false) if argsJSON isn't shaped like edit_file args.
func renderEditDiff(argsJSON string) (string, bool) {
	var a editArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", false
	}
	if a.Path == "" || a.OldString == "" {
		return "", false
	}

	header := stylePathHeader.Render(a.Path)
	if a.ReplaceAll {
		header += " " + lipgloss.NewStyle().Foreground(colorMuted).Render("(replace_all)")
	}

	var b strings.Builder
	b.WriteString(header + "\n")

	if delRows, addRows, ok := intralineDiffLines(a.OldString, a.NewString); ok {
		for _, row := range delRows {
			fmt.Fprintln(&b, styleDiffDel.Render("- ")+row)
		}
		for _, row := range addRows {
			fmt.Fprintln(&b, styleDiffAdd.Render("+ ")+row)
		}
		return strings.TrimRight(b.String(), "\n"), true
	}

	// Highlight whole blocks at once so chroma sees enough context to
	// classify tokens correctly. We split on newlines after the fact
	// to apply the +/- markers; the embedded ANSI codes survive.
	oldHL := HighlightFromPath(a.OldString, a.Path)
	newHL := HighlightFromPath(a.NewString, a.Path)

	for _, line := range strings.Split(strings.TrimRight(oldHL, "\n"), "\n") {
		fmt.Fprintln(&b, styleDiffDel.Render("- ")+line)
	}
	for _, line := range strings.Split(strings.TrimRight(newHL, "\n"), "\n") {
		fmt.Fprintln(&b, styleDiffAdd.Render("+ ")+line)
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// editFileDiffRows builds the body rows of the unified tool card for an
// edit_file invocation: the gutter prefix plus a `- old` / `+ new` row
// for each line of the change, with the content syntax-highlighted via
// Chroma using the file's extension as the lexer hint.
//
// Returns ok=false when argsJSON isn't shaped like edit_file args; the
// caller falls back to the generic text-body card path.
//
// Rendering invariants:
//   - Every line of the deleted block gets its own `- ` marker; every
//     line of the added block gets its own `+ ` marker. Markers are
//     bold + state-colored (red / green foreground) so they pop against
//     the surrounding text.
//   - Content uses foreground-only state coloring (no bg tint) — the
//     marker glyph carries the add/remove signal and Chroma's per-token
//     `\x1b[0m` resets compose cleanly when there's no bg to preserve.
//   - Total visible rows are capped at cardBodyLineCap; overflow shows a
//     dim "…N more line(s)" notice on a final row.
//   - Each row carries the `│ ` card body gutter (gutter glyph + 1
//     space) so it composes cleanly with the rest of the card chrome
//     and aligns under the header/footer text at column 2.
func editFileDiffRows(argsJSON string, cardWidth int) ([]string, bool) {
	var a editArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, false
	}
	if a.Path == "" || a.OldString == "" {
		return nil, false
	}

	innerWidth := cardWidth - 2 // subtract `│ ` (gutter glyph + 1-space body indent)
	if innerWidth < 4 {
		innerWidth = 4
	}

	var rows []string
	emitLines := func(markStyle lipgloss.Style, marker string, lines []string) {
		markerCellW := 2 // "- " or "+ "
		contentW := innerWidth - markerCellW
		if contentW < 1 {
			contentW = 1
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > contentW {
				line = ansi.Truncate(line, contentW, "…")
			}
			rows = append(rows,
				styleCardGutter.Render("│ ")+markStyle.Render(marker+" ")+line)
		}
	}

	if delRows, addRows, ok := intralineDiffLines(a.OldString, a.NewString); ok {
		// Paired replacement: per-line intraline emphasis (changed span
		// in reverse video) instead of chroma highlighting.
		emitLines(styleDiffDel, "-", delRows)
		emitLines(styleDiffAdd, "+", addRows)
	} else {
		// Asymmetric change: highlight whole blocks at once so chroma
		// sees enough context to classify tokens correctly; we split on
		// newlines after the fact to apply the +/- markers — the
		// embedded ANSI codes survive.
		oldHL := HighlightFromPath(a.OldString, a.Path)
		newHL := HighlightFromPath(a.NewString, a.Path)
		emitLines(styleDiffDel, "-", strings.Split(strings.TrimRight(oldHL, "\n"), "\n"))
		emitLines(styleDiffAdd, "+", strings.Split(strings.TrimRight(newHL, "\n"), "\n"))
	}

	if len(rows) > cardBodyLineCap {
		hidden := len(rows) - cardBodyLineCap
		rows = append(rows[:cardBodyLineCap],
			styleCardGutter.Render("│ ")+styleCardMeta.Render(fmt.Sprintf("…%d more line(s)", hidden)))
	}
	return rows, true
}

// writeArgs mirrors agent.WriteFileTool's expected arguments. Same
// rationale as editArgs — local re-declaration avoids pulling the agent
// package in just for a struct shape.
type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// writeFileBodyRows builds the body rows of the unified tool card for a
// write_file invocation. The whole content is rendered as `+` lines so
// it reads as a pure addition — a new file (or an overwrite) is, from
// the diff perspective, "everything is new". Mirrors editFileDiffRows's
// shape so write and edit cards scan the same way.
//
// Returns ok=false when argsJSON isn't shaped like write_file args; the
// caller falls back to the generic text-body card path.
func writeFileBodyRows(argsJSON string, cardWidth int) ([]string, bool) {
	var a writeArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, false
	}
	if a.Path == "" {
		return nil, false
	}

	innerWidth := cardWidth - 2 // subtract `│ ` (gutter glyph + 1-space body indent)
	if innerWidth < 4 {
		innerWidth = 4
	}

	hl := HighlightFromPath(a.Content, a.Path)

	var rows []string
	lines := strings.Split(strings.TrimRight(hl, "\n"), "\n")
	markerCellW := 2 // "+ "
	contentW := innerWidth - markerCellW
	if contentW < 1 {
		contentW = 1
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > contentW {
			line = ansi.Truncate(line, contentW, "…")
		}
		rows = append(rows,
			styleCardGutter.Render("│ ")+styleDiffAdd.Render("+ ")+line)
	}

	if len(rows) > cardBodyLineCap {
		hidden := len(rows) - cardBodyLineCap
		rows = append(rows[:cardBodyLineCap],
			styleCardGutter.Render("│ ")+styleCardMeta.Render(fmt.Sprintf("…%d more line(s)", hidden)))
	}
	return rows, true
}
