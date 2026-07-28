package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
	"github.com/yottadynamics/yottacode/internal/providerops"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

// inlineCopilotAuthCodeMsg is sent once the device code has been
// obtained from GitHub. The TUI surfaces the user_code +
// verification_uri and then kicks off the polling cmd.
type inlineCopilotAuthCodeMsg struct {
	dc  copilotauth.DeviceCode
	err error
}

// inlineCopilotAuthDoneMsg is sent when the user completes the
// device authorization (or it fails / times out). On success the
// GitHub token is persisted and the Copilot API token is verified.
type inlineCopilotAuthDoneMsg struct {
	err error
}

// pendingCopilotAdd carries the deferred provider-add state, same
// pattern as pendingOpenAIAuthAdd.
type pendingCopilotAdd struct {
	add           providerops.AddProvider
	becomesActive bool
	fromPicker    bool
}

// startInlineCopilotAuthCmd requests a device code from GitHub.
func startInlineCopilotAuthCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		dc, err := copilotauth.RequestDeviceCode(ctx, nil, "")
		return inlineCopilotAuthCodeMsg{dc: dc, err: err}
	}
}

// pollCopilotAuthCmd polls for user authorization, then saves the
// token and verifies Copilot access.
func pollCopilotAuthCmd(ctx context.Context, dc copilotauth.DeviceCode) tea.Cmd {
	return func() tea.Msg {
		token, err := copilotauth.PollForToken(ctx, nil, "", dc)
		if err != nil {
			return inlineCopilotAuthDoneMsg{err: err}
		}
		storePath, err := copilotauth.DefaultStorePath()
		if err != nil {
			return inlineCopilotAuthDoneMsg{err: fmt.Errorf("resolve token store path: %w", err)}
		}
		ts := copilotauth.TokenSet{GitHubToken: token}
		if err := copilotauth.Save(storePath, ts); err != nil {
			return inlineCopilotAuthDoneMsg{err: fmt.Errorf("save token: %w", err)}
		}
		ct, err := copilotauth.FetchCopilotToken(ctx, nil, token)
		if err != nil {
			return inlineCopilotAuthDoneMsg{err: fmt.Errorf("verify Copilot access: %w", err)}
		}
		_, _ = copilotauth.FetchAndCacheModels(ctx, ct)
		return inlineCopilotAuthDoneMsg{err: nil}
	}
}

func handleInlineCopilotAuthCode(m Model, msg inlineCopilotAuthCodeMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "copilot", "device code request failed", msg.err.Error())))
		if m.copilotPendingAdd != nil {
			m.appendLine(styleAuto.Render(SysMsg(SysWarning, "copilot", "profile not saved", m.copilotPendingAdd.add.Name)))
			m.copilotPendingAdd = nil
		}
		return m, nil
	}
	m.appendLine(styleAuto.Render(SysMsg(SysProgress, "copilot", "device code sign-in", fmt.Sprintf("open %s", msg.dc.VerificationURI), "code "+msg.dc.UserCode)))
	return m, pollCopilotAuthCmd(m.parentCtx, msg.dc)
}

func handleInlineCopilotAuthDone(m Model, msg inlineCopilotAuthDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "copilot", "sign-in failed", msg.err.Error())))
		if m.copilotPendingAdd != nil {
			m.appendLine(styleAuto.Render(SysMsg(SysWarning, "copilot", "profile not saved", m.copilotPendingAdd.add.Name)))
			m.copilotPendingAdd = nil
		}
		m.appendLine(styleAuto.Render(SysMsg(SysState, "copilot", "retry", "run `yottacode copilot-auth login`")))
		return m, nil
	}
	m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "copilot", "signed in")))

	if pending := m.copilotPendingAdd; pending != nil {
		m.copilotPendingAdd = nil
		fakePicked := wizard.CatalogEntry{Name: "copilot-auth", Kind: "copilot"}
		_, becameActive, ok := commitProviderAddNow(&m, pending.add, "", fakePicked)
		if !ok {
			return m, nil
		}
		if becameActive {
			return providerUse(m, pending.add.Name)
		}
	}
	if m.cfg.Adapter == nil {
		cfg := loadConfigForCommand(m)
		if active := cfg.FindProvider(cfg.Active.Provider); active != nil && active.Kind == "copilot" {
			return providerUse(m, active.Name)
		}
	}
	return m, nil
}
