package github

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

// ErrGitHubUnreachable signals that the network couldn't reach
// api.github.com — DNS failure, refused connection, TLS error,
// timeout. Distinct from ErrGhUnavailable (auth) and ErrPRNotFound
// (logical) so callers can branch on the right recovery — auth
// failures want `gh auth login`, unreachable wants "check your
// network".
//
// Surface bubbled up from net.OpError / *url.Error / net DNSError
// shapes returned by go-github. Tested in cache_test.go and
// typed_test.go.
var ErrGitHubUnreachable = errors.New("github API unreachable")

// RateLimitSnapshot is the typed envelope for GitHub's per-hour
// REST rate-limit budget. Returned by TypedClient.RateLimit after
// at least one API call has populated the tracker. Limit is the
// hourly cap (5000 for authenticated users); Remaining is what's
// left; Reset is when the budget refills.
//
// Zero-value snapshot (LastUpdated.IsZero) means "no API call
// has populated this tracker yet" — callers should not infer
// anything from Remaining=0 unless LastUpdated is non-zero.
type RateLimitSnapshot struct {
	Limit       int
	Remaining   int
	Reset       time.Time
	LastUpdated time.Time
}

// IsSet reports whether the snapshot reflects at least one
// observed API call. Callers branch on this before inferring
// anything from the other fields.
func (s RateLimitSnapshot) IsSet() bool {
	return !s.LastUpdated.IsZero()
}

// IsLow reports whether the remaining budget is at or below the
// soft-warn threshold (100). Matches the roadmap's
// "soft-warn at ≤ 100 remaining" target. Callers use this to
// decide whether to surface a warning to the user.
func (s RateLimitSnapshot) IsLow() bool {
	return s.IsSet() && s.Remaining <= 100
}

// WarningText is the canonical scrollback message for a low
// rate-limit state. Empty when the snapshot doesn't warrant a
// warning. Tools call this after a mutation and append the
// non-empty result to their output envelope so the user sees the
// budget pressure inline.
func (s RateLimitSnapshot) WarningText() string {
	if !s.IsLow() {
		return ""
	}
	resetIn := time.Until(s.Reset).Round(time.Minute)
	if resetIn < 0 {
		// Already reset — shouldn't normally hit, but defensive.
		return fmt.Sprintf("[github rate limit: %d/%d remaining]",
			s.Remaining, s.Limit)
	}
	return fmt.Sprintf("[github rate limit: %d/%d remaining; resets in %s]",
		s.Remaining, s.Limit, resetIn)
}

// rateLimitTracker is the lock-protected store TypedClient updates
// after each API call. Sharing a tracker across multiple
// TypedClient instances (e.g., wrappers like CachingClient) is
// not supported — each TypedClient owns its own. The tracker is
// embedded in TypedClient to avoid the pointer-soup of a separate
// type.
type rateLimitTracker struct {
	mu   sync.RWMutex
	snap RateLimitSnapshot
}

func (t *rateLimitTracker) update(snap RateLimitSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	snap.LastUpdated = time.Now()
	t.snap = snap
}

func (t *rateLimitTracker) get() RateLimitSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snap
}

// classifyNetworkError remaps net-shape errors to
// ErrGitHubUnreachable so callers branch on the typed sentinel
// rather than parsing free-text. Covers DNS lookup failures,
// connection refused, TLS handshake failures, and timeouts.
//
// Falls through (returns the original err) for shapes that aren't
// network-level — those are kept distinct because the caller's
// recovery differs (a 422 from the API isn't a network issue).
func classifyNetworkError(err error) error {
	if err == nil {
		return nil
	}
	// *url.Error wraps the underlying net error for go-github's
	// HTTP calls.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// Inner error chain: net.OpError → *net.DNSError /
		// syscall.Errno / tls handshake. All of these are
		// reachability problems for our purposes.
		var dnsErr *net.DNSError
		if errors.As(urlErr.Err, &dnsErr) {
			return fmt.Errorf("%w: dns: %s", ErrGitHubUnreachable, dnsErr.Err)
		}
		var netErr *net.OpError
		if errors.As(urlErr.Err, &netErr) {
			return fmt.Errorf("%w: %s", ErrGitHubUnreachable, netErr.Err)
		}
		// Unknown URL-wrapped error — still a network-layer
		// concern, classify generously.
		return fmt.Errorf("%w: %s", ErrGitHubUnreachable, urlErr.Err)
	}
	// Bare net error (uncommon — go-github usually wraps).
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return fmt.Errorf("%w: %s", ErrGitHubUnreachable, netErr.Err)
	}
	return err
}
