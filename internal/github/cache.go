package github

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// CachingClient is a memoizing wrapper around any Interface. Reads
// are cached for the lifetime of the wrapper (which the runtime
// holds for the session). Writes pass through and invalidate
// matching read entries so the next read sees fresh state.
//
// Cache shape is deliberately simple: per-method maps keyed by
// request fingerprints. No TTL — the runtime tears the wrapper
// down at session end, which is the only invalidation event the
// roadmap commits to.
//
// Thread-safe via a single mutex. Sessions can have parallel tool
// calls in flight (the agent runs read tools concurrently when
// they're parallel-safe), so locking matters. The mutex is held
// only across map mutation, not across the Inner call — a cache
// miss releases the lock, makes the call, then re-acquires to
// store. Two concurrent misses on the same key make two calls;
// that's the cost of not holding the lock across the network. The
// double-fetch is rare in practice (the agent dispatches reads
// once per turn) and avoiding it would require per-key
// singleflight, which is more machinery than the current signal
// warrants.
type CachingClient struct {
	Inner Interface

	mu         sync.Mutex
	prs        map[string]PRDetails
	prDiffs    map[string]string
	checks     map[string][]CheckRun
	issues     map[string]IssueDetails
	issueLists map[string][]IssueSummary
}

// NewCachingClient wraps the supplied Interface with read caches.
// Returns a *CachingClient (not Interface) so wiring sites can
// reach the Stats / Reset methods for debugging without a type
// assertion. The struct satisfies Interface.
func NewCachingClient(inner Interface) *CachingClient {
	return &CachingClient{
		Inner:      inner,
		prs:        map[string]PRDetails{},
		prDiffs:    map[string]string{},
		checks:     map[string][]CheckRun{},
		issues:     map[string]IssueDetails{},
		issueLists: map[string][]IssueSummary{},
	}
}

// Stats reports per-method cache occupancy. Useful for debugging
// and for the eventual doctor probe — we want to see whether the
// cache is actually hot during a session.
type CacheStats struct {
	PRs        int
	PRDiffs    int
	Checks     int
	Issues     int
	IssueLists int
}

func (c *CachingClient) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{
		PRs:        len(c.prs),
		PRDiffs:    len(c.prDiffs),
		Checks:     len(c.checks),
		Issues:     len(c.issues),
		IssueLists: len(c.issueLists),
	}
}

// Reset drops every cached entry. Not used during normal session
// lifecycle (the cache lives for the session); exposed for tests
// and for a future "force fresh fetch" debug command.
func (c *CachingClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prs = map[string]PRDetails{}
	c.prDiffs = map[string]string{}
	c.checks = map[string][]CheckRun{}
	c.issues = map[string]IssueDetails{}
	c.issueLists = map[string][]IssueSummary{}
}

// prKey fingerprints a PR-shaped request. The Ref-as-given is the
// key, not a resolved (owner/repo/number) — see the type-level
// doc for the rationale.
func prKey(owner, repo, ref string) string {
	return owner + "/" + repo + "#" + ref
}

func issueKey(owner, repo string, number, maxComments int) string {
	// maxComments is part of the key because two reads with
	// different caps produce different result shapes — caching
	// the smaller result would shortchange the larger request.
	return owner + "/" + repo + "#" + itoa(number) + "@" + itoa(maxComments)
}

func issueListKey(req ListIssuesRequest) string {
	// Labels are sorted before hashing so the same filter
	// supplied in different orders hits the same cache entry.
	labels := append([]string(nil), req.Labels...)
	sort.Strings(labels)
	var b strings.Builder
	b.WriteString(req.Owner)
	b.WriteString("/")
	b.WriteString(req.Repo)
	b.WriteString("?labels=")
	b.WriteString(strings.Join(labels, ","))
	b.WriteString("&assignee=")
	b.WriteString(req.Assignee)
	b.WriteString("&milestone=")
	b.WriteString(req.Milestone)
	return b.String()
}

// itoa avoids pulling strconv for a single-purpose helper used only
// here. Tiny but real cost saved.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// CreatePR is a write — passes through and invalidates nothing.
// A newly opened PR didn't exist before so there's no stale entry
// to evict. The PR's URL / number become known after this call,
// but the next ReadPR will populate the cache fresh.
func (c *CachingClient) CreatePR(ctx context.Context, req CreatePRRequest) (CreatePRResult, error) {
	return c.Inner.CreatePR(ctx, req)
}

// ReadPR is cached by (owner, repo, ref). Cache miss falls through
// to Inner; success populates the entry. Errors (including the
// typed sentinels) are NOT cached — re-fetching after a transient
// failure is the right behavior, and caching ErrPRNotFound would
// make subsequent retries impossible without a Reset.
func (c *CachingClient) ReadPR(ctx context.Context, req ReadPRRequest) (PRDetails, error) {
	key := prKey(req.Owner, req.Repo, req.Ref)

	c.mu.Lock()
	if hit, ok := c.prs[key]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	res, err := c.Inner.ReadPR(ctx, req)
	if err != nil {
		return res, err
	}
	c.mu.Lock()
	c.prs[key] = res
	c.mu.Unlock()
	return res, nil
}

// ReadPRDiff is cached by the same key shape as ReadPR. Diffs
// don't change without a new commit, and the cache lives only for
// the session — so a long diff fetched once carries through any
// follow-up reads in the same turn.
func (c *CachingClient) ReadPRDiff(ctx context.Context, req ReadPRRequest) (string, error) {
	key := prKey(req.Owner, req.Repo, req.Ref)

	c.mu.Lock()
	if hit, ok := c.prDiffs[key]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	res, err := c.Inner.ReadPRDiff(ctx, req)
	if err != nil {
		return res, err
	}
	c.mu.Lock()
	c.prDiffs[key] = res
	c.mu.Unlock()
	return res, nil
}

// ListPRChecks is cached. Like the diff, checks lag head SHA
// updates, but within a single session turn the cache hit is
// always correct.
func (c *CachingClient) ListPRChecks(ctx context.Context, req ReadPRRequest) ([]CheckRun, error) {
	key := prKey(req.Owner, req.Repo, req.Ref)

	c.mu.Lock()
	if hit, ok := c.checks[key]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	res, err := c.Inner.ListPRChecks(ctx, req)
	if err != nil {
		return res, err
	}
	c.mu.Lock()
	c.checks[key] = res
	c.mu.Unlock()
	return res, nil
}

// UpdatePR rewrites title/body — the next ReadPR must see fresh
// data, so we evict the matching ReadPR entry before passing
// through. Diff and checks are unaffected (the head SHA hasn't
// moved), so those caches stay.
func (c *CachingClient) UpdatePR(ctx context.Context, req UpdatePRRequest) (UpdatePRResult, error) {
	res, err := c.Inner.UpdatePR(ctx, req)
	if err != nil {
		return res, err
	}
	c.mu.Lock()
	delete(c.prs, prKey(req.Owner, req.Repo, req.Ref))
	c.mu.Unlock()
	return res, nil
}

// ReadIssue is cached by (owner, repo, number, maxComments).
// Comments are part of the cached value, so the maxComments
// element of the key matters (different caps produce different
// result shapes).
func (c *CachingClient) ReadIssue(ctx context.Context, req ReadIssueRequest) (IssueDetails, error) {
	key := issueKey(req.Owner, req.Repo, req.Number, req.MaxComments)

	c.mu.Lock()
	if hit, ok := c.issues[key]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	res, err := c.Inner.ReadIssue(ctx, req)
	if err != nil {
		return res, err
	}
	c.mu.Lock()
	c.issues[key] = res
	c.mu.Unlock()
	return res, nil
}

// ListOpenIssues is cached by the full filter fingerprint —
// labels (order-independent), assignee, milestone, owner/repo.
// Two model calls with the same filter share one API request.
func (c *CachingClient) ListOpenIssues(ctx context.Context, req ListIssuesRequest) ([]IssueSummary, error) {
	key := issueListKey(req)

	c.mu.Lock()
	if hit, ok := c.issueLists[key]; ok {
		c.mu.Unlock()
		return hit, nil
	}
	c.mu.Unlock()

	res, err := c.Inner.ListOpenIssues(ctx, req)
	if err != nil {
		return res, err
	}
	c.mu.Lock()
	c.issueLists[key] = res
	c.mu.Unlock()
	return res, nil
}

// RateLimit passes through to the inner. Cache layer has no
// independent rate state — the inner is what makes the API calls.
func (c *CachingClient) RateLimit() RateLimitSnapshot {
	return c.Inner.RateLimit()
}

// AddPRComment is a write — passes through. Doesn't invalidate
// any cached read: comments aren't carried in PRDetails (only the
// PR body is), so the next ReadPR is still accurate. If a future
// PRDetails grows a Comments field, this method must learn to
// evict matching ReadPR entries; the test suite pins that
// expectation so the regression is caught.
func (c *CachingClient) AddPRComment(ctx context.Context, req AddPRCommentRequest) (AddPRCommentResult, error) {
	return c.Inner.AddPRComment(ctx, req)
}
