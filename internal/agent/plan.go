package agent

import "sync"

// TodoStatus is the lifecycle state of a single todo item. The three
// core values mirror Claude Code's TodoWrite contract so the model has a
// familiar schema target. skipped is yottacode-specific: it lets the agent
// explicitly abandon obsolete plan items instead of leaving them pending.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoSkipped    TodoStatus = "skipped"
)

// Todo is one item in the agent's working plan. Content is the
// human-readable description; Status is one of the lifecycle
// values above. There is intentionally no stable ID — the model
// passes the full list every call, so identity is positional and
// renames are indistinguishable from delete+add (matching Claude's
// shape).
type Todo struct {
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

// PlanStore is the session-scoped owner of the current todo list.
// The TodoWriteTool replaces the list wholesale on each call; the
// agent loop reads the snapshot to emit a TodoUpdate event; the TUI
// renders from that event; session save/load copies the slice
// in/out for cross-session persistence.
type PlanStore struct {
	mu    sync.Mutex
	todos []Todo
}

func NewPlanStore() *PlanStore {
	return &PlanStore{}
}

// Snapshot returns a copy of the current todo list. Safe for the
// caller to retain — the returned slice does not alias the store's
// internal state.
func (p *PlanStore) Snapshot() []Todo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.todos) == 0 {
		return nil
	}
	out := make([]Todo, len(p.todos))
	copy(out, p.todos)
	return out
}

// Replace overwrites the list with the given items. The caller is
// responsible for validation (TodoWriteTool.Execute does it before
// reaching here).
func (p *PlanStore) Replace(items []Todo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(items) == 0 {
		p.todos = nil
		return
	}
	cp := make([]Todo, len(items))
	copy(cp, items)
	p.todos = cp
}
