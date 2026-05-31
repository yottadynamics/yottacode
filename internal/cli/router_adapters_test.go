package cli

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

func routingTestConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{
			Name:         "anthropic",
			Kind:         "anthropic",
			BaseURL:      "https://api.anthropic.com",
			APIKeyEnv:    "ANTHROPIC_API_KEY",
			DefaultModel: "claude-opus-4-6",
			Models: []config.Model{
				{Name: "claude-opus-4-6", Tier: "expensive"},
				{Name: "claude-haiku-4-5", Tier: "cheap"},
			},
		},
	}
	return cfg
}

func TestBuildRouterAdapters_DisabledReturnsNil(t *testing.T) {
	cfg := routingTestConfig() // Router.Mode unset == off
	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	if ra != nil {
		t.Errorf("expected nil RouterAdapters when routing off, got %+v", ra)
	}
}

func TestBuildRouterAdapters_ResolvesFastAndSmart(t *testing.T) {
	cfg := routingTestConfig()
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	if ra == nil {
		t.Fatal("expected RouterAdapters, got nil")
	}
	if ra.FastModel != "claude-haiku-4-5" || ra.SmartModel != "claude-opus-4-6" {
		t.Errorf("models = %q / %q, want haiku / opus", ra.FastModel, ra.SmartModel)
	}
	if ra.Fast == nil || ra.Smart == nil {
		t.Errorf("Fast/Smart adapters must be non-nil")
	}
}

func TestRouterAdapters_ResolveByName(t *testing.T) {
	cfg := routingTestConfig()
	cfg.Router.Mode = config.RouterModeManual
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	// A configured model resolves to a (memoized) adapter.
	if got := ra.Resolve("claude-haiku-4-5"); got == nil {
		t.Errorf("Resolve(haiku) returned nil for a configured model")
	}
	if got := ra.Resolve("claude-haiku-4-5"); got != ra.Fast {
		t.Errorf("Resolve(haiku) should return the memoized fast adapter")
	}
	// An unknown model resolves to nil so the caller inherits.
	if got := ra.Resolve("ghost-model"); got != nil {
		t.Errorf("Resolve(unknown) = %v, want nil", got)
	}
	if got := ra.Resolve(""); got != nil {
		t.Errorf("Resolve(empty) = %v, want nil", got)
	}
}
