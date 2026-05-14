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
// Scope: CreatePR + the read surface needed by /git-review-pr
// (ReadPR, ReadPRDiff, ListPRChecks). Issue reads, PR comments,
// and the v0.5.0 in-session cache / rate-limit awareness land with
// the typed go-github client.
package github

import (
	"context"
	"errors"
	"time"
)

// ErrGhUnavailable signals that the local environment cannot satisfy
// a GitHub call — either the `gh` binary isn't installed, or it is
// installed but unauthenticated. Callers branch on this so the
// procedural /create-pr can fall through to a draft-only preview
// instead of failing the turn opaquely.
var ErrGhUnavailable = errors.New("gh CLI unavailable or unauthenticated")

// ErrPRNotFound signals that the requested PR doesn't exist (no
// open PR for the supplied branch, or the explicit number resolves
// to a missing PR). Callers branch on this to surface a clean
// "no PR found" instead of treating the missing PR as an opaque
// gh exit-non-zero.
var ErrPRNotFound = errors.New("pull request not found")

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

	// ReadPR fetches typed metadata about a single pull request.
	// Ref accepts either a PR number ("17") or a branch name; gh
	// itself accepts both, and the v0.5.0 typed client will mirror
	// that ergonomics. Returns ErrPRNotFound when nothing matches.
	ReadPR(ctx context.Context, req ReadPRRequest) (PRDetails, error)

	// ReadPRDiff fetches the unified diff for a pull request as a
	// single string. Capped by the caller (tool wrapper trims for
	// model consumption); the Interface itself returns the full diff.
	ReadPRDiff(ctx context.Context, req ReadPRRequest) (string, error)

	// ListPRChecks returns the typed status of every check run on a
	// PR. Empty slice is a valid result (PR with no CI). The
	// `/git-review-pr` flow surfaces failing checks at the top of
	// the review, which is why typed access matters here.
	ListPRChecks(ctx context.Context, req ReadPRRequest) ([]CheckRun, error)

	// UpdatePR rewrites an existing PR's title and body. Used by
	// /git-update-pr after follow-up commits make the original
	// description stale. Other PR-level edits (labels, base,
	// reviewers, draft toggle) are intentionally out of scope —
	// the v0.5.0 spec defers them until a concrete workflow asks.
	// Returns ErrPRNotFound when nothing matches the ref;
	// ErrGhUnavailable when the local environment can't make the
	// call.
	UpdatePR(ctx context.Context, req UpdatePRRequest) (UpdatePRResult, error)
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

// ReadPRRequest is the typed payload for the read trio (ReadPR,
// ReadPRDiff, ListPRChecks). Owner / Repo follow the same
// optional-inference semantics as CreatePRRequest.
//
// Ref is the PR identifier — either a number ("17") or a branch
// name ("feature/x"). Empty Ref tells the implementation to use
// the cwd's current branch, mirroring `gh pr view` with no arg.
type ReadPRRequest struct {
	Owner string
	Repo  string
	Ref   string // PR number or branch name; "" = current branch
}

// PRDetails is the typed envelope ReadPR returns. Fields mirror
// the `gh pr view --json` schema yottacode needs today; growing
// it as callers ask for more fields keeps the contract surface
// pinned to actual use rather than front-loading the entire API.
//
// State and Mergeable are uppercase strings matching the GitHub
// API's enum literals (OPEN / CLOSED / MERGED, MERGEABLE /
// CONFLICTING / UNKNOWN) so downstream pattern-matching against
// the wire format stays unambiguous.
type PRDetails struct {
	Number    int
	Title     string
	Body      string
	State     string // "OPEN" | "CLOSED" | "MERGED"
	Draft     bool
	BaseRef   string
	HeadRef   string
	HeadSHA   string
	Mergeable string // "MERGEABLE" | "CONFLICTING" | "UNKNOWN"
	Author    string // login, not display name
	URL       string
	Labels    []string
}

// UpdatePRRequest is the typed payload for Interface.UpdatePR.
// Owner / Repo follow the same optional-inference semantics as
// the other request types. Ref accepts a PR number or branch
// name. Both Title and Body must be non-empty — empty Body would
// clobber the existing description, which is almost never what
// the caller wants and is easy to do by accident.
type UpdatePRRequest struct {
	Owner string
	Repo  string
	Ref   string
	Title string
	Body  string
}

// UpdatePRResult is the typed envelope UpdatePR returns on
// success. URL is the canonical PR URL (unchanged by an edit),
// surfaced so callers can re-link to the updated PR.
type UpdatePRResult struct {
	URL    string
	Number int
}

// CheckRun is one row from ListPRChecks. Name is the check's
// label (e.g. "build", "test", "lint"). State is the lifecycle
// state ("QUEUED" / "IN_PROGRESS" / "COMPLETED"). Conclusion is
// the outcome once State is COMPLETED ("SUCCESS" / "FAILURE" /
// "CANCELLED" / "NEUTRAL" / "SKIPPED" / "TIMED_OUT" /
// "ACTION_REQUIRED"); empty before completion.
//
// Times are zero-valued when the check hasn't started or
// completed yet — callers must IsZero-check before formatting.
type CheckRun struct {
	Name        string
	State       string
	Conclusion  string
	StartedAt   time.Time
	CompletedAt time.Time
}
