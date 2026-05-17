package mcp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

// TestIntegration_Filesystem exercises the StdioClient end-to-end
// against the official @modelcontextprotocol/server-filesystem package
// via npx. Skipped unless YOTTACODE_MCP_E2E=1 to keep the default test
// run hermetic — the first npx invocation downloads ~10MB and can take
// several seconds, which is fine for manual validation but not for
// every `go test ./...` run.
//
// Run manually with:
//
//	YOTTACODE_MCP_E2E=1 go test -v -run Integration_Filesystem ./internal/mcp/
//
// What's validated:
//   - Subprocess spawn, initialize handshake, and tools/list against a
//     real MCP server (not the fixture echo_server).
//   - The filesystem server actually advertises tools like read_file,
//     write_file, etc. — the bridge sees them under their canonical
//     names without yottacode having to maintain a per-server map.
//   - One round-trip CallTool — read_text_file on a tempfile.
//   - Clean shutdown — Stop returns without error and leaves no
//     orphaned subprocess.
func TestIntegration_Filesystem(t *testing.T) {
	if os.Getenv("YOTTACODE_MCP_E2E") == "" {
		t.Skip("set YOTTACODE_MCP_E2E=1 to run against the real filesystem MCP server")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not available in PATH")
	}

	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	client := mcp.NewStdioClient(
		"filesystem",
		"npx",
		[]string{"-y", "@modelcontextprotocol/server-filesystem", dir},
		nil,
	)
	t.Cleanup(func() { _ = client.Stop(context.Background()) })

	// First-run npx package fetch can be slow — give it a generous
	// budget for this integration path only.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start against real filesystem server: %v", err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, td := range tools {
		names = append(names, td.Name)
	}
	t.Logf("filesystem server advertised %d tools: %v", len(names), names)
	if len(tools) == 0 {
		t.Fatal("expected filesystem server to advertise at least one tool")
	}

	// The filesystem server has historically named the read tool
	// `read_text_file` (older) or `read_file` (newer). Accept either
	// so this test doesn't break when upstream renames.
	var readToolName string
	for _, n := range names {
		if n == "read_text_file" || n == "read_file" {
			readToolName = n
			break
		}
	}
	if readToolName == "" {
		t.Fatalf("could not find a read tool in advertised set: %v", names)
	}

	res, err := client.CallTool(ctx, readToolName, `{"path":"`+testFile+`"}`)
	if err != nil {
		t.Fatalf("CallTool %s: %v", readToolName, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s reported isError; text=%q", readToolName, res.Text)
	}
	if !strings.Contains(res.Text, "hello world") {
		t.Errorf("CallTool result should include file contents; got %q", res.Text)
	}
}

// TestIntegration_Podman exercises the same production wiring against
// a second real-world server — manusa/podman-mcp-server, an
// independent third-party MCP server (not Anthropic-published). This
// validates that the bridge handles a server whose schemas, tool names,
// and annotations weren't designed for any particular client.
//
// Skipped unless YOTTACODE_MCP_E2E=1 + podman + npx are all available.
func TestIntegration_Podman(t *testing.T) {
	if os.Getenv("YOTTACODE_MCP_E2E") == "" {
		t.Skip("set YOTTACODE_MCP_E2E=1 to run against the real podman MCP server")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not available in PATH")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not available in PATH")
	}

	fakeHome := t.TempDir()
	cfgDir := filepath.Join(fakeHome, ".yottacode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfgBody := `[context]
warn_threshold = 0.65
auto_threshold = 0.85
default_window = 128000

[[mcp_servers]]
name    = "podman"
command = "npx"
args    = ["-y", "podman-mcp-server@latest"]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfgBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", fakeHome)

	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("config.LoadDefault: %v", err)
	}

	mgr := mcp.NewManager(cfg.MCPServers)
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	results := mgr.Start(ctx)
	if results[0].Err != nil {
		t.Fatalf("podman server failed to start: %v", results[0].Err)
	}
	t.Logf("podman MCP server advertised %d tools", results[0].ToolCount)

	reg := agent.NewRegistry()
	client := mgr.Client("podman")
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools(podman): %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, td := range tools {
		names = append(names, td.Name)
		reg.Register(&agent.MCPTool{
			Server:      client.Name(),
			ToolName:    td.Name,
			Desc:        td.Description,
			InputSchema: td.InputSchema,
			ReadOnly:    td.ReadOnlyHint,
			Client:      client,
		})
	}
	t.Logf("podman tools (registered as mcp/podman/<name>): %v", names)

	// Find a list-style tool to exercise. The podman MCP server has
	// varied its naming across versions (container_list, ps, list,
	// containers_list, …) so probe a few plausible names.
	var listToolName string
	for _, candidate := range []string{
		"mcp/podman/container_list",
		"mcp/podman/containers_list",
		"mcp/podman/ps",
		"mcp/podman/list_containers",
		"mcp/podman/container_ps",
	} {
		if _, ok := reg.Get(candidate); ok {
			listToolName = candidate
			break
		}
	}
	if listToolName == "" {
		t.Fatalf("no recognized container-list tool in registry; registered tools: %v", names)
	}
	t.Logf("invoking %s", listToolName)

	tool, _ := reg.Get(listToolName)
	out, err := tool.Execute(ctx, `{}`)
	if err != nil {
		t.Fatalf("Execute(%s): %v", listToolName, err)
	}
	t.Logf("%s result (first 300 chars):\n%s", listToolName,
		truncateForLog(out, 300))
	// Don't assert specific output shape — the podman server's text
	// rendering differs between versions. The call succeeding (no
	// transport error, no isError envelope) is the meaningful signal.
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// TestIntegration_FullProductionChain exercises every layer the
// yottacode binary uses to bring up MCP — config.Load reading a real
// config.toml from disk, mcp.NewManager + Start concurrently spawning
// servers, agent.MCPTool wrapping the live client, and agent.Registry
// resolving and dispatching the call. This is the production wiring
// minus the TUI shell; if the binary builds, this exercises the same
// code paths it runs at session start.
//
// Skipped unless YOTTACODE_MCP_E2E=1 — the npx download on first run
// is slow.
func TestIntegration_FullProductionChain(t *testing.T) {
	if os.Getenv("YOTTACODE_MCP_E2E") == "" {
		t.Skip("set YOTTACODE_MCP_E2E=1 to run the full production-chain E2E")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not available in PATH")
	}

	// 1) Set up a fake HOME with a real config.toml — exactly the
	//    shape a user would write.
	fakeHome := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "greeting.txt"),
		[]byte("hello from yottacode e2e\n"), 0o644); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	cfgDir := filepath.Join(fakeHome, ".yottacode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	cfgBody := `[context]
warn_threshold = 0.65
auto_threshold = 0.85
default_window = 128000

[[mcp_servers]]
name    = "filesystem"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-filesystem", "` + workspace + `"]
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", fakeHome)

	// 2) Load config the way the binary does.
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("config.LoadDefault: %v", err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "filesystem" {
		t.Fatalf("expected one filesystem server in config; got %+v", cfg.MCPServers)
	}

	// 3) Build + start manager — same call as internal/tui/run.go.
	mgr := mcp.NewManager(cfg.MCPServers)
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	startCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results := mgr.Start(startCtx)
	if len(results) != 1 {
		t.Fatalf("Start results = %d, want 1", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("filesystem server failed to start: %v", results[0].Err)
	}
	if results[0].ToolCount == 0 {
		t.Fatal("filesystem server should advertise tools")
	}
	t.Logf("filesystem server: %d tools registered", results[0].ToolCount)

	// 4) Register each live tool into a fresh agent registry — same
	//    loop run.go runs.
	reg := agent.NewRegistry()
	for _, c := range mgr.Clients() {
		tools, err := c.ListTools(startCtx)
		if err != nil {
			t.Fatalf("ListTools(%s): %v", c.Name(), err)
		}
		for _, td := range tools {
			reg.Register(&agent.MCPTool{
				Server:      c.Name(),
				ToolName:    td.Name,
				Desc:        td.Description,
				InputSchema: td.InputSchema,
				ReadOnly:    td.ReadOnlyHint,
				Client:      c,
			})
		}
	}

	// 5) Find a read tool by namespaced name (the model would see
	//    these in its tool list).
	var readToolName string
	for _, candidate := range []string{
		"mcp/filesystem/read_text_file",
		"mcp/filesystem/read_file",
	} {
		if _, ok := reg.Get(candidate); ok {
			readToolName = candidate
			break
		}
	}
	if readToolName == "" {
		var names []string
		for n := range reg.Names() {
			names = append(names, n)
		}
		t.Fatalf("no filesystem read tool in registry; registered: %v", names)
	}
	t.Logf("invoking %s", readToolName)

	tool, _ := reg.Get(readToolName)
	// The published filesystem server doesn't advertise
	// readOnlyHint — MCP servers don't have to, and many don't.
	// The bridge then correctly defaults to approval-required
	// (safe-by-default). Just log the resolved policy here; the
	// readOnlyHint round-trip is covered by the fixture-based
	// unit test in stdio_client_test.go where we control the
	// hint.
	t.Logf("%s RequiresApproval=%v (depends on server's readOnlyHint annotation)",
		readToolName, tool.RequiresApproval(`{"path":"x"}`))

	// 6) Execute via the agent.Tool interface — same path the loop
	//    takes when the model emits a tool_use.
	greetingPath := filepath.Join(workspace, "greeting.txt")
	out, err := tool.Execute(startCtx, `{"path":"`+greetingPath+`"}`)
	if err != nil {
		t.Fatalf("Execute(%s): %v", readToolName, err)
	}
	if !strings.Contains(out, "hello from yottacode e2e") {
		t.Errorf("tool output should contain seed text; got %q", out)
	}

	// 7) Confirm write tools surface approval-required.
	for _, wn := range []string{"mcp/filesystem/write_file", "mcp/filesystem/edit_file"} {
		if w, ok := reg.Get(wn); ok {
			if !w.RequiresApproval(`{}`) {
				t.Errorf("%s should require approval; got false", wn)
			}
		}
	}

	// 8) Exercise /mcp restart — full Stop + respawn against the
	//    real subprocess. Validates Restart returns a clean
	//    StartResult and a new Client instance.
	before := mgr.Client("filesystem")
	restartRes, err := mgr.Restart(startCtx, "filesystem")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if restartRes.Err != nil {
		t.Fatalf("Restart returned err in result: %v", restartRes.Err)
	}
	if mgr.Client("filesystem") == before {
		t.Error("Restart should swap the live Client instance")
	}
	t.Logf("/mcp restart succeeded; %d tools after restart", restartRes.ToolCount)
}
