package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

const mcpServerTOML = `
[[mcp_servers]]
name    = "echo"
command = "echo"
args    = ["hello"]

[[mcp_servers]]
name     = "disabled-srv"
command  = "echo"
disabled = true
`

func TestMcpListPrintsServers(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, mcpServerTOML)
	out, _, err := runCobra(t, "mcp", "list")
	if err != nil {
		t.Fatalf("mcp list: %v", err)
	}
	if !strings.Contains(out, "echo") {
		t.Errorf("output should contain server name; got %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("output should contain args; got %q", out)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("output should mark disabled server; got %q", out)
	}
}

func TestMcpListEmpty(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	out, _, err := runCobra(t, "mcp", "list")
	if err != nil {
		t.Fatalf("mcp list: %v", err)
	}
	if !strings.Contains(out, "no MCP servers configured") {
		t.Errorf("empty list should explain; got %q", out)
	}
}

func TestMcpListJSON(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, mcpServerTOML)
	out, _, err := runCobra(t, "mcp", "list", "--json")
	if err != nil {
		t.Fatalf("mcp list --json: %v", err)
	}
	var got struct {
		MCPServers []config.MCPServer `json:"mcp_servers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("JSON decode: %v\n%s", err, out)
	}
	if len(got.MCPServers) != 2 {
		t.Errorf("expected 2 servers; got %d", len(got.MCPServers))
	}
}

func TestMcpAddAppendsAndPersists(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	out, _, err := runCobra(t, "mcp", "add", "podman", "npx", "-y", "podman-mcp-server@latest")
	if err != nil {
		t.Fatalf("mcp add: %v", err)
	}
	if !strings.Contains(out, "added") {
		t.Errorf("output should confirm add; got %q", out)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("expected 1 server; got %d", len(cfg.MCPServers))
	}
	s := cfg.MCPServers[0]
	if s.Name != "podman" {
		t.Errorf("name = %q, want podman", s.Name)
	}
	if s.Command != "npx" {
		t.Errorf("command = %q, want npx", s.Command)
	}
	if len(s.Args) != 2 || s.Args[0] != "-y" || s.Args[1] != "podman-mcp-server@latest" {
		t.Errorf("args = %v, want [-y podman-mcp-server@latest]", s.Args)
	}
}

func TestMcpAddDuplicateErrors(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, mcpServerTOML)
	_, _, err := runCobra(t, "mcp", "add", "echo", "echo")
	if err == nil {
		t.Fatal("adding duplicate should error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention duplicate; got %q", err)
	}
}

func TestMcpAddInvalidNameErrors(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, _, err := runCobra(t, "mcp", "add", "Bad-Name!", "echo")
	if err == nil {
		t.Fatal("invalid name should error")
	}
}

func TestMcpRemoveDeletesAndPersists(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, mcpServerTOML)
	out, _, err := runCobra(t, "mcp", "remove", "echo")
	if err != nil {
		t.Fatalf("mcp remove: %v", err)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("output should confirm removal; got %q", out)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, s := range cfg.MCPServers {
		if s.Name == "echo" {
			t.Error("echo server should have been removed from config")
		}
	}
}

func TestMcpRemoveUnknownErrors(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, _, err := runCobra(t, "mcp", "remove", "ghost")
	if err == nil {
		t.Fatal("removing unknown server should error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention the missing name; got %q", err)
	}
}
