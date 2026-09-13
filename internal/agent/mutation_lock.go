package agent

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

// MutationLockRegistry serializes Mutator tool calls that target the same
// file within one session. AgentTool is ParallelSafe and, by default,
// shares the parent's live cwd with every foreground subagent it spawns.
// A parent that fans out several agent calls can otherwise race edits.
//
// It also guards a second, orthogonal hazard via cwdMu: enter_worktree /
// exit_worktree mutate the session's shared *CwdRef, which every other
// mutating tool resolves its relative path against — once for the lock
// (ToolPathsToSnapshot, before Execute) and again independently inside its
// own Execute. If a cwd swap lands between those two reads, the lock can
// end up claiming a different path than the one actually written. Routing
// this through the same per-path claim map as Acquire (e.g. a shared
// sentinel path every mutation must also claim) would work but serializes
// every mutation against every other one, defeating the whole point of
// per-path locking. An RWMutex keeps the two concerns separate: any number
// of ordinary mutations hold the read side concurrently (their paths still
// serialize normally via Acquire when they actually collide); a worktree
// swap takes the write side, which waits for every in-flight mutation to
// finish and blocks new ones from starting until the swap completes.
type MutationLockRegistry struct {
	mu   sync.Mutex
	busy map[string]mutationClaim
	next uint64

	cwdMu sync.RWMutex
}

// RLockCwdStability is held by an ordinary mutating tool call for the
// span between resolving its lock paths and finishing Execute, so it
// cannot straddle a concurrent enter_worktree/exit_worktree cwd swap. Many
// callers hold this concurrently — it never serializes mutation against
// mutation, only mutation against a cwd swap. A nil registry is a safe
// no-op, matching Acquire.
//
// ctx-aware: sync.RWMutex has no context-aware blocking acquire, so this
// uses short TryLock polling. That avoids abandoning a blocked acquisition in
// a background goroutine if the current holder never releases.
func (r *MutationLockRegistry) RLockCwdStability(ctx context.Context) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	if err := waitForCwdLock(ctx, r.cwdMu.TryRLock); err != nil {
		return func() {}, err
	}
	return r.cwdMu.RUnlock, nil
}

// waitForCwdLock waits for a cwd lock without creating a goroutine that can
// outlive a canceled turn. Try-lock polling is deliberate: sync.RWMutex has
// no context-aware blocking acquire, and abandoning a blocked acquisition in
// a goroutine leaks that goroutine if the current holder never releases.
func waitForCwdLock(ctx context.Context, try func() bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if try() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// LockCwdStability is held by enter_worktree/exit_worktree for the
// duration of their own Execute: exclusive, so it waits for every
// in-flight mutation (holding the read side above) to finish and blocks
// new ones from starting — and from a concurrent worktree-swap tool call
// too, since a second writer also waits on the same exclusive lock — until
// the swap completes. A nil registry is a safe no-op, matching Acquire.
//
// ctx-aware for the same reason as RLockCwdStability — see its comment.
func (r *MutationLockRegistry) LockCwdStability(ctx context.Context) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	if err := waitForCwdLock(ctx, r.cwdMu.TryLock); err != nil {
		return func() {}, err
	}
	return r.cwdMu.Unlock, nil
}

type mutationClaim struct {
	owner string
	id    uint64
}

// Acquire claims every path atomically. Lock identity is canonicalized so
// lexical aliases and existing symlink aliases share one claim. The returned
// conflict path remains a cleaned caller path for readable errors.
func (r *MutationLockRegistry) Acquire(owner string, paths []string) (release func(), conflictPath, conflictOwner string, ok bool) {
	if r == nil || len(paths) == 0 {
		return func() {}, "", "", true
	}
	type lockPath struct {
		key     string
		display string
	}
	resolved := make([]lockPath, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		clean := filepath.Clean(p)
		key := canonicalMutationPath(clean)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		resolved = append(resolved, lockPath{key: key, display: clean})
	}
	if len(resolved) == 0 {
		return func() {}, "", "", true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range resolved {
		if claim, taken := r.busy[p.key]; taken {
			return func() {}, p.display, claim.owner, false
		}
	}
	if r.busy == nil {
		r.busy = make(map[string]mutationClaim, len(resolved))
	}
	r.next++
	id := r.next
	for _, p := range resolved {
		r.busy[p.key] = mutationClaim{owner: owner, id: id}
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, p := range resolved {
			if claim, taken := r.busy[p.key]; taken && claim.id == id {
				delete(r.busy, p.key)
			}
		}
	}, "", "", true
}

// canonicalMutationPath resolves existing symlink components while retaining
// a clean path for files that do not exist yet. This covers aliases without
// making creation of a new output fail merely because it is not present.
func canonicalMutationPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	parent := filepath.Dir(path)
	base := filepath.Base(path)
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolved, base)
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return path
}
