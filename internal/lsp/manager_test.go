package lsp

import (
	"context"
	"errors"
	"io"
	"sync"
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

func TestManagerEvictsIdleClients(t *testing.T) {
	mgr := NewManager(2, 5*time.Millisecond)
	closed := make(chan struct{}, 1)
	mgr.closeClient = func(*Client) error {
		closed <- struct{}{}
		return nil
	}
	defer mgr.CloseAll()
	mgr.newClient = func(context.Context, Language, string) (*Client, error) { return &Client{}, nil }
	lang := Language{ID: "go", Name: "Go", Command: []string{"gopls"}}
	client, err := mgr.Acquire(context.Background(), lang, t.TempDir())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = client.Close()
	time.Sleep(10 * time.Millisecond)
	other := Language{ID: "python", Name: "Python", Command: []string{"pyright-langserver"}}
	second, err := mgr.Acquire(context.Background(), other, t.TempDir())
	if err != nil {
		t.Fatalf("Acquire second: %v", err)
	}
	_ = second.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("expected idle client to be closed")
	}
	if stats := mgr.Stats(); stats.Evictions == 0 {
		t.Fatalf("expected idle eviction, got stats %+v", stats)
	}
}

func TestManagerConcurrentAcquireClosesDuplicateStart(t *testing.T) {
	mgr := NewManager(4, time.Hour)
	defer mgr.CloseAll()
	lang := Language{ID: "go", Name: "Go", Command: []string{"gopls"}}
	startBarrier := make(chan struct{})
	var starts int
	var mu sync.Mutex
	closed := 0
	mgr.closeClient = func(*Client) error {
		mu.Lock()
		closed++
		mu.Unlock()
		return nil
	}
	mgr.newClient = func(context.Context, Language, string) (*Client, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		<-startBarrier
		return &Client{}, nil
	}
	root := t.TempDir()
	var wg sync.WaitGroup
	results := make([]*PooledClient, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = mgr.Acquire(context.Background(), lang, root)
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	close(startBarrier)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}
	for _, client := range results {
		_ = client.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 {
		t.Fatalf("starts = %d, want 2 racing starts", starts)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want one duplicate closed", closed)
	}
	if stats := mgr.Stats(); stats.Reuses == 0 || stats.OpenServers != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
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
