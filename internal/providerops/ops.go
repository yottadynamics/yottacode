// Package providerops centralizes the provider/model mutation
// operations the TUI picker and the cobra `yottacode provider` /
// `yottacode model` subcommands both perform. Pure functions over
// config.Config — they don't write to disk, they don't render UI.
// Callers (TUI picker, CLI) handle persistence + presentation
// separately so the mutations stay testable in isolation.
package providerops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/config"
)

// SetActive switches the session-default provider. If the named
// provider has a default_model, we adopt it as the active model
// (both legacy Model and canonical DefaultModel) so callers don't
// also need to make a SetActiveModel call. Returns the modified
// config; the caller persists.
func SetActive(cfg config.Config, name string) (config.Config, error) {
	p := cfg.FindProvider(name)
	if p == nil {
		return cfg, fmt.Errorf("provider %q not configured (run `yottacode provider list`)", name)
	}
	cfg.Active.Provider = p.Name
	if p.DefaultModel != "" {
		cfg.Active.Model = p.DefaultModel
		cfg.Active.DefaultModel = p.DefaultModel
	}
	return cfg, nil
}

// PickFallback returns the name of the first provider remaining in
// cfg.Providers, or "" if none are left. Used by removal call sites
// that auto-switch the active profile after deleting the previous
// active one — declaration order is the chosen heuristic since users
// already arrange [[providers]] in their preferred order.
func PickFallback(cfg config.Config) string {
	if len(cfg.Providers) == 0 {
		return ""
	}
	return cfg.Providers[0].Name
}

// SuggestAPIKeyEnv picks the env-var name a new profile should use
// for its bearer token. The first profile of a given vendor keeps
// the well-known default (e.g. NVIDIA_API_KEY) so existing scripts
// and CI pipelines keep working. Subsequent profiles get a unique
// name derived from the new profile's `name` so each profile carries
// its own key in ~/.yottacode/.env — without this, two NVIDIA NIM
// profiles share one bearer and switching between models that need
// different keys fails with a 401.
//
// fallback is the catalog's default env var (empty for keyless
// providers like Ollama and openai-auth). When fallback is empty,
// SuggestAPIKeyEnv returns "" — the caller skips API-key handling
// entirely.
func SuggestAPIKeyEnv(cfg config.Config, profileName, fallback string) string {
	if fallback == "" {
		return ""
	}
	if !envInUse(cfg, fallback) {
		return fallback
	}
	derived := deriveAPIKeyEnv(profileName)
	if derived == "" {
		return fallback
	}
	if !envInUse(cfg, derived) {
		return derived
	}
	// Pathological case: derived name clashes with a third profile.
	// Append numeric suffix until free. Bound the search so a corrupt
	// config can't spin forever.
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s_%d", derived, i)
		if !envInUse(cfg, candidate) {
			return candidate
		}
	}
	return derived
}

// envInUse reports whether any provider in cfg already declares
// `env` as its api_key_env. Case-sensitive — env-var names are.
func envInUse(cfg config.Config, env string) bool {
	for _, p := range cfg.Providers {
		if p.APIKeyEnv == env {
			return true
		}
	}
	return false
}

// deriveAPIKeyEnv turns a profile name like "nvidia-nim-2" into
// "NVIDIA_NIM_2_API_KEY". Hyphens become underscores; non-alphanumeric
// runes are dropped; ASCII letters uppercase. Returns "" when the
// result would be empty or wouldn't start with a letter (env vars
// that begin with a digit are technically valid in POSIX but
// confuse some shells).
func deriveAPIKeyEnv(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r == '-' || r == '_' || r == ' ':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return ""
	}
	if c := out[0]; c >= '0' && c <= '9' {
		return ""
	}
	return out + "_API_KEY"
}

// Remove deletes the named provider. If it was the active one,
// Active is cleared so the caller can prompt for a fresh choice.
func Remove(cfg config.Config, name string) (config.Config, error) {
	found := false
	out := make([]config.Provider, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return cfg, fmt.Errorf("provider %q not configured", name)
	}
	cfg.Providers = out
	if cfg.Active.Provider == name {
		cfg.Active = config.Active{}
	}
	return cfg, nil
}

// AddProvider is the request shape for Add. The CLI fills it from
// flags; the TUI picker fills it from collected user input. Models
// is optional — free-form providers (Ollama, NVIDIA NIM, custom
// proxies) leave it empty and the catalog is filled at runtime via
// the live /models fetch.
type AddProvider struct {
	Name         string
	Kind         string
	BaseURL      string
	APIKeyEnv    string
	DefaultModel string
	Models       []config.Model
}

// Add appends a new provider. Duplicates and invalid kinds are
// rejected with a clean error so the caller can show the user.
func Add(cfg config.Config, p AddProvider) (config.Config, error) {
	if strings.TrimSpace(p.Name) == "" {
		return cfg, errors.New("provider name is required")
	}
	if cfg.FindProvider(p.Name) != nil {
		return cfg, fmt.Errorf("provider %q already exists; use `provider remove` first", p.Name)
	}
	if !validKind(p.Kind) {
		return cfg, fmt.Errorf("invalid kind %q (expected one of %s)",
			p.Kind, strings.Join(config.ValidKinds, ", "))
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return cfg, errors.New("base_url is required")
	}
	if p.DefaultModel != "" && len(p.Models) > 0 {
		// When a catalog is supplied alongside a default_model, the
		// default_model must appear in it. Mirrors the config.Validate
		// check so we fail at the picker's "save" step, not later.
		ok := false
		for _, m := range p.Models {
			if m.Name == p.DefaultModel {
				ok = true
				break
			}
		}
		if !ok {
			return cfg, fmt.Errorf("default_model %q is not in models list", p.DefaultModel)
		}
	}
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name:         p.Name,
		Kind:         p.Kind,
		BaseURL:      p.BaseURL,
		APIKeyEnv:    p.APIKeyEnv,
		DefaultModel: p.DefaultModel,
		Models:       append([]config.Model(nil), p.Models...),
	})
	return cfg, nil
}

// SetActiveModel updates the active provider's default_model. Used
// by `yottacode model use NAME` and the /model picker. Free-form
// providers (empty Models[]) skip the catalog membership check —
// the catalog is filled at runtime.
func SetActiveModel(cfg config.Config, modelName string) (config.Config, error) {
	if strings.TrimSpace(modelName) == "" {
		return cfg, errors.New("model name is required")
	}
	if cfg.Active.Provider == "" {
		return cfg, errors.New("no active provider; run `provider use <name>` first")
	}
	p := cfg.FindProvider(cfg.Active.Provider)
	if p == nil {
		return cfg, fmt.Errorf("active.provider %q is missing from providers", cfg.Active.Provider)
	}
	if len(p.Models) > 0 {
		found := false
		for _, m := range p.Models {
			if m.Name == modelName {
				found = true
				break
			}
		}
		if !found {
			return cfg, fmt.Errorf("model %q not in providers[%q].models", modelName, p.Name)
		}
	}
	cfg.Active.Model = modelName
	cfg.Active.DefaultModel = modelName
	return cfg, nil
}

func validKind(k string) bool {
	for _, v := range config.ValidKinds {
		if v == k {
			return true
		}
	}
	return false
}
