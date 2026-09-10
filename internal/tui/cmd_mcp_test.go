package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

var (
	mcpEchoOnce sync.Once
	mcpEchoBin  string
	mcpEchoErr  error
	// realHome is captured at init before any test overrides HOME.
	realHome = os.Getenv("HOME")
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
		// Point HOME and GOPATH at the real home so module cache
		// files don't land inside t.TempDir(). Read-only module
		// cache entries cause TempDir cleanup to fail with
		// "permission denied" on CI.
		cmd.Env = append(os.Environ(),
			"HOME="+realHome,
			"GOPATH="+filepath.Join(realHome, "go"),
		)
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
	}, 0, mcp.Policy{})
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
	}, 0, mcp.Policy{})
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
	}, 0, mcp.Policy{})
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
	}, 0, mcp.Policy{})
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
	}, 0, mcp.Policy{})
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
	}, 0, mcp.Policy{})
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

func TestSlash_MCPAddWarnsOnStragglerFlagAfterCommand(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")

	// --disabled placed after --command's args is swallowed as a literal
	// argument to "echo" rather than parsed as /mcp add's own flag — this
	// must not happen silently.
	m, _ = typeAndEnter(t, m, "/mcp add off --command echo --disabled")
	content := m.transcript.String()
	if !strings.Contains(content, `"--disabled"`) || !strings.Contains(content, "looks like") {
		t.Errorf("expected a straggler-flag warning; got %q", content)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Disabled {
		t.Errorf("the server should NOT actually be disabled (that's the bug this warns about); got %+v", cfg.MCPServers)
	}
}

func TestSlash_MCPAddHTTPTransportPersists(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")

	m, _ = typeAndEnter(t, m, `/mcp add linear --transport http --url https://mcp.linear.app/mcp --header Authorization=Bearer secret-token`)
	content := m.transcript.String()
	if !strings.Contains(content, "linear") {
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
	if s.Transport != "http" || s.URL != "https://mcp.linear.app/mcp" {
		t.Errorf("server = %+v, want transport=http url=https://mcp.linear.app/mcp", s)
	}
	if s.Headers["Authorization"] != "Bearer secret-token" {
		t.Errorf("headers[Authorization] = %q", s.Headers["Authorization"])
	}
}

func TestSlash_MCPAddRejectsCommandAndURLTogether(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")

	// --url must precede --command: --command consumes every token after
	// it verbatim as the executable + args, by design.
	m, _ = typeAndEnter(t, m, `/mcp add bad --url https://example.com/mcp --command npx server`)
	content := m.transcript.String()
	if !strings.Contains(content, "cannot combine") {
		t.Errorf("combining --command with --url should be rejected; got %q", content)
	}
}

func TestSlash_MCPAddRejectsPlaintextRemoteURL(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")

	m, _ = typeAndEnter(t, m, `/mcp add insecure --transport http --url http://evil.example/mcp`)
	content := m.transcript.String()
	if !strings.Contains(content, "policy") {
		t.Errorf("plaintext remote URL should be rejected by policy; got %q", content)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Error("a policy-rejected add must not be persisted")
	}
}

func TestSlash_MCPAddDisabledSkipsStart(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	mgr := mcp.NewManager(nil, 0, mcp.Policy{})
	m.mcpManager = mgr

	bin := buildMCPEchoServer(t)
	m, _ = typeAndEnter(t, m, "/mcp add off --disabled --command "+bin)
	content := m.transcript.String()
	if !strings.Contains(content, "disabled") {
		t.Errorf("expected confirmation the server was added disabled; got %q", content)
	}
	if mgr.Client("off") != nil {
		t.Error("a --disabled add must not hot-start a live client")
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(cfg.MCPServers) != 1 || !cfg.MCPServers[0].Disabled {
		t.Errorf("expected one disabled server; got %+v", cfg.MCPServers)
	}
}

func TestSlash_MCPEnableDisableRoundTrip(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	bin := buildMCPEchoServer(t)

	mgr := mcp.NewManager([]config.MCPServer{{Name: "echo", Command: bin}}, 0, mcp.Policy{})
	results := mgr.Start(t.Context())
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("echo server should start cleanly; got %+v", results)
	}
	m.mcpManager = mgr
	m.handleMCPStartupDone(results)
	// writeConfig reads/writes m.fileCfg's backing file — seed it to match
	// the manager's view so /mcp disable's config lookup finds "echo".
	seedConfigTOML(t, `
[[mcp_servers]]
name    = "echo"
command = "`+bin+`"
`)

	reg := m.cfg.Registry
	if _, ok := reg.Get("mcp/echo/echo"); !ok {
		t.Fatal("expected mcp/echo/echo registered before disabling")
	}

	m, _ = typeAndEnter(t, m, "/mcp disable echo")
	content := m.transcript.String()
	if !strings.Contains(content, "disabled") {
		t.Errorf("expected disable confirmation; got %q", content)
	}
	if mgr.Client("echo") != nil {
		t.Error("Disable should remove the live client")
	}
	if _, ok := reg.Get("mcp/echo/echo"); ok {
		t.Error("Disable should deregister the server's tools")
	}

	m, _ = typeAndEnter(t, m, "/mcp enable echo")
	content = m.transcript.String()
	if !strings.Contains(content, "enabled") {
		t.Errorf("expected enable confirmation; got %q", content)
	}
	if mgr.Client("echo") == nil {
		t.Error("Enable should leave a live client behind")
	}
	if _, ok := reg.Get("mcp/echo/echo"); !ok {
		t.Error("Enable should re-register the server's tools")
	}
}

func TestSlash_MCPToolsListsCatalogWithFilterMarkers(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[mcp_servers]]
name    = "echo"
command = "`+buildMCPEchoServer(t)+`"
include = ["echo"]
`)
	cfg := loadConfigForCommand(m)
	mgr := mcp.NewManager(cfg.MCPServers, 0, mcp.Policy{})
	results := mgr.Start(t.Context())
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("echo server should start cleanly; got %+v", results)
	}
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp tools echo")
	content := m.transcript.String()
	if !strings.Contains(content, "echo") {
		t.Errorf("tools listing should include the echo tool; got %q", content)
	}
	if !strings.Contains(content, "hidden") {
		t.Errorf("non-included tools (slow, flaky, crash) should show as hidden; got %q", content)
	}
	if !strings.Contains(content, "shown") {
		t.Errorf("the included echo tool should show as shown; got %q", content)
	}
}

func TestMCPStartupDoneRegistersTools(t *testing.T) {
	m := newTestModel(t)

	bin := buildMCPEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin},
	}, 0, mcp.Policy{})
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
	}, 0, mcp.Policy{})
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

func TestSlash_MCPAuthUnknownServerErrors(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m.mcpManager = mcp.NewManager(nil, 0, mcp.Policy{})

	m, _ = typeAndEnter(t, m, "/mcp auth ghost")
	content := m.transcript.String()
	if !strings.Contains(content, "ghost") {
		t.Errorf("/mcp auth on an unknown server should mention the name; got %q", content)
	}
}

func TestSlash_MCPAuthRejectsNonOAuthServer(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[mcp_servers]]
name      = "linear"
transport = "http"
url       = "https://mcp.linear.app/mcp"
`)
	m.mcpManager = mcp.NewManager(nil, 0, mcp.Policy{})

	m, _ = typeAndEnter(t, m, "/mcp auth linear")
	content := m.transcript.String()
	if !strings.Contains(content, "oauth") {
		t.Errorf("/mcp auth on a non-oauth server should explain the mismatch; got %q", content)
	}
}

func TestSlash_MCPAuthStartsFlowAndSurfacesURL(t *testing.T) {
	// A 404-everything server: the SDK's protected-resource discovery
	// misses and falls back to treating the resource server's own root
	// as the authorization server (2025-03-26 spec fallback) — enough
	// to build a valid authorize URL without a real IdP, since this test
	// only exercises the dispatch → cmd → msg wiring up to "URL known",
	// not a full sign-in (that's covered end to end in
	// internal/mcp/oauth_test.go).
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	t.Setenv("HOME", t.TempDir())

	m := newTestModel(t)
	seedConfigTOML(t, fmt.Sprintf(`
[[mcp_servers]]
name            = "gmail"
transport       = "http"
url             = "%s/mcp/v1"
auth            = "oauth"
oauth_client_id = "test-client"
`, ts.URL))
	m.mcpManager = mcp.NewManager(nil, 0, mcp.Policy{})

	m, cmd := typeAndEnter(t, m, "/mcp auth gmail")
	if cmd == nil {
		t.Fatal("/mcp auth on a valid oauth server should return a tea.Cmd")
	}
	if m.mcpOAuthCancel == nil {
		t.Error("expected mcpOAuthCancel to be set while a sign-in is starting")
	}

	msg := cmd()
	urlMsg, ok := msg.(mcpOAuthURLMsg)
	if !ok {
		t.Fatalf("expected mcpOAuthURLMsg, got %T (%+v)", msg, msg)
	}
	if urlMsg.err != nil {
		t.Fatalf("unexpected error building the authorize URL: %v", urlMsg.err)
	}

	m, _ = applyMsg(m, urlMsg)
	content := m.transcript.String()
	if !strings.Contains(content, "opening browser") {
		t.Errorf("transcript should announce the sign-in; got %q", content)
	}
	if !strings.Contains(content, urlMsg.pending.AuthURL) {
		t.Errorf("transcript should surface the authorize URL as a fallback; got %q", content)
	}
	if m.mcpOAuthPending == nil || m.mcpOAuthPendingName != "gmail" {
		t.Errorf("model should track the pending login; pending=%v name=%q", m.mcpOAuthPending, m.mcpOAuthPendingName)
	}

	// A concurrent second attempt must be refused, not silently clobber
	// the first (it would otherwise leak the first attempt's loopback
	// listener reservation and confuse both flows' results).
	m2, _ := typeAndEnter(t, m, "/mcp auth gmail")
	if !strings.Contains(m2.transcript.String(), "already in progress") {
		t.Errorf("a second /mcp auth while one is pending should be refused; got %q", m2.transcript.String())
	}
}

func TestSlash_MCPLogoutDeletesToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := mcp.SaveToken("gmail", &oauth2.Token{AccessToken: "at-1"}); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	m := newTestModel(t)
	seedConfigTOML(t, "")
	m.mcpManager = mcp.NewManager(nil, 0, mcp.Policy{})

	m, _ = typeAndEnter(t, m, "/mcp logout gmail")
	content := m.transcript.String()
	if !strings.Contains(content, "logged out") {
		t.Errorf("/mcp logout should confirm; got %q", content)
	}
	if tok, err := mcp.LoadToken("gmail"); err != nil || tok != nil {
		t.Errorf("LoadToken after logout = (%v, %v), want (nil, nil)", tok, err)
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
