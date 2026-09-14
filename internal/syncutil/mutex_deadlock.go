//go:build deadlock

// Built only under `-tags deadlock` (see mutex.go for the default build and
// the rationale). Swaps every syncutil.Mutex/RWMutex in the codebase for
// sasha-s/go-deadlock's instrumented equivalents: a goroutine that Locks a
// mutex it already holds — directly, or indirectly through a chain of
// helper calls — gets a stack trace and a fast failure (deadlock.Opts.
// DeadlockTimeout, a few seconds by default) instead of hanging forever.
// Cross-goroutine lock-order cycles are caught the same way. Kept off a
// build tag rather than the default because the extra per-Lock bookkeeping
// isn't worth paying in production or in every ordinary `go test` run —
// it's for the dedicated `-tags deadlock` CI job and for reaching for
// locally when a hang is suspected.
package syncutil

import "github.com/sasha-s/go-deadlock"

// Mutex is deadlock.Mutex under -tags deadlock.
type Mutex = deadlock.Mutex

// RWMutex is deadlock.RWMutex under -tags deadlock.
type RWMutex = deadlock.RWMutex

func init() {
	// Recursive locking and inconsistent lock ordering are detected before
	// blocking. Disable the separate wait-duration heuristic because some
	// yottacode mutexes intentionally cover operations that may exceed any
	// short global threshold (for example, browser actions). A slow but
	// progressing critical section is contention, not proof of deadlock.
	deadlock.Opts.DeadlockTimeout = 0
	// The two-goroutine stack dump go-deadlock prints by default is exactly
	// the pair that raced into the lock; the goroutine that's actually
	// holding it (the one that will never call Unlock, which is what you
	// need to see to find the bug) is often a third one. Dump every
	// goroutine instead, the same shape of output the SIGUSR1 handler in
	// cmd/yottacode/debugdump.go produces for a live hang.
	deadlock.Opts.PrintAllCurrentGoroutines = true
}
