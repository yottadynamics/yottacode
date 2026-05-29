package cost

// xaiPrices is the per-model xAI (Grok) catalog. Numbers are USD per
// 1M tokens, sourced from https://docs.x.ai/docs/models on the date
// stamped in CatalogVersion.
//
// xAI uses standard OpenAI-shape billing: Input + Output, with
// cached_input pricing applied to CacheRead. Reasoning tokens on
// the grok reasoning models bill as Output, so Reasoning stays 0.
var xaiPrices = map[string]ModelPrice{
	// grok-4
	"grok-4":        {Input: 3, Output: 15, CacheRead: 0.75},
	"grok-4-0709":   {Input: 3, Output: 15, CacheRead: 0.75},
	"grok-4-fast":   {Input: 0.20, Output: 0.50, CacheRead: 0.05},

	// grok-3 family
	"grok-3":             {Input: 3, Output: 15, CacheRead: 0.75},
	"grok-3-mini":        {Input: 0.30, Output: 0.50, CacheRead: 0.075},
	"grok-3-mini-fast":   {Input: 0.60, Output: 4, CacheRead: 0.15},
	"grok-3-reasoning":   {Input: 3, Output: 15, CacheRead: 0.75},

	// grok-2 (legacy)
	"grok-2":         {Input: 2, Output: 10, CacheRead: 0.50},
	"grok-2-vision":  {Input: 2, Output: 10, CacheRead: 0.50},
}
