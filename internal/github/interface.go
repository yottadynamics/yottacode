// Package github is the typed adapter between yottacode and GitHub.
//
// Shipped surface (v0.5.0):
//
//   - Typed client (TypedClient) wrapping go-github/v66, with HTTPClient
//     injection for tests.
//   - Auth resolver chain: $GITHUB_TOKEN → `gh auth token` → ~/.yottacode/github.json.
//   - PR surface: CreatePR, ReadPR, ReadPRDiff, ListPRChecks, UpdatePR.
//   - Issue surface: ReadIssue, ListOpenIssues.
//   - PR comment surface: AddPRComment.
//
// Callers depend on the Interface, not on TypedClient directly, so
// the cloud bot (SaaS Phase 2) can swap in a JWT-installation-token
// implementation behind the same surface without changing call sites.
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

// ErrIssueNotFound signals that the requested issue doesn't exist.
// Sibling of ErrPRNotFound — same semantic, different resource.
// Callers branch on this so /git-implement-issue can surface a
// clean "no issue found" and STOP instead of treating the missing
// issue as an opaque API failure.
var ErrIssueNotFound = errors.New("issue not found")

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

	// ReadIssue fetches typed metadata about a single issue —
	// title, body, state, labels, assignees, and the most-recent
	// comments. Powers /git-implement-issue's first step. Returns
	// ErrIssueNotFound when the issue doesn't exist.
	ReadIssue(ctx context.Context, req ReadIssueRequest) (IssueDetails, error)

	// ListOpenIssues returns lightweight summaries of open issues
	// matching the supplied filters. Empty filters return all open
	// issues (paginated by GitHub's defaults — we don't fetch all
	// pages here; callers that need pagination ask for it).
	// Surfaces issues for /git-implement-issue tab-completion and
	// for future planning surfaces.
	ListOpenIssues(ctx context.Context, req ListIssuesRequest) ([]IssueSummary, error)

	// AddPRComment posts a top-level conversation comment on a PR.
	// Approval-gated by the tool layer (see GHPRAddCommentTool).
	// Used to cross-link related issues, request reviewers via
	// @-mention, or post structured follow-ups after a /git-review-pr
	// run. Not for inline review comments (different endpoint;
	// different scope).
	AddPRComment(ctx context.Context, req AddPRCommentRequest) (AddPRCommentResult, error)

	// RateLimit returns the most recent rate-limit snapshot the
	// implementation has observed. Snapshot.IsSet() is false when
	// no API call has populated the tracker yet. Used by the doctor
	// probe and by mutation tools to surface low-budget warnings.
	// Implementations that don't track rate state (test doubles)
	// may return a zero-valued snapshot.
	RateLimit() RateLimitSnapshot
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

// ReadIssueRequest is the typed payload for Interface.ReadIssue.
// Owner / Repo follow the same optional-inference semantics as
// the PR request types. Number is required (issues don't have a
// branch fallback the way PRs do).
//
// MaxComments caps the comment fetch — issues can be lengthy
// threads, and the model rarely needs every comment for context.
// Zero means "use the implementation's default" (currently 20
// most-recent comments). Negative means "skip comments entirely".
type ReadIssueRequest struct {
	Owner       string
	Repo        string
	Number      int
	MaxComments int
}

// IssueDetails is the typed envelope ReadIssue returns. Field
// shape mirrors PRDetails where it overlaps (Number, Title, Body,
// State, Author, URL, Labels) so the model brief and tool wrappers
// can render issues and PRs with shared template code.
//
// Assignees is the login list (not display names) — matches the
// Author convention. Comments is most-recent-first, capped per
// MaxComments. Empty Comments doesn't mean "no comments existed"
// when MaxComments was negative; check the request to disambiguate.
type IssueDetails struct {
	Number    int
	Title     string
	Body      string
	State     string // "OPEN" | "CLOSED"
	Author    string
	URL       string
	Labels    []string
	Assignees []string
	Comments  []IssueComment
}

// IssueComment is one row from IssueDetails.Comments. Body is the
// raw Markdown source the commenter wrote. Author is the login.
// Created is the UTC creation timestamp.
type IssueComment struct {
	Author  string
	Body    string
	Created time.Time
}

// ListIssuesRequest is the typed payload for Interface.ListOpenIssues.
// Owner / Repo follow the same optional-inference semantics as the
// other request types. Filters are AND-ed — e.g., Labels=["bug"]
// and Assignee="octocat" returns only issues with both.
//
// Empty Labels means "no label filter" (any label OK, no label OK).
// Empty Assignee / Milestone means "no filter on that axis".
// State is fixed to "open" by the method semantics — callers that
// need closed issues use ReadIssue with a specific number.
type ListIssuesRequest struct {
	Owner     string
	Repo      string
	Labels    []string
	Assignee  string
	Milestone string
}

// IssueSummary is the lightweight envelope ListOpenIssues returns.
// Subset of IssueDetails — enough for a picker / completion list.
// Callers that need bodies + comments follow up with ReadIssue.
type IssueSummary struct {
	Number    int
	Title     string
	Author    string
	URL       string
	Labels    []string
	Assignees []string
}

// AddPRCommentRequest is the typed payload for Interface.AddPRComment.
// Owner / Repo follow the same optional-inference semantics. Ref
// accepts a PR number or branch name, mirroring ReadPRRequest.
// Body is the Markdown source — required, non-empty.
type AddPRCommentRequest struct {
	Owner string
	Repo  string
	Ref   string
	Body  string
}

// AddPRCommentResult is the typed envelope AddPRComment returns
// on success. URL is the comment's permalink — callers surface
// it so the user can jump to the posted comment on GitHub. ID is
// the comment's numeric ID, useful for future edit/delete flows
// (out of scope for v0.5.0 but exposed now to avoid a breaking
// change later).
type AddPRCommentResult struct {
	URL string
	ID  int64
}
