package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

// inlineOpenAIAuthURLMsg is sent by startInlineOpenAIAuthLoginCmd
// once the loopback listener is up and the auth URL is built. The
// model surfaces the URL to the transcript before dispatching the
// blocking Wait cmd, so a failed browser launch doesn't strand the
// user.
type inlineOpenAIAuthURLMsg struct {
	pending *openaiauth.PendingLogin
	err     error
}

// inlineOpenAIAuthDoneMsg is sent by waitInlineOpenAIAuthLoginCmd
// when the user finishes the browser flow (or it errors out). On
// success the token is already persisted to the default store and
// accessToken is forwarded to the post-login scan cmd.
type inlineOpenAIAuthDoneMsg struct {
	// pending identifies the sign-in that produced this result. The
	// handler ignores the message unless it matches m.openAIAuthPending,
	// so a superseded attempt — one the user abandoned, whose Wait later
	// times out — can't tear down the current attempt's state.
	pending     *openaiauth.PendingLogin
	err         error
	accessToken string
}

// inlineOpenAIAuthScanDoneMsg is sent by scanInlineOpenAIAuthCmd
// after the post-login model scan completes. On success the per-user
// model list has been written to ~/.yottacode/auth/openai-auth-models.json;
// on failure the file (if any) is untouched and err carries the cause.
type inlineOpenAIAuthScanDoneMsg struct {
	models []string
	err    error
}

// inlineOpenAIAuthLoginTimeout bounds how long an inline browser
// sign-in may stay open. Long enough for a real OpenAI login (password,
// 2FA, consent), but finite so a sign-in the user walks away from
// eventually releases the fixed loopback port instead of pinning it for
// the life of the process. closePendingOpenAIAuthLogin handles fast
// in-session retries; this backstops the walk-away case.
const inlineOpenAIAuthLoginTimeout = 10 * time.Minute

// startInlineOpenAIAuthLoginCmd kicks off the synchronous prep
// phase of the OAuth flow. Mirrors the wizard's same-named cmd —
// duplication is deliberate: the two surfaces have separate
// model types + message channels.
func startInlineOpenAIAuthLoginCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		pending, err := openaiauth.StartLogin(ctx, openaiauth.LoginOptions{Timeout: inlineOpenAIAuthLoginTimeout})
		return inlineOpenAIAuthURLMsg{pending: pending, err: err}
	}
}

// waitInlineOpenAIAuthLoginCmd blocks on the user's browser sign-in
// and persists the resulting token to the default store on success.
// The access token is forwarded so the follow-up scan doesn't have
// to re-load the token store.
func waitInlineOpenAIAuthLoginCmd(ctx context.Context, pending *openaiauth.PendingLogin) tea.Cmd {
	return func() tea.Msg {
		ts, err := pending.Wait(ctx)
		if err != nil {
			return inlineOpenAIAuthDoneMsg{pending: pending, err: err}
		}
		path, err := openaiauth.DefaultStorePath()
		if err != nil {
			return inlineOpenAIAuthDoneMsg{pending: pending, err: fmt.Errorf("resolve token store path: %w", err)}
		}
		if err := openaiauth.Save(path, ts); err != nil {
			return inlineOpenAIAuthDoneMsg{pending: pending, err: fmt.Errorf("save tokens: %w", err)}
		}
		return inlineOpenAIAuthDoneMsg{pending: pending, err: nil, accessToken: ts.AccessToken}
	}
}

// scanInlineOpenAIAuthCmd runs the post-login model scan. Failures
// don't touch the existing models file (if any); the caller surfaces
// the error to the transcript so the user knows to retry login.
func scanInlineOpenAIAuthCmd(ctx context.Context, accessToken string) tea.Cmd {
	return func() tea.Msg {
		models, err := openaiauth.ScanAndPersist(ctx, accessToken)
		return inlineOpenAIAuthScanDoneMsg{models: models, err: err}
	}
}

// closePendingOpenAIAuthLogin tears down any in-flight inline OAuth
// login and releases the fixed loopback port so a fresh sign-in can
// bind it. Safe to call when nothing is pending.
//
// Without this, a sign-in the user abandoned — closing the browser
// before finishing — strands its callback server until its timeout
// (inlineOpenAIAuthLoginTimeout) fires. Until then the port stays held,
// so an immediate retry's StartLogin fails with "address already in
// use" against the user's own stranded server.
func (m *Model) closePendingOpenAIAuthLogin() {
	if m.openAIAuthPending != nil {
		m.openAIAuthPending.Close()
		m.openAIAuthPending = nil
	}
}

// handleInlineOpenAIAuthURL stores the pending login on the model
// and surfaces the auth URL to the transcript before dispatching
// the blocking wait cmd. On a start-up failure (port bind, browser
// launch), drops the pending-add stash so the cancelled profile
// never lands in config.toml.
func handleInlineOpenAIAuthURL(m Model, msg inlineOpenAIAuthURLMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render(statusLine("openai-auth", "sign-in failed to start: "+msg.err.Error())))
		if m.openAIAuthPendingAdd != nil {
			m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("profile %q was not saved; retry /provider add",
				m.openAIAuthPendingAdd.add.Name))))
			m.openAIAuthPendingAdd = nil
		}
		m.appendLine(styleAuto.Render(statusHintLine("run `yottacode openai-auth login` to retry")))
		return m, nil
	}
	m.openAIAuthPending = msg.pending
	m.appendLine(styleAuto.Render(statusActionLine("openai-auth", "browser opened; sign in to finish")))
	return m, waitInlineOpenAIAuthLoginCmd(m.parentCtx, msg.pending)
}

// handleInlineOpenAIAuthDone surfaces the OAuth result to the
// transcript. On success it commits the pending profile to
// config.toml (deferred from /provider add so a cancelled sign-in
// can't leave a broken profile on disk), kicks off the post-login
// scan, and rebuilds the adapter when the new profile became active.
// On failure the pending profile is dropped — the user's transcript
// shows what would have been added but config.toml is untouched.
func handleInlineOpenAIAuthDone(m Model, msg inlineOpenAIAuthDoneMsg) (Model, tea.Cmd) {
	// Drop completions from a superseded sign-in. An abandoned login
	// keeps a Wait goroutine parked until its timeout fires; acting on
	// that stale result would nil the current attempt's pending state
	// and strand its provider profile — tokens saved, but the profile
	// never persisted to config.toml.
	if msg.pending != m.openAIAuthPending {
		return m, nil
	}
	m.openAIAuthPending = nil
	if msg.err != nil {
		m.appendLine(styleError.Render(statusLine("openai-auth", "sign-in failed: "+msg.err.Error())))
		if m.openAIAuthPendingAdd != nil {
			m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("profile %q was not saved; retry /provider add",
				m.openAIAuthPendingAdd.add.Name))))
			m.openAIAuthPendingAdd = nil
		}
		m.appendLine(styleAuto.Render(statusHintLine("run `yottacode openai-auth login` to retry")))
		return m, nil
	}
	m.appendLine(styleAuto.Render(statusOKLine("openai-auth", "signed in; token saved; scanning models…")))
	scanCmd := scanInlineOpenAIAuthCmd(m.parentCtx, msg.accessToken)
	// Persist the deferred profile now that the OAuth token is on
	// disk. Reuses commitProviderAddNow so the success transcript
	// (added line, config-written line, etc.) matches the non-deferred
	// path. After this, providerUse can rebuild the adapter against
	// the freshly-saved profile.
	if pending := m.openAIAuthPendingAdd; pending != nil {
		m.openAIAuthPendingAdd = nil
		fakePicked := wizard.CatalogEntry{Name: "openai-auth", Kind: "openai-auth"}
		_, becameActive, ok := commitProviderAddNow(&m, pending.add, "", fakePicked)
		if !ok {
			// commitProviderAddNow already logged the specific
			// failure (validate / write); also drop the half-applied
			// state so the user can retry cleanly.
			return m, nil
		}
		if becameActive {
			usedM, useCmd := providerUse(m, pending.add.Name)
			return usedM, tea.Batch(scanCmd, useCmd)
		}
	}
	if m.cfg.Adapter == nil {
		cfg := loadConfigForCommand(m)
		if active := cfg.FindProvider(cfg.Active.Provider); active != nil && active.Kind == "openai-auth" {
			updated, useCmd := providerUse(m, active.Name)
			return updated, tea.Batch(scanCmd, useCmd)
		}
	}
	return m, scanCmd
}

// handleInlineOpenAIAuthScanDone surfaces the post-login scan result
// to the transcript. On success it lists the discovered models so
// the user knows what they can pick; on failure it points them at
// the retry path. Tokens stay saved either way — only the models
// file is at issue.
func handleInlineOpenAIAuthScanDone(m Model, msg inlineOpenAIAuthScanDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render(statusWarnLine("openai-auth", "model scan failed: "+msg.err.Error())))
		m.appendLine(styleAuto.Render(statusHintLine("tokens saved; rerun `yottacode openai-auth login` to retry")))
		return m, nil
	}
	m.appendLine(styleAuto.Render(statusOKLine("openai-auth", fmt.Sprintf("%d models available: %s", len(msg.models), strings.Join(msg.models, ", ")))))
	cfg := loadConfigForCommand(m)
	active := cfg.FindProvider(cfg.Active.Provider)
	if active == nil || active.Kind != "openai-auth" {
		return m, nil
	}
	return providerUse(m, active.Name)
}
