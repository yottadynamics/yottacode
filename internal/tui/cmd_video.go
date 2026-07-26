package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdVideo is a guided marketing-video entry point. It does not introduce a
// separate editor UI; it submits a workflow prompt so the ordinary agent loop,
// media tools, and approval gates remain the single execution path.
func cmdVideo(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[video] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	prompt, display := videoDirective(args)
	if display == "/video" {
		m.appendLine(videoHelpText())
		return m, nil
	}
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

func videoDirective(args []string) (string, string) {
	trimmed := strings.TrimSpace(strings.Join(args, " "))
	if trimmed == "" || trimmed == "help" || trimmed == "--help" || trimmed == "-h" {
		return "", "/video"
	}
	fields := strings.Fields(trimmed)
	mode := "edit"
	path := trimmed
	if len(fields) > 1 {
		switch fields[0] {
		case "edit", "analyze", "render":
			mode = fields[0]
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		}
	}
	if path == "" {
		return "", "/video"
	}
	return fmt.Sprintf(`Run the yottacode marketing-video workflow for %q.

Intent: %s.

Use the public media tools, not shell commands, unless a tool reports a missing local dependency:
1. Call media_probe on the file and summarize the stream metadata.
2. Call media_analyze with mode="auto". If the probe shows a silent terminal demo or the auto analysis skips audio, also consider mode="terminal_demo" when you need tighter visual-idle candidates.
3. Propose an edit plan with candidate cut ranges and explain why each range should or should not be removed. Be conservative: do not remove meaningful terminal output, command results, or transitions the viewer needs.
4. Stop for user approval before rendering. Do not call media_render until the user approves exact cut_ranges or keep_ranges.
5. After approval, call media_render with the approved ranges and profiles ["youtube_16x9", "x_16x9"]. If the user asks for a shareable GIF or teaser loop, include "gif_preview" or "gif_preview_large" too; use speed=1.5 or speed=2 when the clip has slow terminal pauses and the user wants it sped up. Use a clear output base path under out/.

Keep all output in the normal conversation.`, path, mode), "/video " + trimmed
}

func videoHelpText() string {
	return `[video] yottacode marketing video workflow

Capabilities:
- probe local recordings with media_probe
- find fluff with media_analyze using audio silence and visual terminal-idle detectors
- propose cut ranges for review before rendering
- render approved edits with media_render to YouTube/X MP4 profiles, larger/readable GIF previews, and optionally sped-up GIF loops

Examples:
- /video out/demo.mp4
- /video edit out/demo.mp4
- /video analyze out/demo.mp4

Natural language still works: you can also type “analyze this demo video and propose marketing cuts”.`
}
