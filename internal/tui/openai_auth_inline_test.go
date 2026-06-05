package tui

import (
	"errors"
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
	m, cmd := handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{err: nil, accessToken: "tkn"})

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
	m, cmd := handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{err: errors.New("user cancelled")})

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
	m, _ = handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{err: errors.New("user cancelled")})

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
	m, cmd := handleInlineOpenAIAuthDone(m, inlineOpenAIAuthDoneMsg{err: nil, accessToken: "tkn"})

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
