package browser

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManager_NavigateRefusesBlockedURLWithoutLaunching(t *testing.T) {
	var launches atomic.Int32
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			launches.Add(1)
			return &fakeSession{}, nil
		},
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { return t.TempDir(), nil },
	}
	_, err := m.Navigate(context.Background(), "http://169.254.169.254/latest/meta-data/", "load")
	if !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("Navigate: err = %v, want ErrBlockedURL", err)
	}
	if launches.Load() != 0 {
		t.Errorf("a refused URL launched a browser (%d launches)", launches.Load())
	}
}

func TestManager_DownloadByURLRefusesBlockedURL(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	_, err := m.Download(context.Background(), "", "http://metadata.google.internal/x", t.TempDir()+"/out")
	if !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("Download: err = %v, want ErrBlockedURL", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("session was touched: %v", fake.calls)
	}
}

func TestManager_InspectPassesOptionsThrough(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Inspect(context.Background(), "#x", InspectOptions{InteractiveOnly: true}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := fake.calls[len(fake.calls)-1]; got != "inspect:interactive=true" {
		t.Errorf("last call = %q", got)
	}
}

func TestManager_EvalAndBackDelegate(t *testing.T) {
	fake := &fakeSession{evalResult: "42", url: "https://example.com/prev"}
	m := newTestManager(fake, t.TempDir())
	ctx := context.Background()

	got, err := m.Eval(ctx, "6*7")
	if err != nil || got != "42" {
		t.Fatalf("Eval = %q, %v", got, err)
	}
	res, err := m.Back(ctx)
	if err != nil || res.URL != "https://example.com/prev" {
		t.Fatalf("Back = %+v, %v", res, err)
	}

	fake.evalErr = ErrEvalFailed
	fake.backErr = ErrNoHistory
	if _, err := m.Eval(ctx, "x"); !errors.Is(err, ErrEvalFailed) {
		t.Errorf("Eval error not propagated: %v", err)
	}
	if _, err := m.Back(ctx); !errors.Is(err, ErrNoHistory) {
		t.Errorf("Back error not propagated: %v", err)
	}
}

func TestManager_EvalAndBackAfterCloseAreDenied(t *testing.T) {
	m := newTestManager(&fakeSession{}, t.TempDir())
	ctx := context.Background()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := m.Eval(ctx, "1"); !errors.Is(err, ErrActionDenied) {
		t.Errorf("Eval after Close: %v", err)
	}
	if _, err := m.Back(ctx); !errors.Is(err, ErrActionDenied) {
		t.Errorf("Back after Close: %v", err)
	}
	if err := m.SetDialogPolicy(true, ""); !errors.Is(err, ErrActionDenied) {
		t.Errorf("SetDialogPolicy after Close: %v", err)
	}
}

func TestManager_DialogPolicyIsRememberedAndAppliedToEachSession(t *testing.T) {
	first, second := &fakeSession{}, &fakeSession{}
	sessions := []*fakeSession{first, second}
	var n int
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			s := sessions[n]
			n++
			return s, nil
		},
		findBinary:   func() (string, error) { return "/fake/chrome", nil },
		mkProfileDir: func() (string, error) { return t.TempDir(), nil },
	}
	ctx := context.Background()

	// Set before any launch: never launches, is reported back, and is applied
	// to the session the first action launches.
	if err := m.SetDialogPolicy(true, "Ada"); err != nil {
		t.Fatalf("SetDialogPolicy: %v", err)
	}
	if n != 0 {
		t.Fatal("SetDialogPolicy must not launch a browser")
	}
	if accept, text := m.DialogPolicy(); !accept || text != "Ada" {
		t.Errorf("DialogPolicy = %t,%q", accept, text)
	}
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if !hasCall(first, "dialogPolicy:true:Ada") {
		t.Errorf("first session never got the policy: %v", first.calls)
	}

	// Changing it later reaches the live session immediately.
	if err := m.SetDialogPolicy(false, ""); err != nil {
		t.Fatalf("SetDialogPolicy: %v", err)
	}
	if !hasCall(first, "dialogPolicy:false:") {
		t.Errorf("live session not updated: %v", first.calls)
	}

	// A crash-relaunch gets the remembered policy too.
	first.mu.Lock()
	first.dead = true
	first.mu.Unlock()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate after crash: %v", err)
	}
	if !hasCall(second, "dialogPolicy:false:") {
		t.Errorf("relaunched session lost the policy: %v", second.calls)
	}
}

func hasCall(f *fakeSession, want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
}

func TestDialogPolicy_NilReceiverIsDismiss(t *testing.T) {
	var d *dialogPolicy
	d.set(true, "ignored") // must not panic
	if accept, text := d.get(); accept || text != "" {
		t.Errorf("nil policy = %t,%q, want dismiss", accept, text)
	}
	d = &dialogPolicy{}
	d.set(true, "hi")
	if accept, text := d.get(); !accept || text != "hi" {
		t.Errorf("policy = %t,%q", accept, text)
	}
}
