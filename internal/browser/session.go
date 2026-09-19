package browser

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// NavigateResult is the outcome of a successful browser_navigate call.
type NavigateResult struct {
	URL   string
	Title string
}

// TabInfo is a point-in-time snapshot of one tracked page, returned by
// browser_tabs. Indexes identify the current point-in-time listing; closing
// a tab can shift later indexes, so callers should list again before acting
// on a stale index.
type TabInfo struct {
	Index  int
	ID     string
	Title  string
	URL    string
	Active bool
}

// maxBufferedEntries caps how many console/network entries a single
// tracked page buffers before evicting the oldest (FIFO) — bounds
// memory for a long-lived session against a chatty page, and keeps
// browser_console_logs/browser_network_requests fast to snapshot.
const maxBufferedEntries = 200

// ConsoleEntry is one captured console.* call or uncaught JS exception,
// returned by browser_console_logs. At is stamped locally (time.Now())
// when the event is received rather than taken from CDP's own
// timestamp fields, which are session-local monotonic values, not
// wall-clock.
type ConsoleEntry struct {
	Level string // "log", "debug", "info", "warning", "error", "exception", ...
	Text  string
	At    time.Time
}

// NetworkEntry is one captured request, returned by
// browser_network_requests. Status/StatusText/MIMEType stay zero until
// a response arrives; Failed/ErrorText are set instead if the request
// never got one. At is stamped locally at request-start, same reasoning
// as ConsoleEntry.At.
type NetworkEntry struct {
	RequestID  string
	Method     string
	URL        string
	Status     int
	StatusText string
	MIMEType   string
	Failed     bool
	ErrorText  string
	At         time.Time
}

// pageSession is the browser-package-internal surface Manager drives.
// *session is the real go-rod/CDP implementation; Manager's tests inject a
// fake to exercise lifecycle/serialization/error-mapping without spawning
// a real Chrome process.
type pageSession interface {
	navigate(ctx context.Context, url, waitUntil string) (NavigateResult, error)
	screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error)
	inspect(ctx context.Context, selector string) (string, error)
	click(ctx context.Context, selector string) error
	typeText(ctx context.Context, selector, text string, submit bool) error
	hotkey(ctx context.Context, keys string) error
	scroll(ctx context.Context, selector string, deltaX, deltaY float64) error
	wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error
	// snapshot atomically reads the active page and the tracked-page count
	// together (see the session.snapshot doc comment for why this can't
	// be two separate calls), then resolves the active page's current
	// URL. Used by Status, which takes no ctx of its own (a deliberate,
	// pre-existing "stays cheap" property) — same unbound-context
	// Info() call the original currentURL() made.
	snapshot() (url string, tabCount int)
	close() error
	// alive reports whether the underlying browser process is still
	// running — see Manager.ensureLocked's crash-recovery check.
	alive() bool
	// forceCleanup best-effort tears down the OS process and launcher
	// state without any CDP round-trip — used instead of close() when
	// alive() is already false, since a graceful Browser.Close() over a
	// connection to a process that's already dead has nothing to
	// negotiate with and could hang rather than fail fast.
	forceCleanup()

	// tabs snapshots every tracked page (main page plus any popup/tab
	// opened since launch), including each one's live title/URL. Read-only.
	tabs(ctx context.Context) []TabInfo
	// switchTab changes which tracked page subsequent actions act on.
	switchTab(index int) error
	// closeTab closes one tracked page by index without tearing down the
	// whole session. Refuses to close the only remaining page.
	closeTab(ctx context.Context, index int) error
	// setFiles sets a file input element's files directly via CDP.
	setFiles(ctx context.Context, selector string, paths []string) error
	// downloadViaClick/downloadViaURL trigger a download (by clicking an
	// element, or navigating directly to a downloadable URL) and block
	// until it completes, saving it under dir. The returned info's GUID
	// names the saved file within dir — see Manager.Download, which owns
	// moving it to the caller's validated destination.
	downloadViaClick(ctx context.Context, selector, dir string) (*proto.PageDownloadWillBegin, error)
	downloadViaURL(ctx context.Context, url, dir string) (*proto.PageDownloadWillBegin, error)

	// consoleLogs/networkRequests snapshot the active page's buffered
	// entries, most-recent last, capped to the last limit entries
	// (limit<=0 means the full buffer). Pure in-memory, no ctx.
	consoleLogs(limit int) []ConsoleEntry
	networkRequests(limit int) []NetworkEntry
}

// trackedPage is one page/tab the session has seen, from launch or from
// the background target-tracking goroutine below.
type trackedPage struct {
	id       proto.TargetTargetID
	page     *rod.Page
	openedAt time.Time

	// mu guards console/network — written by this page's own
	// console/network EachEvent subscriptions (attachPageCapture),
	// independent of session.mu and of every other tracked page's.
	mu      syncutil.Mutex
	console []ConsoleEntry
	network []*NetworkEntry
}

// recordConsole appends one entry, evicting the oldest if over
// maxBufferedEntries.
func (tp *trackedPage) recordConsole(e ConsoleEntry) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.console = append(tp.console, e)
	if len(tp.console) > maxBufferedEntries {
		tp.console = tp.console[len(tp.console)-maxBufferedEntries:]
	}
}

// recordRequestStart buffers a new request, evicting the oldest if over
// maxBufferedEntries.
func (tp *trackedPage) recordRequestStart(id, method, url string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.network = append(tp.network, &NetworkEntry{RequestID: id, Method: method, URL: url, At: time.Now()})
	if len(tp.network) > maxBufferedEntries {
		tp.network = tp.network[len(tp.network)-maxBufferedEntries:]
	}
}

// recordRequestUpdate applies fn to the buffered entry matching id, if
// still present — a silent no-op otherwise (the entry was evicted, or
// this event arrived for a request whose start was never seen, e.g. one
// already in flight before Network.enable armed). Never creates a new
// entry: doing so here would leave a malformed one with no Method/URL.
// The buffer is small (<=maxBufferedEntries) so a linear scan for id is
// simpler and safer than an index map eviction would have to keep
// patched.
func (tp *trackedPage) recordRequestUpdate(id string, fn func(*NetworkEntry)) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	for _, e := range tp.network {
		if e.RequestID == id {
			fn(e)
			return
		}
	}
}

func (tp *trackedPage) consoleSnapshot(limit int) []ConsoleEntry {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	entries := tp.console
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	out := make([]ConsoleEntry, len(entries))
	copy(out, entries)
	return out
}

func (tp *trackedPage) networkSnapshot(limit int) []NetworkEntry {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	entries := tp.network
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	out := make([]NetworkEntry, len(entries))
	for i, e := range entries {
		out[i] = *e
	}
	return out
}

// session is a thin wrapper over one launched *rod.Browser and the set of
// pages it has opened. It owns no launch/close lifecycle policy (lazy
// launch, closed-once guard, action serialization) — that's Manager's
// job; session only knows how to perform one already-launched browser's
// actions, including tracking which of its (possibly several) pages is
// currently active.
type session struct {
	launcher *launcher.Launcher
	browser  *rod.Browser

	// mu guards pages/active. Separate from Manager.mu: the background
	// target-tracking goroutine started in launchSession appends/removes
	// tracked pages concurrently with whatever action call Manager.mu is
	// currently serializing, so this needs its own lock rather than
	// relying on Manager's.
	mu     syncutil.Mutex
	pages  []*trackedPage
	active int
}

// newTabDetectWindow bounds how long click/type(submit=true) wait to see
// whether the action opened a new tab (target="_blank" links,
// window.open()). Named and justified the same way networkIdleWindow is:
// a fixed, documented heuristic rather than an unbounded wait. Every

// click already goes through human approval — far slower than 300ms — so
// this fixed tax is negligible in relative terms, and it's scoped to
// click/submit only: navigate/screenshot/inspect/scroll/wait/hotkey pay
// nothing extra.
const newTabDetectWindow = 2 * time.Second

// launchSession starts a headless Chromium/Chrome using the given binary
// and isolated profile dir, and opens its single initial page. bin and
// profileDir are always explicit (see findChromeBinary/newProfileDir) so
// rod never falls back to auto-downloading its own pinned build.
//
// ctx bounds only the launch itself (a binary that starts but never opens
// its debug port would otherwise hang Launch forever — see Launcher.Context).
// It is deliberately NOT threaded onto the returned Browser/pages: those
// are reused across every later action for the rest of the session, each
// of which binds its own per-call context (see every session method's
// `pg.Context(ctx)`), so tying the long-lived connection itself to one
// call's short deadline would cancel it out from under every later call.
func launchSession(ctx context.Context, bin, profileDir string) (pageSession, error) {
	return launchSessionMode(ctx, bin, profileDir, true)
}

// launchHeadedSession is launchSession with a visible window, for
// Manager.Handoff: the human completes a step (a bot-verification
// challenge) that the headless session can't show them.
func launchHeadedSession(ctx context.Context, bin, profileDir string) (pageSession, error) {
	return launchSessionMode(ctx, bin, profileDir, false)
}

func launchSessionMode(ctx context.Context, bin, profileDir string, headless bool) (pageSession, error) {
	l := hardenBrowserFlags(launcher.New()).
		Bin(bin).
		Headless(headless).
		UserDataDir(profileDir).
		Leakless(leaklessUsable()).
		Context(ctx)

	u, err := l.Launch()
	if err != nil {
		l.Cleanup()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}

	br := rod.New().ControlURL(u)
	if !headless {
		// rod emulates a fixed 1280x800 Mac-laptop device (viewport and
		// user agent) on every page it creates. That's a sensible fixed
		// viewport for a headless session, but in a visible window it pins
		// the page to the top-left corner with dead space around it and
		// ignores the window's real size. The window is for a human, so let
		// the page fill it.
		br = br.NoDefaultDevice()
	}
	if err := br.Connect(); err != nil {
		l.Kill()
		l.Cleanup()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}

	pg, err := br.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_ = br.Close()
		l.Kill()
		l.Cleanup()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}

	s := &session{launcher: l, browser: br}
	tp, _ := s.trackPage(pg.TargetID, pg)
	attachPageCapture(pg, tp)

	// Passively track every later page the browser opens (popups,
	// target="_blank" links, window.open) so click/type-submit can follow
	// one without every ordinary action paying for a blocking wait — see
	// followNewPage/newTabDetectWindow. Runs for the life of the session;
	// no explicit teardown needed, same as attachPageCapture's own
	// subscriptions (they end on their own once the browser closes) —
	// this is rod's own documented idiom for the purpose (see
	// Browser.EachEvent's doc example).
	go br.EachEvent(func(e *proto.TargetTargetCreated) {
		if e.TargetInfo.Type != "page" {
			return
		}
		newPg, err := br.PageFromTarget(e.TargetInfo.TargetID)
		if err != nil {
			return
		}
		if newTP, created := s.trackPage(e.TargetInfo.TargetID, newPg); created {
			attachPageCapture(newPg, newTP)
		}
	}, func(e *proto.TargetTargetDestroyed) {
		s.untrackPage(e.TargetID)
	})()

	return s, nil
}

// attachPageCapture wires every background behavior a tracked page
// needs for the rest of its life: auto-dismissing JS dialogs, and
// buffering its console/network activity into tp for
// browser_console_logs/browser_network_requests. Callers must only
// invoke this once per page — see trackPage's created return value —
// since each of these subscribes independently and calling it twice
// would double-dismiss dialogs and double-record every entry.
func attachPageCapture(pg *rod.Page, tp *trackedPage) {
	dismissDialogs(pg)
	startConsoleCapture(pg, tp)
	startNetworkCapture(pg, tp)
}

// dismissDialogs auto-dismisses every JS-initiated dialog
// (alert/confirm/prompt/beforeunload) on pg. A dialog blocks the page's
// own execution — and every browser_* call that touches it — until
// something answers Page.handleJavaScriptDialog. There is no human here
// to click it, so auto-dismiss every one instead of letting a tool call
// hang until its timeout. Dismiss (not accept) is the safer default: the
// triggering click/navigate was already approval-gated, but a confirm()
// can gate its own separate destructive action, and silently accepting
// that goes beyond what the click's approval covered.
func dismissDialogs(pg *rod.Page) {
	go pg.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
		_ = proto.PageHandleJavaScriptDialog{Accept: false}.Call(pg)
	})()
}

// startConsoleCapture arms the Runtime domain and records every
// console.* call and uncaught exception into tp for the rest of pg's
// life. RuntimeConsoleAPICalled.Args are rendered as a space-joined
// text line: a primitive's Value (via gson.JSON.String()) when present,
// else its Description (objects/functions), else its bare Type as a
// last resort.
func startConsoleCapture(pg *rod.Page, tp *trackedPage) {
	_ = proto.RuntimeEnable{}.Call(pg)
	go pg.EachEvent(func(e *proto.RuntimeConsoleAPICalled) {
		parts := make([]string, 0, len(e.Args))
		for _, arg := range e.Args {
			parts = append(parts, consoleArgText(arg))
		}
		tp.recordConsole(ConsoleEntry{Level: string(e.Type), Text: strings.Join(parts, " "), At: time.Now()})
	}, func(e *proto.RuntimeExceptionThrown) {
		text := ""
		if e.ExceptionDetails != nil {
			text = e.ExceptionDetails.Text
			if e.ExceptionDetails.Exception != nil {
				if d := consoleArgText(e.ExceptionDetails.Exception); d != "" {
					text = text + ": " + d
				}
			}
		}
		tp.recordConsole(ConsoleEntry{Level: "exception", Text: text, At: time.Now()})
	})()
}

// consoleArgText renders one console-call argument as text: a
// primitive's JSON value when present, else its object-form
// Description, else its bare Type as a last resort.
func consoleArgText(arg *proto.RuntimeRemoteObject) string {
	if arg == nil {
		return ""
	}
	if !arg.Value.Nil() {
		return arg.Value.String()
	}
	if arg.Description != "" {
		return arg.Description
	}
	return string(arg.Type)
}

// startNetworkCapture arms the Network domain and records every request
// pg makes into tp for the rest of its life: requestWillBeSent seeds an
// entry, responseReceived fills in its status once one arrives, and
// loadingFailed marks it failed for requests that never get a response
// (DNS/connection errors, CORS blocks, etc.).
func startNetworkCapture(pg *rod.Page, tp *trackedPage) {
	_ = proto.NetworkEnable{}.Call(pg)
	go pg.EachEvent(func(e *proto.NetworkRequestWillBeSent) {
		if e.Request == nil {
			return
		}
		tp.recordRequestStart(string(e.RequestID), e.Request.Method, e.Request.URL)
	}, func(e *proto.NetworkResponseReceived) {
		if e.Response == nil {
			return
		}
		tp.recordRequestUpdate(string(e.RequestID), func(entry *NetworkEntry) {
			entry.Status = e.Response.Status
			entry.StatusText = e.Response.StatusText
			entry.MIMEType = e.Response.MIMEType
		})
	}, func(e *proto.NetworkLoadingFailed) {
		tp.recordRequestUpdate(string(e.RequestID), func(entry *NetworkEntry) {
			entry.Failed = true
			entry.ErrorText = e.ErrorText
		})
	})()
}

// trackPage adds id/pg to the registry, or no-ops if it's already there
// (created=false) — the background target-tracking goroutine and
// followNewPage's own race-safety call can both reach the same new page,
// and only the first must win. Callers use created to decide whether to
// attachPageCapture (dialog-dismiss, console/network capture): doing that
// twice for the same page would double-subscribe and double-count every
// buffered entry.
func (s *session) trackPage(id proto.TargetTargetID, pg *rod.Page) (tp *trackedPage, created bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.pages {
		if existing.id == id {
			return existing, false
		}
	}
	tp = &trackedPage{id: id, page: pg, openedAt: time.Now()}
	s.pages = append(s.pages, tp)
	return tp, true
}

// untrackPage removes id and keeps active pointing at the same *page*
// it pointed at before, not just the same index — an index-based
// adjustment (e.g. "clamp if now out of range") gets this wrong whenever
// a page *before* the active one closes, since every later page shifts
// down by one but the index isn't a valid proxy for identity anymore.
// Re-resolving by id after removal handles that, plus the active page
// itself closing, uniformly.
func (s *session) untrackPage(id proto.TargetTargetID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var activeID proto.TargetTargetID
	if s.active < len(s.pages) {
		activeID = s.pages[s.active].id
	}
	for i, tp := range s.pages {
		if tp.id == id {
			// Keep the final page as a tombstone. Chrome can remain alive
			// after its last target is externally destroyed; removing the
			// entry would let a concurrent Status/action index an empty
			// registry between Manager's liveness check and its page lookup.
			// The retained rod page fails subsequent CDP operations normally,
			// while the next successful action or explicit close can tear the
			// session down without a host-process panic.
			if len(s.pages) == 1 {
				return
			}
			s.pages = append(s.pages[:i], s.pages[i+1:]...)
			break
		}
	}
	if len(s.pages) == 0 {
		s.active = 0
		return
	}
	if activeID == id {
		// The active page itself was just closed — fall back to the most
		// recently opened remaining page, matching what closing a tab by
		// hand usually does (focus lands on the next-most-recent one).
		s.active = len(s.pages) - 1
		return
	}
	for i, tp := range s.pages {
		if tp.id == activeID {
			s.active = i
			return
		}
	}
	// activeID vanished for some other reason (shouldn't happen) — clamp
	// defensively rather than leave an out-of-range index.
	if s.active >= len(s.pages) {
		s.active = len(s.pages) - 1
	}
}

// activeTrackedPage returns the currently active trackedPage (registry
// entry, not just its *rod.Page) — the shared lock/index lookup
// activePage and the console/network read methods below all build on.
// Callers normally have at least one tracked page: launchSession tracks
// the initial page, and untrackPage retains a tombstone when the final
// target disappears so an external close cannot create an empty slice
// between the manager's liveness check and this lookup.
func (s *session) activeTrackedPage() *trackedPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pages[s.active]
}

// activePage is a convenience wrapper over activeTrackedPage for the
// (common) case a caller only needs the *rod.Page, not the registry
// entry itself.
func (s *session) activePage() *rod.Page {
	return s.activeTrackedPage().page
}

func (s *session) consoleLogs(limit int) []ConsoleEntry {
	return s.activeTrackedPage().consoleSnapshot(limit)
}

func (s *session) networkRequests(limit int) []NetworkEntry {
	return s.activeTrackedPage().networkSnapshot(limit)
}

func (s *session) close() error {
	err := s.browser.Close()
	s.launcher.Kill()
	s.launcher.Cleanup()
	return err
}

// snapshot reads the active page reference and the tab count together
// under one lock acquisition, then resolves the active page's URL
// outside the lock. This must NOT be split into two separate calls (one
// for the page/URL, one for the count): the background target-tracking
// goroutine in launchSession mutates s.pages/s.active independently of
// any Manager-level call, so two independently-locked reads can observe
// different versions of that state — e.g. the active page has just been
// destroyed (its Info() call fails, URL comes back "") in the first
// read, while the second read already reflects the post-close tab count.
// Capturing both under one critical section removes that window; only
// the much narrower one between releasing the lock and calling Info()
// remains, which Info()'s own error handling already covers.
func (s *session) snapshot() (url string, tabCount int) {
	s.mu.Lock()

	if len(s.pages) == 0 {
		// The last page closed since the caller last checked alive() (a
		// window.close(), or the user closing the window). Report "nothing"
		// rather than indexing an empty registry.
		s.mu.Unlock()
		return "", 0
	}
	pg := s.pages[s.active].page
	tabCount = len(s.pages)
	s.mu.Unlock()

	info, err := pg.Info()
	if err != nil {
		return "", tabCount
	}
	return info.URL, tabCount
}

// alive checks whether the launched OS process is still running, via a
// zero-signal existence probe (see kill(2) — signal 0 sends nothing, it
// only reports ESRCH if the pid is gone). This is deliberately a process
// check, not a CDP round-trip: it's what lets Manager.ensureLocked notice
// a crashed browser (OOM-killed, killed externally, or just crashed)
// cheaply, before attempting to reuse a connection that's already dead.
//
// PID reuse by the OS could in principle make a long-dead process look
// alive again, but that requires the OS to cycle back to the exact same
// pid within one session's lifetime — a risk this crash check accepts as
// negligible in exchange for not needing rod's private launch-exit
// channel (not part of Launcher's public API).
func (s *session) alive() bool {
	// A browser process can remain running after its last target is closed.
	// Treat an empty page registry as a dead session so Manager discards it
	// and relaunches cleanly instead of allowing activePage/snapshot to index
	// an empty slice and panic on the next action.
	s.mu.Lock()
	hasPage := len(s.pages) > 0
	s.mu.Unlock()
	if !hasPage {
		return false
	}
	pid := s.launcher.PID()
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// forceCleanup skips the CDP-level Browser.Close() — there's no live
// connection to negotiate a graceful shutdown with — and only reaps the
// OS process and launcher state.
func (s *session) forceCleanup() {
	s.launcher.Kill()
	s.launcher.Cleanup()
}

// classifyErr wraps a raw rod/CDP error with the taxonomy sentinel that
// best describes it, so tool callers can use errors.Is regardless of the
// underlying error's concrete type. fallback is used for a plain
// context-deadline error, which carries no type information of its own.
func classifyErr(err error, fallback error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*rod.ElementNotFoundError](err); ok {
		return fmt.Errorf("%w: %v", ErrSelectorNotFound, err)
	}
	if _, ok := errors.AsType[*rod.NavigationError](err); ok {
		return fmt.Errorf("%w: %v", ErrNavigationTimeout, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", fallback, err)
	}
	return err
}

// element resolves selector against pg — always the caller's own
// activePage() snapshot, taken once at the top of the calling method, so
// a single action always acts on one consistent page even though the
// session may be tracking several.
func (s *session) element(ctx context.Context, pg *rod.Page, selector string) (*rod.Element, error) {
	el, err := pg.Context(ctx).Element(selector)
	if err != nil {
		return nil, classifyErr(err, ErrSelectorNotFound)
	}
	return el, nil
}

// networkIdleWindow is how long the network must stay quiet before
// "networkidle" is considered satisfied — a fixed heuristic quiet-period
// (the same order of magnitude Playwright's own networkidle uses), not an
// overall deadline. The overall deadline for the call is whatever ctx
// already carries (see Manager.withActionTimeout / Manager.Wait).
const networkIdleWindow = 500 * time.Millisecond

func waitUntilCondition(pg *rod.Page, waitUntil string) error {
	switch strings.ToLower(strings.TrimSpace(waitUntil)) {
	case "networkidle":
		return pg.WaitIdle(networkIdleWindow)
	case "domcontentloaded":
		return pg.WaitDOMStable(300*time.Millisecond, 0)
	default: // "", "load"
		return pg.WaitLoad()
	}
}

func (s *session) navigate(ctx context.Context, url, waitUntil string) (NavigateResult, error) {
	pg := s.activePage().Context(ctx)
	if err := pg.Navigate(url); err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	if err := waitUntilCondition(pg, waitUntil); err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	info, err := pg.Info()
	if err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	return NavigateResult{URL: info.URL, Title: info.Title}, nil
}

func (s *session) screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error) {
	activePg := s.activePage()
	pg := activePg.Context(ctx)
	if strings.TrimSpace(selector) != "" {
		el, err := s.element(ctx, activePg, selector)
		if err != nil {
			return nil, err
		}
		data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
		if err != nil {
			return nil, fmt.Errorf("browser screenshot: %w", err)
		}
		return data, nil
	}
	data, err := pg.Screenshot(fullPage, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return nil, fmt.Errorf("browser screenshot: %w", err)
	}
	return data, nil
}

func (s *session) inspect(ctx context.Context, selector string) (string, error) {
	activePg := s.activePage()
	pg := activePg.Context(ctx)
	_ = proto.AccessibilityEnable{}.Call(pg)

	if strings.TrimSpace(selector) == "" {
		res, err := proto.AccessibilityGetFullAXTree{}.Call(pg)
		if err != nil {
			return "", fmt.Errorf("browser inspect: %w", err)
		}
		return renderAXTree(res.Nodes), nil
	}

	el, err := s.element(ctx, activePg, selector)
	if err != nil {
		return "", err
	}
	res, err := proto.AccessibilityGetPartialAXTree{ObjectID: el.Object.ObjectID, FetchRelatives: true}.Call(pg)
	if err != nil {
		return "", fmt.Errorf("browser inspect: %w", err)
	}
	return renderAXTree(res.Nodes), nil
}

// followNewPage runs action, then — if it caused fromPage to open a new
// page within newTabDetectWindow — switches the session's active page to
// it. Uses go-rod's own Page.WaitOpen() idiom: the wait must be armed
// BEFORE action runs, to avoid missing a TargetCreated event that fires
// during it rather than after. When nothing opens (the overwhelming
// majority of clicks/submits), wait() simply returns once detectCtx
// expires and this is a no-op.
func (s *session) followNewPage(ctx context.Context, fromPage *rod.Page, action func() error) error {
	detectCtx, cancel := context.WithTimeout(ctx, newTabDetectWindow)
	defer cancel()
	wait := fromPage.Context(detectCtx).WaitOpen()

	if err := action(); err != nil {
		return err
	}

	newPage, err := wait()
	if err != nil || newPage == nil {
		return nil
	}
	// Ensure the new page is registered (and, if this call is the one that
	// actually wins that race, that it gets dialog-dismiss/console/network
	// capture attached) before switching to it — the background
	// target-tracking goroutine in launchSession subscribes to the same
	// TargetTargetCreated event independently, and there's no ordering
	// guarantee between the two subscriptions, so this call's own wait()
	// can resolve before that goroutine has appended the page.
	if tp, created := s.trackPage(newPage.TargetID, newPage); created {
		attachPageCapture(newPage, tp)
	}
	s.mu.Lock()
	for i, tp := range s.pages {
		if tp.id == newPage.TargetID {
			s.active = i
			break
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *session) click(ctx context.Context, selector string) error {
	pg := s.activePage()
	el, err := s.element(ctx, pg, selector)
	if err != nil {
		return err
	}
	if _, err := el.WaitInteractable(); err != nil {
		return classifyErr(err, ErrSelectorNotFound)
	}
	return s.followNewPage(ctx, pg, func() error {
		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return fmt.Errorf("browser click: %w", err)
		}
		return nil
	})
}

func (s *session) typeText(ctx context.Context, selector, text string, submit bool) error {
	pg := s.activePage()
	el, err := s.element(ctx, pg, selector)
	if err != nil {
		return err
	}
	if _, err := el.WaitInteractable(); err != nil {
		return classifyErr(err, ErrSelectorNotFound)
	}
	if err := el.Input(text); err != nil {
		return fmt.Errorf("browser type: %w", err)
	}
	if !submit {
		return nil
	}
	return s.followNewPage(ctx, pg, func() error {
		if err := el.Type(input.Enter); err != nil {
			return fmt.Errorf("browser type: submit: %w", err)
		}
		return nil
	})
}

func (s *session) hotkey(ctx context.Context, keys string) error {
	combo, err := parseHotkey(keys)
	if err != nil {
		return err
	}
	ka := s.activePage().Context(ctx).KeyActions()
	for _, k := range combo.modifiers {
		ka = ka.Press(k)
	}
	ka = ka.Type(combo.main)
	for i := len(combo.modifiers) - 1; i >= 0; i-- {
		ka = ka.Release(combo.modifiers[i])
	}
	if err := ka.Do(); err != nil {
		return fmt.Errorf("browser hotkey %q: %w", keys, err)
	}
	return nil
}

func (s *session) scroll(ctx context.Context, selector string, deltaX, deltaY float64) error {
	activePg := s.activePage()
	pg := activePg.Context(ctx)
	if strings.TrimSpace(selector) != "" {
		el, err := s.element(ctx, activePg, selector)
		if err != nil {
			return err
		}
		if err := el.ScrollIntoView(); err != nil {
			return fmt.Errorf("browser scroll: %w", err)
		}
		return nil
	}

	metrics, err := proto.PageGetLayoutMetrics{}.Call(pg)
	if err != nil {
		return fmt.Errorf("browser scroll: %w", err)
	}
	var cx, cy float64
	if metrics.CSSLayoutViewport != nil {
		cx = float64(metrics.CSSLayoutViewport.ClientWidth) / 2
		cy = float64(metrics.CSSLayoutViewport.ClientHeight) / 2
	}
	if err := pg.Mouse.MoveTo(proto.NewPoint(cx, cy)); err != nil {
		return fmt.Errorf("browser scroll: %w", err)
	}
	if err := pg.Mouse.Scroll(deltaX, deltaY, 1); err != nil {
		return fmt.Errorf("browser scroll: %w", err)
	}
	return nil
}

// wait assumes the caller (Manager.Wait) has already resolved timeout to
// a positive value — the "built-in timeout" the tool docs promise, not
// left unbounded.
func (s *session) wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pg := s.activePage().Context(ctx)

	if networkIdle {
		if err := pg.WaitIdle(networkIdleWindow); err != nil {
			return classifyErr(err, ErrNavigationTimeout)
		}
	}
	if strings.TrimSpace(selector) != "" {
		el, err := pg.Element(selector)
		if err != nil {
			return classifyErr(err, ErrSelectorNotFound)
		}
		if err := el.WaitVisible(); err != nil {
			return classifyErr(err, ErrSelectorNotFound)
		}
	}
	if strings.TrimSpace(text) != "" {
		if _, err := pg.ElementR("*", regexp.QuoteMeta(text)); err != nil {
			return classifyErr(err, ErrSelectorNotFound)
		}
	}
	return nil
}

func (s *session) tabs(ctx context.Context) []TabInfo {
	s.mu.Lock()
	snapshot := make([]*trackedPage, len(s.pages))
	copy(snapshot, s.pages)
	activeIdx := s.active
	s.mu.Unlock()

	out := make([]TabInfo, 0, len(snapshot))
	for i, tp := range snapshot {
		var title, url string
		if info, err := tp.page.Context(ctx).Info(); err == nil {
			title, url = info.Title, info.URL
		}
		out = append(out, TabInfo{Index: i, ID: string(tp.id), Title: title, URL: url, Active: i == activeIdx})
	}
	return out
}

func (s *session) switchTab(index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.pages) {
		return fmt.Errorf("%w: index %d, have %d tab(s)", ErrTabNotFound, index, len(s.pages))
	}
	s.active = index
	return nil
}

// closeTab closes one tracked page by index. pg.Close() triggers the
// same CDP target-destroyed path the background listener already
// handles for externally-closed tabs (e.g. a popup's own
// window.close()), but this also untracks synchronously afterward so
// the tool call's own result reflects the change immediately rather
// than waiting on that async event — untrackPage is idempotent, so the
// listener's own later (redundant) call is a harmless no-op.
func (s *session) closeTab(ctx context.Context, index int) error {
	s.mu.Lock()
	if index < 0 || index >= len(s.pages) {
		s.mu.Unlock()
		return fmt.Errorf("%w: index %d, have %d tab(s)", ErrTabNotFound, index, len(s.pages))
	}
	if len(s.pages) == 1 {
		s.mu.Unlock()
		return errors.New("cannot close the only remaining tab; use browser_close to end the session instead")
	}
	id := s.pages[index].id
	pg := s.pages[index].page
	s.mu.Unlock()

	if err := pg.Context(ctx).Close(); err != nil {
		return fmt.Errorf("browser close tab: %w", err)
	}
	s.untrackPage(id)
	return nil
}

// setFiles sets a file input element's files directly via CDP
// (DOM.setFileInputFiles). Unlike click/typeText this needs no
// WaitInteractable precondition: it targets the DOM node directly rather
// than synthesizing mouse/keyboard input, so it works on the common
// display:none file input pattern too.
func (s *session) setFiles(ctx context.Context, selector string, paths []string) error {
	activePg := s.activePage()
	el, err := s.element(ctx, activePg, selector)
	if err != nil {
		return err
	}
	if err := el.SetFiles(paths); err != nil {
		return fmt.Errorf("browser upload: %w", err)
	}
	return nil
}

func (s *session) downloadViaClick(ctx context.Context, selector, dir string) (*proto.PageDownloadWillBegin, error) {
	activePg := s.activePage()
	el, err := s.element(ctx, activePg, selector)
	if err != nil {
		return nil, err
	}
	if _, err := el.WaitInteractable(); err != nil {
		return nil, classifyErr(err, ErrSelectorNotFound)
	}
	wait := s.browser.Context(ctx).WaitDownload(dir)
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return nil, fmt.Errorf("browser download: click: %w", err)
	}
	info := wait()
	if info == nil {
		return nil, classifyErr(ctx.Err(), ErrDownloadFailed)
	}
	return info, nil
}

func (s *session) downloadViaURL(ctx context.Context, url, dir string) (*proto.PageDownloadWillBegin, error) {
	wait := s.browser.Context(ctx).WaitDownload(dir)
	// A download response legitimately aborts the page's own navigation
	// (the response never becomes a loaded document), so the navigate
	// error is deliberately not treated as fatal here — the point of this
	// call is the download event, not a loaded page.
	_ = s.activePage().Context(ctx).Navigate(url)
	info := wait()
	if info == nil {
		return nil, classifyErr(ctx.Err(), ErrDownloadFailed)
	}
	return info, nil
}
