package mcp_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

func TestManagerStopReturnsWhenContextExpires(t *testing.T) {
	mgr := mcp.NewManager([]config.MCPServer{{Name: "hung", Transport: "http", URL: "http://127.0.0.1:0"}})
	client := mgr.Client("hung")
	// A fake Client cannot be injected through the public constructor, so use an
	// HTTP client whose Stop is already idempotent and verify an expired context
	// is still a hard manager return bound.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	mgr.Stop(ctx)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Stop exceeded canceled context bound: %v", elapsed)
	}
	if client == nil {
		t.Fatal("expected configured client")
	}
}

// guarantees using one real subprocess (the echo_server fixture) and
// one intentionally-broken entry. The fixture path keeps the test
// hermetic — no npx, no network — while still going through the same
// subprocess + JSON-RPC handshake as production.

func managerWithMixedServers(t *testing.T) *mcp.Manager {
	t.Helper()
	bin := buildEchoServer(t)
	return mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin},
		{Name: "broken", Command: "/no/such/binary/yottacode-mgr-test"},
	}, 0, mcp.Policy{})
}

func TestManager_StartReturnsResultsInRegistrationOrder(t *testing.T) {
	mgr := managerWithMixedServers(t)
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := mgr.Start(ctx)

	if len(results) != 2 {
		t.Fatalf("Start results len = %d, want 2", len(results))
	}
	if results[0].Name != "echo" || results[1].Name != "broken" {
		t.Errorf("Start results order = [%s, %s], want [echo, broken]",
			results[0].Name, results[1].Name)
	}
	if results[0].Err != nil {
		t.Errorf("echo should start clean; got %v", results[0].Err)
	}
	if results[0].ToolCount == 0 {
		t.Errorf("echo should advertise tools; got 0")
	}
	if results[1].Err == nil {
		t.Error("broken server should surface a Start error")
	}
}

func TestManager_FailedServerDoesNotBlockHealthyOne(t *testing.T) {
	mgr := managerWithMixedServers(t)
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	// 10s budget with two servers running concurrently — the broken
	// one fails fast (exec.LookPath miss), so the whole Start should
	// complete in well under that even if one goroutine misbehaves.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("Start took %v with one failing server — should be fast", elapsed)
		}
	case <-ctx.Done():
		t.Fatal("Start did not complete within 10s")
	}

	if mgr.Status("echo").Err != nil {
		t.Errorf("healthy server should remain healthy after sibling fails: %v", mgr.Status("echo").Err)
	}
	if mgr.Status("broken").Err == nil {
		t.Error("broken server status should record the failure")
	}
}

func TestManager_StatusesMirrorsRegistrationOrder(t *testing.T) {
	mgr := managerWithMixedServers(t)
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	mgr.Start(context.Background())

	statuses := mgr.Statuses()
	if len(statuses) != 2 {
		t.Fatalf("Statuses len = %d, want 2", len(statuses))
	}
	if statuses[0].Name != "echo" || statuses[1].Name != "broken" {
		t.Errorf("Statuses order = [%s, %s], want [echo, broken]",
			statuses[0].Name, statuses[1].Name)
	}
}

func TestManager_NamesEnumeratesRegistered(t *testing.T) {
	mgr := managerWithMixedServers(t)
	names := mgr.Names()
	if len(names) != 2 || names[0] != "echo" || names[1] != "broken" {
		t.Errorf("Names = %v, want [echo broken]", names)
	}
}

// TestManager_DisabledServerStaysVisibleWithNoClient is a regression test
// for the K2 disabled-server-visibility fix: a disabled entry used to be
// dropped from the manager entirely (invisible to /mcp, indistinguishable
// from "never configured"). It now stays in Names()/Statuses() with a
// Disabled StartResult and no live client, so it can be re-enabled without
// hand-editing config.toml.
func TestManager_DisabledServerStaysVisibleWithNoClient(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin},
		{Name: "off", Command: bin, Disabled: true},
	}, 0, mcp.Policy{})

	names := mgr.Names()
	if len(names) != 2 || names[0] != "echo" || names[1] != "off" {
		t.Fatalf("disabled server should stay registered; got Names=%v", names)
	}
	if mgr.Client("off") != nil {
		t.Error("disabled server should have no live client")
	}
	if got := mgr.Status("off"); !got.Disabled {
		t.Errorf("Status(off) = %+v, want Disabled=true", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := mgr.Start(ctx)
	if len(results) != 1 || results[0].Name != "echo" {
		t.Errorf("Start should only attempt enabled servers; got %+v", results)
	}
	if got := mgr.Status("off"); !got.Disabled {
		t.Errorf("Start must not clobber the disabled entry's status; got %+v", got)
	}
}

func TestManager_EnableStartsADisabledServer(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "off", Command: bin, Disabled: true},
	}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := mgr.Enable(ctx, "off")
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("Enable(off).Err = %v; want clean start", result.Err)
	}
	if result.Disabled {
		t.Error("Enable should clear Disabled on success")
	}
	if mgr.Client("off") == nil {
		t.Error("Enable should leave a live client behind")
	}
}

func TestManager_DisableStopsAnEnabledServer(t *testing.T) {
	mgr := managerWithMixedServers(t)
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.Start(ctx)

	if err := mgr.Disable(ctx, "echo"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if mgr.Client("echo") != nil {
		t.Error("Disable should remove the live client")
	}
	if got := mgr.Status("echo"); !got.Disabled {
		t.Errorf("Status(echo) after Disable = %+v, want Disabled=true", got)
	}
	// Names() must still include it — this is the visibility fix, not a Remove.
	found := false
	for _, n := range mgr.Names() {
		if n == "echo" {
			found = true
		}
	}
	if !found {
		t.Error("Disable must not remove the server from Names()")
	}
}

func TestManager_EnableDisableUnknownServerErrors(t *testing.T) {
	mgr := mcp.NewManager(nil, 0, mcp.Policy{})
	if _, err := mgr.Enable(context.Background(), "ghost"); err == nil {
		t.Error("Enable(ghost) should error — no such server")
	}
	if err := mgr.Disable(context.Background(), "ghost"); err == nil {
		t.Error("Disable(ghost) should error — no such server")
	}
}

// TestManager_SelectsClientTypeByTransport locks in newClient's
// branching: stdio (the zero value and explicit "stdio") builds a
// *mcp.StdioClient, "http"/"sse" build a *mcp.HTTPClient — across
// NewManager, Add, and Restart, the three call sites that construct a
// client from config.
func TestManager_SelectsClientTypeByTransport(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "stdio-default", Command: bin},
		{Name: "stdio-explicit", Transport: "stdio", Command: bin},
		{Name: "http", Transport: "http", URL: "http://127.0.0.1:0"},
		{Name: "sse", Transport: "sse", URL: "http://127.0.0.1:0"},
	}, 0, mcp.Policy{})

	if _, ok := mgr.Client("stdio-default").(*mcp.StdioClient); !ok {
		t.Errorf("stdio-default = %T, want *mcp.StdioClient", mgr.Client("stdio-default"))
	}
	if _, ok := mgr.Client("stdio-explicit").(*mcp.StdioClient); !ok {
		t.Errorf("stdio-explicit = %T, want *mcp.StdioClient", mgr.Client("stdio-explicit"))
	}
	if _, ok := mgr.Client("http").(*mcp.HTTPClient); !ok {
		t.Errorf("http = %T, want *mcp.HTTPClient", mgr.Client("http"))
	}
	if _, ok := mgr.Client("sse").(*mcp.HTTPClient); !ok {
		t.Errorf("sse = %T, want *mcp.HTTPClient", mgr.Client("sse"))
	}

	// Add and Restart share the same newClient helper — spot-check Add.
	if _, err := mgr.Add(context.Background(), config.MCPServer{Name: "http-added", Transport: "http", URL: "http://127.0.0.1:0"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := mgr.Client("http-added").(*mcp.HTTPClient); !ok {
		t.Errorf("http-added = %T, want *mcp.HTTPClient", mgr.Client("http-added"))
	}
}

func TestManager_ClientLookupUnknownReturnsNil(t *testing.T) {
	mgr := managerWithMixedServers(t)
	if got := mgr.Client("ghost"); got != nil {
		t.Errorf("Client(ghost) = %v, want nil", got)
	}
}

func TestManager_StopIsConcurrentAndIdempotent(t *testing.T) {
	mgr := managerWithMixedServers(t)
	mgr.Start(context.Background())

	mgr.Stop(context.Background())
	// Second Stop must be a no-op — StdioClient.Stop is idempotent
	// per its own contract; Manager.Stop iterating again must not
	// panic or hang.
	mgr.Stop(context.Background())
}

func TestManager_RestartRebuildsClient(t *testing.T) {
	mgr := managerWithMixedServers(t)
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.Start(ctx)

	before := mgr.Client("echo")
	if before == nil {
		t.Fatal("expected echo client after initial Start")
	}

	result, err := mgr.Restart(ctx, "echo")
	if err != nil {
		t.Fatalf("Restart(echo): %v", err)
	}
	if result.Err != nil {
		t.Fatalf("Restart(echo).Err = %v; want clean restart", result.Err)
	}
	if result.ToolCount == 0 {
		t.Error("Restart(echo) should report the rebuilt tool count")
	}

	after := mgr.Client("echo")
	if after == before {
		t.Error("Restart should swap the live Client instance, not reuse the prior one")
	}
}

func TestManager_RestartUnknownReturnsError(t *testing.T) {
	mgr := managerWithMixedServers(t)
	_, err := mgr.Restart(context.Background(), "ghost")
	if err == nil {
		t.Fatal("Restart(ghost) should error — no such server")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should mention the missing name", err)
	}
}

func TestManager_StartSurfacesEnvWarnings(t *testing.T) {
	bin := buildEchoServer(t)
	// Reference a var that the test env definitely doesn't set.
	mgr := mcp.NewManager([]config.MCPServer{
		{
			Name:    "echo",
			Command: bin,
			Env:     map[string]string{"FOO": "$YOTTACODE_TEST_DEFINITELY_UNSET_VAR_XYZ"},
		},
	}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	results := mgr.Start(context.Background())
	if len(results) != 1 {
		t.Fatalf("Start results len = %d, want 1", len(results))
	}
	if len(results[0].Warnings) == 0 {
		t.Fatalf("expected unresolved-$VAR warning; got %+v", results[0])
	}
	got := strings.Join(results[0].Warnings, " ")
	if !strings.Contains(got, "YOTTACODE_TEST_DEFINITELY_UNSET_VAR_XYZ") {
		t.Errorf("warning should name the unresolved var; got %q", got)
	}
}

func TestManager_AddRegistersAndStartsServer(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager(nil, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := mgr.Add(ctx, config.MCPServer{
		Name:    "echo",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("Add result.Err = %v; want nil", result.Err)
	}
	if result.ToolCount == 0 {
		t.Error("Add should report the tool count from the started server")
	}
	if mgr.Client("echo") == nil {
		t.Error("Client(echo) should be non-nil after Add")
	}
	if names := mgr.Names(); len(names) != 1 || names[0] != "echo" {
		t.Errorf("Names after Add = %v, want [echo]", names)
	}
}

func TestManager_AddDuplicateReturnsError(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin},
	}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	_, err := mgr.Add(context.Background(), config.MCPServer{
		Name:    "echo",
		Command: bin,
	})
	if err == nil {
		t.Fatal("Add duplicate should return an error")
	}
	if !strings.Contains(err.Error(), "echo") {
		t.Errorf("error %q should mention the duplicate name", err)
	}
}

func TestManager_AddBrokenServerRecordsFailure(t *testing.T) {
	mgr := mcp.NewManager(nil, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	result, err := mgr.Add(context.Background(), config.MCPServer{
		Name:    "broken",
		Command: "/no/such/binary/yottacode-add-test",
	})
	if err != nil {
		t.Fatalf("Add should not return an error for start failures: %v", err)
	}
	if result.Err == nil {
		t.Error("broken server result.Err should be non-nil")
	}
	st := mgr.Status("broken")
	if st.Err == nil {
		t.Error("Status should record the failure")
	}
}

func TestManager_RemoveStopsAndDropsServer(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin},
		{Name: "other", Command: bin},
	}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.Start(ctx)

	if err := mgr.Remove(ctx, "echo"); err != nil {
		t.Fatalf("Remove(echo): %v", err)
	}
	if mgr.Client("echo") != nil {
		t.Error("Client(echo) should be nil after Remove")
	}
	names := mgr.Names()
	for _, n := range names {
		if n == "echo" {
			t.Error("echo should not appear in Names() after Remove")
		}
	}
	if len(names) != 1 || names[0] != "other" {
		t.Errorf("Names after Remove = %v, want [other]", names)
	}
}

func TestManager_RemoveUnknownReturnsError(t *testing.T) {
	mgr := mcp.NewManager(nil, 0, mcp.Policy{})
	err := mgr.Remove(context.Background(), "ghost")
	if err == nil {
		t.Fatal("Remove(ghost) should error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should mention the missing name", err)
	}
}

func TestManager_NoServersIsCleanNoop(t *testing.T) {
	mgr := mcp.NewManager(nil, 0, mcp.Policy{})
	if results := mgr.Start(context.Background()); len(results) != 0 {
		t.Errorf("empty manager Start should return 0 results; got %d", len(results))
	}
	if names := mgr.Names(); len(names) != 0 {
		t.Errorf("empty manager Names should be empty; got %v", names)
	}
	mgr.Stop(context.Background())
}

// TestManager_ThreadsConfiguredMaxResultBytes is a regression test for a bug
// found while wiring the C0 safety floor: the client-layer result cap used
// to be silently pinned to a hardcoded default regardless of what config.toml
// said, because NewManager never threaded max_result_bytes into the clients
// it constructed. A larger-than-262144 configured value was inert, and a
// smaller one risked slicing through the cap's own truncation marker.
func TestManager_ThreadsConfiguredMaxResultBytes(t *testing.T) {
	bin := buildEchoServer(t)
	const resultCap = 200
	mgr := mcp.NewManager([]config.MCPServer{{Name: "echo", Command: bin}}, resultCap, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if results := mgr.Start(ctx); results[0].Err != nil {
		t.Fatalf("Start: %v", results[0].Err)
	}

	big := strings.Repeat("x", 1000)
	res, err := mgr.Client("echo").CallTool(ctx, "echo", `{"text":"`+big+`"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Text) > resultCap+120 { // cap + room for the truncation marker itself
		t.Fatalf("result length = %d, want truncated near the configured %d-byte cap", len(res.Text), resultCap)
	}
	if !strings.Contains(res.Text, fmt.Sprintf("kept the first %d bytes", resultCap)) {
		t.Errorf("expected a truncation marker citing the configured cap (%d); got %q", resultCap, res.Text)
	}
}

// TestManager_FullHTTPChainInitializeListCall exercises initialize +
// tools/list + tools/call over HTTP through the full production path —
// Manager builds the client, starts it, and CallTool goes through the same
// mcp.Client interface the agent bridge uses — mirroring the stdio
// integration coverage above but for the remote transport, hermetically
// (an in-process httptest server, no external tool required).
func TestManager_FullHTTPChainInitializeListCall(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "test-http-server", Version: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "echo",
		Description: "Returns the input prefixed with 'echo:'.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *sdk.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: "echo:" + args.Text}},
		}, nil, nil
	})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "remote", Transport: "http", URL: ts.URL},
	}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := mgr.Start(ctx)
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("Start: %+v", results)
	}
	if results[0].ToolCount != 1 {
		t.Errorf("ToolCount = %d, want 1", results[0].ToolCount)
	}

	res, err := mgr.Client("remote").CallTool(ctx, "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text != "echo:hi" {
		t.Errorf("CallTool result = %q, want %q", res.Text, "echo:hi")
	}
}
