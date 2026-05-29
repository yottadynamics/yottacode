package cost

// anthropicPrices is the per-model Anthropic price catalog. Numbers
// are USD per 1M tokens, sourced from https://www.anthropic.com/pricing#anthropic-api
// on the date stamped in CatalogVersion.
//
// Anthropic exposes four distinct rates per model:
//   - Input (uncached prompt tokens)
//   - Output (assistant tokens)
//   - CacheWrite (cached_input writes; 1.25x Input)
//   - CacheRead (cached_input reads; 0.1x Input)
//
// Keys are the Anthropic model IDs as the API accepts them. Aliases
// (e.g. "claude-sonnet-4-5" vs the dated form) get separate entries
// so users who pin a specific snapshot still get a price.
var anthropicPrices = map[string]ModelPrice{
	// Claude Sonnet 4.5 / 4.7 (latest generation)
	"claude-sonnet-4-7":          {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-sonnet-4-7-20251201": {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-sonnet-4-5":          {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-sonnet-4-5-20250929": {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-sonnet-4-0":          {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},

	// Claude Haiku 4.5
	"claude-haiku-4-5":          {Input: 1, Output: 5, CacheWrite: 1.25, CacheRead: 0.10},
	"claude-haiku-4-5-20251001": {Input: 1, Output: 5, CacheWrite: 1.25, CacheRead: 0.10},

	// Claude Opus 4.x
	"claude-opus-4-7":          {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50},
	"claude-opus-4-5":          {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50},
	"claude-opus-4-5-20250929": {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50},
	"claude-opus-4-1":          {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50},
	"claude-opus-4-0":          {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50},

	// Claude 3.7 / 3.5 (still widely referenced in scripts)
	"claude-3-7-sonnet-latest":   {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-3-7-sonnet-20250219": {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-3-5-sonnet-latest":   {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-3-5-sonnet-20241022": {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
	"claude-3-5-haiku-latest":    {Input: 0.80, Output: 4, CacheWrite: 1, CacheRead: 0.08},
	"claude-3-5-haiku-20241022":  {Input: 0.80, Output: 4, CacheWrite: 1, CacheRead: 0.08},
}
