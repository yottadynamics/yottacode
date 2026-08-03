package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeMediaProbe(t *testing.T) {
	input := []byte(`{
		"format":{"duration":"12.34567","format_name":"mov,mp4,m4a,3gp,3g2,mj2"},
		"streams":[
			{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"30/1","tags":{"rotate":"90"}},
			{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2}
		]
	}`)
	got, err := summarizeMediaProbe(input)
	if err != nil {
		t.Fatalf("summarizeMediaProbe: %v", err)
	}
	for _, want := range []string{"duration: 12.346 seconds", "video[0]: codec=h264 1920x1080 fps=30/1 rotation=90", "audio[1]: codec=aac sample_rate=48000 channels=2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestParseSilenceDetect(t *testing.T) {
	stderr := `[silencedetect @ 0x1] silence_start: 1.25
[silencedetect @ 0x1] silence_end: 2.50 | silence_duration: 1.25
[silencedetect @ 0x1] silence_start: 5
[silencedetect @ 0x1] silence_end: 5.2 | silence_duration: 0.2`
	cuts := parseSilenceDetect(stderr, 0.5)
	if len(cuts) != 1 {
		t.Fatalf("cuts len = %d, want 1: %+v", len(cuts), cuts)
	}
	if cuts[0].Start != 1.25 || cuts[0].End != 2.50 {
		t.Fatalf("unexpected cut: %+v", cuts[0])
	}
}

func TestParseFreezeDetect(t *testing.T) {
	stderr := `[freezedetect @ 0x1] freeze_start: 10.4
[freezedetect @ 0x1] freeze_duration: 1.2
[freezedetect @ 0x1] freeze_end: 11.6
[freezedetect @ 0x1] freeze_start: 20
[freezedetect @ 0x1] freeze_end: 20.2`
	cuts := parseFreezeDetect(stderr, 0.5)
	if len(cuts) != 1 {
		t.Fatalf("cuts len = %d, want 1: %+v", len(cuts), cuts)
	}
	if cuts[0].Start != 10.4 || cuts[0].End != 11.6 {
		t.Fatalf("unexpected cut: %+v", cuts[0])
	}
}

func TestSelectMediaDetectorsAutoNoAudio(t *testing.T) {
	probe := mediaProbeJSON{Streams: []mediaStream{{CodecType: "video"}}}
	got := selectMediaDetectors("auto", nil, probe)
	if len(got) != 1 || got[0] != "visual_idle" {
		t.Fatalf("detectors = %#v, want visual_idle", got)
	}
}

func TestSelectMediaDetectorsAutoAudioVideo(t *testing.T) {
	probe := mediaProbeJSON{Streams: []mediaStream{{CodecType: "video"}, {CodecType: "audio"}}}
	got := selectMediaDetectors("auto", nil, probe)
	if strings.Join(got, ",") != "audio_silence,visual_idle" {
		t.Fatalf("detectors = %#v, want audio_silence,visual_idle", got)
	}
}

func TestRenderMediaCandidatesIncludesDetectorReason(t *testing.T) {
	got := renderMediaCandidates("terminal_demo", []string{"visual_idle"}, []mediaCandidate{{Range: mediaRange{Start: 1, End: 2.5}, Detector: "visual_idle", Reason: "unchanged frames / terminal idle", Confidence: 0.7}}, nil)
	for _, want := range []string{"mode: terminal_demo", "detectors: visual_idle", "detector=visual_idle", `reason="unchanged frames / terminal idle"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestCutsToKeepsMergesOverlappingCuts(t *testing.T) {
	got := cutsToKeeps([]mediaRange{{Start: 4, End: 6}, {Start: 1, End: 2}, {Start: 1.5, End: 3}}, 10)
	want := []mediaRange{{Start: 0, End: 1}, {Start: 3, End: 4}, {Start: 6, End: 10}}
	if len(got) != len(want) {
		t.Fatalf("keeps len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keep[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMediaConcatFilterVideoOnly(t *testing.T) {
	filter, err := mediaConcatFilter([]mediaRange{{Start: 0, End: 2}, {Start: 4, End: 7}}, "scale=1920:1080,format=yuv420p", false)
	if err != nil {
		t.Fatalf("mediaConcatFilter: %v", err)
	}
	for _, want := range []string{"trim=start=0.000:end=2.000", "concat=n=2:v=1:a=0", "[vcat]scale=1920:1080,format=yuv420p[vout]"} {
		if !strings.Contains(filter, want) {
			t.Fatalf("filter missing %q:\n%s", want, filter)
		}
	}
	if strings.Contains(filter, "[0:a]") {
		t.Fatalf("video-only concat filter must not reference audio:\n%s", filter)
	}
}

func TestBuildMediaRenderArgsMultiKeepRangesUsesConcat(t *testing.T) {
	args, err := buildMediaRenderArgs("in.mp4", "out.mp4", "youtube_16x9", mediaRenderArgs{Overwrite: true, HasAudio: false, KeepRanges: []mediaRange{{Start: 0, End: 1}, {Start: 2, End: 3}}})
	if err != nil {
		t.Fatalf("buildMediaRenderArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-filter_complex", "concat=n=2:v=1:a=0", "-map [vout]", "-an"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
}

func TestMediaOutputPathsMultipleProfiles(t *testing.T) {
	cwd := t.TempDir()
	got, err := mediaOutputPaths(cwd, "out/demo.mp4", []string{"youtube_16x9", "x_16x9"})
	if err != nil {
		t.Fatalf("mediaOutputPaths: %v", err)
	}
	want0 := filepath.Join(cwd, "out", "demo-youtube_16x9.mp4")
	want1 := filepath.Join(cwd, "out", "demo-x_16x9.mp4")
	if len(got) != 2 || got[0] != want0 || got[1] != want1 {
		t.Fatalf("outputs = %#v, want %#v %#v", got, want0, want1)
	}
}

func TestMediaOutputPathsGIFProfileUsesGifExtension(t *testing.T) {
	cwd := t.TempDir()
	got, err := mediaOutputPaths(cwd, "out/demo.mp4", []string{"youtube_16x9", "gif_preview"})
	if err != nil {
		t.Fatalf("mediaOutputPaths: %v", err)
	}
	wantGIF := filepath.Join(cwd, "out", "demo-gif_preview.gif")
	if len(got) != 2 || got[1] != wantGIF {
		t.Fatalf("outputs = %#v, want second %q", got, wantGIF)
	}

	single, err := mediaOutputPaths(cwd, "out/demo.mp4", []string{"gif_preview"})
	if err != nil {
		t.Fatalf("mediaOutputPaths single gif: %v", err)
	}
	if want := filepath.Join(cwd, "out", "demo.gif"); len(single) != 1 || single[0] != want {
		t.Fatalf("single gif output = %#v, want %q", single, want)
	}
}

func TestBuildMediaRenderArgsGIFPreviewUsesPalette(t *testing.T) {
	args, err := buildMediaRenderArgs("in.mp4", "out.gif", "gif_preview", mediaRenderArgs{Overwrite: true, KeepRanges: []mediaRange{{Start: 1, End: 3}}})
	if err != nil {
		t.Fatalf("buildMediaRenderArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-y", "-ss 1.000", "-to 3.000", "fps=12", "scale=960:-1:flags=lanczos", "palettegen", "paletteuse", "-loop 0", "out.gif"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("gif args missing %q:\n%s", want, joined)
		}
	}
}

func TestBuildMediaRenderArgsGIFPreviewMultiRangeUsesConcat(t *testing.T) {
	args, err := buildMediaRenderArgs("in.mp4", "out.gif", "gif_preview", mediaRenderArgs{Overwrite: true, KeepRanges: []mediaRange{{Start: 0, End: 1}, {Start: 2, End: 3}}})
	if err != nil {
		t.Fatalf("buildMediaRenderArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-filter_complex", "concat=n=2:v=1:a=0", "palettegen", "paletteuse", "-map [vout]"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("gif concat args missing %q:\n%s", want, joined)
		}
	}
}

func TestBuildMediaRenderArgsGIFPreviewLargeAndSpeed(t *testing.T) {
	args, err := buildMediaRenderArgs("in.mp4", "out.gif", "gif_preview_large", mediaRenderArgs{Overwrite: true, GIFWidth: 1600, GIFFPS: 10, Speed: 2})
	if err != nil {
		t.Fatalf("buildMediaRenderArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"fps=10", "scale=1600:-1:flags=lanczos", "setpts=PTS/2", "palettegen", "paletteuse"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("gif large/speed args missing %q:\n%s", want, joined)
		}
	}
}

func TestBuildMediaRenderArgsProfiles(t *testing.T) {
	args, err := buildMediaRenderArgs("in.mp4", "out.mp4", "x_vertical_9x16", mediaRenderArgs{Overwrite: true, KeepRanges: []mediaRange{{Start: 1, End: 3}}})
	if err != nil {
		t.Fatalf("buildMediaRenderArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-y", "-ss 1.000", "-to 3.000", "crop=1080:1920", "loudnorm=I=-16"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "/bin/sh") {
		t.Fatalf("render args must not invoke a shell: %s", joined)
	}
}

func TestMediaProbeMissingFFprobe(t *testing.T) {
	oldLookPath := mediaLookPath
	mediaLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { mediaLookPath = oldLookPath })

	cwd := t.TempDir()
	input := filepath.Join(cwd, "demo.mp4")
	if err := os.WriteFile(input, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &MediaProbeTool{Cwd: NewCwdRef(cwd)}
	_, err := tool.Execute(context.Background(), `{"path":"demo.mp4"}`)
	if err == nil || !strings.Contains(err.Error(), "ffprobe binary not found") {
		t.Fatalf("err = %v, want missing ffprobe", err)
	}
}

func TestMediaRenderPathsToSnapshot(t *testing.T) {
	cwd := t.TempDir()
	tool := &MediaRenderTool{Cwd: NewCwdRef(cwd)}
	paths := tool.PathsToSnapshot(cwd, `{"output":"dist/demo.mp4","profiles":["youtube_16x9","x_16x9"]}`)
	if len(paths) != 2 {
		t.Fatalf("paths len = %d, want 2: %#v", len(paths), paths)
	}
	if !strings.HasSuffix(paths[0], filepath.Join("dist", "demo-youtube_16x9.mp4")) {
		t.Fatalf("unexpected first path: %s", paths[0])
	}
}

func TestMediaRenderSchemaRequired(t *testing.T) {
	schema := (&MediaRenderTool{}).Schema()
	b, _ := json.Marshal(schema)
	if !strings.Contains(string(b), "youtube_16x9") || !strings.Contains(string(b), "cut_ranges") {
		t.Fatalf("schema missing expected media hints: %s", b)
	}
}

func TestMediaComposeSchemaRequired(t *testing.T) {
	schema := (&MediaComposeTool{}).Schema()
	b, _ := json.Marshal(schema)
	for _, want := range []string{"segments", "title", "clip", "image", "overwrite", "template", "motion", "transition", "caption"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("schema missing %q: %s", want, b)
		}
	}
}

func TestMediaComposeValidation(t *testing.T) {
	tests := []struct {
		name string
		args mediaComposeArgs
		want string
	}{
		{name: "output required", args: mediaComposeArgs{Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1}}}, want: "output is required"},
		{name: "mp4 output", args: mediaComposeArgs{Output: "out.mov", Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1}}}, want: "must end with .mp4"},
		{name: "segment required", args: mediaComposeArgs{Output: "out.mp4"}, want: "at least one segment"},
		{name: "title text", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "title", Duration: 1}}}, want: "title text"},
		{name: "image path", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "image", Duration: 1}}}, want: "image path"},
		{name: "clip path", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "clip"}}}, want: "clip path"},
		{name: "unknown type", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "audio"}}}, want: "unknown type"},
		{name: "duration too long", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: mediaComposeMaxSyntheticDuration + 1}}}, want: "exceeds max"},
		{name: "multiple keep ranges", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "clip", Path: "in.mp4", KeepRanges: []mediaRange{{Start: 0, End: 1}, {Start: 2, End: 3}}}}}, want: "at most one keep_range"},
		{name: "bad template", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1, Template: "sparkles"}}}, want: "template must be one of"},
		{name: "bad motion", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "image", Path: "card.png", Duration: 1, Motion: "spin"}}}, want: "motion must be one of"},
		{name: "bad transition", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1, Transition: "wipe"}}}, want: "transition must be one of"},
		{name: "motion on title", args: mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1, Motion: "zoom_in"}}}, want: "motion is only supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMediaComposeArgs(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestMediaComposeAllowsExplicitNoMotionOnNonImage(t *testing.T) {
	err := validateMediaComposeArgs(mediaComposeArgs{Output: "out.mp4", Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1, Motion: "none"}}})
	if err != nil {
		t.Fatalf("validateMediaComposeArgs: %v", err)
	}
}

func TestBuildMediaComposeArgsTitleImageClip(t *testing.T) {
	cwd := t.TempDir()
	args, err := buildMediaComposeArgs(cwd, filepath.Join(cwd, "out", "promo.mp4"), mediaComposeArgs{
		Output:    "out/promo.mp4",
		Overwrite: true,
		Segments: []mediaComposeSegment{
			{Type: "title", Text: "Ship: faster", Duration: 2, Template: "hero", Transition: "fade"},
			{Type: "image", Path: "assets/card.png", Duration: 3, Motion: "zoom_in", Caption: "Reusable skills"},
			{Type: "clip", Path: "raw/demo.mp4", KeepRanges: []mediaRange{{Start: 1, End: 4}}, Template: "feature", Caption: "Approved local footage"},
		},
	})
	if err != nil {
		t.Fatalf("buildMediaComposeArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-hide_banner", "-y", "color=c=#0b0f0d:s=1920x1080:r=30", "drawtext=text='Ship\\: faster'", "0x39ff14", "zoompan", "drawbox=x=80:y=ih-190", "Reusable skills", "fade=t=in", "-loop 1", filepath.Join(cwd, "assets", "card.png"), filepath.Join(cwd, "raw", "demo.mp4"), "trim=start=1.000:end=4.000", "concat=n=3:v=1:a=0", "-map [vout]", "-an", "-c:v libx264"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("compose args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "/bin/sh") {
		t.Fatalf("compose args must not invoke a shell: %s", joined)
	}
}

func TestEscapeFFmpegDrawTextDisablesExpansion(t *testing.T) {
	filter := mediaComposeTitleFilter(mediaComposeSegment{Type: "title", Text: "50% faster: it's ok"})
	for _, want := range []string{"50% faster\\: it\\'s ok", "expansion=none"} {
		if !strings.Contains(filter, want) {
			t.Fatalf("title filter missing %q:\n%s", want, filter)
		}
	}
}

func TestBuildMediaComposeArgsResetsUntrimmedClipPTS(t *testing.T) {
	cwd := t.TempDir()
	args, err := buildMediaComposeArgs(cwd, filepath.Join(cwd, "out.mp4"), mediaComposeArgs{
		Output: "out.mp4",
		Segments: []mediaComposeSegment{
			{Type: "title", Text: "Hi", Duration: 1},
			{Type: "clip", Path: "raw/demo.mp4"},
		},
	})
	if err != nil {
		t.Fatalf("buildMediaComposeArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "[1:v]setpts=PTS-STARTPTS") {
		t.Fatalf("untrimmed clip must reset PTS before concat:\n%s", joined)
	}
}

func TestBuildMediaComposeArgsRejectsInvalidCanvas(t *testing.T) {
	_, err := buildMediaComposeArgs(t.TempDir(), "out.mp4", mediaComposeArgs{
		Output:   "out.mp4",
		Width:    100000,
		Segments: []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "width") {
		t.Fatalf("err = %v, want width validation", err)
	}
}

func TestBuildMediaComposeArgsRejectsUnsafeBackgroundColor(t *testing.T) {
	_, err := buildMediaComposeArgs(t.TempDir(), "out.mp4", mediaComposeArgs{
		Output:          "out.mp4",
		BackgroundColor: "red:size=999x999",
		Segments:        []mediaComposeSegment{{Type: "title", Text: "Hi", Duration: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "background_color") {
		t.Fatalf("err = %v, want background_color validation", err)
	}
}

func TestMediaComposePathsToSnapshot(t *testing.T) {
	cwd := t.TempDir()
	tool := &MediaComposeTool{Cwd: NewCwdRef(cwd)}
	paths := tool.PathsToSnapshot(cwd, `{"output":"marketing/out/promo.mp4","segments":[{"type":"title","text":"Hi","duration":1}]}`)
	want := filepath.Join(cwd, "marketing", "out", "promo.mp4")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("paths = %#v, want %q", paths, want)
	}
}

func TestMediaComposeMissingFFmpeg(t *testing.T) {
	oldLookPath := mediaLookPath
	mediaLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { mediaLookPath = oldLookPath })

	cwd := t.TempDir()
	tool := &MediaComposeTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}}
	_, err := tool.Execute(context.Background(), `{"output":"out/promo.mp4","segments":[{"type":"title","text":"Hi","duration":1}]}`)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg binary not found") {
		t.Fatalf("err = %v, want missing ffmpeg", err)
	}
}
