package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

var mediaDoctorLookPath = exec.LookPath

// MediaDoctorResult reports readiness for the optional local video editing
// workflow. yottacode never installs these binaries; it only tells the user
// what is present so media tools fail predictably.
type MediaDoctorResult struct {
	Status        doctorStatus        `json:"status"`
	FFmpeg        MediaDoctorBinary   `json:"ffmpeg"`
	FFprobe       MediaDoctorBinary   `json:"ffprobe"`
	Transcription []MediaDoctorBinary `json:"transcription,omitempty"`
	Issues        []string            `json:"issues,omitempty"`
	Warnings      []string            `json:"warnings,omitempty"`
}

type MediaDoctorBinary struct {
	Command     string `json:"command"`
	Installed   bool   `json:"installed"`
	Path        string `json:"path,omitempty"`
	Required    bool   `json:"required"`
	InstallHint string `json:"install_hint,omitempty"`
}

func probeMediaDoctor(ctx context.Context) MediaDoctorResult {
	_ = ctx // Reserved for future version probes with a timeout.
	result := MediaDoctorResult{
		FFmpeg:  probeMediaBinary("ffmpeg", true, "install ffmpeg with your package manager, e.g. brew install ffmpeg or sudo apt install ffmpeg"),
		FFprobe: probeMediaBinary("ffprobe", true, "ffprobe ships with ffmpeg; install the ffmpeg package"),
		Transcription: []MediaDoctorBinary{
			probeMediaBinary("whisper", false, "optional for local captions; install whisper.cpp or use a hosted transcription workflow"),
			probeMediaBinary("whisper-cli", false, "optional for local captions; install whisper.cpp or use a hosted transcription workflow"),
		},
	}
	if !result.FFmpeg.Installed {
		result.Issues = append(result.Issues, "ffmpeg is required for media_analyze and media_render")
	}
	if !result.FFprobe.Installed {
		result.Issues = append(result.Issues, "ffprobe is required for media_probe")
	}
	if !result.Transcription[0].Installed && !result.Transcription[1].Installed {
		result.Warnings = append(result.Warnings, "local caption transcription is unavailable; media editing still works without captions")
	}
	result.Status = statusFromIssuesWarnings(result.Issues, result.Warnings)
	return result
}

func probeMediaBinary(command string, required bool, hint string) MediaDoctorBinary {
	out := MediaDoctorBinary{Command: command, Required: required, InstallHint: hint}
	if p, err := mediaDoctorLookPath(command); err == nil {
		out.Installed = true
		out.Path = p
		out.InstallHint = ""
	}
	return out
}

func renderMediaDoctor(result MediaDoctorResult) string {
	var b strings.Builder
	b.WriteString("\n\nMedia Editing:\n")
	renderMediaBinary(&b, result.FFmpeg)
	renderMediaBinary(&b, result.FFprobe)
	for _, bin := range result.Transcription {
		renderMediaBinary(&b, bin)
	}
	for _, issue := range result.Issues {
		fmt.Fprintf(&b, "\nissue: %s", issue)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s", warning)
	}
	if len(result.Issues) == 0 && len(result.Warnings) == 0 {
		b.WriteString("\nresult: ok")
	}
	return b.String()
}

func renderMediaBinary(b *strings.Builder, bin MediaDoctorBinary) {
	status := "missing"
	if bin.Installed {
		status = "installed"
	}
	required := "optional"
	if bin.Required {
		required = "required"
	}
	fmt.Fprintf(b, "- %s: %s (%s)", bin.Command, status, required)
	if bin.Path != "" {
		fmt.Fprintf(b, " path=%s", bin.Path)
	}
	if !bin.Installed && bin.InstallHint != "" {
		fmt.Fprintf(b, "\n  hint: %s", bin.InstallHint)
	}
	b.WriteByte('\n')
}
