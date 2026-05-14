package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"

	gogithub "github.com/google/go-github/v66/github"
)

// TypedClient is the Interface implementation backed by the
// go-github typed REST client. Replaces the gh-CLI ShellOut
// implementation we shipped first.
//
// Three reliability wins over ShellOut:
//
//  1. **No gh CLI dependency.** The auth resolver still
//     opportunistically piggybacks on `gh auth token` if it's
//     available (one-shot at first use), but API calls go
//     direct over HTTPS. Users can `apt remove gh` and
//     yottacode still works if $GITHUB_TOKEN is set or the
//     `~/.yottacode/github.json` file exists.
//  2. **No GraphQL field deprecation traps.** ShellOut hit
//     `repository.pullRequest.projectCards` deprecation through
//     gh's internal query. The REST endpoints we use here
//     don't reference projectCards.
//  3. **Faster per call.** No subprocess spawn; in-process HTTP.
//
// Cwd is used for inferring (owner, repo) when the caller's
// request doesn't supply them explicitly. Resolver and the
// underlying *gogithub.Client are lazy-initialized via sync.Once
// so the first call pays the auth cost and subsequent calls
// reuse the client.
type TypedClient struct {
	Cwd      string
	Resolver *TokenResolver

	once   sync.Once
	client *gogithub.Client
	initErr error

	// HTTPClient is an optional injection point for tests. When
	// nil, the typed client uses go-github's default
	// http.Client. Tests provide a transport that returns
	// canned responses.
	HTTPClient *http.Client
}

// NewTypedClient builds a TypedClient wired with a fresh
// TokenResolver. Callers can also construct the struct directly
// (e.g. tests injecting an HTTPClient).
func NewTypedClient(cwd string) *TypedClient {
	return &TypedClient{
		Cwd:      cwd,
		Resolver: NewTokenResolver(),
	}
}

// init returns the cached *gogithub.Client, building it on the
// first call. Auth resolution failures cache as initErr so
// subsequent calls fail fast without re-walking the chain.
func (c *TypedClient) init(ctx context.Context) (*gogithub.Client, error) {
	if c.Resolver == nil {
		c.Resolver = NewTokenResolver()
	}
	c.once.Do(func() {
		token, _, err := c.Resolver.Resolve(ctx)
		if err != nil {
			if errors.Is(err, ErrNoToken) {
				c.initErr = ErrGhUnavailable
				return
			}
			c.initErr = fmt.Errorf("resolve auth: %w", err)
			return
		}
		base := c.HTTPClient
		if base == nil {
			c.client = gogithub.NewClient(nil).WithAuthToken(token)
		} else {
			c.client = gogithub.NewClient(base).WithAuthToken(token)
		}
	})
	return c.client, c.initErr
}

// resolveOwnerRepo returns the (owner, repo) to use for a
// request. Caller-provided fields win; otherwise we detect
// from cwd. Returns an error when both paths fail.
func (c *TypedClient) resolveOwnerRepo(ctx context.Context, owner, repo string) (string, string, error) {
	if owner != "" && repo != "" {
		return owner, repo, nil
	}
	detectedOwner, detectedRepo, err := DetectRepo(ctx, c.Cwd)
	if err != nil {
		return "", "", err
	}
	return detectedOwner, detectedRepo, nil
}

// classifyAPIError maps go-github's response errors into our
// typed sentinels. 404 → ErrPRNotFound. Auth failures
// (401 / 403 from missing or invalid token) → ErrGhUnavailable
// for caller-side parity with the ShellOut behavior. Everything
// else bubbles up wrapped.
func classifyAPIError(err error) error {
	var apiErr *gogithub.ErrorResponse
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		switch apiErr.Response.StatusCode {
		case http.StatusNotFound:
			return ErrPRNotFound
		case http.StatusUnauthorized:
			return ErrGhUnavailable
		}
	}
	return err
}

// CreatePR opens a pull request via REST. Body is sent as the
// JSON `body` field directly — no shell quoting concerns.
func (c *TypedClient) CreatePR(ctx context.Context, req CreatePRRequest) (CreatePRResult, error) {
	var res CreatePRResult
	cl, err := c.init(ctx)
	if err != nil {
		return res, err
	}
	owner, repo, err := c.resolveOwnerRepo(ctx, req.Owner, req.Repo)
	if err != nil {
		return res, err
	}
	head := req.Head
	if strings.TrimSpace(head) == "" {
		// Default to the current branch. go-github needs the
		// head explicitly when invoking from a non-default
		// branch context; "" would 422.
		current, derr := currentBranch(ctx, c.Cwd)
		if derr != nil {
			return res, fmt.Errorf("create pr: detect head branch: %w", derr)
		}
		head = current
	}

	newPR := &gogithub.NewPullRequest{
		Title: gogithub.String(req.Title),
		Body:  gogithub.String(req.Body),
		Base:  gogithub.String(req.Base),
		Head:  gogithub.String(head),
		Draft: gogithub.Bool(req.Draft),
	}
	created, _, err := cl.PullRequests.Create(ctx, owner, repo, newPR)
	if err != nil {
		return res, fmt.Errorf("create pr: %w", classifyAPIError(err))
	}
	if created.HTMLURL != nil {
		res.URL = *created.HTMLURL
	}
	if created.Number != nil {
		res.Number = *created.Number
	}
	return res, nil
}

// ReadPR fetches PR metadata. Ref accepts a PR number string
// or a branch name. Number form goes direct; branch form lists
// PRs filtered by head and picks the first open match.
func (c *TypedClient) ReadPR(ctx context.Context, req ReadPRRequest) (PRDetails, error) {
	var details PRDetails
	cl, err := c.init(ctx)
	if err != nil {
		return details, err
	}
	owner, repo, err := c.resolveOwnerRepo(ctx, req.Owner, req.Repo)
	if err != nil {
		return details, err
	}
	number, err := c.resolvePRNumber(ctx, cl, owner, repo, req.Ref)
	if err != nil {
		return details, err
	}
	pr, _, err := cl.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return details, fmt.Errorf("read pr: %w", classifyAPIError(err))
	}
	return mapPRDetails(pr), nil
}

// ReadPRDiff fetches the unified diff. go-github exposes raw
// media-type access via PullRequests.GetRaw with
// RawOptions{Type: Diff}.
func (c *TypedClient) ReadPRDiff(ctx context.Context, req ReadPRRequest) (string, error) {
	cl, err := c.init(ctx)
	if err != nil {
		return "", err
	}
	owner, repo, err := c.resolveOwnerRepo(ctx, req.Owner, req.Repo)
	if err != nil {
		return "", err
	}
	number, err := c.resolvePRNumber(ctx, cl, owner, repo, req.Ref)
	if err != nil {
		return "", err
	}
	diff, _, err := cl.PullRequests.GetRaw(ctx, owner, repo, number,
		gogithub.RawOptions{Type: gogithub.Diff})
	if err != nil {
		return "", fmt.Errorf("read pr diff: %w", classifyAPIError(err))
	}
	return diff, nil
}

// ListPRChecks combines check-run rollup (the newer Checks API)
// and legacy status contexts into a single typed list. gh's
// statusCheckRollup field on the GraphQL side fuses both; the
// REST API exposes them separately so we merge here.
func (c *TypedClient) ListPRChecks(ctx context.Context, req ReadPRRequest) ([]CheckRun, error) {
	cl, err := c.init(ctx)
	if err != nil {
		return nil, err
	}
	owner, repo, err := c.resolveOwnerRepo(ctx, req.Owner, req.Repo)
	if err != nil {
		return nil, err
	}
	number, err := c.resolvePRNumber(ctx, cl, owner, repo, req.Ref)
	if err != nil {
		return nil, err
	}
	pr, _, err := cl.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("list checks: read pr: %w", classifyAPIError(err))
	}
	if pr.Head == nil || pr.Head.SHA == nil {
		return nil, fmt.Errorf("list checks: PR head SHA missing")
	}
	headSHA := *pr.Head.SHA

	out := []CheckRun{}

	// Check-run rollup — the newer Checks API surface.
	checks, _, cerr := cl.Checks.ListCheckRunsForRef(ctx, owner, repo, headSHA, nil)
	if cerr != nil {
		return nil, fmt.Errorf("list checks: %w", classifyAPIError(cerr))
	}
	if checks != nil {
		for _, cr := range checks.CheckRuns {
			out = append(out, mapCheckRun(cr))
		}
	}

	// Legacy commit-status contexts — what statusCheckRollup's
	// StatusContext branch covers. ListStatuses returns the
	// latest status for each context.
	statuses, _, serr := cl.Repositories.ListStatuses(ctx, owner, repo, headSHA, nil)
	if serr != nil {
		// Status fetch is soft-fail: check-run rollup alone is
		// the common case, statuses are a legacy supplement.
		return out, nil
	}
	seen := map[string]bool{}
	for _, st := range statuses {
		if st.Context == nil || seen[*st.Context] {
			// ListStatuses returns history per context; we
			// only want the most-recent (first occurrence).
			continue
		}
		seen[*st.Context] = true
		out = append(out, mapStatus(st))
	}
	return out, nil
}

// UpdatePR rewrites an existing PR's title and body. Other
// fields stay locked — the v1 scope is title + body only.
func (c *TypedClient) UpdatePR(ctx context.Context, req UpdatePRRequest) (UpdatePRResult, error) {
	var res UpdatePRResult
	cl, err := c.init(ctx)
	if err != nil {
		return res, err
	}
	owner, repo, err := c.resolveOwnerRepo(ctx, req.Owner, req.Repo)
	if err != nil {
		return res, err
	}
	number, err := c.resolvePRNumber(ctx, cl, owner, repo, req.Ref)
	if err != nil {
		return res, err
	}
	patch := &gogithub.PullRequest{
		Title: gogithub.String(req.Title),
		Body:  gogithub.String(req.Body),
	}
	updated, _, err := cl.PullRequests.Edit(ctx, owner, repo, number, patch)
	if err != nil {
		return res, fmt.Errorf("update pr: %w", classifyAPIError(err))
	}
	if updated.HTMLURL != nil {
		res.URL = *updated.HTMLURL
	}
	if updated.Number != nil {
		res.Number = *updated.Number
	}
	return res, nil
}

// resolvePRNumber turns Ref (number or branch name) into a PR
// number int. Numeric refs short-circuit without an API call;
// branch refs trigger PullRequests.List filtered by head and
// pick the first open match. Returns ErrPRNotFound when nothing
// matches.
func (c *TypedClient) resolvePRNumber(ctx context.Context, cl *gogithub.Client, owner, repo, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		// "" defaults to the current branch — same ergonomics
		// as ShellOut's gh pr view with no arg.
		current, err := currentBranch(ctx, c.Cwd)
		if err != nil {
			return 0, fmt.Errorf("resolve pr number: detect current branch: %w", err)
		}
		ref = current
	}
	if n, err := parseInt(ref); err == nil && n > 0 {
		return n, nil
	}
	// Branch form: list open PRs filtered by head=<owner>:<branch>.
	opts := &gogithub.PullRequestListOptions{
		State: "open",
		Head:  owner + ":" + ref,
	}
	prs, _, err := cl.PullRequests.List(ctx, owner, repo, opts)
	if err != nil {
		return 0, fmt.Errorf("resolve pr number: list prs: %w", classifyAPIError(err))
	}
	if len(prs) == 0 {
		return 0, ErrPRNotFound
	}
	if prs[0].Number == nil {
		return 0, fmt.Errorf("resolve pr number: PR list entry missing number")
	}
	return *prs[0].Number, nil
}

// parseInt is a small wrapper that only succeeds on
// purely-numeric strings — guards against "17abc" parsing as
// "17" when the caller meant a branch.
func parseInt(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-numeric")
		}
	}
	var n int
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// currentBranch is the typed-client sibling of the helper that
// lives in the agent package's git push tool. Duplicated here
// because internal/github can't import internal/agent (cycle).
// Returns the literal "HEAD" check is upstream's responsibility;
// here we just return whatever `git rev-parse` gave us.
func currentBranch(ctx context.Context, cwd string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// mapPRDetails converts go-github's *PullRequest into our
// PRDetails. Pointer dereferences are guarded so a partially
// populated response doesn't panic.
func mapPRDetails(pr *gogithub.PullRequest) PRDetails {
	var d PRDetails
	if pr == nil {
		return d
	}
	if pr.Number != nil {
		d.Number = *pr.Number
	}
	if pr.Title != nil {
		d.Title = *pr.Title
	}
	if pr.Body != nil {
		d.Body = *pr.Body
	}
	if pr.State != nil {
		d.State = strings.ToUpper(*pr.State)
	}
	if pr.Draft != nil {
		d.Draft = *pr.Draft
	}
	if pr.Base != nil && pr.Base.Ref != nil {
		d.BaseRef = *pr.Base.Ref
	}
	if pr.Head != nil && pr.Head.Ref != nil {
		d.HeadRef = *pr.Head.Ref
	}
	if pr.Head != nil && pr.Head.SHA != nil {
		d.HeadSHA = *pr.Head.SHA
	}
	if pr.MergeableState != nil {
		d.Mergeable = strings.ToUpper(*pr.MergeableState)
	}
	if pr.User != nil && pr.User.Login != nil {
		d.Author = *pr.User.Login
	}
	if pr.HTMLURL != nil {
		d.URL = *pr.HTMLURL
	}
	for _, l := range pr.Labels {
		if l != nil && l.Name != nil && *l.Name != "" {
			d.Labels = append(d.Labels, *l.Name)
		}
	}
	return d
}

// mapCheckRun converts a go-github CheckRun to ours. The Checks
// API uses lifecycle ("queued" / "in_progress" / "completed")
// for Status and outcome ("success" / "failure" / ...) for
// Conclusion. We uppercase to match the State field semantics
// the existing tests pin.
func mapCheckRun(cr *gogithub.CheckRun) CheckRun {
	var c CheckRun
	if cr == nil {
		return c
	}
	if cr.Name != nil {
		c.Name = *cr.Name
	}
	if cr.Status != nil {
		c.State = strings.ToUpper(*cr.Status)
	}
	if cr.Conclusion != nil {
		c.Conclusion = strings.ToUpper(*cr.Conclusion)
	}
	if cr.StartedAt != nil {
		c.StartedAt = cr.StartedAt.Time
	}
	if cr.CompletedAt != nil {
		c.CompletedAt = cr.CompletedAt.Time
	}
	return c
}

// mapStatus converts a legacy commit-status context entry into
// our CheckRun shape. Status contexts use "pending" / "success"
// / "failure" / "error" — different vocabulary than check-runs.
// We surface them under State so the existing failing-check
// classifier (isFailingCheck in agent) handles both.
func mapStatus(st *gogithub.RepoStatus) CheckRun {
	var c CheckRun
	if st == nil {
		return c
	}
	if st.Context != nil {
		c.Name = *st.Context
	}
	if st.State != nil {
		c.State = strings.ToUpper(*st.State)
	}
	// Status contexts don't have a separate Conclusion field;
	// State carries the outcome directly.
	if st.CreatedAt != nil {
		c.StartedAt = st.CreatedAt.Time
	}
	if st.UpdatedAt != nil {
		c.CompletedAt = st.UpdatedAt.Time
	}
	return c
}
