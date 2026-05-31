package config

import (
	"strings"
	"testing"
)

// routingConfigSrc is a minimal config with two providers/models that the
// router-routing tests parameterize via the [router] block appended to it.
const routingConfigSrc = `
[active]
provider = "anthropic"
model    = "claude-opus-4-6"

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
api_key_env   = "ANTHROPIC_API_KEY"
default_model = "claude-opus-4-6"

  [[providers.models]]
  name = "claude-opus-4-6"
  tier = "expensive"

  [[providers.models]]
  name = "claude-haiku-4-5"
  tier = "cheap"
`

func TestRouter_ModeOffByDefault(t *testing.T) {
	cfg, err := Load(writeFile(t, routingConfigSrc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Router.RoutingEnabled() {
		t.Errorf("routing should be disabled when [router].mode is absent")
	}
	if cfg.Router.RoutingAuto() {
		t.Errorf("auto routing should be off when [router].mode is absent")
	}
}

func TestRouter_ParsesAndResolvesModels(t *testing.T) {
	src := routingConfigSrc + `
[router]
mode        = "auto"
fast_model  = "anthropic:claude-haiku-4-5"
smart_model = "anthropic:claude-opus-4-6"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Router.RoutingEnabled() || !cfg.Router.RoutingAuto() {
		t.Fatalf("expected auto routing enabled, got mode=%q", cfg.Router.Mode)
	}
	fast, smart, err := cfg.ResolveRouterModels()
	if err != nil {
		t.Fatalf("ResolveRouterModels: %v", err)
	}
	if fast.Model != "claude-haiku-4-5" || fast.Tier != "cheap" {
		t.Errorf("fast resolved to %+v, want haiku/cheap", fast)
	}
	if smart.Model != "claude-opus-4-6" || smart.Tier != "expensive" {
		t.Errorf("smart resolved to %+v, want opus/expensive", smart)
	}
}

func TestRouter_ManualModeEnabledButNotAuto(t *testing.T) {
	src := routingConfigSrc + `
[router]
mode        = "manual"
fast_model  = "anthropic:claude-haiku-4-5"
smart_model = "anthropic:claude-opus-4-6"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Router.RoutingEnabled() {
		t.Errorf("manual mode should report routing enabled")
	}
	if cfg.Router.RoutingAuto() {
		t.Errorf("manual mode must NOT enable the auto heuristic")
	}
}

func TestRouter_RejectsUnknownMode(t *testing.T) {
	src := routingConfigSrc + `
[router]
mode        = "turbo"
fast_model  = "anthropic:claude-haiku-4-5"
smart_model = "anthropic:claude-opus-4-6"
`
	_, err := Load(writeFile(t, src))
	if err == nil || !strings.Contains(err.Error(), "router.mode") {
		t.Fatalf("expected router.mode validation error, got %v", err)
	}
}

func TestRouter_RequiresBothModelsWhenEnabled(t *testing.T) {
	src := routingConfigSrc + `
[router]
mode       = "auto"
fast_model = "anthropic:claude-haiku-4-5"
`
	_, err := Load(writeFile(t, src))
	if err == nil || !strings.Contains(err.Error(), "smart_model") {
		t.Fatalf("expected missing smart_model error, got %v", err)
	}
}

func TestRouter_RejectsUnresolvableModel(t *testing.T) {
	src := routingConfigSrc + `
[router]
mode        = "auto"
fast_model  = "anthropic:no-such-model"
smart_model = "anthropic:claude-opus-4-6"
`
	_, err := Load(writeFile(t, src))
	if err == nil || !strings.Contains(err.Error(), "fast_model") {
		t.Fatalf("expected fast_model resolution error, got %v", err)
	}
}
