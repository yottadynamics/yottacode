package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// rollbackOpenAIAuth must drop the openai-auth provider from the
// just-written config when OAuth fails — without this, a cancelled
// or failed sign-in leaves a broken openai-auth profile on disk that
// the next chat turn 401s against. Mirrors the TUI's persist-on-failure
// behavior; the wizard applies it as post-hoc rollback because Apply
// writes everything in one shot before OAuth runs.
func TestRollbackOpenAIAuth_DropsProvider(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")

	plan := Plan{
		Providers: []PlanProvider{
			{
				Name:         "anthropic",
				Kind:         "anthropic",
				BaseURL:      "https://api.anthropic.com",
				APIKeyEnv:    "ANTHROPIC_API_KEY",
				DefaultModel: "claude-sonnet-4-6",
			},
			{
				Name:         "chatgpt",
				Kind:         "openai-auth",
				BaseURL:      "https://chatgpt.com/backend-api/codex",
				DefaultModel: "gpt-5.5",
			},
		},
		ActiveProvider: "anthropic",
		ActiveModel:    "claude-sonnet-4-6",
		EnvKeys:        map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
	}
	out, err := Apply(plan, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Sanity: openai-auth IS in the just-written config.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load before rollback: %v", err)
	}
	if cfg.FindProvider("chatgpt") == nil {
		t.Fatalf("seed: chatgpt should be in config before rollback")
	}

	// Construct a wizardModel that points at the just-written config
	// and call rollback directly. We don't drive the full Bubbletea
	// program here — the rollback helper is the unit under test.
	m := &wizardModel{outcome: out}
	m.rollbackOpenAIAuth()

	cfg, err = config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after rollback: %v", err)
	}
	if cfg.FindProvider("chatgpt") != nil {
		t.Errorf("chatgpt provider should be gone after rollback; cfg = %+v", cfg.Providers)
	}
	// Anthropic — the working profile — must survive. Without this
	// guard, a wizard that picks anthropic+openai-auth and has the
	// OAuth flow fail would lose the anthropic config too, which
	// would be more destructive than the bug we're fixing.
	if cfg.FindProvider("anthropic") == nil {
		t.Errorf("anthropic should be preserved; cfg = %+v", cfg.Providers)
	}
}

// When [active] pointed at the openai-auth profile (user picked it
// as default), rollback must clear active. providerops.Remove already
// does this — verify the wizard rollback inherits that behavior so
// the user lands on a config they can fix from the picker rather
// than one that fails Validate at next launch.
func TestRollbackOpenAIAuth_ClearsActiveWhenOpenAIAuthWasDefault(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")

	plan := Plan{
		Providers: []PlanProvider{
			{
				Name:         "chatgpt",
				Kind:         "openai-auth",
				BaseURL:      "https://chatgpt.com/backend-api/codex",
				DefaultModel: "gpt-5.5",
			},
		},
		ActiveProvider: "chatgpt",
		ActiveModel:    "gpt-5.5",
	}
	out, err := Apply(plan, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	m := &wizardModel{outcome: out}
	m.rollbackOpenAIAuth()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after rollback: %v", err)
	}
	if cfg.Active.Provider != "" {
		t.Errorf("Active.Provider should be cleared after rollback removed openai-auth; got %q",
			cfg.Active.Provider)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("expected zero providers after rollback; got %+v", cfg.Providers)
	}
}

// When openai-auth wasn't picked at all (or it was already removed),
// rollback must be a no-op — don't rewrite the file unnecessarily,
// don't drop unrelated providers. Without this guard, a wizard run
// that took the OAuth-failure code path even when openai-auth wasn't
// in the plan would silently rewrite config.toml.
func TestRollbackOpenAIAuth_NoOpWhenNothingToDrop(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")

	out, err := Apply(newAnthropicPlan(), WriteOptions{ConfigPath: cfgPath, EnvPath: envPath})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Capture the on-disk body before rollback so we can assert it
	// didn't change.
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	m := &wizardModel{outcome: out}
	m.rollbackOpenAIAuth()

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !strings.EqualFold(string(before), string(after)) {
		t.Errorf("rollback should be a no-op when nothing to drop;\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
}

// rollback is silent when the outcome says nothing was written
// (Apply errored before the write, or the wizard never reached
// stepWriting). Without this guard, the OAuth failure handler would
// blow up on the load attempt for a missing file.
func TestRollbackOpenAIAuth_NoOpWhenWriteNeverHappened(t *testing.T) {
	m := &wizardModel{outcome: WriteOutcome{}} // WroteConfig=false
	m.rollbackOpenAIAuth()                      // must not panic
}
