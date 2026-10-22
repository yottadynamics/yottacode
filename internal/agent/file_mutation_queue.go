package agent

import (
	"path/filepath"
	"sort"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

type editPathLockEntry struct {
	lock syncutil.Mutex
	refs int
}

var editPathLocks struct {
	mu      syncutil.Mutex
	entries map[string]*editPathLockEntry
}

// withEditPathLock serializes one-path filesystem mutations.
func withEditPathLock[T any](path string, fn func() (T, error)) (T, error) {
	return withEditPathLocks([]string{path}, fn)
}

// withEditPathLocks serializes multi-path mutations. Stable acquisition order
// prevents deadlocks when two operations claim the same paths in reverse order.
func withEditPathLocks[T any](paths []string, fn func() (T, error)) (T, error) {
	keys := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		key := canonicalMutationPath(filepath.Clean(path))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]*editPathLockEntry, 0, len(keys))
	for _, key := range keys {
		editPathLocks.mu.Lock()
		if editPathLocks.entries == nil {
			editPathLocks.entries = make(map[string]*editPathLockEntry)
		}
		entry := editPathLocks.entries[key]
		if entry == nil {
			entry = &editPathLockEntry{}
			editPathLocks.entries[key] = entry
		}
		entry.refs++
		editPathLocks.mu.Unlock()
		entry.lock.Lock()
		entries = append(entries, entry)
	}
	defer func() {
		for i := len(entries) - 1; i >= 0; i-- {
			entries[i].lock.Unlock()
		}
		editPathLocks.mu.Lock()
		for i, key := range keys {
			entry := entries[i]
			entry.refs--
			if entry.refs == 0 {
				delete(editPathLocks.entries, key)
			}
		}
		editPathLocks.mu.Unlock()
	}()
	return fn()
}
