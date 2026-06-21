package tui

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/providerops"
)

// URL-ready handler stores the pending login on the model, surfaces
// the auth URL to the transcript for fallback, and returns the
// blocking wait cmd.
func TestHandleInlineOpenAIAuthURL_Success(t *testing.T) {
	m := newTestModel(t)
	pending := &openaiauth.PendingLogin{AuthURL: "https://auth.openai.com/oauth/authorize?test=1"}

	m, cmd := handleInlineOpenAIAuthURL(m, inlineOpenAIAuthURLMsg{pending: pending})

	if m.openAIAuthPending != pending {
		t.Errorf("openAIAuthPending should be set to msg.pending")
	}
	if cmd == nil {
		t.Errorf("expected wait cmd; got nil")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "browser opened") {
		t.Errorf("transcript missing 'browser opened':\n%s", out)
	}
}

// URL-ready handler with error: surfaces failure, leaves pending nil,
// returns no cmd.
func TestHandleInlineOpenAIAuthURL_Error(t *testing.T) {
	m := newTestModel(t)
	m, cmd := handleInlineOpenAIAuthURL(m, inlineOpenAIAuthURLMsg{err: errors.New("port 1455 in use")})

	if m.openAIAuthPending != nil {
		t.Errorf("openAIAuthPending should stay nil after a start failure")
	}
	if cmd != nil {
		t.Errorf("no follow-up cmd expected on start failure")
	}
	out := stripANSI(m.transcript.String())
	for _, want := range []string{"sign-in failed to start", "port 1455 in use", "yottacode openai-auth login"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
}

// Done handler success: clears pending, surfaces ✓ + scan kickoff
// line, returns the scan cmd.
func TestHandleInlineOpenAIAuthDone_Success(t *testing.T) {
	m := newTestModel(t)
	m.openAIAuthPending = &openaiauth.PendingLogin{}
	m, cmd := handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{pending: m.openAIAuthPending, err: nil, accessToken: "tkn"})

	if m.openAIAuthPending != nil {
		t.Errorf("openAIAuthPending should be cleared on completion")
	}
	if cmd == nil {
		t.Errorf("expected scan cmd after successful login")
	}
	out := stripANSI(m.transcript.String())
	for _, want := range []string{"signed in", "scanning models"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
}

// Scan-done handler success: surfaces ✓ + count + comma-separated list.
func TestHandleInlineOpenAIAuthScanDone_Success(t *testing.T) {
	m := newTestModel(t)
	m, cmd := handleInlineOpenAIAuthScanDone(m, inlineOpenAIAuthScanDoneMsg{
		models: []string{"gpt-5.5", "gpt-5.4"},
	})
	if cmd != nil {
		t.Errorf("no follow-up cmd expected after scan success")
	}
	out := stripANSI(m.transcript.String())
	for _, want := range []string{"2 models available", "gpt-5.5", "gpt-5.4"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
}

// Scan-done handler failure: surfaces ⚠ + error + retry hint; tokens
// remain saved (handled in waitInlineOpenAIAuthLoginCmd) so the user
// can retry login cheaply.
func TestHandleInlineOpenAIAuthScanDone_Failure(t *testing.T) {
	m := newTestModel(t)
	m, cmd := handleInlineOpenAIAuthScanDone(m, inlineOpenAIAuthScanDoneMsg{
		err: errors.New("backend down"),
	})
	if cmd != nil {
		t.Errorf("no follow-up cmd expected after scan failure")
	}
	out := stripANSI(m.transcript.String())
	for _, want := range []string{"model scan failed", "backend down", "yottacode openai-auth login"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
}

// Done handler failure: clears pending, surfaces error + retry hint.
func TestHandleInlineOpenAIAuthDone_Failure(t *testing.T) {
	m := newTestModel(t)
	m.openAIAuthPending = &openaiauth.PendingLogin{}
	m, cmd := handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{pending: m.openAIAuthPending, err: errors.New("user cancelled")})

	if m.openAIAuthPending != nil {
		t.Errorf("openAIAuthPending should be cleared even on failure")
	}
	if cmd != nil {
		t.Errorf("no follow-up cmd expected after failure")
	}
	out := stripANSI(m.transcript.String())
	for _, want := range []string{"sign-in failed", "user cancelled", "yottacode openai-auth login"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
}

// The persist-on-failure regression: when the OAuth callback returns
// an error, the pending profile must NOT be written to config.toml.
// Before the fix, persistProviderAdd / slash providerAdd had already
// committed the profile to disk before the OAuth flow ran, so a
// cancelled or failed sign-in left a broken openai-auth profile that
// the next chat turn 401'd against. The fix defers persist to the
// done-handler success branch; this test verifies the failure branch
// drops the stash AND leaves config.toml untouched.
func TestHandleInlineOpenAIAuthDone_FailureDropsPendingAdd(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m.openAIAuthPending = &openaiauth.PendingLogin{}
	m.openAIAuthPendingAdd = &pendingOpenAIAuthAdd{
		add: providerops.AddProvider{
			Name:         "chatgpt",
			Kind:         "openai-auth",
			BaseURL:      "https://chatgpt.com/backend-api/codex",
			DefaultModel: "gpt-5.5",
		},
		becomesActive: true,
		fromPicker:    true,
	}
	m, _ = handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{pending: m.openAIAuthPending, err: errors.New("user cancelled")})

	if m.openAIAuthPendingAdd != nil {
		t.Errorf("pendingAdd stash should be dropped on failure")
	}
	cfgPath := filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read cfg: %v", err)
	}
	if strings.Contains(string(body), `name          = "chatgpt"`) {
		t.Errorf("chatgpt provider should NOT have been written to config.toml; got:\n%s", body)
	}
	if strings.Contains(string(body), "openai-auth") {
		t.Errorf("openai-auth profile should NOT have been written; got:\n%s", body)
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, `"chatgpt" was not saved`) {
		t.Errorf("transcript should explain the profile wasn't saved; got:\n%s", out)
	}
}

// The persist-on-success path: when OAuth completes, the deferred
// profile finally lands in config.toml + the in-memory adapter is
// rebuilt + the post-login scan kicks off. The transcript shows the
// "added" line only after sign-in succeeds — so what the user sees
// matches the on-disk state.
func TestHandleInlineOpenAIAuthDone_SuccessPersistsPendingAdd(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m.openAIAuthPending = &openaiauth.PendingLogin{}
	m.openAIAuthPendingAdd = &pendingOpenAIAuthAdd{
		add: providerops.AddProvider{
			Name:         "chatgpt",
			Kind:         "openai-auth",
			BaseURL:      "https://chatgpt.com/backend-api/codex",
			DefaultModel: "gpt-5.5",
		},
		becomesActive: true,
		fromPicker:    true,
	}
	m, cmd := handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{pending: m.openAIAuthPending, err: nil, accessToken: "tkn"})

	if m.openAIAuthPendingAdd != nil {
		t.Errorf("pendingAdd stash should be cleared after persist")
	}
	if cmd == nil {
		t.Errorf("expected scan + provider-use cmd batch on success")
	}
	cfgPath := filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read cfg: %v", err)
	}
	if !strings.Contains(string(body), `name          = "chatgpt"`) {
		t.Errorf("chatgpt provider should be in config.toml after success; got:\n%s", body)
	}
	out := stripANSI(m.transcript.String())
	for _, want := range []string{"signed in", `added "chatgpt"`} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
}

func TestHandleInlineOpenAIAuthScanDone_RefreshesActiveAdapter(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[active]
provider      = "chatgpt"
default_model = "gpt-5.5"

[[providers]]
name          = "chatgpt"
kind          = "openai-auth"
base_url      = "https://chatgpt.com/backend-api/codex"
default_model = "gpt-5.5"
`)

	m, _ = providerUse(m, "chatgpt")
	events := m.cfg.Adapter.ChatStream(m.parentCtx, nil, nil)
	first := <-events
	if first.Err == nil || !strings.Contains(first.Err.Error(), "no models discovered yet") {
		t.Fatalf("setup should start with the pre-scan errored adapter, got %#v", first.Err)
	}

	modelsPath, err := openaiauth.DefaultModelsPath()
	if err != nil {
		t.Fatalf("DefaultModelsPath: %v", err)
	}
	if err := openaiauth.SaveModels(modelsPath, openaiauth.ModelsFile{Models: []string{"gpt-5.5", "gpt-5.4-mini"}}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}

	m, cmd := handleInlineOpenAIAuthScanDone(m, inlineOpenAIAuthScanDoneMsg{models: []string{"gpt-5.5", "gpt-5.4-mini"}})
	if cmd == nil {
		t.Fatalf("active openai-auth scan success should refresh the provider")
	}
	events = m.cfg.Adapter.ChatStream(m.parentCtx, nil, nil)
	first = <-events
	if first.Err != nil && strings.Contains(first.Err.Error(), "no models discovered yet") {
		t.Fatalf("adapter was not rebuilt after scan success: %v", first.Err)
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "switched to chatgpt") {
		t.Errorf("transcript should show provider refresh; got:\n%s", out)
	}
}

// URL-ready failure (port bind / browser launch fails) must also
// drop the pending-add stash. Without this, a port-bind failure on
// the first add leaves the unsaved profile lingering on the Model
// across subsequent picker opens.
func TestHandleInlineOpenAIAuthURL_DropsPendingAddOnError(t *testing.T) {
	m := newTestModel(t)
	m.openAIAuthPendingAdd = &pendingOpenAIAuthAdd{
		add: providerops.AddProvider{Name: "chatgpt", Kind: "openai-auth"},
	}
	m, _ = handleInlineOpenAIAuthURL(m, inlineOpenAIAuthURLMsg{err: errors.New("port 1455 in use")})

	if m.openAIAuthPendingAdd != nil {
		t.Errorf("pendingAdd stash should be dropped on URL-ready failure")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, `"chatgpt" was not saved`) {
		t.Errorf("transcript should explain the profile wasn't saved; got:\n%s", out)
	}
}

// TestClosePendingOpenAIAuthLoginReclaimsPort reproduces the
// closed-the-browser-can't-retry case: an abandoned sign-in leaves its
// callback server bound (the inline flow has no timeout, so Wait never
// returns). Closing the pending login must release the port so the
// next sign-in can bind it.
func TestClosePendingOpenAIAuthLoginReclaimsPort(t *testing.T) {
	// Grab a free loopback port, then stand up a real pending login on
	// it — the same machinery the trigger sites use, minus the browser.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(probe.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()

	pending, err := openaiauth.StartLogin(context.Background(), openaiauth.LoginOptions{
		RedirectURI: "http://127.0.0.1:" + port + "/auth/callback",
		OpenBrowser: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}

	m := newTestModel(t)
	m.openAIAuthPending = pending
	m.closePendingOpenAIAuthLogin()

	if m.openAIAuthPending != nil {
		t.Error("openAIAuthPending should be nil after close")
	}
	// The listener must be gone — a fresh bind on the same port proves
	// the abandoned sign-in no longer holds it.
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("port %s still held after close: %v", port, err)
	}
	l.Close()
}

// TestHandleInlineOpenAIAuthDone_IgnoresStaleCompletion guards the race
// a superseded sign-in opens: an abandoned login's Wait stays parked
// until its timeout, and when that finally fires its stale result must
// NOT tear down the current attempt — otherwise the live login saves
// tokens but its provider profile is silently dropped.
func TestHandleInlineOpenAIAuthDone_IgnoresStaleCompletion(t *testing.T) {
	m := newTestModel(t)
	current := &openaiauth.PendingLogin{}
	m.openAIAuthPending = current
	m.openAIAuthPendingAdd = &pendingOpenAIAuthAdd{
		add: providerops.AddProvider{Name: "chatgpt", Kind: "openai-auth"},
	}

	// A different, superseded login reports completion (here its Wait
	// timed out). The pointer mismatch must make the handler a no-op.
	stale := &openaiauth.PendingLogin{}
	m, cmd := handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{pending: stale, err: errors.New("context deadline exceeded")})

	if m.openAIAuthPending != current {
		t.Errorf("current pending login must be untouched by a stale completion")
	}
	if m.openAIAuthPendingAdd == nil {
		t.Errorf("pending provider-add must NOT be dropped by a stale completion")
	}
	if cmd != nil {
		t.Errorf("stale completion should produce no follow-up cmd")
	}
}
