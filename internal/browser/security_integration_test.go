//go:build integration

// Real-Chrome checks of the browser tools' security posture. Each one pins a
// behavior that was verified by hand against a live browser when the hardening
// was added, so a regression shows up here rather than in the field. Skips
// cleanly with no system browser, like the rest of the integration suite.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fileCanary = "AWS_SECRET_ACCESS_KEY=CANARY-must-not-reach-the-agent"

// writeCanary drops a credential-shaped file the browser could read via
// file:// if it were allowed to.
func writeCanary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(fileCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Before the URL policy, browser_navigate("file:///…/.env") + browser_inspect
// returned the file's contents to the model — a way around the read deny list
// that every read tool enforces. Now every non-web scheme is refused, and
// refused BEFORE a browser is started.
func TestIntegration_NavigateBlocksNonWebSchemes(t *testing.T) {
	skipIfNoBrowser(t)
	canary := writeCanary(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	for _, u := range []string{
		"file://" + canary,
		"file://localhost" + canary,
		"chrome://version",
		"chrome://settings/passwords",
		"view-source:https://example.com",
		"javascript:document.title='ran'",
		"data:text/html,<title>x</title>",
	} {
		if _, err := m.Navigate(ctx, u, "load"); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("Navigate(%q) = %v, want ErrBlockedURL", u, err)
		}
	}
	if m.Status().Active {
		t.Error("a refused URL must not have launched a browser")
	}

	// The download-by-URL form navigates too, so it gets the same policy.
	if _, err := m.Download(ctx, "", "file://"+canary, filepath.Join(t.TempDir(), "leak")); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("Download(file://…) = %v, want ErrBlockedURL", err)
	}

	// And an ordinary URL still works after all those refusals.
	srv := newFixtureServer(t)
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate(%s) after refusals: %v", srv.URL, err)
	}
}

// The URL policy only covers URLs the agent supplies. The other way to reach a
// blocked scheme is for a loaded page to link or redirect there. Chrome itself
// refuses that for web pages; this pins it, since the design relies on it.
func TestIntegration_WebPageCannotNavigateToFile(t *testing.T) {
	skipIfNoBrowser(t)
	canary := writeCanary(t)
	fileURL := "file://" + canary
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><title>hostile</title><body>
<a id="link" href="%[1]s">local file</a>
<button id="assign" onclick="location.href='%[1]s'">assign</button>
<button id="open" onclick="window.open('%[1]s')">open</button>
</body>`, fileURL)
	}))
	t.Cleanup(srv.Close)

	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	for _, sel := range []string{"#link", "#assign", "#open"} {
		if err := m.Click(ctx, sel); err != nil {
			t.Fatalf("Click(%s): %v", sel, err)
		}
		time.Sleep(700 * time.Millisecond)

		tabs, err := m.Tabs(ctx)
		if err != nil {
			t.Fatalf("Tabs: %v", err)
		}
		for _, tab := range tabs {
			if strings.HasPrefix(tab.URL, "file:") {
				t.Errorf("clicking %s let a web page open %s", sel, tab.URL)
			}
		}
		if tree, err := m.Inspect(ctx, ""); err == nil && strings.Contains(tree, "CANARY-must-not-reach-the-agent") {
			t.Errorf("clicking %s exposed the local file's contents to the agent", sel)
		}
	}
}

// The launch flags rod ships weaken the browser's process isolation; they are
// undone, and Chrome's own sandbox is never disabled.
func TestIntegration_LaunchIsHardened(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	s, ok := m.sess.(*session)
	if !ok {
		t.Fatalf("m.sess is %T, want *session", m.sess)
	}
	args := strings.Join(s.launcher.FormatArgs(), " ")
	for _, bad := range []string{"site-per-process", "disable-site-isolation-trials", "NetworkServiceInProcess", "no-sandbox", "disable-web-security"} {
		if strings.Contains(args, bad) {
			t.Errorf("launch arguments contain %q:\n%s", bad, args)
		}
	}
	// Not a security flag: the tools depend on it.
	if !strings.Contains(args, "remote-debugging-port") {
		t.Errorf("the debugging port flag is gone; the tools can't drive the browser:\n%s", args)
	}
}

// rod extracts its leakless guard to a predictable /tmp path and runs whatever
// is there. The launch verifies the helper is ours and locks its directory down
// first, so another local user can't pre-plant a program at that path.
func TestIntegration_LeaklessHelperIsPrivate(t *testing.T) {
	skipIfNoBrowser(t)
	if !leaklessUsable() {
		t.Skip("leakless guard not usable on this host; the browser launches without it")
	}
	// leaklessUsable ran GetLeaklessBin and verified/tightened the helper.
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	srv := newFixtureServer(t)
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	s := m.sess.(*session)
	if s.launcher.PID() <= 0 {
		t.Fatal("no browser pid")
	}
	if err := syscall.Kill(s.launcher.PID(), syscall.Signal(0)); err != nil {
		t.Fatalf("browser should be running: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "leakless-*"))
	if len(matches) == 0 {
		t.Skip("leakless helper directory not found; nothing to check")
	}
	for _, dir := range matches {
		fi, err := os.Lstat(dir)
		if err != nil {
			continue
		}
		owner, _ := fileOwner(fi)
		if owner != os.Getuid() {
			continue // someone else's; we would not have run it
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %o; our helper directory must not be open to group/other", dir, perm)
		}
	}
}

// End to end against real Chrome: a session killed with SIGKILL leaves its
// profile (and the cookies in it) behind, the next launch's sweep removes it —
// and a session that is still running is never touched, even when its
// directory is old enough to look abandoned. The second half is the one that
// matters: it proves Chrome's own lock file is what keeps a live profile safe.
func TestIntegration_SweepRemovesKilledSessionsProfileButNotALiveOne(t *testing.T) {
	skipIfNoBrowser(t)
	// Profiles are made under the temp dir; point it at a private one so this
	// test can only ever see (and delete) directories it created itself. Keep
	// the path SHORT: Chrome puts a unix socket under TMPDIR, and a long path
	// (t.TempDir() names one after the test) exceeds the 108-byte limit.
	tmp, err := os.MkdirTemp("", "yc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("TMPDIR", tmp)
	srv := newFixtureServer(t)
	ctx := context.Background()

	live := NewManager()
	t.Cleanup(func() { _ = live.Close(ctx) })
	if _, err := live.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("live Navigate: %v", err)
	}
	killed := NewManager()
	t.Cleanup(func() { _ = killed.Close(ctx) })
	if _, err := killed.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("killed Navigate: %v", err)
	}
	livePath, killedPath := live.Status().ProfileDir, killed.Status().ProfileDir
	for _, p := range []string{livePath, killedPath} {
		if filepath.Dir(p) != tmp {
			t.Fatalf("profile %s is not under the private temp dir %s", p, tmp)
		}
	}

	// SIGKILL: nothing gets to run Close.
	pid := killed.sess.(*session).launcher.PID()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("browser pid %d still alive after SIGKILL", pid)
	}
	if !exists(killedPath) {
		t.Fatal("precondition: the killed session's profile should still be on disk")
	}

	// Make both directories look abandoned by age alone.
	old := time.Now().Add(-3 * time.Hour)
	for _, p := range []string{livePath, killedPath} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	removed := sweepStaleProfiles(tmp, time.Now(), time.Hour, os.Getuid(), host, processAlive, "")

	if exists(killedPath) {
		t.Errorf("the killed session's profile %s should have been swept (removed=%v)", killedPath, removed)
	}
	if !exists(livePath) {
		t.Fatalf("the sweep removed a LIVE session's profile %s", livePath)
	}
	// And that session still works.
	if _, err := live.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Errorf("the live session broke after the sweep: %v", err)
	}
}
