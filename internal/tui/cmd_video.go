package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
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
	if fields[0] == "prompt" {
		goal := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		if goal == "" {
			return "", "/video"
		}
		return videoPromptDirective(goal), "/video prompt " + goal
	}
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

func videoPromptDirective(goal string) string {
	return fmt.Sprintf(`Run the yottacode prompt-driven marketing-video workflow.

Goal: %s

Use the public media tools, not shell commands, unless a tool reports a missing local dependency.

Workflow:
1. Infer the video type: release video, feature teaser, tutorial, social clip, or generic promo.
2. Gather referenced local assets and docs through normal read/search tools. Treat release notes, changelogs, screenshots, logos, and recordings as source material.
3. If the goal references media recordings, call media_probe on each recording and use media_analyze for candidate cuts when cleanup is needed.
4. Draft a storyboard before rendering. Include target duration, audience, hook, ordered segments, script/caption text, referenced assets, proposed output profiles, and any cut_ranges or keep_ranges.
5. Stop for user approval before rendering. Do not call media_compose or media_render until the user approves the exact storyboard and media ranges.
6. After approval, use media_compose to assemble title cards, screenshots/images, and approved clip segments into a draft MP4. Prefer branded templates, lower-third captions, simple fades, and image zoom/pan motion when they make the result clearer. Then use media_render for final YouTube/X/GIF profile exports when requested.

Phase 1 boundary:
- This is asset-based video creation: script, storyboard, title cards, captions, branded templates, simple motion, cuts, sequencing, and local ffmpeg rendering.
- Do not claim to generate synthetic video clips or product UI with hosted AI video models.
- Generated conceptual b-roll, TTS narration, and external media providers are optional future work, not part of this workflow unless the user explicitly supplies those assets.

Keep all output in the normal conversation.`, goal)
}

func videoHelpText() string {
	return `[video] yottacode marketing video workflow

Capabilities:
- probe local recordings with media_probe
- find fluff with media_analyze using audio silence and visual terminal-idle detectors
- propose cut ranges for review before rendering
- compose prompt-driven marketing videos with media_compose from title cards, screenshots, and clips, including branded templates, lower thirds, fades, and image zoom/pan motion
- render approved edits with media_render to YouTube/X MP4 profiles, larger/readable GIF previews, and optionally sped-up GIF loops

Examples:
- /video out/demo.mp4
- /video edit out/demo.mp4
- /video analyze out/demo.mp4
- /video prompt Create a 45-second release video from RELEASE_v0.3.0.md and marketing/raw/demo.mp4

Natural language still works: you can also type “analyze this demo video and propose marketing cuts”.`
}
