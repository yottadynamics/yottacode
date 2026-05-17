package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

func TestSlash_MCPWithoutManagerSaysNotConfigured(t *testing.T) {
	m := newTestModel(t)
	// mcpManager is nil in the default test model.
	m, _ = typeAndEnter(t, m, "/mcp")
	content := m.transcript.String()
	if !strings.Contains(content, "no MCP servers configured") {
		t.Errorf("/mcp without a manager should explain the situation; got %q", content)
	}
}

func TestSlash_MCPListsConfiguredServersWithStatus(t *testing.T) {
	m := newTestModel(t)
	// Spin up a manager with a single intentionally-broken server
	// (missing command). Start records the failure in Statuses;
	// rendering must show the server with `failed` status.
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-mcp-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp")
	content := m.transcript.String()
	if !strings.Contains(content, "fake") {
		t.Errorf("/mcp output should mention the configured server: %q", content)
	}
	if !strings.Contains(content, "failed") {
		t.Errorf("/mcp output should mark the broken server as failed: %q", content)
	}
}

func TestSlash_MCPLogsRequiresServerName(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-mcp-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp logs")
	content := m.transcript.String()
	if !strings.Contains(content, "usage:") {
		t.Errorf("/mcp logs without a name should show usage; got %q", content)
	}
}

func TestSlash_MCPLogsUnknownServer(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-mcp-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp logs ghost")
	content := m.transcript.String()
	if !strings.Contains(content, "ghost") || !strings.Contains(content, "no MCP server") {
		t.Errorf("/mcp logs ghost should say no such server; got %q", content)
	}
}

func TestSlash_MCPRestartWithoutNameShowsUsage(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-mcp-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp restart")
	content := m.transcript.String()
	if !strings.Contains(content, "usage:") {
		t.Errorf("/mcp restart without a name should show usage; got %q", content)
	}
}

func TestSlash_MCPRestartUnknownServer(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-mcp-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp restart ghost")
	content := m.transcript.String()
	if !strings.Contains(content, "ghost") || !strings.Contains(content, "no MCP server") {
		t.Errorf("/mcp restart ghost should report no such server; got %q", content)
	}
}

func TestSlash_MCPRestartFailedServerSurfacesError(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-mcp-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp restart fake")
	content := m.transcript.String()
	if !strings.Contains(content, "failed to restart") {
		t.Errorf("/mcp restart of a broken server should report the failure; got %q", content)
	}
}
