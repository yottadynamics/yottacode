package github

import (
	"errors"
	"net/http"
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

func TestClassifyAPIError_401IsGhUnavailable(t *testing.T) {
	// Token rejected / missing → ErrGhUnavailable parity with
	// ShellOut's behavior so callers don't have to branch on
	// new error types.
	err := &gogithub.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnauthorized},
		Message:  "Bad credentials",
	}
	got := classifyAPIError(err)
	if !errors.Is(got, ErrGhUnavailable) {
		t.Errorf("401 should classify as ErrGhUnavailable; got %v", got)
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
	if errors.Is(got, ErrPRNotFound) || errors.Is(got, ErrGhUnavailable) {
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
