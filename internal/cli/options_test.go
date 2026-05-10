package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatedHome points HOME at a fresh tempdir so the test never sees
// the test runner's real ~/.yottacode/config.toml or .env files.
// Otherwise an `[active]` block on the developer's machine would
// resolve profile defaults and silently break tests that assert on
// "no model configured" errors.
func isolatedHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestResolve_FlagsWinOverEnv(t *testing.T) {
	isolatedHome(t)
	t.Setenv(EnvModel, "env-model")
	t.Setenv(EnvBaseURL, "http://env/v1")
	t.Setenv(EnvAPIKey, "env-key")

	opts := ChatOptions{
		Model:   "flag-model",
		BaseURL: "http://flag/v1",
		APIKey:  "flag-key",
	}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Model != "flag-model" {
		t.Errorf("Model = %q, want flag-model", opts.Model)
	}
	if opts.BaseURL != "http://flag/v1" {
		t.Errorf("BaseURL = %q, want flag value", opts.BaseURL)
	}
	if opts.APIKey != "flag-key" {
		t.Errorf("APIKey = %q, want flag value", opts.APIKey)
	}
}

func TestResolve_FillsFromEnvWhenFlagsEmpty(t *testing.T) {
	isolatedHome(t)
	t.Setenv(EnvModel, "qwen3.5:latest")
	t.Setenv(EnvBaseURL, "http://localhost:11434/v1")
	t.Setenv(EnvAPIKey, "sk-test")

	opts := ChatOptions{}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Model != "qwen3.5:latest" {
		t.Errorf("Model from env = %q", opts.Model)
	}
	if opts.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL from env = %q", opts.BaseURL)
	}
	if opts.APIKey != "sk-test" {
		t.Errorf("APIKey from env = %q", opts.APIKey)
	}
}

func TestResolve_ErrorsWhenModelMissing(t *testing.T) {
	isolatedHome(t)
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "http://x/v1")

	opts := ChatOptions{}
	err := Resolve(&opts)
	if err == nil {
		t.Fatalf("expected error when model missing")
	}
	if !strings.Contains(err.Error(), EnvModel) {
		t.Errorf("error should mention %s; got %q", EnvModel, err.Error())
	}
}

func TestResolve_ErrorsWhenBaseURLMissing(t *testing.T) {
	isolatedHome(t)
	t.Setenv(EnvModel, "m")
	t.Setenv(EnvBaseURL, "")

	opts := ChatOptions{}
	err := Resolve(&opts)
	if err == nil {
		t.Fatalf("expected error when base URL missing")
	}
	if !strings.Contains(err.Error(), EnvBaseURL) {
		t.Errorf("error should mention %s; got %q", EnvBaseURL, err.Error())
	}
}

func TestResolve_APIKeyOptional(t *testing.T) {
	isolatedHome(t)
	t.Setenv(EnvModel, "m")
	t.Setenv(EnvBaseURL, "http://x/v1")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{}
	if err := Resolve(&opts); err != nil {
		t.Errorf("missing API key should not be fatal: %v", err)
	}
	if opts.APIKey != "" {
		t.Errorf("APIKey should stay empty; got %q", opts.APIKey)
	}
}

func TestResolve_AllowPathsFromEnv(t *testing.T) {
	isolatedHome(t)
	t.Setenv(EnvAllowPaths, "/from/env")
	opts := ChatOptions{Model: "x", BaseURL: "http://x/v1"}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.AllowPaths != "/from/env" {
		t.Errorf("env should populate AllowPaths, got %q", opts.AllowPaths)
	}
}

func TestResolve_ProviderFlagsFromEnv(t *testing.T) {
	isolatedHome(t)
	t.Setenv(EnvProvider, "openai")
	t.Setenv(EnvReasoningEffort, "high")
	t.Setenv(EnvEnableWebSearch, "1")
	t.Setenv(EnvEnableXSearch, "true")
	t.Setenv(EnvEnableCodeInterpreter, "true")
	t.Setenv(EnvSearchAllowedDomains, "example.com,docs.example.com")
	t.Setenv(EnvXSearchAllowedHandles, "xai,openai")
	t.Setenv(EnvXSearchFromDate, "2025-10-01")
	t.Setenv(EnvXSearchToDate, "2025-10-10")

	opts := ChatOptions{Model: "x", BaseURL: "http://x/v1"}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", opts.Provider)
	}
	if opts.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want high", opts.ReasoningEffort)
	}
	if !opts.EnableWebSearch {
		t.Errorf("EnableWebSearch = false, want true")
	}
	if !opts.EnableXSearch {
		t.Errorf("EnableXSearch = false, want true")
	}
	if !opts.EnableCodeInterpreter {
		t.Errorf("EnableCodeInterpreter = false, want true")
	}
	if opts.SearchAllowedDomains != "example.com,docs.example.com" {
		t.Errorf("SearchAllowedDomains = %q", opts.SearchAllowedDomains)
	}
	if opts.XSearchAllowedHandles != "xai,openai" {
		t.Errorf("XSearchAllowedHandles = %q", opts.XSearchAllowedHandles)
	}
	if opts.XSearchFromDate != "2025-10-01" || opts.XSearchToDate != "2025-10-10" {
		t.Errorf("x_search date range = %q..%q", opts.XSearchFromDate, opts.XSearchToDate)
	}
}

func TestResolve_RejectsInvalidReasoningEffort(t *testing.T) {
	opts := ChatOptions{
		Model:           "x",
		BaseURL:         "http://x/v1",
		ReasoningEffort: "turbo",
	}
	if err := Resolve(&opts); err == nil {
		t.Fatalf("expected invalid reasoning effort error")
	}
}

func TestResolve_RejectsConflictingSearchFilters(t *testing.T) {
	opts := ChatOptions{
		Model:                 "x",
		BaseURL:               "http://x/v1",
		SearchAllowedDomains:  "example.com",
		SearchExcludedDomains: "example.org",
	}
	if err := Resolve(&opts); err == nil {
		t.Fatalf("expected conflicting search filter error")
	}
}

func TestResolve_RejectsConflictingXSearchHandles(t *testing.T) {
	opts := ChatOptions{
		Model:                  "x",
		BaseURL:                "http://x/v1",
		XSearchAllowedHandles:  "xai",
		XSearchExcludedHandles: "openai",
	}
	if err := Resolve(&opts); err == nil {
		t.Fatalf("expected conflicting x_search handles error")
	}
}

func TestResolve_RejectsConflictingWebSearchEnableDisable(t *testing.T) {
	opts := ChatOptions{
		Model:            "x",
		BaseURL:          "http://x/v1",
		EnableWebSearch:  true,
		DisableWebSearch: true,
	}
	if err := Resolve(&opts); err == nil {
		t.Fatalf("expected conflicting web search toggle error")
	}
}

// TestResolve_AppliesProviderProfile verifies that a configured
// [[providers]] entry fills empty BaseURL / Model / APIKey when
// --provider names it. Active.Model takes precedence over
// DefaultModel when active.provider matches the profile.
func TestResolve_AppliesProviderProfile(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[active]
provider = "anthropic"
model    = "claude-sonnet-4-6"

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
api_key_env   = "ANTHROPIC_API_KEY"
default_model = "claude-haiku-4-5"

  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

  [[providers.models]]
  name = "claude-haiku-4-5"
  tier = "cheap"
`)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{Provider: "anthropic"}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.BaseURL != "https://api.anthropic.com" {
		t.Errorf("BaseURL = %q", opts.BaseURL)
	}
	if opts.Model != "claude-sonnet-4-6" {
		t.Errorf("Model should come from active.model, got %q", opts.Model)
	}
	if opts.APIKey != "sk-ant-test" {
		t.Errorf("APIKey should be picked from ANTHROPIC_API_KEY env, got %q", opts.APIKey)
	}
}

// TestResolve_FallsBackToProviderDefaultModel verifies that when
// --provider matches a profile but [active] is unset (or names a
// different provider), the profile's default_model fills opts.Model.
func TestResolve_FallsBackToProviderDefaultModel(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[[providers]]
name          = "openai"
kind          = "openai"
base_url      = "https://api.openai.com/v1"
api_key_env   = "OPENAI_API_KEY"
default_model = "gpt-4o"

  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`)
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{Provider: "openai"}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Model != "gpt-4o" {
		t.Errorf("Model should come from default_model, got %q", opts.Model)
	}
	if opts.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q", opts.BaseURL)
	}
}

// TestResolve_ExplicitFlagsBeatProfile confirms the precedence
// guarantee: a flag value already on opts is never overwritten by a
// profile match.
func TestResolve_ExplicitFlagsBeatProfile(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[[providers]]
name = "openai"
kind = "openai"
base_url = "https://from-profile.example/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-4o"

  [[providers.models]]
  name = "gpt-4o"
`)
	t.Setenv("OPENAI_API_KEY", "from-env")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{
		Provider: "openai",
		Model:    "from-flag",
		BaseURL:  "https://from-flag.example/v1",
		APIKey:   "from-flag",
	}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Model != "from-flag" || opts.BaseURL != "https://from-flag.example/v1" || opts.APIKey != "from-flag" {
		t.Errorf("explicit flags should win, got Model=%q BaseURL=%q APIKey=%q",
			opts.Model, opts.BaseURL, opts.APIKey)
	}
}

// TestResolve_DotEnvLoadsAPIKey verifies that an api key in
// ~/.yottacode/.env is visible via os.Getenv after Resolve, so the
// profile lookup picks it up. We don't pre-set ANTHROPIC_API_KEY here.
func TestResolve_DotEnvLoadsAPIKey(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", ".env"),
		"ANTHROPIC_API_KEY=sk-from-dotenv\n")
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[[providers]]
name        = "anthropic"
kind        = "anthropic"
base_url    = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`)
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")
	// Explicitly clear so the dotenv has somewhere to land.
	if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("unset: %v", err)
	}

	opts := ChatOptions{Provider: "anthropic"}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.APIKey != "sk-from-dotenv" {
		t.Errorf("APIKey from .env = %q", opts.APIKey)
	}
}

// TestResolve_OSEnvBeatsDotEnv verifies that a pre-set OS env var is
// not clobbered by a value in .env (CI secrets must win over a
// committed-by-accident file).
func TestResolve_OSEnvBeatsDotEnv(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", ".env"),
		"ANTHROPIC_API_KEY=from-file\n")
	t.Setenv("ANTHROPIC_API_KEY", "from-os-env")
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`)
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")

	opts := ChatOptions{Provider: "anthropic"}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.APIKey != "from-os-env" {
		t.Errorf("OS env should win over .env, got %q", opts.APIKey)
	}
}

// TestResolve_FallsBackToFirstProviderWhenNoActive locks in the
// recovery path for configs that have providers but no [active]
// block — e.g. a user who ran `/provider add foo …` from an
// older yottacode build that didn't auto-set active, or a
// hand-edited config with the [active] section deleted. Without
// this fallback the user sees "no model set: …run `yottacode
// setup`" while staring at a perfectly good [[providers]] entry.
func TestResolve_FallsBackToFirstProviderWhenNoActive(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
api_key_env   = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"
`)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fallback")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{} // no --provider, no flags
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve should succeed via first-provider fallback; got: %v", err)
	}
	if opts.Model != "claude-sonnet-4-6" {
		t.Errorf("Model should come from first provider's default_model; got %q", opts.Model)
	}
	if opts.BaseURL != "https://api.anthropic.com" {
		t.Errorf("BaseURL should come from first provider; got %q", opts.BaseURL)
	}
	if opts.APIKey != "sk-ant-fallback" {
		t.Errorf("APIKey should come from first provider's api_key_env; got %q", opts.APIKey)
	}
}

// When [active] names a provider, the fallback must NOT kick in —
// the active provider is the source of truth, even when other
// providers are configured before it.
func TestResolve_FallbackYieldsToExplicitActive(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[active]
provider = "openai"
model    = "gpt-4o"

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
api_key_env   = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"

[[providers]]
name          = "openai"
kind          = "openai"
base_url      = "https://api.openai.com/v1"
api_key_env   = "OPENAI_API_KEY"
default_model = "gpt-4o"
`)
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Model != "gpt-4o" {
		t.Errorf("Model should come from active.model (openai), not first provider; got %q", opts.Model)
	}
	if opts.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL should come from active provider; got %q", opts.BaseURL)
	}
	// opts.Provider is what the status bar / diagnostics surface as
	// "which profile is active." Must reflect the resolved profile
	// even when [active] (not --provider / env) was the source.
	if opts.Provider != "openai" {
		t.Errorf("Provider should reflect active profile name; got %q", opts.Provider)
	}
}

// Empty providers list + no env + no flags → still errors as before.
// The fallback only kicks in when there's at least one configured
// profile to fall back TO.
func TestResolve_NoFallbackWhenProvidersEmpty(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `# empty config`)
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{}
	if err := Resolve(&opts); err == nil {
		t.Errorf("Resolve should error when no providers and no env vars; got nil")
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// openai-auth is a real Provider.Kind that must round-trip through
// config.Validate + Resolve cleanly. Worth locking explicitly because
// the kind has no api_key_env and no live api.openai.com URL — paths
// that previously assumed "remote provider implies API key" used to
// reject it.
func TestResolve_OpenAIAuthKindIsValid(t *testing.T) {
	home := isolatedHome(t)
	mustWriteFile(t, filepath.Join(home, ".yottacode", "config.toml"), `
[active]
provider      = "openai-auth"
default_model = "gpt-5.5"

[[providers]]
name          = "openai-auth"
kind          = "openai-auth"
base_url      = "https://chatgpt.com/backend-api/codex"
default_model = "gpt-5.5"
`)
	t.Setenv(EnvModel, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAPIKey, "")

	opts := ChatOptions{}
	if err := Resolve(&opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Provider != "openai-auth" {
		t.Errorf("Provider = %q, want openai-auth", opts.Provider)
	}
	if opts.Model != "gpt-5.5" {
		t.Errorf("Model = %q, want gpt-5.5", opts.Model)
	}
	if opts.BaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("BaseURL = %q", opts.BaseURL)
	}
	if opts.APIKey != "" {
		t.Errorf("APIKey should stay empty; got %q", opts.APIKey)
	}
}
