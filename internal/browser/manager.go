package browser

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// Status is a point-in-time snapshot of the manager's lifecycle state,
// returned by browser_status. Computing it never launches a browser or
// otherwise changes state — it only reports the binary path (discovered
// on demand, cheaply, via PATH/well-known-location checks) and whatever
// session is already running.
type Status struct {
	BinaryPath string
	Active     bool
	CurrentURL string
	ProfileDir string
}

// defaultActionTimeout bounds every browser_* action call that doesn't
// take its own explicit deadline (browser_wait's timeout_ms is the only
// one that does). Manager holds its mutex for a call's entire duration
// (see the Manager doc comment), so an unbounded action — an unreachable
// site, a `load` event that never fires, a Chrome binary that starts but
// never opens its debug port — wouldn't just fail that one call, it would
// wedge the whole session, including browser_close, until the process
// itself is killed. context.WithTimeout composes with whatever deadline
// the caller's ctx already carries (the earlier of the two always wins),
// so this only ever tightens a looser or absent deadline, never loosens a
// tighter one.
const defaultActionTimeout = 60 * time.Second

// Manager owns the session-scoped browser lifecycle: at most one launched
// Chrome/Chromium process and one active page, lazily started on the
// first action call. Every exported method takes mu for its full
// duration — including the underlying CDP round-trip — because there is
// exactly one page to drive; overlapping calls would race on it with no
// benefit, so serializing here is simpler and safer than trying to prove
// any subset of calls is independent. This is also what a concurrent
// `go test -race` exercise over Manager is checking.
type Manager struct {
	mu         sync.Mutex
	sess       pageSession
	binPath    string
	profileDir string
	closed     bool

	// actionTimeout overrides defaultActionTimeout when positive — see
	// the timeout method. Tests set this to something short to exercise
	// timeout behavior without actually waiting a minute; a zero value
	// (including every Manager literal that predates this field) falls
	// back to defaultActionTimeout, so existing callers are unaffected.
	actionTimeout time.Duration

	// newSession, findBinary, and mkProfileDir are overridable so unit
	// tests can exercise lifecycle/serialization/error-mapping without
	// spawning a real Chrome process. NewManager wires the real
	// implementations; tests construct a Manager literal directly (same
	// package) with fakes instead.
	newSession   func(ctx context.Context, bin, profileDir string) (pageSession, error)
	findBinary   func() (string, error)
	mkProfileDir func() (string, error)
}

// NewManager returns an unlaunched Manager. The browser process starts
// lazily on the first action call that needs it (see ensureLocked);
// browser_status and browser_wait never trigger a launch.
func NewManager() *Manager {
	return &Manager{
		newSession:   launchSession,
		findBinary:   findChromeBinary,
		mkProfileDir: newProfileDir,
	}
}

// timeout resolves the effective per-action deadline: actionTimeout when
// a test (or future config knob) has set one, defaultActionTimeout
// otherwise.
func (m *Manager) timeout() time.Duration {
	if m.actionTimeout > 0 {
		return m.actionTimeout
	}
	return defaultActionTimeout
}

// ensureLocked lazily launches the browser on first use. Callers must
// already hold m.mu, and ctx should already carry the action's bounded
// deadline (see withActionTimeout) so a launch that never completes — a
// binary that starts but never opens its debug port — can't hang forever
// either.
//
// It also recovers from a crashed browser: if the process behind m.sess
// died since the last call (OOM-killed, killed externally, or just
// crashed), every future call would otherwise fail forever with an
// opaque dead-connection error, since nothing else ever notices and
// clears m.sess. Checking alive() here — cheap, a process-existence
// probe, not a CDP round-trip — means a crash instead degrades to one
// clean relaunch on the next action, transparent to the caller.
func (m *Manager) ensureLocked(ctx context.Context) (pageSession, error) {
	if m.closed {
		return nil, ErrActionDenied
	}
	if m.sess != nil {
		if m.sess.alive() {
			return m.sess, nil
		}
		m.discardDeadSessionLocked()
	}
	bin, err := m.findBinary()
	if err != nil {
		return nil, err
	}
	dir, err := m.mkProfileDir()
	if err != nil {
		return nil, err
	}
	sess, err := m.newSession(ctx, bin, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	m.binPath = bin
	m.profileDir = dir
	m.sess = sess
	return sess, nil
}

// discardDeadSessionLocked tears down a session whose process has already
// died (best-effort, and via forceCleanup rather than close — there's no
// live connection left to gracefully negotiate a shutdown with, and doing
// so could hang rather than fail fast) and clears m.sess/m.profileDir so
// the next ensureLocked call launches a fresh one. Unlike Close, this does
// NOT set m.closed: a crash isn't a user decision to stop using the
// browser, so future calls should still relaunch normally. Callers must
// already hold m.mu, and must have already confirmed the session is dead
// (checked !alive()) before calling this.
func (m *Manager) discardDeadSessionLocked() {
	m.sess.forceCleanup()
	m.sess = nil
	if m.profileDir != "" {
		_ = os.RemoveAll(m.profileDir)
		m.profileDir = ""
	}
}

// withActionTimeout returns a derived context bounded by m.timeout() and
// its cancel func. Composes safely with a caller-provided deadline:
// context.WithTimeout always resolves to whichever bound fires first, so
// this only ever tightens a looser or absent deadline.
func (m *Manager) withActionTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, m.timeout())
}

// Status never launches the browser — see the Status doc comment.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{BinaryPath: m.binPath, ProfileDir: m.profileDir}
	if st.BinaryPath == "" {
		if bin, err := m.findBinary(); err == nil {
			st.BinaryPath = bin
		}
	}
	// alive() is a cheap process-existence probe, not a CDP round-trip, so
	// checking it here doesn't violate Status's "never changes state"
	// contract — it just keeps Active honest after a crash instead of
	// reporting a session that no longer exists. Recovery itself (clearing
	// m.sess) still only happens in ensureLocked, on the next real action.
	if m.sess != nil && m.sess.alive() {
		st.Active = true
		st.CurrentURL = m.sess.currentURL()
	}
	return st
}

func (m *Manager) Navigate(ctx context.Context, url, waitUntil string) (NavigateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return NavigateResult{}, err
	}
	return s.navigate(ctx, url, waitUntil)
}

func (m *Manager) Screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return nil, err
	}
	return s.screenshot(ctx, selector, fullPage)
}

func (m *Manager) Inspect(ctx context.Context, selector string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return "", err
	}
	return s.inspect(ctx, selector)
}

func (m *Manager) Click(ctx context.Context, selector string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return err
	}
	return s.click(ctx, selector)
}

func (m *Manager) Type(ctx context.Context, selector, text string, submit bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return err
	}
	return s.typeText(ctx, selector, text, submit)
}

func (m *Manager) Hotkey(ctx context.Context, keys string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return err
	}
	return s.hotkey(ctx, keys)
}

func (m *Manager) Scroll(ctx context.Context, selector string, deltaX, deltaY float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return err
	}
	return s.scroll(ctx, selector, deltaX, deltaY)
}

// Wait deliberately does NOT launch the browser: waiting is defined as
// blocking until an already-visible condition is met, and a session that
// was never launched has nothing to wait for. Launching a process is a
// real state change, which is exactly what browser_wait's no-approval
// exemption assumes it never does. A non-positive timeout (the "built-in
// timeout" the tool docs promise) falls back to m.timeout() rather than
// waiting unbounded — the same policy every other action gets automatically.
func (m *Manager) Wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrActionDenied
	}
	if m.sess == nil {
		return nil
	}
	if !m.sess.alive() {
		// Discard it now rather than leave the next call (whatever tool
		// it is) to rediscover the same crash — but report it here too:
		// silently returning nil would claim the wait condition was
		// checked and satisfied, when nothing was actually checked at all.
		m.discardDeadSessionLocked()
		return errors.New("browser process is no longer running (it crashed or was killed); the next browser_* call will relaunch it")
	}
	if timeout <= 0 {
		timeout = m.timeout()
	}
	return m.sess.wait(ctx, selector, text, networkIdle, timeout)
}

// Close tears down the browser process and removes its temp profile
// directory. Idempotent: safe to call multiple times (an explicit
// browser_close, then again via CleanupTool.Cleanup at session exit).
// Once closed, the manager never relaunches — every later action call
// returns ErrActionDenied instead of silently starting a new browser.
func (m *Manager) Close(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var err error
	if m.sess != nil {
		if m.sess.alive() {
			err = m.sess.close()
		} else {
			// Already dead — nothing to gracefully close over a dead
			// connection (see forceCleanup's doc comment).
			m.sess.forceCleanup()
		}
		m.sess = nil
	}
	if m.profileDir != "" {
		if rmErr := os.RemoveAll(m.profileDir); rmErr != nil && err == nil {
			err = rmErr
		}
		m.profileDir = ""
	}
	return err
}
