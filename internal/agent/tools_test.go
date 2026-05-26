package agent

import (
	"fmt"
	"sync"
	"testing"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&ReadFileTool{Cwd: NewCwdRef("/x")})
	r.Register(&WriteFileTool{Cwd: NewCwdRef("/x")})

	if got, ok := r.Get("read_file"); !ok || got.Name() != "read_file" {
		t.Errorf("Get(read_file): ok=%v name=%q", ok, got)
	}
	if _, ok := r.Get("missing"); ok {
		t.Errorf("Get(missing) should be !ok")
	}
}

func TestRegistry_Deregister(t *testing.T) {
	r := NewRegistry()
	r.Register(&ReadFileTool{Cwd: NewCwdRef("/x")})
	r.Register(&WriteFileTool{Cwd: NewCwdRef("/x")})

	if ok := r.Deregister("read_file"); !ok {
		t.Errorf("Deregister(read_file) returned false; want true")
	}
	if _, ok := r.Get("read_file"); ok {
		t.Errorf("Get(read_file) after Deregister should miss")
	}
	if _, ok := r.Get("write_file"); !ok {
		t.Errorf("Deregister(read_file) should not affect write_file")
	}
	if ok := r.Deregister("read_file"); ok {
		t.Errorf("Deregister of an already-removed tool should return false")
	}
	if ok := r.Deregister("never_registered"); ok {
		t.Errorf("Deregister of an unknown tool should return false")
	}
}

// TestRegistry_ConcurrentAccessIsSafe stresses the read+write paths so
// `go test -race` flags any path that bypasses the mutex. The scenario
// mirrors production: the agent loop calls Get to dispatch tools while
// the TUI's /mcp restart path concurrently Deregisters and Registers
// the MCP tool generation.
func TestRegistry_ConcurrentAccessIsSafe(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 16; i++ {
		r.Register(&ReadFileTool{Cwd: NewCwdRef(fmt.Sprintf("/seed-%d", i))})
	}

	const goroutines = 32
	const iterations = 200
	var wg sync.WaitGroup

	// Hot loop of readers — Get, Names, Tools, AsAdapterTools — mirrors
	// the agent loop dispatching tool calls and the schema sender
	// preparing the next assistant message.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, _ = r.Get("read_file")
				_ = r.Names()
				_ = r.Tools()
				_ = r.AsAdapterTools()
			}
		}()
	}

	// Writers churn the registry — mirrors /mcp restart deregistering a
	// generation and registering the next.
	for i := 0; i < goroutines/4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := fmt.Sprintf("/scratch-%d", id)
			for j := 0; j < iterations; j++ {
				r.Register(&ReadFileTool{Cwd: NewCwdRef(name)})
				r.Deregister("read_file") // may or may not exist this round
				r.Register(&ReadFileTool{Cwd: NewCwdRef("/x")})
			}
		}(i)
	}

	wg.Wait()
}

func TestRegistry_AsAdapterTools(t *testing.T) {
	r := NewRegistry()
	r.Register(&ReadFileTool{Cwd: NewCwdRef("/x")})
	r.Register(&WriteFileTool{Cwd: NewCwdRef("/x")})
	r.Register(&ReadManyFilesTool{Cwd: NewCwdRef("/x")})

	tools := r.AsAdapterTools()
	if len(tools) != 3 {
		t.Fatalf("AsAdapterTools len = %d, want 3", len(tools))
	}
	seen := map[string]bool{}
	for _, to := range tools {
		seen[to.Name] = true
		if to.Schema == nil {
			t.Errorf("%s: missing schema", to.Name)
		}
		if to.Description == "" {
			t.Errorf("%s: missing description", to.Name)
		}
	}
	if !seen["read_file"] || !seen["write_file"] || !seen["read_many_files"] {
		t.Errorf("AsAdapterTools didn't surface all tools: %+v", seen)
	}
}
