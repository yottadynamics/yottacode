package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// Prompt 2 styling. Two tones: an "accept" color for the inline
// confirmation that lands in scrollback when the user grants the
// elevation, and a "reject" tone for the deny acknowledgment.
// Both stay plain text per [[feedback_no_emoji_in_ui]].
//
// Bare declarations — see approval_modal.go's note: initializers
// here would freeze colors at package init and skip every
// ApplyTheme rebuild. (The originals hardcoded 256-color indices,
// so this modal ignored /themes entirely.) Construction lives in
// buildStyles (styles.go).
var (
	stylePathTrustBorder   lipgloss.Style
	stylePathTrustTitle    lipgloss.Style
	stylePathTrustBodyKey  lipgloss.Style
	stylePathTrustBodyHint lipgloss.Style
	stylePathTrustAccept   lipgloss.Style
	stylePathTrustReject   lipgloss.Style
)

// renderPathTrustModal builds the inline elevation prompt
// (Prompt 2 in yottacode-roadmap/folder-trust.md). The body
// names the requested path, the workspace boundary, and the
// three choices in the same labeled-box family as approvals and
// plan decisions. Three options, three hotkeys each:
//
//	[1] allow once      trust just this file for the session
//	[2] trust session   trust this directory and every subfolder
//	[3] reject          model sees the original error
//
// The modal stays narrow and plain-text; no emoji.
func renderPathTrustModal(m Model) string {
	req := m.pathTrustReq
	capW := capLabeledBoxWidth(m.width)

	var body []string
	appendWrapped := func(line string) {
		for _, wrapped := range hardWrapLabeled(line, capW) {
			body = append(body, labeledBoxIndent+wrapped)
		}
	}
	body = append(body, "")
	appendWrapped(stylePathTrustBodyKey.Render("requested"))
	appendWrapped(req.Path)
	body = append(body, "")
	appendWrapped(stylePathTrustBodyKey.Render("workspace"))
	appendWrapped(req.Cwd)
	if len(req.AllowedRoots) > 0 {
		body = append(body, "")
		appendWrapped(stylePathTrustBodyKey.Render("allowed roots"))
		for _, r := range req.AllowedRoots {
			appendWrapped(r)
		}
	}
	body = append(body, "")
	for _, line := range pathTrustHotkeys() {
		appendWrapped(line)
	}
	body = append(body, "")

	leftLabel := " " + stylePathTrustTitle.Render("Write outside workspace") + " "
	right := req.ToolName
	if strings.TrimSpace(right) == "" {
		right = "write_file"
	}
	rightLabel := " " + styleApprovalTool.Render(right) + " "
	return renderLabeledBox(leftLabel, rightLabel, body, capW, colorWarning)
}

func pathTrustHotkeys() []string {
	return []string{
		stylePathTrustBodyKey.Render("[1] allow once") + stylePathTrustBodyHint.Render("      trust just this file for the session"),
		stylePathTrustBodyKey.Render("[2] trust session") + stylePathTrustBodyHint.Render("   trust this directory and every subfolder"),
		stylePathTrustBodyKey.Render("[3] reject") + stylePathTrustBodyHint.Render("          model sees the original error"),
	}
}

// addAllowedPathToWriteTools appends path to WriteOpts.AllowedPaths
// on every registered write/edit tool. Mirrors
// applyPlanFileToWriteTools — registry-stored tools are
// pointer-typed, so the in-flight agent goroutine sees the new
// slice on its next ValidateWritePath call.
//
// Tools without a WriteOpts (read-only, run_bash, git_*) are
// silently skipped via the type-switch's default arm.
func addAllowedPathToWriteTools(reg *agent.Registry, path string) {
	if reg == nil || path == "" {
		return
	}
	for _, name := range []string{"write_file", "edit_file", "apply_diff", "mkdir", "copy_file", "move_file", "delete_file"} {
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		switch tt := t.(type) {
		case *agent.WriteFileTool:
			tt.WriteOpts.AllowedPaths = appendDedup(tt.WriteOpts.AllowedPaths, path)
		case *agent.EditFileTool:
			tt.WriteOpts.AllowedPaths = appendDedup(tt.WriteOpts.AllowedPaths, path)
		case *agent.ApplyDiffTool:
			tt.WriteOpts.AllowedPaths = appendDedup(tt.WriteOpts.AllowedPaths, path)
		case *agent.MkdirTool:
			tt.WriteOpts.AllowedPaths = appendDedup(tt.WriteOpts.AllowedPaths, path)
		case *agent.CopyFileTool:
			tt.WriteOpts.AllowedPaths = appendDedup(tt.WriteOpts.AllowedPaths, path)
		case *agent.MoveFileTool:
			tt.WriteOpts.AllowedPaths = appendDedup(tt.WriteOpts.AllowedPaths, path)
		case *agent.DeleteFileTool:
			tt.WriteOpts.AllowedPaths = appendDedup(tt.WriteOpts.AllowedPaths, path)
		}
	}
}

func appendDedup(slice []string, s string) []string {
	for _, x := range slice {
		if x == s {
			return slice
		}
	}
	return append(slice, s)
}
