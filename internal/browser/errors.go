// Package browser drives a real Chromium/Chrome instance over the Chrome
// DevTools Protocol via go-rod/rod, giving the agent's browser_* tools a
// pure-Go backend with no Node.js or Playwright dependency. See
// roadmap/v0.5.0/j1-browser-automation-rod.md for the full spec.
package browser

import "errors"

// Stable error taxonomy the browser_* tools map to user-facing messages.
// Every error this package returns wraps exactly one of these via %w, so
// callers can classify failures with errors.Is regardless of the
// underlying rod/CDP error type.
var (
	// ErrNoBinaryFound means no system Chrome/Chromium/Edge binary was
	// found on PATH or in well-known install locations. Never triggers an
	// automatic download — see findChromeBinary.
	ErrNoBinaryFound = errors.New("no Chrome/Chromium binary found")

	// ErrLaunchFailed means a binary was found but the browser process
	// failed to start or the initial CDP connection failed.
	ErrLaunchFailed = errors.New("browser launch failed")

	// ErrNavigationTimeout means a navigation or wait-for-load-state
	// condition did not complete before its deadline.
	ErrNavigationTimeout = errors.New("navigation timed out")

	// ErrSelectorNotFound means a CSS selector did not resolve to an
	// element before its deadline.
	ErrSelectorNotFound = errors.New("selector not found")

	// ErrActionDenied means the manager's session was already torn down
	// by an explicit browser_close. Once closed, a Manager never
	// relaunches — close only ever reduces capability, per the safety
	// model in the roadmap doc.
	ErrActionDenied = errors.New("action denied: browser session is closed")
)
