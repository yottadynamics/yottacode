package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
		// Cache the model list so the picker doesn't need a network call.
		if modelsPath, pathErr := copilotauth.DefaultModelsPath(); pathErr == nil {
			if models, fetchErr := fetchCopilotModelsForCache(ctx, ct); fetchErr == nil {
				_ = copilotauth.SaveModels(modelsPath, copilotauth.ModelsFile{
					CachedAt: time.Now().UTC(),
					Models:   models,
				})
			}
		}
		return inlineCopilotAuthDoneMsg{err: nil}
	}
}

func handleInlineCopilotAuthCode(m Model, msg inlineCopilotAuthCodeMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render("[copilot] device code request failed: " + msg.err.Error()))
		if m.copilotPendingAdd != nil {
			m.appendLine(styleAuto.Render(fmt.Sprintf("(profile %q was not saved — retry /provider add)",
				m.copilotPendingAdd.add.Name)))
			m.copilotPendingAdd = nil
		}
		return m, nil
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf("[copilot] open %s and enter code: %s",
		msg.dc.VerificationURI, msg.dc.UserCode)))
	return m, pollCopilotAuthCmd(m.parentCtx, msg.dc)
}

func handleInlineCopilotAuthDone(m Model, msg inlineCopilotAuthDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render("[copilot] sign-in failed: " + msg.err.Error()))
		if m.copilotPendingAdd != nil {
			m.appendLine(styleAuto.Render(fmt.Sprintf("(profile %q was not saved — retry /provider add)",
				m.copilotPendingAdd.add.Name)))
			m.copilotPendingAdd = nil
		}
		m.appendLine(styleAuto.Render("[copilot] run `yottacode copilot-auth login` to retry"))
		return m, nil
	}
	m.appendLine(styleAuto.Render("[copilot] signed in — token saved and Copilot access verified"))

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

func fetchCopilotModelsForCache(ctx context.Context, ct copilotauth.CopilotToken) ([]copilotauth.CachedModel, error) {
	endpoint := ct.Endpoints.API
	if endpoint == "" {
		endpoint = copilotauth.CopilotAPIEndpoint
	}
	url := strings.TrimRight(endpoint, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+ct.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Editor-Version", "vscode/1.95.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.250.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env struct {
		Data []struct {
			ID                  string `json:"id"`
			Name                string `json:"name"`
			ModelPickerEnabled  bool   `json:"model_picker_enabled"`
			ModelPickerCategory string `json:"model_picker_category"`
			Capabilities        struct {
				Limits struct {
					MaxContextWindowTokens int `json:"max_context_window_tokens"`
					MaxOutputTokens        int `json:"max_output_tokens"`
				} `json:"limits"`
			} `json:"capabilities"`
			Policy struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	out := make([]copilotauth.CachedModel, 0, len(env.Data))
	for _, m := range env.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || !copilotauth.IsChatModel(id) {
			continue
		}
		if !m.ModelPickerEnabled && m.ModelPickerCategory == "" {
			continue
		}
		out = append(out, copilotauth.CachedModel{
			ID:            id,
			Name:          m.Name,
			ContextWindow: m.Capabilities.Limits.MaxContextWindowTokens,
			MaxOutput:     m.Capabilities.Limits.MaxOutputTokens,
			Disabled:      m.Policy.State == "disabled",
		})
	}
	return out, nil
}
