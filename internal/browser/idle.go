package browser

import (
	"os"
	"time"
)

// defaultIdleTimeout is how long a launched browser may sit unused before
// the manager closes it. A session that lives for hours in the TUI would
// otherwise keep a headless Chrome (hundreds of MB) running long after the
// agent last touched it. Generous on purpose: closing discards the tabs and
// any page state, so this should only ever fire on a genuinely abandoned
// session, not between two steps of a slow task.
const defaultIdleTimeout = 15 * time.Minute

// touchLocked records that the browser was just used and (re)arms the idle
// timer. Callers must hold m.mu. A no-op when idle reaping is disabled or no
// session exists.
func (m *Manager) touchLocked() {
	if m.idleTimeout <= 0 || m.sess == nil {
		return
	}
	m.lastUsed = time.Now()
	if m.idleTimer == nil {
		m.idleTimer = time.AfterFunc(m.idleTimeout, m.reapIdle)
		return
	}
	m.idleTimer.Reset(m.idleTimeout)
}

// stopIdleTimerLocked disarms the idle timer. Callers must hold m.mu.
func (m *Manager) stopIdleTimerLocked() {
	if m.idleTimer != nil {
		m.idleTimer.Stop()
	}
}

// reapIdle is the idle timer's callback: it closes a session that has gone
// unused for the whole idle timeout. Unlike Close it does NOT mark the
// manager closed — reaping is housekeeping, not a user decision to stop, so
// the next action simply launches a fresh browser (with a fresh, empty
// profile, like any relaunch). Every action holds m.mu for its whole
// duration, so this can never run mid-action.
func (m *Manager) reapIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.sess == nil {
		return
	}
	if remaining := m.idleTimeout - time.Since(m.lastUsed); remaining > 0 {
		// Used since the timer was armed (touchLocked resets it, but this
		// callback may already have been queued on the lock): wait out the rest.
		m.idleTimer.Reset(remaining)
		return
	}
	if m.sess.alive() {
		_ = m.sess.close()
	} else {
		m.sess.forceCleanup()
	}
	m.sess = nil
	if m.profileDir != "" {
		_ = os.RemoveAll(m.profileDir)
		m.profileDir = ""
	}
	m.reapedIdle = true
}
