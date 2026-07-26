package main

import (
	"os"
	"strings"
	"testing"
)

func TestProbeMediaDoctorReportsInstalledAndMissing(t *testing.T) {
	old := mediaDoctorLookPath
	mediaDoctorLookPath = func(command string) (string, error) {
		switch command {
		case "ffmpeg", "ffprobe":
			return "/usr/bin/" + command, nil
		default:
			return "", os.ErrNotExist
		}
	}
	t.Cleanup(func() { mediaDoctorLookPath = old })

	got := probeMediaDoctor(t.Context())
	if !got.FFmpeg.Installed || !got.FFprobe.Installed {
		t.Fatalf("expected ffmpeg/ffprobe installed: %+v", got)
	}
	if len(got.Issues) != 0 {
		t.Fatalf("issues = %+v, want none", got.Issues)
	}
	if len(got.Warnings) == 0 {
		t.Fatalf("expected optional transcription warning")
	}
}

func TestRenderMediaDoctorShowsHints(t *testing.T) {
	out := renderMediaDoctor(MediaDoctorResult{
		FFmpeg:  MediaDoctorBinary{Command: "ffmpeg", Required: true, InstallHint: "install ffmpeg"},
		FFprobe: MediaDoctorBinary{Command: "ffprobe", Required: true, Installed: true, Path: "/bin/ffprobe"},
		Issues:  []string{"ffmpeg is required"},
	})
	for _, want := range []string{"Media Editing:", "ffmpeg: missing", "hint: install ffmpeg", "issue: ffmpeg is required"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q:\n%s", want, out)
		}
	}
}
