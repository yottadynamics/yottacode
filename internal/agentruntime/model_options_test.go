package agentruntime

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

func writeTwoProviderConfig(t *testing.T) {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{Name: "alpha", Kind: "openai", BaseURL: "http://127.0.0.1:0/v1", APIKeyEnv: "ALPHA_KEY", DefaultModel: "alpha-1",
			Models: []config.Model{{Name: "alpha-1"}, {Name: "shared"}}},
		{Name: "beta", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1", APIKeyEnv: "BETA_KEY", DefaultModel: "beta-1",
			Models: []config.Model{{Name: "beta-1"}, {Name: "shared"}}},
	}
	cfg.Active.Provider = "alpha"
	cfg.Active.Model = "alpha-1"
	t.Setenv("ALPHA_KEY", "sk-alpha")
	t.Setenv("BETA_KEY", "sk-beta")
	writeTestConfig(t, cfg)
}

func TestModelChoices_DedupesAcrossProviders(t *testing.T) {
	spec := newTestSpec(t)
	writeTwoProviderConfig(t)
	rt := mustBuild(t, spec)

	got := map[string]string{}
	for _, c := range ModelChoices(rt) {
		if _, dup := got[c.Model]; dup {
			t.Errorf("model %q listed twice", c.Model)
		}
		got[c.Model] = c.Provider
	}
	for _, m := range []string{"alpha-1", "beta-1", "shared", "test-model"} {
		if _, ok := got[m]; !ok {
			t.Errorf("missing model %q in %v", m, got)
		}
	}
	if got["shared"] != "alpha" {
		t.Errorf("shared model provider = %q, want active provider alpha", got["shared"])
	}
}

func TestRebuildAdapterForModel_SwitchesProvider(t *testing.T) {
	spec := newTestSpec(t)
	writeTwoProviderConfig(t)
	rt := mustBuild(t, spec)
	before := rt.Adapter

	if err := RebuildAdapterForModel(rt, "beta-1"); err != nil {
		t.Fatalf("RebuildAdapterForModel: %v", err)
	}
	o := rt.ChatOptions
	if o.Model != "beta-1" || o.Provider != "beta" || o.ProviderKind != "openai-compatible" ||
		o.BaseURL != "http://127.0.0.1:1/v1" || o.APIKey != "sk-beta" {
		t.Errorf("ChatOptions not adopted from provider beta: %+v", o)
	}
	if rt.Model != "beta-1" || rt.Session.Model != "beta-1" {
		t.Errorf("rt.Model=%q Session.Model=%q, want beta-1", rt.Model, rt.Session.Model)
	}
	if rt.Adapter == before || rt.Cfg.Adapter != rt.Adapter || rt.AgentTool.Adapter != rt.Adapter {
		t.Error("adapter not rebuilt and kept in sync")
	}
}

func TestRebuildAdapterForModel_UnknownModelRejected(t *testing.T) {
	spec := newTestSpec(t)
	writeTwoProviderConfig(t)
	rt := mustBuild(t, spec)
	before := rt.Adapter

	if err := RebuildAdapterForModel(rt, "nope"); err == nil {
		t.Fatal("expected error for unknown model")
	}
	if rt.Adapter != before || rt.ChatOptions.Model != "test-model" {
		t.Error("a rejected model change must not touch the session")
	}
}

func TestRebuildAdapterForModel_RefusedUnderRouting(t *testing.T) {
	spec := newTestSpec(t)
	writeRouterTestConfig(t)
	rt := mustBuild(t, spec)
	if err := RebuildAdapterForModel(rt, "implementer-model"); err == nil {
		t.Fatal("expected refusal while advisor routing owns the main model")
	}
}
