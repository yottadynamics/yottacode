package agentruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/yottadynamics/yottacode/internal/config"
)

func TestIsValidEffortLevel(t *testing.T) {
	for _, ok := range []string{"", "low", "medium", "high"} {
		if !IsValidEffortLevel(ok) {
			t.Errorf("IsValidEffortLevel(%q) = false, want true", ok)
		}
	}
	if IsValidEffortLevel("bogus") {
		t.Error("IsValidEffortLevel(\"bogus\") = true, want false")
	}
}

func TestRebuildAdapterForEffort_NoRouting(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)

	before := rt.Adapter
	if err := RebuildAdapterForEffort(rt, "high"); err != nil {
		t.Fatalf("RebuildAdapterForEffort: %v", err)
	}
	if rt.ChatOptions.ReasoningEffort != "high" {
		t.Errorf("ChatOptions.ReasoningEffort = %q, want \"high\"", rt.ChatOptions.ReasoningEffort)
	}
	if rt.Adapter == before {
		t.Error("expected a freshly constructed adapter, got the same instance")
	}
	if rt.Cfg.Adapter != rt.Adapter {
		t.Error("rt.Cfg.Adapter must stay in sync with rt.Adapter")
	}
	if rt.AgentTool.Adapter != rt.Adapter {
		t.Error("rt.AgentTool.Adapter must stay in sync with rt.Adapter")
	}
}

func TestRebuildAdapterForEffort_InvalidLevelIsRejected(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)
	before := rt.Adapter

	if err := RebuildAdapterForEffort(rt, "bogus"); err == nil {
		t.Error("expected an error for an invalid effort level")
	}
	if rt.Adapter != before {
		t.Error("a rejected effort change must not touch the existing adapter")
	}
}

// writeTestConfig encodes cfg to TOML and writes it to the isolated HOME's
// config.toml (newTestSpec already redirected HOME to a temp dir). Shared
// by every test in this file that needs Build to read a specific,
// hand-built [router]/[[providers]] combination from disk.
func writeTestConfig(t *testing.T, cfg config.Config) {
	t.Helper()
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("config.DefaultPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		t.Fatalf("encode test config: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
}

// writeRouterTestConfig writes a config.toml (under the isolated HOME
// newTestSpec already set up) with router.mode=auto and a single
// provider offering both the advisor and implementer models. No stub
// HTTP server needed: Build's own adapter-selection logic skips
// preflight entirely on the routed path (see runtime.go's Build), and
// BuildRouterAdapters/adapter.NewWithConfig are both lazy — construction
// never dials the network.
func writeRouterTestConfig(t *testing.T) {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{
			Name:         "stub",
			Kind:         "openai",
			BaseURL:      "http://127.0.0.1:0/v1",
			APIKeyEnv:    "STUB_API_KEY",
			DefaultModel: "advisor-model",
			Models: []config.Model{
				{Name: "advisor-model", Tier: "expensive"},
				{Name: "implementer-model", Tier: "cheap"},
			},
		},
	}
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.AdvisorModel = "stub:advisor-model"
	cfg.Router.ImplementerModel = "stub:implementer-model"
	t.Setenv("STUB_API_KEY", "sk-stub")
	writeTestConfig(t, cfg)
}

func TestRebuildAdapterForEffort_KeepsRoutedSessionOnAdvisor(t *testing.T) {
	spec := newTestSpec(t)
	writeRouterTestConfig(t)
	rt, err := NewBuilder().Build(context.Background(), spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rt.Model != "advisor-model" {
		t.Fatalf("precondition: rt.Model = %q, want \"advisor-model\" (routing should already be active)", rt.Model)
	}
	if rt.RouterAdapters == nil {
		t.Fatal("precondition: rt.RouterAdapters must be set for a routed session")
	}

	if err := RebuildAdapterForEffort(rt, "low"); err != nil {
		t.Fatalf("RebuildAdapterForEffort: %v", err)
	}

	if rt.Model != "advisor-model" {
		t.Errorf("rt.Model = %q after effort change — a routed session must stay on the advisor model", rt.Model)
	}
	if rt.RouterAdapters == nil || rt.RouterAdapters.Advisor == nil {
		t.Fatal("rt.RouterAdapters must still be populated after the rebuild")
	}
	if rt.Adapter != rt.RouterAdapters.Advisor {
		t.Error("rt.Adapter must be the freshly rebuilt advisor adapter, not an independent plain adapter")
	}
	if rt.ChatOptions.ReasoningEffort != "low" {
		t.Errorf("ChatOptions.ReasoningEffort = %q, want \"low\"", rt.ChatOptions.ReasoningEffort)
	}
}

func TestRebuildAdapterForEffort_RefreshesActiveSubagentRoutingOnly(t *testing.T) {
	spec := newTestSpec(t)
	writeRouterTestConfig(t)
	rt, err := NewBuilder().Build(context.Background(), spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !rt.RoutingAuto {
		t.Fatal("precondition: config.toml sets mode=auto, expected rt.RoutingAuto=true")
	}
	beforeImplementer := rt.AgentTool.ImplementerAdapter

	if err := RebuildAdapterForEffort(rt, "high"); err != nil {
		t.Fatalf("RebuildAdapterForEffort: %v", err)
	}
	if rt.AgentTool.ImplementerAdapter == beforeImplementer {
		t.Error("subagent routing was already active — its implementer adapter should have been refreshed to the new pair, not left stale")
	}
	if !rt.AgentTool.RouteAuto {
		t.Error("RouteAuto must stay true across an effort rebuild when it was already true")
	}
}

// TestBuild_ModeOffSkipsRouterResolutionForOneshotSpec locks in the fix for
// a `yottacode run` bug: a leftover/misconfigured [router] advisor pair with
// mode="off" must not even be looked at for a spec that doesn't support a
// live routing toggle (oneshot's shape — see SessionSpec.
// SupportsLiveRouterToggle). Before the fix, Build called
// cli.BuildRouterAdapters unconditionally, which resolved the pair, failed
// on the unconfigured provider, and surfaced a confusing "[advisor] pair
// unresolved" warning for a session that would never use it.
func TestBuild_ModeOffSkipsRouterResolutionForOneshotSpec(t *testing.T) {
	spec := newTestSpec(t) // SupportsLiveRouterToggle: false, same as oneshot

	cfg := config.Default()
	cfg.Router.Mode = config.RouterModeOff
	cfg.Router.ImplementerModel = "openai-auth:gpt-5.5"
	cfg.Router.AdvisorModel = "nvidia-nim-glm:z-ai/glm-5.2" // provider not configured
	writeTestConfig(t, cfg)

	rt, err := NewBuilder().Build(context.Background(), spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rt.RouterAdapters != nil {
		t.Error("rt.RouterAdapters should stay nil — the pair must never be resolved for this spec/mode combination")
	}
	for _, w := range rt.Warnings {
		if strings.Contains(w, "pair unresolved") {
			t.Errorf("unexpected advisor-pair warning for a oneshot-shaped, routing-off session: %q", w)
		}
	}
}

// TestBuild_ModeAutoWithUnresolvablePairDegradesGracefully locks in the
// fix for a second, more severe variant of the same bug: a user turns
// routing on with a valid pair, then the underlying provider/model gets
// deleted (or its auth revoked) — mode="auto" stays persisted, but the
// pair no longer resolves. Before the fix this was fatal twice over:
// config.Validate hard-failed config.Load itself (discarding the user's
// whole config, breaking every command — TUI, run, ACP all call
// LoadDefault), and even past that, Build's own RoutingEnabled() branch
// returned the resolution error, refusing to construct a session at all.
// Both must now degrade to a warning, for both TUI/ACP-shaped specs
// (SupportsLiveRouterToggle: true) and oneshot-shaped ones.
func TestBuild_ModeAutoWithUnresolvablePairDegradesGracefully(t *testing.T) {
	for _, liveToggle := range []bool{true, false} {
		t.Run(fmt.Sprintf("SupportsLiveRouterToggle=%v", liveToggle), func(t *testing.T) {
			spec := newTestSpec(t)
			spec.SupportsLiveRouterToggle = liveToggle

			cfg := config.Default()
			cfg.Router.Mode = config.RouterModeAuto
			cfg.Router.ImplementerModel = "openai-auth:gpt-5.5"
			cfg.Router.AdvisorModel = "nvidia-nim-glm:z-ai/glm-5.2" // provider deleted after the pair was set
			writeTestConfig(t, cfg)

			rt, err := NewBuilder().Build(context.Background(), spec)
			if err != nil {
				t.Fatalf("Build must degrade gracefully, not fail the whole session: %v", err)
			}
			if rt.RouterAdapters != nil {
				t.Error("rt.RouterAdapters should be nil — the pair never resolved")
			}
			if rt.RoutingAuto {
				t.Error("rt.RoutingAuto must be false — persisted mode=auto must not claim live routing when the pair failed to resolve")
			}
			if rt.AgentTool.RouteAuto {
				t.Error("AgentTool.RouteAuto must be false to match rt.RoutingAuto")
			}
			found := false
			for _, w := range rt.Warnings {
				if strings.Contains(w, "pair unresolved") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an \"[advisor] pair unresolved\" warning, got: %v", rt.Warnings)
			}
			// The session must still be usable on its active/default model.
			if rt.Adapter == nil {
				t.Error("rt.Adapter must still be set — a broken router pair must fall back to the plain adapter")
			}
		})
	}
}

// TestBuild_CandidatesRouterWithUnresolvableCandidateDegradesGracefully
// is the [router].candidates (multi-provider failover) counterpart of
// TestBuild_ModeAutoWithUnresolvablePairDegradesGracefully: this is a
// separate opt-in feature (Router.Enabled, not Router.Mode) with its own
// identical lockout bug — a candidate whose provider got deleted used to
// fail config.Validate/config.Load outright, breaking every command.
func TestBuild_CandidatesRouterWithUnresolvableCandidateDegradesGracefully(t *testing.T) {
	spec := newTestSpec(t)

	cfg := config.Default()
	cfg.Router.Enabled = true
	cfg.Router.Candidates = []string{"ghost-provider:some-model"} // provider deleted after being set

	writeTestConfig(t, cfg)

	rt, err := NewBuilder().Build(context.Background(), spec)
	if err != nil {
		t.Fatalf("Build must degrade gracefully, not fail the whole session: %v", err)
	}
	found := false
	for _, w := range rt.Warnings {
		if strings.Contains(w, "candidates unresolved") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a \"[router] candidates unresolved\" warning, got: %v", rt.Warnings)
	}
	if rt.Adapter == nil {
		t.Error("rt.Adapter must still be set — an unresolvable candidates router must fall back to the plain adapter")
	}
}

// TestBuild_AdvisorChainWithBadFallbackKeepsRoutingLive is the
// end-to-end regression test for the code-review finding that one stale
// entry in an advisor_models/implementer_models failover chain used to
// discard the WHOLE chain — including a perfectly good primary — even
// though nothing was actually wrong with the primary the user configured
// and is actively using. A session in this state must come up with
// routing still fully live on the surviving primary, not degraded to no
// routing at all.
func TestBuild_AdvisorChainWithBadFallbackKeepsRoutingLive(t *testing.T) {
	spec := newTestSpec(t)
	spec.SupportsLiveRouterToggle = true

	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{
			Name:         "stub",
			Kind:         "openai",
			BaseURL:      "http://127.0.0.1:0/v1",
			APIKeyEnv:    "STUB_API_KEY",
			DefaultModel: "advisor-model",
			Models: []config.Model{
				{Name: "advisor-model", Tier: "expensive"},
				{Name: "implementer-model", Tier: "cheap"},
			},
		},
	}
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.ImplementerModel = "stub:implementer-model"
	cfg.Router.AdvisorModels = []string{"stub:advisor-model", "ghost-provider:some-model"} // good primary, stale fallback
	t.Setenv("STUB_API_KEY", "sk-stub")
	writeTestConfig(t, cfg)

	rt, err := NewBuilder().Build(context.Background(), spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rt.RouterAdapters == nil || rt.RouterAdapters.Advisor == nil {
		t.Fatalf("rt.RouterAdapters must still be built from the surviving primary, got %+v", rt.RouterAdapters)
	}
	if rt.RouterAdapters.AdvisorModel != "advisor-model" {
		t.Errorf("AdvisorModel = %q, want the good primary advisor-model", rt.RouterAdapters.AdvisorModel)
	}
	if !rt.RoutingAuto {
		t.Error("rt.RoutingAuto must stay true — the pair is live on its surviving primary, not fully unresolved")
	}
	if !rt.AgentTool.RouteAuto {
		t.Error("AgentTool.RouteAuto must match rt.RoutingAuto")
	}
	if rt.Adapter != rt.RouterAdapters.Advisor {
		t.Error("the main session adapter must be the resolved advisor adapter")
	}
	found := false
	for _, w := range rt.Warnings {
		if strings.Contains(w, "partially unresolved") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a \"pair partially unresolved\" warning describing the dropped fallback, got: %v", rt.Warnings)
	}
}

func TestSetAdvisorRouting_EnableWithoutPairErrors(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)
	if rt.RouterAdapters != nil {
		t.Fatal("precondition: this session should have no router pair configured")
	}
	if err := SetAdvisorRouting(rt, true); err == nil {
		t.Error("expected an error enabling advisor routing with no configured pair")
	}
}

func TestSetAdvisorRouting_TogglesAgentToolWiringOnly(t *testing.T) {
	spec := newTestSpec(t)
	writeRouterTestConfig(t)
	rt, err := NewBuilder().Build(context.Background(), spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	mainAdapterBefore := rt.Adapter

	if err := SetAdvisorRouting(rt, false); err != nil {
		t.Fatalf("SetAdvisorRouting(false): %v", err)
	}
	if rt.AgentTool.RouteAuto {
		t.Error("RouteAuto should be false after disabling")
	}
	if rt.AgentTool.ImplementerAdapter != nil || rt.AgentTool.AdvisorAdapter != nil {
		t.Error("implementer/advisor adapters should be cleared after disabling")
	}
	if rt.Adapter != mainAdapterBefore {
		t.Error("SetAdvisorRouting must never touch the main conversation's adapter")
	}

	if err := SetAdvisorRouting(rt, true); err != nil {
		t.Fatalf("SetAdvisorRouting(true): %v", err)
	}
	if !rt.AgentTool.RouteAuto {
		t.Error("RouteAuto should be true after re-enabling")
	}
	if rt.AgentTool.ImplementerAdapter == nil || rt.AgentTool.AdvisorAdapter == nil {
		t.Error("implementer/advisor adapters should be wired again after re-enabling")
	}
	if rt.Adapter != mainAdapterBefore {
		t.Error("SetAdvisorRouting must never touch the main conversation's adapter")
	}
}
