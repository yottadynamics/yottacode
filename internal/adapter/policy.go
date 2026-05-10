package adapter

import "sort"

// Tier is the coarse cost/capability bucket a candidate model belongs to.
// Mirrors the string enum already used in config.toml's
// [[providers.models]] entries (cheap | balanced | expensive). Empty tier
// is "unspecified" and sorts last under cost-aware policies.
type Tier string

const (
	TierUnspecified Tier = ""
	TierCheap       Tier = "cheap"
	TierBalanced    Tier = "balanced"
	TierExpensive   Tier = "expensive"
)

// Candidate is one dispatch target the router may select. Label is the
// human-readable identifier surfaced in EventFallback so the user can see
// which provider/model just failed and which one took over —
// e.g. "anthropic/claude-haiku-4-5" or "openai/gpt-4o". Tier is optional
// metadata consumed by tier-aware policies (CheapFirst); policies that do
// not consult it (FallbackChain) can ignore it. Profile lets the router
// satisfy the Client interface — the TUI's system-prompt composition and
// connection probe both want a ProviderProfile, and the router's
// representative profile is the first candidate's.
type Candidate struct {
	Streamer Streamer
	Label    string
	Tier     Tier
	Profile  ProviderProfile
}

// Policy decides the order in which a MultiStreamer tries its candidates
// for one request. It is invoked once per ChatStream call, with the full
// candidate slice; the returned slice is iterated in order until one
// succeeds (returns EventDone) or the list is exhausted.
//
// Policy is intentionally not request-aware in this iteration. Smarter
// per-request routing (capability gating, request classification) is a
// Phase 2 concern; declarative policies first.
type Policy interface {
	// Order returns the candidates in the order they should be tried.
	// Implementations must not mutate the input slice.
	Order(candidates []Candidate) []Candidate
	// Name is a short identifier surfaced in EventFallback for telemetry
	// and TUI logging. e.g. "fallback-chain", "cheap-first".
	Name() string
}

// FallbackChain tries candidates in declared order. The simplest policy
// and the right default when the user has hand-ordered their providers
// by preference (typical: "use Anthropic by default, fall through to
// OpenAI if Anthropic is degraded").
type FallbackChain struct{}

func (FallbackChain) Order(candidates []Candidate) []Candidate {
	out := make([]Candidate, len(candidates))
	copy(out, candidates)
	return out
}

func (FallbackChain) Name() string { return "fallback-chain" }

// CheapFirst sorts candidates by tier ascending: cheap → balanced →
// expensive → unspecified. Uses a stable sort, so candidates with the
// same tier (or no tier) preserve their declared order — this lets the
// user disambiguate two cheap-tier candidates by listing the preferred
// one first.
type CheapFirst struct{}

func (CheapFirst) Order(candidates []Candidate) []Candidate {
	out := make([]Candidate, len(candidates))
	copy(out, candidates)
	sort.SliceStable(out, func(i, j int) bool {
		return tierRank(out[i].Tier) < tierRank(out[j].Tier)
	})
	return out
}

func (CheapFirst) Name() string { return "cheap-first" }

// tierRank gives Tier values a total order for sorting. Unspecified
// sorts last so a candidate with no tier annotation does not silently
// outrank a properly-tagged cheap-tier one.
func tierRank(t Tier) int {
	switch t {
	case TierCheap:
		return 0
	case TierBalanced:
		return 1
	case TierExpensive:
		return 2
	default:
		return 3
	}
}
