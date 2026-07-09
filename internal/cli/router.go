package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
)

// BuildRouter returns a multi-provider router as adapter.Client when
// cfg.Router.Enabled, otherwise returns (nil, nil) to signal "use the
// single-adapter dispatch path." Errors only on misconfiguration that
// Validate didn't catch (e.g., an env-backed API key is missing at
// runtime even though api_key_env was declared).
//
// Each candidate's adapter is built via adapter.NewWithConfig with the
// same builtin-tool / search flags that the primary adapter would have
// received. Capability gating across heterogeneous providers is a
// Phase 2 concern; the first candidate's profile is the
// representative one returned by router.Profile().
func BuildRouter(cfg config.Config, opts ChatOptions) (adapter.Client, error) {
	if !cfg.Router.Enabled {
		return nil, nil
	}
	resolved, err := cfg.ResolveCandidates()
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	if len(resolved) == 0 {
		return nil, nil
	}
	candidates := make([]adapter.Candidate, 0, len(resolved))
	for _, rc := range resolved {
		adCfg := candidateAdapterConfig(rc, opts)
		client := adapter.NewWithConfig(adCfg)
		label := fmt.Sprintf("%s/%s", rc.Provider.Name, rc.Model)
		candidates = append(candidates, adapter.Candidate{
			Streamer: client,
			Label:    label,
			Tier:     adapter.Tier(rc.Tier),
			Profile:  client.Profile(),
		})
	}
	policy, err := pickPolicy(cfg.Router.Policy)
	if err != nil {
		return nil, err
	}
	return adapter.NewMultiStreamer(candidates, policy, adapter.WithHealth(healthOptionsFromConfig(cfg.Router)))
}

// RouterAdapters bundles the resolved fast/smart adapters for cache-safe
// task routing, plus a resolver for an agent's explicit `model:`
// frontmatter override. These adapters drive only isolated contexts
// (subagents, summarization) — never the main-thread model
// mid-conversation — so swapping a task onto Fast never invalidates the
// parent's prompt cache.
type RouterAdapters struct {
	Fast       adapter.Client
	Smart      adapter.Client
	FastModel  string
	SmartModel string
	// Resolve returns an adapter for an arbitrary configured model
	// name, or nil when the name matches no configured provider model
	// (the caller then inherits the parent/smart adapter). Adapters are
	// memoized so repeated dispatches of the same agent type reuse one
	// client.
	Resolve func(model string) adapter.Streamer
}

// BuildRouterAdapters resolves the [router].fast_model / smart_model
// pair and returns their adapters plus an on-demand resolver. Returns
// (nil, nil) when task routing is disabled (mode "off"/absent). Errors
// only on misconfiguration Validate didn't catch.
func BuildRouterAdapters(cfg config.Config, opts ChatOptions) (*RouterAdapters, error) {
	if !cfg.Router.RoutingEnabled() {
		return nil, nil
	}
	fastRC, smartRC, err := cfg.ResolveRouterModels()
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	// built memoizes one adapter per model name. ra.Resolve is called
	// concurrently — the agent can dispatch several subagents that each
	// declare an explicit `model:` (and background subagents) in the same
	// turn — so every read+write of `built` must hold mu. Without it the
	// Go runtime fatally panics ("concurrent map read and map write"),
	// which is uncatchable and kills the whole session.
	var mu sync.Mutex
	built := map[string]adapter.Client{}
	get := func(rc config.ResolvedCandidate) adapter.Client {
		mu.Lock()
		defer mu.Unlock()
		if c, ok := built[rc.Model]; ok {
			return c
		}
		c := adapter.NewWithConfig(candidateAdapterConfig(rc, opts))
		built[rc.Model] = c
		return c
	}
	ra := &RouterAdapters{
		Fast:       get(fastRC),
		Smart:      get(smartRC),
		FastModel:  fastRC.Model,
		SmartModel: smartRC.Model,
	}
	ra.Resolve = func(model string) adapter.Streamer {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil
		}
		mu.Lock()
		c, ok := built[model]
		mu.Unlock()
		if ok {
			return c
		}
		// Miss: get() takes mu itself, so don't hold it across this call.
		rc, ok := resolveConfiguredModel(cfg, model)
		if !ok {
			return nil
		}
		return get(rc)
	}
	return ra, nil
}

// resolveConfiguredModel finds the first provider that lists the named
// model and returns it as a ResolvedCandidate. Mirrors the TUI's
// /model lookup: curated providers expose the embedded catalog, others
// expose their hand-curated providers.models list.
func resolveConfiguredModel(cfg config.Config, model string) (config.ResolvedCandidate, bool) {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !providerHasModel(p, model) {
			continue
		}
		var tier string
		for _, mm := range p.Models {
			if mm.Name == model {
				tier = mm.Tier
				break
			}
		}
		return config.ResolvedCandidate{Provider: *p, Model: model, Tier: tier}, true
	}
	return config.ResolvedCandidate{}, false
}

func providerHasModel(p *config.Provider, model string) bool {
	if catalog.IsCurated(*p) {
		for _, m := range catalog.Curated(p.Kind) {
			if m.ID == model {
				return true
			}
		}
	}
	for _, mm := range p.Models {
		if mm.Name == model {
			return true
		}
	}
	return false
}

// healthOptionsFromConfig translates the config's wire shape into the
// adapter's HealthOptions. Both fields are runtime-configurable; if the
// user sets either to 0 explicitly the tracker disables observation.
// When both are unset (zero values from a [router] block that omits
// the health knobs), apply the documented defaults.
func healthOptionsFromConfig(rc config.RouterConfig) adapter.HealthOptions {
	window := rc.HealthWindowSeconds
	threshold := rc.HealthFailureThreshold
	if window == 0 && threshold == 0 {
		window = config.DefaultRouterHealthWindowSeconds
		threshold = config.DefaultRouterHealthFailureThreshold
	}
	return adapter.HealthOptions{
		Window:    time.Duration(window) * time.Second,
		Threshold: threshold,
	}
}

// candidateAdapterConfig builds the adapter.Config for one router
// candidate. Pulls api_key_env-resolved key + base_url + model from the
// provider profile, then layers on the search/reasoning flags from
// opts. Builtin-tool toggles are global to the user's intent — if they
// passed --enable-web-search, every capability-supporting candidate
// should advertise it.
func candidateAdapterConfig(rc config.ResolvedCandidate, opts ChatOptions) adapter.Config {
	apiKey := ""
	if rc.Provider.APIKeyEnv != "" {
		apiKey = os.Getenv(rc.Provider.APIKeyEnv)
	}
	maxOutput, supportsThinking := catalog.ReasoningInfo(rc.Model)
	return adapter.Config{
		BaseURL:                rc.Provider.BaseURL,
		APIKey:                 apiKey,
		Model:                  rc.Model,
		ProviderOverride:       adapter.Provider(strings.TrimSpace(rc.Provider.Kind)),
		ReasoningEffort:        opts.ReasoningEffort,
		CacheKey:               opts.CacheKey,
		ModelMaxOutput:         maxOutput,
		ModelSupportsThinking:  supportsThinking,
		EnableWebSearch:        opts.EnableWebSearch,
		DisableWebSearch:       opts.DisableWebSearch,
		EnableXSearch:          opts.EnableXSearch,
		EnableCodeInterpreter:  opts.EnableCodeInterpreter,
		SearchAllowedDomains:   splitCSVField(opts.SearchAllowedDomains),
		SearchExcludedDomains:  splitCSVField(opts.SearchExcludedDomains),
		XSearchAllowedHandles:  splitCSVField(opts.XSearchAllowedHandles),
		XSearchExcludedHandles: splitCSVField(opts.XSearchExcludedHandles),
		XSearchFromDate:        strings.TrimSpace(opts.XSearchFromDate),
		XSearchToDate:          strings.TrimSpace(opts.XSearchToDate),
	}
}

// pickPolicy resolves a config policy name to its concrete adapter.Policy
// implementation. Empty string defaults to fallback-chain — the most
// conservative behavior, matching what a user who turned routing on
// without picking a policy probably wants.
func pickPolicy(name string) (adapter.Policy, error) {
	switch strings.TrimSpace(name) {
	case "", "fallback-chain":
		return adapter.FallbackChain{}, nil
	case "cheap-first":
		return adapter.CheapFirst{}, nil
	default:
		return nil, fmt.Errorf("router: unknown policy %q", name)
	}
}

// splitCSVField mirrors splitCSV in tui/run.go for the cli package's
// use here — keeps internal/cli free of an internal/tui dependency.
func splitCSVField(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
