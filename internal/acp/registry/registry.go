package registry

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/syncutil"

	coderacp "github.com/coder/acp-go-sdk"
)

const (
	DefaultMaxConnections = 64
	DefaultQueueSize      = 128
)

var ErrConnectionLimit = errors.New("acp connection limit reached")
var ErrSlowClient = errors.New("acp client outbound queue is full")

type Registry struct {
	mu          syncutil.Mutex
	max         int
	connections map[string]*Connection
}

type Connection struct {
	ID      string
	TokenID string
	Scopes  []string
	Target  interface {
		SessionUpdate(context.Context, coderacp.SessionNotification) error
		RequestPermission(context.Context, coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error)
	}
	queue        chan coderacp.SessionNotification
	done         chan struct{}
	closeOnce    sync.Once
	lastMu       syncutil.Mutex
	lastActivity time.Time
}

func New(max int) *Registry {
	if max <= 0 {
		max = DefaultMaxConnections
	}
	return &Registry{max: max, connections: map[string]*Connection{}}
}

func (r *Registry) Add(id, tokenID string, scopes []string, target ConnectionTarget) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.connections) >= r.max {
		return nil, ErrConnectionLimit
	}
	c := &Connection{ID: id, TokenID: tokenID, Scopes: append([]string(nil), scopes...), Target: target, queue: make(chan coderacp.SessionNotification, DefaultQueueSize), done: make(chan struct{}), lastActivity: time.Now()}
	r.connections[id] = c
	go c.run()
	return c, nil
}

type ConnectionTarget interface {
	SessionUpdate(context.Context, coderacp.SessionNotification) error
	RequestPermission(context.Context, coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error)
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	c := r.connections[id]
	delete(r.connections, id)
	r.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
func (r *Registry) Len() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.connections) }

func (c *Connection) SessionUpdate(ctx context.Context, n coderacp.SessionNotification) error {
	c.lastMu.Lock()
	c.lastActivity = time.Now()
	c.lastMu.Unlock()
	select {
	case c.queue <- n:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return context.Canceled
	default:
		return ErrSlowClient
	}
}
func (c *Connection) RequestPermission(ctx context.Context, req coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	c.lastMu.Lock()
	c.lastActivity = time.Now()
	c.lastMu.Unlock()
	return c.Target.RequestPermission(ctx, req)
}
func (c *Connection) Close() { c.closeOnce.Do(func() { close(c.done) }) }
func (c *Connection) run() {
	for {
		select {
		case n := <-c.queue:
			_ = c.Target.SessionUpdate(context.Background(), n)
		case <-c.done:
			return
		}
	}
}
