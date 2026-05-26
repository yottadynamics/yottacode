package agent

import "sync/atomic"

// CwdRef is the shared working-directory holder a session's tools
// read through instead of stashing an immutable string at construction
// time. It exists so enter_worktree / exit_worktree can swap the
// session's working directory mid-conversation without rebuilding the
// tool registry.
//
// All tools that previously held `Cwd string` now hold `CwdRef *CwdRef`
// and call CwdRef.Get() inside their Execute path. The same *CwdRef is
// shared across every tool at registration time (see tui/run.go and
// oneshot/oneshot.go), so one Set propagates to all of them on the
// next tool dispatch.
//
// Backed by atomic.Pointer[string] so the race detector stays clean
// when (rarely) parallel-safe reads from concurrent tool dispatch land
// alongside a Set from enter_worktree. In normal use the loop runs
// tool calls sequentially within a turn — atomicity is belt-and-braces.
type CwdRef struct {
	p atomic.Pointer[string]
}

// NewCwdRef constructs a CwdRef holding the given initial value.
func NewCwdRef(initial string) *CwdRef {
	r := &CwdRef{}
	r.p.Store(&initial)
	return r
}

// Get returns the current cwd. Nil-safe: returns "" when r is nil
// (defensive — tests sometimes construct tools without one).
func (r *CwdRef) Get() string {
	if r == nil {
		return ""
	}
	s := r.p.Load()
	if s == nil {
		return ""
	}
	return *s
}

// Set replaces the current cwd. Subsequent Get calls (from any tool
// sharing this CwdRef) observe the new value.
func (r *CwdRef) Set(v string) {
	if r == nil {
		return
	}
	r.p.Store(&v)
}
