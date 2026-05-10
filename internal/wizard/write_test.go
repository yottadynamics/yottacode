package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestApplyAtomicAndPerms checks the canonical happy path: writes
// config.toml at 0644, .env at 0600, atomic via tmp+rename (no leftover
// .tmp file), and the result parses back via config.Load.
func TestApplyAtomicAndPerms(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")
	plan := newAnthropicPlan()
	out, err := Apply(plan, WriteOptions{
		ConfigPath: cfgPath,
		EnvPath:    envPath,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.WroteConfig || !out.WroteEnv {
		t.Errorf("WroteConfig=%v WroteEnv=%v, want both true", out.WroteConfig, out.WroteEnv)
	}
	if out.WroteBackup {
		t.Errorf("first write should not back up")
	}

	// Permissions.
	if st, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("stat cfg: %v", err)
	} else if perm := st.Mode().Perm(); perm != 0o644 {
		t.Errorf("config perm = %o, want 0644", perm)
	}
	if st, err := os.Stat(envPath); err != nil {
		t.Fatalf("stat env: %v", err)
	} else if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("env perm = %o, want 0600", perm)
	}

	// No stray .tmp files.
	if _, err := os.Stat(cfgPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("config.tmp leaked: err=%v", err)
	}
	if _, err := os.Stat(envPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("env.tmp leaked: err=%v", err)
	}

	// Round-trip: written config must Load + Validate cleanly.
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got.Active.Provider != "anthropic" {
		t.Errorf("Active.Provider = %q, want anthropic", got.Active.Provider)
	}
	if len(got.Providers) != 1 || got.Providers[0].Name != "anthropic" {
		t.Errorf("got Providers = %+v, want one anthropic entry", got.Providers)
	}

	// .env body has the expected key.
	body, _ := os.ReadFile(envPath)
	if !strings.Contains(string(body), "ANTHROPIC_API_KEY=sk-test") {
		t.Errorf(".env body missing key: %s", body)
	}
}

// TestApplyMergePreservesTunables: write once, then write again with
// a different plan, and confirm a hand-edited [retrieval] tunable
// from the first write is preserved (not steamrolled by the merge).
func TestApplyMergePreservesTunables(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")

	// First write: a plan whose to-disk render carries default tunables.
	first := newAnthropicPlan()
	if _, err := Apply(first, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Hand-edit: customize retrieval.top_k to 7 to simulate the user
	// tweaking a tunable.
	body, _ := os.ReadFile(cfgPath)
	patched := strings.Replace(string(body), `[active]`, "[retrieval]\ntop_k = 7\n\n[active]", 1)
	if patched == string(body) {
		t.Fatalf("could not patch test fixture")
	}
	if err := os.WriteFile(cfgPath, []byte(patched), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	gemini := *FindCatalogEntry("gemini")
	pp := PlanProvider{
		Name: gemini.Name, Kind: gemini.Kind, BaseURL: gemini.BaseURL,
		APIKeyEnv: gemini.APIKeyEnv, DefaultModel: "gemini-2.5-flash",
	}
	second := newAnthropicPlan()
	second.Providers = append(second.Providers, pp)

	if _, err := Apply(second, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath}); err != nil {
		t.Fatalf("merge apply: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after merge: %v", err)
	}
	if got.Retrieval.TopK != 7 {
		t.Errorf("merge dropped tunable: retrieval.top_k = %v, want 7", got.Retrieval.TopK)
	}
	names := []string{}
	for _, p := range got.Providers {
		names = append(names, p.Name)
	}
	wantNames := map[string]bool{"anthropic": true, "gemini": true}
	for _, n := range names {
		if !wantNames[n] {
			t.Errorf("merged providers contain unexpected %q", n)
		}
	}
	if len(names) != 2 {
		t.Errorf("merged providers = %v, want 2", names)
	}
}

// TestApplyForceBackup: --force replaces and backs up.
func TestApplyForceBackup(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")
	plan := newAnthropicPlan()
	if _, err := Apply(plan, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	out, err := Apply(plan, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath, Force: true})
	if err != nil {
		t.Fatalf("forced apply: %v", err)
	}
	if !out.WroteBackup || out.BackupPath == "" {
		t.Errorf("Force should produce a backup, got outcome %+v", out)
	}
	if _, err := os.Stat(out.BackupPath); err != nil {
		t.Errorf("backup path %q missing: %v", out.BackupPath, err)
	}
}

// TestApplySkipEnvWrite: keys collected but flag says don't persist.
// The .env should not exist on disk; config.toml still does.
func TestApplySkipEnvWrite(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")
	plan := newAnthropicPlan()
	out, err := Apply(plan, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath, SkipEnvWrite: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out.WroteEnv {
		t.Errorf("SkipEnvWrite=true but WroteEnv=true")
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf(".env should not exist when SkipEnvWrite, err=%v", err)
	}
}

// TestApplyMergeEnvKeepsExisting: pre-existing keys in .env that
// aren't in the plan are preserved.
func TestApplyMergeEnvKeepsExisting(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	envPath := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envPath, []byte("EXISTING_KEY=existing-val\n"), 0o600); err != nil {
		t.Fatalf("seed env: %v", err)
	}
	plan := newAnthropicPlan() // brings ANTHROPIC_API_KEY=sk-test
	if _, err := Apply(plan, WriteOptions{ConfigPath: cfgPath, EnvPath: envPath}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	body, _ := os.ReadFile(envPath)
	if !strings.Contains(string(body), "EXISTING_KEY=existing-val") {
		t.Errorf(".env merge dropped pre-existing key: %s", body)
	}
	if !strings.Contains(string(body), "ANTHROPIC_API_KEY=sk-test") {
		t.Errorf(".env merge dropped new key: %s", body)
	}
}
