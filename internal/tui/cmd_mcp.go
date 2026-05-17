package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

// MCP-status styles. Stay narrow and reuse existing color tokens so
// /mcp matches the visual vocabulary of /subagents and /provider.
var (
	styleMCPHeader = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleMCPName   = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleMCPOK     = lipgloss.NewStyle().Foreground(colorSuccess)
	styleMCPFail   = lipgloss.NewStyle().Foreground(colorError)
	styleMCPMeta   = lipgloss.NewStyle().Foreground(colorMuted)
)

// cmdMCP implements the /mcp slash command.
//
//	/mcp                  — list configured servers, their status, tool count
//	/mcp logs <name>      — dump recent stderr from a server
//	/mcp restart <name>   — stop + respawn one server, replace its tools in the registry
func cmdMCP(m Model, args []string) (Model, tea.Cmd) {
	mgr := m.mcpManager
	if mgr == nil || len(mgr.Clients()) == 0 {
		m.appendLine(styleMCPMeta.Render("no MCP servers configured (add [[mcp_servers]] to ~/.yottacode/config.toml)"))
		return m, nil
	}

	if len(args) == 0 {
		renderMCPStatus(&m, mgr)
		return m, nil
	}

	switch strings.ToLower(args[0]) {
	case "logs":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /mcp logs <server-name>"))
			return m, nil
		}
		name := args[1]
		client := mgr.Client(name)
		if client == nil {
			m.appendLine(styleError.Render(fmt.Sprintf("no MCP server named %q (try /mcp to list)", name)))
			return m, nil
		}
		// Only StdioClient captures stderr today; type-assert without
		// crashing if a future transport (HTTP) replaces it.
		sc, ok := client.(*mcp.StdioClient)
		if !ok {
			m.appendLine(styleMCPMeta.Render(fmt.Sprintf("server %q has no stderr (non-stdio transport)", name)))
			return m, nil
		}
		lines := sc.StderrTail()
		if len(lines) == 0 {
			m.appendLine(styleMCPMeta.Render(fmt.Sprintf("server %q has produced no stderr yet", name)))
			return m, nil
		}
		m.appendLine(styleMCPHeader.Render(fmt.Sprintf("── %s — stderr (last %d lines) ──", name, len(lines))))
		for _, ln := range lines {
			m.appendLine(ln)
		}
	case "restart":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /mcp restart <server-name>"))
			return m, nil
		}
		restartMCPServer(&m, mgr, args[1])
	default:
		m.appendLine(styleError.Render(fmt.Sprintf("unknown /mcp subcommand %q (try: logs <name>)", args[0])))
	}
	return m, nil
}

// restartMCPServer drives the full restart flow for one MCP server:
// drop its prior generation of tools from the agent registry, ask
// the manager to stop + respawn the subprocess, then register the
// fresh generation's tools. The registry-write side has to live in
// the TUI package because the mcp package doesn't know about the
// agent registry — that's intentional separation, not a layering
// bug.
//
// If the restarted server fails to come back (bad command, init
// timeout, crashed on boot), the old tools are still removed and
// nothing replaces them — the user sees the failure in the rendered
// status line and can fix config + restart again. We don't try to
// "preserve" the old tools because their client is already torn down
// at that point.
func restartMCPServer(m *Model, mgr *mcp.Manager, name string) {
	if mgr.Client(name) == nil {
		m.appendLine(styleError.Render(fmt.Sprintf("no MCP server named %q (try /mcp to list)", name)))
		return
	}

	registry := m.cfg.Registry
	// Best-effort: list the current generation's tools so we can
	// drop them by name. If ListTools fails (e.g. the server is
	// already dead), we still proceed — the manager's Restart
	// rebuilds from config either way, and stale tool entries
	// without a live Client will already error on next invocation.
	listCtx, listCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if oldTools, err := mgr.Client(name).ListTools(listCtx); err == nil && registry != nil {
		for _, td := range oldTools {
			registry.Deregister("mcp/" + name + "/" + td.Name)
		}
	}
	listCancel()

	restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer restartCancel()
	result, err := mgr.Restart(restartCtx, name)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("restart %q: %v", name, err)))
		return
	}
	if result.Err != nil {
		m.appendLine(styleMCPFail.Render(fmt.Sprintf("server %q failed to restart: %v", name, result.Err)))
		return
	}
	for _, w := range result.Warnings {
		m.appendLine(styleMCPMeta.Render(w))
	}

	// Re-register the post-restart tool generation. ListTools on the
	// fresh client should always succeed at this point — Manager
	// already validated the catalog as part of Restart's
	// post-Start dance.
	fresh := mgr.Client(name)
	tools, err := fresh.ListTools(restartCtx)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("restart %q: list tools: %v", name, err)))
		return
	}
	if registry != nil {
		for _, td := range tools {
			registry.Register(&agent.MCPTool{
				Server:      name,
				ToolName:    td.Name,
				Desc:        td.Description,
				InputSchema: td.InputSchema,
				ReadOnly:    td.ReadOnlyHint,
				Client:      fresh,
			})
		}
	}
	m.appendLine(styleMCPOK.Render(fmt.Sprintf("server %q restarted — %d tools registered", name, len(tools))))
}

// renderMCPStatus prints a single block summarizing every configured
// MCP server: name, transport, status, tool count, last error.
func renderMCPStatus(m *Model, mgr *mcp.Manager) {
	statuses := mgr.Statuses()
	m.appendLine(styleMCPHeader.Render(fmt.Sprintf("MCP servers (%d configured)", len(statuses))))
	for _, st := range statuses {
		var statusBadge, detail string
		if st.Err != nil {
			statusBadge = styleMCPFail.Render("failed")
			detail = styleMCPFail.Render(st.Err.Error())
		} else {
			statusBadge = styleMCPOK.Render("running")
			detail = styleMCPMeta.Render(fmt.Sprintf("%d tools", st.ToolCount))
		}
		m.appendLine(fmt.Sprintf("  %s  %s  %s",
			styleMCPName.Render(st.Name), statusBadge, detail))
	}
	m.appendLine(styleMCPMeta.Render("  type `/mcp logs <name>` to inspect a server's stderr"))
}
