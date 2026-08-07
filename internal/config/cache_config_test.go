package config

import (
	"strings"
	"testing"
)

// TestLoad_CacheAnthropicTTLValidateAndRoundTrip guards the persistence
// path for the opt-in cache TTL: a config.toml value survives
// Load → Render → Load unchanged.
func TestLoad_CacheAnthropicTTLValidateAndRoundTrip(t *testing.T) {
	cfg, err := Load(writeFile(t, `
[cache]
anthropic_ttl = "1h"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cache.AnthropicTTL != "1h" {
		t.Fatalf("cache.anthropic_ttl = %q, want \"1h\"", cfg.Cache.AnthropicTTL)
	}
	roundTripped, err := Load(writeFile(t, Render(cfg)))
	if err != nil {
		t.Fatalf("Load(Render): %v", err)
	}
	if roundTripped.Cache.AnthropicTTL != "1h" {
		t.Fatalf("anthropic_ttl lost after Render: %q\nrendered:\n%s", roundTripped.Cache.AnthropicTTL, Render(cfg))
	}
}

// TestLoad_CacheAnthropicTTLEmptyIsValid confirms the unset/default
// case loads cleanly — empty means "use Anthropic's own 5m default",
// not an error.
func TestLoad_CacheAnthropicTTLEmptyIsValid(t *testing.T) {
	cfg, err := Load(writeFile(t, `[cache]`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cache.AnthropicTTL != "" {
		t.Fatalf("cache.anthropic_ttl = %q, want empty", cfg.Cache.AnthropicTTL)
	}
}

// TestLoad_RejectsInvalidCacheAnthropicTTL guards against a typo'd
// value silently no-op'ing at the Anthropic adapter (which only
// special-cases the literal "1h") instead of failing fast at load.
func TestLoad_RejectsInvalidCacheAnthropicTTL(t *testing.T) {
	_, err := Load(writeFile(t, `
[cache]
anthropic_ttl = "1 hour"
`))
	if err == nil || !strings.Contains(err.Error(), "cache.anthropic_ttl") {
		t.Fatalf("Load error = %v, want containing \"cache.anthropic_ttl\"", err)
	}
}
