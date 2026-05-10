package main

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

func TestModelListPrintsActiveProviderModels(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	out, _, err := runCobra(t, "model", "list")
	if err != nil {
		t.Fatalf("model list: %v", err)
	}
	if !strings.Contains(out, "claude-sonnet-4-6") {
		t.Errorf("expected anthropic models in output; got:\n%s", out)
	}
	if strings.Contains(out, "gpt-4o") {
		t.Errorf("default-list (active provider only) should NOT include gpt-4o; got:\n%s", out)
	}
}

// CLI mirror: `yottacode model list` for a free-form provider
// (NVIDIA NIM with empty Models[]) must surface the configured
// default + API-key status, not just say "free-form" and leave the
// user wondering if their setup is wired up.
func TestModelListFreeFormShowsConfiguredDefaultAndKeyStatus(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, `
[active]
provider      = "nvidia-nim"
default_model = "nvidia/nemotron-3-super-120b-a12b"

[[providers]]
name = "nvidia-nim"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/nemotron-3-super-120b-a12b"
`)
	t.Setenv("NVIDIA_API_KEY", "sk-set")
	out, _, err := runCobra(t, "model", "list")
	if err != nil {
		t.Fatalf("model list: %v", err)
	}
	for _, want := range []string{
		"nvidia-nim",
		"nvidia/nemotron-3-super-120b-a12b",
		"configured default",
		"NVIDIA_API_KEY set",
		"free-form",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("model list missing %q in free-form output:\n%s", want, out)
		}
	}
}

func TestModelListAllShowsEveryProvider(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	out, _, err := runCobra(t, "model", "list", "--all")
	if err != nil {
		t.Fatalf("model list --all: %v", err)
	}
	for _, want := range []string{"claude-sonnet-4-6", "gpt-4o"} {
		if !strings.Contains(out, want) {
			t.Errorf("--all should include %q; got:\n%s", want, out)
		}
	}
}

func TestModelUseWritesDefaultModel(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	_, _, err := runCobra(t, "model", "use", "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("model use: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if cfg.Active.DefaultModel != "claude-haiku-4-5" {
		t.Errorf("DefaultModel = %q, want claude-haiku-4-5", cfg.Active.DefaultModel)
	}
}

func TestModelUseUnknownModelErrors(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	_, _, err := runCobra(t, "model", "use", "claude-undefined")
	if err == nil || !strings.Contains(err.Error(), "claude-undefined") {
		t.Errorf("want unknown-model error, got %v", err)
	}
}

// model fetch hits the live API. Without a real network we just
// confirm the command surfaces a clear error rather than crashing
// when called against an unreachable endpoint.
func TestModelFetchSurfacesNetworkErrorGracefully(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, `
[[providers]]
name = "unreachable"
kind = "openai-compatible"
base_url = "http://127.0.0.1:1/v1"
default_model = "x"
  [[providers.models]]
  name = "x"
`)
	out, errOut, err := runCobra(t, "model", "fetch", "unreachable")
	// We don't expect a hard failure — the command should print
	// a fetch warning and the (catalog fallback) entry list.
	if err != nil {
		t.Fatalf("model fetch should not hard-fail on network error; got %v", err)
	}
	if !strings.Contains(errOut, "fetch warning") {
		t.Errorf("expected fetch warning on stderr; got:\n%s", errOut)
	}
	if !strings.Contains(out, "unreachable") {
		t.Errorf("expected provider name in stdout; got:\n%s", out)
	}
}

func TestModelFetchUnknownProviderErrors(t *testing.T) {
	isolateHome(t)
	seedTestConfig(t, twoProviderTOML)
	_, _, err := runCobra(t, "model", "fetch", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("want unknown-provider error, got %v", err)
	}
}
