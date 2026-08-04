package github

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// countingInner is a minimal Interface impl that counts every
// method call. Tests use it to verify cache hits (call count stays
// flat across duplicate read requests) and write pass-through
// (call count increments).
type countingInner struct {
	mu sync.Mutex

	readPRCount      int
	readPRDiffCount  int
	listChecksCount  int
	updatePRCount    int
	createPRCount    int
	readIssueCount   int
	listIssuesCount  int
	addCommentCount  int
	createIssueCount int

	prRes          PRDetails
	prErr          error
	diffRes        string
	diffErr        error
	checksRes      []CheckRun
	checksErr      error
	updatePRRes    UpdatePRResult
	createPRRes    CreatePRResult
	issueRes       IssueDetails
	issueErr       error
	issuesRes      []IssueSummary
	issuesErr      error
	commentRes     AddPRCommentResult
	createIssueRes CreateIssueResult
}

func (c *countingInner) ReadPR(_ context.Context, req ReadPRRequest) (PRDetails, error) {
	c.mu.Lock()
	c.readPRCount++
	c.mu.Unlock()
	return c.prRes, c.prErr
}

func (c *countingInner) ReadPRDiff(_ context.Context, req ReadPRRequest) (string, error) {
	c.mu.Lock()
	c.readPRDiffCount++
	c.mu.Unlock()
	return c.diffRes, c.diffErr
}

func (c *countingInner) ListPRChecks(_ context.Context, req ReadPRRequest) ([]CheckRun, error) {
	c.mu.Lock()
	c.listChecksCount++
	c.mu.Unlock()
	return c.checksRes, c.checksErr
}

func (c *countingInner) ListFailedWorkflowJobLogTails(_ context.Context, req FailedWorkflowLogsRequest) (FailedWorkflowLogsResult, error) {
	return FailedWorkflowLogsResult{}, nil
}

func (c *countingInner) UpdatePR(_ context.Context, req UpdatePRRequest) (UpdatePRResult, error) {
	c.mu.Lock()
	c.updatePRCount++
	c.mu.Unlock()
	return c.updatePRRes, nil
}

func (c *countingInner) CreatePR(_ context.Context, req CreatePRRequest) (CreatePRResult, error) {
	c.mu.Lock()
	c.createPRCount++
	c.mu.Unlock()
	return c.createPRRes, nil
}

func (c *countingInner) ReadIssue(_ context.Context, req ReadIssueRequest) (IssueDetails, error) {
	c.mu.Lock()
	c.readIssueCount++
	c.mu.Unlock()
	return c.issueRes, c.issueErr
}

func (c *countingInner) ListOpenIssues(_ context.Context, req ListIssuesRequest) ([]IssueSummary, error) {
	c.mu.Lock()
	c.listIssuesCount++
	c.mu.Unlock()
	return c.issuesRes, c.issuesErr
}

func (c *countingInner) AddPRComment(_ context.Context, req AddPRCommentRequest) (AddPRCommentResult, error) {
	c.mu.Lock()
	c.addCommentCount++
	c.mu.Unlock()
	return c.commentRes, nil
}

func (c *countingInner) CreateIssue(_ context.Context, req CreateIssueRequest) (CreateIssueResult, error) {
	c.mu.Lock()
	c.createIssueCount++
	c.mu.Unlock()
	return c.createIssueRes, nil
}

func (c *countingInner) RateLimit() RateLimitSnapshot {
	// Test double — no rate state to track. Returns zero so callers
	// branching on IsSet() see "unset" and skip rate-related logic.
	return RateLimitSnapshot{}
}

// TestCachingClient_DuplicateReadPR_OneCall pins the cache's
// load-bearing behavior: two identical ReadPR calls within one
// session hit the API exactly once. If this test fails the cache
// is doing nothing.
func TestCachingClient_DuplicateReadPR_OneCall(t *testing.T) {
	inner := &countingInner{prRes: PRDetails{Number: 29}}
	c := NewCachingClient(inner)
	for i := 0; i < 3; i++ {
		got, err := c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
		if err != nil {
			t.Fatalf("ReadPR: %v", err)
		}
		if got.Number != 29 {
			t.Errorf("call %d: PR = %+v", i, got)
		}
	}
	if inner.readPRCount != 1 {
		t.Errorf("expected 1 inner call; got %d", inner.readPRCount)
	}
}

func TestCachingClient_DuplicateReadPRDiff_OneCall(t *testing.T) {
	inner := &countingInner{diffRes: "diff content"}
	c := NewCachingClient(inner)
	for i := 0; i < 3; i++ {
		_, _ = c.ReadPRDiff(context.Background(), ReadPRRequest{Ref: "29"})
	}
	if inner.readPRDiffCount != 1 {
		t.Errorf("expected 1 inner call; got %d", inner.readPRDiffCount)
	}
}

func TestCachingClient_ListPRChecksEveryCall(t *testing.T) {
	inner := &countingInner{checksRes: []CheckRun{{Name: "ci"}}}
	c := NewCachingClient(inner)
	for i := 0; i < 3; i++ {
		_, _ = c.ListPRChecks(context.Background(), ReadPRRequest{Ref: "29"})
	}
	if inner.listChecksCount != 3 {
		t.Errorf("checks must be fetched live every time; got %d calls", inner.listChecksCount)
	}
}

func TestCachingClient_ListPRChecksPendingThenSuccess(t *testing.T) {
	// CI checks can complete while the PR head SHA is unchanged. The cache layer
	// must not pin the earlier pending result for the rest of the session.
	inner := &countingInner{checksRes: []CheckRun{{Name: "ci", State: "IN_PROGRESS"}}}
	c := NewCachingClient(inner)
	first, _ := c.ListPRChecks(context.Background(), ReadPRRequest{Ref: "29"})
	if got := first[0].State; got != "IN_PROGRESS" {
		t.Fatalf("first check state = %q, want IN_PROGRESS", got)
	}

	inner.checksRes = []CheckRun{{Name: "ci", State: "COMPLETED", Conclusion: "SUCCESS"}}
	second, _ := c.ListPRChecks(context.Background(), ReadPRRequest{Ref: "29"})
	if got := second[0].Conclusion; got != "SUCCESS" {
		t.Fatalf("second check conclusion = %q, want SUCCESS", got)
	}
	if inner.listChecksCount != 2 {
		t.Errorf("expected two live check fetches; got %d", inner.listChecksCount)
	}
}

func TestCachingClient_DuplicateReadIssue_OneCall(t *testing.T) {
	inner := &countingInner{issueRes: IssueDetails{Number: 42}}
	c := NewCachingClient(inner)
	for i := 0; i < 3; i++ {
		_, _ = c.ReadIssue(context.Background(), ReadIssueRequest{Number: 42})
	}
	if inner.readIssueCount != 1 {
		t.Errorf("expected 1 inner call; got %d", inner.readIssueCount)
	}
}

func TestCachingClient_DuplicateListOpenIssues_OneCall(t *testing.T) {
	inner := &countingInner{issuesRes: []IssueSummary{{Number: 1}}}
	c := NewCachingClient(inner)
	for i := 0; i < 3; i++ {
		_, _ = c.ListOpenIssues(context.Background(), ListIssuesRequest{Labels: []string{"bug"}})
	}
	if inner.listIssuesCount != 1 {
		t.Errorf("expected 1 inner call; got %d", inner.listIssuesCount)
	}
}

func TestCachingClient_DifferentRefs_DifferentEntries(t *testing.T) {
	inner := &countingInner{prRes: PRDetails{Number: 29}}
	c := NewCachingClient(inner)
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "30"})
	if inner.readPRCount != 2 {
		t.Errorf("different refs should miss cache separately; got %d calls", inner.readPRCount)
	}
}

func TestCachingClient_MaxCommentsPartOfKey(t *testing.T) {
	// Two reads with different MaxComments must NOT share a cache
	// entry — they return different shapes (different comment count).
	inner := &countingInner{issueRes: IssueDetails{Number: 42}}
	c := NewCachingClient(inner)
	_, _ = c.ReadIssue(context.Background(), ReadIssueRequest{Number: 42, MaxComments: 5})
	_, _ = c.ReadIssue(context.Background(), ReadIssueRequest{Number: 42, MaxComments: 20})
	if inner.readIssueCount != 2 {
		t.Errorf("different MaxComments should be separate entries; got %d", inner.readIssueCount)
	}
}

func TestCachingClient_IssueListLabelOrderIndependent(t *testing.T) {
	// Labels supplied in different orders should hit the same
	// cache entry — the filter semantically matches regardless of
	// list order.
	inner := &countingInner{issuesRes: []IssueSummary{}}
	c := NewCachingClient(inner)
	_, _ = c.ListOpenIssues(context.Background(), ListIssuesRequest{
		Labels: []string{"bug", "priority-high"},
	})
	_, _ = c.ListOpenIssues(context.Background(), ListIssuesRequest{
		Labels: []string{"priority-high", "bug"}, // reordered
	})
	if inner.listIssuesCount != 1 {
		t.Errorf("label order should not affect caching; got %d calls", inner.listIssuesCount)
	}
}

func TestCachingClient_CreateIssueInvalidatesIssueLists(t *testing.T) {
	// A successful create makes every cached issue list stale — the
	// new issue belongs in any matching filter's results. The next
	// list call must go back to the API, not serve the cached list.
	inner := &countingInner{issuesRes: []IssueSummary{{Number: 1}}}
	c := NewCachingClient(inner)
	_, _ = c.ListOpenIssues(context.Background(), ListIssuesRequest{})
	_, _ = c.ListOpenIssues(context.Background(), ListIssuesRequest{})
	if inner.listIssuesCount != 1 {
		t.Fatalf("expected list cached after first call; got %d calls", inner.listIssuesCount)
	}
	if _, err := c.CreateIssue(context.Background(), CreateIssueRequest{Title: "t"}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	_, _ = c.ListOpenIssues(context.Background(), ListIssuesRequest{})
	if inner.listIssuesCount != 2 {
		t.Errorf("expected list re-fetched after CreateIssue; got %d calls", inner.listIssuesCount)
	}
	if inner.createIssueCount != 1 {
		t.Errorf("expected exactly 1 CreateIssue call; got %d", inner.createIssueCount)
	}
}

func TestCachingClient_ErrorsNotCached(t *testing.T) {
	// A typed sentinel from the inner must NOT be cached — the
	// caller may retry after fixing auth or after a transient
	// failure passes.
	inner := &countingInner{prErr: ErrPRNotFound}
	c := NewCachingClient(inner)
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "999"})
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "999"})
	if inner.readPRCount != 2 {
		t.Errorf("errors must not be cached; got %d calls", inner.readPRCount)
	}
}

func TestCachingClient_GenericErrorNotCached(t *testing.T) {
	inner := &countingInner{prErr: errors.New("transient: 503")}
	c := NewCachingClient(inner)
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	if inner.readPRCount != 2 {
		t.Errorf("generic errors must not be cached; got %d calls", inner.readPRCount)
	}
}

func TestCachingClient_UpdatePRInvalidatesReadPR(t *testing.T) {
	// UpdatePR rewrites title/body. A subsequent ReadPR must see
	// fresh data, not the cached pre-update snapshot.
	inner := &countingInner{prRes: PRDetails{Number: 29, Title: "old"}}
	c := NewCachingClient(inner)
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	if inner.readPRCount != 1 {
		t.Fatalf("setup: expected 1 read; got %d", inner.readPRCount)
	}

	// Mutate inner's response so a cache-bypass would surface
	// the new title.
	inner.prRes = PRDetails{Number: 29, Title: "new"}
	_, _ = c.UpdatePR(context.Background(), UpdatePRRequest{Ref: "29", Title: "new", Body: "x"})

	got, _ := c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	if got.Title != "new" {
		t.Errorf("UpdatePR should invalidate; got cached title %q", got.Title)
	}
	if inner.readPRCount != 2 {
		t.Errorf("expected a fresh inner ReadPR after UpdatePR; got %d total", inner.readPRCount)
	}
}

func TestCachingClient_WritesPassThrough(t *testing.T) {
	// Write methods are not cached and pass through every call.
	inner := &countingInner{}
	c := NewCachingClient(inner)
	for i := 0; i < 3; i++ {
		_, _ = c.CreatePR(context.Background(), CreatePRRequest{Title: "t", Body: "b", Base: "main"})
		_, _ = c.AddPRComment(context.Background(), AddPRCommentRequest{Ref: "29", Body: "x"})
	}
	if inner.createPRCount != 3 {
		t.Errorf("CreatePR should pass through every call; got %d", inner.createPRCount)
	}
	if inner.addCommentCount != 3 {
		t.Errorf("AddPRComment should pass through every call; got %d", inner.addCommentCount)
	}
}

func TestCachingClient_Stats(t *testing.T) {
	inner := &countingInner{
		prRes:     PRDetails{Number: 29},
		issueRes:  IssueDetails{Number: 42},
		issuesRes: []IssueSummary{},
	}
	c := NewCachingClient(inner)
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	_, _ = c.ReadIssue(context.Background(), ReadIssueRequest{Number: 42})
	_, _ = c.ListOpenIssues(context.Background(), ListIssuesRequest{})

	s := c.Stats()
	if s.PRs != 1 || s.Issues != 1 || s.IssueLists != 1 {
		t.Errorf("unexpected stats: %+v", s)
	}
}

func TestCachingClient_Reset(t *testing.T) {
	inner := &countingInner{prRes: PRDetails{Number: 29}}
	c := NewCachingClient(inner)
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	c.Reset()
	_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
	if inner.readPRCount != 2 {
		t.Errorf("after Reset, next read should miss; got %d total", inner.readPRCount)
	}
	s := c.Stats()
	if s.PRs != 1 {
		t.Errorf("post-reset cache should have one entry (the new read); got %+v", s)
	}
}

func TestCachingClient_ConcurrentReads(t *testing.T) {
	// Race detector exercises this when go test -race; sequential
	// run still verifies no panic / no key corruption.
	inner := &countingInner{prRes: PRDetails{Number: 29}}
	c := NewCachingClient(inner)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.ReadPR(context.Background(), ReadPRRequest{Ref: "29"})
		}()
	}
	wg.Wait()
	// Cache hit count isn't asserted (concurrent misses can both
	// hit Inner before either stores), but the test must not race
	// or panic.
	if inner.readPRCount < 1 || inner.readPRCount > 10 {
		t.Errorf("inner call count out of expected band: %d", inner.readPRCount)
	}
}

func TestCachingClient_SatisfiesInterface(t *testing.T) {
	// Compile-time assertion that *CachingClient implements
	// Interface. Doubles as a runtime check; if the interface
	// shape changes and CachingClient drifts, this fails to
	// compile with a clear error.
	var _ Interface = (*CachingClient)(nil)
}

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"}, {1, "1"}, {-1, "-1"}, {42, "42"}, {-42, "-42"},
		{99999, "99999"}, {-99999, "-99999"},
	}
	for _, tc := range cases {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestIssueListKey_DeterministicAcrossOrderings(t *testing.T) {
	a := issueListKey(ListIssuesRequest{Labels: []string{"bug", "urgent"}})
	b := issueListKey(ListIssuesRequest{Labels: []string{"urgent", "bug"}})
	if a != b {
		t.Errorf("key should be order-independent: %q vs %q", a, b)
	}
}

func TestIssueListKey_DistinguishesFilters(t *testing.T) {
	a := issueListKey(ListIssuesRequest{Labels: []string{"bug"}})
	b := issueListKey(ListIssuesRequest{Labels: []string{"bug"}, Assignee: "octocat"})
	if a == b {
		t.Errorf("different filters should produce different keys; got %q", a)
	}
	if !strings.Contains(b, "octocat") {
		t.Errorf("assignee should appear in key; got %q", b)
	}
}
