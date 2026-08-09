package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

// mcpStartupDoneMsg is sent by the background MCP startup cmd once
// all configured servers have attempted initialization. The Update
// loop registers tools for the successful ones.
type mcpStartupDoneMsg struct {
	results []mcp.StartResult
}

// startMCPServers returns a tea.Cmd that starts every configured MCP
// server in the background. The TUI renders immediately; tools are
// registered when the msg lands in Update.
func startMCPServers(ctx context.Context, mgr *mcp.Manager) tea.Cmd {
	if mgr == nil {
		return nil
	}
	return func() tea.Msg {
		return mcpStartupDoneMsg{results: mgr.Start(ctx)}
	}
}

// MCP-status styles. Stay narrow and reuse existing color tokens so
// /mcp matches the visual vocabulary of /subagents and /provider.
var (
	styleMCPHeader = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleMCPName   = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleMCPOK     = lipgloss.NewStyle().Foreground(colorSuccess)
	styleMCPFail   = lipgloss.NewStyle().Foreground(colorError)
	styleMCPMeta   = lipgloss.NewStyle().Foreground(colorDim)
)

// cmdMCP implements the /mcp slash command.
//
//	/mcp                       — list configured servers, their status, tool count
//	/mcp add <name> --command  — add a server to config.toml
//	/mcp remove <name>         — remove a server from config.toml
//	/mcp logs <name>           — dump recent stderr from a server
//	/mcp restart <name>        — stop + respawn one server, replace its tools in the registry
func cmdMCP(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.openMCPPicker()
		return m, nil
	}

	switch strings.ToLower(args[0]) {
	case "add":
		return mcpAdd(m, args[1:])
	case "remove":
		return mcpRemove(m, args[1:])
	case "list":
		mgr := m.mcpManager
		if mgr == nil || len(mgr.Clients()) == 0 {
			m.appendLine(styleMCPMeta.Render("no MCP servers configured (use /mcp add to add one)"))
			return m, nil
		}
		renderMCPStatus(&m, mgr)
		return m, nil
	}

	mgr := m.mcpManager
	if mgr == nil {
		m.appendLine(styleMCPMeta.Render("no MCP servers configured"))
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
		m.appendLine(styleError.Render(fmt.Sprintf("unknown /mcp subcommand %q (try: add, remove, logs, restart)", args[0])))
	}
	return m, nil
}

// mcpAdd implements `/mcp add <name> --command <cmd> [args...]`.
// Everything after --command is the executable + args. Appends a new
// [[mcp_servers]] entry to config.toml, starts the server, and
// registers its tools — no restart needed.
func mcpAdd(m Model, args []string) (Model, tea.Cmd) {
	var name string
	var cmdTokens []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--command" {
			cmdTokens = args[i+1:]
			break
		}
		if name == "" {
			name = args[i]
		}
	}
	for j := range cmdTokens {
		cmdTokens[j] = strings.Trim(cmdTokens[j], `"'`)
	}
	parts := make([]string, 0, len(cmdTokens))
	for _, t := range cmdTokens {
		if t != "" {
			parts = append(parts, t)
		}
	}
	if name == "" || len(parts) == 0 {
		m.appendLine(styleError.Render("usage: /mcp add <name> --command <cmd> [args...]"))
		return m, nil
	}

	cfg := loadConfigForCommand(m)
	for _, s := range cfg.MCPServers {
		if s.Name == name {
			m.appendLine(styleError.Render(fmt.Sprintf("MCP server %q already exists; remove it first with /mcp remove %s", name, name)))
			return m, nil
		}
	}

	server := config.MCPServer{
		Name:    name,
		Command: parts[0],
		Args:    parts[1:],
	}
	cfg.MCPServers = append(cfg.MCPServers, server)

	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("failed to write config: %v", err)))
		return m, nil
	}

	argsHint := ""
	if len(parts) > 1 {
		argsHint = fmt.Sprintf(" %s", strings.Join(parts[1:], " "))
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] added %q (%s%s)", name, parts[0], argsHint)))

	mgr := m.mcpManager
	if mgr == nil {
		m.appendLine(styleAuto.Render("[mcp] restart yottacode to start this server"))
		return m, nil
	}

	m.appendLine(styleAuto.Render("[mcp] starting server..."))
	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startCancel()
	result, err := mgr.Add(startCtx, server)
	if err != nil {
		_ = mgr.Remove(startCtx, name)
		result, err = mgr.Add(startCtx, server)
	}
	if err != nil {
		m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] failed to start %q: %v", name, err)))
		return m, nil
	}
	if result.Err != nil {
		m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] server %q failed to start: %v", name, result.Err)))
		m.appendLine(styleAuto.Render("[mcp] check /mcp logs " + name + " for details"))
		return m, nil
	}
	for _, w := range result.Warnings {
		m.appendLine(styleAuto.Render("[mcp] " + w))
	}

	fresh := mgr.Client(name)
	tools, err := fresh.ListTools(startCtx)
	if err != nil {
		m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] %s: list tools: %v", name, err)))
		return m, nil
	}
	registry := m.cfg.Registry
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
	m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] server %q started — %d tools registered", name, len(tools))))
	return m, nil
}

// mcpRemove implements `/mcp remove <name>`.
// Removes the named [[mcp_servers]] entry from config.toml.
func mcpRemove(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendLine(styleError.Render("usage: /mcp remove <server-name>"))
		return m, nil
	}
	name := args[0]

	cfg := loadConfigForCommand(m)
	found := -1
	for i, s := range cfg.MCPServers {
		if s.Name == name {
			found = i
			break
		}
	}
	if found < 0 {
		m.appendLine(styleError.Render(fmt.Sprintf("no MCP server named %q in config.toml", name)))
		return m, nil
	}

	cfg.MCPServers = append(cfg.MCPServers[:found], cfg.MCPServers[found+1:]...)

	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("failed to write config: %v", err)))
		return m, nil
	}

	m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] removed %q", name)))
	stopMCPServer(&m, name)
	return m, nil
}

// stopMCPServer deregisters a server's tools and removes it from the
// manager. Used by /mcp remove and the picker's remove action.
func stopMCPServer(m *Model, name string) {
	mgr := m.mcpManager
	if mgr == nil {
		return
	}
	registry := m.cfg.Registry
	if registry != nil {
		listCtx, listCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if client := mgr.Client(name); client != nil {
			if tools, err := client.ListTools(listCtx); err == nil {
				for _, td := range tools {
					registry.Deregister("mcp/" + name + "/" + td.Name)
				}
			}
		}
		listCancel()
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = mgr.Remove(stopCtx, name)
	stopCancel()
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

// handleMCPStartupDone processes the async MCP startup results: registers
// tools for successful servers, logs failures to stderr-style output.
func (m *Model) handleMCPStartupDone(results []mcp.StartResult) {
	mgr := m.mcpManager
	if mgr == nil {
		return
	}
	registry := m.cfg.Registry
	for _, r := range results {
		if r.Err != nil {
			m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] server %q failed to start: %v", r.Name, r.Err)))
			continue
		}
		client := mgr.Client(r.Name)
		if client == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tools, err := client.ListTools(ctx)
		cancel()
		if err != nil {
			m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] %s: list tools: %v", r.Name, err)))
			continue
		}
		if registry != nil {
			for _, td := range tools {
				registry.Register(&agent.MCPTool{
					Server:      r.Name,
					ToolName:    td.Name,
					Desc:        td.Description,
					InputSchema: td.InputSchema,
					ReadOnly:    td.ReadOnlyHint,
					Client:      client,
				})
			}
		}
	}
}

// renderMCPStatus prints a single block summarizing every configured
// MCP server: name, transport, status, tool count, last error.
func renderMCPStatus(m *Model, mgr *mcp.Manager) {
	names := mgr.Names()
	statuses := mgr.Statuses()
	m.appendLine(styleMCPHeader.Render(fmt.Sprintf("MCP servers (%d configured)", len(names))))
	for i, st := range statuses {
		name := names[i]
		var statusBadge, detail string
		if st.Name == "" {
			statusBadge = styleMCPMeta.Render("starting...")
			detail = ""
		} else if st.Err != nil {
			statusBadge = styleMCPFail.Render("failed")
			detail = styleMCPFail.Render(st.Err.Error())
		} else {
			statusBadge = styleMCPOK.Render("running")
			detail = styleMCPMeta.Render(fmt.Sprintf("%d tools", st.ToolCount))
		}
		m.appendLine(fmt.Sprintf("  %s  %s  %s",
			styleMCPName.Render(name), statusBadge, detail))
	}
	m.appendLine(styleMCPMeta.Render("  type `/mcp logs <name>` to inspect a server's stderr"))
}
