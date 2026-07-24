package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/lsp"
)

// cmdLSP renders the same readiness signal as the model-facing lsp_status tool,
// but as a user-facing slash command so setup is discoverable without asking the
// model to call a tool.
func cmdLSP(m Model, args []string) (Model, tea.Cmd) {
	if !stringInSlice(m.experimentalEnabled, string(experimental.LSPCodeIntelligence)) {
		m.appendLine(styleError.Render("[lsp] experimental feature is disabled — enable lsp_code_intelligence in [experimental]"))
		return m, nil
	}
	path := m.cwd
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		path = args[0]
	}
	langs, err := lsp.DetectWorkspace(context.Background(), path, 2000)
	if err != nil {
		m.appendLine(styleError.Render("[lsp] " + err.Error()))
		return m, nil
	}
	langs = lsp.ApplyOverridesToDetected(langs, m.fileCfg.LSP.Servers)
	if len(langs) == 0 {
		m.appendLine(styleAuto.Render("[lsp] no supported languages detected (Go, TypeScript/JavaScript, Python, Rust)"))
		return m, nil
	}
	m.appendLine(styleAssistantHeader.Render("LSP status"))
	for _, lang := range langs {
		status := styleError.Render("missing")
		if lang.ServerAvailable {
			status = styleAuto.Render("installed")
		}
		m.appendLine(fmt.Sprintf("  %s  files=%d  server=%s  %s", lang.Name, lang.FilesAvailable, strings.Join(lang.Command, " "), status))
		if !lang.ServerAvailable {
			m.appendLine(styleAuto.Render("    hint: " + lang.InstallHint))
		}
	}
	if m.lspManager != nil {
		stats := m.lspManager.Stats()
		m.appendLine(styleAuto.Render(fmt.Sprintf("  manager  open=%d/%d  starts=%d  reuses=%d  evictions=%d  last_start=%s", stats.OpenServers, stats.MaxServers, stats.Starts, stats.Reuses, stats.Evictions, stats.LastStart)))
	}
	return m, nil
}

func stringInSlice(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
