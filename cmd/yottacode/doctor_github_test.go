package main

import (
	"strings"
	"testing"
	"time"

	gh "github.com/yottadynamics/yottacode/internal/github"
)

func TestRenderGitHubProbe_NoToken(t *testing.T) {
	r := GitHubProbeResult{
		Issues: []string{"no GitHub token configured"},
	}
	out := renderGitHubProbe(r)
	for _, want := range []string{"--- github ---", "token: (none)", "github issue: no GitHub token"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nout:\n%s", want, out)
		}
	}
}

func TestRenderGitHubProbe_HappyPath(t *testing.T) {
	r := GitHubProbeResult{
		TokenSource: "env",
		Reachable:   true,
		AuthOK:      true,
		Login:       "octocat",
		Rate: gh.RateLimitSnapshot{
			Limit:       5000,
			Remaining:   4500,
			Reset:       time.Now().Add(45 * time.Minute),
			LastUpdated: time.Now(),
		},
	}
	out := renderGitHubProbe(r)
	for _, want := range []string{
		"--- github ---",
		"token: present (source=env)",
		"reachable=yes",
		"auth=yes",
		"user=octocat",
		"4500/5000 remaining",
		"github: ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nout:\n%s", want, out)
		}
	}
}

func TestRenderGitHubProbe_AuthFailure(t *testing.T) {
	r := GitHubProbeResult{
		TokenSource: "file",
		Reachable:   true,
		AuthOK:      false,
		Issues:      []string{"token rejected by api.github.com (401)"},
	}
	out := renderGitHubProbe(r)
	for _, want := range []string{
		"token: present (source=file)",
		"reachable=yes",
		"auth=no",
		"github issue: token rejected",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nout:\n%s", want, out)
		}
	}
	if strings.Contains(out, "github: ok") {
		t.Errorf("ok marker must not appear when issues are present")
	}
}

func TestRenderGitHubProbe_Unreachable(t *testing.T) {
	r := GitHubProbeResult{
		TokenSource: "env",
		Reachable:   false,
		AuthOK:      false,
		Issues:      []string{"api.github.com unreachable: dns: no such host"},
	}
	out := renderGitHubProbe(r)
	for _, want := range []string{
		"reachable=no",
		"auth=no",
		"unreachable",
		"dns",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nout:\n%s", want, out)
		}
	}
}

func TestRenderGitHubProbe_LowRateWarns(t *testing.T) {
	r := GitHubProbeResult{
		TokenSource: "env",
		Reachable:   true,
		AuthOK:      true,
		Login:       "octocat",
		Rate: gh.RateLimitSnapshot{
			Limit:       5000,
			Remaining:   50,
			Reset:       time.Now().Add(15 * time.Minute),
			LastUpdated: time.Now(),
		},
		Warnings: []string{"rate limit low: 50/5000 remaining"},
	}
	out := renderGitHubProbe(r)
	for _, want := range []string{
		"50/5000 remaining",
		"github warning: rate limit low",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nout:\n%s", want, out)
		}
	}
	// The "ok" marker should NOT appear when warnings are present.
	if strings.Contains(out, "github: ok") {
		t.Errorf("ok marker must not appear alongside warnings")
	}
}

func TestRenderGitHubProbe_OmitsResetWhenZero(t *testing.T) {
	r := GitHubProbeResult{
		TokenSource: "env",
		Reachable:   true,
		AuthOK:      true,
		Rate: gh.RateLimitSnapshot{
			Limit:       5000,
			Remaining:   4500,
			LastUpdated: time.Now(),
			// Reset intentionally zero
		},
	}
	out := renderGitHubProbe(r)
	if strings.Contains(out, "resets in") {
		t.Errorf("reset-in clause should be omitted when Reset is zero; got:\n%s", out)
	}
}
