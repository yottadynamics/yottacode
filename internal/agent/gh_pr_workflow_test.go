package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/github"
)

// fakeGH is the test double for github.Interface. Captures the last
// call's request and lets each test pre-set the response.
type fakeGH struct {
	lastReq github.CreatePRRequest
	calls   int
	res     github.CreatePRResult
	err     error
}

func (f *fakeGH) CreatePR(_ context.Context, req github.CreatePRRequest) (github.CreatePRResult, error) {
	f.calls++
	f.lastReq = req
	return f.res, f.err
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
