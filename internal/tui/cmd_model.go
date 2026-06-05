package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
)

// cmdModel dispatches /model. The bare invocation opens the picker
// overlay (the new default UX); /model list keeps the flat text dump
// for users who prefer it; /model <name> remains a power-user shortcut
// that switches the active model in one keystroke.
//
//	/model                — open the picker overlay (Phase 1A default)
//	/model list           — flat text list of the active provider's models
//	/model list all       — flat text list of every configured provider
//	/model <name>         — power-user shortcut: switch the default-model
//	                        slot in-place, side-effect-switching the
//	                        active provider when the name belongs to a
//	                        different profile's catalog.
func cmdModel(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		return openPickerForActiveProvider(m)
	}
	if strings.EqualFold(args[0], "list") {
		showAll := len(args) >= 2 && strings.EqualFold(args[1], "all")
		m.appendLine(formatModelList(loadConfigForCommand(m), m.modelName, showAll))
		return m, nil
	}
	return modelShortcutSwitch(m, args[0])
}

// openPickerForActiveProvider is the bare-/model entry point. Picks
// the [[providers]] block matching the session's base_url (or
// synthesizes one for ad-hoc sessions) and kicks off the async
// fetch.
func openPickerForActiveProvider(m Model) (Model, tea.Cmd) {
	provider := m.providerProfileForModel()
	cmd := m.openModelPicker(provider)
	return m, cmd
}

// modelShortcutSwitch is the /model <name> power-user path. Searches
// every configured provider's catalog for the name; switches profile
// + model in one shot when found, falls back to "switch model on the
// current provider" otherwise. Mirrors pre-picker behavior so muscle
// memory keeps working.
func modelShortcutSwitch(m Model, newTag string) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	newBaseURL := m.baseURL
	newAPIKey := m.apiKey
	switchedProfile := ""
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !providerOwnsModel(p, newTag) {
			continue
		}
		if p.BaseURL != "" {
			newBaseURL = p.BaseURL
		}
		if p.APIKeyEnv != "" {
			if v := os.Getenv(p.APIKeyEnv); v != "" {
				newAPIKey = v
			}
		}
		switchedProfile = p.Name
		break
	}

	m.apiKey = newAPIKey
	m.baseURL = newBaseURL
	ad := adapter.NewWithConfig(m.adapterConfig(newTag, newBaseURL))
	m.cfg.Adapter = ad
	// Also update the AgentTool's Adapter so subagents inherit the new provider
	if m.subagentTool != nil {
		m.subagentTool.Adapter = ad
	}
	m.providerProfile = ad.Profile()
	m.modelName = newTag
	m.sess.Model = newTag
	m, _ = reloadMemoryNow(m, "")
	if switchedProfile != "" && switchedProfile != strings.TrimSpace(m.provider) {
		m.appendLine(styleAuto.Render(statusLine("model", fmt.Sprintf("switched to %s (provider: %s)", newTag, switchedProfile))))
	} else {
		m.appendLine(styleAuto.Render(statusLine("model", fmt.Sprintf("switched to %s", newTag))))
	}
	return m, runProviderProbe(m.parentCtx, m.adapterConfig(newTag, newBaseURL), false)
}

// providerOwnsModel reports whether p's model surface contains tag.
// Sources searched, in order: (1) the embedded catalog for curated
// kinds (anthropic/openai/gemini), (2) p.Models from config.toml
// (legacy hand-curated lists). Returns false for non-curated kinds
// without an explicit p.Models entry — the shortcut then falls back
// to "switch model on current provider," matching pre-catalog
// muscle memory.
func providerOwnsModel(p *config.Provider, tag string) bool {
	if catalog.IsCurated(*p) {
		for _, m := range catalog.Get(p.Kind) {
			if m.ID == tag {
				return true
			}
		}
	}
	for _, mm := range p.Models {
		if mm.Name == tag {
			return true
		}
	}
	return false
}
