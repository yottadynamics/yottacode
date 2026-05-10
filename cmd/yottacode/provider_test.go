package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// seedTestConfig writes a fixture config.toml under HOME (which is
// already isolated to a tempdir by t.TempDir / t.Setenv).
func seedTestConfig(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

const twoProviderTOML = `
[active]
provider      = "anthropic"
default_model = "claude-sonnet-4-6"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"
  [[providers.models]]
  name = "claude-haiku-4-5"
  tier = "cheap"

[[providers]]
name = "openai"
kind = "openai"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`

// runCobra executes the root with a clean env and captures stdout/stderr.
// Returns the trimmed stdout for assertions and the run error.
func runCobra(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("HOME", os.Getenv("HOME")) // keep current HOME isolation
	root := newCLI()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errBuf.String(), err
}

func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestProviderListPrintsConfiguredProfiles(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	out, _, err := runCobra(t, "provider", "list")
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	for _, want := range []string{"anthropic", "openai", "active: anthropic"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestProviderListJSON(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	out, _, err := runCobra(t, "provider", "list", "--json")
	if err != nil {
		t.Fatalf("provider list --json: %v", err)
	}
	var got struct {
		Active    config.Active     `json:"active"`
		Providers []config.Provider `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out)
	}
	if got.Active.Provider != "anthropic" {
		t.Errorf("Active.Provider = %q, want anthropic", got.Active.Provider)
	}
	if len(got.Providers) != 2 {
		t.Errorf("want 2 providers in JSON, got %d", len(got.Providers))
	}
}

func TestProviderUseSwitchesActive(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	_, _, err := runCobra(t, "provider", "use", "openai")
	if err != nil {
		t.Fatalf("provider use: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if cfg.Active.Provider != "openai" || cfg.Active.DefaultModel != "gpt-4o" {
		t.Errorf("active not switched; got %+v", cfg.Active)
	}
}

func TestProviderUseUnknownErrors(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	_, _, err := runCobra(t, "provider", "use", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("want unknown-provider error, got %v", err)
	}
}

func TestProviderRemoveDeletes(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	_, _, err := runCobra(t, "provider", "remove", "openai")
	if err != nil {
		t.Fatalf("provider remove: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if cfg.FindProvider("openai") != nil {
		t.Errorf("openai should be gone")
	}
	if cfg.FindProvider("anthropic") == nil {
		t.Errorf("anthropic should still be present")
	}
}

func TestProviderRemoveActiveAutoSwitches(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	out, _, err := runCobra(t, "provider", "remove", "anthropic")
	if err != nil {
		t.Fatalf("provider remove: %v", err)
	}
	// Removing the active provider auto-switches to the first
	// remaining (declaration order). Without this, the next
	// `yottacode` invocation errors with "no model set" until the user
	// manually picks a new active.
	if !strings.Contains(out, `removed provider "anthropic"`) {
		t.Errorf("expected removed line; out:\n%s", out)
	}
	if !strings.Contains(out, `switched active provider to "openai"`) {
		t.Errorf("expected auto-switch line; out:\n%s", out)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if cfg.Active.Provider != "openai" {
		t.Errorf("Active.Provider should be openai after auto-switch; got %q", cfg.Active.Provider)
	}
	if cfg.Active.DefaultModel == "" {
		t.Errorf("Active.DefaultModel should be inherited from openai; got empty")
	}
}

func TestProviderRemoveLastProviderEmptyState(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	if _, _, err := runCobra(t, "provider", "remove", "openai"); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	out, _, err := runCobra(t, "provider", "remove", "anthropic")
	if err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if !strings.Contains(out, "no other providers configured") {
		t.Errorf("expected empty-state hint; out:\n%s", out)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("expected zero providers; got %d", len(cfg.Providers))
	}
	if cfg.Active.Provider != "" {
		t.Errorf("Active.Provider should be empty; got %q", cfg.Active.Provider)
	}
}

// `provider remove` should also clean the matching API key out of
// ~/.yottacode/.env. Without this, a user who removes a provider
// leaves an orphaned bearer in their .env file forever — a pure
// hygiene win, also relevant for shared devboxes where removed
// keys shouldn't linger.
func TestProviderRemoveCleansAPIKeyFromEnv(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	envPath := filepath.Join(os.Getenv("HOME"), ".yottacode", ".env")
	if err := os.WriteFile(envPath,
		[]byte(`ANTHROPIC_API_KEY="sk-ant-test"`+"\n"+`OPENAI_API_KEY="sk-openai-test"`+"\n"),
		0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}
	out, _, err := runCobra(t, "provider", "remove", "openai")
	if err != nil {
		t.Fatalf("provider remove: %v", err)
	}
	if !strings.Contains(out, "removed OPENAI_API_KEY") {
		t.Errorf("expected env-cleanup notice in output; got:\n%s", out)
	}
	body, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if strings.Contains(string(body), "OPENAI_API_KEY") {
		t.Errorf("OPENAI_API_KEY should be gone from .env; got:\n%s", body)
	}
	if !strings.Contains(string(body), "ANTHROPIC_API_KEY") {
		t.Errorf("ANTHROPIC_API_KEY (other provider) should be preserved; got:\n%s", body)
	}
}

// Legacy/shared key path: when two profiles point at the same
// api_key_env (configs from before SuggestAPIKeyEnv minted unique
// names), removing one profile must NOT delete the env var the
// surviving profile still depends on. The cleanup must skip and
// surface why so the user knows.
func TestProviderRemoveKeepsSharedAPIKey(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, `
[active]
provider = "nvidia-nim"
default_model = "nvidia/nemotron-3-super-120b-a12b"

[[providers]]
name = "nvidia-nim"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/nemotron-3-super-120b-a12b"

[[providers]]
name = "nvidia-nim-2"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/some-other-model"
`)
	envPath := filepath.Join(os.Getenv("HOME"), ".yottacode", ".env")
	if err := os.WriteFile(envPath, []byte(`NVIDIA_API_KEY="nvapi-shared"`+"\n"), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}
	out, _, err := runCobra(t, "provider", "remove", "nvidia-nim-2")
	if err != nil {
		t.Fatalf("provider remove: %v", err)
	}
	if !strings.Contains(out, "kept NVIDIA_API_KEY") {
		t.Errorf("expected 'kept NVIDIA_API_KEY' notice; out:\n%s", out)
	}
	body, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if !strings.Contains(string(body), "NVIDIA_API_KEY") {
		t.Errorf("NVIDIA_API_KEY should be preserved when shared; got:\n%s", body)
	}
}

// Ollama has empty api_key_env — removing it must not log an env
// cleanup line and must not touch .env (there's nothing to delete,
// and a misleading "removed" log would confuse users).
func TestProviderRemoveOllamaSkipsEnvCleanup(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, `
[active]
provider = "ollama"
default_model = "llama3.1:8b"

[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "llama3.1:8b"
`)
	out, _, err := runCobra(t, "provider", "remove", "ollama")
	if err != nil {
		t.Fatalf("provider remove: %v", err)
	}
	if strings.Contains(out, "removed ") && strings.Contains(out, "from") && strings.Contains(out, ".env") {
		t.Errorf("Ollama remove should not log a .env cleanup; out:\n%s", out)
	}
}

func TestProviderAddAppendsAndPersists(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, _, err := runCobra(t, "provider", "add", "my-router",
		"--kind", "openai-compatible",
		"--base-url", "https://openrouter.ai/api/v1",
		"--key-env", "OPENROUTER_API_KEY",
		"--models", "gpt-4o,claude-sonnet-4-6",
		"--default-model", "gpt-4o",
	)
	if err != nil {
		t.Fatalf("provider add: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	p := cfg.FindProvider("my-router")
	if p == nil {
		t.Fatalf("my-router not found in config")
	}
	if p.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("BaseURL = %q", p.BaseURL)
	}
	if p.DefaultModel != "gpt-4o" {
		t.Errorf("DefaultModel = %q", p.DefaultModel)
	}
	if len(p.Models) != 2 {
		t.Errorf("want 2 models, got %d", len(p.Models))
	}
}

func TestProviderAddMissingKindErrors(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, _, err := runCobra(t, "provider", "add", "x", "--base-url", "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("want --kind error, got %v", err)
	}
}

func TestProviderAddWritesAPIKeyToEnvFile(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, "")
	_, _, err := runCobra(t, "provider", "add", "anthropic-test",
		"--kind", "anthropic",
		"--base-url", "https://api.anthropic.com",
		"--key-env", "ANTHROPIC_API_KEY",
		"--api-key", "sk-ant-test-key",
		"--default-model", "claude-sonnet-4-6",
		"--models", "claude-sonnet-4-6",
	)
	if err != nil {
		t.Fatalf("provider add: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if !strings.Contains(string(body), `ANTHROPIC_API_KEY="sk-ant-test-key"`) {
		t.Errorf(".env should contain the key line; got:\n%s", body)
	}
	stat, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".yottacode", ".env"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := stat.Mode().Perm(); perm != 0o600 {
		t.Errorf(".env perm = %o, want 0600", perm)
	}
}
