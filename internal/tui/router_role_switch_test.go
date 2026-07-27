package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/session"
)

// roleSwitchAdapter is a tiny adapter.Client implementation for role-switch
// tests. The switch helper should reuse the already-built router adapter, not
// rebuild a client, so the test needs stable pointer identity plus a profile.
type roleSwitchAdapter struct {
	profile adapter.ProviderProfile
}

func (a *roleSwitchAdapter) ChatStream(context.Context, []adapter.Message, []adapter.Tool) <-chan adapter.StreamEvent {
	ch := make(chan adapter.StreamEvent)
	close(ch)
	return ch
}

func (a *roleSwitchAdapter) Profile() adapter.ProviderProfile { return a.profile }

func TestSwitchActiveModelToRouterRole_AdvisorAndImplementer(t *testing.T) {
	seedRoleSwitchConfig(t)
	advisor := &roleSwitchAdapter{profile: adapter.ProviderProfile{Provider: adapter.ProviderAnthropic}}
	implementer := &roleSwitchAdapter{profile: adapter.ProviderProfile{Provider: adapter.ProviderOpenAI}}
	reg := agent.NewRegistry()
	sess, err := session.New("gpt-4o-mini", "/cwd")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	m := Model{
		parentCtx:    context.Background(),
		modelName:    "gpt-4o-mini",
		baseURL:      "https://api.openai.com/v1",
		provider:     "openai",
		apiKey:       "openai-key",
		cfg:          agent.LoopConfig{Adapter: implementer, Registry: reg},
		subagentTool: &agent.AgentTool{Adapter: implementer},
		sess:         sess,
		transcript:   &strings.Builder{},
		routerMode:   config.RouterModeAuto,
		router: &cli.RouterAdapters{
			Advisor:          advisor,
			AdvisorModel:     "claude-opus-4-6",
			AdvisorRef:       "anthropic:claude-opus-4-6",
			Implementer:      implementer,
			ImplementerModel: "gpt-4o-mini",
			ImplementerRef:   "openai:gpt-4o-mini",
		},
	}

	m, _ = m.switchActiveModelToRouterRole("advisor")
	if m.cfg.Adapter != advisor {
		t.Fatal("active adapter did not switch to advisor")
	}
	if m.subagentTool.Adapter != advisor {
		t.Fatal("subagent inherited adapter did not switch to advisor")
	}
	if m.modelName != "claude-opus-4-6" || m.sess.Model != "claude-opus-4-6" {
		t.Fatalf("model fields = (%q, %q), want advisor", m.modelName, m.sess.Model)
	}
	if m.provider != "anthropic" || m.baseURL != "https://api.anthropic.com" || m.apiKey != "anthropic-key" {
		t.Fatalf("provider fields after advisor switch = (%q, %q, %q)", m.provider, m.baseURL, m.apiKey)
	}
	if m.providerProfile.Provider != adapter.ProviderAnthropic {
		t.Fatalf("providerProfile = %s, want anthropic", m.providerProfile.Provider)
	}
	if _, ok := reg.Get(agent.ConsultAdvisorToolName); ok {
		t.Fatal("advisor-driven main session should not expose consult_advisor")
	}

	m, _ = m.switchActiveModelToRouterRole("implementer")
	if m.cfg.Adapter != implementer {
		t.Fatal("active adapter did not switch to implementer")
	}
	if m.subagentTool.Adapter != implementer {
		t.Fatal("subagent inherited adapter did not switch to implementer")
	}
	if m.modelName != "gpt-4o-mini" || m.sess.Model != "gpt-4o-mini" {
		t.Fatalf("model fields = (%q, %q), want implementer", m.modelName, m.sess.Model)
	}
	if m.provider != "openai" || m.baseURL != "https://api.openai.com/v1" || m.apiKey != "openai-key" {
		t.Fatalf("provider fields after implementer switch = (%q, %q, %q)", m.provider, m.baseURL, m.apiKey)
	}
	if m.providerProfile.Provider != adapter.ProviderOpenAI {
		t.Fatalf("providerProfile = %s, want openai", m.providerProfile.Provider)
	}
	got, ok := reg.Get(agent.ConsultAdvisorToolName)
	if !ok {
		t.Fatal("implementer-driven main session should expose consult_advisor")
	}
	if _, ok := got.(*agent.ConsultAdvisorTool); !ok {
		t.Fatalf("consult_advisor tool type = %T", got)
	}
}

func TestSwitchActiveModelToRouterRole_NoopsWhenIncomplete(t *testing.T) {
	active := &roleSwitchAdapter{profile: adapter.ProviderProfile{Provider: adapter.ProviderOpenAI}}
	m := Model{
		modelName:    "gpt-4o-mini",
		cfg:          agent.LoopConfig{Adapter: active},
		subagentTool: &agent.AgentTool{Adapter: active},
		transcript:   &strings.Builder{},
	}

	m2, _ := m.switchActiveModelToRouterRole("advisor")
	if m2.cfg.Adapter != active || m2.subagentTool.Adapter != active || m2.modelName != "gpt-4o-mini" {
		t.Fatalf("nil router should leave active model unchanged")
	}

	m.router = &cli.RouterAdapters{AdvisorModel: "claude-opus-4-6", AdvisorRef: "anthropic:claude-opus-4-6"}
	m2, _ = m.switchActiveModelToRouterRole("advisor")
	if m2.cfg.Adapter != active || m2.subagentTool.Adapter != active || m2.modelName != "gpt-4o-mini" {
		t.Fatalf("nil advisor adapter should leave active model unchanged")
	}
}

func TestAutoModeSwitchesToImplementer(t *testing.T) {
	seedRoleSwitchConfig(t)
	advisor := &roleSwitchAdapter{profile: adapter.ProviderProfile{Provider: adapter.ProviderAnthropic}}
	implementer := &roleSwitchAdapter{profile: adapter.ProviderProfile{Provider: adapter.ProviderOpenAI}}
	reg := agent.NewRegistry()
	m := Model{
		parentCtx:    context.Background(),
		modelName:    "claude-opus-4-6",
		baseURL:      "https://api.anthropic.com",
		provider:     "anthropic",
		apiKey:       "anthropic-key",
		cfg:          agent.LoopConfig{Adapter: advisor, Registry: reg, AutoMode: &agent.AutoModeState{}, PlanMode: &agent.PlanModeState{}},
		subagentTool: &agent.AgentTool{Adapter: advisor},
		transcript:   &strings.Builder{},
		routerMode:   config.RouterModeAuto,
		router: &cli.RouterAdapters{
			Advisor:          advisor,
			AdvisorModel:     "claude-opus-4-6",
			AdvisorRef:       "anthropic:claude-opus-4-6",
			Implementer:      implementer,
			ImplementerModel: "gpt-4o-mini",
			ImplementerRef:   "openai:gpt-4o-mini",
		},
	}

	m, _ = toggleAutoMode(m)
	if m.cfg.Adapter != implementer || m.subagentTool.Adapter != implementer {
		t.Fatal("entering auto mode should switch active adapters to implementer")
	}
	if m.modelName != "gpt-4o-mini" || m.provider != "openai" {
		t.Fatalf("auto mode model/provider = (%q, %q), want implementer", m.modelName, m.provider)
	}
	if _, ok := reg.Get(agent.ConsultAdvisorToolName); !ok {
		t.Fatal("auto-mode implementer should have consult_advisor in the main registry")
	}
}

func seedRoleSwitchConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{
			Name:         "anthropic",
			Kind:         "anthropic",
			BaseURL:      "https://api.anthropic.com",
			APIKeyEnv:    "ANTHROPIC_API_KEY",
			DefaultModel: "claude-opus-4-6",
			Models:       []config.Model{{Name: "claude-opus-4-6", Tier: "expensive"}},
		},
		{
			Name:         "openai",
			Kind:         "openai",
			BaseURL:      "https://api.openai.com/v1",
			APIKeyEnv:    "OPENAI_API_KEY",
			DefaultModel: "gpt-4o-mini",
			Models:       []config.Model{{Name: "gpt-4o-mini", Tier: "cheap"}},
		},
	}
	if err := config.Save(cfg, ""); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}
