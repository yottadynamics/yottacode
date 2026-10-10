// Package browser drives a real Chromium/Chrome instance over the Chrome
// DevTools Protocol via chromedp/cdproto, giving the agent's browser_* tools a
// pure-Go backend with no Node.js or Playwright dependency. See
// docs/tools.md and docs/security-and-allow-lists.md for the tool
// contract and safety model.
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

	// ErrCanceled means the caller canceled a browser action before it completed.
	ErrCanceled = errors.New("browser action canceled")

	// ErrBrowserCrashed means the browser process or its CDP connection died.
	ErrBrowserCrashed = errors.New("browser process exited unexpectedly")

	// by an explicit browser_close. Once closed, a Manager never
	// relaunches — close only ever reduces capability, per the safety
	// model documented in docs/security-and-allow-lists.md.
	ErrActionDenied = errors.New("action denied: browser session is closed")

	// ErrTabNotFound means a browser_switch_tab index didn't resolve to a
	// currently tracked page (out of range, or the tab closed since the
	// caller last listed tabs).
	ErrTabNotFound = errors.New("tab index not found")

	// ErrNoDisplay means a visible browser window was requested
	// (browser_handoff) on a host with no display server to show it on —
	// an SSH session, container, or CI box. The isolated headless session
	// is left untouched; the human has to complete the step some other
	// way.
	ErrNoDisplay = errors.New("no display available for a visible browser window")

	// ErrBlockedURL means the URL the agent asked to open uses a scheme the
	// browser tools refuse (file:, chrome:, javascript:, data:, …). Only
	// http, https, and about:blank are allowed — see checkNavigableURL.
	ErrBlockedURL = errors.New("blocked URL")

	// ErrDownloadFailed means a triggered download never completed
	// before the action deadline — the click/navigate that should have
	// started it didn't, or the browser never reported completion.
	ErrDownloadFailed = errors.New("download did not complete before deadline")

	// ErrDownloadTooLarge means a browser download exceeded the hard safety limit.
	ErrDownloadTooLarge = errors.New("browser download exceeds maximum size")
)
