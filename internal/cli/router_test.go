package cli

import (
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
)

func TestBuildRouter_DisabledReturnsNil(t *testing.T) {
	cfg := config.Default()
	cfg.Router.Enabled = false
	got, err := BuildRouter(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil router when disabled, got %T", got)
	}
}

func TestBuildRouter_BuildsCandidatesInOrder(t *testing.T) {
	cfg := config.Default()
	cfg.Router = config.RouterConfig{
		Enabled:    true,
		Policy:     "fallback-chain",
		Candidates: []string{"anthropic", "openai:gpt-4o"},
	}
	cfg.Providers = []config.Provider{
		{
			Name:         "anthropic",
			Kind:         "anthropic",
			BaseURL:      "https://api.anthropic.com",
			APIKeyEnv:    "ANTHROPIC_API_KEY",
			DefaultModel: "claude-haiku-4-5",
			Models: []config.Model{
				{Name: "claude-haiku-4-5", Tier: "cheap"},
			},
		},
		{
			Name:         "openai",
			Kind:         "openai",
			BaseURL:      "https://api.openai.com/v1",
			APIKeyEnv:    "OPENAI_API_KEY",
			DefaultModel: "gpt-4o",
			Models: []config.Model{
				{Name: "gpt-4o", Tier: "balanced"},
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENAI_API_KEY", "sk-test")

	client, err := BuildRouter(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	if client == nil {
		t.Fatal("expected router, got nil")
	}
	router, ok := client.(*adapter.MultiStreamer)
	if !ok {
		t.Fatalf("BuildRouter returned %T, want *adapter.MultiStreamer", client)
	}
	if router.PolicyName() != "fallback-chain" {
		t.Errorf("PolicyName() = %q, want fallback-chain", router.PolicyName())
	}
	cands := router.Candidates()
	if len(cands) != 2 {
		t.Fatalf("Candidates() len = %d, want 2", len(cands))
	}
	if cands[0].Label != "anthropic/claude-haiku-4-5" {
		t.Errorf("first candidate label = %q, want %q", cands[0].Label, "anthropic/claude-haiku-4-5")
	}
	if cands[0].Tier != adapter.TierCheap {
		t.Errorf("first candidate tier = %q, want %q", cands[0].Tier, adapter.TierCheap)
	}
	if cands[1].Label != "openai/gpt-4o" {
		t.Errorf("second candidate label = %q, want %q", cands[1].Label, "openai/gpt-4o")
	}
	if cands[1].Tier != adapter.TierBalanced {
		t.Errorf("second candidate tier = %q, want %q", cands[1].Tier, adapter.TierBalanced)
	}
}

func TestBuildRouter_PicksCheapFirstPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Router = config.RouterConfig{
		Enabled:    true,
		Policy:     "cheap-first",
		Candidates: []string{"openai:gpt-4o", "anthropic:claude-haiku-4-5"},
	}
	cfg.Providers = []config.Provider{
		{
			Name:    "openai",
			Kind:    "openai",
			BaseURL: "https://api.openai.com/v1",
			Models:  []config.Model{{Name: "gpt-4o", Tier: "balanced"}},
		},
		{
			Name:    "anthropic",
			Kind:    "anthropic",
			BaseURL: "https://api.anthropic.com",
			Models:  []config.Model{{Name: "claude-haiku-4-5", Tier: "cheap"}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client, err := BuildRouter(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	router := client.(*adapter.MultiStreamer)
	if router.PolicyName() != "cheap-first" {
		t.Errorf("PolicyName() = %q, want cheap-first", router.PolicyName())
	}
}

func TestBuildRouter_DefaultPolicyIsFallbackChain(t *testing.T) {
	cfg := config.Default()
	cfg.Router = config.RouterConfig{
		Enabled:    true,
		Candidates: []string{"anthropic"},
	}
	cfg.Providers = []config.Provider{
		{
			Name:         "anthropic",
			Kind:         "anthropic",
			BaseURL:      "https://api.anthropic.com",
			DefaultModel: "claude-haiku-4-5",
			Models:       []config.Model{{Name: "claude-haiku-4-5", Tier: "cheap"}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	client, err := BuildRouter(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	if client.(*adapter.MultiStreamer).PolicyName() != "fallback-chain" {
		t.Errorf("default policy = %q, want fallback-chain", client.(*adapter.MultiStreamer).PolicyName())
	}
}

func TestPickPolicy_RejectsUnknown(t *testing.T) {
	if _, err := pickPolicy("magic-router"); err == nil {
		t.Error("expected error for unknown policy")
	}
}

func TestHealthOptionsFromConfig_AppliesDefaultsWhenUnset(t *testing.T) {
	got := healthOptionsFromConfig(config.RouterConfig{Enabled: true})
	if got.Threshold != config.DefaultRouterHealthFailureThreshold {
		t.Errorf("Threshold = %d, want default %d",
			got.Threshold, config.DefaultRouterHealthFailureThreshold)
	}
	wantWindow := time.Duration(config.DefaultRouterHealthWindowSeconds) * time.Second
	if got.Window != wantWindow {
		t.Errorf("Window = %v, want default %v", got.Window, wantWindow)
	}
}

func TestHealthOptionsFromConfig_PreservesExplicitValues(t *testing.T) {
	got := healthOptionsFromConfig(config.RouterConfig{
		Enabled:                true,
		HealthWindowSeconds:    30,
		HealthFailureThreshold: 5,
	})
	if got.Window != 30*time.Second {
		t.Errorf("Window = %v, want 30s", got.Window)
	}
	if got.Threshold != 5 {
		t.Errorf("Threshold = %d, want 5", got.Threshold)
	}
}

func TestHealthOptionsFromConfig_DisabledWhenWindowZero(t *testing.T) {
	// User opt-out: setting window=0 explicitly with a non-zero
	// threshold should NOT trigger the both-zero default branch.
	got := healthOptionsFromConfig(config.RouterConfig{
		Enabled:                true,
		HealthWindowSeconds:    0,
		HealthFailureThreshold: 5,
	})
	if got.Window != 0 {
		t.Errorf("Window = %v, want 0 (explicit user opt-out)", got.Window)
	}
	if got.Threshold != 5 {
		t.Errorf("Threshold = %d, want 5 (preserved)", got.Threshold)
	}
}

// TestResolveConfiguredModel_GeminiIncludesModelsDevMerge guards the
// router's model→provider resolution: a Gemini model present only via
// the models.dev augmentation (not the embedded catalog) must still
// resolve to the gemini profile, since the /model picker offers it
// and the user can persist it as the default.
func TestResolveConfiguredModel_GeminiIncludesModelsDevMerge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	embedded := map[string]bool{}
	for _, m := range catalog.Get("gemini") {
		embedded[m.ID] = true
	}
	var merged string
	for _, m := range catalog.Curated("gemini") {
		if !embedded[m.ID] {
			merged = m.ID
			break
		}
	}
	if merged == "" {
		t.Fatalf("models.dev snapshot should add gemini models beyond the embedded catalog")
	}

	cfg := config.Default()
	cfg.Providers = []config.Provider{{Name: "gemini", Kind: "gemini"}}
	rc, ok := resolveConfiguredModel(cfg, merged)
	if !ok {
		t.Fatalf("resolveConfiguredModel(%q) not found; merged gemini models must resolve", merged)
	}
	if rc.Provider.Name != "gemini" || rc.Model != merged {
		t.Errorf("resolved %s/%s, want gemini/%s", rc.Provider.Name, rc.Model, merged)
	}
}
