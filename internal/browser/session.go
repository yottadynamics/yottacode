package browser

import (
	"context"
	"errors"
	"fmt"
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

type ConsoleEntry struct {
	Level, Text string
	At          time.Time
}
type NetworkEntry struct {
	RequestID, Method, URL string
	Status                 int
	StatusText, MIMEType   string
	Failed                 bool
	ErrorText              string
	At                     time.Time
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
}

type trackedPage struct {
	id       target.ID
	ctx      context.Context
	cancel   context.CancelFunc
	openedAt time.Time
	mu       syncutil.Mutex
	console  []ConsoleEntry
	network  []*NetworkEntry
}

func (p *trackedPage) recordConsole(e ConsoleEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.console = append(p.console, e)
	if len(p.console) > maxBufferedEntries {
		p.console = p.console[len(p.console)-maxBufferedEntries:]
	}
}
func (p *trackedPage) recordRequestStart(id, method, url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.network = append(p.network, &NetworkEntry{RequestID: id, Method: method, URL: url, At: time.Now()})
	if len(p.network) > maxBufferedEntries {
		p.network = p.network[len(p.network)-maxBufferedEntries:]
	}
}
func (p *trackedPage) recordRequestUpdate(id string, fn func(*NetworkEntry)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.network {
		if e.RequestID == id {
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
}

type downloadWaiter struct {
	begun    chan *browser.EventDownloadWillBegin
	done     chan struct{}
	failed   chan error
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
	opts := []chromedp.ExecAllocatorOption{chromedp.ExecPath(bin), chromedp.UserDataDir(profile), chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.Flag("disable-features", "TranslateUI")}
	if headless {
		opts = append(opts, chromedp.Headless)
	}
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
			return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
		}
	case <-ctx.Done():
		browserCancel()
		cancel()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, ctx.Err())
	case <-startupTimer.C:
		browserCancel()
		cancel()
		return nil, fmt.Errorf("%w: browser startup timed out", ErrLaunchFailed)
	}
	s := &session{allocCtx: allocCtx, cancel: cancel, browserCtx: bc, browserCancel: browserCancel, browser: chromedp.FromContext(bc).Browser, downloads: make(map[string]*downloadWaiter)}
	if s.browser != nil && s.browser.Process() != nil {
		s.pid = s.browser.Process().Pid
	}
	pc, pcancel := chromedp.NewContext(bc)
	if err := chromedp.Run(pc, chromedp.Navigate("about:blank")); err != nil {
		cancel()
		pcancel()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}
	id := chromedp.FromContext(pc).Target.TargetID
	tp := s.track(id, pc, pcancel)
	attachPageCapture(pc, tp)
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
			ctx, cancel := chromedp.NewContext(bc, chromedp.WithTargetID(e.TargetInfo.TargetID))
			p, created := s.trackPage(e.TargetInfo.TargetID, ctx)
			if created {
				attachPageCapture(ctx, p)
			} else {
				cancel()
			}
		case *target.EventTargetDestroyed:
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
			s.downloads[e.GUID] = w
			select {
			case w.begun <- e:
			default:
			}
		}
	case *browser.EventDownloadProgress:
		if w := s.downloads[e.GUID]; w != nil && (e.TotalBytes > float64(w.maxBytes) || e.ReceivedBytes > float64(w.maxBytes)) {
			select {
			case w.failed <- fmt.Errorf("%w: received %.0f bytes, limit is %d", ErrDownloadTooLarge, e.ReceivedBytes, w.maxBytes):
			default:
			}
			delete(s.downloads, e.GUID)
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
	return p, true
}

func (s *session) untrackPage(id target.ID) {
	s.mu.Lock()
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
func attachPageCapture(ctx context.Context, p *trackedPage) {
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventJavascriptDialogOpening:
			// Dialogs have no browser_* interaction surface; dismiss them so
			// a modal alert cannot block the target indefinitely.
			go func() { _ = chromedp.Run(ctx, page.HandleJavaScriptDialog(false)) }()
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
				p.recordRequestStart(string(e.RequestID), e.Request.Method, e.Request.URL)
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
	// Do not synchronously execute CDP commands from a browser event listener:
	// chromedp's browser dispatcher holds its execution lock while invoking
	// listeners, so doing so would deadlock target-created events.
	go func() { _ = chromedp.Run(ctx, runtime.Enable(), network.Enable(), page.Enable()) }()
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
func (s *session) close() error {
	err := chromedp.Cancel(s.browserCtx)
	s.browserCancel()
	s.cancel()
	return err
}
func (s *session) forceCleanup() {
	s.browserCancel()
	s.cancel()
}
func (s *session) alive() bool {
	if s.browser == nil || s.browser.Process() == nil {
		return false
	}
	return s.browser.Process().Signal(syscall.Signal(0)) == nil
}
func (s *session) snapshot() (string, int) {
	p := s.activeTrackedPage()
	if p == nil {
		return "", 0
	}
	var u string
	_ = chromedp.Run(p.ctx, chromedp.Location(&u))
	s.mu.Lock()
	n := len(s.pages)
	s.mu.Unlock()
	return u, n
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
	if err := chromedp.Run(c, chromedp.Navigate(u)); err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	switch strings.ToLower(w) {
	case "domcontentloaded":
		if err := chromedp.Run(c, chromedp.WaitReady("html")); err != nil {
			return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
		}
	case "networkidle":
		if err := s.waitNetworkIdle(c, 500*time.Millisecond); err != nil {
			return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
		}
	}
	var url, title string
	if err := chromedp.Run(c, chromedp.Location(&url), chromedp.Title(&title)); err != nil {
		return NavigateResult{}, err
	}
	return NavigateResult{url, title}, nil
}
func (s *session) waitNetworkIdle(ctx context.Context, quiet time.Duration) error {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		var state string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.readyState`, &state)); err != nil {
			return err
		}
		if state == "complete" {
			t := time.NewTimer(quiet)
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-deadline.C:
				t.Stop()
				return nil
			}
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

func (s *session) screenshot(ctx context.Context, sel string, full bool) ([]byte, error) {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	var b []byte
	var a chromedp.Action = chromedp.FullScreenshot(&b, 100)
	if sel != "" {
		a = chromedp.Screenshot(sel, &b)
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
	if sel == "" {
		// CDP commands need chromedp's executor installed by Run.
		axErr = chromedp.Run(c, chromedp.ActionFunc(func(execCtx context.Context) error {
			var err error
			nodes, err = accessibility.GetFullAXTree().Do(execCtx)
			return err
		}))
	} else {
		var ids []cdp.NodeID
		axErr = chromedp.Run(c, chromedp.NodeIDs(sel, &ids))
		if axErr == nil && len(ids) > 0 {
			axErr = chromedp.Run(c, chromedp.ActionFunc(func(execCtx context.Context) error {
				var err error
				nodes, err = accessibility.GetPartialAXTree().WithNodeID(ids[0]).WithFetchRelatives(true).Do(execCtx)
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
				return out.join('\\n') + (document.body ? '\\n' + document.body.innerText : '');
			})()`, &fallback)); err == nil && fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("browser inspect: %w", axErr)
	}
	return renderAXTree(nodes), nil
}
func (s *session) click(ctx context.Context, sel string) error {
	p := s.activePage()
	before := s.pageCount()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	if err := chromedp.Run(c, chromedp.Click(sel)); err != nil {
		return classifySelectorErr(err)
	}
	s.followNewPage(before, ctx)
	return nil
}
func (s *session) typeText(ctx context.Context, sel, text string, submit bool) error {
	a := []chromedp.Action{chromedp.Focus(sel), chromedp.SendKeys(sel, text)}
	if submit {
		a = append(a, chromedp.SendKeys(sel, "\r"))
	}
	pageCtx := s.activePage().ctx
	cctx, cancel := actionContext(pageCtx, ctx)
	defer cancel()
	if err := chromedp.Run(cctx, a...); err != nil {
		return classifySelectorErr(err)
	}
	return nil
}
func (s *session) pageCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.pages) }
func (s *session) followNewPage(before int, ctx context.Context) {
	deadline := time.NewTimer(newTabDetectWindow)
	defer deadline.Stop()
	for {
		if s.pageCount() > before {
			s.mu.Lock()
			s.active = len(s.pages) - 1
			s.mu.Unlock()
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
		return classifySelectorErr(chromedp.Run(c, chromedp.ScrollIntoView(sel)))
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
		if e := chromedp.Run(c, chromedp.Sleep(500*time.Millisecond)); e != nil {
			return e
		}
	}
	if sel != "" {
		if e := chromedp.Run(c, chromedp.WaitVisible(sel)); e != nil {
			return classifySelectorErr(e)
		}
	}
	if text != "" {
		var ok bool
		if e := chromedp.Run(c, chromedp.Evaluate(fmt.Sprintf("document.body&&document.body.innerText.includes(%q)", text), &ok)); e != nil {
			return classifySelectorErr(e)
		} else if !ok {
			return fmt.Errorf("%w: text %q not found", ErrSelectorNotFound, text)
		}
	}
	return nil
}
func (s *session) tabs(ctx context.Context) []TabInfo {
	s.mu.Lock()
	p := append([]*trackedPage(nil), s.pages...)
	a := s.active
	s.mu.Unlock()
	o := make([]TabInfo, len(p))
	for i, x := range p {
		var u, t string
		_ = chromedp.Run(x.ctx, chromedp.Location(&u), chromedp.Title(&t))
		o[i] = TabInfo{i, string(x.id), t, u, i == a}
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
	_ = chromedp.Run(p.ctx, target.CloseTarget(p.id))
	p.cancel()
	s.untrack(p.id)
	return nil
}
func (s *session) setFiles(ctx context.Context, sel string, paths []string) error {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	return classifySelectorErr(chromedp.Run(c, chromedp.SetUploadFiles(sel, paths)))
}
func (s *session) downloadViaClick(ctx context.Context, sel, dir string) (*browser.EventDownloadWillBegin, error) {
	return s.download(ctx, dir, func(p context.Context) error { return chromedp.Run(p, chromedp.Click(sel)) })
}

func (s *session) downloadViaURL(ctx context.Context, u, dir string) (*browser.EventDownloadWillBegin, error) {
	return s.download(ctx, dir, func(p context.Context) error {
		err := chromedp.Run(p, chromedp.Navigate(u))
		if err != nil && strings.Contains(err.Error(), "ERR_ABORTED") {
			return nil
		}
		return err
	})
}

func (s *session) download(ctx context.Context, dir string, trigger func(context.Context) error) (*browser.EventDownloadWillBegin, error) {
	p := s.activePage()
	triggerCtx, triggerCancel := actionContext(p.ctx, ctx)
	defer triggerCancel()
	w := &downloadWaiter{begun: make(chan *browser.EventDownloadWillBegin, 1), done: make(chan struct{}), failed: make(chan error, 1), maxBytes: MaxDownloadBytes}
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
		return nil, classifyDownloadErr(fmt.Errorf("browser download: %w", wait.Err()))
	}
	select {
	case <-w.done:
	case err := <-w.failed:
		return nil, err
	case <-wait.Done():
		return nil, classifyDownloadErr(fmt.Errorf("browser download: %w", wait.Err()))
	}
	return info, nil
}
