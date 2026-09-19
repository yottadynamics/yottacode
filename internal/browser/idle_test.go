package browser

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// idleTestManager returns a Manager over a fresh fakeSession per launch,
// with a short idle timeout, plus the launch counter and session list.
func idleTestManager(t *testing.T, timeout time.Duration) (*Manager, *atomic.Int32, func() []*fakeSession) {
	t.Helper()
	var launches atomic.Int32
	var mu syncutil.Mutex
	var made []*fakeSession
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			launches.Add(1)
			f := &fakeSession{}
			mu.Lock()
			made = append(made, f)
			mu.Unlock()
			return f, nil
		},
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { return t.TempDir(), nil },
		idleTimeout:  timeout,
	}
	sessions := func() []*fakeSession {
		mu.Lock()
		defer mu.Unlock()
		return append([]*fakeSession(nil), made...)
	}
	return m, &launches, sessions
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestManager_IdleSessionIsReapedAndNextActionRelaunches(t *testing.T) {
	m, launches, sessions := idleTestManager(t, 40*time.Millisecond)
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	profile := m.Status().ProfileDir
	if profile == "" {
		t.Fatal("no profile dir recorded")
	}

	if !waitFor(t, 2*time.Second, func() bool { return !m.Status().Active }) {
		t.Fatal("an idle session was never reaped")
	}
	st := m.Status()
	if !st.ReapedIdle {
		t.Error("Status.ReapedIdle should say the session was closed for sitting idle")
	}
	if !hasCall(sessions()[0], "close") {
		t.Errorf("the idle session was not closed gracefully: %v", sessions()[0].calls)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("profile dir %s survived the reap (stat err = %v)", profile, err)
	}

	// Reaping is housekeeping, not a user decision to stop: the next action
	// launches a fresh browser instead of being denied.
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate after reap: %v", err)
	}
	if launches.Load() != 2 {
		t.Errorf("launches = %d, want 2", launches.Load())
	}
	if m.Status().ReapedIdle {
		t.Error("ReapedIdle should clear once a new session is running")
	}
	_ = m.Close(ctx)
}

func TestManager_ActivityKeepsTheSessionAlive(t *testing.T) {
	m, launches, _ := idleTestManager(t, 600*time.Millisecond)
	ctx := context.Background()
	// Use the browser every 60ms for well over the idle timeout (a 10x margin, so a
	// slow CI runner cannot stretch a gap past it).
	for range 14 {
		if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
			t.Fatalf("Navigate: %v", err)
		}
		time.Sleep(60 * time.Millisecond)
	}
	if !m.Status().Active || launches.Load() != 1 {
		t.Errorf("an in-use session was reaped (active=%t launches=%d)", m.Status().Active, launches.Load())
	}
	_ = m.Close(ctx)
}

func TestManager_NonLaunchingCallsCountAsUse(t *testing.T) {
	m, _, _ := idleTestManager(t, 600*time.Millisecond)
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	for range 14 {
		if _, err := m.Tabs(ctx); err != nil {
			t.Fatalf("Tabs: %v", err)
		}
		if _, err := m.ConsoleLogs(ctx, 0); err != nil {
			t.Fatalf("ConsoleLogs: %v", err)
		}
		time.Sleep(60 * time.Millisecond)
	}
	if !m.Status().Active {
		t.Error("reading tabs/console logs should keep the session from going idle")
	}
	_ = m.Close(ctx)
}

func TestManager_CloseStopsTheIdleTimer(t *testing.T) {
	m, _, sessions := idleTestManager(t, 30*time.Millisecond)
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	time.Sleep(120 * time.Millisecond) // several idle periods
	closes := 0
	for _, c := range sessions()[0].calls {
		if c == "close" {
			closes++
		}
	}
	if closes != 1 {
		t.Errorf("session closed %d times, want exactly 1 (the timer must not fire after Close)", closes)
	}
}

func TestManager_ZeroIdleTimeoutDisablesReaping(t *testing.T) {
	m, _, _ := idleTestManager(t, 0)
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if !m.Status().Active {
		t.Error("with reaping disabled the session must stay up")
	}
	_ = m.Close(ctx)
}

func TestNewManager_DefaultsToTheIdleTimeout(t *testing.T) {
	if got := NewManager().idleTimeout; got != defaultIdleTimeout {
		t.Errorf("NewManager idleTimeout = %s, want %s", got, defaultIdleTimeout)
	}
}
