package adapter

import (
	"net/http"
	"testing"
	"time"
)

// TestRecordRateLimit_OpenAIFamily parses the x-ratelimit-* headers
// (duration-style reset) and confirms both buckets land with an
// absolute reset time in the near future.
func TestRecordRateLimit_OpenAIFamily(t *testing.T) {
	SetRateLimitForTest(ProviderOpenAI, nil)
	defer SetRateLimitForTest(ProviderOpenAI, nil)

	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "5000")
	h.Set("x-ratelimit-remaining-requests", "4998")
	h.Set("x-ratelimit-reset-requests", "12s")
	h.Set("x-ratelimit-limit-tokens", "2000000")
	h.Set("x-ratelimit-remaining-tokens", "1824000")
	h.Set("x-ratelimit-reset-tokens", "6m0s")

	recordRateLimit(ProviderOpenAI, h)

	snap := LastRateLimit(ProviderOpenAI)
	if snap == nil {
		t.Fatal("expected a snapshot")
	}
	if !snap.HasRequests || snap.RequestsLimit != 5000 || snap.RequestsRemaining != 4998 {
		t.Errorf("requests bucket wrong: %+v", snap)
	}
	if !snap.HasTokens || snap.TokensLimit != 2_000_000 || snap.TokensRemaining != 1_824_000 {
		t.Errorf("tokens bucket wrong: %+v", snap)
	}
	if d := time.Until(snap.TokensReset); d <= 0 || d > 7*time.Minute {
		t.Errorf("tokens reset should be ~6m out, got %s", d)
	}
}

// TestRecordRateLimit_AnthropicFamily parses the anthropic-ratelimit-*
// headers (RFC 3339 reset) and confirms the combined tokens + requests
// buckets land.
func TestRecordRateLimit_AnthropicFamily(t *testing.T) {
	SetRateLimitForTest(ProviderAnthropic, nil)
	defer SetRateLimitForTest(ProviderAnthropic, nil)

	resetAt := time.Now().Add(45 * time.Second).UTC().Format(time.RFC3339)
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-limit", "4000")
	h.Set("anthropic-ratelimit-requests-remaining", "3998")
	h.Set("anthropic-ratelimit-requests-reset", resetAt)
	h.Set("anthropic-ratelimit-tokens-limit", "2000000")
	h.Set("anthropic-ratelimit-tokens-remaining", "1999000")
	h.Set("anthropic-ratelimit-tokens-reset", resetAt)

	recordRateLimit(ProviderAnthropic, h)

	snap := LastRateLimit(ProviderAnthropic)
	if snap == nil {
		t.Fatal("expected a snapshot")
	}
	if !snap.HasRequests || snap.RequestsRemaining != 3998 {
		t.Errorf("requests bucket wrong: %+v", snap)
	}
	if !snap.HasTokens || snap.TokensLimit != 2_000_000 || snap.TokensRemaining != 1_999_000 {
		t.Errorf("tokens bucket wrong: %+v", snap)
	}
	if d := time.Until(snap.TokensReset); d <= 0 || d > time.Minute {
		t.Errorf("tokens reset should be ~45s out, got %s", d)
	}
}

// TestRecordRateLimit_NoHeadersPreservesSnapshot confirms a response
// without rate-limit headers (e.g. a non-message endpoint) does NOT
// clobber a previously recorded snapshot with an empty one.
func TestRecordRateLimit_NoHeadersPreservesSnapshot(t *testing.T) {
	SetRateLimitForTest(ProviderOpenAI, &RateLimitSnapshot{
		Provider:        ProviderOpenAI,
		HasTokens:       true,
		TokensRemaining: 123,
		TokensLimit:     456,
		Observed:        time.Now(),
	})
	defer SetRateLimitForTest(ProviderOpenAI, nil)

	recordRateLimit(ProviderOpenAI, http.Header{}) // no rate-limit headers

	snap := LastRateLimit(ProviderOpenAI)
	if snap == nil || snap.TokensRemaining != 123 {
		t.Errorf("empty response should not overwrite prior snapshot; got %+v", snap)
	}
}

// TestLastRateLimit_UnknownProvider returns nil so the renderer
// self-hides the block.
func TestLastRateLimit_UnknownProvider(t *testing.T) {
	SetRateLimitForTest(ProviderGemini, nil)
	if snap := LastRateLimit(ProviderGemini); snap != nil {
		t.Errorf("expected nil for a provider with no snapshot, got %+v", snap)
	}
}
