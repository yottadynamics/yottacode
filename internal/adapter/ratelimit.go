package adapter

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimitSnapshot is the parsed quota state from the most recent
// successful API response for a provider. Pay-per-use providers
// (OpenAI, Anthropic, xAI) return per-minute rate-limit headers on
// every 200, costing nothing extra beyond the inference call we
// already make. /usage surfaces this as a live "how much headroom is
// left this minute" gauge — distinct from cost (which we estimate from
// token counts) and from the openai-auth 429 memo (which is only
// populated reactively on a rate-limit error).
//
// The Has* flags distinguish "header absent" from "header present and
// zero" so the renderer can omit a family entirely rather than show a
// misleading 0.
type RateLimitSnapshot struct {
	Provider Provider
	Observed time.Time

	// Requests-per-window budget.
	HasRequests       bool
	RequestsLimit     int64
	RequestsRemaining int64
	RequestsReset     time.Time

	// Token budget. For OpenAI this is the single tokens bucket; for
	// Anthropic it's the combined tokens bucket (the most restrictive
	// limit in effect, per their docs).
	HasTokens       bool
	TokensLimit     int64
	TokensRemaining int64
	TokensReset     time.Time
}

var (
	rateLimitsMu sync.RWMutex
	rateLimits   = map[Provider]*RateLimitSnapshot{}
)

// LastRateLimit returns the most recent rate-limit snapshot for a
// provider, or nil if none has been observed this run. The returned
// pointer is a copy — callers can read it without holding the lock.
func LastRateLimit(provider Provider) *RateLimitSnapshot {
	rateLimitsMu.RLock()
	defer rateLimitsMu.RUnlock()
	snap, ok := rateLimits[provider]
	if !ok {
		return nil
	}
	c := *snap
	return &c
}

// SetRateLimitForTest seeds (or clears, with nil) the snapshot for a
// provider so the /usage renderer can be exercised without a live API
// round-trip.
func SetRateLimitForTest(provider Provider, snap *RateLimitSnapshot) {
	rateLimitsMu.Lock()
	defer rateLimitsMu.Unlock()
	if snap == nil {
		delete(rateLimits, provider)
		return
	}
	c := *snap
	rateLimits[provider] = &c
}

// recordRateLimit parses the rate-limit headers off a response and
// stores the snapshot keyed by provider. Both header families are
// tried; a response carries at most one. When neither family is
// present (e.g. a non-message endpoint, or a provider that doesn't
// emit them) the existing snapshot is left untouched rather than
// overwritten with an empty one.
func recordRateLimit(provider Provider, h http.Header) {
	if h == nil {
		return
	}
	now := time.Now()
	snap := RateLimitSnapshot{Provider: provider, Observed: now}

	// OpenAI / xAI family: x-ratelimit-*-{requests,tokens}, where the
	// reset value is a Go-style duration string ("880ms", "6m0s").
	if lim, rem, reset, ok := parseOpenAIRateFamily(h, "requests", now); ok {
		snap.HasRequests, snap.RequestsLimit, snap.RequestsRemaining, snap.RequestsReset = true, lim, rem, reset
	}
	if lim, rem, reset, ok := parseOpenAIRateFamily(h, "tokens", now); ok {
		snap.HasTokens, snap.TokensLimit, snap.TokensRemaining, snap.TokensReset = true, lim, rem, reset
	}

	// Anthropic family: anthropic-ratelimit-{requests,tokens}-*, where
	// the reset value is an RFC 3339 timestamp.
	if lim, rem, reset, ok := parseAnthropicRateFamily(h, "requests"); ok {
		snap.HasRequests, snap.RequestsLimit, snap.RequestsRemaining, snap.RequestsReset = true, lim, rem, reset
	}
	if lim, rem, reset, ok := parseAnthropicRateFamily(h, "tokens"); ok {
		snap.HasTokens, snap.TokensLimit, snap.TokensRemaining, snap.TokensReset = true, lim, rem, reset
	}

	if !snap.HasRequests && !snap.HasTokens {
		return
	}
	rateLimitsMu.Lock()
	rateLimits[provider] = &snap
	rateLimitsMu.Unlock()
}

// parseOpenAIRateFamily reads x-ratelimit-{limit,remaining,reset}-<kind>.
// The reset header is a duration relative to now ("1s", "6m0s"), which
// we convert to an absolute time. Returns ok=false unless at least the
// remaining value parses — a family with no usable numbers isn't worth
// recording.
func parseOpenAIRateFamily(h http.Header, kind string, now time.Time) (limit, remaining int64, reset time.Time, ok bool) {
	rem, remOK := parseHeaderInt(h, "x-ratelimit-remaining-"+kind)
	if !remOK {
		return 0, 0, time.Time{}, false
	}
	lim, _ := parseHeaderInt(h, "x-ratelimit-limit-"+kind)
	if d, err := time.ParseDuration(h.Get("x-ratelimit-reset-" + kind)); err == nil && d > 0 {
		reset = now.Add(d)
	}
	return lim, rem, reset, true
}

// parseAnthropicRateFamily reads anthropic-ratelimit-<kind>-{limit,
// remaining,reset}. The reset header is an RFC 3339 timestamp.
func parseAnthropicRateFamily(h http.Header, kind string) (limit, remaining int64, reset time.Time, ok bool) {
	rem, remOK := parseHeaderInt(h, "anthropic-ratelimit-"+kind+"-remaining")
	if !remOK {
		return 0, 0, time.Time{}, false
	}
	lim, _ := parseHeaderInt(h, "anthropic-ratelimit-"+kind+"-limit")
	if t, err := time.Parse(time.RFC3339, h.Get("anthropic-ratelimit-"+kind+"-reset")); err == nil {
		reset = t
	}
	return lim, rem, reset, true
}

func parseHeaderInt(h http.Header, key string) (int64, bool) {
	v := h.Get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// recordRateLimitMiddleware returns an SDK request middleware that
// snapshots rate-limit headers off every response. The func type
// matches both openai-go's and anthropic-sdk-go's option.Middleware
// (each is a type alias to this same underlying signature), so a single
// definition wires into both clients. We only read resp.Header —
// reading headers never touches resp.Body, so streaming responses are
// left intact for the SDK to consume.
func recordRateLimitMiddleware(provider Provider) func(*http.Request, func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		resp, err := next(req)
		if err == nil && resp != nil {
			recordRateLimit(provider, resp.Header)
		}
		return resp, err
	}
}
