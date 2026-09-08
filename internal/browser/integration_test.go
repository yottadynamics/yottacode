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
	"strings"
	"syscall"
	"testing"
	"time"
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

func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		page := fixturePage
		if r.URL.Path == "/alert" {
			page = alertFixturePage
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
