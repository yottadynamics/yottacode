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

// TestRouter_RenderRoundTrip proves the task-routing fields (mode +
// fast/smart) survive Render → Load — the persistence path the /router
// picker relies on. Render historically emitted only the fallback
// router's enabled/candidates fields, silently dropping these.
func TestRouter_RenderRoundTrip(t *testing.T) {
	cfg, err := Load(writeFile(t, routingConfigSrc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Router.Mode = RouterModeAuto
	cfg.Router.ImplementerModel = "anthropic:claude-haiku-4-5"
	cfg.Router.AdvisorModel = "anthropic:claude-opus-4-6"

	rendered := Render(cfg)
	for _, want := range []string{
		`mode               = "auto"`,
		`advisor_model      = "anthropic:claude-opus-4-6"`,
		`implementer_model  = "anthropic:claude-haiku-4-5"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Render output missing %q\n--- got ---\n%s", want, rendered)
		}
	}

	reloaded, err := Load(writeFile(t, rendered))
	if err != nil {
		t.Fatalf("reload rendered config: %v", err)
	}
	if reloaded.Router.Mode != RouterModeAuto ||
		reloaded.Router.ImplementerModel != "anthropic:claude-haiku-4-5" ||
		reloaded.Router.AdvisorModel != "anthropic:claude-opus-4-6" {
		t.Errorf("round-trip lost router fields: %+v", reloaded.Router)
	}
	if err := Validate(reloaded); err != nil {
		t.Errorf("round-tripped config should validate: %v", err)
	}
}

func TestRouterChain_Coalescing(t *testing.T) {
	// Plural wins and preserves order.
	if got := (RouterConfig{FastModels: []string{"a", "b"}}).FastChain(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("FastChain plural = %v, want [a b]", got)
	}
	// Singular becomes a one-element chain.
	if got := (RouterConfig{FastModel: "x"}).FastChain(); len(got) != 1 || got[0] != "x" {
		t.Errorf("FastChain singular = %v, want [x]", got)
	}
	// Blank entries are dropped.
	if got := (RouterConfig{SmartModels: []string{" ", "y", ""}}).SmartChain(); len(got) != 1 || got[0] != "y" {
		t.Errorf("SmartChain blanks = %v, want [y]", got)
	}
	// Nothing set → empty chain.
	if got := (RouterConfig{}).FastChain(); len(got) != 0 {
		t.Errorf("empty FastChain = %v, want []", got)
	}
}

// TestRouter_ChainRenderRoundTrip: a fast/smart failover chain
// (smart_models) survives Render → Load and validates.
func TestRouter_ChainRenderRoundTrip(t *testing.T) {
	cfg, err := Load(writeFile(t, routingConfigSrc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Router.Mode = RouterModeAuto
	cfg.Router.ImplementerModel = "anthropic:claude-haiku-4-5"
	cfg.Router.AdvisorModels = []string{"anthropic:claude-opus-4-6", "anthropic:claude-haiku-4-5"}

	rendered := Render(cfg)
	if !strings.Contains(rendered, `advisor_models     = ["anthropic:claude-opus-4-6", "anthropic:claude-haiku-4-5"]`) {
		t.Errorf("render missing advisor_models list:\n%s", rendered)
	}
	reloaded, err := Load(writeFile(t, rendered))
	if err != nil {
		t.Fatalf("reload rendered config: %v", err)
	}
	if len(reloaded.Router.AdvisorModels) != 2 || reloaded.Router.AdvisorModels[0] != "anthropic:claude-opus-4-6" {
		t.Errorf("round-trip lost advisor_models: %v", reloaded.Router.AdvisorModels)
	}
	if err := Validate(reloaded); err != nil {
		t.Errorf("chain config should validate: %v", err)
	}
}

func TestRouter_RejectsBothSingularAndPlural(t *testing.T) {
	src := routingConfigSrc + `
[router]
mode        = "auto"
fast_model  = "anthropic:claude-haiku-4-5"
fast_models = ["anthropic:claude-haiku-4-5", "anthropic:claude-opus-4-6"]
smart_model = "anthropic:claude-opus-4-6"
`
	if _, err := Load(writeFile(t, src)); err == nil {
		t.Fatal("expected an error when both fast_model and fast_models are set")
	} else if !strings.Contains(err.Error(), "fast_model or fast_models") {
		t.Errorf("error = %v, want the both-set message", err)
	}
}

func TestRouter_RejectsUnresolvableChainEntry(t *testing.T) {
	src := routingConfigSrc + `
[router]
mode         = "auto"
fast_model   = "anthropic:claude-haiku-4-5"
smart_models = ["anthropic:claude-opus-4-6", "ghost:model"]
`
	if _, err := Load(writeFile(t, src)); err == nil {
		t.Fatal("expected an error for an unresolvable chain entry")
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
	if err == nil || !strings.Contains(err.Error(), "advisor_model") {
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
	if err == nil || !strings.Contains(err.Error(), "implementer_model") {
		t.Fatalf("expected fast_model resolution error, got %v", err)
	}
}

// TestRouter_RenderKeepsPolicyAndHealthForChains pins the config-clobber
// regression: policy and the health knobs used to render only when the
// CANDIDATES router was enabled, so a chain-only config (smart_models +
// policy + health, exactly what docs/models.md's failover example shows)
// lost all three keys on every /router picker write.
func TestRouter_RenderKeepsPolicyAndHealthForChains(t *testing.T) {
	cfg, err := Load(writeFile(t, routingConfigSrc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Router.Mode = RouterModeAuto
	cfg.Router.FastModels = []string{"anthropic:claude-haiku-4-5"}
	cfg.Router.SmartModels = []string{"anthropic:claude-opus-4-6", "anthropic:claude-haiku-4-5"}
	cfg.Router.Policy = "fallback-chain"
	cfg.Router.HealthWindowSeconds = 90
	cfg.Router.HealthFailureThreshold = 3

	rendered := Render(cfg)
	for _, want := range []string{
		`policy`,
		`health_window_seconds    = 90`,
		`health_failure_threshold = 3`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Render dropped %q from a chain-only config\n--- got ---\n%s", want, rendered)
		}
	}

	reloaded, err := Load(writeFile(t, rendered))
	if err != nil {
		t.Fatalf("reload rendered config: %v", err)
	}
	if reloaded.Router.Policy != "fallback-chain" ||
		reloaded.Router.HealthWindowSeconds != 90 ||
		reloaded.Router.HealthFailureThreshold != 3 {
		t.Errorf("round-trip lost policy/health: %+v", reloaded.Router)
	}
}
