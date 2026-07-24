package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultManagerMaxServers = 4
	defaultManagerIdle       = 10 * time.Minute
)

// ManagerStats is a snapshot of the reusable LSP server pool. It is intentionally
// small and printable so status tools can surface latency and reuse without
// depending on internal pool details.
type ManagerStats struct {
	OpenServers int
	MaxServers  int
	Starts      int
	Reuses      int
	Evictions   int
	LastStart   time.Duration
}

func DefaultManagerMaxServers() int { return defaultManagerMaxServers }

// Manager keeps a bounded pool of initialized LSP servers keyed by language,
// workspace root, and command. It avoids paying process startup on every tool
// call while still staying simple: all servers are closed at session teardown
// and idle/oldest entries are evicted before new starts.
type Manager struct {
	mu          sync.Mutex
	clients     map[string]*managerEntry
	maxServers  int
	idleTimeout time.Duration
	stats       ManagerStats
	newClient   func(context.Context, Language, string) (*Client, error)
	closeClient func(*Client) error
}

type managerEntry struct {
	client   *Client
	lang     Language
	root     string
	lastUsed time.Time
}

// NewManager constructs a bounded LSP server manager. Zero values select safe
// defaults so callers do not need to tune this for ordinary sessions.
func NewManager(maxServers int, idleTimeout time.Duration) *Manager {
	if maxServers <= 0 {
		maxServers = defaultManagerMaxServers
	}
	if idleTimeout <= 0 {
		idleTimeout = defaultManagerIdle
	}
	return &Manager{clients: map[string]*managerEntry{}, maxServers: maxServers, idleTimeout: idleTimeout, stats: ManagerStats{MaxServers: maxServers}, newClient: NewClient, closeClient: func(c *Client) error { return c.Close() }}
}

// Acquire returns a reusable client wrapper. Closing the wrapper releases it
// back to the pool; CloseAll tears down the underlying subprocesses.
func (m *Manager) Acquire(ctx context.Context, lang Language, root string) (*PooledClient, error) {
	if m == nil {
		c, err := NewClient(ctx, lang, root)
		if err != nil {
			return nil, err
		}
		return &PooledClient{Client: c, closeReal: true}, nil
	}
	root = cleanRoot(root)
	key := managerKey(lang, root)
	now := time.Now()
	m.mu.Lock()
	m.evictIdleLocked(now)
	if entry := m.clients[key]; entry != nil {
		entry.lastUsed = now
		m.stats.Reuses++
		m.mu.Unlock()
		return &PooledClient{Client: entry.client, manager: m, key: key}, nil
	}
	m.evictToCapacityLocked()
	m.mu.Unlock()

	start := time.Now()
	client, err := m.newClient(ctx, lang, root)
	elapsed := time.Since(start)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.clients[key]; existing != nil {
		m.stats.Reuses++
		m.closeClient(client)
		return &PooledClient{Client: existing.client, manager: m, key: key}, nil
	}
	m.clients[key] = &managerEntry{client: client, lang: lang, root: root, lastUsed: time.Now()}
	m.stats.Starts++
	m.stats.LastStart = elapsed
	m.stats.OpenServers = len(m.clients)
	return &PooledClient{Client: client, manager: m, key: key}, nil
}

// Stats returns a lock-protected snapshot for lsp_status output.
func (m *Manager) Stats() ManagerStats {
	if m == nil {
		return ManagerStats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.stats
	out.OpenServers = len(m.clients)
	out.MaxServers = m.maxServers
	return out
}

// CloseAll terminates every pooled server. It is safe to call multiple times.
func (m *Manager) CloseAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for key, entry := range m.clients {
		clients = append(clients, entry.client)
		delete(m.clients, key)
	}
	m.stats.OpenServers = 0
	m.mu.Unlock()
	for _, client := range clients {
		_ = m.closeClient(client)
	}
}

func (m *Manager) release(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.clients[key]; entry != nil {
		entry.lastUsed = time.Now()
	}
}

func (m *Manager) evictIdleLocked(now time.Time) {
	for key, entry := range m.clients {
		if now.Sub(entry.lastUsed) <= m.idleTimeout {
			continue
		}
		delete(m.clients, key)
		m.stats.Evictions++
		_ = m.closeClient(entry.client)
	}
	m.stats.OpenServers = len(m.clients)
}

func (m *Manager) evictToCapacityLocked() {
	for len(m.clients) >= m.maxServers {
		var oldestKey string
		var oldestTime time.Time
		for key, entry := range m.clients {
			if oldestKey == "" || entry.lastUsed.Before(oldestTime) {
				oldestKey = key
				oldestTime = entry.lastUsed
			}
		}
		if oldestKey == "" {
			return
		}
		entry := m.clients[oldestKey]
		delete(m.clients, oldestKey)
		m.stats.Evictions++
		_ = m.closeClient(entry.client)
	}
	m.stats.OpenServers = len(m.clients)
}

func managerKey(lang Language, root string) string {
	return lang.ID + "\x00" + cleanRoot(root) + "\x00" + strings.Join(lang.Command, "\x00")
}

func cleanRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	return filepath.Clean(abs)
}

// PooledClient wraps a Client so tool-level Close releases the server for reuse.
type PooledClient struct {
	*Client
	manager   *Manager
	key       string
	closeReal bool
}

func (c *PooledClient) Close() error {
	if c == nil || c.Client == nil {
		return nil
	}
	if c.closeReal || c.manager == nil {
		return c.Client.Close()
	}
	c.manager.release(c.key)
	return nil
}
