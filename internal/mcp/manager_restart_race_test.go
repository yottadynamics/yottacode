package mcp_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

// TestManager_ConcurrentRestartsConverge fires many restarts of the same
// server simultaneously. The per-server generation guard must ensure exactly
// one client survives in the manager and that client is healthy and callable —
// no leaked/stale client left registered, no torn state. Run with -race to
// confirm the generation accounting and map writes are coherent.
//
// Before the generation guard, overlapping restarts each published their own
// rebuilt client, so the last writer could clobber a still-starting one and
// the loser's subprocess leaked.
func TestManager_ConcurrentRestartsConverge(t *testing.T) {
	bin := buildEchoServer(t)
	mgr := mcp.NewManager([]config.MCPServer{{Name: "echo", Command: bin}})
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgr.Start(ctx)

	const restarts = 12
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
