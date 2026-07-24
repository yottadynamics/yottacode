package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/lsp"
)

// LSPDoctorResult is the command-line doctor snapshot for the experimental LSP
// feature. It stays independent of the session-owned LSP manager because doctor
// is a preflight command, not a chat session.
type LSPDoctorResult struct {
	Enabled   bool                `json:"enabled"`
	Languages []LSPDoctorLanguage `json:"languages,omitempty"`
	Manager   LSPDoctorManager    `json:"manager"`
	Note      string              `json:"note,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// LSPDoctorLanguage reports one supported language detected in the workspace.
type LSPDoctorLanguage struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Files           int      `json:"files"`
	Command         []string `json:"command"`
	ServerAvailable bool     `json:"server_available"`
	InstallHint     string   `json:"install_hint,omitempty"`
	Override        bool     `json:"override"`
}

// LSPDoctorManager reports the default session manager settings. Doctor does
// not start chat-session servers, so runtime counters are always zero here.
type LSPDoctorManager struct {
	MaxServers int `json:"max_servers"`
}

func probeLSPDoctor(ctx context.Context, opts cli.ChatOptions, cfg config.Config) LSPDoctorResult {
	set := experimental.NewSet()
	for _, name := range opts.Experimental {
		set.Enable(name)
	}
	for name, enabled := range cfg.Experimental {
		if enabled {
			set.Enable(name)
		}
	}
	if !set.IsEnabled(experimental.LSPCodeIntelligence) {
		return LSPDoctorResult{Enabled: false, Note: "enable with --experimental lsp_code_intelligence or [experimental].lsp_code_intelligence = true"}
	}
	langs, err := lsp.DetectWorkspace(ctx, ".", 2000)
	if err != nil {
		return LSPDoctorResult{Enabled: true, Error: err.Error()}
	}
	langs = lsp.ApplyOverridesToDetected(langs, cfg.LSP.Servers)
	out := LSPDoctorResult{Enabled: true, Manager: LSPDoctorManager{MaxServers: lsp.DefaultManagerMaxServers()}, Note: "experimental opt-in; servers are local subprocesses and are never auto-installed"}
	for _, lang := range langs {
		out.Languages = append(out.Languages, LSPDoctorLanguage{
			ID:              lang.ID,
			Name:            lang.Name,
			Files:           lang.FilesAvailable,
			Command:         append([]string(nil), lang.Command...),
			ServerAvailable: lang.ServerAvailable,
			InstallHint:     installHintIfMissing(lang),
			Override:        len(cfg.LSP.Servers[lang.ID]) > 0,
		})
	}
	if len(out.Languages) == 0 {
		out.Note = "enabled; no supported languages detected in this workspace"
	}
	return out
}

func renderLSPDoctor(result LSPDoctorResult) string {
	var b strings.Builder
	b.WriteString("\n\nLSP Code Intelligence:\n")
	fmt.Fprintf(&b, "feature: %s", yesNo(result.Enabled))
	if result.Note != "" {
		fmt.Fprintf(&b, " (%s)", result.Note)
	}
	if result.Error != "" {
		fmt.Fprintf(&b, "\nerror: %s", result.Error)
		return b.String()
	}
	if result.Enabled && result.Manager.MaxServers > 0 {
		fmt.Fprintf(&b, "\nmanager: max_servers=%d (runtime open/start/reuse stats are available in lsp_status)", result.Manager.MaxServers)
	}
	for _, lang := range result.Languages {
		status := "missing"
		if lang.ServerAvailable {
			status = "installed"
		}
		override := ""
		if lang.Override {
			override = " override=yes"
		}
		fmt.Fprintf(&b, "\n- %s: files=%d server=%s status=%s%s", lang.Name, lang.Files, strings.Join(lang.Command, " "), status, override)
		if !lang.ServerAvailable && lang.InstallHint != "" {
			fmt.Fprintf(&b, "\n  hint: %s", lang.InstallHint)
		}
	}
	return b.String()
}

func installHintIfMissing(lang lsp.DetectedLanguage) string {
	if lang.ServerAvailable {
		return ""
	}
	return lang.InstallHint
}
