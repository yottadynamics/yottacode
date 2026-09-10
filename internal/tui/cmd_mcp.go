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
	styleMCPMeta   = lipgloss.NewStyle().Foreground(colorMuted)
)

// cmdMCP implements the /mcp slash command.
//
//	/mcp                                          — list configured servers, their status, tool count
//	/mcp add <name> --command <cmd> [args...]     — add a stdio server to config.toml
//	/mcp add <name> --transport http|sse --url .. — add a remote server to config.toml
//	/mcp remove <name>                            — remove a server from config.toml
//	/mcp logs <name>                               — dump recent log output from a server
//	/mcp restart <name>                            — stop + respawn one server, replace its tools in the registry
//	/mcp enable <name>                             — start a disabled server and register its tools
//	/mcp disable <name>                            — stop a server without removing it from config.toml
//	/mcp tools <name>                              — list a server's full catalog with shown/hidden filter markers
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
		if mgr == nil || len(mgr.Names()) == 0 {
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
		src, ok := client.(mcp.LogSource)
		if !ok {
			m.appendLine(styleMCPMeta.Render(fmt.Sprintf("server %q has no logs available", name)))
			return m, nil
		}
		lines := src.LogTail()
		if len(lines) == 0 {
			m.appendLine(styleMCPMeta.Render(fmt.Sprintf("server %q has produced no log output yet", name)))
			return m, nil
		}
		m.appendLine(styleMCPHeader.Render(fmt.Sprintf("── %s — logs (last %d lines) ──", name, len(lines))))
		for _, ln := range lines {
			m.appendLine(ln)
		}
	case "restart":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /mcp restart <server-name>"))
			return m, nil
		}
		restartMCPServer(&m, mgr, args[1])
	case "enable":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /mcp enable <server-name>"))
			return m, nil
		}
		return mcpSetDisabled(m, mgr, args[1], false)
	case "disable":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /mcp disable <server-name>"))
			return m, nil
		}
		return mcpSetDisabled(m, mgr, args[1], true)
	case "tools":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /mcp tools <server-name>"))
			return m, nil
		}
		mcpListTools(&m, mgr, args[1])
	default:
		m.appendLine(styleError.Render(fmt.Sprintf("unknown /mcp subcommand %q (try: add, remove, logs, restart, enable, disable, tools)", args[0])))
	}
	return m, nil
}

// mcpAdd implements `/mcp add <name> --command <cmd> [args...]` for a stdio
// server, or `/mcp add <name> --transport http|sse --url <url> [--header
// K=V]... [--env K=V]... [--disabled]` for a remote one. Flags other than
// --command may appear in any order before it; --command consumes every
// token after it as the executable + args (unchanged from before), so it
// must come last. --header/--env values may contain spaces (e.g.
// "Authorization=Bearer sk-...") even though dispatchSlash tokenizes on
// whitespace with no quote-awareness — consumeFlagValue rejoins tokens
// until the next "--"-prefixed flag. Appends a new [[mcp_servers]] entry to
// config.toml; a non-disabled server starts immediately and registers its
// tools — no restart needed.
func mcpAdd(m Model, args []string) (Model, tea.Cmd) {
	var name, transport, url string
	var cmdTokens, headerFlags, envFlags []string
	var disabled bool
	for i := 0; i < len(args); {
		switch args[i] {
		case "--command":
			cmdTokens = args[i+1:]
			i = len(args)
		case "--transport":
			if i+1 < len(args) {
				transport = args[i+1]
			}
			i += 2
		case "--url":
			if i+1 < len(args) {
				url = args[i+1]
			}
			i += 2
		case "--header":
			var val string
			val, i = consumeFlagValue(args, i)
			if val != "" {
				headerFlags = append(headerFlags, val)
			}
		case "--env":
			var val string
			val, i = consumeFlagValue(args, i)
			if val != "" {
				envFlags = append(envFlags, val)
			}
		case "--disabled":
			disabled = true
			i++
		default:
			if name == "" {
				name = args[i]
			}
			i++
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
	const usage = "usage: /mcp add <name> --command <cmd> [args...]  OR  " +
		"/mcp add <name> --transport http --url <url> [--header K=V]..."
	if name == "" || (len(parts) == 0 && url == "") {
		m.appendLine(styleError.Render(usage))
		return m, nil
	}
	if len(parts) > 0 && url != "" {
		m.appendLine(styleError.Render("cannot combine --command with --url; pick one transport shape"))
		return m, nil
	}
	for _, tok := range stragglerFlagsIn(parts) {
		m.appendLine(styleMCPMeta.Render(fmt.Sprintf(
			"warning: %q looks like an /mcp add flag but appeared after --command — it was passed to the command verbatim instead. Move it before --command if that's not what you wanted.", tok)))
	}

	headerMap, err := parseKeyValueFlags(headerFlags)
	if err != nil {
		m.appendLine(styleError.Render("--header: " + err.Error()))
		return m, nil
	}
	envMap, err := parseKeyValueFlags(envFlags)
	if err != nil {
		m.appendLine(styleError.Render("--env: " + err.Error()))
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
		Name:      name,
		Transport: transport,
		URL:       url,
		Headers:   headerMap,
		Env:       envMap,
		Disabled:  disabled,
	}
	if len(parts) > 0 {
		server.Command = parts[0]
		server.Args = parts[1:]
	}

	if server.Transport == "http" || server.Transport == "sse" {
		policy := mcp.Policy{RequireTLS: cfg.MCP.RequireTLS, AllowedHosts: cfg.MCP.AllowedHosts}
		if err := policy.CheckURL(server.URL); err != nil {
			m.appendLine(styleError.Render(err.Error()))
			return m, nil
		}
	}

	cfg.MCPServers = append(cfg.MCPServers, server)

	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("failed to write config: %v", err)))
		return m, nil
	}

	if server.URL != "" {
		displayTransport := server.Transport
		if displayTransport == "" {
			displayTransport = "stdio"
		}
		m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] added %q (%s %s)", name, displayTransport, server.URL)))
	} else {
		argsHint := ""
		if len(parts) > 1 {
			argsHint = fmt.Sprintf(" %s", strings.Join(parts[1:], " "))
		}
		m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] added %q (%s%s)", name, parts[0], argsHint)))
	}

	if disabled {
		// Keep the disabled entry in the live manager so it can be enabled
		// without restarting the session.
		if mgr := m.mcpManager; mgr != nil {
			if _, err := mgr.Add(context.Background(), server); err != nil {
				m.appendLine(styleError.Render(fmt.Sprintf("[mcp] failed to register %q: %v", name, err)))
				return m, nil
			}
		}
		m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] server %q added as disabled — use /mcp enable %s to start it", name, name)))
		return m, nil
	}

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
		m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] server %q failed to start: %s", name, mcp.Redact(result.Err.Error()))))
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
	registerMCPTools(registry, cfg, name, fresh, tools)
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
		m.appendLine(styleMCPFail.Render(fmt.Sprintf("server %q failed to restart: %s", name, mcp.Redact(result.Err.Error()))))
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
	registerMCPTools(registry, m.fileCfg, name, fresh, tools)
	m.appendLine(styleMCPOK.Render(fmt.Sprintf("server %q restarted — %d tools registered", name, len(tools))))
}

// mcpSetDisabled implements `/mcp enable <name>` (disabled=false) and
// `/mcp disable <name>` (disabled=true): persists config.MCPServer.Disabled
// and starts/stops the live client to match, without requiring a session
// restart or hand-editing config.toml.
func mcpSetDisabled(m Model, mgr *mcp.Manager, name string, disabled bool) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	found := false
	for i := range cfg.MCPServers {
		if cfg.MCPServers[i].Name == name {
			cfg.MCPServers[i].Disabled = disabled
			found = true
			break
		}
	}
	if !found {
		m.appendLine(styleError.Render(fmt.Sprintf("no MCP server named %q (try /mcp to list)", name)))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("failed to write config: %v", err)))
		return m, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	registry := m.cfg.Registry

	if disabled {
		if client := mgr.Client(name); client != nil && registry != nil {
			if tools, err := client.ListTools(ctx); err == nil {
				for _, td := range tools {
					registry.Deregister("mcp/" + name + "/" + td.Name)
				}
			}
		}
		if err := mgr.Disable(ctx, name); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("disable %q: %v", name, err)))
			return m, nil
		}
		if err := writeConfig(cfg); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("failed to persist disable %q: %v", name, err)))
			return m, nil
		}
		m.appendLine(styleMCPOK.Render(fmt.Sprintf("server %q disabled", name)))
		return m, nil
	}

	result, err := mgr.Enable(ctx, name)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("enable %q: %v", name, err)))
		return m, nil
	}
	if result.Err != nil {
		m.appendLine(styleMCPFail.Render(fmt.Sprintf("server %q failed to start: %s", name, mcp.Redact(result.Err.Error()))))
		m.appendLine(styleAuto.Render("[mcp] check /mcp logs " + name + " for details"))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("failed to persist enable %q: %v", name, err)))
		return m, nil
	}
	if client := mgr.Client(name); client != nil {
		if tools, err := client.ListTools(ctx); err == nil {
			registerMCPTools(registry, cfg, name, client, tools)
		}
	}
	m.appendLine(styleMCPOK.Render(fmt.Sprintf("server %q enabled — %d tools registered", name, result.ToolCount)))
	return m, nil
}

// mcpListTools implements the `/mcp tools <name>` read-only inspector:
// lists the server's full advertised catalog with a shown/hidden marker
// per tool, reflecting the server's Include/Exclude filters.
func mcpListTools(m *Model, mgr *mcp.Manager, name string) {
	client := mgr.Client(name)
	if client == nil {
		m.appendLine(styleError.Render(fmt.Sprintf("no live client for %q (server may be disabled or failed to start)", name)))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	all, err := client.ListTools(ctx)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("tools %q: %v", name, err)))
		return
	}

	var include, exclude []string
	for _, s := range loadConfigForCommand(*m).MCPServers {
		if s.Name == name {
			include, exclude = s.Include, s.Exclude
			break
		}
	}
	kept, hidden := mcp.FilterTools(all, include, exclude)
	shown := make(map[string]bool, len(kept))
	for _, td := range kept {
		shown[td.Name] = true
	}

	m.appendLine(styleMCPHeader.Render(fmt.Sprintf("── %s — %d tools (%d hidden by filter) ──", name, len(kept), hidden)))
	for _, td := range all {
		badge := styleMCPOK.Render("shown ")
		if !shown[td.Name] {
			badge = styleMCPMeta.Render("hidden")
		}
		m.appendLine(fmt.Sprintf("  %s  %s", badge, td.Name))
	}
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
			m.appendLine(styleAuto.Render(fmt.Sprintf("[mcp] server %q failed to start: %s", r.Name, mcp.Redact(r.Err.Error()))))
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
		registerMCPTools(registry, m.fileCfg, r.Name, client, tools)
	}
}

// consumeFlagValue reads a --header/--env value starting at args[i+1] and
// returns it plus the index of the first unconsumed token. The TUI's slash
// command dispatcher tokenizes on whitespace with no quote-awareness (see
// dispatchSlash), so a value like "Authorization=Bearer sk-..." would
// otherwise be split into two tokens and silently truncated — worth
// handling here since a Bearer header is the primary use case for
// --header. Every token starting with "--" is treated as the next flag, so
// consumption stops there; tokens in between are rejoined with a single
// space, which loses any original multi-space runs but preserves the
// value's words.
func consumeFlagValue(args []string, i int) (value string, next int) {
	j := i + 1
	var parts []string
	for j < len(args) && !strings.HasPrefix(args[j], "--") {
		parts = append(parts, args[j])
		j++
	}
	return strings.Join(parts, " "), j
}

// mcpAddRecognizedFlags is /mcp add's own flag vocabulary — used by
// stragglerFlagsIn to catch the ordering footgun where one of these,
// typed after --command, silently becomes a literal argument to that
// command instead of being parsed as a flag.
var mcpAddRecognizedFlags = []string{"--transport", "--url", "--header", "--env", "--disabled"}

// stragglerFlagsIn scans a command's captured argument tokens for
// anything that looks like one of /mcp add's own flags. It can't tell
// whether the user meant "pass --disabled to the command" or "I put
// --disabled in the wrong place" — so it returns candidates for a warning
// rather than blocking the add.
func stragglerFlagsIn(cmdTokens []string) []string {
	var found []string
	for _, tok := range cmdTokens {
		for _, flag := range mcpAddRecognizedFlags {
			if tok == flag {
				found = append(found, tok)
				break
			}
		}
	}
	return found
}

// parseKeyValueFlags parses repeated "KEY=VALUE" flag values into a map.
// Returns nil (not an empty map) when kvs is empty, so callers can leave
// config.MCPServer.Headers/Env unset rather than an empty non-nil map.
func parseKeyValueFlags(kvs []string) (map[string]string, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("expected KEY=VALUE, got %q", kv)
		}
		out[k] = v
	}
	return out, nil
}

func registerMCPTools(registry *agent.Registry, cfg config.Config, server string, client mcp.Client, tools []mcp.ToolDescriptor) {
	if registry == nil {
		return
	}
	approvalMode := cfg.MCP.ApprovalMode
	if approvalMode == "" {
		approvalMode = config.MCPApprovalAsk
	}
	var timeout time.Duration
	if seconds := cfg.MCPCallTimeout(); seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	for _, s := range cfg.MCPServers {
		if s.Name == server && (len(s.Include) > 0 || len(s.Exclude) > 0) {
			tools, _ = mcp.FilterTools(tools, s.Include, s.Exclude)
			break
		}
	}
	for _, td := range tools {
		registry.Register(agent.NewMCPTool(
			server,
			td,
			client,
			approvalMode,
			cfg.MCP.TrustsAnnotations(server),
			timeout,
		))
	}
}

// renderMCPStatus prints a single block summarizing every configured
// MCP server: name, status (including a disabled badge), tool count
// (with an include/exclude hidden-count note when the server filters its
// catalog), last error.
func renderMCPStatus(m *Model, mgr *mcp.Manager) {
	names := mgr.Names()
	statuses := mgr.Statuses()
	filters := make(map[string]config.MCPServer, len(m.fileCfg.MCPServers))
	for _, s := range m.fileCfg.MCPServers {
		filters[s.Name] = s
	}
	m.appendLine(styleMCPHeader.Render(fmt.Sprintf("MCP servers (%d configured)", len(names))))
	for i, st := range statuses {
		name := names[i]
		var statusBadge, detail string
		switch {
		case st.Disabled:
			statusBadge = styleMCPMeta.Render("disabled")
		case st.Name == "":
			statusBadge = styleMCPMeta.Render("starting...")
		case st.Err != nil:
			statusBadge = styleMCPFail.Render("failed")
			detail = styleMCPFail.Render(mcp.Redact(st.Err.Error()))
		default:
			statusBadge = styleMCPOK.Render("running")
			toolsText := fmt.Sprintf("%d tools", st.ToolCount)
			if s := filters[name]; len(s.Include) > 0 || len(s.Exclude) > 0 {
				if client := mgr.Client(name); client != nil {
					if all, err := client.ListTools(context.Background()); err == nil {
						kept, hidden := mcp.FilterTools(all, s.Include, s.Exclude)
						if hidden > 0 {
							toolsText = fmt.Sprintf("%d tools (%d hidden by filter)", len(kept), hidden)
						}
					}
				}
			}
			detail = styleMCPMeta.Render(toolsText)
		}
		m.appendLine(fmt.Sprintf("  %s  %s  %s",
			styleMCPName.Render(name), statusBadge, detail))
	}
	m.appendLine(styleMCPMeta.Render("  type `/mcp logs <name>` to inspect a server's log output"))
}
