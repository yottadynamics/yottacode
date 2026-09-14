package syncutil

import "sync"

// PollingRWMutex always uses sync.RWMutex, including deadlock-tagged builds.
//
// It is reserved for context-aware acquisition loops that repeatedly call
// TryLock or TryRLock. go-deadlock v0.3.9 removes an existing holder from its
// bookkeeping after a failed try-lock from another goroutine, so instrumenting
// such polling would make later recursive-lock and lock-order reports unsound.
// Keep ordinary locks on Mutex/RWMutex so the detector covers them.
type PollingRWMutex = sync.RWMutex
