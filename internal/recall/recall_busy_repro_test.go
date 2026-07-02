package recall

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIndexSessionSQLiteBusyRepro intentionally holds a write transaction on
// one recall connection while another connection attempts a single indexing
// transaction. It reproduces the user-visible TUI warning path that the retry
// wrapper now suppresses for transient locks:
// `recall: upsert session: database is locked (5) (SQLITE_BUSY)`.
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
	t.Logf("reproduced recall index failure: %s", got)
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
