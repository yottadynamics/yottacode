package mcp_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

// TestManager_ConcurrentRestartsConverge fires overlapping restarts of the same
// server. The count stays deliberately modest so the test validates restart
// convergence without exhausting process limits in sandboxed unit-test runs. The
// per-server generation guard must ensure exactly one client survives in the
// manager and that client is healthy and callable — no leaked/stale client left
// registered, no torn state. Run with -race to confirm the generation accounting
// and map writes are coherent.
//
// Before the generation guard, overlapping restarts each published their own
// rebuilt client, so the last writer could clobber a still-starting one and
// the loser's subprocess leaked.
func TestManager_ConcurrentRestartsConverge(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{{Name: "echo", Command: bin}}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgr.Start(ctx)

	const restarts = 4
	var wg sync.WaitGroup
	wg.Add(restarts)
	for i := 0; i < restarts; i++ {
		go func() {
			defer wg.Done()
			if _, err := mgr.Restart(ctx, "echo"); err != nil {
				t.Errorf("Restart: %v", err)
			}
		}()
	}
	wg.Wait()

	// Exactly one healthy client must remain registered.
	got := mgr.Client("echo")
	if got == nil {
		t.Fatal("no client registered for echo after concurrent restarts")
	}
	if err := mgr.Status("echo").Err; err != nil {
		t.Fatalf("surviving client is unhealthy: %v", err)
	}

	// The survivor must be fully functional, not a half-initialized leftover.
	res, err := got.CallTool(ctx, "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("CallTool on survivor: %v", err)
	}
	if res.IsError {
		t.Fatalf("echo returned error result: %v", res.Text)
	}
}

// TestManager_ConcurrentEnableDisableConverges is the Enable/Disable analog
// of the restart race test above — fires interleaved Enable and Disable
// calls at the same server and checks the end state is internally
// consistent (a live client iff the status isn't Disabled) rather than
// torn (e.g. a live client left behind alongside a Disabled status, or a
// leaked client from a superseded call). Before Enable shared Restart's
// generation guard, Enable published its freshly built client
// unconditionally, so a Disable that ran while an Enable was still
// starting could be silently overwritten by the Enable finishing later.
func TestManager_ConcurrentEnableDisableConverges(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin, Disabled: true},
	}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const rounds = 4
	var wg sync.WaitGroup
	wg.Add(rounds * 2)
	for i := 0; i < rounds; i++ {
		go func() {
			defer wg.Done()
			_, _ = mgr.Enable(ctx, "echo")
		}()
		go func() {
			defer wg.Done()
			_ = mgr.Disable(ctx, "echo")
		}()
	}
	wg.Wait() // every call stops its own loser client synchronously before returning

	client := mgr.Client("echo")
	status := mgr.Status("echo")
	if (client != nil) == status.Disabled {
		t.Fatalf("inconsistent end state: client present=%v, status.Disabled=%v", client != nil, status.Disabled)
	}
	if client != nil {
		res, err := client.CallTool(ctx, "echo", `{"text":"hi"}`)
		if err != nil {
			t.Fatalf("CallTool on final enabled client: %v", err)
		}
		if res.IsError {
			t.Fatalf("echo returned error result: %v", res.Text)
		}
	}
}

// TestManager_RestartRefusesDisabledServer is a regression test: Restart
// used to silently re-enable a disabled server's client in memory without
// clearing config.MCPServer.Disabled, leaving the manager's live state and
// the persisted config disagreeing about whether the server was disabled.
func TestManager_RestartRefusesDisabledServer(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "echo", Command: bin, Disabled: true},
	}, 0, mcp.Policy{})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	_, err := mgr.Restart(context.Background(), "echo")
	if err == nil {
		t.Fatal("Restart on a disabled server should error, not silently start it")
	}
	if mgr.Client("echo") != nil {
		t.Error("a refused Restart must not leave a live client behind")
	}
}
