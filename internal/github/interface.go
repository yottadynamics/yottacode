// Package github is the typed adapter between yottacode and GitHub.
//
// The v0.5.0 roadmap (yottacode-roadmap/github-integration.md) calls
// for a typed `go-github` client with auth precedence, in-session
// caching, and rate-limit surfacing. That work is intentionally not
// done here. What this package does ship is the Interface shape the
// rest of the codebase consumes, plus a thin `gh` shell-out
// implementation so procedural callers (the /create-pr slash
// command, the gh_pr_workflow composite tool) can depend on a typed
// surface from day one. When the v0.5.0 client lands, callers
// continue working unchanged — only the registration of which
// implementation to inject moves.
//
// Scope deliberately narrow: CreatePR only. Read operations
// (ReadIssue, ReadPR, ListPRChecks) and other writes (AddPRComment)
// land with the typed client, where caching and per-endpoint rate
// awareness actually matter.
package github

import (
	"context"
	"errors"
)

// ErrGhUnavailable signals that the local environment cannot satisfy
// a GitHub call — either the `gh` binary isn't installed, or it is
// installed but unauthenticated. Callers branch on this so the
// procedural /create-pr can fall through to a draft-only preview
// instead of failing the turn opaquely.
var ErrGhUnavailable = errors.New("gh CLI unavailable or unauthenticated")

// Interface is the typed surface yottacode uses to talk to GitHub.
// Kept minimal: only the methods at least one shipped caller needs.
// Growing it as new commands need new endpoints (rather than
// front-loading the entire `go-github` surface) keeps the test
// burden bounded — each method we add ships with at least one
// caller that exercises it.
type Interface interface {
	// CreatePR opens a pull request. Returns ErrGhUnavailable when
	// the local environment can't make the call (no gh, no auth)
	// so callers can fall back gracefully rather than reporting a
	// generic execution failure.
	CreatePR(ctx context.Context, req CreatePRRequest) (CreatePRResult, error)
}

// CreatePRRequest is the typed payload for Interface.CreatePR.
//
// Owner and Repo are optional: when both are empty, the underlying
// implementation infers them from the working directory's git
// remote (the gh CLI's default behavior). Setting them explicitly
// is what the future cloud bot will need (it can't rely on cwd) and
// the local CLI can always use it for cross-repo cases.
type CreatePRRequest struct {
	Owner string // repo owner (optional; inferred from cwd when empty)
	Repo  string // repo name  (optional; inferred from cwd when empty)
	Base  string // base branch the PR merges into (required)
	Head  string // head branch / SHA the PR ships (optional; "" = current branch)
	Title string // PR title (required)
	Body  string // PR body / description (required)
	Draft bool   // open as draft (default: open as ready-for-review)
}

// CreatePRResult is the typed envelope CreatePR returns on success.
// Number is the GitHub PR number (best-effort: shellout impl returns
// 0 when it can't parse one; typed v0.5.0 impl will always populate
// it). URL is the canonical https://github.com/... PR URL.
type CreatePRResult struct {
	URL    string
	Number int
}
