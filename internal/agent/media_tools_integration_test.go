//go:build integration

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMediaComposeRendersMixedAspectRatioSources is an end-to-end regression
// test for the concat-SAR-mismatch bug: it builds a real title+clip composition
// where the clip's source resolution does not divide the canvas evenly and
// verifies ffmpeg can render the composition.
func TestMediaComposeRendersMixedAspectRatioSources(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	cwd := t.TempDir()
	clip := filepath.Join(cwd, "clip.mp4")
	genArgs := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=red:s=1078x1918:r=30", "-t", "1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", clip}
	if out, err := exec.Command("ffmpeg", genArgs...).CombinedOutput(); err != nil {
		t.Fatalf("generate source clip: %v: %s", err, out)
	}

	output := filepath.Join(cwd, "out.mp4")
	args, err := buildMediaComposeArgs(cwd, output, mediaComposeArgs{
		Output:    "out.mp4",
		Overwrite: true,
		Segments: []mediaComposeSegment{
			{Type: "title", Text: "Hi", Duration: 1},
			{Type: "clip", Path: "clip.mp4"},
		},
	})
	if err != nil {
		t.Fatalf("buildMediaComposeArgs: %v", err)
	}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg failed: %v: %s", err, out)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("output not written: %v", err)
	}
}

// TestMediaRenderToolExecuteRendersRealFile drives MediaRenderTool.Execute end
// to end with real ffmpeg and explicit MaxThreads/RenderTimeout for MP4 and
// GIF profiles. Unit tests cover argv shape; this integration test proves those
// resource-bound commands run to completion with ffmpeg.
func TestMediaRenderToolExecuteRendersRealFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	cwd := t.TempDir()
	input := filepath.Join(cwd, "in.mp4")
	genArgs := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=15", "-t", "2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", input}
	if out, err := exec.Command("ffmpeg", genArgs...).CombinedOutput(); err != nil {
		t.Fatalf("generate source clip: %v: %s", err, out)
	}

	tool := &MediaRenderTool{
		Cwd:           NewCwdRef(cwd),
		WriteOpts:     WritePathOptions{Cwd: NewCwdRef(cwd)},
		MaxThreads:    2,
		RenderTimeout: 30 * time.Second,
	}

	t.Run("mp4 profile", func(t *testing.T) {
		argsJSON := `{"input":"in.mp4","output":"out.mp4","overwrite":true,"profiles":["youtube_16x9"]}`
		got, err := tool.Execute(context.Background(), argsJSON)
		if err != nil {
			t.Fatalf("Execute: %v: %s", err, got)
		}
		if _, err := os.Stat(filepath.Join(cwd, "out.mp4")); err != nil {
			t.Fatalf("output not written: %v", err)
		}
	})

	t.Run("gif profile", func(t *testing.T) {
		argsJSON := `{"input":"in.mp4","output":"out.gif","overwrite":true,"profiles":["gif_preview"]}`
		got, err := tool.Execute(context.Background(), argsJSON)
		if err != nil {
			t.Fatalf("Execute: %v: %s", err, got)
		}
		if _, err := os.Stat(filepath.Join(cwd, "out.gif")); err != nil {
			t.Fatalf("output not written: %v", err)
		}
	})
}
