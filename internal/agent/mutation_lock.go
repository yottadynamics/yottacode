package agent

import (
	"path/filepath"
	"sync"
)

// MutationLockRegistry serializes Mutator tool calls that target the same
// file within one session. AgentTool is ParallelSafe and, by default,
// shares the parent's live cwd with every foreground subagent it spawns.
// A parent that fans out several agent calls can otherwise race edits.
type MutationLockRegistry struct {
	mu   sync.Mutex
	busy map[string]mutationClaim
	next uint64
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
