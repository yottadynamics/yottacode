// Package doctor provides the shared diagnostic probes used by interactive and
// command-line health reports. Results are intentionally data-only so each UI can
// render the same status and issue semantics without parsing human output.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	gh "github.com/yottadynamics/yottacode/internal/github"
	"github.com/yottadynamics/yottacode/internal/lsp"
)

// Status is the common doctor status vocabulary.
type Status string

const (
	StatusOK      Status = "ok"
	StatusWarning Status = "warning"
	StatusIssue   Status = "issue"
	StatusSkipped Status = "skipped"
)

// Result is the complete provider and local-tool diagnostic snapshot.
type Result struct {
	Provider adapter.ProbeResult
	GitHub   GitHubResult
	LSP      LSPResult
	Media    MediaResult
	Sandbox  Section
}

// GitHubResult reports authentication and API reachability.
type GitHubResult struct {
	Status      Status
	TokenSource string
	Reachable   bool
	AuthOK      bool
	Login       string
	Issues      []string
	Warnings    []string
}

// LSPResult reports detected language servers without starting persistent session servers.
type LSPResult struct {
	Status    Status
	Enabled   bool
	Languages []LSPLanguage
	Note      string
	Error     string
}

// LSPLanguage reports one detected language server.
type LSPLanguage struct {
	ID              string
	Name            string
	Files           int
	ServerAvailable bool
	InstallHint     string
	Probe           string
	Capabilities    string
}

// MediaResult reports local media binary readiness.
type MediaResult struct {
	Status   Status
	FFmpeg   Binary
	FFprobe  Binary
	Issues   []string
	Warnings []string
}

// Binary describes one executable used by media tools.
type Binary struct {
	Command     string
	Installed   bool
	Path        string
	Required    bool
	InstallHint string
}

// Section is a category that is not probed by this package yet.
type Section struct {
	Status Status
	Note   string
}

// Probe runs all independent diagnostics with bounded network and LSP probes.
func Probe(ctx context.Context, cwd string, provider adapter.Config, lspConfig config.LSPConfig, sandboxEnabled bool) Result {
	result := Result{Sandbox: Section{Status: StatusSkipped, Note: "sandbox cache diagnostics are reported by the CLI doctor"}}
	result.Provider = adapter.Probe(ctx, provider)
	result.GitHub = probeGitHub(ctx, cwd)
	result.LSP = probeLSP(ctx, cwd, lspConfig)
	result.Media = probeMedia()
	if sandboxEnabled {
		result.Sandbox = Section{Status: StatusSkipped, Note: "sandbox cache diagnostics are not yet available in the TUI"}
	}
	return result
}

func status(issues, warnings []string) Status {
	if len(issues) > 0 {
		return StatusIssue
	}
	if len(warnings) > 0 {
		return StatusWarning
	}
	return StatusOK
}

func probeGitHub(ctx context.Context, cwd string) (r GitHubResult) {
	resolver := gh.NewTokenResolver()
	_, source, err := resolver.Resolve(ctx)
	if err != nil {
		if errors.Is(err, gh.ErrNoToken) {
			r.Issues = []string{"no GitHub token configured"}
		} else {
			r.Issues = []string{fmt.Sprintf("auth resolution failed: %v", err)}
		}
		r.Status = status(r.Issues, r.Warnings)
		return
	}
	r.TokenSource = source
	client := gh.NewTypedClient(cwd)
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	user, err := client.AuthedUserLogin(probeCtx)
	if err != nil {
		r.Issues = []string{fmt.Sprintf("GitHub API probe failed: %v", err)}
		r.Status = status(r.Issues, r.Warnings)
		return
	}
	r.Login = user
	r.Status = status(r.Issues, r.Warnings)
	return
}

func probeLSP(ctx context.Context, cwd string, cfg config.LSPConfig) LSPResult {
	langs, err := lsp.DetectWorkspace(ctx, cwd, 2000)
	if err != nil {
		return LSPResult{Status: StatusIssue, Enabled: true, Error: err.Error()}
	}
	r := LSPResult{Status: StatusOK, Enabled: true, Note: "servers are local subprocesses and are never auto-installed"}
	for _, lang := range lsp.ApplyOverridesToDetected(langs, cfg.Servers) {
		p := "missing"
		if lang.ServerAvailable {
			p = "ok"
			c, cancel := context.WithTimeout(ctx, 4*time.Second)
			client, e := lsp.NewClient(c, lang.Language, lsp.WorkspaceRoot(cwd, lang.Language, "."))
			cancel()
			if e != nil {
				p = "failed: " + e.Error()
			} else {
				_ = client.Close()
			}
		}
		r.Languages = append(r.Languages, LSPLanguage{ID: lang.ID, Name: lang.Name, Files: lang.FilesAvailable, ServerAvailable: lang.ServerAvailable, InstallHint: lang.InstallHint, Probe: p})
		if strings.HasPrefix(p, "failed:") {
			r.Status = StatusIssue
		} else if p == "missing" && r.Status == StatusOK {
			r.Status = StatusWarning
		}
	}
	if len(r.Languages) == 0 {
		r.Note = "no supported languages detected in this workspace"
	}
	return r
}

func probeMedia() MediaResult {
	r := MediaResult{FFmpeg: binary("ffmpeg", true), FFprobe: binary("ffprobe", true)}
	if !r.FFmpeg.Installed {
		r.Issues = append(r.Issues, "ffmpeg is required for media tools")
	}
	if !r.FFprobe.Installed {
		r.Issues = append(r.Issues, "ffprobe is required for media_probe")
	}
	r.Status = status(r.Issues, r.Warnings)
	return r
}
func binary(command string, required bool) Binary {
	b := Binary{Command: command, Required: required}
	if p, e := exec.LookPath(command); e == nil {
		b.Installed = true
		b.Path = p
	}
	return b
}
