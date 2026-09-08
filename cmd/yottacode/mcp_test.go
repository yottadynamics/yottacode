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

func TestMcpAddHTTPTransportPersists(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	out, _, err := runCobra(t, "mcp", "add", "linear",
		"--transport", "http",
		"--url", "https://mcp.linear.app/mcp",
		"--header", "Authorization=Bearer secret-token",
	)
	if err != nil {
		t.Fatalf("mcp add: %v", err)
	}
	if !strings.Contains(out, "linear") || !strings.Contains(out, "mcp.linear.app") {
		t.Errorf("output should confirm the added server and URL; got %q", out)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("expected 1 server; got %d", len(cfg.MCPServers))
	}
	s := cfg.MCPServers[0]
	if s.Transport != "http" {
		t.Errorf("transport = %q, want http", s.Transport)
	}
	if s.URL != "https://mcp.linear.app/mcp" {
		t.Errorf("url = %q, want https://mcp.linear.app/mcp", s.URL)
	}
	if s.Headers["Authorization"] != "Bearer secret-token" {
		t.Errorf("headers[Authorization] = %q, want Bearer secret-token", s.Headers["Authorization"])
	}
	if s.Command != "" {
		t.Errorf("command should be empty for an http server; got %q", s.Command)
	}
}

func TestMcpAddRejectsCommandAndURLTogether(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	// --url must precede the command token: once the command starts,
	// everything after it (including anything shaped like a flag) is
	// passed through to that command verbatim, by design — see
	// parseAddArgs. So the only ordering that can actually detect this
	// conflict is flags-then-command.
	_, _, err := runCobra(t, "mcp", "add", "bad", "--url", "https://example.com/mcp", "npx", "server")
	if err == nil {
		t.Fatal("combining a command with --url should error")
	}
	if !strings.Contains(err.Error(), "--url") {
		t.Errorf("error should mention --url; got %q", err)
	}
}

func TestMcpAddRejectsPlaintextRemoteURL(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, _, err := runCobra(t, "mcp", "add", "insecure", "--transport", "http", "--url", "http://evil.example/mcp")
	if err == nil {
		t.Fatal("a plaintext URL to a non-loopback host should fail closed at add time")
	}
	if !strings.Contains(err.Error(), "policy") {
		t.Errorf("error should mention the transport policy; got %q", err)
	}
	cfg, loadErr := config.LoadDefault()
	if loadErr != nil {
		t.Fatalf("reload: %v", loadErr)
	}
	if len(cfg.MCPServers) != 0 {
		t.Error("a policy-rejected add must not be persisted")
	}
}

func TestMcpAddAllowsLoopbackHTTP(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, _, err := runCobra(t, "mcp", "add", "local-dev", "--transport", "http", "--url", "http://127.0.0.1:8080/mcp")
	if err != nil {
		t.Fatalf("loopback http should be allowed under the default policy: %v", err)
	}
}

func TestMcpAddDisabledFlagPersists(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	// --disabled must precede the command token — see parseAddArgs.
	out, _, err := runCobra(t, "mcp", "add", "off", "--disabled", "echo")
	if err != nil {
		t.Fatalf("mcp add: %v", err)
	}
	if !strings.Contains(out, "off") {
		t.Errorf("output should confirm the added server; got %q", out)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.MCPServers) != 1 || !cfg.MCPServers[0].Disabled {
		t.Errorf("expected one disabled server; got %+v", cfg.MCPServers)
	}
}

func TestMcpAddWarnsOnStragglerFlagAfterCommand(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	// --disabled placed after the command is swallowed as a literal
	// argument to "echo" rather than being parsed as mcp add's own flag —
	// this must not happen silently.
	_, stderr, err := runCobra(t, "mcp", "add", "off", "echo", "--disabled")
	if err != nil {
		t.Fatalf("mcp add: %v", err)
	}
	if !strings.Contains(stderr, `"--disabled"`) || !strings.Contains(stderr, "looks like") {
		t.Errorf("expected a straggler-flag warning on stderr; got %q", stderr)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Disabled {
		t.Errorf("the server should NOT actually be disabled (that's the bug this warns about); got %+v", cfg.MCPServers)
	}
	if len(cfg.MCPServers[0].Args) != 1 || cfg.MCPServers[0].Args[0] != "--disabled" {
		t.Errorf("args should contain the literal --disabled token; got %v", cfg.MCPServers[0].Args)
	}
}

func TestMcpAddNoWarningWhenFlagsAreCorrectlyOrdered(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, stderr, err := runCobra(t, "mcp", "add", "off", "--disabled", "echo")
	if err != nil {
		t.Fatalf("mcp add: %v", err)
	}
	if strings.Contains(stderr, "looks like") {
		t.Errorf("correctly-ordered flags should not trigger the straggler warning; got %q", stderr)
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
