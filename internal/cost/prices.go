// Package cost holds per-provider, per-model price tables and the
// thin arithmetic that converts an adapter.Usage tally into a USD
// estimate. Prefer the package's own ~ prefix everywhere it surfaces
// in the TUI: real invoices drift from this number once batch
// discounts, prompt-caching tiers, and zone-specific rates apply.
//
// Adding a new model: append to the per-provider map in the matching
// file (anthropic.go, openai.go, gemini.go, xai.go). Numbers are USD
// per 1M tokens, taken from each provider's public pricing page at
// the date stamped in version.go's CatalogVersion. Bump
// CatalogVersion whenever any price changes — /usage surfaces it as
// a freshness signal.
//
// Unknown models intentionally return ok=false so the renderer
// branches on "tokens only, no price data" rather than guessing.
package cost

import "github.com/yottadynamics/yottacode/internal/adapter"

// ModelPrice is USD per 1,000,000 tokens for each token class.
// Reasoning of 0 means "billed at the same rate as Output" (the
// common case — OpenAI bills reasoning tokens as completion
// tokens). CacheWrite / CacheRead are populated only for providers
// with prompt caching exposed in the API.
type ModelPrice struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
	Reasoning  float64
}

// Cost is the computed dollar estimate plus a flag indicating
// whether all the inputs were actually priced (when false, the
// renderer should label the figure as a lower bound).
type Cost struct {
	USD      float64
	Complete bool // false if any token class had no price entry
}

// Lookup returns the price entry for (provider, model) or ok=false
// when the model isn't in any catalog. Matches on the model string
// verbatim — adapter callers pass the same model name they sent on
// the request, so e.g. "claude-sonnet-4-5" and
// "claude-sonnet-4-5-20250929" are distinct keys.
//
// For openai-auth (ChatGPT subscription) and copilot (GitHub
// subscription) Lookup always returns ok=false — there is no
// per-call billing to compute. Callers branch on the provider kind
// and render "subscription" instead of a number.
func Lookup(provider adapter.Provider, model string) (ModelPrice, bool) {
	switch provider {
	case adapter.ProviderAnthropic:
		p, ok := anthropicPrices[model]
		return p, ok
	case adapter.ProviderOpenAI, adapter.ProviderOpenAICompatible:
		p, ok := openaiPrices[model]
		return p, ok
	case adapter.ProviderGemini:
		p, ok := geminiPrices[model]
		return p, ok
	case adapter.ProviderXAI:
		p, ok := xaiPrices[model]
		return p, ok
	}
	return ModelPrice{}, false
}

// Compute converts a usage tally to a dollar estimate using the
// given price entry. Per-1M-tokens rates are scaled by 1e6.
//
// CacheCreationTokens uses CacheWrite when set, otherwise falls
// back to Input (some providers bill cache writes at the same rate
// as ordinary input). Reasoning uses Reasoning when set, otherwise
// falls back to Output (OpenAI's billing convention).
//
// Complete=true means every nonzero token class had a price — if
// the catalog is missing CacheWrite for a model that suddenly
// started reporting CacheCreationTokens, the renderer flags the
// figure as a lower bound and prompts the user to update the
// catalog.
func Compute(price ModelPrice, u adapter.Usage) Cost {
	const perMillion = 1_000_000.0
	var total float64
	complete := true

	if u.InputTokens > 0 {
		if price.Input == 0 {
			complete = false
		} else {
			total += float64(u.InputTokens) * price.Input / perMillion
		}
	}
	if u.OutputTokens > 0 {
		if price.Output == 0 {
			complete = false
		} else {
			total += float64(u.OutputTokens) * price.Output / perMillion
		}
	}
	if u.CacheReadTokens > 0 {
		rate := price.CacheRead
		if rate == 0 {
			rate = price.Input
		}
		if rate == 0 {
			complete = false
		} else {
			total += float64(u.CacheReadTokens) * rate / perMillion
		}
	}
	if u.CacheCreationTokens > 0 {
		rate := price.CacheWrite
		if rate == 0 {
			rate = price.Input
		}
		if rate == 0 {
			complete = false
		} else {
			total += float64(u.CacheCreationTokens) * rate / perMillion
		}
	}
	// ReasoningTokens are a subset of OutputTokens (OpenAI bills them
	// as completion). Counting them separately would double-bill, so
	// only add the *difference* when Reasoning has a distinct rate.
	if u.ReasoningTokens > 0 && price.Reasoning > 0 && price.Reasoning != price.Output {
		delta := (price.Reasoning - price.Output) * float64(u.ReasoningTokens) / perMillion
		total += delta
	}

	return Cost{USD: total, Complete: complete}
}
