//go:build integration

// Integration test for the real go-rod/CDP path: launches a system
// Chrome/Chromium, drives it against a local httptest.Server fixture
// page, and confirms the full browser_navigate → browser_screenshot →
// browser_inspect → browser_click/browser_type workflow the roadmap doc's
// Acceptance section describes. Skips cleanly when no system browser is
// found, so `go test ./...` (no -tags) never depends on one; run
// explicitly with `go test -tags integration ./internal/browser/...` on
// a machine with Chrome/Chromium installed.
package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

const fixturePage = `<!DOCTYPE html>
<html><head><title>Fixture</title></head>
<body>
  <input id="q" type="text">
  <button id="go" onclick="document.getElementById('out').textContent = 'clicked:' + document.getElementById('q').value">Go</button>
  <div id="out"></div>
</body></html>`

const alertFixturePage = `<!DOCTYPE html>
<html><head><title>Alert Fixture</title></head>
<body>
  <button id="alert-btn" onclick="alert('boo'); document.getElementById('out').textContent = 'after-alert'">Trigger</button>
  <div id="out"></div>
</body></html>`

const popupFixturePage = `<!DOCTYPE html>
<html><head><title>Popup Opener</title></head>
<body>
  <a id="popup-link" href="/popup-target" target="_blank">Open</a>
</body></html>`

const popupTargetFixturePage = `<!DOCTYPE html>
<html><head><title>Popup Target</title></head>
<body>popup landed</body></html>`

const popupAutoCloseFixturePage = `<!DOCTYPE html>
<html><head><title>Popup Opener (Autoclose)</title></head>
<body>
  <a id="popup-link" href="/popup-target-autoclose" target="_blank">Open</a>
</body></html>`

const popupTargetAutoCloseFixturePage = `<!DOCTYPE html>
<html><head><title>Popup Target (Autoclose)</title></head>
<body>
<script>setTimeout(function() { window.close(); }, 100);</script>
closing soon
</body></html>`

const uploadFixturePage = `<!DOCTYPE html>
<html><head><title>Upload Fixture</title></head>
<body>
  <input id="file-input" type="file" style="display:none" onchange="document.getElementById('out').textContent = 'file:' + this.files[0].name">
  <div id="out"></div>
</body></html>`

const uploadMultipleFixturePage = `<!DOCTYPE html>
<html><head><title>Upload Multiple Fixture</title></head>
<body>
  <input id="file-input" type="file" multiple style="display:none" onchange="document.getElementById('out').textContent = 'files:' + Array.from(this.files).map(f => f.name).sort().join(',')">
  <div id="out"></div>
</body></html>`

const downloadFixturePage = `<!DOCTYPE html>
<html><head><title>Download Fixture</title></head>
<body>
  <a id="dl-link" href="/download-file">Download</a>
</body></html>`

const downloadFileContent = "download fixture contents\n"

const consoleFixturePage = `<!DOCTYPE html>
<html><head><title>Console Fixture</title></head>
<body>
<script>
  console.log('hello', 'world');
  console.error('boom');
  setTimeout(function() { nonExistentFunction(); }, 0);
</script>
</body></html>`

const networkFixturePage = `<!DOCTYPE html>
<html><head><title>Network Fixture</title></head>
<body>
<div id="out"></div>
<script>
  Promise.allSettled([fetch('/api/ping'), fetch('/api/missing')])
    .then(function() { document.getElementById('out').textContent = 'done'; });
</script>
</body></html>`

func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download-file":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
			_, _ = w.Write([]byte(downloadFileContent))
			return
		case "/api/ping":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/api/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		page := fixturePage
		switch r.URL.Path {
		case "/alert":
			page = alertFixturePage
		case "/popup":
			page = popupFixturePage
		case "/popup-target":
			page = popupTargetFixturePage
		case "/popup-autoclose":
			page = popupAutoCloseFixturePage
		case "/popup-target-autoclose":
			page = popupTargetAutoCloseFixturePage
		case "/upload":
			page = uploadFixturePage
		case "/upload-multiple":
			page = uploadMultipleFixturePage
		case "/download":
			page = downloadFixturePage
		case "/console":
			page = consoleFixturePage
		case "/network":
			page = networkFixturePage
		}
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func skipIfNoBrowser(t *testing.T) {
	t.Helper()
	if _, err := findChromeBinary(); err != nil {
		t.Skipf("no system Chrome/Chromium found, skipping: %v", err)
	}
}

func TestIntegration_FullWorkflow(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()

	res, err := m.Navigate(ctx, srv.URL, "load")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if res.Title != "Fixture" {
		t.Errorf("Title = %q, want %q", res.Title, "Fixture")
	}

	png, err := m.Screenshot(ctx, "", false)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(png) < 8 || string(png[1:4]) != "PNG" {
		t.Errorf("Screenshot did not return PNG data (len=%d)", len(png))
	}

	tree, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.Contains(strings.ToLower(tree), "button") {
		t.Errorf("Inspect output missing the button role, got:\n%s", tree)
	}

	if err := m.Type(ctx, "#q", "hello", false); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if err := m.Click(ctx, "#go"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := m.Wait(ctx, "#out", "clicked:hello", false, 5*time.Second); err != nil {
		t.Fatalf("Wait for click result: %v", err)
	}

	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestIntegration_AlertDoesNotHangClick proves the auto-dismiss dialog
// handler in launchSession actually works against a real browser: a click
// that triggers a JS alert() must still return promptly (not block until
// a test-imposed deadline), and the session must stay usable afterward.
func TestIntegration_AlertDoesNotHangClick(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()

	if _, err := m.Navigate(ctx, srv.URL+"/alert", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- m.Click(ctx, "#alert-btn") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Click: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Click did not return within 10s — the alert() dialog is blocking the session")
	}

	if err := m.Wait(ctx, "#out", "after-alert", false, 5*time.Second); err != nil {
		t.Fatalf("session appears wedged after the alert: %v", err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestIntegration_RecoversAfterRealCrash kills the real Chrome process out
// from under an active session (SIGKILL, not browser_close) and proves the
// next action transparently relaunches a fresh browser instead of failing
// forever with an opaque dead-connection error.
func TestIntegration_RecoversAfterRealCrash(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	firstSess, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T, want *session", m.sess)
	}
	firstPID := firstSess.launcher.PID()
	if firstPID <= 0 {
		t.Fatalf("launcher reported no PID: %d", firstPID)
	}

	if err := syscall.Kill(firstPID, syscall.SIGKILL); err != nil {
		t.Fatalf("failed to kill pid %d to simulate a crash: %v", firstPID, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(firstPID, syscall.Signal(0)) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d did not die within 5s of SIGKILL", firstPID)
		}
		time.Sleep(20 * time.Millisecond)
	}

	res, err := m.Navigate(ctx, srv.URL, "load")
	if err != nil {
		t.Fatalf("Navigate after crash: %v", err)
	}
	if res.Title != "Fixture" {
		t.Errorf("Title = %q after recovery, want %q", res.Title, "Fixture")
	}

	secondSess, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T, want *session", m.sess)
	}
	if secondSess.launcher.PID() == firstPID {
		t.Error("recovered session reused the same pid — expected a genuinely new process")
	}

	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestIntegration_CleanupLeavesNoProcessOrTempDir drives a real session
// then closes it, and checks both that the launched OS process is gone
// and that the isolated temp profile directory was removed — the
// Acceptance section's "session teardown always closes the browser
// process and removes the temp profile directory" bar.
func TestIntegration_CleanupLeavesNoProcessOrTempDir(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	st := m.Status()
	if !st.Active || st.ProfileDir == "" {
		t.Fatalf("expected an active session with a profile dir, got %+v", st)
	}
	profileDir := st.ProfileDir

	sess, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T, want *session (is this running against a fake?)", m.sess)
	}
	pid := sess.launcher.PID()
	if pid <= 0 {
		t.Fatalf("launcher reported no PID: %d", pid)
	}

	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Errorf("profile dir %s still exists after Close (err=%v)", profileDir, err)
	}

	// A zero signal doesn't actually signal the process — it only checks
	// whether it (or its PID, reused by the OS) is still alive.
	if err := syscall.Kill(pid, syscall.Signal(0)); err == nil {
		t.Errorf("pid %d is still alive after Close", pid)
	}
}

// TestIntegration_ClickFollowsNewTab proves the real gap this feature set
// closes: clicking a target="_blank" link against a real browser actually
// switches the active page, not just leaves the original page's URL
// unchanged while a popup silently opens in the background.
func TestIntegration_ClickFollowsNewTab(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/popup", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if st := m.Status(); st.TabCount != 1 {
		t.Fatalf("TabCount before click = %d, want 1", st.TabCount)
	}

	if err := m.Click(ctx, "#popup-link"); err != nil {
		t.Fatalf("Click: %v", err)
	}

	st := m.Status()
	if st.TabCount != 2 {
		t.Fatalf("TabCount after click = %d, want 2", st.TabCount)
	}
	if !strings.Contains(st.CurrentURL, "/popup-target") {
		t.Errorf("active page did not follow the new tab: CurrentURL = %q", st.CurrentURL)
	}

	tabs, err := m.Tabs(ctx)
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(tabs) != 2 {
		t.Fatalf("Tabs returned %d entries, want 2: %+v", len(tabs), tabs)
	}
	if !tabs[0].Active && !tabs[1].Active {
		t.Errorf("no tab reported Active: %+v", tabs)
	}

	// SwitchTab back to the original page should work too.
	if err := m.SwitchTab(ctx, 0); err != nil {
		t.Fatalf("SwitchTab: %v", err)
	}
	if st := m.Status(); !strings.Contains(st.CurrentURL, "/popup") || strings.Contains(st.CurrentURL, "/popup-target") {
		t.Errorf("SwitchTab(0) did not restore the original page: CurrentURL = %q", st.CurrentURL)
	}
}

// TestIntegration_Upload proves Upload actually sets a real file input's
// files via CDP against a real browser, including the common
// display:none pattern the fixture uses.
func TestIntegration_Upload(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/upload", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	tmpFile := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(tmpFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write fixture upload file: %v", err)
	}

	if err := m.Upload(ctx, "#file-input", []string{tmpFile}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if err := m.Wait(ctx, "#out", "file:hello.txt", false, 5*time.Second); err != nil {
		t.Fatalf("page did not observe the uploaded file: %v", err)
	}
}

// TestIntegration_DownloadViaClick proves Download drives a real
// Chrome-initiated download (a Content-Disposition: attachment response)
// through to a file at the caller's requested destination, with the
// scratch temp dir cleaned up afterward.
func TestIntegration_DownloadViaClick(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/download", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "saved", "report.txt")
	res, err := m.Download(ctx, "#dl-link", "", dest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Path != dest {
		t.Errorf("DownloadResult.Path = %q, want %q", res.Path, dest)
	}
	if res.SuggestedFilename != "report.txt" {
		t.Errorf("SuggestedFilename = %q, want %q", res.SuggestedFilename, "report.txt")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("downloaded file missing at %s: %v", dest, err)
	}
	if string(data) != downloadFileContent {
		t.Errorf("downloaded content = %q, want %q", data, downloadFileContent)
	}
}

// TestIntegration_DownloadViaURL covers the other trigger path
// (navigating directly to a downloadable URL rather than clicking an
// element) — TestIntegration_DownloadViaClick only exercises the
// click-triggered variant.
func TestIntegration_DownloadViaURL(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	dest := filepath.Join(t.TempDir(), "report.txt")
	res, err := m.Download(ctx, "", srv.URL+"/download-file", dest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.SuggestedFilename != "report.txt" {
		t.Errorf("SuggestedFilename = %q, want %q", res.SuggestedFilename, "report.txt")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("downloaded file missing at %s: %v", dest, err)
	}
	if string(data) != downloadFileContent {
		t.Errorf("downloaded content = %q, want %q", data, downloadFileContent)
	}
}

// TestIntegration_UploadMultipleFiles covers the multi-file case —
// TestIntegration_Upload only exercises a single file.
func TestIntegration_UploadMultipleFiles(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/upload-multiple", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(fileA, []byte("a"), 0o644); err != nil {
		t.Fatalf("write fixture file a: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("b"), 0o644); err != nil {
		t.Fatalf("write fixture file b: %v", err)
	}

	if err := m.Upload(ctx, "#file-input", []string{fileA, fileB}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if err := m.Wait(ctx, "#out", "files:a.txt,b.txt", false, 5*time.Second); err != nil {
		t.Fatalf("page did not observe both uploaded files: %v", err)
	}
}

// TestIntegration_BackgroundTabSelfCloseFallsBackToRemainingTab is the
// end-to-end proof for the untrackPage fix: open a popup (auto-followed,
// so it becomes the active tab), let it close itself via window.close()
// (a real Chrome-permitted self-close since it's a script-opened
// window), and confirm the active tab falls back to the original page
// rather than being left pointing at a page that no longer exists.
func TestIntegration_BackgroundTabSelfCloseFallsBackToRemainingTab(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/popup-autoclose", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Click(ctx, "#popup-link"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if st := m.Status(); st.TabCount != 2 || !strings.Contains(st.CurrentURL, "/popup-target-autoclose") {
		t.Fatalf("expected the click to follow the new tab, got %+v", st)
	}

	// The popup closes itself ~100ms after load; poll until the manager
	// observes it, bounded well above that.
	deadline := time.Now().Add(5 * time.Second)
	var st Status
	for time.Now().Before(deadline) {
		st = m.Status()
		if st.TabCount == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.TabCount != 1 {
		t.Fatalf("popup tab never closed (TabCount stuck at %d)", st.TabCount)
	}
	if !strings.Contains(st.CurrentURL, "/popup-autoclose") || strings.Contains(st.CurrentURL, "/popup-target-autoclose") {
		tabs, _ := m.Tabs(ctx)
		t.Errorf("active tab did not fall back to the remaining page: CurrentURL = %q, tabs = %+v", st.CurrentURL, tabs)
	}

	// The session must still be fully usable against the surviving page.
	if _, err := m.Screenshot(ctx, "", false); err != nil {
		t.Fatalf("session unusable after popup self-close: %v", err)
	}
}

// TestIntegration_CloseTab proves CloseTab actually closes the real CDP
// target (not just the local registry entry) and that the session stays
// usable against the remaining page afterward, using an explicit
// browser_close_tab call rather than the page closing itself.
func TestIntegration_CloseTab(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/popup", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Click(ctx, "#popup-link"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if st := m.Status(); st.TabCount != 2 {
		t.Fatalf("expected 2 tabs after the popup opened, got %d", st.TabCount)
	}

	// The popup is active (index 1, auto-followed by the click); close it
	// explicitly rather than switching back first, to prove closing the
	// *active* tab correctly falls back too.
	if err := m.CloseTab(ctx, 1); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	st := m.Status()
	if st.TabCount != 1 {
		t.Fatalf("TabCount after CloseTab = %d, want 1", st.TabCount)
	}
	if !strings.Contains(st.CurrentURL, "/popup") || strings.Contains(st.CurrentURL, "/popup-target") {
		t.Errorf("active tab did not fall back to the remaining page: CurrentURL = %q", st.CurrentURL)
	}
	if _, err := m.Screenshot(ctx, "", false); err != nil {
		t.Fatalf("session unusable after CloseTab: %v", err)
	}

	// Closing the last remaining tab must be refused, not silently leave
	// the session with zero tracked pages.
	if err := m.CloseTab(ctx, 0); err == nil {
		t.Error("expected CloseTab to refuse closing the only remaining tab")
	}
}

// TestIntegration_ConsoleLogsCaptured proves console capture is wired
// against a real page: console.log/console.error calls and an uncaught
// exception (fired via setTimeout, so it happens after the page has
// already loaded — proving capture isn't a one-shot "read at load time"
// but a continuous subscription) all show up in ConsoleLogs.
func TestIntegration_ConsoleLogsCaptured(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/console", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	var entries []ConsoleEntry
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		entries, err = m.ConsoleLogs(ctx, 0)
		if err != nil {
			t.Fatalf("ConsoleLogs: %v", err)
		}
		if len(entries) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(entries) < 3 {
		t.Fatalf("got %d console entries, want at least 3: %+v", len(entries), entries)
	}

	var sawLog, sawError, sawException bool
	for _, e := range entries {
		switch {
		case e.Level == "log" && strings.Contains(e.Text, "hello") && strings.Contains(e.Text, "world"):
			sawLog = true
		case e.Level == "error" && strings.Contains(e.Text, "boom"):
			sawError = true
		case e.Level == "exception" && strings.Contains(e.Text, "nonExistentFunction"):
			sawException = true
		}
	}
	if !sawLog || !sawError || !sawException {
		t.Errorf("missing expected entries (log=%t error=%t exception=%t): %+v", sawLog, sawError, sawException, entries)
	}
}

// TestIntegration_NetworkRequestsCaptured proves network capture is
// wired against a real page: a successful fetch and a failed (404)
// fetch both show up with the right method/status.
func TestIntegration_NetworkRequestsCaptured(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/network", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Wait(ctx, "#out", "done", false, 5*time.Second); err != nil {
		t.Fatalf("page's fetches never settled: %v", err)
	}

	entries, err := m.NetworkRequests(ctx, 0)
	if err != nil {
		t.Fatalf("NetworkRequests: %v", err)
	}

	var sawOK, sawNotFound bool
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.URL, "/api/ping") && e.Status == 200:
			sawOK = true
		case strings.HasSuffix(e.URL, "/api/missing") && e.Status == 404:
			sawNotFound = true
		}
	}
	if !sawOK || !sawNotFound {
		t.Errorf("missing expected entries (ok=%t notFound=%t): %+v", sawOK, sawNotFound, entries)
	}
}

// TestIntegration_HandoffOpensVisibleIsolatedSession proves the real
// headless -> headed swap browser_handoff performs: the visible browser is
// a genuinely different (non-headless) process on a fresh isolated profile,
// it reopens the page the headless session was on, the old process and
// profile are gone, and the session keeps working afterwards. Needs a
// display, so it skips on hosts without one (run under xvfb-run in CI).
func TestIntegration_HandoffOpensVisibleIsolatedSession(t *testing.T) {
	skipIfNoBrowser(t)
	if !displayAvailable() {
		t.Skip("no display available for a headed browser; run under xvfb-run")
	}
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	before, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T, want *session", m.sess)
	}
	if !before.launcher.Has(flags.Headless) {
		t.Fatal("precondition: the initial session should be headless")
	}
	oldPID := before.launcher.PID()
	oldProfile := m.Status().ProfileDir

	res, err := m.Handoff(ctx)
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if !strings.HasPrefix(res.URL, srv.URL) {
		t.Errorf("Handoff reopened %q, want the page the headless session was on (%s)", res.URL, srv.URL)
	}
	if res.LoadWarning != "" {
		t.Errorf("unexpected LoadWarning: %s", res.LoadWarning)
	}

	after, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T after handoff, want *session", m.sess)
	}
	if after.launcher.Has(flags.Headless) {
		t.Error("session after Handoff is still headless")
	}
	st := m.Status()
	if !st.Headed || !st.Active {
		t.Errorf("Status = %+v, want Active && Headed", st)
	}
	if st.ProfileDir == oldProfile {
		t.Error("headed session reused the headless profile dir")
	}
	if _, err := os.Stat(oldProfile); !os.IsNotExist(err) {
		t.Errorf("headless profile dir %s still exists (err=%v)", oldProfile, err)
	}
	if err := syscall.Kill(oldPID, syscall.Signal(0)); err == nil {
		t.Errorf("headless browser pid %d is still alive after Handoff", oldPID)
	}
	if st.CurrentURL == "" || !strings.HasPrefix(st.CurrentURL, srv.URL) {
		t.Errorf("visible page URL = %q, want it on %s", st.CurrentURL, srv.URL)
	}

	// The visible session is a normal session: it keeps serving actions.
	if err := m.Type(ctx, "#q", "visible", false); err != nil {
		t.Fatalf("Type in headed session: %v", err)
	}
	if err := m.Click(ctx, "#go"); err != nil {
		t.Fatalf("Click in headed session: %v", err)
	}
	if err := m.Wait(ctx, "#out", "clicked:visible", false, 5*time.Second); err != nil {
		t.Fatalf("Wait in headed session: %v", err)
	}

	headedProfile := st.ProfileDir
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(headedProfile); !os.IsNotExist(err) {
		t.Errorf("headed profile dir %s still exists after Close (err=%v)", headedProfile, err)
	}
}

// TestIntegration_HandoffStaysIsolatedAndDoesNotShareCookies pins what the
// handoff window is and isn't. It IS still an isolated yottacode browser (a
// yottacode-browser-* temp profile, never a real Chrome profile), and it
// starts from a fresh cookie jar: a cookie the headless session earned is not
// sent by the visible one. That second half is also the reason solving a
// challenge in the user's *normal* browser can't unblock the agent — separate
// profiles never share cookies — and why the challenge has to be completed in
// this window instead.
func TestIntegration_HandoffStaysIsolatedAndDoesNotShareCookies(t *testing.T) {
	skipIfNoBrowser(t)
	if !displayAvailable() {
		t.Skip("no display available for a headed browser; run under xvfb-run")
	}

	// The server records the "sid" cookie each page load arrives with, and
	// always tries to set one.
	var mu syncutil.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		got := ""
		if c, err := r.Cookie("sid"); err == nil {
			got = c.Value
		}
		mu.Lock()
		seen = append(seen, got)
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "earned-in-headless", Path: "/"})
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><title>Cookie Fixture</title><body>ok</body>"))
	}))
	t.Cleanup(srv.Close)

	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	// Twice in the headless session: the second load proves the cookie really
	// is sent back within one profile, so "no cookie" after handoff means
	// something.
	for i := 0; i < 2; i++ {
		if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
			t.Fatalf("Navigate #%d: %v", i+1, err)
		}
	}
	if _, err := m.Handoff(ctx); err != nil {
		t.Fatalf("Handoff: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	want := []string{"", "earned-in-headless", ""}
	if len(got) != len(want) {
		t.Fatalf("page loads saw cookies %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("load #%d saw cookie %q, want %q (all: %q)", i+1, got[i], want[i], got)
		}
	}

	after, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T, want *session", m.sess)
	}
	dir := after.launcher.Get(flags.UserDataDir)
	t.Logf("visible session: headless=%t user-data-dir=%s", after.launcher.Has(flags.Headless), dir)
	if !strings.HasPrefix(filepath.Base(dir), "yottacode-browser-") {
		t.Errorf("visible session profile %q is not a yottacode isolated profile", dir)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, filepath.Join(home, ".config")) {
		t.Errorf("visible session profile %q is inside the user's real config dir", dir)
	}
	if dir != m.Status().ProfileDir {
		t.Errorf("launcher profile %q != manager profile %q", dir, m.Status().ProfileDir)
	}
}

// TestIntegration_HandoffViewportFollowsWindow guards a bug seen on a real
// desktop: rod emulates a fixed 1280x800 laptop viewport on every page by
// default, which is right for a headless session but in a visible window
// leaves the page pinned to the top-left corner with dead space around it.
// The handoff window must let the page fill the window, so resizing the
// window has to resize the page's viewport.
func TestIntegration_HandoffViewportFollowsWindow(t *testing.T) {
	skipIfNoBrowser(t)
	if !displayAvailable() {
		t.Skip("no display available for a headed browser; run under xvfb-run")
	}
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := m.Handoff(ctx); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	sess, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T, want *session", m.sess)
	}
	pg := sess.activePage()
	win, err := proto.BrowserGetWindowForTarget{TargetID: pg.TargetID}.Call(sess.browser)
	if err != nil {
		t.Fatalf("Browser.getWindowForTarget: %v", err)
	}

	// Both widths differ clearly from the 1280 rod would pin the page to, and
	// both fit any real screen: a desktop window manager (macOS in
	// particular) clamps a window to the screen, so anything wider than a
	// small CI display would make this flaky.
	for _, want := range []int{700, 1000} {
		h := 900
		if err := (proto.BrowserSetWindowBounds{
			WindowID: win.WindowID,
			Bounds:   &proto.BrowserBounds{Width: &want, Height: &h},
		}).Call(sess.browser); err != nil {
			t.Fatalf("Browser.setWindowBounds(%d): %v", want, err)
		}
		var got int
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			res, err := pg.Eval("() => window.innerWidth")
			if err != nil {
				t.Fatalf("Eval innerWidth: %v", err)
			}
			got = res.Value.Int()
			if got >= want-40 && got <= want+40 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if got < want-40 || got > want+40 {
			t.Errorf("window resized to %dpx wide but page viewport is %dpx: the page is not filling the window", want, got)
		}
	}
}
