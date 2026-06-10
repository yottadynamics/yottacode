package cli

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
)

func TestBuildRouterAdapters_SingleModelIsPlainClient(t *testing.T) {
	cfg := routingTestConfig()
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	if _, isMulti := ra.Smart.(*adapter.MultiStreamer); isMulti {
		t.Error("a single-model slot should be a plain client, not a MultiStreamer")
	}
}

// TestBuildRouterAdapters_ChainIsMultiStreamer proves a multi-model slot
// becomes a failover MultiStreamer, transparently dropped into the
// adapter.Client slot, with the primary name surfaced for display.
func TestBuildRouterAdapters_ChainIsMultiStreamer(t *testing.T) {
	cfg := routingTestConfig()
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModels = []string{"anthropic:claude-opus-4-6", "anthropic:claude-haiku-4-5"}
	cfg.Router.Policy = "fallback-chain"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	if ra.Smart == nil {
		t.Fatal("smart chain adapter must be non-nil")
	}
	if _, isMulti := ra.Smart.(*adapter.MultiStreamer); !isMulti {
		t.Errorf("a multi-model slot should be a *MultiStreamer, got %T", ra.Smart)
	}
	if ra.SmartModel != "claude-opus-4-6" {
		t.Errorf("SmartModel should be the primary, got %q", ra.SmartModel)
	}
	if _, isMulti := ra.Fast.(*adapter.MultiStreamer); isMulti {
		t.Error("a single fast model should not be wrapped in a MultiStreamer")
	}
}

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
	cfg := routingTestConfig() // Router.Mode unset == off, no fast/smart pair
	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	if ra != nil {
		t.Errorf("expected nil RouterAdapters when no pair is configured, got %+v", ra)
	}
}

// TestBuildRouterAdapters_BuildsWhenConfiguredEvenIfModeOff proves the
// build is decoupled from Mode: a configured fast/smart pair resolves its
// adapters even in "off" mode, so /router can flip routing on live
// without rebuilding. The caller (run.go) gates *use* via RoutingAuto().
func TestBuildRouterAdapters_BuildsWhenConfiguredEvenIfModeOff(t *testing.T) {
	cfg := routingTestConfig()
	cfg.Router.Mode = config.RouterModeOff
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	if ra == nil {
		t.Fatal("expected adapters built for a configured pair even in off mode")
	}
	if ra.Fast == nil || ra.Smart == nil {
		t.Error("Fast/Smart adapters must be non-nil")
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

// TestBuildRouterAdapters_SameModelDifferentProvidersDistinctClients is the
// regression guard for the memo-key bug: a failover chain naming the SAME
// model on two providers must build two distinct adapters (one per
// provider), or the MultiStreamer silently fails over to the same endpoint.
func TestBuildRouterAdapters_SameModelDifferentProvidersDistinctClients(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{
			Name: "prov-a", Kind: "openai-compatible",
			BaseURL: "http://a.example/v1", APIKeyEnv: "A_KEY",
			DefaultModel: "shared-model",
			Models:       []config.Model{{Name: "shared-model"}},
		},
		{
			Name: "prov-b", Kind: "openai-compatible",
			BaseURL: "http://b.example/v1", APIKeyEnv: "B_KEY",
			DefaultModel: "shared-model",
			Models:       []config.Model{{Name: "shared-model"}},
		},
	}
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.FastModel = "prov-a:shared-model"
	cfg.Router.SmartModels = []string{"prov-a:shared-model", "prov-b:shared-model"}
	t.Setenv("A_KEY", "k1")
	t.Setenv("B_KEY", "k2")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}
	ms, ok := ra.Smart.(*adapter.MultiStreamer)
	if !ok {
		t.Fatalf("smart slot should be a *MultiStreamer, got %T", ra.Smart)
	}
	cands := ms.Candidates()
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if cands[0].Streamer == cands[1].Streamer {
		t.Error("same-model chain on two providers collapsed to one adapter — failover is a no-op (memo collision)")
	}
}

// A stale pair (provider removed after the models were picked) errors
// out of BuildRouterAdapters regardless of mode — the run.go/oneshot.go
// callers degrade that to a warning when routing is OFF (a session that
// doesn't route must not refuse to start over a leftover pair), and
// abort only when routing is enabled.
func TestBuildRouterAdapters_StalePairErrorsEvenWhenOff(t *testing.T) {
	cfg := routingTestConfig()
	cfg.Router.Mode = config.RouterModeOff
	cfg.Router.FastModel = "ghost-provider:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	if _, err := BuildRouterAdapters(cfg, ChatOptions{}); err == nil {
		t.Fatal("expected an error for a pair referencing an unknown provider")
	}
}

// Slot chains dispatch in WRITTEN order and ignore [router].policy —
// that knob orders the candidates router only. Pinned by building with
// a policy name pickPolicy rejects: chains must succeed anyway (they
// no longer consult it); the candidates router still validates it.
func TestBuildRouterAdapters_ChainsIgnoreCandidatesPolicy(t *testing.T) {
	cfg := routingTestConfig()
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.Policy = "bogus-policy"
	cfg.Router.FastModels = []string{"anthropic:claude-haiku-4-5"}
	cfg.Router.SmartModels = []string{"anthropic:claude-opus-4-6", "anthropic:claude-haiku-4-5"}
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("chains must not consult the candidates policy; got %v", err)
	}
	if ra.SmartModel != "claude-opus-4-6" {
		t.Errorf("SmartModel = %q, want the chain's written primary", ra.SmartModel)
	}
}
