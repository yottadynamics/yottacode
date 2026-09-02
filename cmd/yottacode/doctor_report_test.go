package main

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestFormatDoctorReportHappyPath(t *testing.T) {
	provider := doctorProviderFixture(nil, nil)
	github := GitHubProbeResult{Status: doctorStatusOK, TokenSource: "env", Reachable: true, AuthOK: true, Login: "octocat"}
	lsp := LSPDoctorResult{Status: doctorStatusOK, Enabled: true, Manager: LSPDoctorManager{MaxServers: 4}}
	media := MediaDoctorResult{Status: doctorStatusOK, FFmpeg: MediaDoctorBinary{Command: "ffmpeg", Installed: true, Path: "/usr/bin/ffmpeg", Required: true}, FFprobe: MediaDoctorBinary{Command: "ffprobe", Installed: true, Path: "/usr/bin/ffprobe", Required: true}}
	sandbox := SandboxDoctorResult{Status: doctorStatusSkipped, Backend: "none", Skipped: true}

	out := formatDoctorReport(newDoctorSummary(provider, github, lsp, media, sandbox), provider, github, lsp, media, sandbox)
	for _, want := range []string{
		"yottacode doctor",
		"Summary:\n- provider: ok\n- github: ok\n- lsp: ok\n- media: ok\n- sandbox: skipped",
		"Provider:\n  status: ok",
		"GitHub:\n  status: ok",
		"LSP Code Intelligence:\n  status: ok",
		"Media Editing:\n  status: ok",
		"Sandbox:\n  status: skipped",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatDoctorReportWarningOnlyPath(t *testing.T) {
	provider := doctorProviderFixture(nil, []string{"API key is empty for a remote provider"})
	github := GitHubProbeResult{Status: doctorStatusWarning, TokenSource: "env", Reachable: true, AuthOK: true, Warnings: []string{"rate limit low"}}
	lsp := LSPDoctorResult{Status: doctorStatusOK, Enabled: true}
	media := MediaDoctorResult{Status: doctorStatusWarning, Warnings: []string{"local caption transcription is unavailable"}}
	sandbox := SandboxDoctorResult{Status: doctorStatusWarning, Backend: "podman", Image: "img", Warnings: []string{"sandbox Go cache exceeds 2 GB"}}

	out := formatDoctorReport(newDoctorSummary(provider, github, lsp, media, sandbox), provider, github, lsp, media, sandbox)
	for _, want := range []string{"- provider: warning", "- github: warning", "- media: warning", "- sandbox: warning", "  status: warning", "  warning: rate limit low"} {
		if !strings.Contains(out, want) {
			t.Fatalf("warning report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "result: ok") {
		t.Fatalf("new grouped report should not use legacy result trailer:\n%s", out)
	}
}

func TestFormatDoctorReportIssuePath(t *testing.T) {
	provider := doctorProviderFixture([]string{"model \"gpt-5\" not listed by /models"}, nil)
	github := GitHubProbeResult{Status: doctorStatusOK, TokenSource: "env", Reachable: true, AuthOK: true}
	lsp := LSPDoctorResult{Status: doctorStatusIssue, Enabled: true, Error: "gopls failed"}
	media := MediaDoctorResult{Status: doctorStatusOK}
	sandbox := SandboxDoctorResult{Status: doctorStatusOK, Backend: "podman"}

	out := formatDoctorReport(newDoctorSummary(provider, github, lsp, media, sandbox), provider, github, lsp, media, sandbox)
	for _, want := range []string{"- provider: issue", "- lsp: issue", "  issue: model \"gpt-5\" not listed by /models", "  issue: gopls failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("issue report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatDoctorReportSkippedGitHub(t *testing.T) {
	provider := doctorProviderFixture(nil, nil)
	github := GitHubProbeResult{Status: doctorStatusSkipped, Skipped: true}
	lsp := LSPDoctorResult{Status: doctorStatusOK, Enabled: true}
	media := MediaDoctorResult{Status: doctorStatusOK}
	sandbox := SandboxDoctorResult{Status: doctorStatusOK, Backend: "podman"}

	out := formatDoctorReport(newDoctorSummary(provider, github, lsp, media, sandbox), provider, github, lsp, media, sandbox)
	for _, want := range []string{"- github: skipped", "GitHub:\n  status: skipped\n  skipped: --no-github"} {
		if !strings.Contains(out, want) {
			t.Fatalf("skipped report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatModelSampleTruncatesLargeLists(t *testing.T) {
	models := []string{"m1", "m2", "m3", "m4", "m5"}
	got := formatModelSample(models, 3)
	if got != "5 available (showing 3): m1, m2, m3" {
		t.Fatalf("sample = %q", got)
	}
	if strings.Contains(got, "m4") || strings.Contains(got, "m5") {
		t.Fatalf("sample leaked truncated models: %q", got)
	}
}

func doctorProviderFixture(issues, warnings []string) adapter.ProbeResult {
	return adapter.ProbeResult{
		Profile: adapter.ProviderProfile{
			Provider:         adapter.ProviderOpenAI,
			UsesResponsesAPI: true,
		},
		BaseURL:           "https://api.openai.com/v1",
		Model:             "gpt-5",
		HTTPStatus:        200,
		EndpointReachable: true,
		AuthOK:            true,
		ModelVisible:      len(issues) == 0,
		AvailableModels:   []string{"gpt-5", "gpt-4.1"},
		Issues:            issues,
		Warnings:          warnings,
	}
}
