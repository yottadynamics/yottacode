package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMutationLockRegistry_NilAndEmptyAlwaysSucceed(t *testing.T) {
	var r *MutationLockRegistry
	release, conflictPath, conflictOwner, ok := r.Acquire("owner", []string{"/a"})
	if !ok || conflictPath != "" || conflictOwner != "" {
		t.Fatalf("nil registry: got ok=%v conflictPath=%q conflictOwner=%q", ok, conflictPath, conflictOwner)
	}
	release()
	r = &MutationLockRegistry{}
	release, _, _, ok = r.Acquire("owner", nil)
	if !ok {
		t.Fatal("empty paths: expected Acquire to succeed")
	}
	release()
}

func TestMutationLockRegistry_ConflictAndRelease(t *testing.T) {
	r := &MutationLockRegistry{}
	release1, _, _, ok := r.Acquire("first", []string{"/f"})
	if !ok {
		t.Fatal("first Acquire should succeed")
	}
	_, conflictPath, conflictOwner, ok := r.Acquire("second", []string{"/f"})
	if ok || conflictPath != "/f" || conflictOwner != "first" {
		t.Fatalf("conflict = ok=%v path=%q owner=%q", ok, conflictPath, conflictOwner)
	}
	release1()
	release2, _, _, ok := r.Acquire("second", []string{"/f"})
	if !ok {
		t.Fatal("Acquire should succeed after release")
	}
	release2()
}

func TestMutationLockRegistry_AllOrNothing(t *testing.T) {
	r := &MutationLockRegistry{}
	releaseTaken, _, _, ok := r.Acquire("holder", []string{"/b"})
	if !ok {
		t.Fatal("setup Acquire should succeed")
	}
	defer releaseTaken()
	_, conflictPath, _, ok := r.Acquire("second", []string{"/a", "/b"})
	if ok || conflictPath != "/b" {
		t.Fatalf("conflict = ok=%v path=%q", ok, conflictPath)
	}
	releaseA, _, _, ok := r.Acquire("third", []string{"/a"})
	if !ok {
		t.Fatal("/a should remain free")
	}
	releaseA()
}

func TestMutationLockRegistry_DoubleReleaseIsSafe(t *testing.T) {
	r := &MutationLockRegistry{}
	release, _, _, ok := r.Acquire("owner", []string{"/f"})
	if !ok {
		t.Fatal("Acquire should succeed")
	}
	release()
	release()
	release2, _, _, ok := r.Acquire("owner2", []string{"/f"})
	if !ok {
		t.Fatal("path should be free")
	}
	release2()
}

func TestMutationLockRegistry_StaleReleaseDoesNotEvictLaterClaim(t *testing.T) {
	r := &MutationLockRegistry{}
	release1, _, _, ok := r.Acquire("edit_file", []string{"/f"})
	if !ok {
		t.Fatal("first Acquire should succeed")
	}
	release1()
	release2, _, _, ok := r.Acquire("edit_file", []string{"/f"})
	if !ok {
		t.Fatal("second Acquire should succeed")
	}
	release1()
	_, conflictPath, conflictOwner, ok := r.Acquire("third", []string{"/f"})
	if ok || conflictPath != "/f" || conflictOwner != "edit_file" {
		t.Fatalf("stale release = ok=%v path=%q owner=%q", ok, conflictPath, conflictOwner)
	}
	release2()
}

func TestMutationLockRegistry_AliasedExistingPathConflicts(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(realDir, "shared.go")
	if err := os.WriteFile(file, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := &MutationLockRegistry{}
	release, _, _, ok := r.Acquire("first", []string{file})
	if !ok {
		t.Fatal("first Acquire should succeed")
	}
	defer release()
	_, conflictPath, conflictOwner, ok := r.Acquire("second", []string{filepath.Join(link, "shared.go")})
	if ok || conflictPath == "" || conflictOwner != "first" {
		t.Fatalf("alias conflict = ok=%v path=%q owner=%q", ok, conflictPath, conflictOwner)
	}
}
