package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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

// startInlineOpenAIAuthLoginCmd kicks off the synchronous prep
// phase of the OAuth flow. Mirrors the wizard's same-named cmd —
// duplication is deliberate: the two surfaces have separate
// model types + message channels.
func startInlineOpenAIAuthLoginCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		pending, err := openaiauth.StartLogin(ctx, openaiauth.LoginOptions{})
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
			return inlineOpenAIAuthDoneMsg{err: err}
		}
		path, err := openaiauth.DefaultStorePath()
		if err != nil {
			return inlineOpenAIAuthDoneMsg{err: fmt.Errorf("resolve token store path: %w", err)}
		}
		if err := openaiauth.Save(path, ts); err != nil {
			return inlineOpenAIAuthDoneMsg{err: fmt.Errorf("save tokens: %w", err)}
		}
		return inlineOpenAIAuthDoneMsg{err: nil, accessToken: ts.AccessToken}
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

// handleInlineOpenAIAuthURL stores the pending login on the model
// and surfaces the auth URL to the transcript before dispatching
// the blocking wait cmd. On a start-up failure (port bind, browser
// launch), drops the pending-add stash so the cancelled profile
// never lands in config.toml.
func handleInlineOpenAIAuthURL(m Model, msg inlineOpenAIAuthURLMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render("[openai-auth] sign-in failed to start: " + msg.err.Error()))
		if m.openAIAuthPendingAdd != nil {
			m.appendLine(styleAuto.Render(fmt.Sprintf("(profile %q was not saved — retry /provider add to try again)",
				m.openAIAuthPendingAdd.add.Name)))
			m.openAIAuthPendingAdd = nil
		}
		m.appendLine(styleAuto.Render("[openai-auth] run `yottacode openai-auth login` to retry"))
		return m, nil
	}
	m.openAIAuthPending = msg.pending
	m.appendLine(styleAuto.Render("[openai-auth] browser opened — sign in to complete setup"))
	if msg.pending.AuthURL != "" {
		m.appendLine(styleAuto.Render("[openai-auth] fallback URL: " + msg.pending.AuthURL))
	}
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
	m.openAIAuthPending = nil
	if msg.err != nil {
		m.appendLine(styleError.Render("[openai-auth] sign-in failed: " + msg.err.Error()))
		if m.openAIAuthPendingAdd != nil {
			m.appendLine(styleAuto.Render(fmt.Sprintf("(profile %q was not saved — retry /provider add to try again)",
				m.openAIAuthPendingAdd.add.Name)))
			m.openAIAuthPendingAdd = nil
		}
		m.appendLine(styleAuto.Render("[openai-auth] run `yottacode openai-auth login` to retry"))
		return m, nil
	}
	m.appendLine(styleAuto.Render("[openai-auth] ✓ signed in — token saved; scanning available models..."))
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
		m.appendLine(styleError.Render("[openai-auth] ⚠ model scan failed: " + msg.err.Error()))
		m.appendLine(styleAuto.Render("[openai-auth] tokens saved; rerun `yottacode openai-auth login` to retry"))
		return m, nil
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf("[openai-auth] ✓ %d models available: %s", len(msg.models), strings.Join(msg.models, ", "))))
	return m, nil
}
