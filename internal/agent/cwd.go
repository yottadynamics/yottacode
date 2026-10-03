package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// CwdRef is the shared working-directory holder a session's tools read through
// instead of stashing an immutable string at construction time. It also keeps
// the startup project root so a deleted worktree can be recovered safely.
type CwdRef struct {
	p atomic.Pointer[string]

	mu          syncutil.Mutex
	projectRoot string
}

var processCwdMu syncutil.Mutex

// NewCwdRef constructs a CwdRef holding the given initial value. When the
// initial directory exists, it becomes the stable fallback for later cwd
// recovery. A missing initial directory is deliberately not guessed: silently
// falling back to / would be unsafe for commands and hides startup errors.
func NewCwdRef(initial string) *CwdRef {
	r := &CwdRef{}
	r.p.Store(&initial)
	if info, err := os.Stat(initial); err == nil && info.IsDir() {
		r.projectRoot = filepath.Clean(initial)
	}
	return r
}

// Get returns the current cwd without performing I/O or state transitions.
// Recovery is explicit so the dispatch boundary can emit exactly one event.
func (r *CwdRef) Get() string {
	if r == nil {
		return ""
	}
	return r.load()
}

// Recover validates the current cwd. If it disappeared, it switches to the
// stable project root and reports the old and new paths. The update is atomic
// with respect to other recovery attempts, so concurrent tools do not emit
// duplicate state transitions.
func (r *CwdRef) Recover() (CwdRecovery, error) {
	if r == nil {
		return CwdRecovery{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	if current == "" {
		return CwdRecovery{}, fmt.Errorf("working directory is empty")
	}
	if info, err := os.Stat(current); err == nil && info.IsDir() {
		return CwdRecovery{}, nil
	}
	if r.projectRoot == "" {
		return CwdRecovery{}, nil
	}
	if info, err := os.Stat(r.projectRoot); err != nil || !info.IsDir() {
		return CwdRecovery{}, fmt.Errorf("working directory %q no longer exists and stable project root %q is unavailable", current, r.projectRoot)
	}
	processCwdMu.Lock()
	defer processCwdMu.Unlock()
	if err := os.Chdir(r.projectRoot); err != nil {
		return CwdRecovery{}, fmt.Errorf("recover working directory from %q to %q: %w", current, r.projectRoot, err)
	}
	r.store(r.projectRoot)
	return CwdRecovery{Requested: current, Actual: r.projectRoot, Recovered: true}, nil
}

type CwdRecovery struct {
	Requested string
	Actual    string
	Recovered bool
}

func (r *CwdRef) load() string {
	s := r.p.Load()
	if s == nil {
		return ""
	}
	return *s
}

func (r *CwdRef) store(v string) {
	r.p.Store(&v)
}

// Set replaces the current cwd. The startup project root is intentionally not
// changed when entering or leaving a managed worktree.
func (r *CwdRef) Set(v string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, err := os.Stat(v); (err != nil || !info.IsDir()) && !pathWithin(v, r.projectRoot) {
		r.projectRoot = ""
	}
	r.store(v)
}

func pathWithin(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
