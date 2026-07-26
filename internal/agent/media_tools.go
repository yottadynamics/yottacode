package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const mediaMaxOutputBytes = 256 * 1024

var mediaLookPath = exec.LookPath

type mediaExecFunc func(context.Context, string, ...string) *exec.Cmd

var mediaCommandContext mediaExecFunc = exec.CommandContext

// MediaProbeTool inspects a media file through ffprobe without sending the
// media bytes to the model. The result is a compact metadata snapshot the agent
// can use to plan edits and pick output profiles.
type MediaProbeTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string
}

func (t *MediaProbeTool) Name() string { return "media_probe" }
func (t *MediaProbeTool) Description() string {
	return "Inspect a local audio/video file with ffprobe and return compact stream metadata for planning edits. Requires ffprobe on PATH."
}
func (t *MediaProbeTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Audio/video file to inspect"},
		},
		"required": []string{"path"},
	}
}
func (t *MediaProbeTool) RequiresApproval(string) bool { return false }
func (t *MediaProbeTool) ParallelSafe(string) bool     { return true }
func (t *MediaProbeTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("media_probe(%s)", a.Path)
}
func (t *MediaProbeTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("media_probe: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("media_probe: path is required")
	}
	input := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateReadPath(input, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("media_probe: %w", err)
	}
	if _, err := mediaLookPath("ffprobe"); err != nil {
		return "", errors.New("media_probe: ffprobe binary not found in PATH; install ffmpeg/ffprobe first")
	}
	args := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", input}
	cmd := mediaCommandContext(ctx, "ffprobe", args...)
	cmd.Dir = t.Cwd.Get()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{buf: &stdout, max: mediaMaxOutputBytes}
	cmd.Stderr = &capped{buf: &stderr, max: 32 * 1024}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("media_probe: ffprobe failed: %w; stderr=%q", err, stderr.String())
	}
	return summarizeMediaProbe(stdout.Bytes())
}

// MediaAnalyzeTool runs one public analysis surface over separate internal
// detectors. Audio-backed demos use silence detection; silent terminal demos use
// visual freeze/idle detection; auto mode picks the useful detectors from the
// file's streams.
type MediaAnalyzeTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string
}

func (t *MediaAnalyzeTool) Name() string { return "media_analyze" }
func (t *MediaAnalyzeTool) Description() string {
	return "Analyze a local media file with ffmpeg and return unified candidate fluff cuts from audio silence and/or visual idle detectors. Requires ffmpeg on PATH; auto mode falls back to visual analysis for silent videos."
}
func (t *MediaAnalyzeTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":                   map[string]any{"type": "string", "description": "Audio/video file to analyze"},
			"mode":                   map[string]any{"type": "string", "description": "Analysis mode: auto, audio_silence, visual_idle, terminal_demo, or all (default auto)"},
			"detectors":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional detector override: audio_silence, visual_idle"},
			"silence_threshold_db":   map[string]any{"type": "number", "description": "Silence threshold in dB (default -35)"},
			"min_silence_duration":   map[string]any{"type": "number", "description": "Minimum audio silence duration in seconds (default 0.6)"},
			"visual_noise_threshold": map[string]any{"type": "number", "description": "Freeze/idle visual noise threshold (default 0.003; terminal_demo uses 0.0015)"},
			"min_idle_duration":      map[string]any{"type": "number", "description": "Minimum visual idle duration in seconds (default 1.0; terminal_demo uses 0.8)"},
		},
		"required": []string{"path"},
	}
}
func (t *MediaAnalyzeTool) RequiresApproval(string) bool { return false }
func (t *MediaAnalyzeTool) ParallelSafe(string) bool     { return true }
func (t *MediaAnalyzeTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Mode) == "" {
		return fmt.Sprintf("media_analyze(%s)", a.Path)
	}
	return fmt.Sprintf("media_analyze(%s mode=%s)", a.Path, a.Mode)
}
func (t *MediaAnalyzeTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a mediaAnalyzeArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("media_analyze: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("media_analyze: path is required")
	}
	threshold := a.SilenceThresholdDB
	if threshold == 0 {
		threshold = -35
	}
	minSilence := a.MinSilenceDuration
	if minSilence <= 0 {
		minSilence = 0.6
	}
	mode := strings.TrimSpace(a.Mode)
	if mode == "" {
		mode = "auto"
	}
	visualNoise := a.VisualNoiseThreshold
	if visualNoise <= 0 {
		visualNoise = 0.003
	}
	minIdle := a.MinIdleDuration
	if minIdle <= 0 {
		minIdle = 1.0
	}
	if mode == "terminal_demo" {
		if a.VisualNoiseThreshold <= 0 {
			visualNoise = 0.0015
		}
		if a.MinIdleDuration <= 0 {
			minIdle = 0.8
		}
	}
	input := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateReadPath(input, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("media_analyze: %w", err)
	}
	if _, err := mediaLookPath("ffmpeg"); err != nil {
		return "", errors.New("media_analyze: ffmpeg binary not found in PATH; install ffmpeg first")
	}
	probe, _ := probeMediaFile(ctx, t.Cwd.Get(), input)
	detectors := selectMediaDetectors(mode, a.Detectors, probe)
	var candidates []mediaCandidate
	var notes []string
	for _, detector := range detectors {
		switch detector {
		case "audio_silence":
			if !probe.hasAudio() {
				notes = append(notes, "audio_silence skipped: no audio stream")
				continue
			}
			cuts, err := runAudioSilenceDetect(ctx, t.Cwd.Get(), input, threshold, minSilence)
			if err != nil {
				notes = append(notes, err.Error())
				continue
			}
			for _, c := range cuts {
				candidates = append(candidates, mediaCandidate{Range: c, Detector: "audio_silence", Reason: "audio silence", Confidence: 0.85})
			}
		case "visual_idle":
			if !probe.hasVideo() {
				notes = append(notes, "visual_idle skipped: no video stream")
				continue
			}
			cuts, err := runVisualFreezeDetect(ctx, t.Cwd.Get(), input, visualNoise, minIdle)
			if err != nil {
				notes = append(notes, err.Error())
				continue
			}
			for _, c := range cuts {
				candidates = append(candidates, mediaCandidate{Range: c, Detector: "visual_idle", Reason: "unchanged frames / terminal idle", Confidence: 0.7})
			}
		}
	}
	return renderMediaCandidates(mode, detectors, candidates, notes), nil
}

type mediaAnalyzeArgs struct {
	Path                 string   `json:"path"`
	Mode                 string   `json:"mode"`
	Detectors            []string `json:"detectors"`
	SilenceThresholdDB   float64  `json:"silence_threshold_db"`
	MinSilenceDuration   float64  `json:"min_silence_duration"`
	VisualNoiseThreshold float64  `json:"visual_noise_threshold"`
	MinIdleDuration      float64  `json:"min_idle_duration"`
}

type mediaCandidate struct {
	Range      mediaRange
	Detector   string
	Reason     string
	Confidence float64
}

// MediaRenderTool renders approved edit ranges into platform-specific outputs.
// It shells out to ffmpeg with argv-only construction; no filter text is ever
// interpreted by a shell.
type MediaRenderTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string
	WriteOpts     WritePathOptions
}

func (t *MediaRenderTool) Name() string { return "media_render" }
func (t *MediaRenderTool) Description() string {
	return "Render an approved video edit plan with ffmpeg into YouTube and/or X profiles. Requires ffmpeg on PATH."
}
func (t *MediaRenderTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input":         map[string]any{"type": "string", "description": "Source media file"},
			"output":        map[string]any{"type": "string", "description": "Output path, or base path when rendering multiple profiles"},
			"profiles":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Output profiles: youtube_16x9, x_16x9, x_vertical_9x16, gif_preview, gif_preview_large"},
			"gif_width":     map[string]any{"type": "integer", "description": "Optional GIF width override in pixels for GIF profiles"},
			"gif_fps":       map[string]any{"type": "number", "description": "Optional GIF frame rate override for GIF profiles"},
			"speed":         map[string]any{"type": "number", "description": "Optional playback speed multiplier for GIF profiles, e.g. 1.5 or 2.0"},
			"cut_ranges":    mediaRangeSchema("Ranges to remove, in seconds"),
			"keep_ranges":   mediaRangeSchema("Ranges to keep, in seconds; overrides cut_ranges"),
			"captions_path": map[string]any{"type": "string", "description": "Optional .srt/.ass captions file to burn in"},
			"intro_path":    map[string]any{"type": "string", "description": "Optional intro clip to prepend"},
			"outro_path":    map[string]any{"type": "string", "description": "Optional outro clip to append"},
			"overwrite":     map[string]any{"type": "boolean", "description": "Allow replacing existing output files (default false)"},
		},
		"required": []string{"input", "output"},
	}
}
func mediaRangeSchema(desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "object", "properties": map[string]any{"start": map[string]any{"type": "number"}, "end": map[string]any{"type": "number"}}, "required": []string{"start", "end"}}}
}
func (t *MediaRenderTool) RequiresApproval(string) bool { return true }
func (t *MediaRenderTool) PreviewCall(argsJSON string) string {
	var a mediaRenderArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	profiles := a.Profiles
	if len(profiles) == 0 {
		profiles = []string{"youtube_16x9"}
	}
	return fmt.Sprintf("media_render(%s -> %s profiles=%s)", a.Input, a.Output, strings.Join(profiles, ","))
}
func (t *MediaRenderTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a mediaRenderArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Output == "" {
		return nil
	}
	outs, err := mediaOutputPaths(cwd, a.Output, a.Profiles)
	if err != nil {
		return nil
	}
	return outs
}
func (t *MediaRenderTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a mediaRenderArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("media_render: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Input) == "" || strings.TrimSpace(a.Output) == "" {
		return "", errors.New("media_render: input and output are required")
	}
	if _, err := mediaLookPath("ffmpeg"); err != nil {
		return "", errors.New("media_render: ffmpeg binary not found in PATH; install ffmpeg first")
	}
	input := resolvePath(t.Cwd.Get(), a.Input)
	if err := ValidateReadPath(input, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("media_render: input: %w", err)
	}
	for label, p := range map[string]string{"captions_path": a.CaptionsPath, "intro_path": a.IntroPath, "outro_path": a.OutroPath} {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if err := ValidateReadPath(resolvePath(t.Cwd.Get(), p), t.DenyReadPaths); err != nil {
			return "", fmt.Errorf("media_render: %s: %w", label, err)
		}
	}
	outs, err := mediaOutputPaths(t.Cwd.Get(), a.Output, a.Profiles)
	if err != nil {
		return "", fmt.Errorf("media_render: %w", err)
	}
	for _, out := range outs {
		if err := ValidateWritePath(out, t.WriteOpts); err != nil {
			return "", fmt.Errorf("media_render: output: %w", err)
		}
		if !a.Overwrite {
			if _, err := os.Stat(out); err == nil {
				return "", fmt.Errorf("media_render: output %q already exists; set overwrite=true to replace it", out)
			}
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", fmt.Errorf("media_render: mkdir output dir: %w", err)
		}
	}
	probe, _ := probeMediaFile(ctx, t.Cwd.Get(), input)
	a.HasAudio = probe.hasAudio()
	if len(a.KeepRanges) == 0 && len(a.CutRanges) > 0 {
		duration := probe.durationSeconds()
		if duration <= 0 {
			return "", errors.New("media_render: cut_ranges require a readable media duration; use keep_ranges instead")
		}
		a.KeepRanges = cutsToKeeps(a.CutRanges, duration)
		if len(a.KeepRanges) == 0 {
			return "", errors.New("media_render: cut_ranges remove the entire file; approve at least one keep range")
		}
	}
	profiles := normalizedMediaProfiles(a.Profiles)
	var rendered []string
	for i, profile := range profiles {
		args, err := buildMediaRenderArgs(input, outs[i], profile, a)
		if err != nil {
			return "", fmt.Errorf("media_render: %w", err)
		}
		cmd := mediaCommandContext(ctx, "ffmpeg", args...)
		cmd.Dir = t.Cwd.Get()
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &capped{buf: &stdout, max: 32 * 1024}
		cmd.Stderr = &capped{buf: &stderr, max: mediaMaxOutputBytes}
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("media_render: ffmpeg failed for %s: %w; stderr=%q", profile, err, stderr.String())
		}
		rendered = append(rendered, outs[i])
	}
	return "rendered:\n" + strings.Join(rendered, "\n") + "\n", nil
}

type mediaRange struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type mediaRenderArgs struct {
	Input        string       `json:"input"`
	Output       string       `json:"output"`
	Profiles     []string     `json:"profiles"`
	CutRanges    []mediaRange `json:"cut_ranges"`
	KeepRanges   []mediaRange `json:"keep_ranges"`
	CaptionsPath string       `json:"captions_path"`
	IntroPath    string       `json:"intro_path"`
	OutroPath    string       `json:"outro_path"`
	GIFWidth     int          `json:"gif_width"`
	GIFFPS       float64      `json:"gif_fps"`
	Speed        float64      `json:"speed"`
	Overwrite    bool         `json:"overwrite"`
	HasAudio     bool         `json:"-"`
}

type mediaStream struct {
	Index       int               `json:"index"`
	CodecType   string            `json:"codec_type"`
	CodecName   string            `json:"codec_name"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	AvgFrame    string            `json:"avg_frame_rate"`
	Duration    string            `json:"duration"`
	SampleRate  string            `json:"sample_rate"`
	Channels    int               `json:"channels"`
	Tags        map[string]string `json:"tags"`
	Disposition map[string]int    `json:"disposition"`
}

type mediaProbeJSON struct {
	Streams []mediaStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
		BitRate  string `json:"bit_rate"`
		Format   string `json:"format_name"`
	} `json:"format"`
}

func summarizeMediaProbe(data []byte) (string, error) {
	var p mediaProbeJSON
	if err := json.Unmarshal(data, &p); err != nil {
		return "", fmt.Errorf("parse ffprobe json: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "duration: %s seconds\n", trimFloatString(p.Format.Duration))
	if p.Format.Format != "" {
		fmt.Fprintf(&b, "container: %s\n", p.Format.Format)
	}
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			fmt.Fprintf(&b, "video[%d]: codec=%s %dx%d fps=%s", s.Index, s.CodecName, s.Width, s.Height, s.AvgFrame)
			if rot := streamRotation(s); rot != "" {
				fmt.Fprintf(&b, " rotation=%s", rot)
			}
			b.WriteByte('\n')
		case "audio":
			fmt.Fprintf(&b, "audio[%d]: codec=%s sample_rate=%s channels=%d\n", s.Index, s.CodecName, s.SampleRate, s.Channels)
		}
	}
	return b.String(), nil
}

func streamRotation(s mediaStream) string {
	if s.Tags != nil && s.Tags["rotate"] != "" {
		return s.Tags["rotate"]
	}
	return ""
}

func trimFloatString(s string) string {
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(f, 'f', 3, 64)
	}
	return s
}

func parseSilenceDetect(stderr string, minDuration float64) []mediaRange {
	var starts []float64
	var cuts []mediaRange
	for _, line := range strings.Split(stderr, "\n") {
		if i := strings.Index(line, "silence_start:"); i >= 0 {
			fields := strings.Fields(strings.TrimSpace(line[i+len("silence_start:"):]))
			if len(fields) == 0 {
				continue
			}
			if f, err := strconv.ParseFloat(fields[0], 64); err == nil {
				starts = append(starts, f)
			}
			continue
		}
		if i := strings.Index(line, "silence_end:"); i >= 0 {
			fields := strings.Fields(strings.TrimSpace(line[i+len("silence_end:"):]))
			if len(fields) == 0 || len(starts) == 0 {
				continue
			}
			end, err := strconv.ParseFloat(fields[0], 64)
			if err != nil {
				continue
			}
			start := starts[len(starts)-1]
			starts = starts[:len(starts)-1]
			if end-start >= minDuration {
				cuts = append(cuts, mediaRange{Start: start, End: end})
			}
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Start < cuts[j].Start })
	return cuts
}

func runAudioSilenceDetect(ctx context.Context, cwd, input string, threshold, minDuration float64) ([]mediaRange, error) {
	filter := fmt.Sprintf("silencedetect=noise=%.1fdB:d=%.3f", threshold, minDuration)
	args := []string{"-hide_banner", "-nostats", "-i", input, "-af", filter, "-f", "null", "-"}
	cmd := mediaCommandContext(ctx, "ffmpeg", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{buf: &stdout, max: 8 * 1024}
	cmd.Stderr = &capped{buf: &stderr, max: mediaMaxOutputBytes}
	_ = cmd.Run() // silencedetect reports through stderr; parse whatever was produced.
	return parseSilenceDetect(stderr.String(), minDuration), nil
}

func runVisualFreezeDetect(ctx context.Context, cwd, input string, noiseThreshold, minDuration float64) ([]mediaRange, error) {
	filter := fmt.Sprintf("freezedetect=n=%.6f:d=%.3f", noiseThreshold, minDuration)
	args := []string{"-hide_banner", "-nostats", "-i", input, "-vf", filter, "-an", "-f", "null", "-"}
	cmd := mediaCommandContext(ctx, "ffmpeg", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{buf: &stdout, max: 8 * 1024}
	cmd.Stderr = &capped{buf: &stderr, max: mediaMaxOutputBytes}
	_ = cmd.Run() // freezedetect reports through stderr; parse whatever was produced.
	return parseFreezeDetect(stderr.String(), minDuration), nil
}

func parseFreezeDetect(stderr string, minDuration float64) []mediaRange {
	var starts []float64
	var cuts []mediaRange
	for _, line := range strings.Split(stderr, "\n") {
		if i := strings.Index(line, "freeze_start:"); i >= 0 {
			fields := strings.Fields(strings.TrimSpace(line[i+len("freeze_start:"):]))
			if len(fields) == 0 {
				continue
			}
			if f, err := strconv.ParseFloat(fields[0], 64); err == nil {
				starts = append(starts, f)
			}
			continue
		}
		if i := strings.Index(line, "freeze_end:"); i >= 0 {
			fields := strings.Fields(strings.TrimSpace(line[i+len("freeze_end:"):]))
			if len(fields) == 0 || len(starts) == 0 {
				continue
			}
			end, err := strconv.ParseFloat(fields[0], 64)
			if err != nil {
				continue
			}
			start := starts[len(starts)-1]
			starts = starts[:len(starts)-1]
			if end-start >= minDuration {
				cuts = append(cuts, mediaRange{Start: start, End: end})
			}
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Start < cuts[j].Start })
	return cuts
}

func renderSilenceCuts(cuts []mediaRange) string {
	if len(cuts) == 0 {
		return "candidate_cuts: none\n"
	}
	var b strings.Builder
	b.WriteString("candidate_cuts:\n")
	for _, c := range cuts {
		fmt.Fprintf(&b, "- start=%.3f end=%.3f duration=%.3f\n", c.Start, c.End, c.End-c.Start)
	}
	return b.String()
}

func renderMediaCandidates(mode string, detectors []string, candidates []mediaCandidate, notes []string) string {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Range.Start < candidates[j].Range.Start })
	var b strings.Builder
	fmt.Fprintf(&b, "mode: %s\n", mode)
	fmt.Fprintf(&b, "detectors: %s\n", strings.Join(detectors, ","))
	if len(candidates) == 0 {
		b.WriteString("candidate_cuts: none\n")
	} else {
		b.WriteString("candidate_cuts:\n")
		for _, c := range candidates {
			fmt.Fprintf(&b, "- start=%.3f end=%.3f duration=%.3f detector=%s confidence=%.2f reason=%q\n", c.Range.Start, c.Range.End, c.Range.End-c.Range.Start, c.Detector, c.Confidence, c.Reason)
		}
	}
	if len(notes) > 0 {
		b.WriteString("notes:\n")
		for _, note := range notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	return b.String()
}

func selectMediaDetectors(mode string, detectors []string, probe mediaProbeJSON) []string {
	if len(detectors) > 0 {
		return cleanMediaDetectors(detectors)
	}
	switch strings.TrimSpace(mode) {
	case "audio_silence", "silence", "audio":
		return []string{"audio_silence"}
	case "visual_idle", "terminal_demo", "visual":
		return []string{"visual_idle"}
	case "all":
		return []string{"audio_silence", "visual_idle"}
	default:
		var out []string
		if probe.hasAudio() {
			out = append(out, "audio_silence")
		}
		if probe.hasVideo() {
			out = append(out, "visual_idle")
		}
		if len(out) == 0 {
			return []string{"audio_silence", "visual_idle"}
		}
		return out
	}
}

func cleanMediaDetectors(detectors []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range detectors {
		d = strings.TrimSpace(d)
		switch d {
		case "audio", "silence":
			d = "audio_silence"
		case "visual", "terminal_demo":
			d = "visual_idle"
		}
		if d == "" || seen[d] {
			continue
		}
		if d == "audio_silence" || d == "visual_idle" {
			out = append(out, d)
			seen[d] = true
		}
	}
	return out
}

func probeMediaFile(ctx context.Context, cwd, input string) (mediaProbeJSON, error) {
	var out mediaProbeJSON
	if _, err := mediaLookPath("ffprobe"); err != nil {
		return out, err
	}
	args := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", input}
	cmd := mediaCommandContext(ctx, "ffprobe", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{buf: &stdout, max: mediaMaxOutputBytes}
	cmd.Stderr = &capped{buf: &stderr, max: 32 * 1024}
	if err := cmd.Run(); err != nil {
		return out, fmt.Errorf("ffprobe failed: %w; stderr=%q", err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return out, err
	}
	return out, nil
}

func (p mediaProbeJSON) hasAudio() bool {
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			return true
		}
	}
	return false
}

func (p mediaProbeJSON) hasVideo() bool {
	for _, s := range p.Streams {
		if s.CodecType == "video" {
			return true
		}
	}
	return false
}

func (p mediaProbeJSON) durationSeconds() float64 {
	if f, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil {
		return f
	}
	for _, s := range p.Streams {
		if f, err := strconv.ParseFloat(s.Duration, 64); err == nil {
			return f
		}
	}
	return 0
}

func normalizedMediaProfiles(in []string) []string {
	if len(in) == 0 {
		return []string{"youtube_16x9"}
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"youtube_16x9"}
	}
	return out
}

func mediaOutputPaths(cwd, output string, profiles []string) ([]string, error) {
	profiles = normalizedMediaProfiles(profiles)
	base := resolvePath(cwd, output)
	if len(profiles) == 1 {
		if mediaProfileIsGIF(profiles[0]) {
			return []string{strings.TrimSuffix(base, filepath.Ext(base)) + ".gif"}, nil
		}
		if filepath.Ext(base) == "" {
			base += mediaProfileExt(profiles[0])
		}
		return []string{base}, nil
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	out := make([]string, 0, len(profiles))
	for _, p := range profiles {
		profileExt := ext
		if profileExt == "" || mediaProfileExt(p) == ".gif" {
			profileExt = mediaProfileExt(p)
		}
		out = append(out, stem+"-"+p+profileExt)
	}
	return out, nil
}

func mediaProfileExt(profile string) string {
	if mediaProfileIsGIF(profile) {
		return ".gif"
	}
	return ".mp4"
}

func mediaProfileIsGIF(profile string) bool {
	return profile == "gif_preview" || profile == "gif_preview_large"
}

func buildMediaRenderArgs(input, output, profile string, a mediaRenderArgs) ([]string, error) {
	if mediaProfileIsGIF(profile) {
		return buildMediaGIFRenderArgs(input, output, profile, a)
	}
	vf, err := mediaVideoFilter(profile, a.CaptionsPath)
	if err != nil {
		return nil, err
	}
	args := []string{"-hide_banner"}
	if a.Overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args, "-i", input)
	if len(a.KeepRanges) > 1 {
		complex, err := mediaConcatFilter(a.KeepRanges, vf, a.HasAudio)
		if err != nil {
			return nil, err
		}
		args = append(args, "-filter_complex", complex, "-map", "[vout]")
		if a.HasAudio {
			args = append(args, "-map", "[aout]")
		} else {
			args = append(args, "-an")
		}
		args = append(args,
			"-c:v", "libx264", "-preset", "medium", "-crf", "20",
			"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", output,
		)
		return args, nil
	}
	if len(a.KeepRanges) == 1 {
		r := a.KeepRanges[0]
		args = append(args, "-ss", fmtSeconds(r.Start), "-to", fmtSeconds(r.End))
	}
	args = append(args,
		"-vf", vf,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-c:v", "libx264", "-preset", "medium", "-crf", "20",
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", output,
	)
	return args, nil
}

func cutsToKeeps(cuts []mediaRange, duration float64) []mediaRange {
	cuts = normalizeMediaRanges(cuts, duration)
	keeps := make([]mediaRange, 0, len(cuts)+1)
	cursor := 0.0
	for _, cut := range cuts {
		if cut.Start > cursor {
			keeps = append(keeps, mediaRange{Start: cursor, End: cut.Start})
		}
		if cut.End > cursor {
			cursor = cut.End
		}
	}
	if cursor < duration {
		keeps = append(keeps, mediaRange{Start: cursor, End: duration})
	}
	return keeps
}

func normalizeMediaRanges(ranges []mediaRange, duration float64) []mediaRange {
	var out []mediaRange
	for _, r := range ranges {
		if r.Start < 0 {
			r.Start = 0
		}
		if duration > 0 && r.End > duration {
			r.End = duration
		}
		if r.End > r.Start {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, r := range out {
		if len(merged) == 0 || r.Start > merged[len(merged)-1].End {
			merged = append(merged, r)
			continue
		}
		if r.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = r.End
		}
	}
	return merged
}

func mediaConcatFilter(keeps []mediaRange, vf string, hasAudio bool) (string, error) {
	keeps = normalizeMediaRanges(keeps, 0)
	if len(keeps) < 2 {
		return "", errors.New("concat filter requires at least two keep ranges")
	}
	var b strings.Builder
	var concatInputs strings.Builder
	for i, r := range keeps {
		fmt.Fprintf(&b, "[0:v]trim=start=%s:end=%s,setpts=PTS-STARTPTS[v%d];", fmtSeconds(r.Start), fmtSeconds(r.End), i)
		fmt.Fprintf(&concatInputs, "[v%d]", i)
		if hasAudio {
			fmt.Fprintf(&b, "[0:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS[a%d];", fmtSeconds(r.Start), fmtSeconds(r.End), i)
			fmt.Fprintf(&concatInputs, "[a%d]", i)
		}
	}
	if hasAudio {
		fmt.Fprintf(&b, "%sconcat=n=%d:v=1:a=1[vcat][acat];[vcat]%s[vout];[acat]loudnorm=I=-16:TP=-1.5:LRA=11[aout]", concatInputs.String(), len(keeps), vf)
	} else {
		fmt.Fprintf(&b, "%sconcat=n=%d:v=1:a=0[vcat];[vcat]%s[vout]", concatInputs.String(), len(keeps), vf)
	}
	return b.String(), nil
}

func buildMediaGIFRenderArgs(input, output, profile string, a mediaRenderArgs) ([]string, error) {
	gifFilter, err := mediaGIFFilter(profile, a)
	if err != nil {
		return nil, err
	}
	gifFilter += ",split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse=dither=bayer:bayer_scale=5"
	if len(a.KeepRanges) > 1 {
		complex, err := mediaConcatFilter(a.KeepRanges, gifFilter, false)
		if err != nil {
			return nil, err
		}
		return mediaGIFArgs(input, output, a.Overwrite, complex), nil
	}
	complex := "[0:v]" + gifFilter + "[vout]"
	args := []string{"-hide_banner"}
	if a.Overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args, "-i", input)
	if len(a.KeepRanges) == 1 {
		r := a.KeepRanges[0]
		args = append(args, "-ss", fmtSeconds(r.Start), "-to", fmtSeconds(r.End))
	}
	args = append(args, "-filter_complex", complex, "-map", "[vout]", "-loop", "0", output)
	return args, nil
}

func mediaGIFArgs(input, output string, overwrite bool, filterComplex string) []string {
	args := []string{"-hide_banner"}
	if overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args, "-i", input, "-filter_complex", filterComplex, "-map", "[vout]", "-loop", "0", output)
	return args
}

func mediaGIFFilter(profile string, a mediaRenderArgs) (string, error) {
	width := 960
	switch profile {
	case "gif_preview":
		width = 960
	case "gif_preview_large":
		width = 1440
	default:
		return "", fmt.Errorf("unknown media profile %q", profile)
	}
	if a.GIFWidth > 0 {
		width = a.GIFWidth
	}
	fps := a.GIFFPS
	if fps <= 0 {
		fps = 12
	}
	speed := a.Speed
	if speed <= 0 {
		speed = 1
	}
	filter := fmt.Sprintf("fps=%s,scale=%d:-1:flags=lanczos", fmtNumber(fps), width)
	if speed != 1 {
		filter += fmt.Sprintf(",setpts=PTS/%s", fmtNumber(speed))
	}
	if strings.TrimSpace(a.CaptionsPath) != "" {
		filter += ",subtitles=" + escapeFFmpegFilterPath(a.CaptionsPath)
	}
	return filter, nil
}

func mediaVideoFilter(profile, captions string) (string, error) {
	var vf string
	switch profile {
	case "youtube_16x9", "x_16x9":
		vf = "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2,format=yuv420p"
	case "x_vertical_9x16":
		vf = "scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920,format=yuv420p"
	case "gif_preview", "gif_preview_large":
		return mediaGIFFilter(profile, mediaRenderArgs{CaptionsPath: captions})
	default:
		return "", fmt.Errorf("unknown media profile %q", profile)
	}
	if strings.TrimSpace(captions) != "" {
		vf += ",subtitles=" + escapeFFmpegFilterPath(captions)
	}
	return vf, nil
}

func fmtSeconds(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

func fmtNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func encodeRanges(ranges []mediaRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, fmt.Sprintf("%.3f-%.3f", r.Start, r.End))
	}
	return strings.Join(parts, ",")
}

func escapeFFmpegFilterPath(path string) string {
	path = strings.ReplaceAll(path, `\`, `\\`)
	path = strings.ReplaceAll(path, `:`, `\:`)
	path = strings.ReplaceAll(path, `'`, `\'`)
	return path
}
