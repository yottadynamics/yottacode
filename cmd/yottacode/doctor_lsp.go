package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/lsp"
)

// LSPDoctorResult is the command-line doctor snapshot for LSP Code
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
	Probe           string   `json:"probe,omitempty"`
	Capabilities    string   `json:"capabilities,omitempty"`
	Override        bool     `json:"override"`
}

// LSPDoctorManager reports the default session manager settings. Doctor does
// not start chat-session servers, so runtime counters are always zero here.
type LSPDoctorManager struct {
	MaxServers int `json:"max_servers"`
}

func probeLSPDoctor(ctx context.Context, _ cli.ChatOptions, cfg config.Config) LSPDoctorResult {
	langs, err := lsp.DetectWorkspace(ctx, ".", 2000)
	if err != nil {
		return LSPDoctorResult{Enabled: true, Error: err.Error()}
	}
	langs = lsp.ApplyOverridesToDetected(langs, cfg.LSP.Servers)
	out := LSPDoctorResult{Enabled: true, Manager: LSPDoctorManager{MaxServers: lsp.DefaultManagerMaxServers()}, Note: "default-on; servers are local subprocesses and are never auto-installed"}
	for _, lang := range langs {
		probe := "missing"
		caps := ""
		disabled := lspDisabled(cfg.LSP.Disabled, lang.ID)
		if disabled {
			probe = "skipped: disabled"
		} else if lang.ServerAvailable {
			probe = "ok"
			probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			client, err := lsp.NewClient(probeCtx, lang.Language, lsp.WorkspaceRoot(".", lang.Language, "."))
			cancel()
			if err != nil {
				probe = "failed: " + err.Error()
			} else {
				caps = formatDoctorLSPCapabilities(client.Capabilities())
				_ = client.Close()
			}
		}
		out.Languages = append(out.Languages, LSPDoctorLanguage{
			ID:              lang.ID,
			Name:            lang.Name,
			Files:           lang.FilesAvailable,
			Command:         append([]string(nil), lang.Command...),
			ServerAvailable: lang.ServerAvailable,
			InstallHint:     installHintIfMissing(lang),
			Probe:           probe,
			Capabilities:    caps,
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
		if lang.Probe != "" {
			fmt.Fprintf(&b, "\n  probe: %s", lang.Probe)
		}
		if lang.Capabilities != "" {
			fmt.Fprintf(&b, "\n  capabilities: %s", lang.Capabilities)
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

func lspDisabled(disabled []string, id string) bool {
	for _, candidate := range disabled {
		if candidate == id {
			return true
		}
	}
	return false
}

func formatDoctorLSPCapabilities(c lsp.Capabilities) string {
	parts := make([]string, 0, 16)
	add := func(name string, ok bool) {
		if ok {
			parts = append(parts, name)
		}
	}
	add("workspace_symbol", c.WorkspaceSymbol)
	add("document_symbol", c.DocumentSymbol)
	add("document_highlight", c.DocumentHighlight)
	add("selection_range", c.SelectionRange)
	add("definition", c.Definition)
	add("type_definition", c.TypeDefinition)
	add("implementation", c.Implementation)
	add("references", c.References)
	add("hover", c.Hover)
	add("signature_help", c.SignatureHelp)
	add("code_action", c.CodeAction)
	add("code_action_resolve", c.CodeActionResolve)
	add("call_hierarchy", c.CallHierarchy)
	add("rename", c.Rename)
	add("rename_prepare", c.RenamePrepare)
	add("formatting", c.Formatting)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}
