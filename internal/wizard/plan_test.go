package wizard

import (
	"strings"
	"testing"
)

// TestPlanRenderTOML walks the canonical render path and checks
// every required section appears in order. We don't compare against
// a golden string because the format is allowed to evolve; we check
// invariants instead.
func TestPlanRenderTOML(t *testing.T) {
	plan := newAnthropicPlan()
	out := plan.RenderTOML()

	wantContains := []string{
		`[active]`,
		`provider      = "anthropic"`,
		`default_model = "claude-sonnet-4-6"`,
		`[[providers]]`,
		`name          = "anthropic"`,
		`kind          = "anthropic"`,
		`base_url      = "https://api.anthropic.com"`,
		`api_key_env   = "ANTHROPIC_API_KEY"`,
		// [[providers.models]] is no longer emitted: the curated
		// model list lives in internal/catalog/catalog.gen.json and
		// is consumed at runtime, not authored into config.toml.
	}
	for _, want := range wantContains {
		if !strings.Contains(out, want) {
			t.Errorf("RenderTOML missing %q\n---\n%s", want, out)
		}
	}

	// [active] must appear before the first [[providers]] block —
	// it's nicer to read.
	if i, j := strings.Index(out, "[active]"), strings.Index(out, "[[providers]]"); !(i < j && i >= 0) {
		t.Errorf("expected [active] before [[providers]] (i=%d j=%d)", i, j)
	}
}

// TestPlanToConfig_DefaultModelMirrorsModel: ToConfig must keep
// Active.Model and Active.DefaultModel in sync so older readers
// (cfg.Active.Model) and newer ones see the same value.
func TestPlanToConfig_DefaultModelMirrorsModel(t *testing.T) {
	plan := newAnthropicPlan()
	cfg := plan.ToConfig()
	if cfg.Active.DefaultModel != cfg.Active.Model {
		t.Errorf("DefaultModel %q != Model %q", cfg.Active.DefaultModel, cfg.Active.Model)
	}
}

// TestPlanRenderEnvSorted: keys must come out in alphabetical order
// so diffs are stable across runs.
func TestPlanRenderEnvSorted(t *testing.T) {
	plan := Plan{
		EnvKeys: map[string]string{
			"OPENAI_API_KEY":    "sk-openai",
			"ANTHROPIC_API_KEY": "sk-ant",
			"GEMINI_API_KEY":    "g-key",
		},
	}
	out := plan.RenderEnv()
	want := []string{"ANTHROPIC_API_KEY=", "GEMINI_API_KEY=", "OPENAI_API_KEY="}
	prev := -1
	for _, w := range want {
		i := strings.Index(out, w)
		if i < 0 {
			t.Fatalf("RenderEnv missing %q in %q", w, out)
		}
		if i <= prev {
			t.Errorf("RenderEnv keys out of order: %q at %d, prev was %d", w, i, prev)
		}
		prev = i
	}
}

// TestPlanRenderEnvSkip: SkipEnvWrite or no keys → empty.
func TestPlanRenderEnvSkip(t *testing.T) {
	if got := (Plan{}).RenderEnv(); got != "" {
		t.Errorf("empty plan should render empty env, got %q", got)
	}
	if got := (Plan{SkipEnvWrite: true, EnvKeys: map[string]string{"K": "v"}}).RenderEnv(); got != "" {
		t.Errorf("skipped plan should render empty, got %q", got)
	}
}

// TestPlanValidate: all the must-fail paths.
func TestPlanValidate(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Plan)
		want string
	}{
		{
			name: "no providers",
			mut:  func(p *Plan) { p.Providers = nil },
			want: "at least one provider",
		},
		{
			name: "duplicate provider name",
			mut: func(p *Plan) {
				p.Providers = append(p.Providers, p.Providers[0])
			},
			want: "duplicate provider",
		},
		{
			name: "empty base url",
			mut:  func(p *Plan) { p.Providers[0].BaseURL = "" },
			want: "base_url is empty",
		},
		{
			name: "active not in list",
			mut:  func(p *Plan) { p.ActiveProvider = "ghost" },
			want: "active provider",
		},
		{
			name: "router invalid policy",
			mut: func(p *Plan) {
				p.EnableRouter = true
				p.RouterPolicy = "neener"
				p.RouterCandList = []string{"anthropic"}
			},
			want: "policy",
		},
		{
			name: "router empty list",
			mut: func(p *Plan) {
				p.EnableRouter = true
				p.RouterPolicy = "fallback-chain"
				p.RouterCandList = nil
			},
			want: "no candidates",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newAnthropicPlan()
			tc.mut(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q", err.Error(), tc.want)
			}
		})
	}
}

// TestPlanValidateSuccessRoute makes sure a valid plan passes both
// the wizard and the underlying config validators.
func TestPlanValidateSuccessRoute(t *testing.T) {
	plan := newAnthropicPlan()
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestPlanToConfig_RetrievalFields(t *testing.T) {
	plan := newAnthropicPlan()
	plan.RetrievalStrategy = "auto"
	plan.EmbeddingModel = "nomic-embed-text"
	cfg := plan.ToConfig()

	if cfg.Retrieval.Strategy != "auto" {
		t.Errorf("expected strategy %q, got %q", "auto", cfg.Retrieval.Strategy)
	}
	if cfg.Retrieval.EmbeddingModel != "nomic-embed-text" {
		t.Errorf("expected embedding_model %q, got %q", "nomic-embed-text", cfg.Retrieval.EmbeddingModel)
	}
}

func TestPlanToConfig_RetrievalFieldsEmpty(t *testing.T) {
	plan := newAnthropicPlan()
	cfg := plan.ToConfig()

	if cfg.Retrieval.Strategy != "auto" {
		t.Errorf("default strategy should be %q, got %q", "auto", cfg.Retrieval.Strategy)
	}
	if cfg.Retrieval.EmbeddingModel != "nomic-embed-text" {
		t.Errorf("default embedding_model should be %q, got %q", "nomic-embed-text", cfg.Retrieval.EmbeddingModel)
	}
}

func TestPlanRenderTOML_WithRetrieval(t *testing.T) {
	plan := newAnthropicPlan()
	plan.RetrievalStrategy = "auto"
	plan.EmbeddingModel = "nomic-embed-text"
	out := plan.RenderTOML()

	wantContains := []string{
		`[retrieval]`,
		`strategy        = "auto"`,
		`embedding_model = "nomic-embed-text"`,
	}
	for _, want := range wantContains {
		if !strings.Contains(out, want) {
			t.Errorf("RenderTOML missing %q\n---\n%s", want, out)
		}
	}
}

func TestPlanRenderTOML_WithoutRetrieval(t *testing.T) {
	plan := newAnthropicPlan()
	out := plan.RenderTOML()

	if strings.Contains(out, "[retrieval]") {
		t.Errorf("RenderTOML should not contain [retrieval] when fields are empty\n---\n%s", out)
	}
}

// newAnthropicPlan builds a representative plan used across many
// tests. Anthropic is convenient because it has a real api_key_env.
// PlanProvider stopped carrying a Models slice when the curated
// model list moved to internal/catalog/catalog.gen.json — the wizard
// only authors directory metadata + default_model now.
func newAnthropicPlan() Plan {
	e := *FindCatalogEntry("anthropic")
	const syntheticModel = "claude-sonnet-4-6"
	pp := PlanProvider{
		Name:         e.Name,
		Kind:         e.Kind,
		BaseURL:      e.BaseURL,
		APIKeyEnv:    e.APIKeyEnv,
		DefaultModel: syntheticModel,
	}
	return Plan{
		Providers:      []PlanProvider{pp},
		ActiveProvider: e.Name,
		ActiveModel:    syntheticModel,
		EnvKeys:        map[string]string{e.APIKeyEnv: "sk-test"},
	}
}
