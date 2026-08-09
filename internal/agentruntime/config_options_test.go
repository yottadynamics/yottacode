package agentruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
