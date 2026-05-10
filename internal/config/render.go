package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Render produces the canonical TOML body for a Config. Stable
// section order: tunables block (auto_memory / context / retrieval)
// first via the BurntSushi encoder, then human-edited sections
// (active, providers, router) hand-rendered with explicit alignment
// so diffs read top-to-bottom. The encoder's emit order isn't
// guaranteed across releases, which is why we don't lean on it for
// the human-edited bits.
//
// Used both by wizard.Apply (for fresh writes and merges) and by
// the TUI's /provider add / /model picker save paths so a single
// rendering function owns the file shape. Callers that need atomic
// persistence should pair this with Save.
func Render(cfg Config) string {
	var b strings.Builder
	b.WriteString("# yottacode configuration\n")
	b.WriteString("# API keys live in ~/.yottacode/.env, not here.\n\n")

	if blob, err := encodeTunables(cfg); err == nil && strings.TrimSpace(blob) != "" {
		b.WriteString(blob)
		if !strings.HasSuffix(blob, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if cfg.Active.Provider != "" {
		b.WriteString("[active]\n")
		fmt.Fprintf(&b, "provider      = %q\n", cfg.Active.Provider)
		// Emit the canonical default_model line; legacy `model` is
		// still parsed by Load but writing it back would create
		// stale duplication. Active.normalize() guarantees
		// DefaultModel reflects whichever key the file used.
		if cfg.Active.DefaultModel != "" {
			fmt.Fprintf(&b, "default_model = %q\n", cfg.Active.DefaultModel)
		}
		b.WriteString("\n")
	}

	for _, p := range cfg.Providers {
		b.WriteString("[[providers]]\n")
		fmt.Fprintf(&b, "name          = %q\n", p.Name)
		fmt.Fprintf(&b, "kind          = %q\n", p.Kind)
		fmt.Fprintf(&b, "base_url      = %q\n", p.BaseURL)
		if p.APIKeyEnv != "" {
			fmt.Fprintf(&b, "api_key_env   = %q\n", p.APIKeyEnv)
		}
		if p.DefaultModel != "" {
			fmt.Fprintf(&b, "default_model = %q\n", p.DefaultModel)
		}
		for _, m := range p.Models {
			b.WriteString("\n  [[providers.models]]\n")
			fmt.Fprintf(&b, "  name = %q\n", m.Name)
			if m.Tier != "" {
				fmt.Fprintf(&b, "  tier = %q\n", m.Tier)
			}
			if m.ContextWindow > 0 {
				fmt.Fprintf(&b, "  context_window = %d\n", m.ContextWindow)
			}
		}
		b.WriteString("\n")
	}

	if cfg.Router.Enabled && len(cfg.Router.Candidates) > 0 {
		b.WriteString("[router]\n")
		b.WriteString("enabled    = true\n")
		policy := cfg.Router.Policy
		if policy == "" {
			policy = "fallback-chain"
		}
		fmt.Fprintf(&b, "policy     = %q\n", policy)
		b.WriteString("candidates = [")
		for i, c := range cfg.Router.Candidates {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", c)
		}
		b.WriteString("]\n")
		if cfg.Router.HealthWindowSeconds > 0 {
			fmt.Fprintf(&b, "health_window_seconds    = %d\n", cfg.Router.HealthWindowSeconds)
		}
		if cfg.Router.HealthFailureThreshold > 0 {
			fmt.Fprintf(&b, "health_failure_threshold = %d\n", cfg.Router.HealthFailureThreshold)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Save writes cfg to path atomically (tmp + rename). Creates parent
// dirs as needed. The file mode is 0644 — keys live in .env, never
// here, so 0600 isn't required. Used by every TUI write path
// (/provider add / /provider remove / /model picker confirm).
func Save(cfg Config, path string) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := Render(cfg)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// encodeTunables renders only the [context] and [retrieval] sections
// via the BurntSushi encoder. We marshal a trimmed struct so the
// encoder doesn't try to emit [active], [[providers]], or [router].
func encodeTunables(cfg Config) (string, error) {
	var trimmed = struct {
		Context   ContextConfig   `toml:"context"`
		Retrieval RetrievalConfig `toml:"retrieval"`
	}{
		Context:   cfg.Context,
		Retrieval: cfg.Retrieval,
	}
	var b strings.Builder
	enc := toml.NewEncoder(&b)
	enc.Indent = ""
	if err := enc.Encode(trimmed); err != nil {
		return "", err
	}
	return b.String(), nil
}
