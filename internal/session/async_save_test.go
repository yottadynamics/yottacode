package session

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

func TestSaveAsyncWritesLatestAndRunsAfter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := New("m", "/cwd")
	if err != nil {
		t.Fatal(err)
	}
	var mu syncutil.Mutex
	var indexed []int
	for i := 0; i < 20; i++ {
		s.Messages = append(s.Messages, adapter.Message{Role: adapter.RoleUser, Content: "hi"})
		s.SaveAsync(nil, func(snap *Session) error {
			mu.Lock()
			indexed = append(indexed, len(snap.Messages))
			mu.Unlock()
			return nil
		})
	}
	s.Flush()
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 20 {
		t.Fatalf("latest snapshot must win: got %d messages", len(loaded.Messages))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(indexed) == 0 || indexed[len(indexed)-1] != 20 {
		t.Fatalf("after hook should run for the last snapshot, got %v", indexed)
	}
}

// A synchronous Save must not be clobbered by an older queued snapshot, and
// mutating the live session while a background write runs must be race-free.
func TestSaveAsyncSnapshotIsolatedAndSaveOrdered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := New("m", "/cwd")
	s.ModelUsage = map[string]adapter.Usage{"a": {InputTokens: 1}}
	s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "one"}}
	s.SaveAsync(nil, nil)
	s.ModelUsage["b"] = adapter.Usage{InputTokens: 2}
	s.Messages = append(s.Messages, adapter.Message{Role: adapter.RoleAssistant, Content: "two"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, _ := Load(s.ID)
	if len(loaded.Messages) != 2 || len(loaded.ModelUsage) != 2 {
		t.Fatalf("sync Save must land last: %d msgs, %d usage", len(loaded.Messages), len(loaded.ModelUsage))
	}
}

func TestSaveAsyncAfterErrorSurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := New("m", "/cwd")
	s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}
	s.SaveAsync(nil, func(*Session) error { return errors.New("index boom") })
	s.Flush()
	if err := s.TakeAsyncErr(); err == nil || err.Error() != "index boom" {
		t.Fatalf("got %v", err)
	}
	if s.TakeAsyncErr() != nil {
		t.Fatal("error should clear after take")
	}
}

func TestSaveQueueReclaimedWhenIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := New("m", "/cwd")
	s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}
	s.SaveAsync(nil, nil)
	s.Flush()
	// The worker releases its reference just after going idle.
	deadline := time.Now().Add(2 * time.Second)
	for {
		queuesMu.Lock()
		n := len(queues)
		queuesMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle queue leaked: %d queues", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Mirrors the two in-place writers found by the audit: the per-turn system
// message rewrite and agent.annotateToolCallLocked.
func TestSnapshotIsolatedFromInPlaceMessageWrites(t *testing.T) {
	lat := int64(5)
	s := &Session{Messages: []adapter.Message{
		{Role: adapter.RoleSystem, Content: "old system", CacheHeadBytes: 3},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "a", Status: "running"}}},
	}}
	snap := s.Snapshot()

	s.Messages[0].Content = "new system"
	s.Messages[0].CacheHeadBytes = 9
	s.Messages[1].ToolCalls[0].Status = "ok"
	s.Messages[1].ToolCalls[0].LatencyMS = &lat

	if got := snap.Messages[0]; got.Content != "old system" || got.CacheHeadBytes != 3 {
		t.Fatalf("system message write leaked into snapshot: %+v", got)
	}
	if got := snap.Messages[1].ToolCalls[0]; got.Status != "running" || got.LatencyMS != nil {
		t.Fatalf("tool call annotation leaked into snapshot: %+v", got)
	}
}

// Hammer one path from many goroutines: reclamation of idle queues must never
// let two queues write the same file at once, and nothing may leak.
func TestSaveQueueNeverWritesConcurrentlyUnderChurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := New("m", "/cwd")
	s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	var active, peak atomic.Int32
	writeObserver = func(start bool) {
		if !start {
			active.Add(-1)
			return
		}
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
	}
	t.Cleanup(func() { writeObserver = nil })

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				switch (g + i) % 3 {
				case 0:
					s.SaveAsync(nil, nil)
				case 1:
					s.Flush()
				default:
					_ = s.Save()
				}
			}
		}(g)
	}
	wg.Wait()
	FlushAll()
	if p := peak.Load(); p != 1 {
		t.Fatalf("peak concurrent writers = %d, want 1", p)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		queuesMu.Lock()
		n := len(queues)
		queuesMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queues leaked after churn: %d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Tripwire for Snapshot's contract: every reference-typed field of Message and
// ToolCall must have been audited (see Snapshot's comment). A new slice, map or
// pointer field fails here until someone decides whether Snapshot must copy it.
func TestSnapshotCoversAllReferenceFields(t *testing.T) {
	audited := map[string]string{
		"Message.Images":     "never modified after append; shared",
		"Message.ToolCalls":  "annotateToolCallLocked writes in place; cloned",
		"Message.Citations":  "never modified after append; shared",
		"Message.Usage":      "replaced wholesale, never written through; shared",
		"Message.Timestamp":  "replaced wholesale; shared",
		"Message.LatencyMS":  "replaced wholesale; shared",
		"Message.TTFTMs":     "replaced wholesale; shared",
		"Message.WallTimeMS": "replaced wholesale; shared",
		"ToolCall.LatencyMS": "replaced wholesale; shared",
	}
	for name, typ := range map[string]reflect.Type{
		"Message":  reflect.TypeOf(adapter.Message{}),
		"ToolCall": reflect.TypeOf(adapter.ToolCall{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			switch f.Type.Kind() {
			case reflect.Slice, reflect.Map, reflect.Ptr, reflect.Array, reflect.Interface:
				if _, ok := audited[name+"."+f.Name]; !ok {
					t.Errorf("%s.%s (%s) is a new reference-typed field: audit its in-place writers and update Session.Snapshot", name, f.Name, f.Type)
				}
			}
		}
	}
}
