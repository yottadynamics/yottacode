package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/lsp"
	"github.com/yottadynamics/yottacode/internal/memory"
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

// cmdHealth gives the user one quick workspace-state card: source control,
// provider connection, memory, LSP readiness, MCP, and experimental flags.
func cmdHealth(m Model, _ []string) (Model, tea.Cmd) {
	var b strings.Builder
	b.WriteString("Workspace health\n")
	fmt.Fprintf(&b, "git: branch=%s dirty=%s\n", tuiEmptyAs(m.branch, "unknown"), tuiYesNo(gitDirty(m.cwd)))
	fmt.Fprintf(&b, "provider: %s model=%s connection=%s\n", tuiEmptyAs(m.providerLabel, m.provider), tuiEmptyAs(m.modelName, "unset"), renderConnectionSummary(m.connection))
	fmt.Fprintf(&b, "memory: %s\n", tuiEmptyAs(m.memorySummary, "none"))
	fmt.Fprintf(&b, "experimental: %s\n", tuiEmptyAs(strings.Join(m.experimentalEnabled, ", "), "none"))
	if m.mcpManager == nil || len(m.mcpManager.Names()) == 0 {
		b.WriteString("mcp: none\n")
	} else {
		b.WriteString(fmt.Sprintf("mcp: %d configured\n", len(m.mcpManager.Names())))
	}
	if stringInSlice(m.experimentalEnabled, string(experimental.LSPCodeIntelligence)) {
		langs, err := lsp.DetectWorkspace(context.Background(), m.cwd, 2000)
		if err == nil {
			langs = lsp.ApplyOverridesToDetected(langs, m.fileCfg.LSP.Servers)
			var installed, missing int
			for _, lang := range langs {
				if lang.ServerAvailable {
					installed++
				} else {
					missing++
				}
			}
			fmt.Fprintf(&b, "lsp: %d languages (%d installed, %d missing)\n", len(langs), installed, missing)
			if m.lspManager != nil {
				stats := m.lspManager.Stats()
				fmt.Fprintf(&b, "lsp_manager: open=%d/%d starts=%d reuses=%d evictions=%d last_start=%s\n", stats.OpenServers, stats.MaxServers, stats.Starts, stats.Reuses, stats.Evictions, stats.LastStart)
			}
		} else {
			fmt.Fprintf(&b, "lsp: error: %v\n", err)
		}
	} else {
		b.WriteString("lsp: disabled\n")
	}
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return m, nil
}

// cmdHandoff writes a concise state note to the transcript for copying into a
// later session, issue, or PR description.
func cmdHandoff(m Model, _ []string) (Model, tea.Cmd) {
	changed := gitLines(m.cwd, "status", "--short")
	var b strings.Builder
	b.WriteString("Handoff note\n")
	fmt.Fprintf(&b, "- Goal: TODO\n")
	fmt.Fprintf(&b, "- Branch: %s\n", tuiEmptyAs(m.branch, "unknown"))
	fmt.Fprintf(&b, "- Changed files:\n%s\n", tuiIndentOrNone(strings.Join(changed, "\n")))
	fmt.Fprintf(&b, "- Tests run: TODO\n")
	fmt.Fprintf(&b, "- Open risks: TODO\n")
	fmt.Fprintf(&b, "- Next step: TODO\n")
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return m, nil
}

// cmdConfigDoctor validates local configuration beyond provider auth: unknown
// config keys are already caught by config.Load; this adds PATH checks for LSP
// and MCP commands plus memory embedding availability.
func cmdConfigDoctor(m Model, _ []string) (Model, tea.Cmd) {
	var b strings.Builder
	b.WriteString("Config doctor\n")
	for _, lang := range lsp.ApplyOverridesToDetected(detectedAllLanguages(), m.fileCfg.LSP.Servers) {
		if lang.ServerAvailable {
			fmt.Fprintf(&b, "✓ lsp.%s: %s\n", lang.ID, strings.Join(lang.Command, " "))
		} else {
			fmt.Fprintf(&b, "⚠ lsp.%s: %s missing — %s\n", lang.ID, lang.Command[0], lang.InstallHint)
		}
	}
	for _, s := range m.fileCfg.MCPServers {
		if s.Disabled {
			fmt.Fprintf(&b, "- mcp.%s: disabled\n", s.Name)
			continue
		}
		if _, err := exec.LookPath(s.Command); err != nil {
			fmt.Fprintf(&b, "⚠ mcp.%s: command %q missing on PATH\n", s.Name, s.Command)
		} else {
			fmt.Fprintf(&b, "✓ mcp.%s: %s\n", s.Name, s.Command)
		}
	}
	if m.fileCfg.Retrieval.Strategy == "semantic" || m.fileCfg.Retrieval.Strategy == "auto" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ec := memory.NewEmbedClient("", m.fileCfg.Retrieval.EmbeddingModel)
		reachable, installed := ec.Status(ctx)
		switch {
		case installed:
			fmt.Fprintf(&b, "✓ memory.embedding: %s\n", ec.Model)
		case reachable:
			fmt.Fprintf(&b, "⚠ memory.embedding: %s missing — run `ollama pull %s`\n", ec.Model, ec.Model)
		default:
			fmt.Fprintf(&b, "⚠ memory.embedding: Ollama unavailable for %s\n", ec.Model)
		}
	}
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return m, nil
}

func detectedAllLanguages() []lsp.DetectedLanguage {
	out := make([]lsp.DetectedLanguage, 0, len(lsp.Languages()))
	for _, lang := range lsp.Languages() {
		out = append(out, lsp.DetectedLanguage{Language: lang, ServerAvailable: lsp.ServerAvailable(lang)})
	}
	return out
}

func gitDirty(cwd string) bool { return len(gitLines(cwd, "status", "--short")) > 0 }

func gitLines(cwd string, args ...string) []string {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func stringInSlice(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func tuiEmptyAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func tuiYesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func tuiIndentOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "  (none)"
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		lines = append(lines, "  "+line)
	}
	return strings.Join(lines, "\n")
}
