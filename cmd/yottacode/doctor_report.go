package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/sandboxcache"
)

const doctorModelSampleLimit = 8

// doctorStatus is the small status vocabulary shared by the human summary and
// additive JSON section fields. Issues are blocking, warnings are advisory, and
// skipped means the user intentionally disabled or bypassed that probe.
type doctorStatus string

const (
	doctorStatusOK      doctorStatus = "ok"
	doctorStatusWarning doctorStatus = "warning"
	doctorStatusIssue   doctorStatus = "issue"
	doctorStatusSkipped doctorStatus = "skipped"
)

// DoctorSectionStatus is an additive JSON object for script consumers that want
// the grouped report state without re-deriving it from legacy top-level fields.
type DoctorSectionStatus struct {
	Status doctorStatus `json:"status"`
}

// DoctorSummary is the additive JSON summary matching the human report's first
// section. Existing top-level provider probe fields remain unchanged.
type DoctorSummary struct {
	Provider doctorStatus `json:"provider"`
	GitHub   doctorStatus `json:"github"`
	LSP      doctorStatus `json:"lsp"`
	Media    doctorStatus `json:"media"`
	Sandbox  doctorStatus `json:"sandbox"`
}

// SandboxDoctorResult reports sandbox configuration and Go cache visibility.
// It is intentionally cheap and local: no Podman process is started by doctor.
type SandboxDoctorResult struct {
	Status       doctorStatus `json:"status"`
	Backend      string       `json:"backend"`
	Image        string       `json:"image,omitempty"`
	GoCacheDir   string       `json:"go_cache_dir,omitempty"`
	GoCacheBytes int64        `json:"go_cache_bytes,omitempty"`
	Warnings     []string     `json:"warnings,omitempty"`
	Issues       []string     `json:"issues,omitempty"`
	Hints        []string     `json:"hints,omitempty"`
	Skipped      bool         `json:"skipped,omitempty"`
}

func newDoctorSummary(provider adapter.ProbeResult, github GitHubProbeResult, lsp LSPDoctorResult, media MediaDoctorResult, sandbox SandboxDoctorResult) DoctorSummary {
	return DoctorSummary{
		Provider: statusFromIssuesWarnings(provider.Issues, provider.Warnings),
		GitHub:   github.Status,
		LSP:      lsp.Status,
		Media:    media.Status,
		Sandbox:  sandbox.Status,
	}
}

func statusFromIssuesWarnings(issues, warnings []string) doctorStatus {
	switch {
	case len(issues) > 0:
		return doctorStatusIssue
	case len(warnings) > 0:
		return doctorStatusWarning
	default:
		return doctorStatusOK
	}
}

func probeSandboxDoctor(cfg config.SandboxConfig) SandboxDoctorResult {
	result := SandboxDoctorResult{
		Backend: strings.TrimSpace(cfg.Backend),
		Image:   strings.TrimSpace(cfg.Image),
	}
	if result.Backend == "" {
		result.Backend = "none"
	}
	if result.Backend == "none" {
		result.Status = doctorStatusSkipped
		result.Skipped = true
		return result
	}
	if result.Backend != "podman" {
		result.Status = doctorStatusIssue
		result.Issues = append(result.Issues, fmt.Sprintf("unsupported sandbox backend %q", result.Backend))
		return result
	}
	cacheDir, err := sandboxcache.GoHostCacheDir()
	if err != nil {
		result.Status = doctorStatusWarning
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not resolve sandbox Go cache: %v", err))
		return result
	}
	result.GoCacheDir = cacheDir
	size, err := dirSize(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			result.Status = doctorStatusOK
			result.Hints = append(result.Hints, "sandbox Go cache has not been created yet; it appears after the first sandboxed Go test/build")
			return result
		}
		result.Status = doctorStatusWarning
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not inspect sandbox Go cache: %v", err))
		return result
	}
	result.GoCacheBytes = size
	if size > 2*1024*1024*1024 {
		result.Status = doctorStatusWarning
		result.Warnings = append(result.Warnings, fmt.Sprintf("sandbox Go cache exceeds 2 GB (%s)", humanBytes(size)))
		result.Hints = append(result.Hints, "run `go clean -cache -modcache` inside a sandboxed shell, or remove ~/.yottacode/sandbox-go-cache when no sandboxed Go jobs are running")
		return result
	}
	result.Status = doctorStatusOK
	return result
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func formatDoctorReport(summary DoctorSummary, provider adapter.ProbeResult, github GitHubProbeResult, lsp LSPDoctorResult, media MediaDoctorResult, sandbox SandboxDoctorResult) string {
	var b strings.Builder
	b.WriteString("yottacode doctor\n\n")
	renderDoctorSummary(&b, summary)
	renderProviderSection(&b, provider, summary.Provider)
	renderGitHubSection(&b, github)
	renderLSPSection(&b, lsp)
	renderMediaSection(&b, media)
	renderSandboxSection(&b, sandbox)
	return strings.TrimRight(b.String(), "\n")
}

func renderDoctorSummary(b *strings.Builder, summary DoctorSummary) {
	b.WriteString("Summary:\n")
	fmt.Fprintf(b, "- provider: %s\n", summary.Provider)
	fmt.Fprintf(b, "- github: %s\n", summary.GitHub)
	fmt.Fprintf(b, "- lsp: %s\n", summary.LSP)
	fmt.Fprintf(b, "- media: %s\n", summary.Media)
	fmt.Fprintf(b, "- sandbox: %s\n", summary.Sandbox)
}

func renderProviderSection(b *strings.Builder, result adapter.ProbeResult, status doctorStatus) {
	b.WriteString("\nProvider:\n")
	fmt.Fprintf(b, "  status: %s\n", status)
	fmt.Fprintf(b, "  provider: %s\n", providerLabel(result.Profile.Provider))
	if result.Profile.UsesResponsesAPI {
		b.WriteString("  api-style: responses\n")
	} else {
		b.WriteString("  api-style: chat-completions\n")
	}
	if result.BaseURL != "" {
		fmt.Fprintf(b, "  base-url: %s\n", result.BaseURL)
	}
	if result.Model != "" {
		fmt.Fprintf(b, "  model: %s\n", result.Model)
	}
	if result.HTTPStatus != 0 {
		fmt.Fprintf(b, "  endpoint: reachable (HTTP %d)\n", result.HTTPStatus)
	} else {
		fmt.Fprintf(b, "  endpoint: reachable=%s\n", yesNo(result.EndpointReachable))
	}
	fmt.Fprintf(b, "  auth: %s\n", okNo(result.AuthOK))
	fmt.Fprintf(b, "  model visibility: %s\n", okNo(result.ModelVisible))
	if len(result.Profile.EnabledBuiltinTools) > 0 {
		parts := make([]string, 0, len(result.Profile.EnabledBuiltinTools))
		for _, tool := range result.Profile.EnabledBuiltinTools {
			parts = append(parts, string(tool))
		}
		fmt.Fprintf(b, "  enabled provider tools: %s\n", strings.Join(parts, ", "))
	} else {
		b.WriteString("  enabled provider tools: none\n")
	}
	if len(result.AvailableModels) > 0 {
		fmt.Fprintf(b, "  models: %s\n", formatModelSample(result.AvailableModels, doctorModelSampleLimit))
	}
	renderIssuesWarnings(b, "  ", result.Issues, result.Warnings)
}

func renderGitHubSection(b *strings.Builder, r GitHubProbeResult) {
	b.WriteString("\nGitHub:\n")
	fmt.Fprintf(b, "  status: %s\n", r.Status)
	if r.Skipped {
		b.WriteString("  skipped: --no-github\n")
		return
	}
	if r.TokenSource == "" {
		b.WriteString("  token: none\n")
	} else {
		fmt.Fprintf(b, "  token: present (source=%s)\n", r.TokenSource)
	}
	auth := okNo(r.AuthOK)
	if r.Login != "" {
		auth += " (user=" + r.Login + ")"
	}
	fmt.Fprintf(b, "  auth: %s\n", auth)
	fmt.Fprintf(b, "  endpoint: reachable=%s\n", yesNo(r.Reachable))
	if r.Rate.IsSet() {
		fmt.Fprintf(b, "  rate: %d/%d remaining", r.Rate.Remaining, r.Rate.Limit)
		if !r.Rate.Reset.IsZero() {
			if resetIn := timeUntilRounded(r.Rate.Reset); resetIn != "" {
				fmt.Fprintf(b, " (resets in %s)", resetIn)
			}
		}
		b.WriteByte('\n')
	}
	renderIssuesWarnings(b, "  ", r.Issues, r.Warnings)
}

func renderLSPSection(b *strings.Builder, result LSPDoctorResult) {
	b.WriteString("\nLSP Code Intelligence:\n")
	fmt.Fprintf(b, "  status: %s\n", result.Status)
	fmt.Fprintf(b, "  feature: %s\n", yesNo(result.Enabled))
	if result.Note != "" {
		fmt.Fprintf(b, "  note: %s\n", result.Note)
	}
	if result.Error != "" {
		fmt.Fprintf(b, "  issue: %s\n", result.Error)
		return
	}
	if result.Enabled && result.Manager.MaxServers > 0 {
		fmt.Fprintf(b, "  manager: max_servers=%d\n", result.Manager.MaxServers)
	}
	if len(result.Languages) > 0 {
		b.WriteString("  languages:\n")
	}
	for _, lang := range result.Languages {
		status := "missing"
		if lang.ServerAvailable {
			status = "installed"
		}
		fmt.Fprintf(b, "  - %s: %s, probe %s, files=%d\n", lang.Name, status, lang.Probe, lang.Files)
		if lang.Override {
			b.WriteString("    override: yes\n")
		}
		if !lang.ServerAvailable && lang.InstallHint != "" {
			fmt.Fprintf(b, "    hint: %s\n", lang.InstallHint)
		}
		if lang.Capabilities != "" {
			fmt.Fprintf(b, "    capabilities: %s\n", lang.Capabilities)
		}
	}
}

func renderMediaSection(b *strings.Builder, result MediaDoctorResult) {
	b.WriteString("\nMedia Editing:\n")
	fmt.Fprintf(b, "  status: %s\n", result.Status)
	renderMediaBinaryLine(b, "  ", result.FFmpeg)
	renderMediaBinaryLine(b, "  ", result.FFprobe)
	for _, bin := range result.Transcription {
		renderMediaBinaryLine(b, "  ", bin)
	}
	renderIssuesWarnings(b, "  ", result.Issues, result.Warnings)
}

func renderSandboxSection(b *strings.Builder, result SandboxDoctorResult) {
	b.WriteString("\nSandbox:\n")
	fmt.Fprintf(b, "  status: %s\n", result.Status)
	fmt.Fprintf(b, "  backend: %s\n", result.Backend)
	if result.Skipped {
		b.WriteString("  skipped: sandbox backend is none\n")
		return
	}
	if result.Image != "" {
		fmt.Fprintf(b, "  image: %s\n", result.Image)
	}
	if result.GoCacheDir != "" {
		cache := humanBytes(result.GoCacheBytes)
		if result.GoCacheBytes == 0 {
			cache = "0 B"
		}
		fmt.Fprintf(b, "  go cache: %s total (%s)\n", cache, result.GoCacheDir)
	}
	renderIssuesWarnings(b, "  ", result.Issues, result.Warnings)
	for _, hint := range result.Hints {
		fmt.Fprintf(b, "  hint: %s\n", hint)
	}
}

func renderMediaBinaryLine(b *strings.Builder, indent string, bin MediaDoctorBinary) {
	status := "missing"
	if bin.Installed {
		status = "installed"
	}
	fmt.Fprintf(b, "%s%s: %s", indent, bin.Command, status)
	if bin.Path != "" {
		fmt.Fprintf(b, " (%s)", bin.Path)
	}
	b.WriteByte('\n')
	if !bin.Installed && bin.InstallHint != "" {
		fmt.Fprintf(b, "%shint: %s\n", indent, bin.InstallHint)
	}
}

func renderIssuesWarnings(b *strings.Builder, indent string, issues, warnings []string) {
	for _, issue := range issues {
		fmt.Fprintf(b, "%sissue: %s\n", indent, issue)
	}
	for _, warning := range warnings {
		fmt.Fprintf(b, "%swarning: %s\n", indent, warning)
	}
}

func formatModelSample(models []string, limit int) string {
	if len(models) == 0 {
		return "0 available"
	}
	if limit <= 0 || len(models) <= limit {
		return fmt.Sprintf("%d available: %s", len(models), strings.Join(models, ", "))
	}
	return fmt.Sprintf("%d available (showing %d): %s", len(models), limit, strings.Join(models[:limit], ", "))
}

func okNo(v bool) string {
	if v {
		return "ok"
	}
	return "no"
}

func timeUntilRounded(t time.Time) string {
	resetIn := time.Until(t).Round(time.Minute)
	if resetIn <= 0 {
		return ""
	}
	return resetIn.String()
}

func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(n)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", n, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}
