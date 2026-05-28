package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLockfile_RoundTrip writes a populated lockfile to disk and
// reads it back, verifying every field survives JSON encode/decode.
// Catches schema drift between LockEntry's Go shape and its JSON
// tags.
func TestLockfile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	lf := newLockfile()
	lf.Set(LockEntry{
		Name:        "alpha",
		SourceType:  SourceGitHub,
		Source:      "owner/repo/skills/alpha",
		Hash:        "sha256:abc",
		InstalledAt: now,
		Trust:       TrustUnverified,
	})
	if err := lf.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadLockfile(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entry, ok := loaded.Get("alpha")
	if !ok {
		t.Fatal("entry alpha missing after reload")
	}
	if entry.SourceType != SourceGitHub {
		t.Errorf("source_type = %q", entry.SourceType)
	}
	if entry.Source != "owner/repo/skills/alpha" {
		t.Errorf("source = %q", entry.Source)
	}
	if entry.Hash != "sha256:abc" {
		t.Errorf("hash = %q", entry.Hash)
	}
	if !entry.InstalledAt.Equal(now) {
		t.Errorf("installed_at = %v, want %v", entry.InstalledAt, now)
	}
	if entry.Trust != TrustUnverified {
		t.Errorf("trust = %q", entry.Trust)
	}
}

// TestLockfile_MissingFileIsEmpty locks the contract that a
// not-yet-created lockfile loads as an empty in-memory document
// (not an error). Without this, every first install would have to
// special-case ErrNotExist at the call site.
func TestLockfile_MissingFileIsEmpty(t *testing.T) {
	lf, err := LoadLockfile(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(lf.Entries) != 0 {
		t.Errorf("expected empty entries, got %d", len(lf.Entries))
	}
	if lf.Version != CurrentLockfileVersion {
		t.Errorf("version = %d, want %d", lf.Version, CurrentLockfileVersion)
	}
}

// TestLockfile_RemoveDropsEntry verifies Remove deletes the named
// entry and Save persists the deletion. The "remove then load" round
// trip is what `skills uninstall` relies on to keep the lockfile in
// sync with the on-disk state.
func TestLockfile_RemoveDropsEntry(t *testing.T) {
	dir := t.TempDir()
	lf := newLockfile()
	lf.Set(LockEntry{Name: "alpha"})
	lf.Set(LockEntry{Name: "bravo"})
	if err := lf.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadLockfile(dir)
	loaded.Remove("alpha")
	if err := loaded.Save(dir); err != nil {
		t.Fatal(err)
	}
	final, _ := LoadLockfile(dir)
	if _, ok := final.Get("alpha"); ok {
		t.Error("alpha should be removed")
	}
	if _, ok := final.Get("bravo"); !ok {
		t.Error("bravo should still be present")
	}
}

// TestLockfile_AllSortedIsDeterministic guards against random map
// iteration leaking into CLI output. Two calls on the same lockfile
// must produce the same order, and the order must be by-name.
func TestLockfile_AllSortedIsDeterministic(t *testing.T) {
	lf := newLockfile()
	lf.Set(LockEntry{Name: "charlie"})
	lf.Set(LockEntry{Name: "alpha"})
	lf.Set(LockEntry{Name: "bravo"})
	out := lf.AllSorted()
	want := []string{"alpha", "bravo", "charlie"}
	if len(out) != len(want) {
		t.Fatalf("got %d entries, want %d", len(out), len(want))
	}
	for i, w := range want {
		if out[i].Name != w {
			t.Errorf("[%d] %q, want %q", i, out[i].Name, w)
		}
	}
}

// TestLockfile_PermissionsAreUser locks the 0600 bit on the saved
// file — the lockfile may someday carry trust hints or signing
// metadata, so it shouldn't be world-readable.
func TestLockfile_PermissionsAreUser(t *testing.T) {
	dir := t.TempDir()
	lf := newLockfile()
	if err := lf.Save(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms = %o, want 0600", info.Mode().Perm())
	}
}
