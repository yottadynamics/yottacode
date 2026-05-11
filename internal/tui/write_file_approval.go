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

// renderWriteFileApprovalSummary produces a one-line "what's about to
// be written" header for the approval modal body — just path + byte
// count + line count. The full file content is emitted to scrollback
// separately by emitWriteFileBodyToScrollback so the modal can stay
// small enough for the hotkeys to dominate the eye.
//
// Returns ("", false) if argsJSON doesn't shape like write_file args;
// the caller falls back to the tool's PreviewCall string.
func renderWriteFileApprovalSummary(argsJSON string) (string, bool) {
	var a writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", false
	}
	if a.Path == "" {
		return "", false
	}
	lines := strings.Count(a.Content, "\n") + 1
	if a.Content == "" {
		lines = 0
	}
	return stylePathHeader.Render(a.Path) + " " +
		styleSplashInfo.Render(fmt.Sprintf("(%d bytes · %d lines)", len(a.Content), lines)), true
}

// emitWriteFileBodyToScrollback writes the full (untruncated) file
// content to scrollback before the approval modal opens — labeled
// header + syntax-highlighted body as plain rows. Same flat-text
// treatment as emitPlanBodyToScrollback: no card wrap, so wide source
// lines wrap naturally on the terminal without fighting an inner
// gutter. The content persists in scrollback after the modal
// dismisses regardless of decision.
func emitWriteFileBodyToScrollback(m *Model, argsJSON string) {
	var a writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return
	}
	if a.Path == "" || a.Content == "" {
		return
	}
	lines := strings.Count(a.Content, "\n") + 1
	// Leading blank line separates the header from whatever
	// preceded it in scrollback, so the emit doesn't visually fuse
	// with prior tool cards or messages.
	m.appendLine("")
	m.appendLine(styleAuto.Render("[write_file]") + " " +
		stylePathHeader.Render(a.Path) + " " +
		styleSplashInfo.Render(fmt.Sprintf("(%d bytes · %d lines)", len(a.Content), lines)) + ":")
	m.appendLine("")
	highlighted := strings.TrimRight(HighlightFromPath(a.Content, a.Path), "\n")
	for _, line := range strings.Split(highlighted, "\n") {
		m.appendLine(line)
	}
	m.appendLine("")
}
