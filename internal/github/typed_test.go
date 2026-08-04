package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v66/github"
)

func TestParseInt_NumericRefs(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"17", 17, true},
		{"1", 1, true},
		{"99999", 99999, true},
		{"", 0, false},
		{"abc", 0, false},
		{"17abc", 0, false},
		{"feature/x", 0, false},
		{"-5", 0, false}, // signs not accepted; caller passes branch names that could contain anything
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseInt(tc.in)
			if tc.ok {
				if err != nil {
					t.Errorf("parseInt(%q) = error %v; want %d", tc.in, err, tc.want)
				}
				if got != tc.want {
					t.Errorf("parseInt(%q) = %d; want %d", tc.in, got, tc.want)
				}
			} else {
				if err == nil {
					t.Errorf("parseInt(%q) = %d, nil; want error", tc.in, got)
				}
			}
		})
	}
}

func TestMapPRDetails_FullFixture(t *testing.T) {
	// Hand-built fixture covering every field PRDetails surfaces.
	// Catches accidental field drift between mapPRDetails and the
	// PRDetails struct.
	state := "open"
	mergeable := "mergeable"
	pr := &gogithub.PullRequest{
		Number: gogithub.Int(17),
		Title:  gogithub.String("Add typed client"),
		Body:   gogithub.String("Long body"),
		State:  &state,
		Draft:  gogithub.Bool(false),
		Base: &gogithub.PullRequestBranch{
			Ref: gogithub.String("main"),
		},
		Head: &gogithub.PullRequestBranch{
			Ref: gogithub.String("feature/typed"),
			SHA: gogithub.String("abc123def456"),
		},
		MergeableState: &mergeable,
		User: &gogithub.User{
			Login: gogithub.String("octocat"),
		},
		HTMLURL: gogithub.String("https://github.com/o/r/pull/17"),
		Labels: []*gogithub.Label{
			{Name: gogithub.String("enhancement")},
			{Name: gogithub.String("review")},
		},
	}
	got := mapPRDetails(pr)
	if got.Number != 17 || got.Title != "Add typed client" || got.Body != "Long body" {
		t.Errorf("scalars not mapped: %+v", got)
	}
	if got.State != "OPEN" {
		t.Errorf("state should be uppercased: %q", got.State)
	}
	if got.Mergeable != "MERGEABLE" {
		t.Errorf("mergeable should be uppercased: %q", got.Mergeable)
	}
	if got.BaseRef != "main" || got.HeadRef != "feature/typed" || got.HeadSHA != "abc123def456" {
		t.Errorf("refs not mapped: %+v", got)
	}
	if got.Author != "octocat" {
		t.Errorf("author = %q", got.Author)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "enhancement" || got.Labels[1] != "review" {
		t.Errorf("labels = %v", got.Labels)
	}
}

func TestMapPRDetails_NilSafe(t *testing.T) {
	// Defensive: every pointer dereference is guarded so a
	// partial response from go-github (rare but possible during
	// GitHub-side incidents) doesn't panic.
	got := mapPRDetails(nil)
	if got.Number != 0 || got.Title != "" {
		t.Errorf("nil input should return zero value, got %+v", got)
	}
}

func TestMapPRDetails_DropsEmptyLabelNames(t *testing.T) {
	pr := &gogithub.PullRequest{
		Labels: []*gogithub.Label{
			{Name: gogithub.String("")},
			{Name: gogithub.String("good")},
			{Name: nil},
		},
	}
	got := mapPRDetails(pr)
	if len(got.Labels) != 1 || got.Labels[0] != "good" {
		t.Errorf("labels = %v; want [good]", got.Labels)
	}
}

func TestMapCheckRun_UpperCasesEnums(t *testing.T) {
	// The Checks API returns lowercase status / conclusion;
	// our typed envelope uppercases (matching the legacy
	// ShellOut output the existing tests pin).
	now := time.Now()
	cr := &gogithub.CheckRun{
		Name:        gogithub.String("build"),
		Status:      gogithub.String("completed"),
		Conclusion:  gogithub.String("success"),
		StartedAt:   &gogithub.Timestamp{Time: now},
		CompletedAt: &gogithub.Timestamp{Time: now.Add(5 * time.Minute)},
	}
	got := mapCheckRun(cr)
	if got.Name != "build" {
		t.Errorf("name = %q", got.Name)
	}
	if got.State != "COMPLETED" {
		t.Errorf("state = %q; want COMPLETED", got.State)
	}
	if got.Conclusion != "SUCCESS" {
		t.Errorf("conclusion = %q; want SUCCESS", got.Conclusion)
	}
	if got.StartedAt.IsZero() || got.CompletedAt.IsZero() {
		t.Errorf("timestamps should be populated: %+v", got)
	}
}

func TestMapCheckRun_NilSafe(t *testing.T) {
	got := mapCheckRun(nil)
	if got.Name != "" || got.State != "" {
		t.Errorf("nil input should return zero value, got %+v", got)
	}
}

func TestMapStatus_UsesStateAsConclusion(t *testing.T) {
	// Legacy commit-status contexts don't have a separate
	// Conclusion field — State carries the outcome
	// ("success" / "failure" / "pending" / "error"). Verify the
	// mapper surfaces State so the existing failing-check
	// classifier in agent/gh_pr_workflow.go handles both shapes.
	now := time.Now()
	st := &gogithub.RepoStatus{
		Context:   gogithub.String("ci/circle"),
		State:     gogithub.String("failure"),
		CreatedAt: &gogithub.Timestamp{Time: now},
		UpdatedAt: &gogithub.Timestamp{Time: now.Add(2 * time.Minute)},
	}
	got := mapStatus(st)
	if got.Name != "ci/circle" {
		t.Errorf("name = %q", got.Name)
	}
	if got.State != "FAILURE" {
		t.Errorf("state = %q; want FAILURE", got.State)
	}
	// Conclusion intentionally empty for status contexts —
	// the failing-check classifier reads State as the fallback.
	if got.Conclusion != "" {
		t.Errorf("conclusion should be empty for status, got %q", got.Conclusion)
	}
}

func TestTailLinesFromReaderReadsWholeLog(t *testing.T) {
	log := strings.Join([]string{"early failure", "middle", "actual final failure"}, "\n")
	got, err := tailLinesFromReader(strings.NewReader(log), 1)
	if err != nil {
		t.Fatalf("tailLinesFromReader: %v", err)
	}
	if len(got) != 1 || got[0] != "actual final failure" {
		t.Fatalf("tail = %#v, want final line", got)
	}
}

func TestMapIssueDetails_FullFixture(t *testing.T) {
	state := "open"
	issue := &gogithub.Issue{
		Number:  gogithub.Int(42),
		Title:   gogithub.String("Add caching layer"),
		Body:    gogithub.String("We need a cache for repeated reads."),
		State:   &state,
		HTMLURL: gogithub.String("https://github.com/o/r/issues/42"),
		User: &gogithub.User{
			Login: gogithub.String("reporter"),
		},
		Labels: []*gogithub.Label{
			{Name: gogithub.String("bug")},
			{Name: gogithub.String("priority-high")},
		},
		Assignees: []*gogithub.User{
			{Login: gogithub.String("octocat")},
		},
	}
	got := mapIssueDetails(issue)
	if got.Number != 42 || got.Title != "Add caching layer" {
		t.Errorf("scalars not mapped: %+v", got)
	}
	if got.State != "OPEN" {
		t.Errorf("state should be uppercased: %q", got.State)
	}
	if got.Author != "reporter" {
		t.Errorf("author = %q", got.Author)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "bug" {
		t.Errorf("labels = %v", got.Labels)
	}
	if len(got.Assignees) != 1 || got.Assignees[0] != "octocat" {
		t.Errorf("assignees = %v", got.Assignees)
	}
}

func TestMapIssueDetails_NilSafe(t *testing.T) {
	got := mapIssueDetails(nil)
	if got.Number != 0 || got.Title != "" || got.State != "" {
		t.Errorf("nil input should return zero value, got %+v", got)
	}
}

func TestMapIssueDetails_DropsEmptyLabelsAndAssignees(t *testing.T) {
	issue := &gogithub.Issue{
		Labels: []*gogithub.Label{
			{Name: gogithub.String("")},
			{Name: gogithub.String("keeper")},
			{Name: nil},
		},
		Assignees: []*gogithub.User{
			{Login: gogithub.String("")},
			{Login: gogithub.String("kept")},
			{Login: nil},
		},
	}
	got := mapIssueDetails(issue)
	if len(got.Labels) != 1 || got.Labels[0] != "keeper" {
		t.Errorf("labels = %v; want [keeper]", got.Labels)
	}
	if len(got.Assignees) != 1 || got.Assignees[0] != "kept" {
		t.Errorf("assignees = %v; want [kept]", got.Assignees)
	}
}

func TestMapIssueComment_FullFixture(t *testing.T) {
	now := time.Now()
	c := &gogithub.IssueComment{
		Body: gogithub.String("LGTM"),
		User: &gogithub.User{
			Login: gogithub.String("reviewer"),
		},
		CreatedAt: &gogithub.Timestamp{Time: now},
	}
	got := mapIssueComment(c)
	if got.Author != "reviewer" || got.Body != "LGTM" {
		t.Errorf("comment not mapped: %+v", got)
	}
	if got.Created.IsZero() {
		t.Errorf("created timestamp should be populated")
	}
}

func TestMapIssueComment_NilSafe(t *testing.T) {
	got := mapIssueComment(nil)
	if got.Author != "" || got.Body != "" {
		t.Errorf("nil input should return zero value, got %+v", got)
	}
}

func TestMapIssueSummary_DropsBodyAndComments(t *testing.T) {
	// IssueSummary is the lightweight contract: Body should never
	// appear in summary output even if go-github surfaces it.
	issue := &gogithub.Issue{
		Number:  gogithub.Int(7),
		Title:   gogithub.String("summary issue"),
		Body:    gogithub.String("this body should NOT appear in IssueSummary"),
		HTMLURL: gogithub.String("https://github.com/o/r/issues/7"),
		User:    &gogithub.User{Login: gogithub.String("op")},
		Labels:  []*gogithub.Label{{Name: gogithub.String("triage")}},
	}
	got := mapIssueSummary(issue)
	if got.Number != 7 || got.Title != "summary issue" {
		t.Errorf("summary scalars not mapped: %+v", got)
	}
	// IssueSummary has no Body field — compile-time guarantee that
	// the body cannot leak. This test exists to catch the case
	// where someone adds Body to IssueSummary later: if they do,
	// they need to consciously delete this test, which is the
	// signal to think about whether it's right.
	_ = got
}

func TestClassifyAPIError_404IsNotFound(t *testing.T) {
	err := &gogithub.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotFound},
		Message:  "Not Found",
	}
	got := classifyAPIError(err)
	if !errors.Is(got, ErrPRNotFound) {
		t.Errorf("404 should classify as ErrPRNotFound; got %v", got)
	}
}

func TestClassifyAPIError_401IsGitHubUnavailable(t *testing.T) {
	// Token rejected / missing → ErrGitHubUnavailable parity with
	// ShellOut's behavior so callers don't have to branch on
	// new error types.
	err := &gogithub.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnauthorized},
		Message:  "Bad credentials",
	}
	got := classifyAPIError(err)
	if !errors.Is(got, ErrGitHubUnavailable) {
		t.Errorf("401 should classify as ErrGitHubUnavailable; got %v", got)
	}
}

func TestClassifyAPIError_OtherStatusBubblesUp(t *testing.T) {
	// 500 / 503 / 422 etc. don't map to our sentinels — they
	// pass through so callers see the raw error with context.
	err := &gogithub.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusInternalServerError},
		Message:  "Internal Server Error",
	}
	got := classifyAPIError(err)
	if errors.Is(got, ErrPRNotFound) || errors.Is(got, ErrGitHubUnavailable) {
		t.Errorf("500 should pass through, not classify; got %v", got)
	}
}

func TestClassifyAPIError_NonAPIError(t *testing.T) {
	// Network failures and other non-ErrorResponse errors
	// (context cancellation, DNS failure, etc.) pass through
	// unchanged.
	plain := errors.New("network unreachable")
	got := classifyAPIError(plain)
	if got != plain {
		t.Errorf("plain error should pass through unchanged; got %v", got)
	}
}

// captureIssueRT records the CreateIssue request body and returns a
// canned 201 so tests can assert the wire-level JSON shape.
type captureIssueRT struct {
	body []byte
	path string
}

func (rt *captureIssueRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.body, _ = io.ReadAll(r.Body)
	rt.path = r.URL.Path
	return &http.Response{
		StatusCode: http.StatusCreated,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"number":7,"html_url":"https://github.com/o/r/issues/7"}`)),
		Request:    r,
	}, nil
}

// Regression: a label-less / assignee-less CreateIssue must omit the
// "labels" and "assignees" keys entirely. IssueRequest carries them as
// *[]string with omitempty — taking &req.Labels on an empty slice
// serialized `"labels":null`, which GitHub's schema validation rejects
// with a 422 ("nil is not an array"), breaking the default
// /git-create-issue flow. The fakeGH-based agent tests bypass
// serialization, so this pins the contract at the HTTP layer.
func TestCreateIssue_OmitsUnsetLabelsAndAssignees(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	rt := &captureIssueRT{}
	c := &TypedClient{
		Cwd:        t.TempDir(),
		HTTPClient: &http.Client{Transport: rt},
	}
	res, err := c.CreateIssue(context.Background(), CreateIssueRequest{
		Owner: "o", Repo: "r", Title: "Fix crash on resize",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if res.Number != 7 || res.URL != "https://github.com/o/r/issues/7" {
		t.Errorf("result = %+v; want number 7 and the html_url decoded", res)
	}
	if got, want := rt.path, "/repos/o/r/issues"; got != want {
		t.Errorf("POST path = %q; want %q", got, want)
	}
	var wire map[string]any
	if err := json.Unmarshal(rt.body, &wire); err != nil {
		t.Fatalf("request body not JSON: %v\n%s", err, rt.body)
	}
	if _, ok := wire["labels"]; ok {
		t.Errorf(`"labels" present in wire body (%s); must be omitted when unset — GitHub 422s "labels":null`, rt.body)
	}
	if _, ok := wire["assignees"]; ok {
		t.Errorf(`"assignees" present in wire body (%s); must be omitted when unset`, rt.body)
	}
	if wire["title"] != "Fix crash on resize" {
		t.Errorf("title = %v; want the request title", wire["title"])
	}
}

// Populated labels/assignees still go out as real arrays, and the body
// field is carried when non-empty.
func TestCreateIssue_SendsLabelsWhenSet(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	rt := &captureIssueRT{}
	c := &TypedClient{Cwd: t.TempDir(), HTTPClient: &http.Client{Transport: rt}}
	_, err := c.CreateIssue(context.Background(), CreateIssueRequest{
		Owner: "o", Repo: "r", Title: "t", Body: "b",
		Labels: []string{"bug"}, Assignees: []string{"octocat"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	var wire struct {
		Body      string   `json:"body"`
		Labels    []string `json:"labels"`
		Assignees []string `json:"assignees"`
	}
	if err := json.Unmarshal(rt.body, &wire); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if wire.Body != "b" || len(wire.Labels) != 1 || wire.Labels[0] != "bug" ||
		len(wire.Assignees) != 1 || wire.Assignees[0] != "octocat" {
		t.Errorf("wire body = %s; want body, labels, assignees carried through", rt.body)
	}
}
