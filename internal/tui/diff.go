package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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

// renderEditDiff produces a colored before/after view for an edit_file
// approval prompt. Each old line is labeled `-`, each new line `+`,
// and the body of each line is run through the syntax highlighter
// using the file's extension as a language hint — keywords, strings,
// and comments get distinct colors so reviewing a code change is
// faster than reading monochrome text.
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

	// Highlight whole blocks at once so chroma sees enough context to
	// classify tokens correctly. We split on newlines after the fact
	// to apply the +/- markers; the embedded ANSI codes survive.
	oldHL := HighlightFromPath(a.OldString, a.Path)
	newHL := HighlightFromPath(a.NewString, a.Path)

	var b strings.Builder
	b.WriteString(header + "\n")
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

	// Highlight whole blocks at once so chroma sees enough context to
	// classify tokens correctly; we split on newlines after the fact to
	// apply the +/- markers — the embedded ANSI codes survive.
	oldHL := HighlightFromPath(a.OldString, a.Path)
	newHL := HighlightFromPath(a.NewString, a.Path)

	var rows []string
	emit := func(markStyle lipgloss.Style, marker, hl string) {
		lines := strings.Split(strings.TrimRight(hl, "\n"), "\n")
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
	emit(styleDiffDel, "-", oldHL)
	emit(styleDiffAdd, "+", newHL)

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
