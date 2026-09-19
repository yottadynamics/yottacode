package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// handoffHarness wires a Manager with one fake per launch mode so tests can
// tell which kind of session Handoff created, and hands out a fresh temp
// profile dir per launch so the old-vs-new profile swap is observable.
type handoffHarness struct {
	m        *Manager
	headless *fakeSession
	headed   *fakeSession
	dirs     []string
	headedN  int
}

func newHandoffHarness(t *testing.T, headless, headed *fakeSession) *handoffHarness {
	t.Helper()
	h := &handoffHarness{headless: headless, headed: headed}
	h.m = &Manager{
		newSession:       func(context.Context, string, string) (pageSession, error) { return headless, nil },
		findBinary:       func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir:     func() (string, error) { d := t.TempDir(); h.dirs = append(h.dirs, d); return d, nil },
		newHeadedSession: func(context.Context, string, string) (pageSession, error) { h.headedN++; return headed, nil },
		hasDisplay:       func() bool { return true },
	}
	return h
}

func TestManager_HandoffSwapsToVisibleSessionAtSameURL(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://streeteasy.com/for-rent/long-island-city", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	headlessDir := h.m.Status().ProfileDir

	res, err := h.m.Handoff(ctx)
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if res.URL != "https://streeteasy.com/for-rent/long-island-city" || res.AlreadyVisible || res.LoadWarning != "" {
		t.Errorf("unexpected result: %+v", res)
	}
	if got := h.headed.callCount("navigate"); got != 1 {
		t.Errorf("headed session navigate calls = %d, want 1 (reopen the same URL)", got)
	}
	if h.headed.url != "https://streeteasy.com/for-rent/long-island-city" {
		t.Errorf("headed session opened %q", h.headed.url)
	}
	if h.headless.callCount("close") != 1 {
		t.Errorf("headless session was not closed: %v", h.headless.calls)
	}

	st := h.m.Status()
	if !st.Active || !st.Headed {
		t.Errorf("Status after handoff = %+v, want Active && Headed", st)
	}
	if st.ProfileDir == headlessDir {
		t.Error("headed session reused the headless profile dir; want a fresh isolated one")
	}
	if _, err := os.Stat(headlessDir); !os.IsNotExist(err) {
		t.Errorf("headless profile dir %s should be removed (err=%v)", headlessDir, err)
	}

	// Later actions drive the visible session, not the old one.
	if _, err := h.m.Inspect(ctx, ""); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if h.headed.callCount("inspect") != 1 || h.headless.callCount("inspect") != 0 {
		t.Errorf("Inspect went to the wrong session: headed=%v headless=%v", h.headed.calls, h.headless.calls)
	}
}

func TestManager_HandoffWithoutPriorSessionOpensBlankWindow(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	res, err := h.m.Handoff(context.Background())
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if res.URL != "" {
		t.Errorf("URL = %q, want empty", res.URL)
	}
	if h.headed.callCount("navigate") != 0 {
		t.Error("nothing to reopen, but the headed session navigated")
	}
	if !h.m.Status().Headed {
		t.Error("expected Headed after handoff")
	}
}

func TestManager_HandoffDoesNotCarryOverNonWebURL(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{url: "about:blank"}, &fakeSession{})
	if _, err := h.m.Navigate(context.Background(), "about:blank", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	res, err := h.m.Handoff(context.Background())
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if res.URL != "" || h.headed.callCount("navigate") != 0 {
		t.Errorf("about:blank should not be reopened: res=%+v calls=%v", res, h.headed.calls)
	}
}

func TestManager_HandoffNoDisplayLeavesHeadlessUntouched(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	h.m.hasDisplay = func() bool { return false }
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := h.m.Handoff(ctx)
	if !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("err = %v, want ErrNoDisplay", err)
	}
	if h.headedN != 0 {
		t.Error("launched a headed browser despite no display")
	}
	if h.headless.callCount("close") != 0 {
		t.Error("headless session was closed even though the handoff failed")
	}
	if st := h.m.Status(); !st.Active || st.Headed {
		t.Errorf("Status = %+v, want the original headless session still active", st)
	}
}

func TestManager_HandoffLaunchFailureLeavesHeadlessUntouched(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	h.m.newHeadedSession = func(context.Context, string, string) (pageSession, error) {
		return nil, fmt.Errorf("%w: boom", ErrLaunchFailed)
	}
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if _, err := h.m.Handoff(ctx); !errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("err = %v, want ErrLaunchFailed", err)
	}
	if h.headless.callCount("close") != 0 {
		t.Error("headless session was closed even though the headed launch failed")
	}
	if st := h.m.Status(); !st.Active || st.Headed {
		t.Errorf("Status = %+v, want the original headless session still active", st)
	}
	// The dir minted for the failed headed launch must not leak.
	failedDir := h.dirs[len(h.dirs)-1]
	if _, err := os.Stat(failedDir); !os.IsNotExist(err) {
		t.Errorf("profile dir for the failed launch (%s) was not removed (err=%v)", failedDir, err)
	}
}

func TestManager_HandoffWhenAlreadyHeadedIsNoop(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := h.m.Handoff(ctx); err != nil {
		t.Fatalf("first Handoff: %v", err)
	}
	res, err := h.m.Handoff(ctx)
	if err != nil {
		t.Fatalf("second Handoff: %v", err)
	}
	if !res.AlreadyVisible {
		t.Errorf("second Handoff = %+v, want AlreadyVisible", res)
	}
	if h.headedN != 1 {
		t.Errorf("headed launches = %d, want 1 (no relaunch when already visible)", h.headedN)
	}
}

func TestManager_HandoffNavigateFailureKeepsWindowWithWarning(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{navigateErr: ErrNavigationTimeout})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	res, err := h.m.Handoff(ctx)
	if err != nil {
		t.Fatalf("Handoff should still succeed when only the reload is slow: %v", err)
	}
	if res.LoadWarning == "" {
		t.Error("expected a LoadWarning")
	}
	if !h.m.Status().Headed {
		t.Error("the visible window should stay in place after a slow load")
	}
}

func TestManager_HeadedResetsToHeadlessAfterCrash(t *testing.T) {
	headless, headed := &fakeSession{}, &fakeSession{}
	h := newHandoffHarness(t, headless, headed)
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := h.m.Handoff(ctx); err != nil {
		t.Fatalf("Handoff: %v", err)
	}

	// The user closes the window (or it crashes).
	headed.mu.Lock()
	headed.dead = true
	headed.mu.Unlock()

	if _, err := h.m.Navigate(ctx, "https://example.com/next", ""); err != nil {
		t.Fatalf("Navigate after window closed: %v", err)
	}
	if h.headedN != 1 {
		t.Errorf("headed launches = %d; a closed window must not silently pop a new one", h.headedN)
	}
	if st := h.m.Status(); !st.Active || st.Headed {
		t.Errorf("Status = %+v, want a fresh headless session", st)
	}
}

func TestManager_HandoffClosedManagerDenied(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	_ = h.m.Close(context.Background())
	if _, err := h.m.Handoff(context.Background()); !errors.Is(err, ErrActionDenied) {
		t.Fatalf("err = %v, want ErrActionDenied", err)
	}
	if h.headedN != 0 {
		t.Error("a closed manager must never launch a window")
	}
}

func TestManager_HandoffUnsupportedWithoutHeadedLauncher(t *testing.T) {
	m := newTestManager(&fakeSession{}, t.TempDir())
	if _, err := m.Handoff(context.Background()); !errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("err = %v, want ErrLaunchFailed", err)
	}
}

func TestManager_HandoffCloseCleansUpHeadedSession(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := h.m.Handoff(ctx); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	headedDir := h.m.Status().ProfileDir

	if err := h.m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if h.headed.callCount("close") != 1 {
		t.Errorf("headed session not closed: %v", h.headed.calls)
	}
	if _, err := os.Stat(headedDir); !os.IsNotExist(err) {
		t.Errorf("headed profile dir %s should be removed on Close (err=%v)", headedDir, err)
	}
}

// orderedFake logs each snapshot() so a test can see when Handoff reads the
// old session relative to launching the new one.
type orderedFake struct {
	*fakeSession
	log *[]string
}

func (o *orderedFake) snapshot() (string, int) {
	*o.log = append(*o.log, "snapshot")
	return o.fakeSession.snapshot()
}

// The launch takes seconds, and the old session's last page can close during
// it; reading the old session after that is a crash waiting to happen. The URL
// must be read from the session Handoff has just confirmed alive — i.e. once,
// before the visible browser is launched.
func TestManager_HandoffReadsOldURLBeforeLaunchingVisibleBrowser(t *testing.T) {
	var order []string
	old := &orderedFake{fakeSession: &fakeSession{}, log: &order}
	headed := &fakeSession{}
	m := &Manager{
		newSession:   func(context.Context, string, string) (pageSession, error) { return old, nil },
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { return t.TempDir(), nil },
		newHeadedSession: func(context.Context, string, string) (pageSession, error) {
			order = append(order, "launch-headed")
			return headed, nil
		},
		hasDisplay: func() bool { return true },
	}
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com/page", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	res, err := m.Handoff(ctx)
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if got := strings.Join(order, ","); got != "snapshot,launch-headed" {
		t.Errorf("order = %q, want the old session read exactly once, before the launch", got)
	}
	if res.URL != "https://example.com/page" {
		t.Errorf("URL = %q, want the page the old session was on", res.URL)
	}
}

func TestManager_HandoffBinaryNotFoundLeavesHeadlessUntouched(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	h.m.findBinary = func() (string, error) { return "", fmt.Errorf("%w: searched nowhere", ErrNoBinaryFound) }

	if _, err := h.m.Handoff(ctx); !errors.Is(err, ErrNoBinaryFound) {
		t.Fatalf("err = %v, want ErrNoBinaryFound", err)
	}
	if h.headedN != 0 || h.headless.callCount("close") != 0 {
		t.Errorf("a failed handoff touched something: headedN=%d headless=%v", h.headedN, h.headless.calls)
	}
	if st := h.m.Status(); !st.Active || st.Headed {
		t.Errorf("Status = %+v, want the original headless session still active", st)
	}
}

func TestManager_HandoffProfileDirFailureLeavesHeadlessUntouched(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	boom := errors.New("disk full")
	h.m.mkProfileDir = func() (string, error) { return "", boom }

	if _, err := h.m.Handoff(ctx); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the mkProfileDir error", err)
	}
	if h.headedN != 0 || h.headless.callCount("close") != 0 {
		t.Errorf("a failed handoff touched something: headedN=%d headless=%v", h.headedN, h.headless.calls)
	}
}

// A session that is already dead when Handoff starts (the user closed the
// window, Chrome crashed) is cleaned up, and the visible window opens blank:
// there is no live page to carry over.
func TestManager_HandoffDiscardsAlreadyDeadSession(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	h.headless.mu.Lock()
	h.headless.dead = true
	h.headless.mu.Unlock()

	res, err := h.m.Handoff(ctx)
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if res.URL != "" || h.headed.callCount("navigate") != 0 {
		t.Errorf("nothing live to reopen, but got res=%+v headed calls=%v", res, h.headed.calls)
	}
	if h.headless.callCount("forceCleanup") != 1 || h.headless.callCount("close") != 0 {
		t.Errorf("dead session should be force-cleaned, not gracefully closed: %v", h.headless.calls)
	}
	if !h.m.Status().Headed {
		t.Error("expected a visible session after handoff")
	}
}

// The old session can die while the visible browser is launching; it must be
// reaped with forceCleanup (no live connection to close over), and the
// handoff must still succeed.
func TestManager_HandoffOldSessionDiesDuringLaunch(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	ctx := context.Background()
	if _, err := h.m.Navigate(ctx, "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	h.m.newHeadedSession = func(context.Context, string, string) (pageSession, error) {
		h.headless.mu.Lock()
		h.headless.dead = true
		h.headless.mu.Unlock()
		return h.headed, nil
	}

	if _, err := h.m.Handoff(ctx); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if h.headless.callCount("forceCleanup") != 1 || h.headless.callCount("close") != 0 {
		t.Errorf("old session died mid-launch; want forceCleanup only, got %v", h.headless.calls)
	}
	if st := h.m.Status(); !st.Active || !st.Headed {
		t.Errorf("Status = %+v, want an active visible session", st)
	}
}

// The cross-filesystem fallback must never write through a symlink planted at
// the destination after the caller validated the path (os.Rename replaces a
// symlink, so only the copy path can be tricked into following one).
func TestCopyThenRemove_RefusesSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("downloaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.Symlink(victim, dst); err != nil {
		t.Fatal(err)
	}

	if err := copyThenRemove(src, dst); err == nil {
		t.Fatal("copyThenRemove wrote through a symlink destination")
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Errorf("symlink target was modified: %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source must be left in place when the copy is refused: %v", err)
	}
}

func TestCopyThenRemove_PlainCopyStillWorks(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "payload" {
		t.Errorf("dst = %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("src should be removed after a successful move (err=%v)", err)
	}
}
