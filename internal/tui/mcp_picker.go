package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

type mcpPickerMode int

const (
	mcpMenuMode mcpPickerMode = iota
	mcpListMode
	mcpAddMode
	mcpRemoveMode
	mcpLogsMode
)

type mcpMenuItem struct {
	Label    string
	Subtitle string
	Action   mcpPickerMode
}

type mcpServerEntry struct {
	Name      string
	Status    string
	ToolCount int
	Error     string
}

type mcpPickerState struct {
	mode       mcpPickerMode
	menuItems  []mcpMenuItem
	menuCursor int

	servers    []mcpServerEntry
	listCursor int

	addFields   []textinput.Model
	addLabels   []string
	addFocused  int
	addInputErr string
}

func snapshotMCPServers(mgr *mcp.Manager, cfg config.Config) []mcpServerEntry {
	if mgr == nil {
		var entries []mcpServerEntry
		for _, s := range cfg.MCPServers {
			status := "not started"
			if s.Disabled {
				status = "disabled"
			}
			entries = append(entries, mcpServerEntry{Name: s.Name, Status: status})
		}
		return entries
	}
	names := mgr.Names()
	statuses := mgr.Statuses()
	entries := make([]mcpServerEntry, len(names))
	for i, name := range names {
		e := mcpServerEntry{Name: name}
		if i < len(statuses) {
			st := statuses[i]
			if st.Name == "" {
				e.Status = "starting..."
			} else if st.Err != nil {
				e.Status = "failed"
				e.Error = st.Err.Error()
			} else {
				e.Status = "running"
				e.ToolCount = st.ToolCount
			}
		}
		entries[i] = e
	}
	return entries
}

func (m *Model) openMCPPicker(initialMode ...mcpPickerMode) {
	cfg := loadConfigForCommand(*m)
	servers := snapshotMCPServers(m.mcpManager, cfg)
	st := &mcpPickerState{
		mode: mcpMenuMode,
		menuItems: []mcpMenuItem{
			{Label: "List", Subtitle: "view configured servers and their status", Action: mcpListMode},
			{Label: "Add", Subtitle: "configure a new MCP server", Action: mcpAddMode},
			{Label: "Remove", Subtitle: "delete a configured server", Action: mcpRemoveMode},
			{Label: "Logs", Subtitle: "view a server's stderr output", Action: mcpLogsMode},
		},
		servers: servers,
	}
	if len(initialMode) > 0 && initialMode[0] != mcpMenuMode {
		st.mode = initialMode[0]
		if st.mode == mcpAddMode {
			populateMCPAddFields(st)
		}
	}
	m.mcpPicker = st
	m.mcpPickerOpen = true
}

func populateMCPAddFields(p *mcpPickerState) {
	name := textinput.New()
	name.Placeholder = "my-server"
	name.CharLimit = 64
	name.Focus()

	command := textinput.New()
	command.Placeholder = "npx"
	command.CharLimit = 256

	args := textinput.New()
	args.Placeholder = "-y @modelcontextprotocol/server-memory"
	args.CharLimit = 512

	p.addFields = []textinput.Model{name, command, args}
	p.addLabels = []string{"Name", "Command", "Args"}
	p.addFocused = 0
	p.addInputErr = ""
}

func (m Model) updateMCPPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.mcpPicker
	if p == nil {
		return m, nil
	}

	switch p.mode {
	case mcpMenuMode:
		return m.updateMCPMenu(msg)
	case mcpListMode:
		return m.updateMCPList(msg)
	case mcpAddMode:
		return m.updateMCPAdd(msg)
	case mcpRemoveMode:
		return m.updateMCPRemove(msg)
	case mcpLogsMode:
		return m.updateMCPLogs(msg)
	}
	return m, nil
}

func (m Model) updateMCPMenu(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.mcpPicker
	switch msg.String() {
	case "esc":
		m.mcpPickerOpen = false
		m.mcpPicker = nil
		return m, nil
	case "up", "k":
		if p.menuCursor > 0 {
			p.menuCursor--
		}
	case "down", "j":
		if p.menuCursor < len(p.menuItems)-1 {
			p.menuCursor++
		}
	case "enter":
		item := p.menuItems[p.menuCursor]
		p.mode = item.Action
		if item.Action == mcpAddMode {
			populateMCPAddFields(p)
		}
	}
	return m, nil
}

func (m Model) updateMCPList(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.mcpPicker
	switch msg.String() {
	case "esc":
		p.mode = mcpMenuMode
		return m, nil
	case "up", "k":
		if p.listCursor > 0 {
			p.listCursor--
		}
	case "down", "j":
		if p.listCursor < len(p.servers)-1 {
			p.listCursor++
		}
	case "enter":
		m.mcpPickerOpen = false
		m.mcpPicker = nil
	}
	return m, nil
}

func (m Model) updateMCPAdd(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.mcpPicker
	switch msg.String() {
	case "esc":
		p.mode = mcpMenuMode
		return m, nil
	case "tab":
		cycleMCPAddFocus(p, 1)
		return m, nil
	case "shift+tab":
		cycleMCPAddFocus(p, -1)
		return m, nil
	case "enter":
		return commitMCPAdd(&m)
	default:
		if p.addFocused < len(p.addFields) {
			var cmd tea.Cmd
			p.addFields[p.addFocused], cmd = p.addFields[p.addFocused].Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func cycleMCPAddFocus(p *mcpPickerState, delta int) {
	n := len(p.addFields)
	if n == 0 {
		return
	}
	p.addFields[p.addFocused].Blur()
	p.addFocused = (p.addFocused + delta + n) % n
	p.addFields[p.addFocused].Focus()
}

func (m Model) updateMCPLogs(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.mcpPicker
	switch msg.String() {
	case "esc":
		p.mode = mcpMenuMode
		return m, nil
	case "up", "k":
		if p.listCursor > 0 {
			p.listCursor--
		}
	case "down", "j":
		if p.listCursor < len(p.servers)-1 {
			p.listCursor++
		}
	case "enter":
		if len(p.servers) > 0 {
			name := p.servers[p.listCursor].Name
			m.mcpPickerOpen = false
			m.mcpPicker = nil
			mgr := m.mcpManager
			if mgr == nil {
				return m, nil
			}
			client := mgr.Client(name)
			if client == nil {
				m.appendLine(styleAuto.Render(SysMsg(SysWarning, "mcp", "no live client", name)))
				return m, nil
			}
			sc, ok := client.(*mcp.StdioClient)
			if !ok {
				m.appendLine(styleAuto.Render(SysMsg(SysState, "mcp", "no stderr", name, "non-stdio transport")))
				return m, nil
			}
			lines := sc.StderrTail()
			if len(lines) == 0 {
				m.appendLine(styleAuto.Render(SysMsg(SysState, "mcp", "no stderr yet", name)))
				return m, nil
			}
			m.appendLine(styleMCPHeader.Render(fmt.Sprintf("-- %s -- stderr (last %d lines) --", name, len(lines))))
			for _, ln := range lines {
				m.appendLine(ln)
			}
		}
	}
	return m, nil
}

func (m Model) updateMCPRemove(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.mcpPicker
	switch msg.String() {
	case "esc":
		p.mode = mcpMenuMode
		return m, nil
	case "up", "k":
		if p.listCursor > 0 {
			p.listCursor--
		}
	case "down", "j":
		if p.listCursor < len(p.servers)-1 {
			p.listCursor++
		}
	case "enter":
		if len(p.servers) > 0 {
			return commitMCPRemove(&m)
		}
	}
	return m, nil
}

func commitMCPAdd(m *Model) (Model, tea.Cmd) {
	p := m.mcpPicker
	name := strings.TrimSpace(p.addFields[0].Value())
	command := strings.TrimSpace(p.addFields[1].Value())
	argsStr := strings.TrimSpace(p.addFields[2].Value())

	if name == "" {
		p.addInputErr = "name is required"
		return *m, nil
	}
	if !config.MCPNameValid(name) {
		p.addInputErr = "name must be lowercase letters, digits, hyphens, underscores; start with a letter"
		return *m, nil
	}
	if command == "" {
		p.addInputErr = "command is required"
		return *m, nil
	}

	var args []string
	if argsStr != "" {
		args = strings.Fields(argsStr)
	}

	cfg := loadConfigForCommand(*m)
	for _, s := range cfg.MCPServers {
		if s.Name == name {
			p.addInputErr = fmt.Sprintf("server %q already exists", name)
			return *m, nil
		}
	}

	server := config.MCPServer{
		Name:    name,
		Command: command,
		Args:    args,
	}
	cfg.MCPServers = append(cfg.MCPServers, server)
	if err := writeConfig(cfg); err != nil {
		p.addInputErr = fmt.Sprintf("write config: %v", err)
		return *m, nil
	}

	m.mcpPickerOpen = false
	m.mcpPicker = nil

	argsHint := ""
	if len(args) > 0 {
		argsHint = fmt.Sprintf(" %s", strings.Join(args, " "))
	}
	m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "mcp", "added", name, strings.TrimSpace(command+argsHint))))

	mgr := m.mcpManager
	if mgr == nil {
		m.appendLine(styleAuto.Render(SysMsg(SysState, "mcp", "restart yottacode", "to start this server")))
		return *m, nil
	}

	// Start the server OFF the Update goroutine: mgr.Add + ListTools can
	// block up to the 30s start timeout on a slow/hung server, which would
	// otherwise freeze the entire TUI right after the user pressed Enter.
	// registry.Register is mutex-guarded, so registering from the command
	// goroutine can't race the agent goroutine's reads.
	m.appendLine(styleAuto.Render(SysMsg(SysProgress, "mcp", "starting server")))
	return *m, startMCPServerCmd(mgr, server, m.cfg.Registry)
}

// mcpServerStartedMsg carries the result lines from an off-thread MCP
// server start back to the Update goroutine for rendering.
type mcpServerStartedMsg struct {
	lines []string
}

// startMCPServerCmd starts an MCP server, lists its tools, and registers
// them — all in a tea.Cmd goroutine so a slow server doesn't freeze the
// UI. Returns the lines to append once it completes.
func startMCPServerCmd(mgr *mcp.Manager, server config.MCPServer, registry *agent.Registry) tea.Cmd {
	name := server.Name
	return func() tea.Msg {
		startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := mgr.Add(startCtx, server)
		if err != nil {
			_ = mgr.Remove(startCtx, name)
			result, err = mgr.Add(startCtx, server)
		}
		if err != nil {
			return mcpServerStartedMsg{lines: []string{SysMsg(SysFailure, "mcp", "failed to start", name, err.Error())}}
		}
		if result.Err != nil {
			return mcpServerStartedMsg{lines: []string{
				SysMsg(SysFailure, "mcp", "failed to start", name, result.Err.Error()),
				SysMsg(SysState, "mcp", "check logs", "/mcp logs "+name),
			}}
		}
		fresh := mgr.Client(name)
		tools, err := fresh.ListTools(startCtx)
		if err != nil {
			return mcpServerStartedMsg{lines: []string{SysMsg(SysFailure, "mcp", "list tools", name, err.Error())}}
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
		return mcpServerStartedMsg{lines: []string{SysMsg(SysSuccess, "mcp", "started", name, fmt.Sprintf("%d tools registered", len(tools)))}}
	}
}

func commitMCPRemove(m *Model) (Model, tea.Cmd) {
	p := m.mcpPicker
	if p.listCursor >= len(p.servers) {
		return *m, nil
	}
	name := p.servers[p.listCursor].Name

	cfg := loadConfigForCommand(*m)
	found := -1
	for i, s := range cfg.MCPServers {
		if s.Name == name {
			found = i
			break
		}
	}
	if found < 0 {
		m.appendLine(styleError.Render(fmt.Sprintf("no MCP server named %q in config.toml", name)))
		m.mcpPickerOpen = false
		m.mcpPicker = nil
		return *m, nil
	}

	cfg.MCPServers = append(cfg.MCPServers[:found], cfg.MCPServers[found+1:]...)
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("failed to write config: %v", err)))
		m.mcpPickerOpen = false
		m.mcpPicker = nil
		return *m, nil
	}

	m.mcpPickerOpen = false
	m.mcpPicker = nil
	m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "mcp", "removed", name)))
	stopMCPServer(m, name)
	return *m, nil
}

// --- renderers ---------------------------------------------------------------

func renderMCPPicker(p *mcpPickerState, width int) string {
	var body string
	switch p.mode {
	case mcpMenuMode:
		body = renderMCPMenu(p)
	case mcpListMode:
		body = renderMCPServerList(p, "MCP servers", false)
	case mcpAddMode:
		body = renderMCPAddForm(p, width)
	case mcpRemoveMode:
		body = renderMCPServerList(p, "Remove MCP server", true)
	case mcpLogsMode:
		body = renderMCPServerList(p, "View server logs", false)
	default:
		body = styleEmpty.Render("(unknown picker state)")
	}
	footerText := "enter confirm · esc back · up/down navigate"
	if p.mode == mcpAddMode {
		footerText = "enter save · tab/shift+tab switch fields · esc back"
	}
	footer := styleFooter.Render(footerText)
	_ = width
	return body + "\n\n" + footer
}

func renderMCPMenu(p *mcpPickerState) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("MCP servers",
		"Manage Model Context Protocol server lifecycle."))
	b.WriteString("\n")
	for i, item := range p.menuItems {
		b.WriteString(renderMenuItem(menuItemOpts{
			Number:     i + 1,
			Label:      item.Label,
			LabelWidth: 10,
			Desc:       item.Subtitle,
			Cursor:     i == p.menuCursor,
		}))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderMCPServerList(p *mcpPickerState, title string, isRemove bool) string {
	var b strings.Builder
	desc := "View server status and tool counts. Press Enter to view logs."
	if isRemove {
		desc = "Select a server to remove from config.toml."
	}
	b.WriteString(renderMenuHeader(title, desc))
	b.WriteString("\n")
	if len(p.servers) == 0 {
		b.WriteString(styleEmpty.Render("  no MCP servers configured"))
		return b.String()
	}
	for i, srv := range p.servers {
		var detail string
		switch srv.Status {
		case "running":
			detail = styleMCPOK.Render("running") + "  " + styleMCPMeta.Render(fmt.Sprintf("%d tools", srv.ToolCount))
		case "failed":
			detail = styleMCPFail.Render("failed") + "  " + styleMCPFail.Render(srv.Error)
		case "starting...":
			detail = styleMCPMeta.Render("starting...")
		case "disabled":
			detail = styleMCPMeta.Render("disabled")
		default:
			detail = styleMCPMeta.Render(srv.Status)
		}
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:      srv.Name,
			LabelWidth: 20,
			Desc:       detail,
			Cursor:     i == p.listCursor,
		}))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderMCPAddForm(p *mcpPickerState, width int) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Add MCP server",
		"Fill in the fields and press Enter to save. The server starts immediately."))
	b.WriteString("\n")

	if p.addInputErr != "" {
		b.WriteString(styleMCPFail.Render("  error: " + p.addInputErr))
		b.WriteString("\n\n")
	}

	fieldWidth := width - 22
	if fieldWidth < 20 {
		fieldWidth = 20
	}
	for i, field := range p.addFields {
		field.SetWidth(fieldWidth)
		p.addFields[i] = field
		cursor := "  "
		if i == p.addFocused {
			cursor = "❯ "
		}
		label := fmt.Sprintf("%-10s", p.addLabels[i])
		b.WriteString(cursor + label + " " + field.View())
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
