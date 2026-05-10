package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// writeFileArgs mirrors agent.WriteFileTool's expected arguments.
// Re-declared here for the same reason editArgs is in diff.go —
// reaching into the agent package just for a struct shape would be an
// awkward dependency.
type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// renderWriteFileApproval produces a syntax-highlighted preview of the
// content a write_file call is about to write, plus a header showing
// the destination path and byte count. Truncates the previewed content
// to a screen-sensible number of lines so a 5000-line write doesn't
// blow out the modal — full content still flows through the tool's
// Execute when approved.
//
// Returns ("", false) if the JSON doesn't parse or path/content are
// empty; the caller falls back to the tool's PreviewCall string.
func renderWriteFileApproval(argsJSON string) (string, bool) {
	var a writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", false
	}
	if a.Path == "" {
		return "", false
	}

	header := stylePathHeader.Render(a.Path) + " " +
		styleSplashInfo.Render(fmt.Sprintf("(%d bytes)", len(a.Content)))

	const maxPreviewLines = 30
	body := a.Content
	lines := strings.Split(body, "\n")
	truncated := false
	if len(lines) > maxPreviewLines {
		lines = lines[:maxPreviewLines]
		body = strings.Join(lines, "\n")
		truncated = true
	}

	highlighted := HighlightFromPath(body, a.Path)

	var b strings.Builder
	b.WriteString(header + "\n")
	b.WriteString(highlighted)
	if truncated {
		b.WriteString("\n" + styleSplashInfo.Render(
			fmt.Sprintf("… (%d more lines, full content writes on approval)",
				strings.Count(a.Content, "\n")+1-maxPreviewLines)))
	}
	return strings.TrimRight(b.String(), "\n"), true
}
