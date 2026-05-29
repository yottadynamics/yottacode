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
// content to scrollback before the approval modal opens, framed as a
// tool card (╭ header / │ body / ╰ footer) so it reads like the
// post-execution cards the user already knows. Body lines carry their
// own chroma-emitted ANSI colors and are emitted with only the gutter
// prefix — they are NOT wrapped in styleCardBody, which would override
// the syntax colors (same approach editFileDiffRows takes for the
// edit_file diff body). Tabs are expanded to 4 spaces up-front because
// ansi.StringWidth treats `\t` as 0-width while the terminal expands
// it, and the gutter alignment depends on width math being honest.
//
// The footer reads "awaiting approval" so the preview card is
// distinguishable from the post-write confirmation card (╰ wrote N
// bytes) that emits later via the standard renderToolCard path.
//
// The content persists in scrollback after the modal dismisses
// regardless of approve/deny — same emit-once-on-ApprovalNeeded
// contract as emitPlanBodyToScrollback.
func emitWriteFileBodyToScrollback(m *Model, argsJSON string) {
	var a writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return
	}
	if a.Path == "" || a.Content == "" {
		return
	}
	lines := strings.Count(a.Content, "\n") + 1
	// Leading blank line separates the card from whatever preceded
	// it in scrollback, so the emit doesn't visually fuse with prior
	// tool cards or messages.
	m.appendLine("")
	m.appendLine(styleCardGutter.Render("╭ ") +
		styleCardHeader.Render("Write("+a.Path+")") + " " +
		styleCardMeta.Render(fmt.Sprintf("(%d bytes · %d lines)", len(a.Content), lines)))
	content := strings.ReplaceAll(a.Content, "\t", "    ")
	highlighted := strings.TrimRight(HighlightFromPath(content, a.Path), "\n")
	gutter := styleCardGutter.Render("│ ")
	for _, line := range strings.Split(highlighted, "\n") {
		m.appendLine(gutter + line)
	}
	m.appendLine(styleCardGutter.Render("╰ ") + styleCardMeta.Render("awaiting approval"))
}
