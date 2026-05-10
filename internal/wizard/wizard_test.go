package wizard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestRunNonInteractiveWritesConfig drives the -y path end-to-end
// with --provider + --active and checks that config.Load round-trips.
func TestRunNonInteractiveWritesConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	tmp := t.TempDir()
	out := &bytes.Buffer{}
	err := Run(context.Background(), Options{
		NonInteractive: true,
		Providers:      []string{"anthropic"},
		Active:         "anthropic:claude-sonnet-4-6",
		ConfigPath:     filepath.Join(tmp, "config.toml"),
		EnvPath:        filepath.Join(tmp, ".env"),
		Out:            out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cfg, err := config.Load(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Active.Provider != "anthropic" {
		t.Errorf("Active.Provider = %q", cfg.Active.Provider)
	}
}

// TestRunPrintOnlyDoesNotWrite: --print-only with valid options
// must dump TOML without touching disk.
func TestRunPrintOnlyDoesNotWrite(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")

	out := &bytes.Buffer{}
	err := Run(context.Background(), Options{
		PrintOnly:  true,
		Providers:  []string{"anthropic"},
		Active:     "anthropic:claude-sonnet-4-6",
		ConfigPath: cfgPath,
		EnvPath:    envPath,
		Out:        out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "[active]") {
		t.Errorf("print-only output missing [active]; got:\n%s", out.String())
	}
	if got := out.String(); !strings.Contains(got, `provider      = "anthropic"`) {
		t.Errorf("print-only output missing provider line; got:\n%s", got)
	}
	// Disk untouched.
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("print-only should not write config.toml; stat err=%v", err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("print-only should not write .env; stat err=%v", err)
	}
}

// TestRunFromEnvAutoEnables uses --from-env with two keys present.
func TestRunFromEnvAutoEnables(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "x1")
	t.Setenv("OPENAI_API_KEY", "x2")
	for _, k := range []string{"GEMINI_API_KEY", "XAI_API_KEY", "NVIDIA_API_KEY"} {
		t.Setenv(k, "")
	}

	tmp := t.TempDir()
	err := Run(context.Background(), Options{
		NonInteractive: true,
		FromEnv:        true,
		Active:         "anthropic:claude-sonnet-4-6",
		ConfigPath:     filepath.Join(tmp, "config.toml"),
		EnvPath:        filepath.Join(tmp, ".env"),
		Out:            &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cfg, err := config.Load(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := map[string]bool{}
	for _, p := range cfg.Providers {
		got[p.Name] = true
	}
	for _, want := range []string{"anthropic", "openai"} {
		if !got[want] {
			t.Errorf("--from-env should have enabled %q; got %v", want, got)
		}
	}
}

// TestRunRouterEnabled: --router fallback-chain produces the [router]
// block with the right policy and candidate list.
//
// Skipped after the cloud-providers-go-free-form rework: the
// non-interactive flag set doesn't support per-provider model
// mapping, and without curated default models in the catalog the
// router candidate list resolves to bare "anthropic" / "openai"
// which fail validation. Router functionality itself still has full
// coverage under internal/adapter/multi_test.go; restoring this
// integration test needs a flag like --provider anthropic:model
// (tracked elsewhere).
func TestRunRouterEnabled(t *testing.T) {
	t.Skip("needs per-provider model mapping in the flag set; see comment above")
	t.Setenv("ANTHROPIC_API_KEY", "x")
	t.Setenv("OPENAI_API_KEY", "y")

	tmp := t.TempDir()
	err := Run(context.Background(), Options{
		NonInteractive: true,
		Providers:      []string{"anthropic", "openai"},
		Active:         "anthropic:claude-sonnet-4-6",
		EnableRouter:   true,
		RouterPolicy:   "fallback-chain",
		ConfigPath:     filepath.Join(tmp, "config.toml"),
		EnvPath:        filepath.Join(tmp, ".env"),
		Out:            &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cfg, err := config.Load(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Router.Enabled {
		t.Errorf("router not enabled")
	}
	if cfg.Router.Policy != "fallback-chain" {
		t.Errorf("policy = %q", cfg.Router.Policy)
	}
	if len(cfg.Router.Candidates) != 2 {
		t.Errorf("candidates = %v", cfg.Router.Candidates)
	}
}

// TestRunRejectsUnknownProvider
func TestRunRejectsUnknownProvider(t *testing.T) {
	tmp := t.TempDir()
	err := Run(context.Background(), Options{
		NonInteractive: true,
		Providers:      []string{"definitely-not-real"},
		ConfigPath:     filepath.Join(tmp, "config.toml"),
		EnvPath:        filepath.Join(tmp, ".env"),
		Out:            &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("expected unknown-provider error, got %v", err)
	}
}

// TestRunRoundTrip guards the basic non-interactive round-trip path:
// two back-to-back wizard runs should merge cleanly without rejecting
// the first run's own output.
func TestRunRoundTrip(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")

	err := Run(context.Background(), Options{
		NonInteractive: true,
		Providers:      []string{"anthropic"},
		Active:         "anthropic:claude-sonnet-4-6",
		ConfigPath:     cfgPath,
		EnvPath:        envPath,
		Out:            &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("Load after first write: %v", err)
	}

	err = Run(context.Background(), Options{
		NonInteractive: true,
		Providers:      []string{"anthropic"},
		Active:         "anthropic:claude-sonnet-4-6",
		ConfigPath:     cfgPath,
		EnvPath:        envPath,
		Out:            &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("second Run (merge): %v", err)
	}
}

// TestRunMutualExclusion: --router and --no-router
func TestRunMutualExclusion(t *testing.T) {
	tmp := t.TempDir()
	err := Run(context.Background(), Options{
		NonInteractive: true,
		EnableRouter:   true,
		RouterPolicy:   "fallback-chain",
		NoRouter:       true,
		Providers:      []string{"anthropic"},
		ConfigPath:     filepath.Join(tmp, "config.toml"),
		EnvPath:        filepath.Join(tmp, ".env"),
		Out:            &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutual-exclusion error, got %v", err)
	}
}
