package agent

import (
	"path/filepath"

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

// withEditPathLock serializes direct edit-tool calls as well as loop-dispatched
// calls. Entries are reference-counted so long sessions do not retain one lock
// forever for every path ever edited.
func withEditPathLock[T any](path string, fn func() (T, error)) (T, error) {
	key := canonicalMutationPath(filepath.Clean(path))
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
	defer func() {
		entry.lock.Unlock()
		editPathLocks.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(editPathLocks.entries, key)
		}
		editPathLocks.mu.Unlock()
	}()
	return fn()
}
