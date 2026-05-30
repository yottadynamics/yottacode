package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/yottadynamics/yottacode/internal/config"
)

// Manager owns the lifecycle of every configured MCP client across a
// yottacode session. Built once from config, started concurrently at
// session init, restarted per-server via /mcp restart, stopped at
// shutdown. The agent loop never touches Manager — it sees the
// registered tools and goes through the same dispatch path as native
// tools.
//
// The Manager keeps the original config entries indexed by name so
// Restart can rebuild a fresh client without the caller re-loading
// config.toml.
type Manager struct {
	mu      sync.RWMutex
	configs map[string]config.MCPServer // original config keyed by name
	clients map[string]Client           // live clients keyed by name
	order   []string                    // stable iteration order (registration order)
	status  map[string]StartResult      // most recent Start outcome per name
	// gen tracks a per-server restart generation. Restart bumps it under
	// mu at entry and re-checks it before publishing the rebuilt client,
	// so two concurrent restarts of the same server can't both win: the
	// latest generation publishes, any superseded restart stops the
	// client it spawned (so its subprocess doesn't leak) and returns the
	// winner's result. Keyed by name; missing == 0.
	gen map[string]uint64
}

// StartResult is the per-server outcome of Start / Restart. Err is nil
// for healthy starts; ToolCount is 0 when Err is non-nil. Warnings
// carries non-fatal config-time observations (e.g. an unresolved $VAR
// in the env block) so the caller can surface them — they don't
// prevent the server from starting.
type StartResult struct {
	Name      string
	Err       error
	ToolCount int
	Warnings  []string
}

// NewManager constructs a Manager from the parsed config block. Each
// non-disabled MCPServer becomes one StdioClient. Disabled entries
// are dropped — they don't appear in /mcp at all in v1; the file is
// the only place that knows about them.
func NewManager(servers []config.MCPServer) *Manager {
	m := &Manager{
		configs: make(map[string]config.MCPServer, len(servers)),
		clients: make(map[string]Client, len(servers)),
		status:  make(map[string]StartResult, len(servers)),
		gen:     make(map[string]uint64, len(servers)),
	}
	for _, s := range servers {
		if s.Disabled {
			continue
		}
		m.configs[s.Name] = s
		m.clients[s.Name] = NewStdioClient(s.Name, s.Command, s.Args, s.Env)
		m.order = append(m.order, s.Name)
	}
	return m
}

// Start spawns every configured client concurrently. Each client gets
// its own goroutine so slow / failing servers don't block the rest.
// Returns one StartResult per client in registration order. Always
// safe to ignore the error result on individual clients — the manager
// retains the failure state for /mcp inspection via Statuses.
func (m *Manager) Start(ctx context.Context) []StartResult {
	m.mu.RLock()
	type pair struct {
		name   string
		client Client
	}
	pairs := make([]pair, 0, len(m.order))
	for _, name := range m.order {
		pairs = append(pairs, pair{name, m.clients[name]})
	}
	m.mu.RUnlock()

	results := make([]StartResult, len(pairs))
	var wg sync.WaitGroup
	wg.Add(len(pairs))
	for i, p := range pairs {
		i, p := i, p
		go func() {
			defer wg.Done()
			results[i] = startOne(ctx, p.client)
		}()
	}
	wg.Wait()

	m.mu.Lock()
	for _, r := range results {
		m.status[r.Name] = r
	}
	m.mu.Unlock()
	return results
}

// startOne runs the per-client Start + ListTools dance and collects
// any warnings (e.g. unresolved $VAR substitutions surfaced via
// StdioClient.Warnings). Shared by Start and Restart.
func startOne(ctx context.Context, c Client) StartResult {
	r := StartResult{Name: c.Name()}
	if w, ok := c.(interface{ Warnings() []string }); ok {
		r.Warnings = append(r.Warnings, w.Warnings()...)
	}
	if err := c.Start(ctx); err != nil {
		r.Err = err
		return r
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		r.Err = err
		return r
	}
	r.ToolCount = len(tools)
	return r
}

// Clients returns the (read-only) set of clients managed here in
// registration order. Healthy or not — callers that only want
// successful ones should filter on Status(name).Err == nil.
func (m *Manager) Clients() []Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Client, 0, len(m.order))
	for _, name := range m.order {
		if c, ok := m.clients[name]; ok {
			out = append(out, c)
		}
	}
	return out
}

// Client looks up a client by configured name. Returns nil if no
// such server is configured.
func (m *Manager) Client(name string) Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[name]
}

// Status returns the most recent Start outcome for a client by name.
// Zero-value StartResult.Name is empty when the name is unknown.
func (m *Manager) Status(name string) StartResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status[name]
}

// Statuses returns every recorded StartResult, ordered by registration
// order. Used by /mcp to render the full picture.
func (m *Manager) Statuses() []StartResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StartResult, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.status[name])
	}
	return out
}

// Names returns the registered server names in registration order.
// Useful for callers (e.g. /mcp restart completion) that need to
// enumerate without retrieving every Client.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// Add registers a new MCP server at runtime and starts it. Used by
// /mcp add to hot-start the server without requiring a TUI restart.
// Returns the StartResult so the caller can register tools. Returns an
// error if a server with the same name already exists.
func (m *Manager) Add(ctx context.Context, cfg config.MCPServer) (StartResult, error) {
	m.mu.Lock()
	if _, exists := m.configs[cfg.Name]; exists {
		m.mu.Unlock()
		return StartResult{}, fmt.Errorf("mcp: server %q already exists", cfg.Name)
	}
	client := NewStdioClient(cfg.Name, cfg.Command, cfg.Args, cfg.Env)
	m.configs[cfg.Name] = cfg
	m.clients[cfg.Name] = client
	m.order = append(m.order, cfg.Name)
	m.mu.Unlock()

	result := startOne(ctx, client)

	m.mu.Lock()
	m.status[cfg.Name] = result
	m.mu.Unlock()
	return result, nil
}

// Restart stops the named client, rebuilds a fresh StdioClient from
// the stored config, and runs the same Start + ListTools dance as
// the initial session-start path. After Restart returns, callers
// must Deregister the prior generation's tools from the agent
// Registry and Register the new generation — the manager doesn't
// touch the agent registry directly (the tui package owns that
// wiring; see cmd_mcp.go).
//
// Returns the new StartResult. The unknown-server case surfaces as
// an error rather than a zero StartResult so callers can distinguish
// "restart failed" from "no such server."
//
// Concurrency: a per-server generation counter makes overlapping
// restarts of the same server safe. Each call claims the next
// generation under the lock; only the latest generation publishes its
// rebuilt client. A restart that gets superseded mid-flight stops the
// client it spawned (so the subprocess doesn't leak) and returns the
// winner's StartResult instead of clobbering it.
func (m *Manager) Restart(ctx context.Context, name string) (StartResult, error) {
	m.mu.Lock()
	cfg, ok := m.configs[name]
	old := m.clients[name]
	if !ok {
		m.mu.Unlock()
		return StartResult{}, fmt.Errorf("mcp: no server named %q", name)
	}
	m.gen[name]++
	myGen := m.gen[name]
	m.mu.Unlock()

	if old != nil {
		_ = old.Stop(ctx)
	}

	fresh := NewStdioClient(cfg.Name, cfg.Command, cfg.Args, cfg.Env)
	result := startOne(ctx, fresh)

	m.mu.Lock()
	if m.gen[name] != myGen {
		// A newer restart superseded us while we were (re)starting.
		// Drop the client we just built so its subprocess doesn't leak,
		// and let the winner's published client/status stand.
		winner := m.status[name]
		m.mu.Unlock()
		_ = fresh.Stop(ctx)
		return winner, nil
	}
	m.clients[name] = fresh
	m.status[name] = result
	m.mu.Unlock()
	return result, nil
}

// Remove stops the named client and removes it from the manager's
// internal state. After Remove, the name no longer appears in Names(),
// Statuses(), or Client(). Callers must Deregister the server's tools
// from the agent registry separately. Returns an error if the name is
// unknown.
func (m *Manager) Remove(ctx context.Context, name string) error {
	m.mu.Lock()
	client, ok := m.clients[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp: no server named %q", name)
	}
	delete(m.configs, name)
	delete(m.clients, name)
	delete(m.status, name)
	filtered := m.order[:0]
	for _, n := range m.order {
		if n != name {
			filtered = append(filtered, n)
		}
	}
	m.order = filtered
	m.mu.Unlock()

	if client != nil {
		_ = client.Stop(ctx)
	}
	return nil
}

// Stop shuts every client down concurrently. Each Stop call is bounded
// by the SDK's TerminateGracePeriod (closing stdin, then SIGTERM,
// then SIGKILL); the caller's ctx is forwarded so a cancelled
// shutdown surfaces quickly.
func (m *Manager) Stop(ctx context.Context) {
	m.mu.RLock()
	clients := make([]Client, 0, len(m.order))
	for _, name := range m.order {
		if c, ok := m.clients[name]; ok {
			clients = append(clients, c)
		}
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	wg.Add(len(clients))
	for _, c := range clients {
		c := c
		go func() {
			defer wg.Done()
			_ = c.Stop(ctx)
		}()
	}
	wg.Wait()
}
