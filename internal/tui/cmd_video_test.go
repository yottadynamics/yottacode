package tui

import (
	"strings"
	"testing"
)

func TestSlash_VideoCommandRegistered(t *testing.T) {
	if findSlash("video") == nil {
		t.Fatalf("expected /video in slash registry")
	}
}

func TestVideoHelpTextNamesCapabilities(t *testing.T) {
	got := videoHelpText()
	for _, want := range []string{"media_probe", "media_analyze", "media_render", "audio silence", "visual terminal-idle", "GIF", "sped-up"} {
		if !strings.Contains(got, want) {
			t.Fatalf("video help missing %q:\n%s", want, got)
		}
	}
}

func TestVideoDirectiveSubmitsWorkflowPrompt(t *testing.T) {
	prompt, display := videoDirective([]string{"edit", "out/demo.mp4"})
	if display != "/video edit out/demo.mp4" {
		t.Fatalf("display = %q", display)
	}
	for _, want := range []string{"out/demo.mp4", "media_probe", `media_analyze with mode="auto"`, "Stop for user approval", "media_render", "youtube_16x9", "gif_preview_large", "speed=1.5"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("video directive missing %q:\n%s", want, prompt)
		}
	}
}

func TestSlash_VideoBarePrintsHelp(t *testing.T) {
	m := newTestModel(t)
	out, cmd := cmdVideo(m, nil)
	if cmd != nil {
		t.Fatalf("bare /video should not start a turn")
	}
	if !strings.Contains(out.transcript.String(), "[video] yottacode marketing video workflow") {
		t.Fatalf("bare /video should print help, got:\n%s", out.transcript.String())
	}
}

func TestSlash_VideoWithPathStartsTurn(t *testing.T) {
	m := newTestModel(t)
	out, _ := cmdVideo(m, []string{"out/demo.mp4"})
	if !strings.Contains(out.transcript.String(), "no provider configured") {
		t.Fatalf("/video path should reach startTurnWithDisplay, got:\n%s", out.transcript.String())
	}
}
