package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// Prompt 2 styling. Two tones: an "accept" color for the inline
// confirmation that lands in scrollback when the user grants the
// elevation, and a "reject" tone for the deny acknowledgment.
// Both stay plain text per [[feedback_no_emoji_in_ui]].
var (
	stylePathTrustBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("214")).Padding(0, 1)
	stylePathTrustTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	stylePathTrustBodyKey   = lipgloss.NewStyle().Bold(true)
	stylePathTrustBodyHint  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	stylePathTrustAccept    = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	stylePathTrustReject    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// renderPathTrustModal builds the inline elevation prompt
// (Prompt 2 in yottacode-roadmap/folder-trust.md). The body
// names the requested path, the workspace boundary, and the
// three choices. Three options, three hotkeys each:
//
//	[1 / O] Allow once         — grant just this exact path
//	[2 / T] Trust for session  — grant the containing directory
//	[3 / N] Reject             — surface ErrPathOutsideWorkspace
//
// The modal stays narrow and plain-text; no emoji.
func renderPathTrustModal(m Model) string {
	req := m.pathTrustReq
	width := liveContentWidth(m.width)
	if width < 40 {
		width = 40
	}

	var b strings.Builder
	fmt.Fprintln(&b, stylePathTrustTitle.Render("Write outside workspace"))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Requested path:")
	fmt.Fprintln(&b, "  "+req.Path)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Session workspace:")
	fmt.Fprintln(&b, "  "+req.Cwd)
	if len(req.AllowedRoots) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "Existing --allow-paths roots:")
		for _, r := range req.AllowedRoots {
			fmt.Fprintln(&b, "  "+r)
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, stylePathTrustBodyKey.Render("[1] Allow once")+stylePathTrustBodyHint.Render("        — trust just this file for the session"))
	fmt.Fprintln(&b, stylePathTrustBodyKey.Render("[2] Trust for session")+stylePathTrustBodyHint.Render(" — trust this directory and every subfolder"))
	fmt.Fprint(&b, stylePathTrustBodyKey.Render("[3] Reject")+stylePathTrustBodyHint.Render("           — model sees the original error"))

	return stylePathTrustBorder.Width(width - 2).Render(b.String())
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
