package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

// Manager tests exercise lifecycle, ordering, and concurrency
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
	})
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

func TestManager_DisabledServerIsExcluded(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin},
		{Name: "off", Command: bin, Disabled: true},
	})
	if names := mgr.Names(); len(names) != 1 || names[0] != "echo" {
		t.Errorf("disabled server should be dropped at construction; got Names=%v", names)
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
	})
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

func TestManager_NoServersIsCleanNoop(t *testing.T) {
	mgr := mcp.NewManager(nil)
	if results := mgr.Start(context.Background()); len(results) != 0 {
		t.Errorf("empty manager Start should return 0 results; got %d", len(results))
	}
	if names := mgr.Names(); len(names) != 0 {
		t.Errorf("empty manager Names should be empty; got %v", names)
	}
	mgr.Stop(context.Background())
}
