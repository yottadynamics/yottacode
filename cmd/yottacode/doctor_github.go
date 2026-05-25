package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	gh "github.com/yottadynamics/yottacode/internal/github"
)

// GitHubProbeResult is the typed envelope the doctor's GitHub
// section returns. Mirrors the existing adapter.ProbeResult shape
// (status flags + issue / warning lists) so the renderer can fold
// it into the same "result: ok / issues / warnings" trailer the
// provider probe uses.
type GitHubProbeResult struct {
	// TokenSource is which tier of the auth chain supplied the
	// token: "env" (GITHUB_TOKEN), "gh" (gh auth token), "file"
	// (~/.yottacode/github.json), or "" when no token was found.
	TokenSource string

	// AuthOK is true when an authenticated /user call returned 200.
	// False when the token failed, no token was found, or the API
	// was unreachable.
	AuthOK bool

	// Reachable is true when the /user endpoint responded at all
	// (HTTP status >= 100). False on DNS / connection / TLS errors.
	Reachable bool

	// Login is the authenticated user's login when AuthOK is true.
	// Reported so the operator can verify they're authed as who
	// they expect (especially under a CI-set GITHUB_TOKEN).
	Login string

	// Rate is the rate-limit snapshot the probe observed. IsSet()
	// is true after a successful call; surfaces in the renderer so
	// the operator sees their current budget without a second
	// command.
	Rate gh.RateLimitSnapshot

	// Issues and Warnings follow the adapter probe convention:
	// non-empty Issues mean the doctor returns a non-zero exit
	// code; Warnings are informational.
	Issues   []string
	Warnings []string
}

// probeGitHub runs the GitHub-side checks for `yottacode doctor`:
//
//  1. Walk the auth chain and report which tier (if any) supplied
//     a token. No-token short-circuits with an actionable hint.
//  2. Make one authenticated call (/user via go-github's
//     Users.Get(ctx, "")) to verify reachability + auth + capture
//     a rate-limit snapshot.
//  3. Classify failures: network errors → unreachable; auth
//     errors → AuthOK=false; everything else surfaces verbatim.
//
// Returns a populated GitHubProbeResult; never panics, never
// returns an error (the doctor's contract is "always render
// something").
func probeGitHub(ctx context.Context) GitHubProbeResult {
	res := GitHubProbeResult{}

	// Step 1: walk the auth chain in a probe-friendly way (no
	// process-wide sync.Once cache; the doctor may run multiple
	// times in one session).
	resolver := gh.NewTokenResolver()
	_, source, err := resolver.Resolve(ctx)
	if err != nil {
		if errors.Is(err, gh.ErrNoToken) {
			res.Issues = append(res.Issues, "no GitHub token configured (set $GITHUB_TOKEN, run `gh auth login`, or `yottacode setup github`)")
			return res
		}
		res.Issues = append(res.Issues, fmt.Sprintf("auth resolution failed: %s", err))
		return res
	}
	res.TokenSource = source

	// Step 2: make one authenticated call to verify the token.
	// We use a fresh TypedClient (not the cached one wired into
	// the agent loop) so the doctor reflects the *current* env
	// state, not whatever the loop saw at startup.
	cwd, _ := os.Getwd()
	client := gh.NewTypedClient(cwd)

	// 5s ceiling — the doctor should never hang on a slow network.
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	user, callErr := client.AuthedUserLogin(probeCtx)
	if callErr != nil {
		switch {
		case errors.Is(callErr, gh.ErrGitHubUnreachable):
			res.Reachable = false
			res.Issues = append(res.Issues, fmt.Sprintf("api.github.com unreachable: %s", strings.TrimPrefix(callErr.Error(), "github API unreachable: ")))
		case errors.Is(callErr, gh.ErrGhUnavailable):
			res.Reachable = true
			res.Issues = append(res.Issues, "token rejected by api.github.com (401) — token is invalid or expired")
		default:
			res.Reachable = true
			res.Issues = append(res.Issues, fmt.Sprintf("test call failed: %s", callErr))
		}
		res.Rate = client.RateLimit()
		return res
	}
	res.Reachable = true
	res.AuthOK = true
	res.Login = user
	res.Rate = client.RateLimit()

	if res.Rate.IsLow() {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("rate limit low: %d/%d remaining", res.Rate.Remaining, res.Rate.Limit))
	}
	return res
}

// renderGitHubProbe formats the result for the doctor's text
// output. Keeps the layout grep-friendly (key: value lines) so
// operators and scripts can scrape it the same way they scrape
// the provider probe section.
func renderGitHubProbe(r GitHubProbeResult) string {
	var b strings.Builder
	b.WriteString("\n--- github ---\n")
	if r.TokenSource == "" {
		b.WriteString("token: (none)\n")
	} else {
		fmt.Fprintf(&b, "token: present (source=%s)\n", r.TokenSource)
	}
	fmt.Fprintf(&b, "probe: reachable=%s auth=%s",
		yesNo(r.Reachable), yesNo(r.AuthOK))
	if r.Login != "" {
		fmt.Fprintf(&b, " user=%s", r.Login)
	}
	if r.Rate.IsSet() {
		fmt.Fprintf(&b, "\nrate: %d/%d remaining", r.Rate.Remaining, r.Rate.Limit)
		if !r.Rate.Reset.IsZero() {
			resetIn := time.Until(r.Rate.Reset).Round(time.Minute)
			if resetIn > 0 {
				fmt.Fprintf(&b, " (resets in %s)", resetIn)
			}
		}
	}
	if len(r.Issues) == 0 && len(r.Warnings) == 0 {
		b.WriteString("\ngithub: ok")
	} else {
		for _, issue := range r.Issues {
			fmt.Fprintf(&b, "\ngithub issue: %s", issue)
		}
		for _, warning := range r.Warnings {
			fmt.Fprintf(&b, "\ngithub warning: %s", warning)
		}
	}
	return b.String()
}
