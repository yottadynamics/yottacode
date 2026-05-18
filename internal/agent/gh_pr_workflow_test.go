package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/github"
)

// fakeGH is the test double for github.Interface. Captures the last
// call's request and lets each test pre-set the response. The read
// methods (ReadPR, ReadPRDiff, ListPRChecks) carry their own canned
// fixtures so a single fakeGH can stand in for whichever subset of
// the Interface a test exercises.
type fakeGH struct {
	// Create-PR side
	lastReq github.CreatePRRequest
	calls   int
	res     github.CreatePRResult
	err     error

	// Read side — each fixture is used by the matching method when
	// the test populates it. Errors short-circuit ahead of the
	// returned value so the test can simulate ErrPRNotFound /
	// ErrGhUnavailable / generic gh failures without contorting the
	// fixture struct.
	readPRRes      github.PRDetails
	readPRErr      error
	readPRReq      github.ReadPRRequest
	readPRDiffRes  string
	readPRDiffErr  error
	readPRDiffReq  github.ReadPRRequest
	listChecksRes  []github.CheckRun
	listChecksErr  error
	listChecksReq  github.ReadPRRequest

	// Update-PR side
	updatePRRes github.UpdatePRResult
	updatePRErr error
	updatePRReq github.UpdatePRRequest

	// Issue side
	readIssueRes      github.IssueDetails
	readIssueErr      error
	readIssueReq      github.ReadIssueRequest
	listIssuesRes     []github.IssueSummary
	listIssuesErr     error
	listIssuesReq     github.ListIssuesRequest
	addPRCommentRes   github.AddPRCommentResult
	addPRCommentErr   error
	addPRCommentReq   github.AddPRCommentRequest
	addPRCommentCalls int
}

func (f *fakeGH) CreatePR(_ context.Context, req github.CreatePRRequest) (github.CreatePRResult, error) {
	f.calls++
	f.lastReq = req
	return f.res, f.err
}

func (f *fakeGH) ReadPR(_ context.Context, req github.ReadPRRequest) (github.PRDetails, error) {
	f.readPRReq = req
	return f.readPRRes, f.readPRErr
}

func (f *fakeGH) ReadPRDiff(_ context.Context, req github.ReadPRRequest) (string, error) {
	f.readPRDiffReq = req
	return f.readPRDiffRes, f.readPRDiffErr
}

func (f *fakeGH) ListPRChecks(_ context.Context, req github.ReadPRRequest) ([]github.CheckRun, error) {
	f.listChecksReq = req
	return f.listChecksRes, f.listChecksErr
}

func (f *fakeGH) UpdatePR(_ context.Context, req github.UpdatePRRequest) (github.UpdatePRResult, error) {
	f.updatePRReq = req
	return f.updatePRRes, f.updatePRErr
}

func (f *fakeGH) ReadIssue(_ context.Context, req github.ReadIssueRequest) (github.IssueDetails, error) {
	f.readIssueReq = req
	return f.readIssueRes, f.readIssueErr
}

func (f *fakeGH) ListOpenIssues(_ context.Context, req github.ListIssuesRequest) ([]github.IssueSummary, error) {
	f.listIssuesReq = req
	return f.listIssuesRes, f.listIssuesErr
}

func (f *fakeGH) AddPRComment(_ context.Context, req github.AddPRCommentRequest) (github.AddPRCommentResult, error) {
	f.addPRCommentCalls++
	f.addPRCommentReq = req
	return f.addPRCommentRes, f.addPRCommentErr
}

func (f *fakeGH) RateLimit() github.RateLimitSnapshot {
	// Test double — no rate tracking. Zero snapshot signals
	// "unset" to callers checking IsSet().
	return github.RateLimitSnapshot{}
}

func TestValidatePRTitle(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string // "" = valid
	}{
		{"happy", "implement caching layer", ""},
		{"trailing newline tolerated", "implement caching layer\n", ""},
		{"empty", "", "title is empty"},
		{"whitespace only", "   ", "title is empty"},
		{"multi-line rejected", "title\nbody", "title must be a single line"},
		{"trailing period rejected", "implement caching.", "title must not end with a period"},
		{"over cap rejected", strings.Repeat("x", PRTitleMaxLen+1), "title is"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validatePRTitle(tc.title)
			if tc.want == "" {
				if got != "" {
					t.Errorf("expected valid, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestCreatePR_ValidationFailsBeforeNetwork(t *testing.T) {
	gh := &fakeGH{}
	res, err := CreatePR(context.Background(), gh, github.CreatePRRequest{
		Base: "main", Title: "fix.", Body: "details",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if res.Created {
		t.Errorf("must not be created on validation failure: %+v", res)
	}
	if res.ValidationErr == "" {
		t.Errorf("expected validation error")
	}
	// Critical: the client was NEVER dialed. Validation failure must
	// not produce a network call.
	if gh.calls != 0 {
		t.Errorf("expected 0 client calls on validation fail; got %d", gh.calls)
	}
}

func TestCreatePR_HappyPath(t *testing.T) {
	gh := &fakeGH{res: github.CreatePRResult{URL: "https://github.com/o/r/pull/9", Number: 9}}
	res, err := CreatePR(context.Background(), gh, github.CreatePRRequest{
		Base: "main", Title: "implement caching", Body: "details",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if !res.Created {
		t.Errorf("expected Created=true: %+v", res)
	}
	if res.URL != "https://github.com/o/r/pull/9" {
		t.Errorf("URL = %q", res.URL)
	}
	if res.Number != 9 {
		t.Errorf("Number = %d", res.Number)
	}
}

func TestCreatePR_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{err: github.ErrGhUnavailable}
	res, err := CreatePR(context.Background(), gh, github.CreatePRRequest{
		Base: "main", Title: "implement caching", Body: "details",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if !res.GhUnavailable {
		t.Errorf("expected GhUnavailable=true: %+v", res)
	}
	if res.Created {
		t.Errorf("must not be Created when gh unavailable: %+v", res)
	}
}

func TestCreatePR_GenericGhErrorSurfaced(t *testing.T) {
	gh := &fakeGH{err: errors.New("rate limited")}
	res, err := CreatePR(context.Background(), gh, github.CreatePRRequest{
		Base: "main", Title: "implement caching", Body: "details",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if res.GhError == "" {
		t.Errorf("expected GhError populated: %+v", res)
	}
	if !strings.Contains(res.GhError, "rate limited") {
		t.Errorf("GhError should surface verbatim cause: %q", res.GhError)
	}
}

func TestResolveBaseBranch_ExplicitWins(t *testing.T) {
	tmp := gitInit(t)
	base, source := resolveBaseBranch(context.Background(), tmp, "develop")
	if base != "develop" || source != "explicit" {
		t.Errorf("base=%q source=%q; want develop / explicit", base, source)
	}
}

func TestResolveBaseBranch_FallbackChain(t *testing.T) {
	// Fresh repo with the default branch only — origin/HEAD is unset,
	// so resolution should fall through to the fallback chain and
	// pick `main` (the only one that exists).
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	base, source := resolveBaseBranch(context.Background(), tmp, "")
	if base != "main" {
		t.Errorf("base=%q; want main", base)
	}
	if !strings.HasPrefix(source, "fallback:") {
		t.Errorf("source=%q; want fallback:main", source)
	}
}

func TestBuildPRContext_BaseEqualsCurrent(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	snap, err := BuildPRContext(context.Background(), tmp, "main")
	if err != nil {
		t.Fatalf("BuildPRContext: %v", err)
	}
	if !snap.BaseEqualsCurrent {
		t.Errorf("expected BaseEqualsCurrent=true (HEAD is main): %+v", snap)
	}
	if snap.AheadCount != 0 {
		t.Errorf("expected AheadCount=0 with no feature branch: %d", snap.AheadCount)
	}
}

func TestBuildPRContext_AheadCountAndLog(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base on main")

	// Branch off to a feature branch and add two commits so ahead-count
	// is computable and the commit-log section has content.
	gitRun(t, tmp, "checkout", "-q", "-b", "feature/x")
	writeFile(t, tmp, "f.txt", "v2\n")
	gitCommit(t, tmp, "feat: bump to v2")
	writeFile(t, tmp, "f.txt", "v3\n")
	gitCommit(t, tmp, "feat: bump to v3")

	snap, err := BuildPRContext(context.Background(), tmp, "main")
	if err != nil {
		t.Fatalf("BuildPRContext: %v", err)
	}
	if snap.BaseEqualsCurrent {
		t.Errorf("expected BaseEqualsCurrent=false on feature branch")
	}
	if snap.AheadCount != 2 {
		t.Errorf("expected AheadCount=2, got %d", snap.AheadCount)
	}
	if len(snap.CommitLog) != 2 {
		t.Errorf("expected 2 entries in CommitLog, got %d: %v", len(snap.CommitLog), snap.CommitLog)
	}
	// The diffstat must reference the changed file. Don't assert on
	// exact line count — git's stat format wraps based on filename
	// width, which is unstable across versions.
	if !strings.Contains(snap.DiffStat, "f.txt") {
		t.Errorf("expected DiffStat to reference f.txt: %q", snap.DiffStat)
	}
}

func TestBuildPRContext_LoadsPRTemplate(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	if err := os.MkdirAll(filepath.Join(tmp, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	template := "## Summary\n\n## Test plan\n"
	if err := os.WriteFile(filepath.Join(tmp, ".github", "pull_request_template.md"), []byte(template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	snap, err := BuildPRContext(context.Background(), tmp, "main")
	if err != nil {
		t.Fatalf("BuildPRContext: %v", err)
	}
	if snap.PRTemplatePath != ".github/pull_request_template.md" {
		t.Errorf("template path = %q", snap.PRTemplatePath)
	}
	if !strings.Contains(snap.PRTemplate, "## Test plan") {
		t.Errorf("expected template body in snapshot: %q", snap.PRTemplate)
	}
}

func TestBuildPRContext_NoOriginNoTemplate(t *testing.T) {
	// Empty repo with no remote and no template. The snapshot must
	// still build without erroring — informational fields are empty,
	// not the source of an error.
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	snap, err := BuildPRContext(context.Background(), tmp, "main")
	if err != nil {
		t.Fatalf("BuildPRContext: %v", err)
	}
	if snap.PushedToOrigin {
		t.Errorf("expected PushedToOrigin=false with no remote: %+v", snap)
	}
	if snap.PRTemplate != "" {
		t.Errorf("expected empty PRTemplate with no template: %q", snap.PRTemplate)
	}
}

func TestGHPRCreateTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{res: github.CreatePRResult{URL: "https://github.com/o/r/pull/3", Number: 3}}
	tool := &GHPRCreateTool{Cwd: t.TempDir(), GH: gh}
	out, err := tool.Execute(context.Background(),
		`{"base":"main","title":"implement caching","body":"why","draft":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "created=true url=") {
		t.Errorf("expected created=true, got:\n%s", out)
	}
	if !gh.lastReq.Draft {
		t.Errorf("expected Draft=true forwarded to client; got %+v", gh.lastReq)
	}
}

func TestBuildPRReviewContext_HappyPath(t *testing.T) {
	gh := &fakeGH{
		readPRRes: github.PRDetails{
			Number: 17, Title: "fix the thing", State: "OPEN", BaseRef: "main",
			HeadRef: "feature/x", Author: "octocat", URL: "https://github.com/o/r/pull/17",
			Labels: []string{"bug"},
		},
		readPRDiffRes: "diff --git a/f b/f\n+v2\n-v1\n",
		listChecksRes: []github.CheckRun{
			{Name: "build", State: "COMPLETED", Conclusion: "SUCCESS"},
			{Name: "lint", State: "COMPLETED", Conclusion: "FAILURE"},
		},
	}
	snap, err := BuildPRReviewContext(context.Background(), gh, "17")
	if err != nil {
		t.Fatalf("BuildPRReviewContext: %v", err)
	}
	if snap.NotFound || snap.GhUnavailable {
		t.Errorf("expected found+available; got %+v", snap)
	}
	if snap.PR.Number != 17 || snap.PR.Title != "fix the thing" {
		t.Errorf("PR metadata not populated: %+v", snap.PR)
	}
	if !slices.Equal(snap.FailingChecks, []string{"lint"}) {
		t.Errorf("FailingChecks = %v; want [lint]", snap.FailingChecks)
	}
	if !strings.Contains(snap.Diff, "+v2") {
		t.Errorf("Diff missing content: %q", snap.Diff)
	}
}

func TestBuildPRReviewContext_NotFoundSurfaced(t *testing.T) {
	gh := &fakeGH{readPRErr: github.ErrPRNotFound}
	snap, err := BuildPRReviewContext(context.Background(), gh, "999")
	if err != nil {
		t.Fatalf("BuildPRReviewContext: %v", err)
	}
	if !snap.NotFound {
		t.Errorf("expected NotFound=true; got %+v", snap)
	}
	// Critical: with NotFound the checks/diff fetches must NOT
	// fire (a missing PR shouldn't burn three round trips).
	if gh.listChecksReq.Ref != "" {
		t.Errorf("ListPRChecks should not have been called on NotFound")
	}
	if gh.readPRDiffReq.Ref != "" {
		t.Errorf("ReadPRDiff should not have been called on NotFound")
	}
}

func TestBuildPRReviewContext_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{readPRErr: github.ErrGhUnavailable}
	snap, err := BuildPRReviewContext(context.Background(), gh, "17")
	if err != nil {
		t.Fatalf("BuildPRReviewContext: %v", err)
	}
	if !snap.GhUnavailable {
		t.Errorf("expected GhUnavailable=true; got %+v", snap)
	}
}

func TestBuildPRReviewContext_ChecksSoftFail(t *testing.T) {
	// A failing checks fetch shouldn't block the review — the PR
	// metadata is the load-bearing piece. We surface the gap via
	// FetchErr but keep going with the diff.
	gh := &fakeGH{
		readPRRes:     github.PRDetails{Number: 17, Title: "t"},
		readPRDiffRes: "diff content",
		listChecksErr: errors.New("transient"),
	}
	snap, err := BuildPRReviewContext(context.Background(), gh, "17")
	if err != nil {
		t.Fatalf("BuildPRReviewContext: %v", err)
	}
	if snap.PR.Number != 17 {
		t.Errorf("expected PR populated despite checks failure")
	}
	if !strings.Contains(snap.FetchErr, "transient") {
		t.Errorf("FetchErr should surface the checks failure; got %q", snap.FetchErr)
	}
	if snap.Diff == "" {
		t.Errorf("Diff should be present despite checks failure")
	}
}

func TestIsFailingCheck(t *testing.T) {
	cases := []struct {
		name string
		c    github.CheckRun
		want bool
	}{
		{"success", github.CheckRun{Conclusion: "SUCCESS"}, false},
		{"failure", github.CheckRun{Conclusion: "FAILURE"}, true},
		{"cancelled", github.CheckRun{Conclusion: "CANCELLED"}, true},
		{"action required", github.CheckRun{Conclusion: "ACTION_REQUIRED"}, true},
		{"neutral non-failing", github.CheckRun{Conclusion: "NEUTRAL"}, false},
		{"skipped non-failing", github.CheckRun{Conclusion: "SKIPPED"}, false},
		{"pending non-failing", github.CheckRun{State: "IN_PROGRESS"}, false},
		// Status-context shape: State="FAILURE" with empty Conclusion
		{"status-context failure", github.CheckRun{State: "FAILURE"}, true},
		{"status-context error", github.CheckRun{State: "ERROR"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFailingCheck(tc.c); got != tc.want {
				t.Errorf("isFailingCheck(%+v) = %v; want %v", tc.c, got, tc.want)
			}
		})
	}
}

func TestBuildPRReadContext_HappyPath(t *testing.T) {
	gh := &fakeGH{
		readPRRes: github.PRDetails{
			Number: 29, Title: "fix prompt", State: "OPEN", BaseRef: "main",
			HeadRef: "feature/prompt", Author: "octocat",
			URL:    "https://github.com/o/r/pull/29",
			Body:   "Body line 1\nBody line 2",
			Labels: []string{"docs"},
		},
	}
	snap := BuildPRReadContext(context.Background(), gh, "29")
	if snap.NotFound || snap.GhUnavailable {
		t.Errorf("expected found+available; got %+v", snap)
	}
	if snap.PR.Number != 29 || snap.PR.Title != "fix prompt" {
		t.Errorf("PR metadata not populated: %+v", snap.PR)
	}
	if snap.PR.Body == "" {
		t.Errorf("Body should be populated")
	}
	// Critical contract: gh_pr_read MUST NOT call diff or checks.
	// If a future refactor accidentally folds them in, this test
	// catches it.
	if gh.readPRDiffReq.Ref != "" {
		t.Errorf("ReadPRDiff must NOT be called by gh_pr_read")
	}
	if gh.listChecksReq.Ref != "" {
		t.Errorf("ListPRChecks must NOT be called by gh_pr_read")
	}
}

func TestBuildPRReadContext_NotFoundSurfaced(t *testing.T) {
	gh := &fakeGH{readPRErr: github.ErrPRNotFound}
	snap := BuildPRReadContext(context.Background(), gh, "999")
	if !snap.NotFound {
		t.Errorf("expected NotFound=true; got %+v", snap)
	}
}

func TestBuildPRReadContext_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{readPRErr: github.ErrGhUnavailable}
	snap := BuildPRReadContext(context.Background(), gh, "29")
	if !snap.GhUnavailable {
		t.Errorf("expected GhUnavailable=true; got %+v", snap)
	}
}

func TestBuildPRReadContext_GenericErrorSurfaced(t *testing.T) {
	gh := &fakeGH{readPRErr: errors.New("api: rate limit exceeded")}
	snap := BuildPRReadContext(context.Background(), gh, "29")
	if snap.NotFound || snap.GhUnavailable {
		t.Errorf("generic error should not flip typed flags: %+v", snap)
	}
	if !strings.Contains(snap.FetchErr, "rate limit") {
		t.Errorf("FetchErr should carry the message; got %q", snap.FetchErr)
	}
}

func TestAddPRComment_HappyPath(t *testing.T) {
	gh := &fakeGH{
		addPRCommentRes: github.AddPRCommentResult{
			URL: "https://github.com/o/r/pull/29#issuecomment-123",
			ID:  123,
		},
	}
	res, err := AddPRComment(context.Background(), gh, github.AddPRCommentRequest{
		Ref:  "29",
		Body: "LGTM, cross-linking Refs #42",
	})
	if err != nil {
		t.Fatalf("AddPRComment: %v", err)
	}
	if !res.Posted {
		t.Errorf("expected Posted=true; got %+v", res)
	}
	if res.URL == "" || res.ID == 0 {
		t.Errorf("expected URL+ID populated; got %+v", res)
	}
	if gh.addPRCommentReq.Ref != "29" {
		t.Errorf("Ref not propagated to request: %q", gh.addPRCommentReq.Ref)
	}
}

func TestAddPRComment_RejectsEmptyBody(t *testing.T) {
	gh := &fakeGH{}
	res, err := AddPRComment(context.Background(), gh, github.AddPRCommentRequest{
		Ref:  "29",
		Body: "   \n  \t  ",
	})
	if err != nil {
		t.Fatalf("AddPRComment: %v", err)
	}
	if res.Posted {
		t.Errorf("expected Posted=false on empty body")
	}
	if !strings.Contains(res.ValidationErr, "empty") {
		t.Errorf("expected validation error; got %+v", res)
	}
	// Critical: validation runs before the network call. An empty
	// body must NOT round-trip to GitHub.
	if gh.addPRCommentCalls != 0 {
		t.Errorf("AddPRComment should not have been called on empty body; calls=%d", gh.addPRCommentCalls)
	}
}

func TestAddPRComment_RejectsOversizeBody(t *testing.T) {
	gh := &fakeGH{}
	huge := strings.Repeat("x", 20*1024) // exceeds 16KiB cap
	res, err := AddPRComment(context.Background(), gh, github.AddPRCommentRequest{
		Ref:  "29",
		Body: huge,
	})
	if err != nil {
		t.Fatalf("AddPRComment: %v", err)
	}
	if res.Posted {
		t.Errorf("expected Posted=false on oversize body")
	}
	if !strings.Contains(res.ValidationErr, "cap is") {
		t.Errorf("expected cap-mention validation error; got %q", res.ValidationErr)
	}
	if gh.addPRCommentCalls != 0 {
		t.Errorf("AddPRComment should not have been called on oversize; calls=%d", gh.addPRCommentCalls)
	}
}

func TestAddPRComment_NotFoundSurfaced(t *testing.T) {
	gh := &fakeGH{addPRCommentErr: github.ErrPRNotFound}
	res, err := AddPRComment(context.Background(), gh, github.AddPRCommentRequest{
		Ref:  "999",
		Body: "body",
	})
	if err != nil {
		t.Fatalf("AddPRComment: %v", err)
	}
	if !res.NotFound {
		t.Errorf("expected NotFound=true; got %+v", res)
	}
}

func TestAddPRComment_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{addPRCommentErr: github.ErrGhUnavailable}
	res, err := AddPRComment(context.Background(), gh, github.AddPRCommentRequest{
		Ref:  "29",
		Body: "body",
	})
	if err != nil {
		t.Fatalf("AddPRComment: %v", err)
	}
	if !res.GhUnavailable {
		t.Errorf("expected GhUnavailable=true; got %+v", res)
	}
}

func TestGHPRAddCommentTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{
		addPRCommentRes: github.AddPRCommentResult{
			URL: "https://github.com/o/r/pull/29#issuecomment-123",
			ID:  123,
		},
	}
	tool := &GHPRAddCommentTool{Cwd: t.TempDir(), GH: gh}
	out, err := tool.Execute(context.Background(), `{"ref":"29","body":"LGTM"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"posted=true", "url=https://github.com", "id=123"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, out)
		}
	}
}

func TestGHPRAddCommentTool_RequiresApproval(t *testing.T) {
	tool := &GHPRAddCommentTool{}
	if !tool.RequiresApproval("") {
		t.Errorf("gh_pr_add_comment is a write tool; MUST require approval")
	}
}

func TestGHPRAddCommentTool_PreviewExcerptCapsLongBody(t *testing.T) {
	tool := &GHPRAddCommentTool{}
	long := strings.Repeat("a", 200)
	got := tool.PreviewCall(`{"ref":"29","body":"` + long + `"}`)
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis in preview for long body; got %q", got)
	}
	if strings.Contains(got, long) {
		t.Errorf("preview must not contain full long body")
	}
}

func TestGHPRAddCommentTool_PreviewCollapsesMultiline(t *testing.T) {
	tool := &GHPRAddCommentTool{}
	got := tool.PreviewCall(`{"ref":"29","body":"line1\nline2\nline3"}`)
	if strings.Contains(got, "\n") {
		t.Errorf("preview should not contain newlines; got %q", got)
	}
	if !strings.Contains(got, "line1 line2 line3") {
		t.Errorf("preview should collapse multiline body; got %q", got)
	}
}

func TestPreviewExcerpt(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cap  int
		want string
	}{
		{"short", "short body", 80, "short body"},
		{"multiline collapses", "line1\nline2", 80, "line1 line2"},
		{"whitespace collapses", "  many   spaces  ", 80, "many spaces"},
		{"truncates at cap", strings.Repeat("a", 100), 10, strings.Repeat("a", 10) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewExcerpt(tc.in, tc.cap)
			if got != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}
}

func TestGHPRReadTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{
		readPRRes: github.PRDetails{
			Number: 29, Title: "fix prompt", State: "OPEN",
			URL: "https://x", Body: "body text",
		},
	}
	tool := &GHPRReadTool{Cwd: t.TempDir(), GH: gh}
	out, err := tool.Execute(context.Background(), `{"ref":"29"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"## state", "## pr", "number=29", "title=fix prompt", "body=|", "body text"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, out)
		}
	}
	// Diff and checks sections must NOT appear in gh_pr_read output —
	// that's the whole point of the tool.
	if strings.Contains(out, "## diff") || strings.Contains(out, "## checks") {
		t.Errorf("gh_pr_read output should not contain diff/checks sections:\n%s", out)
	}
}

func TestGHPRReadTool_NoGHAdapterErrors(t *testing.T) {
	tool := &GHPRReadTool{Cwd: t.TempDir(), GH: nil}
	_, err := tool.Execute(context.Background(), `{"ref":"29"}`)
	if err == nil || !strings.Contains(err.Error(), "no GitHub adapter") {
		t.Errorf("expected adapter-missing error; got %v", err)
	}
}

func TestGHPRReadTool_PreviewCall(t *testing.T) {
	tool := &GHPRReadTool{}
	if got := tool.PreviewCall(""); got != "gh_pr_read()" {
		t.Errorf("empty args preview = %q", got)
	}
	if got := tool.PreviewCall(`{"ref":"29"}`); got != "gh_pr_read(ref=29)" {
		t.Errorf("ref preview = %q", got)
	}
}

func TestGHPRReadTool_NotApprovalRequired(t *testing.T) {
	tool := &GHPRReadTool{}
	if tool.RequiresApproval("") {
		t.Errorf("gh_pr_read is read-only; must not require approval")
	}
}

func TestGHPRReadTool_DescriptionNudgesAwayFromBash(t *testing.T) {
	// Pin the model-nudge contract — the description must point at
	// run_bash explicitly so the model doesn't keep reaching for
	// `gh pr view --json body` from a Bash shell. If this assertion
	// drifts the nudge is lost; intentionally pinned so removal
	// requires a deliberate test edit.
	tool := &GHPRReadTool{}
	desc := tool.Description()
	for _, want := range []string{"run_bash", "gh pr view", "ONE API call"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description missing nudge phrase %q\nfull description: %s", want, desc)
		}
	}
}

func TestGHPRReviewContextTool_DescriptionNudgesAwayFromBash(t *testing.T) {
	// Sibling pin for gh_pr_review_context. Same contract: the
	// description must explicitly steer the model toward typed
	// tools over `gh pr view` / `gh pr diff` / `gh pr checks` via
	// run_bash, and must call out gh_pr_read as the lighter
	// alternative so the model knows to pick.
	tool := &GHPRReviewContextTool{}
	desc := tool.Description()
	for _, want := range []string{"run_bash", "gh_pr_read", "three API calls"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description missing nudge phrase %q\nfull description: %s", want, desc)
		}
	}
}

func TestGHPRReviewContextTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{
		readPRRes:     github.PRDetails{Number: 17, Title: "t", State: "OPEN", URL: "https://x"},
		readPRDiffRes: "diff content",
		listChecksRes: []github.CheckRun{{Name: "ci", State: "SUCCESS"}},
	}
	tool := &GHPRReviewContextTool{Cwd: t.TempDir(), GH: gh}
	out, err := tool.Execute(context.Background(), `{"ref":"17"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "## state") {
		t.Errorf("expected ## state header in output:\n%s", out)
	}
	if !strings.Contains(out, "number=17") {
		t.Errorf("expected number=17 in output:\n%s", out)
	}
}

func TestRenderPRReviewContext_NotFoundShortCircuits(t *testing.T) {
	// NotFound rendering should NOT include the pr/checks/diff
	// sections — they'd be empty and add noise. State header only.
	out := renderPRReviewContext(PRReviewContext{Ref: "999", NotFound: true})
	if !strings.Contains(out, "not_found=true") {
		t.Errorf("expected not_found=true: %q", out)
	}
	if strings.Contains(out, "## pr") || strings.Contains(out, "## diff") {
		t.Errorf("expected NotFound to omit pr/diff sections: %q", out)
	}
}

func TestUpdatePR_ValidationFailsBeforeNetwork(t *testing.T) {
	gh := &fakeGH{}
	// Title with trailing period — validation must fail and the
	// client must NOT be dialed.
	res, err := UpdatePR(context.Background(), gh, github.UpdatePRRequest{
		Ref: "17", Title: "refresh.", Body: "details",
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if res.Updated {
		t.Errorf("must not update on validation failure: %+v", res)
	}
	if res.ValidationErr == "" {
		t.Errorf("expected validation error")
	}
	if gh.updatePRReq.Ref != "" {
		t.Errorf("UpdatePR client must not be called on validation fail; got Ref=%q", gh.updatePRReq.Ref)
	}
}

func TestUpdatePR_EmptyBodyRejected(t *testing.T) {
	// Empty body is the silent footgun: a successful gh call
	// with empty body clobbers the existing PR description.
	// Validation catches it in Go.
	gh := &fakeGH{}
	res, err := UpdatePR(context.Background(), gh, github.UpdatePRRequest{
		Ref: "17", Title: "refreshed title", Body: "   ",
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if res.Updated {
		t.Errorf("must not update with empty body: %+v", res)
	}
	if !strings.Contains(res.ValidationErr, "body is empty") {
		t.Errorf("expected body-empty error, got %q", res.ValidationErr)
	}
}

func TestUpdatePR_HappyPath(t *testing.T) {
	gh := &fakeGH{updatePRRes: github.UpdatePRResult{
		URL: "https://github.com/o/r/pull/17", Number: 17,
	}}
	res, err := UpdatePR(context.Background(), gh, github.UpdatePRRequest{
		Ref: "17", Title: "refreshed scope", Body: "## Summary\n\n- bullet",
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if !res.Updated {
		t.Errorf("expected Updated=true: %+v", res)
	}
	if res.URL != "https://github.com/o/r/pull/17" || res.Number != 17 {
		t.Errorf("URL/Number not populated: %+v", res)
	}
	// Verify the request was forwarded with the validated payload.
	if gh.updatePRReq.Title != "refreshed scope" {
		t.Errorf("title not forwarded: %+v", gh.updatePRReq)
	}
}

func TestUpdatePR_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{updatePRErr: github.ErrGhUnavailable}
	res, err := UpdatePR(context.Background(), gh, github.UpdatePRRequest{
		Ref: "17", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if !res.GhUnavailable {
		t.Errorf("expected GhUnavailable=true: %+v", res)
	}
}

func TestUpdatePR_NotFoundSurfaced(t *testing.T) {
	gh := &fakeGH{updatePRErr: github.ErrPRNotFound}
	res, err := UpdatePR(context.Background(), gh, github.UpdatePRRequest{
		Ref: "999", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if !res.NotFound {
		t.Errorf("expected NotFound=true: %+v", res)
	}
}

func TestUpdatePR_GenericGhErrorSurfaced(t *testing.T) {
	gh := &fakeGH{updatePRErr: errors.New("rate limited")}
	res, err := UpdatePR(context.Background(), gh, github.UpdatePRRequest{
		Ref: "17", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if !strings.Contains(res.GhError, "rate limited") {
		t.Errorf("GhError should surface verbatim cause: %q", res.GhError)
	}
}

func TestGHPRUpdateTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{updatePRRes: github.UpdatePRResult{
		URL: "https://github.com/o/r/pull/17", Number: 17,
	}}
	tool := &GHPRUpdateTool{Cwd: t.TempDir(), GH: gh}
	out, err := tool.Execute(context.Background(),
		`{"ref":"17","title":"refreshed","body":"new body"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "updated=true url=") {
		t.Errorf("expected updated=true with url, got:\n%s", out)
	}
}

func TestRenderPRUpdateResult_NotFoundShape(t *testing.T) {
	out := renderPRUpdateResult(PRUpdateResult{NotFound: true})
	if !strings.Contains(out, "reason=not_found") {
		t.Errorf("missing not_found reason: %q", out)
	}
	// The hint pointing at /git-create-pr is the cross-family
	// nudge — losing it would leave a user wondering what to do
	// when their ref doesn't match an existing PR.
	if !strings.Contains(out, "/git-create-pr") {
		t.Errorf("expected /git-create-pr hint in NotFound shape: %q", out)
	}
}

func TestRenderPRReviewContext_FailingChecksInState(t *testing.T) {
	// FailingChecks must surface in ## state — the slash command
	// reads it from there to flag broken CI at the top of the
	// review before the model even sees the diff.
	out := renderPRReviewContext(PRReviewContext{
		Ref: "17",
		PR:  github.PRDetails{Number: 17, State: "OPEN"},
		Checks: []github.CheckRun{
			{Name: "lint", Conclusion: "FAILURE"},
		},
		FailingChecks: []string{"lint"},
	})
	if !strings.Contains(out, "failing_checks=lint") {
		t.Errorf("expected failing_checks=lint in ## state:\n%s", out)
	}
	// And the checks.summary should report fail=1.
	if !strings.Contains(out, "fail=1") {
		t.Errorf("expected fail=1 in summary:\n%s", out)
	}
}

func TestRenderPRContext_StateBlockFirst(t *testing.T) {
	snap := PRContext{
		ResolvedBase:  "main",
		CurrentBranch: "feature/x",
		AheadCount:    3,
		GhAvailable:   true,
		PushedToOrigin: true,
		BaseResolution: "explicit",
	}
	out := renderPRContext(snap)
	if !strings.HasPrefix(out, "## state\n") {
		t.Errorf("expected ## state block first, got: %q", out)
	}
	for _, frag := range []string{
		"resolved_base=main",
		"current_branch=feature/x",
		"ahead_count=3",
		"gh_available=true",
		"pushed_to_origin=true",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("expected %q in render, got: %q", frag, out)
		}
	}
}

// gitRun is a test helper for non-commit git operations that mirrors
// the existing gitInit/gitCommit helpers. Keeps test setup readable
// without per-test exec.Command boilerplate.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
