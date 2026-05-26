package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

var (
	mcpEchoOnce sync.Once
	mcpEchoBin  string
	mcpEchoErr  error
)

func buildMCPEchoServer(t *testing.T) string {
	t.Helper()
	mcpEchoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tui-echo-server-bin")
		if err != nil {
			mcpEchoErr = err
			return
		}
		mcpEchoBin = filepath.Join(dir, "echo-server")
		cmd := exec.Command("go", "build", "-o", mcpEchoBin, "../../internal/mcp/testdata/echo_server")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			mcpEchoErr = err
			return
		}
	})
	if mcpEchoErr != nil {
		t.Fatalf("build mcp echo fixture: %v", mcpEchoErr)
	}
	return mcpEchoBin
}

func TestSlash_MCPWithoutManagerSaysNotConfigured(t *testing.T) {
	m := newTestModel(t)
	// mcpManager is nil in the default test model.
	m, _ = typeAndEnter(t, m, "/mcp list")
	content := m.transcript.String()
	if !strings.Contains(content, "no MCP servers configured") {
		t.Errorf("/mcp list without a manager should explain the situation; got %q", content)
	}
}

func TestSlash_MCPListsConfiguredServersWithStatus(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-mcp-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp list")
	content := m.transcript.String()
	if !strings.Contains(content, "fake") {
		t.Errorf("/mcp list output should mention the configured server: %q", content)
	}
	if !strings.Contains(content, "failed") {
		t.Errorf("/mcp list output should mark the broken server as failed: %q", content)
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

func TestSlash_MCPAddRequiresNameAndCommand(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/mcp add")
	content := m.transcript.String()
	if !strings.Contains(content, "usage:") {
		t.Errorf("/mcp add without args should show usage; got %q", content)
	}
}

func TestSlash_MCPAddMissingCommand(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/mcp add podman")
	content := m.transcript.String()
	if !strings.Contains(content, "usage:") {
		t.Errorf("/mcp add without --command should show usage; got %q", content)
	}
}

func TestSlash_MCPAddWritesCommandAndArgs(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")

	m, _ = typeAndEnter(t, m, "/mcp add podman --command npx -y podman-mcp-server@latest")
	content := m.transcript.String()
	if !strings.Contains(content, "added") {
		t.Fatalf("/mcp add should confirm; got %q", content)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server; got %d", len(cfg.MCPServers))
	}
	s := cfg.MCPServers[0]
	if s.Command != "npx" {
		t.Errorf("command = %q, want npx", s.Command)
	}
	if len(s.Args) != 2 || s.Args[0] != "-y" || s.Args[1] != "podman-mcp-server@latest" {
		t.Errorf("args = %v, want [-y podman-mcp-server@latest]", s.Args)
	}
}

func TestSlash_MCPAddHandlesQuotedCommand(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")

	// Simulates strings.Fields splitting a quoted --command value.
	// User typed: /mcp add podman --command "npx -y podman-mcp-server@latest"
	// strings.Fields produces: ["add", "podman", "--command", `"npx`, "-y", `podman-mcp-server@latest"`]
	m, _ = typeAndEnter(t, m, `/mcp add podman --command "npx -y podman-mcp-server@latest"`)
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server; got %d", len(cfg.MCPServers))
	}
	s := cfg.MCPServers[0]
	if s.Command != "npx" {
		t.Errorf("command = %q, want npx", s.Command)
	}
	if len(s.Args) != 2 || s.Args[0] != "-y" || s.Args[1] != "podman-mcp-server@latest" {
		t.Errorf("args = %v, want [-y podman-mcp-server@latest]", s.Args)
	}
}

func TestMCPStartupDoneRegistersTools(t *testing.T) {
	m := newTestModel(t)

	bin := buildMCPEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin},
	})
	m.mcpManager = mgr

	results := mgr.Start(t.Context())
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("echo server should start cleanly; got %+v", results)
	}

	m.handleMCPStartupDone(results)

	reg := m.cfg.Registry
	if _, ok := reg.Get("mcp/echo/echo"); !ok {
		t.Error("expected mcp/echo/echo tool to be registered after startup done")
	}
}

func TestMCPStartupDoneSkipsFailedServers(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "broken", Command: "/no/such/binary/yottacode-async-test"},
	})
	m.mcpManager = mgr

	results := mgr.Start(t.Context())
	m.handleMCPStartupDone(results)

	reg := m.cfg.Registry
	for name := range reg.Names() {
		if strings.HasPrefix(name, "mcp/broken/") {
			t.Errorf("failed server should not register tools; found %q", name)
		}
	}
}

func TestSlash_MCPRemoveRequiresName(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/mcp remove")
	content := m.transcript.String()
	if !strings.Contains(content, "usage:") {
		t.Errorf("/mcp remove without a name should show usage; got %q", content)
	}
}

func TestSlash_MCPRemoveUnknownServer(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/mcp remove ghost")
	content := m.transcript.String()
	if !strings.Contains(content, "ghost") && !strings.Contains(content, "no MCP server") {
		t.Errorf("/mcp remove ghost should say not found; got %q", content)
	}
}
