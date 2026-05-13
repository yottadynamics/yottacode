package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// newTestStore returns a Store rooted in t.TempDir, so each test gets
// its own filesystem and parallel runs don't collide.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestBegin_WritesMetaAndMessages(t *testing.T) {
	s := newTestStore(t)
	history := []adapter.Message{
		{Role: adapter.RoleUser, Content: "first"},
		{Role: adapter.RoleAssistant, Content: "reply"},
	}
	cpID, err := s.Begin("sess-1", "do the thing", 2, history)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if cpID == "" {
		t.Fatal("Begin returned empty checkpoint id")
	}

	meta, err := s.LoadMeta("sess-1", cpID)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.UserPrompt != "do the thing" {
		t.Errorf("meta.UserPrompt = %q, want %q", meta.UserPrompt, "do the thing")
	}
	if meta.UserMsgIdx != 2 {
		t.Errorf("meta.UserMsgIdx = %d, want 2", meta.UserMsgIdx)
	}
	if len(meta.Files) != 0 {
		t.Errorf("meta.Files = %v, want empty", meta.Files)
	}

	msgs, err := s.LoadMessages("sess-1", cpID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "first" || msgs[1].Content != "reply" {
		t.Errorf("messages snapshot mismatch: %+v", msgs)
	}

	man, err := s.LoadManifest("sess-1")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(man.Entries) != 1 || man.Entries[0].CheckpointID != cpID {
		t.Errorf("manifest = %+v, want one entry %s", man, cpID)
	}
}

func TestSnapshotPath_IdempotentOnRepeatPath(t *testing.T) {
	s := newTestStore(t)
	cpID, _ := s.Begin("sess", "hi", 0, nil)

	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.SnapshotPath("sess", cpID, f); err != nil {
		t.Fatalf("SnapshotPath 1: %v", err)
	}
	if err := s.SnapshotPath("sess", cpID, f); err != nil {
		t.Fatalf("SnapshotPath 2: %v", err)
	}

	meta, _ := s.LoadMeta("sess", cpID)
	if len(meta.Files) != 1 {
		t.Fatalf("meta.Files=%d, want exactly 1 after duplicate snapshot", len(meta.Files))
	}
	if meta.Files[0].Path != f || meta.Files[0].SHA256 == "" || !meta.Files[0].ExistedAtCapture {
		t.Errorf("file entry wrong: %+v", meta.Files[0])
	}
}

func TestSnapshotPath_MissingFileRecordsExistedFalse(t *testing.T) {
	s := newTestStore(t)
	cpID, _ := s.Begin("sess", "hi", 0, nil)

	missing := filepath.Join(t.TempDir(), "nope.txt")
	if err := s.SnapshotPath("sess", cpID, missing); err != nil {
		t.Fatalf("SnapshotPath: %v", err)
	}

	meta, _ := s.LoadMeta("sess", cpID)
	if len(meta.Files) != 1 {
		t.Fatalf("meta.Files=%d, want 1", len(meta.Files))
	}
	if meta.Files[0].ExistedAtCapture {
		t.Error("ExistedAtCapture should be false for missing file")
	}
	if meta.Files[0].SHA256 != "" {
		t.Error("SHA256 should be empty for missing file")
	}
}

func TestSnapshotPath_RequiresAbsolutePath(t *testing.T) {
	s := newTestStore(t)
	cpID, _ := s.Begin("sess", "hi", 0, nil)

	if err := s.SnapshotPath("sess", cpID, "relative/path.txt"); err == nil {
		t.Fatal("expected error for relative path, got nil")
	}
}

func TestSnapshotPath_DedupsBlobsAcrossCheckpoints(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("identical"), 0o644); err != nil {
		t.Fatal(err)
	}

	cp1, _ := s.Begin("sess", "p1", 0, nil)
	if err := s.SnapshotPath("sess", cp1, f); err != nil {
		t.Fatal(err)
	}
	cp2, _ := s.Begin("sess", "p2", 0, nil)
	if err := s.SnapshotPath("sess", cp2, f); err != nil {
		t.Fatal(err)
	}

	blobs, err := os.ReadDir(s.blobsDir("sess"))
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 {
		t.Errorf("expected 1 blob (content-addressed dedup), got %d", len(blobs))
	}
}

func TestRestoreCode_WritesBackOriginalBytes(t *testing.T) {
	s := newTestStore(t)
	cpID, _ := s.Begin("sess", "hi", 0, nil)

	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	original := []byte("before edit")
	if err := os.WriteFile(f, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SnapshotPath("sess", cpID, f); err != nil {
		t.Fatal(err)
	}
	// Simulate the tool mutating the file.
	if err := os.WriteFile(f, []byte("after edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	errs, err := s.RestoreCode("sess", cpID)
	if err != nil {
		t.Fatalf("RestoreCode: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("per-file errors: %v", errs)
	}
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("file contents = %q, want %q", got, original)
	}
}

func TestRestoreCode_DeletesFilesThatDidNotExist(t *testing.T) {
	s := newTestStore(t)
	cpID, _ := s.Begin("sess", "hi", 0, nil)

	dir := t.TempDir()
	f := filepath.Join(dir, "new.txt")
	// Snapshot before the file exists.
	if err := s.SnapshotPath("sess", cpID, f); err != nil {
		t.Fatal(err)
	}
	// Simulate the tool creating it.
	if err := os.WriteFile(f, []byte("created during turn"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.RestoreCode("sess", cpID); err != nil {
		t.Fatalf("RestoreCode: %v", err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Errorf("file should be deleted on rewind, stat err = %v", err)
	}
}

func TestSweep_DropsExpiredAndGCsOrphanBlobs(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldCP, _ := s.Begin("sess", "old", 0, nil)
	if err := s.SnapshotPath("sess", oldCP, f); err != nil {
		t.Fatal(err)
	}

	// Backdate the manifest entry so it falls outside the TTL.
	man, _ := s.LoadManifest("sess")
	man.Entries[0].Created = time.Now().Add(-48 * time.Hour)
	// Also backdate the meta.json so reads stay consistent.
	meta, _ := s.LoadMeta("sess", oldCP)
	meta.Created = man.Entries[0].Created
	if err := writeJSONAtomic(filepath.Join(s.checkpointDir("sess", oldCP), "meta.json"), meta); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(s.manifestPath("sess"), man); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Sweep(24 * time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	// Session dir should be removed since the only checkpoint was expired.
	if _, err := os.Stat(s.sessionDir("sess")); !os.IsNotExist(err) {
		t.Errorf("session dir should be wiped, stat err = %v", err)
	}
}

func TestSweep_KeepsYoungCheckpoints(t *testing.T) {
	s := newTestStore(t)
	cpID, _ := s.Begin("sess", "fresh", 0, nil)

	if _, err := s.Sweep(24 * time.Hour); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := s.LoadMeta("sess", cpID); err != nil {
		t.Errorf("young checkpoint should survive sweep, got: %v", err)
	}
}

func TestLoadManifest_MissingSessionReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	man, err := s.LoadManifest("never-existed")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(man.Entries) != 0 {
		t.Errorf("expected empty manifest, got %+v", man)
	}
}

func TestLoadManifest_OrdersNewestFirst(t *testing.T) {
	s := newTestStore(t)
	cp1, _ := s.Begin("sess", "old", 0, nil)
	// Force distinct timestamps even on fast filesystems.
	time.Sleep(time.Millisecond)
	cp2, _ := s.Begin("sess", "new", 1, nil)

	man, _ := s.LoadManifest("sess")
	if len(man.Entries) != 2 {
		t.Fatalf("entries=%d", len(man.Entries))
	}
	if man.Entries[0].CheckpointID != cp2 {
		t.Errorf("entries[0] = %s, want %s (newest first)", man.Entries[0].CheckpointID, cp2)
	}
	if man.Entries[1].CheckpointID != cp1 {
		t.Errorf("entries[1] = %s, want %s", man.Entries[1].CheckpointID, cp1)
	}
}
