package lsp

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestManagerReusesClientByKey(t *testing.T) {
	mgr := NewManager(2, time.Hour)
	mgr.closeClient = func(*Client) error { return nil }
	defer mgr.CloseAll()
	starts := 0
	mgr.newClient = func(context.Context, Language, string) (*Client, error) {
		starts++
		return &Client{}, nil
	}
	lang := Language{ID: "go", Name: "Go", Command: []string{"gopls"}}
	first, err := mgr.Acquire(context.Background(), lang, t.TempDir())
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	_ = first.Close()
	second, err := mgr.Acquire(context.Background(), lang, first.keyRootForTest())
	if err != nil {
		t.Fatalf("Acquire second: %v", err)
	}
	_ = second.Close()
	if starts != 1 {
		t.Fatalf("starts = %d, want one reused client", starts)
	}
	stats := mgr.Stats()
	if stats.Starts != 1 || stats.Reuses != 1 || stats.OpenServers != 1 {
		t.Fatalf("stats = %+v, want one start, one reuse, one open", stats)
	}
}

func TestManagerEvictsAtCapacity(t *testing.T) {
	mgr := NewManager(1, time.Hour)
	mgr.closeClient = func(*Client) error { return nil }
	defer mgr.CloseAll()
	mgr.newClient = func(context.Context, Language, string) (*Client, error) { return &Client{}, nil }
	lang := Language{ID: "go", Name: "Go", Command: []string{"gopls"}}
	first, err := mgr.Acquire(context.Background(), lang, t.TempDir())
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	_ = first.Close()
	secondLang := Language{ID: "python", Name: "Python", Command: []string{"pyright-langserver"}}
	second, err := mgr.Acquire(context.Background(), secondLang, t.TempDir())
	if err != nil {
		t.Fatalf("Acquire second: %v", err)
	}
	_ = second.Close()
	stats := mgr.Stats()
	if stats.Evictions != 1 || stats.OpenServers != 1 {
		t.Fatalf("stats = %+v, want one eviction and one open server", stats)
	}
}

func TestManagerNotifyFileChangedEvictsDeadClientAndRetries(t *testing.T) {
	mgr := NewManager(2, time.Hour)
	defer mgr.CloseAll()
	starts := 0
	closed := 0
	mgr.closeClient = func(*Client) error {
		closed++
		return nil
	}
	mgr.newClient = func(context.Context, Language, string) (*Client, error) {
		starts++
		if starts == 1 {
			return &Client{capOK: true, stdin: errWriteCloser{err: errors.New("write |1: broken pipe")}, docs: map[string]openDocumentState{}}, nil
		}
		return &Client{capOK: true, stdin: nopWriteCloser{}, docs: map[string]openDocumentState{}}, nil
	}

	lang := Language{ID: "go", Name: "Go", Command: []string{"gopls"}}
	err := mgr.NotifyFileChanged(context.Background(), lang, t.TempDir(), "main.go", "package main\n")
	if err != nil {
		t.Fatalf("NotifyFileChanged should retry after dead client: %v", err)
	}
	if starts != 2 {
		t.Fatalf("starts = %d, want first dead client plus retry", starts)
	}
	if closed == 0 {
		t.Fatalf("dead client should be closed and evicted")
	}
	stats := mgr.Stats()
	if stats.Evictions == 0 || stats.OpenServers != 1 {
		t.Fatalf("stats = %+v, want one eviction and one live retry client", stats)
	}
}

func (c *PooledClient) keyRootForTest() string {
	if c == nil || c.manager == nil {
		return ""
	}
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	if entry := c.manager.clients[c.key]; entry != nil {
		return entry.root
	}
	return ""
}

type errWriteCloser struct{ err error }

func (w errWriteCloser) Write([]byte) (int, error) { return 0, w.err }
func (w errWriteCloser) Close() error              { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

var _ io.WriteCloser = errWriteCloser{}
var _ io.WriteCloser = nopWriteCloser{}
