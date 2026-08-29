package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

func TestDefault_IsValid(t *testing.T) {
	if err := Validate(Default()); err != nil {
		t.Fatalf("Default config should validate: %v", err)
	}
}

func TestDefaultsTOML_RoundTripsToDefault(t *testing.T) {
	// Writing the documented defaults to disk and reading them back
	// should reproduce Default() exactly. This guards against the
	// docstring drifting from the in-memory defaults.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(DefaultsTOML), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load(DefaultsTOML) = %+v, want %+v", got, want)
	}
}

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("missing file should be soft, got %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("missing file should return defaults; got %+v", cfg)
	}
}

func TestDefault_SandboxUsesNamedDefaultImage(t *testing.T) {
	if got := Default().Sandbox.Image; got != DefaultSandboxImage {
		t.Fatalf("Default().Sandbox.Image = %q, want %q", got, DefaultSandboxImage)
	}
	if got := Default().Sandbox.DocumentsImage; got != DefaultSandboxDocumentsImage {
		t.Fatalf("Default().Sandbox.DocumentsImage = %q, want %q", got, DefaultSandboxDocumentsImage)
	}
	if !strings.Contains(DefaultSandboxImage, "yottacode-sandbox") {
		t.Fatalf("DefaultSandboxImage = %q, want official yottacode sandbox image", DefaultSandboxImage)
	}
}

func TestDefault_SandboxUsesHostNetwork(t *testing.T) {
	// Host networking is a temporary default so sandboxed developer commands can
	// download dependencies until yottacode grows a per-destination allowlist.
	if got := Default().Sandbox.Network; got != "host" {
		t.Fatalf("Default().Sandbox.Network = %q, want host", got)
	}
}

func TestDefault_SandboxUsesDefaultDNS(t *testing.T) {
	if !reflect.DeepEqual(Default().Sandbox.DNS, DefaultSandboxDNS) {
		t.Fatalf("Default().Sandbox.DNS = %v, want %v", Default().Sandbox.DNS, DefaultSandboxDNS)
	}
}

func TestLoad_SandboxDNS(t *testing.T) {
	src := `[sandbox]
backend = "podman"
dns = ["1.1.1.1", "2606:4700:4700::1111"]
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"1.1.1.1", "2606:4700:4700::1111"}
	if !reflect.DeepEqual(cfg.Sandbox.DNS, want) {
		t.Fatalf("Sandbox.DNS = %v, want %v", cfg.Sandbox.DNS, want)
	}
}

func TestLoad_RejectsInvalidSandboxDNS(t *testing.T) {
	for _, dns := range []string{"", "localhost", "1.2.3.999"} {
		src := `[sandbox]
backend = "podman"
dns = ["` + dns + `"]
`
		_, err := Load(writeFile(t, src))
		if err == nil {
			t.Fatalf("expected invalid sandbox.dns %q to fail", dns)
		}
		if !strings.Contains(err.Error(), "sandbox.dns") {
			t.Fatalf("error should mention sandbox.dns, got %v", err)
		}
	}
}

func TestLoad_AppliesOverrides(t *testing.T) {
	src := `
[context]
warn_threshold = 0.5
auto_threshold = 0.9
	compaction_threshold = 0.6
	compaction_target_ratio = 0.25

[retrieval]
top_k = 5
`
	path := writeFile(t, src)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Context.WarnThreshold != 0.5 {
		t.Errorf("warn = %.2f, want 0.5", cfg.Context.WarnThreshold)
	}
	if cfg.Context.AutoThreshold != 0.9 {
		t.Errorf("auto = %.2f, want 0.9", cfg.Context.AutoThreshold)
	}
	if cfg.Context.CompactionThreshold != 0.6 {
		t.Errorf("compaction = %.2f, want 0.6", cfg.Context.CompactionThreshold)
	}
	if cfg.Context.CompactionTargetRatio != 0.25 {
		t.Errorf("target_ratio = %.2f, want 0.25", cfg.Context.CompactionTargetRatio)
	}
	if cfg.Retrieval.TopK != 5 {
		t.Errorf("top_k = %d, want 5", cfg.Retrieval.TopK)
	}
}

// TestLoad_SandboxRejectsMissingResourceLimits pins the sandbox's resource
// contract: enabling podman without concrete cgroup limits must fail at config
// load, not reach NewPodmanSandbox as --memory= / --cpus=0 / --pids-limit=0.
func TestLoad_SandboxRejectsMissingResourceLimits(t *testing.T) {
	for _, src := range []string{
		`[sandbox]
backend = "podman"
memory = ""`,
		`[sandbox]
backend = "podman"
cpus = 0`,
		`[sandbox]
backend = "podman"
pids_limit = 0`,
		`[sandbox]
backend = "podman"
documents_image = ""`,
	} {
		if _, err := Load(writeFile(t, src)); err == nil {
			t.Fatalf("expected podman sandbox with missing/zero limit to fail load: %s", src)
		}
	}
}

func TestLoad_RejectsOutOfRangeThreshold(t *testing.T) {
	src := `[context]
warn_threshold = 1.5
auto_threshold = 0.9`
	path := writeFile(t, src)
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for out-of-range threshold; got nil")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should mention out of range; got %q", err)
	}
}

// TestLoad_ParsesSkillsDefaultOn locks the [skills] block schema.
// Without this test, a TOML-tag typo on DefaultOn would silently
// keep the field empty and the seeding logic in tui/run.go would
// quietly fall back to the default-off behavior — undetectable from
// the outside.
func TestLoad_ParsesSkillsDefaultOn(t *testing.T) {
	src := `
[skills]
default_on = ["test-driven-development", "diagnose"]
`
	path := writeFile(t, src)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Skills.DefaultOn
	want := []string{"test-driven-development", "diagnose"}
	if len(got) != len(want) {
		t.Fatalf("default_on = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoad_RejectsWarnAboveAuto(t *testing.T) {
	src := `[context]
warn_threshold = 0.9
auto_threshold = 0.7`
	path := writeFile(t, src)
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error when warn > auto")
	}
}

func TestLoad_ContextCompactionThreshold(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr bool
	}{
		{
			name: "below auto accepted for busy-turn compaction",
			src: `[context]
warn_threshold = 0.5
auto_threshold = 0.85
compaction_threshold = 0.70`,
		},
		{
			name: "equal auto accepted",
			src: `[context]
warn_threshold = 0.5
auto_threshold = 0.85
compaction_threshold = 0.85`,
		},
		{
			name: "auto disabled with compaction enabled accepted",
			src: `[context]
warn_threshold = 0.5
auto_threshold = 1.0
compaction_threshold = 0.90`,
		},
		{
			name: "compaction disabled accepted",
			src: `[context]
warn_threshold = 0.5
auto_threshold = 0.85
compaction_threshold = 1.0`,
		},
		{
			name: "out of range rejected",
			src: `[context]
warn_threshold = 0.5
auto_threshold = 0.85
compaction_threshold = 1.2`,
			wantErr: true,
		},
		{
			name: "target ratio out of range rejected",
			src: `[context]
warn_threshold = 0.5
auto_threshold = 0.85
compaction_threshold = 0.70
compaction_target_ratio = 0.90`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeFile(t, c.src))
			if c.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoad_UnknownKeyErrors(t *testing.T) {
	src := `[context]
mystery_key = 1`
	path := writeFile(t, src)
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "mystery_key") {
		t.Errorf("error should mention the unknown key; got %q", err)
	}
}

func TestLoad_UnknownSectionErrors(t *testing.T) {
	src := `[wrong_section]
foo = 1`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error for unknown section")
	}
}

func TestLoad_AcceptsCommentsAndBlankLines(t *testing.T) {
	src := `
# top-level comment
[context]
# inline section
warn_threshold = 0.4   # trailing comment
auto_threshold = 0.8
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Context.WarnThreshold != 0.4 {
		t.Errorf("warn = %.2f, want 0.4", cfg.Context.WarnThreshold)
	}
}

func TestEnsureDefault_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	out, err := EnsureDefault(path)
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if out != path {
		t.Errorf("EnsureDefault returned %q, want %q", out, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "warn_threshold") {
		t.Errorf("default file should contain documented keys; got %q", string(body))
	}
}

func TestEnsureDefault_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "# my custom file\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := EnsureDefault(path); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != original {
		t.Errorf("EnsureDefault should not overwrite existing file; got %q", string(body))
	}
}

func TestLoad_AppliesRetrievalOverrides(t *testing.T) {
	src := `
[retrieval]
enabled = false
top_k = 25
min_score = 0.15
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.Enabled {
		t.Errorf("retrieval.enabled = true, want false")
	}
	if cfg.Retrieval.TopK != 25 {
		t.Errorf("retrieval.top_k = %d, want 25", cfg.Retrieval.TopK)
	}
	if cfg.Retrieval.MinScore != 0.15 {
		t.Errorf("retrieval.min_score = %.3f, want 0.15", cfg.Retrieval.MinScore)
	}
}

func TestLoad_RejectsOutOfRangeRetrievalMinScore(t *testing.T) {
	src := `[retrieval]
min_score = 1.5`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error for retrieval.min_score > 1")
	}
	if !strings.Contains(err.Error(), "min_score") {
		t.Errorf("error should mention min_score; got %q", err)
	}
}

func TestRetrieval_SemanticWeightDefaultAndOverride(t *testing.T) {
	// Default is 0.4 (the 60/40 BM25/cosine split).
	if got := Default().Retrieval.SemanticWeight; got != 0.4 {
		t.Errorf("Default semantic_weight = %.3f, want 0.4", got)
	}
	// Unset in config → keeps the default (Load decodes onto Default()).
	cfg, err := Load(writeFile(t, "[retrieval]\ntop_k = 5\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.SemanticWeight != 0.4 {
		t.Errorf("unset semantic_weight = %.3f, want default 0.4", cfg.Retrieval.SemanticWeight)
	}
	// Explicitly set 0.0 (pure BM25) → applied, NOT treated as unset.
	cfg, err = Load(writeFile(t, "[retrieval]\nsemantic_weight = 0.0\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.SemanticWeight != 0.0 {
		t.Errorf("explicit semantic_weight=0.0 = %.3f, want 0.0", cfg.Retrieval.SemanticWeight)
	}
}

func TestLoad_RejectsOutOfRangeSemanticWeight(t *testing.T) {
	_, err := Load(writeFile(t, "[retrieval]\nsemantic_weight = 1.5\n"))
	if err == nil {
		t.Fatalf("expected error for retrieval.semantic_weight > 1")
	}
	if !strings.Contains(err.Error(), "semantic_weight") {
		t.Errorf("error should mention semantic_weight; got %q", err)
	}
}

func TestLoad_RejectsNegativeTopK(t *testing.T) {
	src := `[retrieval]
top_k = -1`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error for negative top_k")
	}
}

func TestSessionRecall_Defaults(t *testing.T) {
	sr := Default().Retrieval.SessionRecall
	if !sr.Auto {
		t.Error("session_recall.auto default = false, want true (on with opt-out)")
	}
	if sr.Scope != "project" {
		t.Errorf("session_recall.scope default = %q, want project", sr.Scope)
	}
	if sr.TopK != 3 || sr.MinScore != 0.6 || sr.MaxBytes != 2000 {
		t.Errorf("session_recall defaults = {TopK:%d MinScore:%.2f MaxBytes:%d}, want {3 0.6 2000}",
			sr.TopK, sr.MinScore, sr.MaxBytes)
	}
	// The documented embedded config must parse and preserve the defaults.
	cfg, err := Load(writeFile(t, DefaultsTOML))
	if err != nil {
		t.Fatalf("Load(DefaultsTOML): %v", err)
	}
	if !cfg.Retrieval.SessionRecall.Auto || cfg.Retrieval.SessionRecall.Scope != "project" {
		t.Errorf("DefaultsTOML session_recall = %+v, want auto+project", cfg.Retrieval.SessionRecall)
	}
}

func TestSessionRecall_EmptyScopeDefaultsAndExplicitAuto(t *testing.T) {
	// Section present without scope → coerced to project.
	cfg, err := Load(writeFile(t, "[retrieval.session_recall]\ntop_k = 5\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.SessionRecall.Scope != "project" {
		t.Errorf("empty scope = %q, want project", cfg.Retrieval.SessionRecall.Scope)
	}
	// Explicit auto=false is honored (not treated as unset).
	cfg, err = Load(writeFile(t, "[retrieval.session_recall]\nauto = false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.SessionRecall.Auto {
		t.Error("explicit auto=false not honored")
	}
}

func TestSessionRecall_RejectsBadScopeAndMinScore(t *testing.T) {
	if _, err := Load(writeFile(t, "[retrieval.session_recall]\nscope = \"galaxy\"\n")); err == nil {
		t.Error("expected error for invalid session_recall.scope")
	}
	if _, err := Load(writeFile(t, "[retrieval.session_recall]\nmin_score = 1.5\n")); err == nil {
		t.Error("expected error for out-of-range session_recall.min_score")
	}
}

func TestLoad_ParsesProvidersWithModels(t *testing.T) {
	src := `
[active]
provider = "anthropic"
model    = "claude-sonnet-4-6"

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
api_key_env   = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name           = "claude-opus-4-7"
  tier           = "expensive"
  context_window = 200000

  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"

  [[providers.models]]
  name = "llama3.1:8b"
  tier = "cheap"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("want 2 providers, got %d", len(cfg.Providers))
	}
	a := cfg.Providers[0]
	if a.Name != "anthropic" || a.Kind != "anthropic" {
		t.Errorf("first provider = %+v", a)
	}
	if len(a.Models) != 2 {
		t.Errorf("want 2 anthropic models, got %d", len(a.Models))
	}
	if a.Models[0].Tier != "expensive" || a.Models[0].ContextWindow != 200000 {
		t.Errorf("opus tier/window = %+v", a.Models[0])
	}
	if cfg.Active.Provider != "anthropic" || cfg.Active.Model != "claude-sonnet-4-6" {
		t.Errorf("active = %+v", cfg.Active)
	}
	if p := cfg.FindProvider("ollama"); p == nil || p.Models[0].Name != "llama3.1:8b" {
		t.Errorf("FindProvider(ollama) = %+v", p)
	}
}

func TestProviderKindForModel(t *testing.T) {
	cfg := Config{
		Active: Active{Provider: "codex"},
		Providers: []Provider{
			// Namesake collision: the API-key provider also lists the
			// active provider's model.
			{Name: "api-openai", Kind: "openai", Models: []Model{{Name: "gpt-5.5"}}},
			{Name: "codex", Kind: "openai-auth", DefaultModel: "gpt-5.5"},
			{Name: "local", Kind: "ollama", DefaultModel: "qwen3.5:9b"},
		},
	}

	// The active provider wins the namesake collision.
	if got := cfg.ProviderKindForModel("gpt-5.5"); got != "openai-auth" {
		t.Errorf("collision: got %q, want openai-auth", got)
	}
	// A non-active provider that explicitly names the model.
	if got := cfg.ProviderKindForModel("qwen3.5:9b"); got != "ollama" {
		t.Errorf("listed elsewhere: got %q, want ollama", got)
	}
	// A model no entry names (openai-auth's scanned set, ollama's local
	// tags) falls back to the active provider's kind.
	if got := cfg.ProviderKindForModel("gpt-5.4-mini"); got != "openai-auth" {
		t.Errorf("unlisted: got %q, want active kind openai-auth", got)
	}
	if got := cfg.ProviderKindForModel(""); got != "" {
		t.Errorf("empty model: got %q, want empty", got)
	}

	// Without an active provider there is no fallback kind.
	cfg.Active.Provider = ""
	if got := cfg.ProviderKindForModel("gpt-5.4-mini"); got != "" {
		t.Errorf("no active provider: got %q, want empty", got)
	}
	// …but explicit listings still resolve.
	if got := cfg.ProviderKindForModel("gpt-5.5"); got != "openai" {
		t.Errorf("no active provider, listed: got %q, want openai", got)
	}
}

func TestContextWindowOverride(t *testing.T) {
	cfg := Config{
		Active: Active{Provider: "primary"},
		Providers: []Provider{
			{
				Name: "primary",
				Models: []Model{
					{Name: "deepseek-v4-flash", ContextWindow: 128_000},
					{Name: "no-window-model"}, // ContextWindow defaults to 0
				},
			},
			{
				Name: "secondary",
				Models: []Model{
					// Name collision with the active provider — the active
					// provider's entry must win.
					{Name: "deepseek-v4-flash", ContextWindow: 64_000},
					{Name: "other-model", ContextWindow: 32_000},
				},
			},
		},
	}

	cases := []struct {
		model string
		want  int
	}{
		{"deepseek-v4-flash", 128_000}, // active provider wins on collision
		{"other-model", 32_000},        // only under a non-active provider, still resolves
		{"no-window-model", 0},         // listed but context_window unset
		{"nonexistent", 0},             // not listed anywhere
		{"", 0},                        // empty model name
	}
	for _, c := range cases {
		if got := cfg.ContextWindowOverride(c.model); got != c.want {
			t.Errorf("ContextWindowOverride(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

func TestLoad_RejectsInlineAPIKey(t *testing.T) {
	src := `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key = "sk-ant-DO-NOT-DO-THIS"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected refusal of inline api_key")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), ".env") {
		t.Errorf("error should point at api_key + .env, got %q", err)
	}
}

func TestLoad_RejectsInvalidTier(t *testing.T) {
	src := `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"

  [[providers.models]]
  name = "claude-opus-4-7"
  tier = "premium"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error for invalid tier")
	}
	if !strings.Contains(err.Error(), "tier") {
		t.Errorf("error should mention tier, got %q", err)
	}
}

func TestLoad_RejectsInvalidKind(t *testing.T) {
	src := `
[[providers]]
name = "weird"
kind = "made-up"
base_url = "https://example.com"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error for invalid kind")
	}
}

func TestLoad_RejectsActiveProviderNotConfigured(t *testing.T) {
	src := `
[active]
provider = "ghost"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error when active.provider points to nothing")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention the missing name, got %q", err)
	}
}

// Legacy single-`model` configs must keep working: after Load, both
// Active.Model and Active.DefaultModel reflect the same value so
// existing callers (cli.Resolve, tui /model handler) read the right
// thing without migration.
func TestLoad_LegacyActiveModelAliasesIntoDefaultModel(t *testing.T) {
	src := `
[active]
provider = "anthropic"
model    = "claude-sonnet-4-6"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"

  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Active.Model != "claude-sonnet-4-6" || cfg.Active.DefaultModel != "claude-sonnet-4-6" {
		t.Errorf("Active = %+v; want both Model and DefaultModel = claude-sonnet-4-6", cfg.Active)
	}
}

// New configs should set default_model directly. Active.Model still
// mirrors it post-normalize so older readers see the same value.
func TestLoad_NewActiveDefaultModelNormalizes(t *testing.T) {
	src := `
[active]
provider      = "anthropic"
default_model = "claude-sonnet-4-6"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"

  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Active.DefaultModel != "claude-sonnet-4-6" {
		t.Errorf("DefaultModel = %q; want claude-sonnet-4-6", cfg.Active.DefaultModel)
	}
	if cfg.Active.Model != cfg.Active.DefaultModel {
		t.Errorf("Active.Model %q should mirror DefaultModel %q", cfg.Active.Model, cfg.Active.DefaultModel)
	}
}

// When both keys are set on disk, default_model wins — that's the
// migration path: the user has moved to the new key and the legacy
// `model` line is stale.
func TestLoad_BothActiveKeysPreferDefaultModel(t *testing.T) {
	src := `
[active]
provider      = "anthropic"
model         = "stale-name"
default_model = "claude-sonnet-4-6"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"

  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Active.DefaultModel != "claude-sonnet-4-6" || cfg.Active.Model != "claude-sonnet-4-6" {
		t.Errorf("default_model should win; Active = %+v", cfg.Active)
	}
}

// Free-form providers (Models[] empty — Ollama, NVIDIA NIM, custom
// proxies) skip the catalog membership check. The catalog gets filled
// by live /models fetch at runtime, so a config-time strict check
// would refuse otherwise-valid setups.
func TestLoad_AcceptsFreeFormProviderWithUnknownActiveModel(t *testing.T) {
	src := `
[active]
provider      = "ollama"
default_model = "llama3.1:8b"

[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Active.DefaultModel != "llama3.1:8b" {
		t.Errorf("free-form default_model dropped; Active = %+v", cfg.Active)
	}
}

// Free-form providers (NVIDIA NIM, Ollama, custom proxies) carry no
// up-front catalog. The user can still set a default_model — the
// runtime fills the catalog via /v1/models or /api/tags. A strict
// membership check at load time would refuse otherwise-valid setups
// (this caused the "default_model 'X' is not in models" error users
// hit when adding NVIDIA NIM via the picker).
func TestLoad_AcceptsFreeFormProviderDefaultModelWithEmptyCatalog(t *testing.T) {
	src := `
[[providers]]
name = "nvidia-nim"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/nemotron-3-super-120b-a12b"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.FindProvider("nvidia-nim")
	if p == nil || p.DefaultModel != "nvidia/nemotron-3-super-120b-a12b" {
		t.Errorf("expected free-form default_model preserved; got %+v", p)
	}
}

func TestLoad_RejectsDefaultModelNotInList(t *testing.T) {
	src := `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
default_model = "claude-undefined"

  [[providers.models]]
  name = "claude-opus-4-7"
  tier = "expensive"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error when default_model is not in models")
	}
}

func TestLoad_RejectsDuplicateProviderNames(t *testing.T) {
	src := `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"

[[providers]]
name = "anthropic"
kind = "openai-compatible"
base_url = "https://other.example"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatalf("expected error for duplicate provider name")
	}
}

func TestLoad_ParsesRouterBlock(t *testing.T) {
	src := `
[router]
enabled    = true
policy     = "fallback-chain"
candidates = ["anthropic:claude-haiku-4-5", "openai:gpt-4o"]

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-haiku-4-5"

  [[providers.models]]
  name = "claude-haiku-4-5"
  tier = "cheap"

[[providers]]
name          = "openai"
kind          = "openai"
base_url      = "https://api.openai.com/v1"
default_model = "gpt-4o"

  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Router.Enabled {
		t.Errorf("router.enabled = false, want true")
	}
	if cfg.Router.Policy != "fallback-chain" {
		t.Errorf("router.policy = %q, want fallback-chain", cfg.Router.Policy)
	}
	if got := len(cfg.Router.Candidates); got != 2 {
		t.Fatalf("router.candidates len = %d, want 2", got)
	}

	resolved, err := cfg.ResolveCandidates()
	if err != nil {
		t.Fatalf("ResolveCandidates: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved len = %d, want 2", len(resolved))
	}
	if resolved[0].Provider.Name != "anthropic" || resolved[0].Model != "claude-haiku-4-5" || resolved[0].Tier != "cheap" {
		t.Errorf("first resolved = %+v", resolved[0])
	}
	if resolved[1].Provider.Name != "openai" || resolved[1].Model != "gpt-4o" || resolved[1].Tier != "balanced" {
		t.Errorf("second resolved = %+v", resolved[1])
	}
}

func TestLoad_RouterCandidateWithoutModelUsesDefault(t *testing.T) {
	src := `
[router]
enabled    = true
candidates = ["anthropic"]

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, err := cfg.ResolveCandidates()
	if err != nil {
		t.Fatalf("ResolveCandidates: %v", err)
	}
	if resolved[0].Model != "claude-sonnet-4-6" {
		t.Errorf("expected fallback to default_model, got %q", resolved[0].Model)
	}
}

func TestLoad_RejectsRouterUnknownProvider(t *testing.T) {
	src := `
[router]
enabled    = true
candidates = ["ghost:some-model"]

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error when candidate names a missing provider")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention provider name %q, got %q", "ghost", err)
	}
}

func TestLoad_RejectsRouterUnknownModel(t *testing.T) {
	src := `
[router]
enabled    = true
candidates = ["anthropic:claude-undefined"]

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error when candidate names a missing model")
	}
	if !strings.Contains(err.Error(), "claude-undefined") {
		t.Errorf("error should mention model name, got %q", err)
	}
}

func TestLoad_RejectsRouterInvalidPolicy(t *testing.T) {
	src := `
[router]
enabled    = true
policy     = "magic-router"
candidates = ["anthropic"]

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error for invalid policy")
	}
	if !strings.Contains(err.Error(), "policy") {
		t.Errorf("error should mention policy, got %q", err)
	}
}

func TestLoad_RejectsRouterEmptyCandidates(t *testing.T) {
	src := `
[router]
enabled = true

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error when router enabled with no candidates")
	}
}

func TestLoad_RejectsRouterCandidateWithoutDefaultModel(t *testing.T) {
	src := `
[router]
enabled    = true
candidates = ["anthropic"]

[[providers]]
name     = "anthropic"
kind     = "anthropic"
base_url = "https://api.anthropic.com"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error when bare candidate has no default_model")
	}
}

func TestLoad_RouterDisabledIgnoresMisconfiguration(t *testing.T) {
	// When router.enabled = false, the rest of the block is descriptive
	// only. A dangling candidate or invalid policy must not fail load.
	src := `
[router]
enabled    = false
policy     = "magic-router"
candidates = ["ghost:something"]

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`
	if _, err := Load(writeFile(t, src)); err != nil {
		t.Errorf("expected disabled router to skip validation, got %v", err)
	}
}

func TestLoad_ParsesRouterHealthFields(t *testing.T) {
	src := `
[router]
enabled                  = true
candidates               = ["anthropic"]
health_window_seconds    = 30
health_failure_threshold = 5

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-haiku-4-5"

  [[providers.models]]
  name = "claude-haiku-4-5"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Router.HealthWindowSeconds != 30 {
		t.Errorf("health_window_seconds = %d, want 30", cfg.Router.HealthWindowSeconds)
	}
	if cfg.Router.HealthFailureThreshold != 5 {
		t.Errorf("health_failure_threshold = %d, want 5", cfg.Router.HealthFailureThreshold)
	}
}

func TestLoad_RejectsNegativeHealthFields(t *testing.T) {
	for _, bad := range []string{
		`health_window_seconds    = -1`,
		`health_failure_threshold = -1`,
	} {
		src := `
[router]
enabled    = true
candidates = ["anthropic"]
` + bad + `

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-haiku-4-5"

  [[providers.models]]
  name = "claude-haiku-4-5"
`
		if _, err := Load(writeFile(t, src)); err == nil {
			t.Errorf("expected error for negative value; src had: %s", bad)
		}
	}
}

func TestParseCandidate(t *testing.T) {
	tests := []struct {
		in           string
		wantProvider string
		wantModel    string
		wantErr      bool
	}{
		{"anthropic", "anthropic", "", false},
		{"anthropic:claude-haiku-4-5", "anthropic", "claude-haiku-4-5", false},
		{"  anthropic : claude-opus  ", "anthropic", "claude-opus", false},
		{"", "", "", true},
		{":model", "", "", true},
		{"provider:", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			p, m, err := ParseCandidate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if p != tt.wantProvider || m != tt.wantModel {
				t.Errorf("ParseCandidate(%q) = (%q, %q), want (%q, %q)",
					tt.in, p, m, tt.wantProvider, tt.wantModel)
			}
		})
	}
}

func TestLoad_ParsesMCPServers(t *testing.T) {
	src := `
[[mcp_servers]]
name    = "filesystem"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-filesystem", "/tmp/scratch"]

[[mcp_servers]]
name     = "memory"
command  = "npx"
args     = ["-y", "@modelcontextprotocol/server-memory"]
env      = { MEMORY_FILE = "$HOME/memory.json" }
disabled = false
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("want 2 MCP servers, got %d", len(cfg.MCPServers))
	}
	fs := cfg.MCPServers[0]
	if fs.Name != "filesystem" || fs.Command != "npx" {
		t.Errorf("filesystem entry = %+v", fs)
	}
	if len(fs.Args) != 3 || fs.Args[2] != "/tmp/scratch" {
		t.Errorf("filesystem args = %+v", fs.Args)
	}
	mem := cfg.MCPServers[1]
	if mem.Env["MEMORY_FILE"] != "$HOME/memory.json" {
		t.Errorf("memory env preserves $VAR raw form; got %+v", mem.Env)
	}
}

func TestLoad_RejectsMCPInvalidName(t *testing.T) {
	for _, bad := range []string{"Filesystem", "1bad", "has space", "name!", ""} {
		src := `
[[mcp_servers]]
name    = "` + bad + `"
command = "npx"
`
		if _, err := Load(writeFile(t, src)); err == nil {
			t.Errorf("expected error for invalid MCP name %q", bad)
		}
	}
}

func TestLoad_RejectsMCPDuplicateName(t *testing.T) {
	src := `
[[mcp_servers]]
name    = "fs"
command = "npx"

[[mcp_servers]]
name    = "fs"
command = "uvx"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error for duplicate MCP name")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate; got %q", err)
	}
}

func TestLoad_RejectsMCPEmptyCommand(t *testing.T) {
	src := `
[[mcp_servers]]
name    = "fs"
command = ""
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error for empty MCP command")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error should mention command; got %q", err)
	}
}

func TestLoad_ParsesMCPHTTPAndSSEServers(t *testing.T) {
	src := `
[[mcp_servers]]
name      = "docs-http"
transport = "http"
url       = "https://example.com/mcp"

[mcp_servers.headers]
Authorization = "Bearer secret"

[[mcp_servers]]
name      = "docs-sse"
transport = "sse"
url       = "https://example.com/mcp/sse"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("want 2 MCP servers, got %d", len(cfg.MCPServers))
	}
	httpSrv := cfg.MCPServers[0]
	if httpSrv.Transport != "http" || httpSrv.URL != "https://example.com/mcp" {
		t.Errorf("http entry = %+v", httpSrv)
	}
	if httpSrv.Headers["Authorization"] != "Bearer secret" {
		t.Errorf("http entry headers = %+v", httpSrv.Headers)
	}
	sseSrv := cfg.MCPServers[1]
	if sseSrv.Transport != "sse" || sseSrv.URL != "https://example.com/mcp/sse" {
		t.Errorf("sse entry = %+v", sseSrv)
	}
}

func TestLoad_RejectsMCPHTTPMissingURL(t *testing.T) {
	src := `
[[mcp_servers]]
name      = "docs"
transport = "http"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error for http transport with no url")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error should mention url; got %q", err)
	}
}

func TestLoad_RejectsMCPUnknownTransport(t *testing.T) {
	src := `
[[mcp_servers]]
name      = "docs"
transport = "carrier-pigeon"
url       = "https://example.com/mcp"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error for unknown transport value")
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Errorf("error should mention transport; got %q", err)
	}
}

func TestLoad_RejectsMCPUnknownKey(t *testing.T) {
	src := `
[[mcp_servers]]
name    = "fs"
command = "npx"
trnsport = "stdio"
`
	_, err := Load(writeFile(t, src))
	if err == nil {
		t.Fatal("expected error for misspelled MCP key (trnsport)")
	}
}

func TestLoad_MCPServersOptional(t *testing.T) {
	cfg, err := Load(writeFile(t, ``))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Errorf("empty file should yield no MCP servers; got %d", len(cfg.MCPServers))
	}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestLoad_MemoryFinalTurnOnQuit locks the [memory] block schema and its
// default-on behavior: absent section keeps the default (true), an
// explicit false disables, and the key survives a Render round-trip —
// encodeTunables must include [memory] or a picker/wizard save would
// silently drop the user's opt-out from disk.
func TestLoad_MemoryFinalTurnOnQuit(t *testing.T) {
	if !Default().Memory.FinalTurnOnQuit {
		t.Fatalf("final_turn_on_quit should default to true")
	}

	cfg, err := Load(writeFile(t, "[context]\nwarn_threshold = 0.5\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Memory.FinalTurnOnQuit {
		t.Errorf("absent [memory] section should keep the default (true)")
	}

	cfg, err = Load(writeFile(t, "[memory]\nfinal_turn_on_quit = false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Memory.FinalTurnOnQuit {
		t.Errorf("explicit final_turn_on_quit = false should disable")
	}

	// Render → Load round-trip preserves the opt-out.
	cfg2, err := Load(writeFile(t, Render(cfg)))
	if err != nil {
		t.Fatalf("Load(Render): %v", err)
	}
	if cfg2.Memory.FinalTurnOnQuit {
		t.Errorf("Render must persist [memory] — opt-out lost in round-trip:\n%s", Render(cfg))
	}
}

// TestLoad_MemoryCaptureReminderEveryTurns locks the periodic capture
// reminder cadence. The load-bearing case is explicit 0: it means DISABLED,
// so it must survive both Load and a Render round-trip rather than being
// coerced back to the default the way an absent key is.
func TestLoad_MemoryCaptureReminderEveryTurns(t *testing.T) {
	if got := Default().Memory.CaptureReminderEveryTurns; got != 6 {
		t.Fatalf("capture_reminder_every_turns should default to 6, got %d", got)
	}

	cfg, err := Load(writeFile(t, "[context]\nwarn_threshold = 0.5\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Memory.CaptureReminderEveryTurns != 6 {
		t.Errorf("absent [memory] section should keep the default 6, got %d", cfg.Memory.CaptureReminderEveryTurns)
	}

	cfg, err = Load(writeFile(t, "[memory]\ncapture_reminder_every_turns = 3\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Memory.CaptureReminderEveryTurns != 3 {
		t.Errorf("explicit cadence = %d, want 3", cfg.Memory.CaptureReminderEveryTurns)
	}

	off, err := Load(writeFile(t, "[memory]\ncapture_reminder_every_turns = 0\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if off.Memory.CaptureReminderEveryTurns != 0 {
		t.Errorf("explicit 0 must stay 0 (disabled), got %d", off.Memory.CaptureReminderEveryTurns)
	}
	roundTripped, err := Load(writeFile(t, Render(off)))
	if err != nil {
		t.Fatalf("Load(Render): %v", err)
	}
	if roundTripped.Memory.CaptureReminderEveryTurns != 0 {
		t.Errorf("Render round-trip resurrected the reminder: got %d, want 0:\n%s",
			roundTripped.Memory.CaptureReminderEveryTurns, Render(off))
	}

	if _, err := Load(writeFile(t, "[memory]\ncapture_reminder_every_turns = -1\n")); err == nil {
		t.Error("negative cadence should be rejected")
	}
}

// NO_COLOR (https://no-color.org/) selects the monochrome theme, but
// only when the user hasn't configured one — explicit configuration
// overrides the env var, exactly as the convention specifies, and an
// empty value does not count as set.
func TestThemeName_HonorsNoColorEnv(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("NO_COLOR", "1")
	cfg, err := Load(filepath.Join(dir, "missing.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Theme.Name != "no-color" {
		t.Errorf("NO_COLOR set + no config: theme = %q, want no-color", cfg.Theme.Name)
	}

	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[theme]\nname = \"nord\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Theme.Name != "nord" {
		t.Errorf("explicit [theme] must override NO_COLOR; got %q", cfg.Theme.Name)
	}

	t.Setenv("NO_COLOR", "")
	cfg, err = Load(filepath.Join(dir, "missing.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Theme.Name != themes.DefaultName {
		t.Errorf("empty NO_COLOR must not trigger monochrome; got %q", cfg.Theme.Name)
	}
}
