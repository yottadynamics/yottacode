package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
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
	return adapter.Config{
		BaseURL:                rc.Provider.BaseURL,
		APIKey:                 apiKey,
		Model:                  rc.Model,
		ProviderOverride:       adapter.Provider(strings.TrimSpace(rc.Provider.Kind)),
		ReasoningEffort:        opts.ReasoningEffort,
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
