package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
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
	// TabCount is the number of tracked pages (main page plus any
	// popup/tab opened since launch). 0 when no session is active.
	TabCount int
	// Provider names the backend ("local", "browserbase"); Remote is true
	// for a browser hosted by a third party, whose pages are processed off
	// this machine. Degraded lists paid provider features that were
	// requested but dropped because the account's plan lacks them.
	Provider string
	Remote   bool
	// Endpoint is the external server's URL for a provider that has one
	// (Camofox); empty otherwise.
	Endpoint string
	Degraded []string
	// ReapedIdle is true when the last session was closed for sitting idle
	// (see idle.go) and no new one has launched since — the tabs and page
	// state it held are gone, and the next action starts a fresh browser.
	ReapedIdle bool
}

// DownloadResult is the outcome of a successful browser_download call —
// the file is already at Path by the time this is returned.
type DownloadResult struct {
	Path              string
	SuggestedFilename string
	SizeBytes         int64
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
	mu         syncutil.Mutex
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

	// dialogAccept/dialogPrompt are the JS-dialog policy (see dialog.go),
	// kept here so it survives a relaunch after a crash and can be set
	// before the first launch. ensureLocked applies it to every new session.
	dialogAccept bool
	dialogPrompt string

	// idleTimeout, when positive, closes a browser left unused that long
	// (see idle.go); lastUsed/idleTimer/reapedIdle are its bookkeeping. A
	// zero value disables reaping, so Manager literals in tests are unaffected.
	idleTimeout time.Duration
	lastUsed    time.Time
	idleTimer   *time.Timer
	reapedIdle  bool

	// newSession, findBinary, and mkProfileDir are overridable so unit
	// tests can exercise lifecycle/serialization/error-mapping without
	// spawning a real Chrome process. NewManager wires the real
	// implementations; tests construct a Manager literal directly (same
	// package) with fakes instead.
	newSession   func(ctx context.Context, bin, profileDir string) (pageSession, error)
	findBinary   func() (string, error)
	mkProfileDir func() (string, error)

	// newRemote, when set, replaces the whole local launch (binary discovery,
	// temp profile, child process) with a connection to a browser hosted
	// elsewhere — see remote.go. provider names the backend for Status.
	newRemote func(ctx context.Context) (pageSession, error)
	provider  string
	// endpoint is where an external provider's server lives (Camofox), shown
	// in Status. Credentials are stripped.
	endpoint string
}

// NewManager returns an unlaunched Manager. The browser process starts
// lazily on the first action call that needs it (see ensureLocked);
// browser_status and browser_wait never trigger a launch.
func NewManager() *Manager {
	return &Manager{
		newSession: func(ctx context.Context, bin, profileDir string) (pageSession, error) {
			return launchSession(ctx, bin, profileDir, launchOptions{})
		},
		findBinary:   findChromeBinary,
		mkProfileDir: newProfileDir,
		idleTimeout:  defaultIdleTimeout,
		provider:     ProviderLocal,
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
			m.touchLocked()
			return m.sess, nil
		}
		m.discardDeadSessionLocked()
	}
	if m.newRemote != nil {
		sess, err := m.newRemote(ctx)
		if err != nil {
			return nil, err
		}
		m.sess = sess
		m.reapedIdle = false
		sess.setDialogPolicy(m.dialogAccept, m.dialogPrompt)
		m.touchLocked()
		return sess, nil
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
	m.reapedIdle = false
	sess.setDialogPolicy(m.dialogAccept, m.dialogPrompt)
	m.touchLocked()
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

// ensureAliveNoLaunchLocked resolves the current session for a call that
// must NOT launch the browser (Tabs, ConsoleLogs, NetworkRequests,
// SwitchTab, CloseTab): a closed manager denies, a dead session is
// discarded and reported rather than silently reused, and no session at
// all yields (nil, false, nil) — callers decide what "nothing to act on
// yet" means for their own semantics (an empty result for a listing
// call, an error for one that references an index). Callers must
// already hold m.mu.
func (m *Manager) ensureAliveNoLaunchLocked() (sess pageSession, ok bool, err error) {
	if m.closed {
		return nil, false, ErrActionDenied
	}
	if m.sess == nil {
		return nil, false, nil
	}
	if !m.sess.alive() {
		m.discardDeadSessionLocked()
		return nil, false, errors.New("browser process is no longer running (it crashed or was killed); the next browser_* call will relaunch it")
	}
	m.touchLocked()
	return m.sess, true, nil
}

// Status never launches the browser — see the Status doc comment.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{BinaryPath: m.binPath, ProfileDir: m.profileDir, ReapedIdle: m.reapedIdle, Provider: m.provider, Remote: m.newRemote != nil, Endpoint: m.endpoint}
	if st.BinaryPath == "" && !st.Remote {
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
		st.CurrentURL, st.TabCount = m.sess.snapshot()
		if d, ok := m.sess.(interface{ degradedFeatures() []string }); ok {
			st.Degraded = d.degradedFeatures()
		}
	}
	return st
}

// Tabs never launches the browser, like Status — an unlaunched session
// has no tabs to report. Unlike Status, each entry costs a live CDP
// round-trip (to read that page's current title/URL), so it's bounded by
// the same action timeout every mutating call gets, even though nothing
// is mutated.
func (m *Manager) Tabs(ctx context.Context) ([]TabInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok, err := m.ensureAliveNoLaunchLocked()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	return sess.tabs(ctx), nil
}

// ConsoleLogs never launches, like Tabs — an unlaunched session has
// nothing buffered yet. Pure in-memory read, no action timeout needed.
func (m *Manager) ConsoleLogs(ctx context.Context, limit int) ([]ConsoleEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok, err := m.ensureAliveNoLaunchLocked()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return sess.consoleLogs(limit), nil
}

// NetworkRequests mirrors ConsoleLogs exactly.
func (m *Manager) NetworkRequests(ctx context.Context, limit int) ([]NetworkEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok, err := m.ensureAliveNoLaunchLocked()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return sess.networkRequests(limit), nil
}

// SwitchTab changes which tracked page subsequent actions act on. Like
// Tabs, it never launches — switching among tabs that don't exist yet
// makes no sense.
func (m *Manager) SwitchTab(ctx context.Context, index int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok, err := m.ensureAliveNoLaunchLocked()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no browser session active", ErrTabNotFound)
	}
	return sess.switchTab(index)
}

// CloseTab closes one tracked tab by index without tearing down the
// whole session. Like SwitchTab, it never launches — closing a tab that
// doesn't exist yet makes no sense — but unlike SwitchTab it does a real
// CDP round-trip, so it gets the same action timeout every mutating call
// gets.
func (m *Manager) CloseTab(ctx context.Context, index int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok, err := m.ensureAliveNoLaunchLocked()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no browser session active", ErrTabNotFound)
	}
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	return sess.closeTab(ctx, index)
}

// Upload sets a file input element's files, lazily launching the browser
// like every other mutating action. Callers must have already validated
// every path is inside the trusted workspace — see BrowserUploadTool,
// which reuses the same write-path boundary as write_file: handing a
// local file's bytes to whatever origin the active page is on is at
// least as sensitive as writing it.
func (m *Manager) Upload(ctx context.Context, selector string, paths []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return err
	}
	return s.setFiles(ctx, selector, paths)
}

// Download triggers a download — by clicking selector, or by navigating
// directly to url (exactly one must be set) — and saves it to destPath,
// which callers must have already validated the same way write_file does
// (see BrowserDownloadTool). It owns the whole sequence: a scratch temp
// dir for Chrome's own GUID-named download file, the CDP download
// arm/wait (session's downloadViaClick/downloadViaURL), then moving the
// result to destPath and cleaning up the temp dir.
func (m *Manager) Download(ctx context.Context, selector, url, destPath string) (DownloadResult, error) {
	if url != "" {
		if err := checkNavigationTarget(ctx, url); err != nil {
			return DownloadResult{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return DownloadResult{}, err
	}

	tmpDir, err := os.MkdirTemp("", "yottacode-browser-download-*")
	if err != nil {
		return DownloadResult{}, fmt.Errorf("browser download: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var info *proto.PageDownloadWillBegin
	if selector != "" {
		info, err = s.downloadViaClick(ctx, selector, tmpDir)
	} else {
		info, err = s.downloadViaURL(ctx, url, tmpDir)
	}
	if err != nil {
		return DownloadResult{}, err
	}

	src := filepath.Join(tmpDir, info.GUID)
	fi, err := os.Stat(src)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("%w: saved file missing: %v", ErrDownloadFailed, err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return DownloadResult{}, fmt.Errorf("browser download: %w", err)
	}
	if err := moveFile(src, destPath); err != nil {
		return DownloadResult{}, fmt.Errorf("browser download: %w", err)
	}
	return DownloadResult{Path: destPath, SuggestedFilename: info.SuggestedFilename, SizeBytes: fi.Size()}, nil
}

// moveFile renames src to dst, falling back to copy-then-remove when
// rename fails — which os.Rename always does across filesystems (EXDEV).
// That's a real case here, not a hypothetical one: src lives under the
// OS temp dir (MkdirTemp's default), while dst is wherever the caller
// validated (cwd or an --allow-paths root), which can easily be a
// different mount.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst) // don't leave a truncated file at the reported-failed destination
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

func (m *Manager) Navigate(ctx context.Context, url, waitUntil string) (NavigateResult, error) {
	// Checked before locking or launching: a refused URL must not cost a
	// Chrome launch, and needs no session to refuse.
	if err := checkNavigationTarget(ctx, url); err != nil {
		return NavigateResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return NavigateResult{}, err
	}
	res, err := s.navigate(ctx, url, waitUntil)
	res.Provider, res.Remote = m.provider, m.newRemote != nil
	return res, err
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

func (m *Manager) Inspect(ctx context.Context, selector string, opts InspectOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return "", err
	}
	return s.inspect(ctx, selector, opts)
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
	m.touchLocked()
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
	m.stopIdleTimerLocked()
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
