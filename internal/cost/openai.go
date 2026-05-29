package cost

// openaiPrices is the per-model OpenAI catalog. Numbers are USD per
// 1M tokens, sourced from https://openai.com/api/pricing/ on the
// date stamped in CatalogVersion.
//
// OpenAI bills:
//   - Input (full prompt incl. cached_input)
//   - cached_input at a discount (modeled here as CacheRead)
//   - Output (assistant + reasoning tokens)
//
// Reasoning tokens are counted inside Output billing — Reasoning
// stays 0 here so Compute doesn't double-bill.
//
// The map deliberately omits openai-auth's subscription models
// (gpt-5-codex etc. via chatgpt.com/backend-api/codex) — those are
// flat-fee. /usage branches on provider kind, not model presence.
var openaiPrices = map[string]ModelPrice{
	// gpt-5 family (Responses API)
	"gpt-5":            {Input: 1.25, Output: 10, CacheRead: 0.125},
	"gpt-5-mini":       {Input: 0.25, Output: 2, CacheRead: 0.025},
	"gpt-5-nano":       {Input: 0.05, Output: 0.40, CacheRead: 0.005},
	"gpt-5-2025-08-07": {Input: 1.25, Output: 10, CacheRead: 0.125},

	// o-series (Responses API)
	"o4-mini":            {Input: 1.10, Output: 4.40, CacheRead: 0.275},
	"o4-mini-2025-04-16": {Input: 1.10, Output: 4.40, CacheRead: 0.275},
	"o3":                 {Input: 2, Output: 8, CacheRead: 0.50},
	"o3-2025-04-16":      {Input: 2, Output: 8, CacheRead: 0.50},
	"o3-mini":            {Input: 1.10, Output: 4.40, CacheRead: 0.55},
	"o3-mini-2025-01-31": {Input: 1.10, Output: 4.40, CacheRead: 0.55},
	"o1":                 {Input: 15, Output: 60, CacheRead: 7.50},
	"o1-mini":            {Input: 1.10, Output: 4.40, CacheRead: 0.55},

	// gpt-4o family (Chat Completions)
	"gpt-4o":                 {Input: 2.50, Output: 10, CacheRead: 1.25},
	"gpt-4o-mini":            {Input: 0.15, Output: 0.60, CacheRead: 0.075},
	"gpt-4o-2024-11-20":      {Input: 2.50, Output: 10, CacheRead: 1.25},
	"gpt-4o-2024-08-06":      {Input: 2.50, Output: 10, CacheRead: 1.25},
	"gpt-4o-mini-2024-07-18": {Input: 0.15, Output: 0.60, CacheRead: 0.075},

	// gpt-4.1 family
	"gpt-4.1":            {Input: 2, Output: 8, CacheRead: 0.50},
	"gpt-4.1-mini":       {Input: 0.40, Output: 1.60, CacheRead: 0.10},
	"gpt-4.1-nano":       {Input: 0.10, Output: 0.40, CacheRead: 0.025},
	"gpt-4.1-2025-04-14": {Input: 2, Output: 8, CacheRead: 0.50},
}
