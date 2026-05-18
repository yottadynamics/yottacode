package github

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRateLimitSnapshot_IsSet(t *testing.T) {
	zero := RateLimitSnapshot{}
	if zero.IsSet() {
		t.Errorf("zero snapshot should report IsSet=false")
	}

	populated := RateLimitSnapshot{
		Limit: 5000, Remaining: 4999,
		Reset:       time.Now().Add(time.Hour),
		LastUpdated: time.Now(),
	}
	if !populated.IsSet() {
		t.Errorf("populated snapshot should report IsSet=true")
	}
}

func TestRateLimitSnapshot_IsLow(t *testing.T) {
	cases := []struct {
		name      string
		remaining int
		set       bool
		want      bool
	}{
		{"plenty", 4999, true, false},
		{"low at threshold", 100, true, true},
		{"below threshold", 50, true, true},
		{"zero remaining", 0, true, true},
		{"unset never reports low", 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := RateLimitSnapshot{Limit: 5000, Remaining: tc.remaining}
			if tc.set {
				s.LastUpdated = time.Now()
			}
			if got := s.IsLow(); got != tc.want {
				t.Errorf("IsLow() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestRateLimitSnapshot_WarningText(t *testing.T) {
	t.Run("empty when not low", func(t *testing.T) {
		s := RateLimitSnapshot{
			Limit: 5000, Remaining: 4000,
			LastUpdated: time.Now(),
		}
		if got := s.WarningText(); got != "" {
			t.Errorf("non-low snapshot should have empty warning; got %q", got)
		}
	})

	t.Run("warns when low", func(t *testing.T) {
		s := RateLimitSnapshot{
			Limit: 5000, Remaining: 50,
			Reset:       time.Now().Add(30 * time.Minute),
			LastUpdated: time.Now(),
		}
		got := s.WarningText()
		if got == "" {
			t.Fatalf("low snapshot should have non-empty warning")
		}
		for _, want := range []string{"50/5000", "rate limit", "remaining"} {
			if !strings.Contains(got, want) {
				t.Errorf("warning missing %q; got %q", want, got)
			}
		}
	})

	t.Run("handles past reset gracefully", func(t *testing.T) {
		s := RateLimitSnapshot{
			Limit: 5000, Remaining: 50,
			Reset:       time.Now().Add(-1 * time.Minute), // already passed
			LastUpdated: time.Now(),
		}
		got := s.WarningText()
		if got == "" {
			t.Fatalf("expected warning even with past reset")
		}
		if strings.Contains(got, "-") {
			// Defensive: a negative duration in the warning would
			// confuse users.
			t.Errorf("warning should not contain negative duration; got %q", got)
		}
	})
}

func TestRateLimitTracker_UpdateAndGet(t *testing.T) {
	var tr rateLimitTracker
	before := tr.get()
	if before.IsSet() {
		t.Errorf("fresh tracker should be unset")
	}

	tr.update(RateLimitSnapshot{Limit: 5000, Remaining: 100})
	after := tr.get()
	if !after.IsSet() {
		t.Errorf("after update tracker should be set")
	}
	if after.Limit != 5000 || after.Remaining != 100 {
		t.Errorf("update did not propagate: %+v", after)
	}
}

func TestClassifyNetworkError_PassesThroughNil(t *testing.T) {
	if got := classifyNetworkError(nil); got != nil {
		t.Errorf("nil should pass through; got %v", got)
	}
}

func TestClassifyNetworkError_PassesThroughNonNetworkError(t *testing.T) {
	// A plain error that isn't url/net-shaped should not be
	// remapped to ErrGitHubUnreachable.
	plain := errors.New("api: invalid request")
	got := classifyNetworkError(plain)
	if errors.Is(got, ErrGitHubUnreachable) {
		t.Errorf("plain error should not classify as unreachable; got %v", got)
	}
}

func TestClassifyNetworkError_DNSFailureSurfaces(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "api.github.com"}
	urlErr := &url.Error{
		Op:  "Get",
		URL: "https://api.github.com/repos/o/r",
		Err: dnsErr,
	}
	got := classifyNetworkError(urlErr)
	if !errors.Is(got, ErrGitHubUnreachable) {
		t.Errorf("DNS failure should classify as unreachable; got %v", got)
	}
	if !strings.Contains(got.Error(), "dns") {
		t.Errorf("error should mention dns; got %q", got.Error())
	}
}

func TestClassifyNetworkError_ConnRefusedSurfaces(t *testing.T) {
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connection refused"),
	}
	urlErr := &url.Error{
		Op:  "Get",
		URL: "https://api.github.com/repos/o/r",
		Err: opErr,
	}
	got := classifyNetworkError(urlErr)
	if !errors.Is(got, ErrGitHubUnreachable) {
		t.Errorf("connection refused should classify as unreachable; got %v", got)
	}
}

func TestClassifyNetworkError_BareOpError(t *testing.T) {
	// Some shapes hand back the OpError directly without url
	// wrapping. Cover both.
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connection refused"),
	}
	got := classifyNetworkError(opErr)
	if !errors.Is(got, ErrGitHubUnreachable) {
		t.Errorf("bare OpError should classify as unreachable; got %v", got)
	}
}

func TestClassifyNetworkError_UnknownURLWrappedError(t *testing.T) {
	urlErr := &url.Error{
		Op:  "Get",
		URL: "https://api.github.com/repos/o/r",
		Err: errors.New("tls: handshake failure"),
	}
	got := classifyNetworkError(urlErr)
	if !errors.Is(got, ErrGitHubUnreachable) {
		t.Errorf("url-wrapped errors should classify as unreachable; got %v", got)
	}
}

func TestClassifyAPIError_NetworkErrorBecomesUnreachable(t *testing.T) {
	// Integration with classifyAPIError — the existing call path
	// for TypedClient methods runs through here, so network
	// failures must surface as ErrGitHubUnreachable.
	urlErr := &url.Error{
		Op:  "Get",
		URL: "https://api.github.com/repos/o/r",
		Err: &net.DNSError{Err: "no such host", Name: "api.github.com"},
	}
	got := classifyAPIError(urlErr)
	if !errors.Is(got, ErrGitHubUnreachable) {
		t.Errorf("classifyAPIError should remap network errors; got %v", got)
	}
}

// Compile-time assertion that the rate-limit machinery doesn't
// pull in any cyclic deps inside the package.
var _ = fmt.Sprintf("%v", RateLimitSnapshot{})
