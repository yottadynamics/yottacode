package providerops

import (
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

func twoProviderConfig() config.Config {
	return config.Config{
		Active: config.Active{Provider: "anthropic", Model: "claude-sonnet-4-6", DefaultModel: "claude-sonnet-4-6"},
		Providers: []config.Provider{
			{
				Name:         "anthropic",
				Kind:         "anthropic",
				BaseURL:      "https://api.anthropic.com",
				APIKeyEnv:    "ANTHROPIC_API_KEY",
				DefaultModel: "claude-sonnet-4-6",
				Models: []config.Model{
					{Name: "claude-opus-4-7", Tier: "expensive"},
					{Name: "claude-sonnet-4-6", Tier: "balanced"},
					{Name: "claude-haiku-4-5", Tier: "cheap"},
				},
			},
			{
				Name:         "openai",
				Kind:         "openai",
				BaseURL:      "https://api.openai.com/v1",
				DefaultModel: "gpt-4o",
				Models: []config.Model{
					{Name: "gpt-4o", Tier: "balanced"},
					{Name: "o3", Tier: "expensive"},
				},
			},
		},
	}
}

func TestSetActive_AdoptsDefaultModel(t *testing.T) {
	cfg, err := SetActive(twoProviderConfig(), "openai")
	if err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if cfg.Active.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", cfg.Active.Provider)
	}
	if cfg.Active.DefaultModel != "gpt-4o" || cfg.Active.Model != "gpt-4o" {
		t.Errorf("expected gpt-4o adopted, got %+v", cfg.Active)
	}
}

func TestSetActive_UnknownProviderErrors(t *testing.T) {
	_, err := SetActive(twoProviderConfig(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("want error mentioning ghost, got %v", err)
	}
}

func TestRemove_DeletesProviderAndClearsActiveWhenMatching(t *testing.T) {
	cfg, err := Remove(twoProviderConfig(), "anthropic")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "openai" {
		t.Errorf("providers = %v", cfg.Providers)
	}
	// Active was anthropic; now cleared so the next session prompts
	// the user to choose. Auto-switch is the caller's job (TUI/CLI),
	// not Remove's — Remove stays a pure config mutation.
	if cfg.Active.Provider != "" {
		t.Errorf("Active.Provider should be cleared, got %q", cfg.Active.Provider)
	}
}

func TestPickFallback_FirstRemaining(t *testing.T) {
	cfg, err := Remove(twoProviderConfig(), "anthropic")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := PickFallback(cfg); got != "openai" {
		t.Errorf("PickFallback = %q, want openai", got)
	}
}

func TestPickFallback_EmptyWhenNoProvidersLeft(t *testing.T) {
	cfg := config.Config{}
	if got := PickFallback(cfg); got != "" {
		t.Errorf("PickFallback on empty cfg = %q, want empty string", got)
	}
}

// SuggestAPIKeyEnv keeps the well-known catalog default for the
// first profile of a vendor — existing scripts and CI pipelines
// keep working unchanged.
func TestSuggestAPIKeyEnv_FirstProfileKeepsDefault(t *testing.T) {
	cfg := config.Config{}
	got := SuggestAPIKeyEnv(cfg, "nvidia-nim", "NVIDIA_API_KEY")
	if got != "NVIDIA_API_KEY" {
		t.Errorf("first profile got %q, want NVIDIA_API_KEY", got)
	}
}

// Second profile of the same vendor mints a unique env var derived
// from the new profile name. Without this two NVIDIA NIM profiles
// would silently share one bearer token and fail with 401 when the
// user switched to a model whose key is different.
func TestSuggestAPIKeyEnv_SecondProfileMintsUnique(t *testing.T) {
	cfg := config.Config{
		Providers: []config.Provider{
			{Name: "nvidia-nim", Kind: "openai-compatible", BaseURL: "https://x", APIKeyEnv: "NVIDIA_API_KEY"},
		},
	}
	got := SuggestAPIKeyEnv(cfg, "nvidia-nim-2", "NVIDIA_API_KEY")
	if got != "NVIDIA_NIM_2_API_KEY" {
		t.Errorf("second profile got %q, want NVIDIA_NIM_2_API_KEY", got)
	}
}

// Empty fallback (Ollama, openai-auth) returns empty — those
// providers don't need an env-var slot.
func TestSuggestAPIKeyEnv_EmptyFallbackReturnsEmpty(t *testing.T) {
	cfg := twoProviderConfig()
	if got := SuggestAPIKeyEnv(cfg, "ollama", ""); got != "" {
		t.Errorf("empty fallback got %q, want empty", got)
	}
}

// When the derived name happens to also be taken (rare, but
// pathological configs can do it), we walk numeric suffixes until
// we find a free slot. Unbounded loops are a real risk on corrupt
// configs so the helper has its own bound — verify the search works.
func TestSuggestAPIKeyEnv_DerivedAlsoTakenWalksSuffix(t *testing.T) {
	cfg := config.Config{
		Providers: []config.Provider{
			{Name: "nvidia-nim", APIKeyEnv: "NVIDIA_API_KEY"},
			{Name: "other", APIKeyEnv: "NVIDIA_NIM_2_API_KEY"},
		},
	}
	got := SuggestAPIKeyEnv(cfg, "nvidia-nim-2", "NVIDIA_API_KEY")
	if got != "NVIDIA_NIM_2_API_KEY_2" {
		t.Errorf("got %q, want NVIDIA_NIM_2_API_KEY_2", got)
	}
}

// Hyphenated profile names with internal digits should derive
// cleanly. Edge case: leading digit in the derived name is
// rejected (POSIX env vars beginning with a digit confuse some
// shells) so we fall back to the original env var.
func TestDeriveAPIKeyEnv_LeadingDigitFallsBack(t *testing.T) {
	if got := deriveAPIKeyEnv("2nd-thing"); got != "" {
		t.Errorf("leading digit got %q, want empty", got)
	}
}

func TestRemove_KeepsActiveWhenDifferent(t *testing.T) {
	cfg, err := Remove(twoProviderConfig(), "openai")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if cfg.Active.Provider != "anthropic" {
		t.Errorf("Active should remain anthropic, got %q", cfg.Active.Provider)
	}
}

func TestRemove_UnknownProviderErrors(t *testing.T) {
	_, err := Remove(twoProviderConfig(), "ghost")
	if err == nil {
		t.Fatal("expected error on unknown")
	}
}

func TestAdd_AppendsValidProvider(t *testing.T) {
	cfg, err := Add(twoProviderConfig(), AddProvider{
		Name:         "ollama",
		Kind:         "ollama",
		BaseURL:      "http://localhost:11434/v1",
		DefaultModel: "llama3.1:8b",
		Models:       []config.Model{{Name: "llama3.1:8b", Tier: "cheap"}},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(cfg.Providers) != 3 || cfg.Providers[2].Name != "ollama" {
		t.Errorf("expected 3 providers ending with ollama, got %v", cfg.Providers)
	}
}

func TestAdd_RejectsDuplicateName(t *testing.T) {
	_, err := Add(twoProviderConfig(), AddProvider{
		Name: "anthropic", Kind: "anthropic", BaseURL: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("want already-exists error, got %v", err)
	}
}

func TestAdd_RejectsInvalidKind(t *testing.T) {
	_, err := Add(twoProviderConfig(), AddProvider{
		Name: "weird", Kind: "snake-oil", BaseURL: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "snake-oil") {
		t.Errorf("want invalid-kind error mentioning snake-oil, got %v", err)
	}
}

func TestAdd_RejectsEmptyName(t *testing.T) {
	_, err := Add(twoProviderConfig(), AddProvider{Kind: "anthropic", BaseURL: "x"})
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("want name error, got %v", err)
	}
}

func TestAdd_RejectsEmptyBaseURL(t *testing.T) {
	_, err := Add(twoProviderConfig(), AddProvider{
		Name: "x", Kind: "anthropic",
	})
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("want base_url error, got %v", err)
	}
}

// When both a default_model and a Models[] catalog are supplied,
// the default must be in the catalog. Mirrors config.Validate so
// the user sees the failure at the picker's save step rather than
// after persistence.
func TestAdd_RejectsDefaultModelNotInCatalog(t *testing.T) {
	_, err := Add(twoProviderConfig(), AddProvider{
		Name:         "x",
		Kind:         "anthropic",
		BaseURL:      "https://api.anthropic.com",
		DefaultModel: "claude-undefined",
		Models:       []config.Model{{Name: "claude-other"}},
	})
	if err == nil || !strings.Contains(err.Error(), "default_model") {
		t.Errorf("want default_model error, got %v", err)
	}
}

// Free-form providers (Ollama, NVIDIA NIM, custom proxies) carry
// no catalog at provider-add time; the catalog is filled at runtime
// via the live /models fetch. Defaulting a model that isn't in the
// (empty) catalog is fine.
func TestAdd_AcceptsFreeFormDefaultModelWithEmptyCatalog(t *testing.T) {
	_, err := Add(twoProviderConfig(), AddProvider{
		Name:         "ollama",
		Kind:         "ollama",
		BaseURL:      "http://localhost:11434/v1",
		DefaultModel: "llama3.1:8b",
	})
	if err != nil {
		t.Errorf("free-form add should succeed, got %v", err)
	}
}

func TestSetActiveModel_UpdatesDefaultModel(t *testing.T) {
	cfg, err := SetActiveModel(twoProviderConfig(), "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("SetActiveModel: %v", err)
	}
	if cfg.Active.DefaultModel != "claude-haiku-4-5" || cfg.Active.Model != "claude-haiku-4-5" {
		t.Errorf("default model not updated; Active = %+v", cfg.Active)
	}
}

func TestSetActiveModel_RejectsModelNotInCatalog(t *testing.T) {
	_, err := SetActiveModel(twoProviderConfig(), "claude-undefined")
	if err == nil || !strings.Contains(err.Error(), "claude-undefined") {
		t.Errorf("want catalog-membership error, got %v", err)
	}
}

func TestSetActiveModel_FreeFormProviderSkipsCatalogCheck(t *testing.T) {
	cfg := config.Config{
		Active: config.Active{Provider: "ollama"},
		Providers: []config.Provider{
			{Name: "ollama", Kind: "ollama", BaseURL: "http://localhost:11434/v1"},
		},
	}
	cfg, err := SetActiveModel(cfg, "phi3:mini")
	if err != nil {
		t.Errorf("free-form provider should accept any model, got %v", err)
	}
	if cfg.Active.DefaultModel != "phi3:mini" {
		t.Errorf("DefaultModel = %q, want phi3:mini", cfg.Active.DefaultModel)
	}
}

func TestSetActiveModel_NoActiveProviderErrors(t *testing.T) {
	cfg := config.Config{
		Providers: []config.Provider{{Name: "x", Kind: "anthropic", BaseURL: "y"}},
	}
	_, err := SetActiveModel(cfg, "any")
	if err == nil || !strings.Contains(err.Error(), "no active provider") {
		t.Errorf("want no-active-provider error, got %v", err)
	}
}

func TestSetActiveModel_EmptyModelNameErrors(t *testing.T) {
	_, err := SetActiveModel(twoProviderConfig(), "")
	if err == nil {
		t.Fatal("expected error on empty model name")
	}
}

// Small smoke test: validKind matches every name in config.ValidKinds.
// Catches the surprise where the helper drifts from the source list.
func TestValidKind_CoversConfigList(t *testing.T) {
	for _, k := range config.ValidKinds {
		if !validKind(k) {
			t.Errorf("validKind(%q) = false; should match config.ValidKinds", k)
		}
	}
	if validKind("snake-oil") {
		t.Errorf("validKind should reject unknown")
	}
}

// errors.Is contract — the per-kind errors don't need to wrap a
// sentinel today, but if a future change uses %w it shouldn't break
// callers that match on substring.
var _ error = errors.New("smoke")
