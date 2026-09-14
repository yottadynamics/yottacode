//go:build deadlock

// Built only under `-tags deadlock` (see mutex.go for the default build and
// the rationale). Swaps every syncutil.Mutex/RWMutex in the codebase for
// sasha-s/go-deadlock's instrumented equivalents: a goroutine that Locks a
// mutex it already holds — directly, or indirectly through a chain of
// helper calls — gets an immediate recursive-lock or inconsistent-order
// report instead of hanging forever. Kept behind a build tag rather than
// the default because the extra per-Lock bookkeeping
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
}
