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
	// maxResultBytes is threaded into every client's sessionOps at
	// construction time (see newClient) so the single, config-resolved
	// cap is authoritative — there is no second, hardcoded cap anywhere
	// downstream (see internal/agent/mcp_tool.go's Execute, which no
	// longer re-caps).
	maxResultBytes int
	// policy is the transport security policy applied to every http/sse
	// client this Manager constructs — see newClient and Policy.
	policy Policy
	// gen tracks a per-server generation. Restart, Enable, and Disable
	// each bump it under mu at entry; startAndPublish (used by Restart and
	// Enable) re-checks it before publishing a freshly built client, so
	// any interleaving of concurrent Restart/Enable/Disable calls on the
	// same server can't both win or race the client into an inconsistent
	// state: the latest generation publishes, any superseded call stops
	// the client it spawned (so its subprocess/connection doesn't leak)
	// and returns the winner's result. Keyed by name; missing == 0.
	gen map[string]uint64
}

// StartResult is the per-server outcome of Start / Restart. Err is nil
// for healthy starts; ToolCount is 0 when Err is non-nil. Warnings
// carries non-fatal config-time observations (e.g. an unresolved $VAR
// in the env block) so the caller can surface them — they don't
// prevent the server from starting. Disabled marks an entry that was
// never started at all (config.MCPServer.Disabled) — Err/ToolCount are
// always zero-value alongside it.
type StartResult struct {
	Name      string
	Err       error
	ToolCount int
	Warnings  []string
	Disabled  bool
}

// NewManager constructs a Manager from the parsed config block. Each
// non-disabled MCPServer becomes one Client (stdio, HTTP, or SSE — see
// newClient). A disabled entry stays visible — it appears in Names/
// Statuses/`/mcp` with a "disabled" status — but gets no client at all
// (no subprocess spawned, no connection dialed) until Enable is called.
// maxResultBytes (config.Config.MCPMaxResultBytes()) is threaded into
// every constructed client so the flattened-result cap is a single,
// config-aware value instead of a second hardcoded one downstream.
// policy bounds which URLs the manager's http/sse clients are willing to
// connect to — see Policy.
func NewManager(servers []config.MCPServer, maxResultBytes int, policy Policy) *Manager {
	m := &Manager{
		configs:        make(map[string]config.MCPServer, len(servers)),
		clients:        make(map[string]Client, len(servers)),
		status:         make(map[string]StartResult, len(servers)),
		gen:            make(map[string]uint64, len(servers)),
		maxResultBytes: maxResultBytes,
		policy:         policy,
	}
	for _, s := range servers {
		m.configs[s.Name] = s
		m.order = append(m.order, s.Name)
		if s.Disabled {
			m.status[s.Name] = StartResult{Name: s.Name, Disabled: true}
			continue
		}
		m.clients[s.Name] = m.newClient(s)
	}
	return m
}

// newClient constructs the right Client implementation for cfg.Transport
// — stdio (the default, "" or "stdio") spawns a subprocess; "http"/"sse"
// dial cfg.URL instead. Configs loaded from config.toml are already
// validated (config.Load calls Validate, which rejects any other
// Transport value); a config supplied directly to Add isn't guaranteed
// to have gone through that check, so an unrecognized Transport falls
// through to stdio rather than panicking — with an empty Command that
// fails cleanly at Start() ("command not found") instead of silently
// misbehaving.
func (m *Manager) newClient(cfg config.MCPServer) Client {
	switch cfg.Transport {
	case "http":
		c := NewHTTPClient(cfg.Name, cfg.URL, cfg.Headers, false, m.policy, cfg.TLSCAFile, oauthOptionsFor(cfg))
		c.ops.maxResultBytes = m.maxResultBytes
		return c
	case "sse":
		c := NewHTTPClient(cfg.Name, cfg.URL, cfg.Headers, true, m.policy, cfg.TLSCAFile, oauthOptionsFor(cfg))
		c.ops.maxResultBytes = m.maxResultBytes
		return c
	default:
		c := NewStdioClient(cfg.Name, cfg.Command, cfg.Args, cfg.Env)
		c.ops.maxResultBytes = m.maxResultBytes
		return c
	}
}

// oauthOptionsFor returns the *OAuthOptions NewHTTPClient needs when
// cfg.Auth is "oauth", or nil for every other auth mode ("", "none",
// "static-header" — those authenticate via cfg.Headers instead).
func oauthOptionsFor(cfg config.MCPServer) *OAuthOptions {
	if cfg.Auth != "oauth" {
		return nil
	}
	return &OAuthOptions{
		ClientID:     cfg.OAuthClientID,
		ClientSecret: cfg.OAuthClientSecret,
		Scopes:       cfg.OAuthScopes,
	}
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
		gen    uint64
	}
	pairs := make([]pair, 0, len(m.order))
	for _, name := range m.order {
		// A disabled entry has no client at all — skip it so startOne
		// never sees a nil Client, and leave its pre-seeded
		// StartResult{Disabled: true} in m.status untouched.
		if c, ok := m.clients[name]; ok {
			pairs = append(pairs, pair{name, c, m.gen[name]})
		}
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
	for i, r := range results {
		if m.gen[pairs[i].name] == pairs[i].gen && m.clients[pairs[i].name] == pairs[i].client {
			m.status[r.Name] = r
		}
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
	if cfg.Disabled {
		m.configs[cfg.Name] = cfg
		m.order = append(m.order, cfg.Name)
		m.status[cfg.Name] = StartResult{Name: cfg.Name, Disabled: true}
		result := m.status[cfg.Name]
		m.mu.Unlock()
		return result, nil
	}
	client := m.newClient(cfg)
	m.configs[cfg.Name] = cfg
	m.clients[cfg.Name] = client
	m.order = append(m.order, cfg.Name)
	m.gen[cfg.Name]++
	myGen := m.gen[cfg.Name]
	m.mu.Unlock()

	result := startOne(ctx, client)

	m.mu.Lock()
	if m.gen[cfg.Name] == myGen && m.clients[cfg.Name] == client {
		m.status[cfg.Name] = result
	}
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
	if cfg.Disabled {
		m.mu.Unlock()
		return StartResult{}, fmt.Errorf("mcp: server %q is disabled — use Enable to start it", name)
	}
	m.gen[name]++
	myGen := m.gen[name]
	m.mu.Unlock()

	if old != nil {
		_ = old.Stop(ctx)
	}
	return m.startAndPublish(ctx, name, cfg, myGen)
}

// startAndPublish builds a fresh client from cfg and starts it, then
// publishes it as name's live client and status — but only if no other
// Restart/Enable/Disable call has bumped the generation counter in the
// meantime (see the gen field's doc comment). If superseded, the freshly
// built client is stopped (so its subprocess/connection doesn't leak) and
// the winner's current status is returned instead. Shared by Restart and
// Enable — the only difference between them is where cfg comes from and
// whether a prior client needs stopping first (Restart's caller does that;
// Enable never has a prior client to stop).
func (m *Manager) startAndPublish(ctx context.Context, name string, cfg config.MCPServer, myGen uint64) (StartResult, error) {
	fresh := m.newClient(cfg)
	result := startOne(ctx, fresh)

	m.mu.Lock()
	if m.gen[name] != myGen {
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

// Remove stops the named client (if any — a disabled entry has none) and
// removes it from the manager's internal state. After Remove, the name no
// longer appears in Names(), Statuses(), or Client(). Callers must
// Deregister the server's tools from the agent registry separately.
// Returns an error if the name is unknown.
func (m *Manager) Remove(ctx context.Context, name string) error {
	m.mu.Lock()
	if _, ok := m.configs[name]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp: no server named %q", name)
	}
	client := m.clients[name]
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

// Disable stops the named client (if running) and marks the entry
// disabled, leaving it visible in Names()/Statuses() with a Disabled
// StartResult and no live client. The caller is responsible for
// persisting config.MCPServer.Disabled = true and for deregistering the
// server's tools from the agent registry. Returns an error if the name
// is unknown.
//
// Concurrency: bumps the generation counter before returning, same as
// Restart/Enable. That's what makes this safe against a concurrent
// Restart/Enable that's mid-flight when Disable runs: startAndPublish's
// post-startOne gen check will see it's been superseded and stop the
// client it just built instead of publishing it over the disable.
func (m *Manager) Disable(ctx context.Context, name string) error {
	m.mu.Lock()
	cfg, ok := m.configs[name]
	client := m.clients[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp: no server named %q", name)
	}
	cfg.Disabled = true
	m.configs[name] = cfg
	delete(m.clients, name)
	m.gen[name]++
	m.status[name] = StartResult{Name: name, Disabled: true}
	m.mu.Unlock()

	if client != nil {
		_ = client.Stop(ctx)
	}
	return nil
}

// Enable constructs a fresh client for a previously-disabled entry and
// starts it — the mirror of Disable. The caller is responsible for
// persisting config.MCPServer.Disabled = false and for registering the
// returned tools with the agent registry. Returns an error if the name is
// unknown; re-enabling an already-enabled server is a no-op success.
//
// Concurrency: shares startAndPublish with Restart, so a concurrent
// Enable/Restart/Disable of the same server can't race the client into an
// inconsistent published state — see startAndPublish's doc comment.
func (m *Manager) Enable(ctx context.Context, name string) (StartResult, error) {
	m.mu.Lock()
	cfg, ok := m.configs[name]
	if !ok {
		m.mu.Unlock()
		return StartResult{}, fmt.Errorf("mcp: no server named %q", name)
	}
	if _, running := m.clients[name]; running {
		status := m.status[name]
		m.mu.Unlock()
		return status, nil
	}
	cfg.Disabled = false
	m.configs[name] = cfg
	m.gen[name]++
	myGen := m.gen[name]
	m.mu.Unlock()

	return m.startAndPublish(ctx, name, cfg, myGen)
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

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(clients))
	for _, c := range clients {
		c := c
		go func() {
			defer wg.Done()
			_ = c.Stop(ctx)
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
