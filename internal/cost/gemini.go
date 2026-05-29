package cost

// geminiPrices is the per-model Google Gemini catalog. Numbers are
// USD per 1M tokens, sourced from https://ai.google.dev/pricing on
// the date stamped in CatalogVersion.
//
// Gemini exposes:
//   - Input (uncached prompt)
//   - Output (assistant + thoughts)
//   - cachedContent reads (CacheRead)
//
// Thoughts tokens are billed as output, so Reasoning stays 0 to
// avoid double-billing in Compute.
//
// Note: Gemini has a two-tier pricing schedule for some models
// (≤200K prompt = base rate, >200K = ~2x). The catalog records the
// base rate; users with very long prompts should treat /usage as a
// lower bound.
var geminiPrices = map[string]ModelPrice{
	// Gemini 2.5 family
	"gemini-2.5-pro":         {Input: 1.25, Output: 10, CacheRead: 0.3125},
	"gemini-2.5-flash":       {Input: 0.30, Output: 2.50, CacheRead: 0.075},
	"gemini-2.5-flash-lite":  {Input: 0.10, Output: 0.40, CacheRead: 0.025},

	// Gemini 2.0 family
	"gemini-2.0-flash":            {Input: 0.10, Output: 0.40, CacheRead: 0.025},
	"gemini-2.0-flash-001":        {Input: 0.10, Output: 0.40, CacheRead: 0.025},
	"gemini-2.0-flash-lite":       {Input: 0.075, Output: 0.30, CacheRead: 0.01875},
	"gemini-2.0-flash-thinking":   {Input: 0.10, Output: 0.40, CacheRead: 0.025},

	// Gemini 1.5 family (still in production for some integrations)
	"gemini-1.5-pro":        {Input: 1.25, Output: 5, CacheRead: 0.3125},
	"gemini-1.5-pro-latest": {Input: 1.25, Output: 5, CacheRead: 0.3125},
	"gemini-1.5-flash":      {Input: 0.075, Output: 0.30, CacheRead: 0.01875},
	"gemini-1.5-flash-8b":   {Input: 0.0375, Output: 0.15, CacheRead: 0.01},
}
