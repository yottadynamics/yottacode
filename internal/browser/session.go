package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

type NavigateResult struct{ URL, Title string }
type TabInfo struct {
	Index  int
	ID     string
	Title  string
	URL    string
	Active bool
}

const maxBufferedEntries = 200

// maxEntryChars bounds one buffered console message or request URL, so a page
// that logs a megabyte per line can't flood the agent's context or our memory.
const maxEntryChars = 2000

// MaxTabs caps how many tabs a session tracks. A popup bomb past it is closed
// as soon as Chrome reports it.
const MaxTabs = 8

func capText(s string) string {
	r := []rune(s)
	if len(r) <= maxEntryChars {
		return s
	}
	return string(r[:maxEntryChars]) + "…[truncated]"
}

type ConsoleEntry struct {
	Level, Text string
	At          time.Time
}
type NetworkEntry struct {
	RequestID, Method, URL string
	// Type is the resource type Chrome reports (Document, XHR, WebSocket, …).
	Type                 string
	Status               int
	StatusText, MIMEType string
	Failed               bool
	ErrorText            string
	At                   time.Time
}

type pageSession interface {
	navigate(context.Context, string, string) (NavigateResult, error)
	screenshot(context.Context, string, bool) ([]byte, error)
	inspect(context.Context, string) (string, error)
	click(context.Context, string) error
	typeText(context.Context, string, string, bool) error
	hotkey(context.Context, string) error
	scroll(context.Context, string, float64, float64) error
	wait(context.Context, string, string, bool, time.Duration) error
	snapshot() (string, int)
	close() error
	alive() bool
	forceCleanup()
	tabs(context.Context) []TabInfo
	switchTab(int) error
	closeTab(context.Context, int) error
	setFiles(context.Context, string, []string) error
	downloadViaClick(context.Context, string, string) (*browser.EventDownloadWillBegin, error)
	downloadViaURL(context.Context, string, string) (*browser.EventDownloadWillBegin, error)
	consoleLogs(int) []ConsoleEntry
	networkRequests(int) []NetworkEntry
	selectOption(ctx context.Context, selector, value, label string) (string, error)
	responseBody(ctx context.Context, requestID string, maxBytes, offset int) (ResponseBody, error)
}

type trackedPage struct {
	id       target.ID
	ctx      context.Context
	cancel   context.CancelFunc
	openedAt time.Time
	url      string
	mu       syncutil.Mutex
	console  []ConsoleEntry
	network  []*NetworkEntry
	// lastBlock is the most recent policy refusal, so a navigation that fails
	// with ERR_BLOCKED_BY_CLIENT can say why.
	lastBlock error
}

func (p *trackedPage) recordConsole(e ConsoleEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.Text = capText(e.Text)
	p.console = append(p.console, e)
	if len(p.console) > maxBufferedEntries {
		p.console = p.console[len(p.console)-maxBufferedEntries:]
	}
}
func (p *trackedPage) recordRequestStart(id, method, url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.network = append(p.network, &NetworkEntry{RequestID: id, Method: method, URL: capText(url), At: time.Now()})
	if len(p.network) > maxBufferedEntries {
		p.network = p.network[len(p.network)-maxBufferedEntries:]
	}
}
func (p *trackedPage) recordRequestUpdate(id string, fn func(*NetworkEntry)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// The most recent hop: redirects reuse the request id.
	for i := len(p.network) - 1; i >= 0; i-- {
		if e := p.network[i]; e.RequestID == id {
			fn(e)
			return
		}
	}
}
func (p *trackedPage) consoleSnapshot(n int) []ConsoleEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.console
	if n > 0 && len(a) > n {
		a = a[len(a)-n:]
	}
	return append([]ConsoleEntry(nil), a...)
}
func (p *trackedPage) networkSnapshot(n int) []NetworkEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.network
	if n > 0 && len(a) > n {
		a = a[len(a)-n:]
	}
	o := make([]NetworkEntry, len(a))
	for i, e := range a {
		o[i] = *e
	}
	return o
}

// recordBlocked notes a policy refusal in the console buffer (so the agent can
// see it via browser_console_logs) and remembers it for the failing action.
func (p *trackedPage) recordBlocked(reqURL string, err error) {
	p.mu.Lock()
	p.lastBlock = err
	p.mu.Unlock()
	p.recordConsole(ConsoleEntry{Level: "blocked", Text: clip(reqURL) + ": " + err.Error(), At: time.Now()})
}
func (p *trackedPage) takeBlock() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.lastBlock
	p.lastBlock = nil
	return e
}
func (p *trackedPage) setURL(u string) {
	p.mu.Lock()
	p.url = u
	p.mu.Unlock()
}
func (p *trackedPage) getURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url
}

type session struct {
	allocCtx        context.Context
	cancel          context.CancelFunc
	browserCtx      context.Context
	browserCancel   context.CancelFunc
	browser         *chromedp.Browser
	mu              syncutil.Mutex
	pages           []*trackedPage
	active          int
	pid             int
	downloadMu      syncutil.Mutex
	downloads       map[string]*downloadWaiter
	pendingDownload *downloadWaiter
	// policy vets every request any tab makes (see netpolicy.go).
	policy *netPolicy
	// guard enforces policy on every target, including out-of-process iframes.
	guard *guard
	// tabsInFlight counts popups being set up, so the tab cap holds under a burst.
	tabsInFlight int
	// destroyed tracks popup targets that disappeared while setup was still running.
	destroyed map[target.ID]bool
	// pendingBlocks holds refusals for pages not tracked yet; trackPage drains them.
	pendingBlocks map[target.ID][]blockNote
	// refs maps browser_inspect element refs to DOM nodes (see selector.go).
	refs *refTable
}

// maxPendingBlockTargets bounds refusals held for pages not yet tracked.
const maxPendingBlockTargets = 32

// pendingBlockTTL is how long a refusal waits for its page to be tracked.
const pendingBlockTTL = 30 * time.Second

type blockNote struct {
	url string
	err error
	at  time.Time
}

type downloadWaiter struct {
	begun    chan *browser.EventDownloadWillBegin
	done     chan struct{}
	failed   chan error
	guid     string
	filePath string
	maxBytes int64
}

const newTabDetectWindow = 2 * time.Second

func launchSession(ctx context.Context, bin, profile string) (pageSession, error) {
	return launchSessionMode(ctx, bin, profile, true)
}
func launchHeadedSession(ctx context.Context, bin, profile string) (pageSession, error) {
	return launchSessionMode(ctx, bin, profile, false)
}
func launchSessionMode(ctx context.Context, bin, profile string, headless bool) (pageSession, error) {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(bin), chromedp.UserDataDir(profile), chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-features", "TranslateUI"),
		// Chrome ships several built-in component extensions (PDF viewer
		// helpers, Cast, etc.) that lazily spin up their own background
		// service workers a second or two after launch on a fresh profile.
		// That startup work runs on the browser process's own threads and
		// competes for CPU with the page's renderer; under load (a busy CI
		// runner) it can starve a freshly launched page's JS for long enough
		// that a script-driven action (e.g. a click handler) appears to hang
		// — reproduced locally: a chrome-extension:// service worker spun up
		// ~1.9s after a click, and the click's handler didn't run until
		// after it finished. None of this exists for real users automation
		// controls, so disable it outright rather than working around its
		// timing.
		chromedp.Flag("disable-component-extensions-with-background-pages", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("metrics-recording-only", true),
	}
	opts = append(opts, stealthLaunchOptions(ctx, bin, headless)...)
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	bc, browserCancel := chromedp.NewContext(allocCtx)
	// Keep the long-lived browser context alive after startup. A child context
	// used only for readiness would cancel the browser target when it returns.
	ready := make(chan error, 1)
	go func() { ready <- chromedp.Run(bc) }()
	startupTimer := time.NewTimer(30 * time.Second)
	defer startupTimer.Stop()
	select {
	case err := <-ready:
		if err != nil {
			browserCancel()
			cancel()
			return nil, fmt.Errorf("%w: %v%s", ErrLaunchFailed, err, launchHint())
		}
	case <-ctx.Done():
		browserCancel()
		cancel()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, ctx.Err())
	case <-startupTimer.C:
		browserCancel()
		cancel()
		return nil, fmt.Errorf("%w: browser startup timed out%s", ErrLaunchFailed, launchHint())
	}
	s := &session{allocCtx: allocCtx, cancel: cancel, browserCtx: bc, browserCancel: browserCancel, browser: chromedp.FromContext(bc).Browser, downloads: make(map[string]*downloadWaiter), policy: newNetPolicy(), destroyed: make(map[target.ID]bool)}
	if s.browser != nil && s.browser.Process() != nil {
		s.pid = s.browser.Process().Pid
	}
	// The guard must be up before any page is used: it pauses new targets until
	// request interception is installed on them. No guard, no session.
	g, gerr := startGuard(ctx, profile, s.policy, s.onGuardBlock, s.guardLost)
	if gerr != nil {
		browserCancel()
		cancel()
		return nil, fmt.Errorf("%w: network guard failed to start: %v", ErrLaunchFailed, gerr)
	}
	s.guard = g
	// Read once per session; applied to every page (initial and later
	// targets) below. Missing (ok=false) just means running with only the
	// launch flags, which is still better than chromedp's defaults.
	stealth, haveStealth := stealthInit(bc, s.browser)
	pc, pcancel := chromedp.NewContext(bc)
	if err := chromedp.Run(pc, chromedp.Navigate("about:blank")); err != nil {
		g.shutdown()
		cancel()
		pcancel()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}
	// The initial target is created outside a browser event callback. Run
	// its ready actions (domain-enable + stealth) synchronously, before
	// publishing it via track, so callers such as handoff — or, for later
	// targets, followNewPage — can't reach the page before it's attach-safe.
	readyCtx, readyCancel := context.WithTimeout(pc, 5*time.Second)
	if err := chromedp.Run(readyCtx, pageReadyActions(stealth, haveStealth)...); err != nil {
		readyCancel()
		g.shutdown()
		cancel()
		pcancel()
		return nil, fmt.Errorf("%w: page readiness setup failed: %v", ErrLaunchFailed, err)
	}
	readyCancel()
	id := chromedp.FromContext(pc).Target.TargetID
	if !headless {
		// --start-maximized's actual resize is an OS window-manager
		// operation, asynchronous relative to Chrome's own page-load
		// lifecycle: "load" (just above) can fire before it's applied, so an
		// action immediately following launch (e.g. handoff's own first
		// type/click) can see a 0x0 viewport and a bogus element rect.
		waitMaximizedViewport(pc, 2*time.Second)
	}
	tp := s.track(id, pc, pcancel)
	attachPageCaptureListeners(pc, tp)
	chromedp.ListenBrowser(bc, func(ev any) {
		s.dispatchBrowserEvent(ev)
		switch e := ev.(type) {
		case *target.EventTargetCreated:
			if e.TargetInfo == nil || e.TargetInfo.Type != "page" {
				return
			}
			if _, ok := s.pageByID(e.TargetInfo.TargetID); ok {
				return
			}
			id, url := e.TargetInfo.TargetID, e.TargetInfo.URL
			// Reserve a slot now, before the setup goroutine runs: pages are only
			// counted once tracked, so a burst of window.open calls would all
			// see the same low count and slip past the cap.
			if !s.reserveTab() {
				s.mu.Lock()
				delete(s.pendingBlocks, id)
				s.mu.Unlock()
				go func() {
					_ = target.CloseTarget(id).Do(cdp.WithExecutor(bc, s.browser))
				}()
				if ap := s.activeTrackedPage(); ap != nil {
					ap.recordConsole(ConsoleEntry{Level: "blocked", Text: fmt.Sprintf("tab limit reached (%d): closed a new tab opened by the page (%s)", MaxTabs, clip(url)), At: time.Now()})
				}
				return
			}
			ctx, cancel := chromedp.NewContext(bc, chromedp.WithTargetID(id))
			// Off the event-dispatch goroutine, same as the dialog
			// auto-dismiss inside attachPageCaptureListeners: chromedp's
			// browser dispatcher holds its execution lock while invoking
			// listeners, so a blocking chromedp.Run here would deadlock
			// target-created events. The page is not published (trackPage)
			// until pageReadyActions completes — publishing first would let
			// a concurrent caller (e.g. followNewPage, polling pageCount())
			// reach this context and issue its own first Run before the
			// attach-triggering one has finished (see pageReadyActions).
			go func() {
				defer s.releaseTab()
				if err := chromedp.Run(ctx, pageReadyActions(stealth, haveStealth)...); err != nil {
					cancel()
					return
				}
				s.mu.Lock()
				gone := s.destroyed[id]
				delete(s.destroyed, id)
				s.mu.Unlock()
				if gone {
					cancel()
					return
				}
				p, created := s.trackPage(id, ctx)
				p.setURL(url)
				if created {
					attachPageCaptureListeners(ctx, p)
				} else {
					cancel()
				}
			}()
		case *target.EventTargetDestroyed:
			s.mu.Lock()
			s.destroyed[e.TargetID] = true
			s.mu.Unlock()
			s.untrackPage(e.TargetID)
		}
	})
	return s, nil
}
func (s *session) dispatchBrowserEvent(ev any) {
	s.downloadMu.Lock()
	defer s.downloadMu.Unlock()
	switch e := ev.(type) {
	case *browser.EventDownloadWillBegin:
		if s.pendingDownload != nil {
			w := s.pendingDownload
			s.pendingDownload = nil
			w.guid = e.GUID
			s.downloads[e.GUID] = w
			select {
			case w.begun <- e:
			default:
			}
		}
	case *browser.EventDownloadProgress:
		if w := s.downloads[e.GUID]; w != nil && (e.TotalBytes > float64(MaxDownloadBytes) || e.ReceivedBytes > float64(MaxDownloadBytes)) {
			select {
			case w.failed <- fmt.Errorf("%w: received %.0f bytes, limit is %d", ErrDownloadTooLarge, e.ReceivedBytes, MaxDownloadBytes):
			default:
			}
			delete(s.downloads, e.GUID)
			_ = browser.CancelDownload(e.GUID).Do(context.Background())
			if e.FilePath != "" {
				_ = os.Remove(e.FilePath)
			}
			return
		}
		if e.State == browser.DownloadProgressStateCompleted {
			if w := s.downloads[e.GUID]; w != nil {
				close(w.done)
				delete(s.downloads, e.GUID)
			}
		}
	}
}
func (s *session) pageByID(id target.ID) (*trackedPage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pages {
		if p.id == id {
			return p, true
		}
	}
	return nil, false
}
func (s *session) track(id target.ID, ctx context.Context, cancel context.CancelFunc) *trackedPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pages {
		if p.id == id {
			return p
		}
	}
	p := &trackedPage{id: id, ctx: ctx, cancel: cancel, openedAt: time.Now()}
	s.pages = append(s.pages, p)
	return p
}
func (s *session) trackPage(id target.ID, ctx context.Context) (*trackedPage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pages {
		if p.id == id {
			return p, false
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pageCtx, cancel := context.WithCancel(ctx)
	p := &trackedPage{id: id, ctx: pageCtx, cancel: cancel, openedAt: time.Now()}
	s.pages = append(s.pages, p)
	for _, n := range s.pendingBlocks[id] {
		p.recordBlocked(n.url, n.err)
	}
	delete(s.pendingBlocks, id)
	return p, true
}

func (s *session) untrackPage(id target.ID) {
	s.mu.Lock()
	delete(s.pendingBlocks, id)
	defer s.mu.Unlock()
	for i, p := range s.pages {
		if p.id != id {
			continue
		}
		if len(s.pages) == 1 {
			return
		}
		wasActive := i == s.active
		s.pages = append(s.pages[:i], s.pages[i+1:]...)
		if wasActive {
			s.active = len(s.pages) - 1
		} else if i < s.active {
			s.active--
		}
		return
	}
}
func (s *session) activeTrackedPage() *trackedPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pages) == 0 {
		return nil
	}
	if s.active < 0 || s.active >= len(s.pages) {
		s.active = len(s.pages) - 1
	}
	return s.pages[s.active]
}
func (s *session) activePage() *trackedPage { return s.activeTrackedPage() }
func (s *session) untrack(id target.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.pages {
		if p.id == id && len(s.pages) > 1 {
			s.pages = append(s.pages[:i], s.pages[i+1:]...)
			if s.active >= len(s.pages) {
				s.active = len(s.pages) - 1
			}
			return
		}
	}
}

// pageReadyActions are the CDP actions that must run, in one chromedp.Run
// call, before anything else touches a freshly created target context:
// chromedp.Context lazily attaches to a new target on its first Run, and
// that attach is not safe to race — a second Run issued concurrently by
// anyone else who can already reach this context (another setup step, or a
// caller like followNewPage once the page is published) corrupts chromedp's
// internal bookkeeping, caught by go test -race. Callers must finish this
// Run before publishing the page (e.g. via trackPage) to anything that might
// act on it independently.
func pageReadyActions(stealth stealthProfile, haveStealth bool) []chromedp.Action {
	actions := []chromedp.Action{runtime.Enable(), network.Enable(), page.Enable()}
	if haveStealth {
		actions = append(actions, stealth.actions()...)
	}
	return actions
}

// attachPageCaptureListeners registers the per-page CDP event listeners:
// console/network capture and JS-dialog auto-dismiss. It does not itself
// call chromedp.Run, so — unlike pageReadyActions — it is safe to call any
// time, including concurrently with other activity on the same context.
func attachPageCaptureListeners(ctx context.Context, p *trackedPage) {
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventJavascriptDialogOpening:
			// Dialogs have no browser_* interaction surface, so a policy
			// answers them: alert and beforeunload are accepted (they only
			// inform, and refusing beforeunload would block navigation);
			// confirm and prompt are cancelled, because accepting would
			// answer a question on the user's behalf. Every dialog is
			// recorded in the console buffer so the agent can see it.
			accept := e.Type == page.DialogTypeAlert || e.Type == page.DialogTypeBeforeunload
			verb := "cancelled"
			if accept {
				verb = "accepted"
			}
			p.recordConsole(ConsoleEntry{Level: "dialog", Text: fmt.Sprintf("%s %s automatically: %s", e.Type, verb, e.Message), At: time.Now()})
			go func() { _ = chromedp.Run(ctx, page.HandleJavaScriptDialog(accept)) }()
		case *runtime.EventConsoleAPICalled:
			var b strings.Builder
			for _, a := range e.Args {
				if a.Value != nil {
					b.WriteString(a.Value.String())
				} else {
					b.WriteString(a.Description)
				}
			}
			p.recordConsole(ConsoleEntry{Level: string(e.Type), Text: b.String(), At: time.Now()})
		case *runtime.EventExceptionThrown:
			text := e.ExceptionDetails.Text
			if e.ExceptionDetails.Exception != nil && e.ExceptionDetails.Exception.Description != "" {
				if text != "" {
					text += ": "
				}
				text += e.ExceptionDetails.Exception.Description
			}
			p.recordConsole(ConsoleEntry{Level: "exception", Text: text, At: time.Now()})
		case *network.EventRequestWillBeSent:
			if e.Request != nil {
				if e.RedirectResponse != nil {
					// Chrome reuses the request id across a redirect chain: close
					// out the previous hop before the next one starts.
					p.recordRequestUpdate(string(e.RequestID), func(n *NetworkEntry) {
						n.Status = int(e.RedirectResponse.Status)
						n.StatusText = e.RedirectResponse.StatusText
					})
				}
				p.recordRequestStart(string(e.RequestID), e.Request.Method, e.Request.URL)
				p.recordRequestUpdate(string(e.RequestID), func(n *NetworkEntry) { n.Type = string(e.Type) })
			}
		case *network.EventResponseReceived:
			if e.Response != nil {
				p.recordRequestUpdate(string(e.RequestID), func(n *NetworkEntry) {
					n.Status = int(e.Response.Status)
					n.StatusText = e.Response.StatusText
					n.MIMEType = e.Response.MimeType
				})
			}
		case *network.EventLoadingFailed:
			p.recordRequestUpdate(string(e.RequestID), func(n *NetworkEntry) { n.Failed = true; n.ErrorText = e.ErrorText })
		}
	})
}

// actionContext retains the target context while propagating the caller's
// cancellation and deadline to chromedp actions.
func actionContext(targetCtx, callCtx context.Context) (context.Context, context.CancelFunc) {
	if callCtx == nil {
		return context.WithCancel(targetCtx)
	}
	ctx, cancel := context.WithCancel(targetCtx)
	go func() {
		select {
		case <-callCtx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
func (s *session) consoleLogs(n int) []ConsoleEntry     { return s.activePage().consoleSnapshot(n) }
func (s *session) networkRequests(n int) []NetworkEntry { return s.activePage().networkSnapshot(n) }

// onGuardBlock records a request the guard refused against the page it came
// from (or the active page, for a frame or worker target).
func (s *session) onGuardBlock(targetID, targetType, reqURL string, err error) {
	if p, ok := s.pageByID(target.ID(targetID)); ok {
		p.recordBlocked(reqURL, err)
		return
	}
	if targetType == "page" {
		// A page we have not finished tracking yet (a popup still being set
		// up): hold the note until it appears so it lands on the right tab.
		s.mu.Lock()
		if s.pendingBlocks == nil {
			s.pendingBlocks = map[target.ID][]blockNote{}
		}
		// Bound the held notes without disturbing those already kept: a new
		// target is dropped once the table is full, an existing one once its
		// own list is.
		now := time.Now()
		for id, notes := range s.pendingBlocks { // forget targets that never showed up
			if len(notes) == 0 || now.Sub(notes[len(notes)-1].at) > pendingBlockTTL {
				delete(s.pendingBlocks, id)
			}
		}
		known := s.pendingBlocks[target.ID(targetID)]
		if (known == nil && len(s.pendingBlocks) >= maxPendingBlockTargets) || len(known) >= maxBufferedEntries {
			s.mu.Unlock()
			return
		}
		s.pendingBlocks[target.ID(targetID)] = append(s.pendingBlocks[target.ID(targetID)], blockNote{reqURL, err, now})
		s.mu.Unlock()
		return
	}
	if p := s.activeTrackedPage(); p != nil {
		p.recordBlocked(reqURL, err)
	}
}

// guardLost stops the browser at once: with the guard gone nothing is vetting
// requests, so pages must not keep running until the next action notices.
func (s *session) guardLost() {
	s.browserCancel()
	s.cancel()
}
func (s *session) close() error {
	if s.guard != nil {
		s.guard.shutdown()
	}
	err := chromedp.Cancel(s.browserCtx)
	s.browserCancel()
	s.cancel()
	return err
}
func (s *session) forceCleanup() {
	if s.guard != nil {
		s.guard.shutdown()
	}
	s.browserCancel()
	s.cancel()
}
func (s *session) alive() bool {
	if s.browser == nil || s.browser.Process() == nil {
		return false
	}
	if s.guard != nil && !s.guard.alive() {
		// Without the guard the network policy is not being enforced; treat the
		// session as dead so the next action relaunches a guarded one.
		return false
	}
	return s.browser.Process().Signal(syscall.Signal(0)) == nil
}
func (s *session) snapshot() (string, int) {
	p := s.activeTrackedPage()
	if p == nil {
		return "", 0
	}
	s.mu.Lock()
	n := len(s.pages)
	s.mu.Unlock()
	return p.getURL(), n
}
func classifyErr(err, fallback error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", fallback, err)
	}
	return err
}

func classifySelectorErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(message, "node with given id") || strings.Contains(message, "no node") || strings.Contains(message, "waiting for element") || strings.Contains(message, "could not find") {
		return fmt.Errorf("%w: %v", ErrSelectorNotFound, err)
	}
	return err
}

func classifyDownloadErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	return err
}
func (s *session) navigate(ctx context.Context, u, w string) (NavigateResult, error) {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	s.policy.allowExplicit(u)
	p.takeBlock()
	s.mu.Lock()
	s.refs = nil
	s.mu.Unlock()
	if err := chromedp.Run(c, chromedp.Navigate(u)); err != nil {
		return NavigateResult{}, s.blockedOr(p, classifyErr(err, ErrNavigationTimeout))
	}
	switch strings.ToLower(w) {
	case "domcontentloaded":
		if err := chromedp.Run(c, chromedp.WaitReady("html")); err != nil {
			return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
		}
	case "networkidle":
		if err := s.waitNetworkIdle(c, p, 500*time.Millisecond); err != nil {
			return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
		}
	}
	var url, title string
	if err := chromedp.Run(c, chromedp.Location(&url), chromedp.Title(&title)); err != nil {
		return NavigateResult{}, err
	}
	p.setURL(url)
	return NavigateResult{url, title}, nil
}

// blockedOr prefers the network policy's explanation over chromedp's opaque
// "net::ERR_BLOCKED_BY_CLIENT" when a request was refused during the action.
func (s *session) blockedOr(p *trackedPage, err error) error {
	if b := p.takeBlock(); b != nil && err != nil && strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT") {
		return b
	}
	return err
}

// networkIdleCap bounds how long waitNetworkIdle waits for a page that never
// goes quiet (long-polling, streaming); it then proceeds rather than failing.
const networkIdleCap = 10 * time.Second

// waitNetworkIdle returns once the document has loaded and no request has been
// in flight for the quiet period. A request still unanswered after
// staleRequestAge (a hung or streaming connection) stops counting, so one
// never-ending request cannot hold the page "busy" forever.
func (s *session) waitNetworkIdle(ctx context.Context, p *trackedPage, quiet time.Duration) error {
	deadline := time.NewTimer(networkIdleCap)
	defer deadline.Stop()
	var quietSince time.Time
	for {
		var state string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.readyState`, &state)); err != nil {
			return err
		}
		if state == "complete" && p.pendingRequests(staleRequestAge) == 0 {
			if quietSince.IsZero() {
				quietSince = time.Now()
			}
			if time.Since(quietSince) >= quiet {
				return nil
			}
		} else {
			quietSince = time.Time{}
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		}
	}
}

// staleRequestAge is how long an unanswered request counts as in flight.
const staleRequestAge = 30 * time.Second

// pendingRequests counts requests that have neither a response nor a failure.
func (p *trackedPage) pendingRequests(maxAge time.Duration) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.network {
		// Long-lived streams never "complete"; they must not hold the page busy.
		if e.Type == "WebSocket" || e.Type == "EventSource" || e.Type == "Preflight" {
			continue
		}
		if e.Status == 0 && !e.Failed && time.Since(e.At) < maxAge {
			n++
		}
	}
	return n
}

// waitFrameText polls until text appears in the text content of the frame the
// selector's frame path leads to ("iframe#a >>> anything" waits inside #a).
func (s *session) waitFrameText(ctx context.Context, framePath, text string) error {
	for {
		inner, opts, err := s.query(ctx, framePath+" "+frameSep+" body")
		if err != nil {
			return err
		}
		var txt string
		if err := chromedp.Run(ctx, chromedp.Text(inner, &txt, append(opts, chromedp.NodeReady)...)); err == nil && strings.Contains(txt, text) {
			return nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w: text %q not found in frame", ErrSelectorNotFound, text)
			}
			return classifyErr(ctx.Err(), ErrNavigationTimeout)
		}
	}
}

func (s *session) screenshot(ctx context.Context, sel string, full bool) ([]byte, error) {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	var b []byte
	var a chromedp.Action = chromedp.FullScreenshot(&b, 100)
	if sel != "" {
		inner, opts, err := s.query(c, sel)
		if err != nil {
			return nil, err
		}
		a = chromedp.Screenshot(inner, &b, opts...)
	}
	if err := chromedp.Run(c, a); err != nil {
		return nil, fmt.Errorf("browser screenshot: %w", err)
	}
	return b, nil
}
func (s *session) inspect(ctx context.Context, sel string) (string, error) {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	var nodes []*accessibility.Node
	var axErr error
	// Every inspect creates a new ref namespace. A scoped inspect must invalidate
	// refs too; otherwise a ref from an older snapshot could act on a changed page.
	refs := s.resetRefs()
	if sel == "" {
		// A full-page snapshot starts a fresh ref numbering: every earlier ref
		// is dropped so none can ever address a different node than it showed.
		refs = s.resetRefs()
		// CDP commands need chromedp's executor installed by Run.
		axErr = chromedp.Run(c, chromedp.ActionFunc(func(execCtx context.Context) error {
			var err error
			nodes, err = fullAXTree(execCtx)
			return err
		}))
	} else {
		refs = s.refs
		inner, opts, qerr := s.query(c, sel)
		if qerr != nil {
			return "", qerr
		}
		if frames, _ := splitFrames(sel); len(frames) > 0 {
			// Refs resolve against the top document only; a node inside a frame
			// would get a ref that can never be used.
			refs = nil
		}
		var ids []cdp.NodeID
		axErr = chromedp.Run(c, chromedp.NodeIDs(inner, &ids, opts...))
		if axErr == nil && len(ids) > 0 {
			axErr = chromedp.Run(c, chromedp.ActionFunc(func(execCtx context.Context) error {
				var err error
				nodes, err = partialAXTree(execCtx, ids[0])
				return err
			}))
		} else if axErr == nil {
			axErr = fmt.Errorf("selector %q matched no nodes", sel)
		}
	}
	if axErr != nil {
		var fallback string
		if err := chromedp.Run(c, chromedp.Evaluate(`(() => {
				const out = [];
				for (const el of document.querySelectorAll('button,[role],input,textarea,select')) {
					const role = el.getAttribute('role') || (el.tagName || '').toLowerCase();
					const name = (el.innerText || el.value || el.getAttribute('aria-label') || '').trim();
					out.push(role + (name ? ': ' + name : ''));
				}
				return out.join('\n') + (document.body ? '\n' + document.body.innerText : '');
			})()`, &fallback)); err == nil && fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("browser inspect: %w", axErr)
	}
	return renderAXTreeRefs(nodes, refs), nil
}
func (s *session) click(ctx context.Context, sel string) error {
	p := s.activePage()
	before := s.pageCount()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	inner, opts, err := s.query(c, sel)
	if err != nil {
		return err
	}
	if err := chromedp.Run(c, chromedp.Click(inner, opts...)); err != nil {
		return classifySelectorErr(err)
	}
	s.followNewPage(before, ctx)
	return nil
}
func (s *session) typeText(ctx context.Context, sel, text string, submit bool) error {
	pageCtx := s.activePage().ctx
	cctx, cancel := actionContext(pageCtx, ctx)
	defer cancel()
	inner, opts, err := s.query(cctx, sel)
	if err != nil {
		return err
	}
	a := []chromedp.Action{chromedp.Focus(inner, opts...), chromedp.SendKeys(inner, text, opts...)}
	if submit {
		a = append(a, chromedp.SendKeys(inner, "\r", opts...))
	}
	if err := chromedp.Run(cctx, a...); err != nil {
		return classifySelectorErr(err)
	}
	return nil
}

// reserveTab claims one of MaxTabs slots for a page about to be set up, counting
// pages still being set up as well as tracked ones, and reports whether there
// was room. Without the in-flight count, every target-created event in a rapid
// series would see the same low page count and all of them would be let in.
func (s *session) reserveTab() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pages)+s.tabsInFlight >= MaxTabs {
		return false
	}
	s.tabsInFlight++
	return true
}

// releaseTab returns a reservation once the page is tracked (or failed).
func (s *session) releaseTab() {
	s.mu.Lock()
	s.tabsInFlight--
	s.mu.Unlock()
}

func (s *session) pageCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.pages) }
func (s *session) followNewPage(before int, ctx context.Context) {
	deadline := time.NewTimer(newTabDetectWindow)
	defer deadline.Stop()
	for {
		if s.pageCount() > before {
			s.mu.Lock()
			s.active = len(s.pages) - 1
			p := s.pages[s.active]
			s.mu.Unlock()
			refreshCtx, cancel := context.WithTimeout(p.ctx, 2*time.Second)
			var url string
			if chromedp.Run(refreshCtx, chromedp.Location(&url)) == nil {
				p.setURL(url)
			}
			cancel()
			return
		}
		select {
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (s *session) hotkey(ctx context.Context, spec string) error {
	c, e := parseHotkey(spec)
	if e != nil {
		return e
	}
	a := []chromedp.Action{}
	for _, k := range c.modifiers {
		a = append(a, chromedp.KeyEvent(k))
	}
	a = append(a, chromedp.KeyEvent(c.main))
	pageCtx := s.activePage().ctx
	cctx, cancel := actionContext(pageCtx, ctx)
	defer cancel()
	if err := chromedp.Run(cctx, a...); err != nil {
		return classifySelectorErr(err)
	}
	return nil
}
func (s *session) scroll(ctx context.Context, sel string, x, y float64) error {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	if sel != "" {
		inner, opts, err := s.query(c, sel)
		if err != nil {
			return err
		}
		return classifySelectorErr(chromedp.Run(c, chromedp.ScrollIntoView(inner, opts...)))
	}
	return chromedp.Run(c, chromedp.Evaluate(fmt.Sprintf("window.scrollBy(%g,%g)", x, y), nil))
}
func (s *session) wait(ctx context.Context, sel, text string, idle bool, d time.Duration) error {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	c, timeoutCancel := context.WithTimeout(c, d)
	defer timeoutCancel()
	if idle {
		if e := s.waitNetworkIdle(c, p, 500*time.Millisecond); e != nil {
			return classifyErr(e, ErrNavigationTimeout)
		}
	}
	if sel != "" {
		inner, opts, err := s.query(c, sel)
		if err != nil {
			return err
		}
		if e := chromedp.Run(c, chromedp.WaitVisible(inner, opts...)); e != nil {
			return classifySelectorErr(e)
		}
	}
	if text != "" {
		// With "frame >>> x" the text is looked for inside that frame rather
		// than in the top document.
		if frames, _ := splitFrames(sel); len(frames) > 0 {
			return s.waitFrameText(c, strings.Join(frames, " "+frameSep+" "), text)
		}
		// Text can be updated asynchronously by the page after an action.
		// Watch for it with a MutationObserver running inside the page
		// (Poll+WithPollingMutation) instead of round-tripping a fresh CDP
		// Evaluate call every N milliseconds from Go: each round trip pays
		// full IPC/serialization latency, and a page that's slow to render
		// (a freshly headed, software-rendered Chrome under Xvfb, say) can
		// make that latency stretch to hundreds of milliseconds, silently
		// starving the number of checks a fixed-interval Go-side poll gets
		// to make before the deadline. The in-page observer fires on the
		// actual DOM mutation, so detection latency no longer depends on
		// how slow or fast the CDP round trip happens to be that day.
		// Bounded by c's own deadline (set to d above), not a separate
		// timeout — no second timer to race against it.
		predicate := fmt.Sprintf("document.body&&document.body.innerText.includes(%q)", text)
		if e := chromedp.Run(c, chromedp.Poll(predicate, nil, chromedp.WithPollingMutation())); e != nil {
			if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, chromedp.ErrPollingTimeout) {
				return fmt.Errorf("%w: text %q not found", ErrSelectorNotFound, text)
			}
			return classifySelectorErr(e)
		}
	}
	return nil
}
func (s *session) tabs(ctx context.Context) []TabInfo {
	s.mu.Lock()
	p := append([]*trackedPage(nil), s.pages...)
	a := s.active
	s.mu.Unlock()

	// Read tab metadata from the browser-level target registry. Querying each
	// page target can block forever when Chrome has created a popup target but
	// has not finished attaching its renderer, which makes browser_tabs hang.
	lookupCtx, cancel := context.WithTimeout(s.browserCtx, 2*time.Second)
	defer cancel()
	infos, _ := target.GetTargets().Do(cdp.WithExecutor(lookupCtx, s.browser))
	byID := make(map[target.ID]*target.Info, len(infos))
	for _, info := range infos {
		if info != nil {
			byID[info.TargetID] = info
		}
	}

	o := make([]TabInfo, len(p))
	for i, x := range p {
		var title, url string
		if info := byID[x.id]; info != nil {
			title, url = info.Title, info.URL
			x.setURL(url)
		}
		o[i] = TabInfo{i, string(x.id), title, url, i == a}
	}
	return o
}
func (s *session) switchTab(i int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.pages) {
		return fmt.Errorf("%w: index %d", ErrTabNotFound, i)
	}
	s.active = i
	return nil
}
func (s *session) closeTab(ctx context.Context, i int) error {
	s.mu.Lock()
	if i < 0 || i >= len(s.pages) {
		s.mu.Unlock()
		return ErrTabNotFound
	}
	if len(s.pages) == 1 {
		s.mu.Unlock()
		return errors.New("cannot close the only remaining tab; use browser_close to end the session instead")
	}
	p := s.pages[i]
	s.mu.Unlock()
	p.cancel()
	if err := target.CloseTarget(p.id).Do(cdp.WithExecutor(s.browserCtx, s.browser)); err != nil {
		return fmt.Errorf("close tab: %w", err)
	}
	s.untrack(p.id)
	return nil
}
func (s *session) setFiles(ctx context.Context, sel string, paths []string) error {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	inner, opts, err := s.query(c, sel)
	if err != nil {
		return err
	}
	return classifySelectorErr(chromedp.Run(c, chromedp.SetUploadFiles(inner, paths, opts...)))
}
func (s *session) downloadViaClick(ctx context.Context, sel, dir string) (*browser.EventDownloadWillBegin, error) {
	return s.download(ctx, dir, func(p context.Context) error {
		inner, opts, err := s.query(p, sel)
		if err != nil {
			return err
		}
		return chromedp.Run(p, chromedp.Click(inner, opts...))
	})
}

func (s *session) downloadViaURL(ctx context.Context, u, dir string) (*browser.EventDownloadWillBegin, error) {
	return s.download(ctx, dir, func(p context.Context) error {
		s.policy.allowExplicit(u)
		err := chromedp.Run(p, chromedp.Navigate(u))
		if err != nil && strings.Contains(err.Error(), "ERR_ABORTED") {
			return nil
		}
		return err
	})
}

func (s *session) cancelDownload(w *downloadWaiter) {
	s.downloadMu.Lock()
	if w.guid != "" {
		delete(s.downloads, w.guid)
		_ = browser.CancelDownload(w.guid).Do(context.Background())
	}
	s.downloadMu.Unlock()
}

func (s *session) download(ctx context.Context, dir string, trigger func(context.Context) error) (*browser.EventDownloadWillBegin, error) {
	p := s.activePage()
	triggerCtx, triggerCancel := actionContext(p.ctx, ctx)
	defer triggerCancel()
	w := &downloadWaiter{begun: make(chan *browser.EventDownloadWillBegin, 1), done: make(chan struct{}), failed: make(chan error, 1)}
	s.downloadMu.Lock()
	s.pendingDownload = w
	s.downloadMu.Unlock()
	defer func() {
		s.downloadMu.Lock()
		if s.pendingDownload == w {
			s.pendingDownload = nil
		}
		s.downloadMu.Unlock()
	}()
	if err := browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllowAndName).WithDownloadPath(dir).WithEventsEnabled(true).Do(cdp.WithExecutor(s.browserCtx, s.browser)); err != nil {
		return nil, fmt.Errorf("browser download: %w", err)
	}
	if err := trigger(triggerCtx); err != nil {
		if mapped := classifySelectorErr(err); mapped != err {
			return nil, mapped
		}
		return nil, classifyDownloadErr(fmt.Errorf("browser download: %w", err))
	}
	wait := ctx
	if wait == nil {
		wait = context.Background()
	}
	var info *browser.EventDownloadWillBegin
	select {
	case info = <-w.begun:
	case err := <-w.failed:
		return nil, err
	case <-wait.Done():
		s.cancelDownload(w)
		return nil, classifyDownloadErr(fmt.Errorf("browser download: %w", wait.Err()))
	}
	select {
	case <-w.done:
	case err := <-w.failed:
		return nil, err
	case <-wait.Done():
		s.cancelDownload(w)
		return nil, classifyDownloadErr(fmt.Errorf("browser download: %w", wait.Err()))
	}
	return info, nil
}
