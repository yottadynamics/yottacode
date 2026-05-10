package wizard

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/yottadynamics/yottacode/internal/config"
)

// Plan is the wizard's output: every decision, ready to be written.
// The wizard's interactive Bubbletea program builds this struct over
// its steps; the non-interactive path (`yottacode setup -y …`) builds
// the same struct directly from CLI flags + env. Either way, the
// downstream writers consume Plan exclusively — the steps are not
// privileged.
type Plan struct {
	// Providers is the ordered list of enabled providers. Order
	// matters: it's the candidate order for fallback-chain routing
	// and the display order in /provider list.
	Providers []PlanProvider

	// Active picks the session-default provider:model. ActiveProvider
	// must match one of Providers[].Name; ActiveModel must be present
	// in that provider's Models.
	ActiveProvider string
	ActiveModel    string

	// EnableRouter and Router* control the [router] config block.
	EnableRouter   bool
	RouterPolicy   string   // "fallback-chain" | "cheap-first"
	RouterCandList []string // each "<provider>" or "<provider>:<model>"

	// EnvKeys maps env-var name → secret value the user entered
	// during the wizard. Already-set environment values are NOT in
	// this map: we never copy a live key from os.Environ to .env.
	// Keys here are written to ~/.yottacode/.env (chmod 0600).
	EnvKeys map[string]string

	// SkipEnvWrite, when true, suppresses the .env write entirely.
	// The wizard still records api_key_env names on each provider so
	// the runtime knows where to look — values are expected to come
	// from the live shell or a secrets manager.
	SkipEnvWrite bool
}

// PlanProvider mirrors a config.Provider but holds only the fields the
// wizard actually sets. The Models field (curated per-provider model
// list) was removed when internal/catalog/catalog.gen.json became the
// source of truth for the cloud-provider model list — the picker reads
// the catalog at /model time, the wizard only authors provider
// directory metadata + default_model.
type PlanProvider struct {
	Name         string
	Kind         string
	BaseURL      string
	APIKeyEnv    string
	DefaultModel string

	// EnvAlreadySet is true when the wizard saw the API key already
	// present in os.Environ. Purely informational — it's how the
	// review screen says "key present in env" instead of "saved to
	// .env."
	EnvAlreadySet bool
}

// ToConfig converts the Plan into a config.Config with [[providers]],
// [active], and (optionally) [router] populated. Other sections —
// context, retrieval — are left at their zero values; the caller
// merges this into an existing config so those fields preserve what
// the user already had on disk.
func (p Plan) ToConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = make([]config.Provider, 0, len(p.Providers))
	for _, pp := range p.Providers {
		cp := config.Provider{
			Name:         pp.Name,
			Kind:         pp.Kind,
			BaseURL:      pp.BaseURL,
			APIKeyEnv:    pp.APIKeyEnv,
			DefaultModel: pp.DefaultModel,
		}
		// pp.Models intentionally not copied. The wizard stopped
		// curating per-provider model lists when the embedded catalog
		// (internal/catalog/catalog.gen.json) became the source of
		// truth — config.Provider.Models is preserved as a TOML field
		// for backward-compat with hand-curated configs but the
		// wizard itself never writes it.
		cfg.Providers = append(cfg.Providers, cp)
	}
	cfg.Active = config.Active{
		Provider:     p.ActiveProvider,
		Model:        p.ActiveModel,
		DefaultModel: p.ActiveModel,
	}
	if p.EnableRouter && len(p.RouterCandList) > 0 {
		cfg.Router = config.RouterConfig{
			Enabled:    true,
			Policy:     p.RouterPolicy,
			Candidates: append([]string(nil), p.RouterCandList...),
		}
	}
	return cfg
}

// Validate runs the same rules config.Validate runs, plus a couple
// wizard-specific checks (active in the provider list, env keys
// referenced from a provider). Returns the first failure as a clean
// error suitable for stderr.
func (p Plan) Validate() error {
	if len(p.Providers) == 0 {
		return fmt.Errorf("at least one provider must be enabled")
	}
	seen := make(map[string]struct{}, len(p.Providers))
	for _, pp := range p.Providers {
		if _, dupe := seen[pp.Name]; dupe {
			return fmt.Errorf("duplicate provider %q", pp.Name)
		}
		seen[pp.Name] = struct{}{}
		if strings.TrimSpace(pp.BaseURL) == "" {
			return fmt.Errorf("provider %q: base_url is empty", pp.Name)
		}
		// We deliberately don't require Models or DefaultModel here.
		// A provider with neither is a "user will set --model later"
		// shape, valid in config.toml. The active-provider check
		// below catches the case where the *active* one is empty.
	}
	if p.ActiveProvider == "" {
		return fmt.Errorf("no active provider chosen")
	}
	if _, ok := seen[p.ActiveProvider]; !ok {
		return fmt.Errorf("active provider %q not in enabled list", p.ActiveProvider)
	}
	if p.EnableRouter {
		if !inWhitelist(config.ValidPolicies, p.RouterPolicy) {
			return fmt.Errorf("router policy %q invalid", p.RouterPolicy)
		}
		if len(p.RouterCandList) == 0 {
			return fmt.Errorf("router enabled but no candidates listed")
		}
	}
	if cfg := p.ToConfig(); cfg.Active.Provider != "" {
		if err := config.Validate(cfg); err != nil {
			return err
		}
	}
	return nil
}

// RenderTOML renders the Plan into the canonical config.toml shape we
// want to write. Used by `--print-only` for a stdout dump and by the
// review screen for the diff view.
//
// We hand-render the [[providers]] / [[providers.models]] sections in
// a fixed key order rather than relying on BurntSushi's encoder so the
// output is stable and reads top-to-bottom in the same order it was
// configured. The encoder's emit order is implementation-defined and
// changes between releases, which would surprise users running diffs.
func (p Plan) RenderTOML() string {
	var b strings.Builder
	b.WriteString("# yottacode configuration — generated by `yottacode setup`\n")
	b.WriteString("# Edit freely; rerun `yottacode setup` to add providers without\n")
	b.WriteString("# losing your tunables. API keys live in ~/.yottacode/.env, not here.\n\n")

	if p.ActiveProvider != "" {
		b.WriteString("[active]\n")
		fmt.Fprintf(&b, "provider      = %q\n", p.ActiveProvider)
		if p.ActiveModel != "" {
			fmt.Fprintf(&b, "default_model = %q\n", p.ActiveModel)
		}
		b.WriteString("\n")
	}

	for _, pp := range p.Providers {
		b.WriteString("[[providers]]\n")
		fmt.Fprintf(&b, "name          = %q\n", pp.Name)
		fmt.Fprintf(&b, "kind          = %q\n", pp.Kind)
		fmt.Fprintf(&b, "base_url      = %q\n", pp.BaseURL)
		if pp.APIKeyEnv != "" {
			fmt.Fprintf(&b, "api_key_env   = %q\n", pp.APIKeyEnv)
		}
		if pp.DefaultModel != "" {
			fmt.Fprintf(&b, "default_model = %q\n", pp.DefaultModel)
		}
		// [[providers.models]] blocks are deliberately not emitted —
		// the embedded catalog (internal/catalog/catalog.gen.json) is
		// the source of truth for the cloud provider model list, and
		// free-form providers (Ollama, openai-compatible) get their
		// list at runtime via /v1/models. Hand-curated entries in
		// config.toml still parse and render in /model list, but we
		// don't author new ones.
		b.WriteString("\n")
	}

	if p.EnableRouter && len(p.RouterCandList) > 0 {
		b.WriteString("[router]\n")
		b.WriteString("enabled    = true\n")
		fmt.Fprintf(&b, "policy     = %q\n", p.RouterPolicy)
		b.WriteString("candidates = [")
		for i, c := range p.RouterCandList {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", c)
		}
		b.WriteString("]\n\n")
	}

	return b.String()
}

// RenderEnv renders the .env file contents. Keys are emitted in
// alphabetical order so the file diffs stably across runs. Returns ""
// when there's nothing to write (no keys collected, or
// SkipEnvWrite=true), so callers can skip the write entirely.
func (p Plan) RenderEnv() string {
	if p.SkipEnvWrite || len(p.EnvKeys) == 0 {
		return ""
	}
	keys := make([]string, 0, len(p.EnvKeys))
	for k := range p.EnvKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# yottacode-managed API keys — written by `yottacode setup`.\n")
	b.WriteString("# Mode 0600. Add to git via .gitignore (yottacode does this for you).\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, p.EnvKeys[k])
	}
	return b.String()
}

// Decode parses an existing config.toml at path so the merge step can
// preserve the user's tunables (context, retrieval) across reruns.
// A missing file returns an empty default config and no error —
// that's the first-run case.
func Decode(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Default(), err
	}
	return cfg, nil
}

// Encode marshals a config.Config back to TOML using the encoder. Used
// only by the merge path (when we want the encoder's section
// rendering for tunables); RenderTOML is preferred for [active] and
// [[providers]] because order matters there.
func Encode(cfg config.Config) (string, error) {
	var b strings.Builder
	enc := toml.NewEncoder(&b)
	enc.Indent = ""
	if err := enc.Encode(cfg); err != nil {
		return "", err
	}
	return b.String(), nil
}

func inWhitelist(ws []string, s string) bool {
	for _, w := range ws {
		if w == s {
			return true
		}
	}
	return false
}
