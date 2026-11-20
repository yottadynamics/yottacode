package session

import (
	"maps"
	"slices"
	"sync"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// saveQueue serializes every write to one session file and carries the
// latest-wins background save. Queues are keyed by file path (not stored on
// Session) so a Snapshot, which shares the path, shares the queue; they are
// reference-counted and dropped once idle (see acquireQueue).
type saveQueue struct {
	path string
	refs int // guarded by queuesMu

	writeMu syncutil.Mutex // held for the duration of one file write

	mu      syncutil.Mutex
	idle    *sync.Cond // signalled when running flips false
	pending *Session
	after   func(*Session) error
	running bool
	err     error
}

var (
	queuesMu syncutil.Mutex
	queues   = map[string]*saveQueue{}
)

// acquireQueue returns the queue for path with its refcount raised; every call
// must be paired with releaseQueue. The map entry, the refcount and the delete
// share queuesMu, so two callers can never hold different queues for one path.
func acquireQueue(path string) *saveQueue {
	queuesMu.Lock()
	defer queuesMu.Unlock()
	q := queues[path]
	if q == nil {
		q = &saveQueue{path: path}
		q.idle = sync.NewCond(&q.mu)
		queues[path] = q
	}
	q.refs++
	return q
}

// releaseQueue drops a reference and forgets the queue once nothing holds it,
// no write is running or queued, and no background error is waiting to be
// collected by TakeAsyncErr.
func releaseQueue(q *saveQueue) {
	queuesMu.Lock()
	defer queuesMu.Unlock()
	q.refs--
	if q.refs > 0 {
		return
	}
	q.mu.Lock()
	reclaim := !q.running && q.pending == nil && q.err == nil
	q.mu.Unlock()
	if reclaim && queues[q.path] == q {
		delete(queues, q.path)
	}
}

// Snapshot returns an independent copy that is safe to marshal on another
// goroutine while s keeps changing. The caller must hold whatever lock guards
// s.Messages (the TUI holds histMu).
//
// What must be copied was established by auditing every in-place writer to
// session messages:
//   - the message elements themselves: the system message at index 0 has its
//     Content/CacheHeadBytes rewritten on the shared backing array every turn
//     (tui/cmd_retrieval.go, agentruntime/runtime.go), and @file injection
//     rewrites Content in place (tui/cmd_filerefs.go);
//   - each message's ToolCalls slice: agent.annotateToolCallLocked sets
//     ToolCalls[j].Status/LatencyMS in place.
//
// Pointer fields (Usage, Timestamp, LatencyMS, ...) are only ever replaced
// wholesale, never written through, and Images/Citations are never modified
// after the message is appended, so those are shared. Session-level slices and
// maps are cloned.
func (s *Session) Snapshot() *Session {
	c := *s
	c.Messages = slices.Clone(s.Messages)
	for i := range c.Messages {
		c.Messages[i].ToolCalls = slices.Clone(c.Messages[i].ToolCalls)
	}
	c.Todos = slices.Clone(s.Todos)
	c.SubagentTasks = slices.Clone(s.SubagentTasks)
	c.CompactionEvents = slices.Clone(s.CompactionEvents)
	c.ModelUsage = maps.Clone(s.ModelUsage)
	c.ToolStats = maps.Clone(s.ToolStats)
	return &c
}

// SaveAsync persists a snapshot of s on a background goroutine so the caller
// (the TUI's Update loop) never waits on marshalling or fsync. mu is the lock
// that guards s against concurrent mutation (the TUI's histMu); it is held
// only while the snapshot is taken, so a caller cannot forget to synchronise.
// Pass nil only when nothing else can touch s meanwhile. Saves coalesce:
// while one is being written, only the newest queued snapshot is kept. after,
// if non-nil, runs on the worker once the file is written (nil error only) and
// may index the snapshot; its error is reported like a save error. Save and
// Flush wait for queued work, so a later synchronous Save can never be
// overwritten by an older background snapshot. Errors surface via
// TakeAsyncErr.
func (s *Session) SaveAsync(mu sync.Locker, after func(*Session) error) {
	if mu != nil {
		mu.Lock()
	}
	snap := s.Snapshot()
	if mu != nil {
		mu.Unlock()
	}
	q := acquireQueue(s.path)
	q.mu.Lock()
	q.pending, q.after = snap, after
	if q.running {
		q.mu.Unlock()
		releaseQueue(q)
		return
	}
	q.running = true
	q.mu.Unlock()
	go q.run() // inherits the reference taken above; released when idle
}

func (q *saveQueue) run() {
	defer releaseQueue(q)
	for {
		q.mu.Lock()
		snap, after := q.pending, q.after
		q.pending, q.after = nil, nil
		if snap == nil {
			q.running = false
			q.idle.Broadcast()
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()

		err := snap.writeFile(q)
		if err == nil && after != nil {
			err = after(snap)
		}
		if err != nil {
			q.mu.Lock()
			q.err = err
			q.mu.Unlock()
		}
	}
}

// Flush blocks until every queued background save has been written.
func (s *Session) Flush() {
	q := acquireQueue(s.path)
	defer releaseQueue(q)
	q.mu.Lock()
	for q.running {
		q.idle.Wait()
	}
	q.mu.Unlock()
}

// TakeAsyncErr returns and clears the most recent background save error.
func (s *Session) TakeAsyncErr() error {
	q := acquireQueue(s.path)
	defer releaseQueue(q)
	q.mu.Lock()
	defer q.mu.Unlock()
	err := q.err
	q.err = nil
	return err
}

// FlushAll blocks until every session's queued background saves have been
// written. Shutdown paths and tests call it so no write outlives its owner
// (for example a test's temporary HOME).
func FlushAll() {
	queuesMu.Lock()
	qs := make([]*saveQueue, 0, len(queues))
	for _, q := range queues {
		q.refs++
		qs = append(qs, q)
	}
	queuesMu.Unlock()
	for _, q := range qs {
		q.mu.Lock()
		for q.running {
			q.idle.Wait()
		}
		q.mu.Unlock()
		releaseQueue(q)
	}
}
