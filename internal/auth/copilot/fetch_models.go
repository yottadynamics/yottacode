package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchAndCacheModels fetches the model list from the Copilot API,
// filters to chat models, and persists the result to the default
// models path. Shared by the CLI, TUI inline auth, and wizard flows.
func FetchAndCacheModels(ctx context.Context, ct CopilotToken) ([]CachedModel, error) {
	models, err := FetchModels(ctx, ct)
	if err != nil {
		return nil, err
	}
	path, err := DefaultModelsPath()
	if err != nil {
		return models, fmt.Errorf("resolve models path: %w", err)
	}
	if err := SaveModels(path, ModelsFile{
		CachedAt: time.Now().UTC(),
		Models:   models,
	}); err != nil {
		return models, fmt.Errorf("save models cache: %w", err)
	}
	return models, nil
}

// FetchModels queries the Copilot /models endpoint and returns
// filtered chat models with plan state.
func FetchModels(ctx context.Context, ct CopilotToken) ([]CachedModel, error) {
	endpoint := ct.Endpoints.API
	if endpoint == "" {
		endpoint = CopilotAPIEndpoint
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
		return nil, fmt.Errorf("parse models: %w", err)
	}
	out := make([]CachedModel, 0, len(env.Data))
	for _, m := range env.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || !IsChatModel(id) {
			continue
		}
		if !m.ModelPickerEnabled && m.ModelPickerCategory == "" {
			continue
		}
		out = append(out, CachedModel{
			ID:            id,
			Name:          m.Name,
			ContextWindow: m.Capabilities.Limits.MaxContextWindowTokens,
			MaxOutput:     m.Capabilities.Limits.MaxOutputTokens,
			Disabled:      m.Policy.State == "disabled",
		})
	}
	return out, nil
}
