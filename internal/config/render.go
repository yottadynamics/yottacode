package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Render produces the canonical TOML body for a Config. Stable
// section order: tunables block (context / retrieval)
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

	// [theme] is only rendered when the user has picked something
	// other than the default — keeps the file minimal for users who
	// never touched /themes. Load() backfills DefaultName when the
	// section is absent, so omitting it here is lossless.
	if name := strings.TrimSpace(cfg.Theme.Name); name != "" && name != "terminal" {
		b.WriteString("[theme]\n")
		fmt.Fprintf(&b, "name = %q\n\n", name)
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

	// The [router] section carries two independent features: cache-safe
	// task routing (mode + advisor/implementer models) and the multi-provider
	// fallback router (enabled + candidates). Either, both, or neither may
	// be configured; render a single section when at least one is set.
	hasTaskRouting := hasModelSlot(cfg.Router.AdvisorModel, cfg.Router.AdvisorModels) ||
		hasModelSlot(cfg.Router.ImplementerModel, cfg.Router.ImplementerModels) ||
		hasModelSlot(cfg.Router.SmartModel, cfg.Router.SmartModels) ||
		hasModelSlot(cfg.Router.FastModel, cfg.Router.FastModels) ||
		(cfg.Router.Mode != "" && cfg.Router.Mode != RouterModeOff)
	hasFallback := cfg.Router.Enabled && len(cfg.Router.Candidates) > 0
	if hasTaskRouting || hasFallback {
		b.WriteString("[router]\n")
	}
	if hasTaskRouting {
		// %-19s aligns every key's `=` (implementer_models is longest).
		if cfg.Router.Mode != "" {
			fmt.Fprintf(&b, "%-19s= %q\n", "mode", cfg.Router.Mode)
		}
		if cfg.Router.AdvisorModel != "" {
			fmt.Fprintf(&b, "%-19s= %q\n", "advisor_model", cfg.Router.AdvisorModel)
		}
		if cfg.Router.ImplementerModel != "" {
			fmt.Fprintf(&b, "%-19s= %q\n", "implementer_model", cfg.Router.ImplementerModel)
		}
		if len(cfg.Router.AdvisorModels) > 0 {
			writeRouterModelList(&b, "advisor_models", cfg.Router.AdvisorModels)
		}
		if len(cfg.Router.ImplementerModels) > 0 {
			writeRouterModelList(&b, "implementer_models", cfg.Router.ImplementerModels)
		}
		// Preserve legacy aliases when a config has not yet been edited onto
		// the canonical role fields.
		if cfg.Router.FastModel != "" {
			fmt.Fprintf(&b, "%-19s= %q\n", "fast_model", cfg.Router.FastModel)
		}
		if cfg.Router.SmartModel != "" {
			fmt.Fprintf(&b, "%-19s= %q\n", "smart_model", cfg.Router.SmartModel)
		}
		if len(cfg.Router.FastModels) > 0 {
			writeRouterModelList(&b, "fast_models", cfg.Router.FastModels)
		}
		if len(cfg.Router.SmartModels) > 0 {
			writeRouterModelList(&b, "smart_models", cfg.Router.SmartModels)
		}
	}
	if hasFallback {
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
	} else if hasTaskRouting && cfg.Router.Policy != "" {
		// A chain-only config can still carry a policy key (the failover
		// docs name it right next to smart_models). It must survive a
		// rewrite: every /router picker commit goes through here, and
		// silently stripping user-set keys is the config-clobber class
		// this project has been bitten by before.
		fmt.Fprintf(&b, "%-13s= %q\n", "policy", cfg.Router.Policy)
	}
	// Health knobs apply to ANY failover surface — the candidates router
	// and fast/smart slot chains both feed healthOptionsFromConfig — so
	// they render whenever set, not only when the candidates router is
	// enabled (which used to drop them from chain-only configs on every
	// picker write).
	if hasTaskRouting || hasFallback {
		if cfg.Router.HealthWindowSeconds > 0 {
			fmt.Fprintf(&b, "health_window_seconds    = %d\n", cfg.Router.HealthWindowSeconds)
		}
		if cfg.Router.HealthFailureThreshold > 0 {
			fmt.Fprintf(&b, "health_failure_threshold = %d\n", cfg.Router.HealthFailureThreshold)
		}
		b.WriteString("\n")
	}

	// [skills] is only rendered when default_on is non-empty so the
	// TOML stays minimal for users who haven't pinned any persistent
	// preferences. Load backfills an empty DefaultOn when the section
	// is absent, so omitting it here is lossless.
	if len(cfg.Skills.DefaultOn) > 0 {
		b.WriteString("[skills]\n")
		b.WriteString("default_on = [")
		for i, name := range cfg.Skills.DefaultOn {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", name)
		}
		b.WriteString("]\n\n")
	}

	for _, s := range cfg.MCPServers {
		b.WriteString("[[mcp_servers]]\n")
		fmt.Fprintf(&b, "name    = %q\n", s.Name)
		fmt.Fprintf(&b, "command = %q\n", s.Command)
		if len(s.Args) > 0 {
			b.WriteString("args    = [")
			for i, a := range s.Args {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%q", a)
			}
			b.WriteString("]\n")
		}
		if len(s.Env) > 0 {
			b.WriteString("env     = { ")
			first := true
			for k, v := range s.Env {
				if !first {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s = %q", k, v)
				first = false
			}
			b.WriteString(" }\n")
		}
		if s.Disabled {
			b.WriteString("disabled = true\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}

// writeRouterModelList renders a [router] string-list assignment
// (fast_models / smart_models) aligned with the other task-routing keys.
func writeRouterModelList(b *strings.Builder, key string, items []string) {
	fmt.Fprintf(b, "%-19s= [", key)
	for i, m := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", m)
	}
	b.WriteString("]\n")
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

// encodeTunables renders only the [context], [retrieval], [memory], [lsp],
// [sandbox], and [experimental] sections via the BurntSushi encoder. We
// marshal a trimmed struct so the encoder doesn't try to emit [active],
// [[providers]], or [router]. Memory/LSP/sandbox/experimental must be
// included: Render rebuilds the file from the struct, so any section left
// out of this list is silently DROPPED from disk the next time a picker or
// wizard saves the config.
func encodeTunables(cfg Config) (string, error) {
	var trimmed = struct {
		Context      ContextConfig   `toml:"context"`
		Retrieval    RetrievalConfig `toml:"retrieval"`
		Cache        CacheConfig     `toml:"cache"`
		Memory       MemoryConfig    `toml:"memory"`
		LSP          LSPConfig       `toml:"lsp"`
		Sandbox      SandboxConfig   `toml:"sandbox"`
		Experimental map[string]bool `toml:"experimental"`
	}{
		Context:      cfg.Context,
		Retrieval:    cfg.Retrieval,
		Cache:        cfg.Cache,
		Memory:       cfg.Memory,
		LSP:          cfg.LSP,
		Sandbox:      cfg.Sandbox,
		Experimental: cfg.Experimental,
	}
	var b strings.Builder
	enc := toml.NewEncoder(&b)
	enc.Indent = ""
	if err := enc.Encode(trimmed); err != nil {
		return "", err
	}
	return b.String(), nil
}
