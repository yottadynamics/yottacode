package cost

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestLookup_KnownAndUnknownModels locks the catalog gate: every
// representative model that ships in the per-provider files must
// return ok=true, and any model the catalog doesn't know must
// return ok=false (so /usage shows "tokens only" rather than
// guessing).
func TestLookup_KnownAndUnknownModels(t *testing.T) {
	known := []struct {
		provider adapter.Provider
		model    string
	}{
		{adapter.ProviderAnthropic, "claude-sonnet-4-5"},
		{adapter.ProviderAnthropic, "claude-opus-4-5"},
		{adapter.ProviderOpenAI, "gpt-5"},
		{adapter.ProviderOpenAI, "gpt-4o"},
		{adapter.ProviderOpenAI, "o4-mini"},
		{adapter.ProviderGemini, "gemini-2.5-pro"},
		{adapter.ProviderXAI, "grok-4"},
	}
	for _, c := range known {
		t.Run(string(c.provider)+"/"+c.model, func(t *testing.T) {
			p, ok := Lookup(c.provider, c.model)
			if !ok {
				t.Fatalf("Lookup(%s, %s) ok=false — catalog missing", c.provider, c.model)
			}
			if p.Input == 0 || p.Output == 0 {
				t.Errorf("Lookup(%s, %s) returned zero rates: %+v", c.provider, c.model, p)
			}
		})
	}

	unknown := []struct {
		provider adapter.Provider
		model    string
	}{
		{adapter.ProviderAnthropic, "claude-vapor-9000"},
		{adapter.ProviderOpenAI, "gpt-12-supernova"},
		{adapter.ProviderOpenAIAuth, "any-model"},   // subscription — never priced
		{adapter.ProviderCopilot, "any-model"},      // subscription — never priced
		{adapter.ProviderOllama, "qwen2.5"},         // local — never priced
	}
	for _, c := range unknown {
		t.Run("unknown/"+string(c.provider)+"/"+c.model, func(t *testing.T) {
			if _, ok := Lookup(c.provider, c.model); ok {
				t.Errorf("Lookup(%s, %s) ok=true — should be unknown", c.provider, c.model)
			}
		})
	}
}

// TestCompute_LocksMath golden-pins the arithmetic for a handful of
// (model, usage) tuples. A drift here means either the catalog was
// edited without bumping CatalogVersion (acceptable) OR the Compute
// formula changed (worth scrutinizing). The numbers below match
// hand-computed expectations:
//
// claude-sonnet-4-5: 10k uncached input + 5k output + 2k cache write
//   + 8k cache read =
//   10000 * 3 + 5000 * 15 + 2000 * 3.75 + 8000 * 0.30 = 30 + 75 + 7.5 + 2.4 = 114.9 µ$
//   → $0.1149
//
// gpt-5: 20k input + 6k output + 4k cache read =
//   20000 * 1.25 + 6000 * 10 + 4000 * 0.125 = 25 + 60 + 0.5 = 85.5 µ$
//   → $0.0855
func TestCompute_LocksMath(t *testing.T) {
	cases := []struct {
		name     string
		provider adapter.Provider
		model    string
		usage    adapter.Usage
		wantUSD  float64
	}{
		{
			name:     "anthropic claude-sonnet-4-5 with cache",
			provider: adapter.ProviderAnthropic,
			model:    "claude-sonnet-4-5",
			usage: adapter.Usage{
				InputTokens:         10_000,
				OutputTokens:        5_000,
				CacheCreationTokens: 2_000,
				CacheReadTokens:     8_000,
			},
			wantUSD: 0.1149,
		},
		{
			name:     "openai gpt-5 with cached_input",
			provider: adapter.ProviderOpenAI,
			model:    "gpt-5",
			usage: adapter.Usage{
				InputTokens:     20_000,
				OutputTokens:    6_000,
				CacheReadTokens: 4_000,
			},
			wantUSD: 0.0855,
		},
		{
			name:     "gemini 2.5 flash, no cache",
			provider: adapter.ProviderGemini,
			model:    "gemini-2.5-flash",
			usage: adapter.Usage{
				InputTokens:  100_000,
				OutputTokens: 5_000,
			},
			// 100000 * 0.30 + 5000 * 2.50 = 30 + 12.5 = 42.5 µ$ → $0.0425
			wantUSD: 0.0425,
		},
		{
			name:     "xai grok-4",
			provider: adapter.ProviderXAI,
			model:    "grok-4",
			usage: adapter.Usage{
				InputTokens:  1_000,
				OutputTokens: 500,
			},
			// 1000 * 3 + 500 * 15 = 3 + 7.5 = 10.5 µ$ → $0.0105
			wantUSD: 0.0105,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			price, ok := Lookup(tc.provider, tc.model)
			if !ok {
				t.Fatalf("Lookup miss")
			}
			got := Compute(price, tc.usage)
			if !got.Complete {
				t.Errorf("Compute.Complete = false; want true for fully-priced model")
			}
			if diff := got.USD - tc.wantUSD; diff > 0.00005 || diff < -0.00005 {
				t.Errorf("Compute.USD = %.6f, want %.4f (diff %.6f)", got.USD, tc.wantUSD, diff)
			}
		})
	}
}

// TestCompute_UnknownClassFlagsIncomplete checks the "we observed a
// token class with no price" path. Anthropic's CacheWrite is missing
// here (zero), so the cache-creation tokens fall back to Input rate
// — but if we deliberately wipe both, Compute must return
// Complete=false so the renderer can label the figure as a lower bound.
func TestCompute_UnknownClassFlagsIncomplete(t *testing.T) {
	price := ModelPrice{Output: 10} // no Input, no CacheWrite
	got := Compute(price, adapter.Usage{InputTokens: 100, OutputTokens: 50})
	if got.Complete {
		t.Errorf("Complete = true; want false when Input price is missing")
	}
	if got.USD == 0 {
		t.Errorf("USD = 0; want some output charge even though input is unpriced")
	}
}

// TestCatalogVersionPresent guards against the version constant being
// silently emptied — /usage's footer depends on this being a non-empty
// date string.
func TestCatalogVersionPresent(t *testing.T) {
	if CatalogVersion == "" {
		t.Fatal("CatalogVersion is empty; bump it on every catalog edit")
	}
	// Loose shape check: YYYY-MM-DD = 10 chars.
	if len(CatalogVersion) != 10 {
		t.Errorf("CatalogVersion = %q; want YYYY-MM-DD", CatalogVersion)
	}
}
