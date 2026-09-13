package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestMutationLockRegistry_CwdStability_NilRegistrySafe covers the
// signature change (func() -> (func(), error)) alongside the existing
// nil-registry conventions: both still succeed as a no-op.
func TestMutationLockRegistry_CwdStability_NilRegistrySafe(t *testing.T) {
	var r *MutationLockRegistry
	releaseR, err := r.RLockCwdStability(context.Background())
	if err != nil {
		t.Fatalf("nil registry RLockCwdStability: %v", err)
	}
	releaseR()
	releaseW, err := r.LockCwdStability(context.Background())
	if err != nil {
		t.Fatalf("nil registry LockCwdStability: %v", err)
	}
	releaseW()
}

// TestMutationLockRegistry_LockCwdStability_CancelUnblocksWaiter is the
// regression test for the ctx-awareness fix: before it, LockCwdStability
// did a bare cwdMu.Lock() with no ctx.Done() guard, so a worktree-swap
// tool call waiting behind an in-flight mutation (holding the read side)
// could not be interrupted by Ctrl+C — it would wait exactly as long as
// the read-side holder took, however long that was, same hang shape as
// the unhardened exec.Cmd calls fixed alongside this.
//
// Reproduced here: hold the read side indefinitely (simulating a stuck —
// or merely slow — concurrent mutation), start a write-side acquisition
// under a context that gets canceled shortly after, and assert it
// returns promptly with ctx.Err() instead of blocking until the read
// side is eventually released.
func TestMutationLockRegistry_LockCwdStability_CancelUnblocksWaiter(t *testing.T) {
	r := &MutationLockRegistry{}
	releaseR, err := r.RLockCwdStability(context.Background())
	if err != nil {
		t.Fatalf("RLockCwdStability: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = r.LockCwdStability(ctx)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("LockCwdStability took %s — did not unblock on ctx cancellation (would have hung until the read side released)", elapsed)
	}
	if err == nil {
		t.Fatal("expected ctx.Err() from a canceled wait; got nil (acquired despite the read lock still being held)")
	}
	if !isCancelErr(err) {
		t.Fatalf("expected a cancel error (isCancelErr); got %v", err)
	}

	// Release the read side now that the canceled waiter has backed off.
	// The abandoned background acquisition attempt should complete and
	// release cleanly — proving the mutex itself was never left locked
	// forever. A fresh, non-canceled acquisition succeeding is the proof:
	// it wouldn't if the abandoned goroutine leaked the lock.
	releaseR()
	done := make(chan struct{})
	go func() {
		release, err := r.LockCwdStability(context.Background())
		if err == nil {
			release()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("LockCwdStability never succeeded after the read side released — the abandoned canceled acquisition leaked the lock")
	}
}

// TestMutationLockRegistry_RLockCwdStability_CancelUnblocksWaiter mirrors
// the write-side test above for the read side: a reader waiting behind
// an in-flight worktree swap (holding the exclusive side) must also be
// interruptible by ctx cancellation, not just the writer.
func TestMutationLockRegistry_RLockCwdStability_CancelUnblocksWaiter(t *testing.T) {
	r := &MutationLockRegistry{}
	releaseW, err := r.LockCwdStability(context.Background())
	if err != nil {
		t.Fatalf("LockCwdStability: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = r.RLockCwdStability(ctx)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("RLockCwdStability took %s — did not unblock on ctx cancellation", elapsed)
	}
	if err == nil || !isCancelErr(err) {
		t.Fatalf("expected a cancel error (isCancelErr); got %v", err)
	}

	releaseW()
	done := make(chan struct{})
	go func() {
		release, err := r.RLockCwdStability(context.Background())
		if err == nil {
			release()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RLockCwdStability never succeeded after the write side released — the abandoned canceled acquisition leaked the lock")
	}
}
