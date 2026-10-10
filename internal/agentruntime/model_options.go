package agentruntime

import (
	"fmt"
	"os"
	"slices"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
)

// ModelChoice is one selectable model for a session: the model id plus the
// configured provider profile that serves it.
type ModelChoice struct {
	Model    string
	Provider string
}

// ModelChoices lists every model reachable from the configured
// [[providers]] (default_model plus [[providers.models]]), de-duplicated by
// model id. When the same id is served by several providers, the session's
// current provider wins, then the active one, then config order — the same
// precedence Config.ProviderKindForModel uses — so a model id alone is a
// stable, unambiguous selector. The session's current model is always
// included so the option's current value is always one of its choices.
func ModelChoices(rt *Runtime) []ModelChoice {
	var out []ModelChoice
	seen := map[string]bool{}
	add := func(model, provider string) {
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		out = append(out, ModelChoice{Model: model, Provider: provider})
	}
	for _, p := range providersByPrecedence(rt) {
		add(p.DefaultModel, p.Name)
		for _, m := range p.Models {
			add(m.Name, p.Name)
		}
	}
	if len(out) > 0 {
		add(rt.ChatOptions.Model, rt.ChatOptions.Provider)
	}
	return out
}

// providersByPrecedence orders providers: the session's current one, the
// active one, then the rest in config order.
func providersByPrecedence(rt *Runtime) []config.Provider {
	var ordered []config.Provider
	for _, name := range []string{rt.ChatOptions.Provider, rt.FileCfg.Active.Provider} {
		if p := rt.FileCfg.FindProvider(name); p != nil && !slices.ContainsFunc(ordered, func(o config.Provider) bool { return o.Name == p.Name }) {
			ordered = append(ordered, *p)
		}
	}
	for _, p := range rt.FileCfg.Providers {
		if !slices.ContainsFunc(ordered, func(o config.Provider) bool { return o.Name == p.Name }) {
			ordered = append(ordered, p)
		}
	}
	return ordered
}

// RebuildAdapterForModel switches the session to model, adopting the owning
// provider's profile (kind, base URL, headers, API key from api_key_env) the
// way the TUI's model picker does, then reconstructs rt.Adapter and refreshes
// the model-dependent compaction window. Session-only: nothing is written to
// config.toml. Refused while advisor routing decides the main model, because
// the router would silently override the choice on the next rebuild.
func RebuildAdapterForModel(rt *Runtime, model string) error {
	if rt.FileCfg.Router.RoutingEnabled() {
		return fmt.Errorf("the main model is fixed by advisor routing; disable routing to choose a model")
	}
	var choice *ModelChoice
	for _, c := range ModelChoices(rt) {
		if c.Model == model {
			c := c
			choice = &c
			break
		}
	}
	if choice == nil {
		return fmt.Errorf("unknown model %q — choose one of the configured provider models", model)
	}

	opts := rt.ChatOptions
	if p := rt.FileCfg.FindProvider(choice.Provider); p != nil {
		opts.Provider = p.Name
		opts.ProviderKind = p.Kind
		opts.BaseURL = p.BaseURL
		opts.Headers = cloneStringMap(p.Headers)
		opts.APIKey = ""
		if p.APIKeyEnv != "" {
			opts.APIKey = os.Getenv(p.APIKeyEnv)
		}
	}
	opts.Model = model

	ad := adapter.NewWithConfig(adapterConfig(opts, rt.FileCfg))
	rt.ChatOptions = opts
	rt.Model = model
	if rt.Session != nil {
		rt.Session.Provider = opts.Provider
	}
	rt.Adapter = ad
	rt.Cfg.Adapter = ad
	if rt.AgentTool != nil {
		rt.AgentTool.Adapter = ad
	}
	if rt.Session != nil {
		rt.Session.Model = model
	}
	if rt.Cfg.Compaction != nil {
		if w := catalog.ResolveWindowForProvider(rt.FileCfg.ProviderKindForModel(model), model, rt.FileCfg.ContextWindowOverride(model), rt.FileCfg.Context.DefaultWindow); w > 0 {
			rt.Cfg.Compaction.Window = w
		}
	}
	return nil
}
