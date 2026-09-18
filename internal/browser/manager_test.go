package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// fakeSession implements pageSession without a real Chrome process, so
// Manager's lifecycle/serialization/error-mapping behavior can be tested
// without spawning a browser. It's internally mutex-protected as a
// defense-in-depth check for the -race run: Manager is expected to
// already serialize every call, so a race here would mean Manager's own
// locking is broken.
type fakeSession struct {
	mu    syncutil.Mutex
	calls []string
	url   string

	navigateErr error
	closeErr    error

	// blockUntilDone, when set, makes navigate ignore navigateErr and
	// instead block until ctx is done — simulating a hung page/action so
	// tests can prove Manager's timeout actually bounds it.
	blockUntilDone bool

	// dead simulates a crashed browser process: alive() reports false
	// once this is set, without needing a real OS process to kill.
	dead bool

	// pages controls the tab count snapshot() reports (and, through it,
	// Status.TabCount). 0 defaults to 1 (just the main page), matching a
	// real session's invariant of always having at least one tracked page.
	pages int
	// tabsResult, consoleLogsResult, networkRequestsResult are returned
	// verbatim by the matching method.
	tabsResult            []TabInfo
	consoleLogsResult     []ConsoleEntry
	networkRequestsResult []NetworkEntry
	// switchTabErr, closeTabErr, setFilesErr, downloadErr are returned
	// verbatim by the matching method.
	switchTabErr error
	closeTabErr  error
	setFilesErr  error
	downloadErr  error
	// downloadResult, when set, is returned by downloadViaClick/
	// downloadViaURL instead of a synthesized default. Either way, the
	// fake actually writes downloadContent (or a default) to
	// filepath.Join(dir, info.GUID) — Manager.Download stats and moves
	// that file for real, so this needs to exist on disk to test the
	// real move/cleanup logic, not just the call plumbing.
	downloadResult  *proto.PageDownloadWillBegin
	downloadContent []byte
}

func (f *fakeSession) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeSession) navigate(ctx context.Context, url, _ string) (NavigateResult, error) {
	f.record("navigate")
	if f.blockUntilDone {
		<-ctx.Done()
		return NavigateResult{}, ctx.Err()
	}
	f.mu.Lock()
	f.url = url
	f.mu.Unlock()
	return NavigateResult{URL: url}, f.navigateErr
}
func (f *fakeSession) screenshot(context.Context, string, bool) ([]byte, error) {
	f.record("screenshot")
	return []byte("png"), nil
}
func (f *fakeSession) inspect(context.Context, string) (string, error) {
	f.record("inspect")
	return "tree", nil
}
func (f *fakeSession) click(context.Context, string) error {
	f.record("click")
	return nil
}
func (f *fakeSession) typeText(context.Context, string, string, bool) error {
	f.record("type")
	return nil
}
func (f *fakeSession) hotkey(context.Context, string) error {
	f.record("hotkey")
	return nil
}
func (f *fakeSession) scroll(context.Context, string, float64, float64) error {
	f.record("scroll")
	return nil
}
func (f *fakeSession) wait(context.Context, string, string, bool, time.Duration) error {
	f.record("wait")
	return nil
}
func (f *fakeSession) snapshot() (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pages := f.pages
	if pages == 0 {
		pages = 1
	}
	return f.url, pages
}
func (f *fakeSession) close() error {
	f.record("close")
	return f.closeErr
}
func (f *fakeSession) alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead
}
func (f *fakeSession) forceCleanup() {
	f.record("forceCleanup")
}

func (f *fakeSession) tabs(context.Context) []TabInfo {
	f.record("tabs")
	return f.tabsResult
}

func (f *fakeSession) consoleLogs(limit int) []ConsoleEntry {
	f.record(fmt.Sprintf("consoleLogs:%d", limit))
	return f.consoleLogsResult
}

func (f *fakeSession) networkRequests(limit int) []NetworkEntry {
	f.record(fmt.Sprintf("networkRequests:%d", limit))
	return f.networkRequestsResult
}

func (f *fakeSession) switchTab(index int) error {
	f.record(fmt.Sprintf("switchTab:%d", index))
	return f.switchTabErr
}

func (f *fakeSession) closeTab(_ context.Context, index int) error {
	f.record(fmt.Sprintf("closeTab:%d", index))
	return f.closeTabErr
}

func (f *fakeSession) setFiles(_ context.Context, selector string, paths []string) error {
	f.record(fmt.Sprintf("setFiles:%s:%v", selector, paths))
	return f.setFilesErr
}

// fakeDownload is the shared body of downloadViaClick/downloadViaURL: it
// writes the configured (or default) content to filepath.Join(dir,
// info.GUID), the same place a real Chrome download would land, so
// Manager.Download's own stat+move logic runs for real against a test
// fake rather than being skipped.
func (f *fakeSession) fakeDownload(dir string) (*proto.PageDownloadWillBegin, error) {
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	info := f.downloadResult
	if info == nil {
		info = &proto.PageDownloadWillBegin{GUID: "fake-guid", SuggestedFilename: "downloaded.txt"}
	}
	content := f.downloadContent
	if content == nil {
		content = []byte("fake download bytes")
	}
	if err := os.WriteFile(filepath.Join(dir, string(info.GUID)), content, 0o644); err != nil {
		return nil, err
	}
	return info, nil
}

func (f *fakeSession) downloadViaClick(_ context.Context, selector, dir string) (*proto.PageDownloadWillBegin, error) {
	f.record("downloadViaClick:" + selector)
	return f.fakeDownload(dir)
}

func (f *fakeSession) downloadViaURL(_ context.Context, url, dir string) (*proto.PageDownloadWillBegin, error) {
	f.record("downloadViaURL:" + url)
	return f.fakeDownload(dir)
}

var _ pageSession = (*fakeSession)(nil)

func (f *fakeSession) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func newTestManager(fake *fakeSession, profileDir string) *Manager {
	return &Manager{
		newSession:   func(context.Context, string, string) (pageSession, error) { return fake, nil },
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { return profileDir, nil },
	}
}

func TestManager_StatusNeverLaunches(t *testing.T) {
	fake := &fakeSession{}
	launched := false
	m := &Manager{
		newSession:   func(context.Context, string, string) (pageSession, error) { launched = true; return fake, nil },
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { t.Fatal("mkProfileDir should not be called"); return "", nil },
	}
	st := m.Status()
	if st.Active {
		t.Error("Status reported Active before any launch")
	}
	if st.BinaryPath != "/fake/chrome" {
		t.Errorf("BinaryPath = %q", st.BinaryPath)
	}
	if launched {
		t.Error("Status triggered a launch")
	}
}

func TestManager_WaitNeverLaunches(t *testing.T) {
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Fatal("newSession should not be called")
			return nil, nil
		},
		findBinary:   func() (string, error) { t.Fatal("findBinary should not be called"); return "", nil },
		mkProfileDir: func() (string, error) { t.Fatal("mkProfileDir should not be called"); return "", nil },
	}
	if err := m.Wait(context.Background(), "#done", "", false, 0); err != nil {
		t.Fatalf("Wait on an unlaunched manager should no-op, got %v", err)
	}
}

func TestManager_LazyLaunchOnFirstAction(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())

	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	st := m.Status()
	if !st.Active {
		t.Error("expected Active after Navigate")
	}
	if st.CurrentURL != "https://example.com" {
		t.Errorf("CurrentURL = %q", st.CurrentURL)
	}

	// A second action reuses the same session — no second launch.
	if err := m.Click(context.Background(), "#go"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if fake.callCount("navigate") != 1 || fake.callCount("click") != 1 {
		t.Errorf("unexpected call counts: %v", fake.calls)
	}
}

func TestManager_CloseIsIdempotent(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if fake.callCount("close") != 1 {
		t.Errorf("session.close() called %d times, want 1", fake.callCount("close"))
	}
}

func TestManager_ClosedManagerDeniesFurtherActions(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); !errors.Is(err, ErrActionDenied) {
		t.Errorf("Navigate after Close: got %v, want ErrActionDenied", err)
	}
	if err := m.Wait(context.Background(), "", "", false, 0); !errors.Is(err, ErrActionDenied) {
		t.Errorf("Wait after Close: got %v, want ErrActionDenied", err)
	}
}

func TestManager_LaunchFailurePropagates(t *testing.T) {
	wantErr := fmt.Errorf("%w: boom", ErrLaunchFailed)
	m := &Manager{
		newSession:   func(context.Context, string, string) (pageSession, error) { return nil, wantErr },
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { return "/tmp/does-not-matter", nil },
	}
	_, err := m.Navigate(context.Background(), "https://example.com", "")
	if !errors.Is(err, ErrLaunchFailed) {
		t.Errorf("got %v, want ErrLaunchFailed", err)
	}
}

func TestManager_NoBinaryPropagates(t *testing.T) {
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Fatal("newSession should not be called")
			return nil, nil
		},
		findBinary:   func() (string, error) { return "", ErrNoBinaryFound },
		mkProfileDir: func() (string, error) { t.Fatal("mkProfileDir should not be called"); return "", nil },
	}
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); !errors.Is(err, ErrNoBinaryFound) {
		t.Errorf("got %v, want ErrNoBinaryFound", err)
	}
}

// TestManager_HungActionTimesOutInsteadOfBlockingForever proves the fix
// for the class of bug the timeout policy exists for: an action that
// never returns on its own must still return once actionTimeout elapses,
// with a deadline-exceeded error — not hang the goroutine (and, in
// production, the mutex) forever.
func TestManager_HungActionTimesOutInsteadOfBlockingForever(t *testing.T) {
	fake := &fakeSession{blockUntilDone: true}
	m := newTestManager(fake, t.TempDir())
	m.actionTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := m.Navigate(context.Background(), "https://example.com", "")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Navigate took %v to time out, want close to actionTimeout (50ms)", elapsed)
	}
}

// TestManager_CloseRecoversAfterHungAction is the actual user-facing
// payoff: today, before this fix, a wedged action would hold m.mu forever
// and browser_close could never run either. With a bounded timeout, Close
// only ever waits up to actionTimeout for the previous call to give up
// the lock, then proceeds normally.
func TestManager_CloseRecoversAfterHungAction(t *testing.T) {
	fake := &fakeSession{blockUntilDone: true}
	m := newTestManager(fake, t.TempDir())
	m.actionTimeout = 50 * time.Millisecond

	navDone := make(chan struct{})
	go func() {
		_, _ = m.Navigate(context.Background(), "https://example.com", "")
		close(navDone)
	}()
	// Give the hung Navigate a moment to actually acquire the lock first,
	// so Close genuinely has to wait for it rather than racing to go first.
	time.Sleep(10 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- m.Close(context.Background()) }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return — a hung action wedged the manager")
	}
	<-navDone
}

// TestManager_RelaunchesAfterCrash is the core crash-recovery behavior:
// once the fake session reports it's dead, the next action must not
// reuse it — it must discard it (via forceCleanup, not close, since
// there's no live connection to gracefully negotiate) and launch a
// genuinely new one.
func TestManager_RelaunchesAfterCrash(t *testing.T) {
	first := &fakeSession{}
	second := &fakeSession{}
	launches := 0
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			launches++
			if launches == 1 {
				return first, nil
			}
			return second, nil
		},
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { return t.TempDir(), nil },
	}

	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("first Navigate: %v", err)
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}

	// Simulate the browser process crashing between calls.
	first.mu.Lock()
	first.dead = true
	first.mu.Unlock()

	if err := m.Click(context.Background(), "#go"); err != nil {
		t.Fatalf("Click after crash: %v", err)
	}
	if launches != 2 {
		t.Fatalf("launches = %d, want 2 (should have relaunched)", launches)
	}
	if first.callCount("close") != 0 {
		t.Errorf("first.close() called %d times, want 0 (should use forceCleanup, not close, on a dead session)", first.callCount("close"))
	}
	if first.callCount("forceCleanup") != 1 {
		t.Errorf("first.forceCleanup() called %d times, want 1", first.callCount("forceCleanup"))
	}
	if second.callCount("click") != 1 {
		t.Errorf("second session should have received the click, calls=%v", second.calls)
	}
}

// TestManager_StatusReflectsCrashWithoutRecovering proves Status reports
// a crashed session as inactive (not stale "Active: true") while leaving
// actual recovery to the next real action — Status must never mutate state.
func TestManager_StatusReflectsCrashWithoutRecovering(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	fake.mu.Lock()
	fake.dead = true
	fake.mu.Unlock()

	st := m.Status()
	if st.Active {
		t.Error("Status reported Active for a crashed session")
	}
	// Status must not itself discard the dead session — that's ensureLocked's job.
	m.mu.Lock()
	stillSet := m.sess != nil
	m.mu.Unlock()
	if !stillSet {
		t.Error("Status discarded the dead session itself; recovery should stay in ensureLocked")
	}
}

// TestManager_WaitReportsCrashInsteadOfSilentlySucceeding proves Wait
// doesn't claim its condition was satisfied when the browser is actually
// dead — silently returning nil would look identical to a real pass.
func TestManager_WaitReportsCrashInsteadOfSilentlySucceeding(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	fake.mu.Lock()
	fake.dead = true
	fake.mu.Unlock()

	if err := m.Wait(context.Background(), "#done", "", false, 0); err == nil {
		t.Error("Wait should report an error for a crashed session, not silently succeed")
	}
	if fake.callCount("wait") != 0 {
		t.Error("Wait should not have called through to the dead session's wait method")
	}
}

// TestManager_CloseUsesForceCleanupWhenAlreadyDead proves Close skips the
// graceful (CDP-round-trip) close path for a session that's already
// crashed — a graceful close over a dead connection has nothing to
// negotiate with and could hang rather than fail fast.
func TestManager_CloseUsesForceCleanupWhenAlreadyDead(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	fake.mu.Lock()
	fake.dead = true
	fake.mu.Unlock()

	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.callCount("close") != 0 {
		t.Errorf("close() called %d times, want 0", fake.callCount("close"))
	}
	if fake.callCount("forceCleanup") != 1 {
		t.Errorf("forceCleanup() called %d times, want 1", fake.callCount("forceCleanup"))
	}
}

// TestManager_ConcurrentActionsSerialize hammers every action method from
// many goroutines at once. Manager holds its mutex for the whole call, so
// this must be race-clean under `go test -race` and every call must
// actually reach the fake exactly once — a broken lock would either panic
// under -race or drop/duplicate calls.
func TestManager_ConcurrentActionsSerialize(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())

	const n = 50
	var wg sync.WaitGroup
	ctx := context.Background()
	actions := []func(){
		func() { _, _ = m.Navigate(ctx, "https://example.com", "") },
		func() { _, _ = m.Screenshot(ctx, "", false) },
		func() { _, _ = m.Inspect(ctx, "") },
		func() { _ = m.Click(ctx, "#go") },
		func() { _ = m.Type(ctx, "#q", "hi", false) },
		func() { _ = m.Hotkey(ctx, "Enter") },
		func() { _ = m.Scroll(ctx, "", 0, 100) },
		func() { _ = m.Wait(ctx, "", "", false, 0) },
		func() { _ = m.Status() },
	}
	for range n {
		for _, action := range actions {
			wg.Add(1)
			go func(fn func()) {
				defer wg.Done()
				fn()
			}(action)
		}
	}
	wg.Wait()

	if got := fake.callCount("navigate"); got != n {
		t.Errorf("navigate called %d times, want %d", got, n)
	}
	if got := fake.callCount("click"); got != n {
		t.Errorf("click called %d times, want %d", got, n)
	}
}

// TestManager_TabsNeverLaunches mirrors TestManager_WaitNeverLaunches:
// listing tabs on a session that was never launched has nothing to list.
func TestManager_TabsNeverLaunches(t *testing.T) {
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Fatal("newSession should not be called")
			return nil, nil
		},
		findBinary:   func() (string, error) { t.Fatal("findBinary should not be called"); return "", nil },
		mkProfileDir: func() (string, error) { t.Fatal("mkProfileDir should not be called"); return "", nil },
	}
	tabs, err := m.Tabs(context.Background())
	if err != nil || tabs != nil {
		t.Fatalf("Tabs on an unlaunched manager = (%v, %v), want (nil, nil)", tabs, err)
	}
}

func TestManager_TabsReflectsRegistry(t *testing.T) {
	want := []TabInfo{{Index: 0, ID: "a", URL: "https://a.example", Active: true}, {Index: 1, ID: "b", URL: "https://b.example"}}
	fake := &fakeSession{tabsResult: want}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://a.example", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	got, err := m.Tabs(context.Background())
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(got) != len(want) || got[1].URL != "https://b.example" {
		t.Errorf("Tabs = %+v, want %+v", got, want)
	}
}

func TestManager_StatusReportsTabCount(t *testing.T) {
	fake := &fakeSession{pages: 3}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if st := m.Status(); st.TabCount != 3 {
		t.Errorf("Status.TabCount = %d, want 3", st.TabCount)
	}
}

func TestManager_SwitchTabPropagatesOutOfRangeError(t *testing.T) {
	fake := &fakeSession{switchTabErr: fmt.Errorf("%w: index 5, have 1 tab(s)", ErrTabNotFound)}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.SwitchTab(context.Background(), 5); !errors.Is(err, ErrTabNotFound) {
		t.Errorf("SwitchTab: got %v, want ErrTabNotFound", err)
	}
}

func TestManager_SwitchTabOnUnlaunchedSessionErrors(t *testing.T) {
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Fatal("newSession should not be called")
			return nil, nil
		},
		findBinary:   func() (string, error) { t.Fatal("findBinary should not be called"); return "", nil },
		mkProfileDir: func() (string, error) { t.Fatal("mkProfileDir should not be called"); return "", nil },
	}
	if err := m.SwitchTab(context.Background(), 0); !errors.Is(err, ErrTabNotFound) {
		t.Errorf("SwitchTab on unlaunched manager: got %v, want ErrTabNotFound", err)
	}
}

func TestManager_CloseTabPropagatesToSession(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.CloseTab(context.Background(), 1); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	if fake.callCount("closeTab:1") != 1 {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestManager_CloseTabPropagatesError(t *testing.T) {
	fake := &fakeSession{closeTabErr: errors.New("cannot close the only remaining tab; use browser_close to end the session instead")}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.CloseTab(context.Background(), 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestManager_CloseTabOnUnlaunchedSessionErrors(t *testing.T) {
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Fatal("newSession should not be called")
			return nil, nil
		},
		findBinary:   func() (string, error) { t.Fatal("findBinary should not be called"); return "", nil },
		mkProfileDir: func() (string, error) { t.Fatal("mkProfileDir should not be called"); return "", nil },
	}
	if err := m.CloseTab(context.Background(), 0); !errors.Is(err, ErrTabNotFound) {
		t.Errorf("CloseTab on unlaunched manager: got %v, want ErrTabNotFound", err)
	}
}

func TestManager_UploadPropagatesToSession(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if err := m.Upload(context.Background(), "#file", []string{"/tmp/a.txt", "/tmp/b.txt"}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if fake.callCount("setFiles:#file:[/tmp/a.txt /tmp/b.txt]") != 1 {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestManager_UploadPropagatesError(t *testing.T) {
	fake := &fakeSession{setFilesErr: errors.New("boom")}
	m := newTestManager(fake, t.TempDir())
	if err := m.Upload(context.Background(), "#file", []string{"/tmp/a.txt"}); err == nil {
		t.Fatal("expected error")
	}
}

// TestManager_DownloadClickMovesFileToDestPath is the core payoff of
// Manager.Download: the fake writes its content under a scratch temp dir
// (simulating what Chrome does), and Download must stat, move, and clean
// that up itself — this exercises the real filesystem logic, not just
// call plumbing.
func TestManager_DownloadClickMovesFileToDestPath(t *testing.T) {
	fake := &fakeSession{
		downloadResult:  &proto.PageDownloadWillBegin{GUID: "guid-1", SuggestedFilename: "report.pdf"},
		downloadContent: []byte("pdf bytes here"),
	}
	m := newTestManager(fake, t.TempDir())
	dest := filepath.Join(t.TempDir(), "nested", "report.pdf")

	res, err := m.Download(context.Background(), "#dl", "", dest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Path != dest || res.SuggestedFilename != "report.pdf" || res.SizeBytes != int64(len("pdf bytes here")) {
		t.Errorf("unexpected result: %+v", res)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("dest file missing: %v", err)
	}
	if string(data) != "pdf bytes here" {
		t.Errorf("dest content = %q", data)
	}
	if fake.callCount("downloadViaClick:#dl") != 1 {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestManager_DownloadViaURLUsesNavigatePath(t *testing.T) {
	fake := &fakeSession{downloadResult: &proto.PageDownloadWillBegin{GUID: "guid-2", SuggestedFilename: "x.bin"}}
	m := newTestManager(fake, t.TempDir())
	dest := filepath.Join(t.TempDir(), "x.bin")

	if _, err := m.Download(context.Background(), "", "https://example.com/x.bin", dest); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if fake.callCount("downloadViaURL:https://example.com/x.bin") != 1 {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestManager_DownloadPropagatesError(t *testing.T) {
	fake := &fakeSession{downloadErr: fmt.Errorf("%w: no download event", ErrDownloadFailed)}
	m := newTestManager(fake, t.TempDir())
	dest := filepath.Join(t.TempDir(), "x.bin")

	if _, err := m.Download(context.Background(), "#dl", "", dest); !errors.Is(err, ErrDownloadFailed) {
		t.Errorf("Download: got %v, want ErrDownloadFailed", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest file should not exist after a failed download")
	}
}
