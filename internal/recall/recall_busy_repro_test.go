package recall

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIndexSessionSQLiteBusyRepro documents the low-level SQLite failure that
// can occur when a separate process holds the writer lock. IndexSession's public
// retry path is covered by TestIndexSessionRetriesTransientSQLiteBusy; this test
// keeps the original error visible without presenting it as an unexpected test
// failure.
func TestIndexSessionSQLiteBusyRepro(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "locked.sqlite")
	locker, err := openAt(dbPath)
	if err != nil {
		t.Fatalf("open locker: %v", err)
	}
	defer locker.Close()
	contender, err := openAt(dbPath)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	defer contender.Close()

	// Keep this repro fast. openAt normally waits longer per busy attempt; the
	// important part here is forcing the exact busy failure mode, not waiting.
	if _, err := contender.db.Exec(`PRAGMA busy_timeout=1`); err != nil {
		t.Fatalf("set busy timeout: %v", err)
	}

	tx, err := locker.db.Begin()
	if err != nil {
		t.Fatalf("begin locker tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO sessions(id, name, model, cwd, created, last_indexed) VALUES (?, ?, ?, ?, ?, ?)`,
		"held-lock", "", "", "", time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("seed held write lock: %v", err)
	}

	err = contender.indexSessionOnce(fakeSession("blocked", "this write should hit SQLITE_BUSY"))
	if err == nil {
		t.Fatal("IndexSession succeeded while another connection held a write lock")
	}
	got := err.Error()
	t.Logf("expected low-level recall lock error: %s", got)
	if !strings.Contains(got, "recall: upsert session") || !strings.Contains(got, "SQLITE_BUSY") {
		t.Fatalf("IndexSession error = %q, want recall upsert SQLITE_BUSY", got)
	}
}

// TestIndexSessionRetriesTransientSQLiteBusy holds SQLite's writer lock only
// briefly. IndexSession should retry the transient SQLITE_BUSY and eventually
// index successfully instead of leaking the warning to the TUI.
func TestIndexSessionRetriesTransientSQLiteBusy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "retry.sqlite")
	locker, err := openAt(dbPath)
	if err != nil {
		t.Fatalf("open locker: %v", err)
	}
	defer locker.Close()
	contender, err := openAt(dbPath)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	defer contender.Close()
	if _, err := contender.db.Exec(`PRAGMA busy_timeout=1`); err != nil {
		t.Fatalf("set busy timeout: %v", err)
	}

	tx, err := locker.db.Begin()
	if err != nil {
		t.Fatalf("begin locker tx: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO sessions(id, name, model, cwd, created, last_indexed) VALUES (?, ?, ?, ?, ?, ?)`,
		"held-lock", "", "", "", time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("seed held write lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- contender.IndexSession(fakeSession("eventual", "retry should index this"))
	}()
	time.Sleep(75 * time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatalf("release held write lock: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("IndexSession should retry transient SQLITE_BUSY: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("IndexSession did not finish after writer lock was released")
	}
	if hits, err := contender.Search("retry", 10); err != nil || len(hits) == 0 {
		t.Fatalf("retried session was not searchable; hits=%d err=%v", len(hits), err)
	}
}

// TestPutVector_SerializesOnRecallWriteMu guards the same invariant
// IndexSession and pruneMissingSessions already hold: a write must
// serialize on the process-wide recallWriteMu, not just the per-handle
// idx.writeMu, because "database/sql permits each handle to have its
// own SQLite connection, so an Index-local mutex alone still lets
// startup backfill race with session saves" (recall.go's own comment
// on recallWriteMu). PutVector — the write BackfillVectors' background
// goroutine calls — was missing this despite its doc comment claiming
// otherwise.
//
// Tested by holding recallWriteMu directly rather than racing real
// SQLite contention: SQLite's busy_timeout plus PutVector's own
// SQLITE_BUSY retry loop can absorb a brief timing-based race even
// without recallWriteMu, which would make a purely concurrent-load
// test flaky in both directions. Directly holding the mutex and
// asserting PutVector blocks until it's released is deterministic and
// tests the actual mechanism, not SQLite's independent contention
// tolerance.
func TestPutVector_SerializesOnRecallWriteMu(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "putvector-lock.sqlite")
	idx, err := openAt(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()
	if err := idx.IndexSession(fakeSession("seed", "seed message")); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Simulate another Index handle's write already in flight (e.g. a
	// turn-end IndexSession call) by holding the process-wide mutex
	// ourselves before PutVector ever gets a chance to take it.
	recallWriteMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- idx.PutVector("seed", 0, "test-model", "seed message", []float32{0.1, 0.2, 0.3})
	}()

	select {
	case <-done:
		recallWriteMu.Unlock()
		t.Fatal("PutVector returned while recallWriteMu was held by another writer — it must serialize on the process-wide mutex, not just idx.writeMu")
	case <-time.After(150 * time.Millisecond):
		// Expected: PutVector is blocked waiting on recallWriteMu.
	}
	recallWriteMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PutVector failed after recallWriteMu was released: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PutVector did not complete after recallWriteMu was released")
	}
}
