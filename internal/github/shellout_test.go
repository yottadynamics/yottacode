package github

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildCreateArgs_MinimalRequest(t *testing.T) {
	args := buildCreateArgs(CreatePRRequest{
		Base:  "main",
		Title: "fix the thing",
		Body:  "details",
	})
	// Order matters — gh accepts named flags in any order, but pinning
	// it here lets the test catch accidental rearrangements that might
	// change observed shell-invocation cost or break test fixtures.
	want := []string{"pr", "create", "--base", "main", "--title", "fix the thing", "--body-file", "-"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v; want %v", args, want)
	}
}

func TestBuildCreateArgs_DraftFlag(t *testing.T) {
	args := buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Draft: true,
	})
	if !slices.Contains(args, "--draft") {
		t.Errorf("draft flag missing: %v", args)
	}
}

func TestBuildCreateArgs_HeadFlag(t *testing.T) {
	args := buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Head: "feature/x",
	})
	if !slices.Contains(args, "--head") || !slices.Contains(args, "feature/x") {
		t.Errorf("head flag missing: %v", args)
	}
}

func TestBuildCreateArgs_OwnerRepoOnlyWhenBoth(t *testing.T) {
	// Only one side populated: skip --repo so gh's cwd-inference path
	// stays in play. Setting --repo with a half-empty value would
	// produce a useless "yotta/" arg.
	args := buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Owner: "yotta",
	})
	if slices.Contains(args, "--repo") {
		t.Errorf("expected no --repo with only Owner set: %v", args)
	}
	args = buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Owner: "yotta", Repo: "code",
	})
	if !slices.Contains(args, "--repo") || !slices.Contains(args, "yotta/code") {
		t.Errorf("expected --repo yotta/code: %v", args)
	}
}

func TestValidateCreateRequest_Required(t *testing.T) {
	cases := []struct {
		name string
		req  CreatePRRequest
		want string
	}{
		{"missing base", CreatePRRequest{Title: "t", Body: "b"}, "Base"},
		{"missing title", CreatePRRequest{Base: "main", Body: "b"}, "Title"},
		{"missing body", CreatePRRequest{Base: "main", Title: "t"}, "Body"},
		{"whitespace title rejected", CreatePRRequest{Base: "main", Title: "   ", Body: "b"}, "Title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCreateRequest(tc.req)
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateCreateRequest_Happy(t *testing.T) {
	err := validateCreateRequest(CreatePRRequest{
		Base: "main", Title: "fix the thing", Body: "details",
	})
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestParsePRURL_Standalone(t *testing.T) {
	url := parsePRURL("https://github.com/owner/repo/pull/42")
	if url != "https://github.com/owner/repo/pull/42" {
		t.Errorf("got %q", url)
	}
}

func TestParsePRURL_WithSurroundingChrome(t *testing.T) {
	// gh sometimes wraps the URL with a creating-status line or a
	// tip; the regex must still pick it out cleanly.
	stdout := "Creating pull request for feature/x into main in owner/repo\n\nhttps://github.com/owner/repo/pull/7\n"
	url := parsePRURL(stdout)
	if url != "https://github.com/owner/repo/pull/7" {
		t.Errorf("got %q", url)
	}
}

func TestParsePRURL_AbsentReturnsEmpty(t *testing.T) {
	if url := parsePRURL("no pr was created"); url != "" {
		t.Errorf("expected empty, got %q", url)
	}
}

func TestBuildPRViewArgs_ExplicitRef(t *testing.T) {
	args := buildPRViewArgs(ReadPRRequest{Ref: "17"})
	// Order: subcommand → ref → flags. Pinning this lets the
	// test catch silent shape regressions ahead of integration.
	want := []string{"pr", "view", "17", "--json", prViewFields}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v; want %v", args, want)
	}
}

func TestBuildPRViewArgs_NoRefSkipsArg(t *testing.T) {
	// Empty ref means "use current branch" — let gh's default apply
	// rather than passing an empty string positional that gh would
	// reject. Verify no empty-string arg leaks through.
	args := buildPRViewArgs(ReadPRRequest{})
	for _, a := range args {
		if a == "" {
			t.Errorf("unexpected empty arg: %v", args)
		}
	}
	if slices.Contains(args, "--json") == false {
		t.Errorf("missing --json: %v", args)
	}
}

func TestBuildPRViewArgs_RepoFlag(t *testing.T) {
	args := buildPRViewArgs(ReadPRRequest{Ref: "17", Owner: "o", Repo: "r"})
	if !slices.Contains(args, "--repo") || !slices.Contains(args, "o/r") {
		t.Errorf("expected --repo o/r in args: %v", args)
	}
}

func TestShortcutPRTarget_FullInfoShortCircuits(t *testing.T) {
	// Owner + Repo + numeric Ref is the only shape that lets us
	// skip the `gh pr view` lookup. Verify the policy doesn't
	// silently expand or contract.
	owner, repo, number, ok := shortcutPRTarget(UpdatePRRequest{
		Owner: "o", Repo: "r", Ref: "17",
	})
	if !ok {
		t.Fatalf("expected shortcut to fire on full info; got ok=false")
	}
	if owner != "o" || repo != "r" || number != 17 {
		t.Errorf("returned %s/%s#%d; want o/r#17", owner, repo, number)
	}
}

func TestShortcutPRTarget_RequiresAllThreeFields(t *testing.T) {
	cases := []struct {
		name string
		req  UpdatePRRequest
	}{
		{"missing owner", UpdatePRRequest{Repo: "r", Ref: "17"}},
		{"missing repo", UpdatePRRequest{Owner: "o", Ref: "17"}},
		{"branch ref not numeric", UpdatePRRequest{Owner: "o", Repo: "r", Ref: "feature/x"}},
		{"empty ref", UpdatePRRequest{Owner: "o", Repo: "r"}},
		{"whitespace ref", UpdatePRRequest{Owner: "o", Repo: "r", Ref: "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, ok := shortcutPRTarget(tc.req)
			if ok {
				t.Errorf("expected shortcut to decline; got ok=true")
			}
		})
	}
}

func TestParsePRTarget_FromGhJSON(t *testing.T) {
	// Shape matching `gh pr view --json url` — the URL embeds
	// owner/repo/number all three.
	in := `{"url": "https://github.com/yottadynamics/yottacode/pull/17"}`
	got, err := parsePRTarget(in)
	if err != nil {
		t.Fatalf("parsePRTarget: %v", err)
	}
	if got.Owner != "yottadynamics" || got.Repo != "yottacode" || got.Number != 17 {
		t.Errorf("got %+v; want yottadynamics/yottacode#17", got)
	}
}

func TestParsePRTarget_InvalidJSON(t *testing.T) {
	_, err := parsePRTarget("not json")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse pr target") {
		t.Errorf("expected wrapped error, got %v", err)
	}
}

func TestPRTargetFromURL_Variants(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		owner  string
		repo   string
		number int
		ok     bool
	}{
		{"same-repo PR", "https://github.com/yottadynamics/yottacode/pull/17", "yottadynamics", "yottacode", 17, true},
		{"large number", "https://github.com/owner/repo/pull/99999", "owner", "repo", 99999, true},
		{"hyphenated names", "https://github.com/some-org/some-repo/pull/3", "some-org", "some-repo", 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := prTargetFromURL(tc.url)
			if err != nil {
				t.Fatalf("prTargetFromURL(%q): %v", tc.url, err)
			}
			if got.Owner != tc.owner || got.Repo != tc.repo || got.Number != tc.number {
				t.Errorf("got %+v; want %s/%s#%d", got, tc.owner, tc.repo, tc.number)
			}
		})
	}
}

func TestPRTargetFromURL_RejectsNearMisses(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"issue not pull", "https://github.com/o/r/issues/17"},
		{"missing pull segment", "https://github.com/o/r/17"},
		{"non-github host", "https://gitlab.com/o/r/pull/17"},
		{"trailing slash", "https://github.com/o/r/pull/17/"},
		{"missing number", "https://github.com/o/r/pull/"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := prTargetFromURL(tc.url)
			if err == nil {
				t.Errorf("expected error for %q, got nil", tc.url)
			}
		})
	}
}

func TestParsePATCHResponse_PullsURLAndNumber(t *testing.T) {
	// GitHub's REST API PATCH returns the full updated PR. We
	// only care about html_url and number; unknown fields are
	// ignored by json.Unmarshal.
	in := `{
		"number": 17,
		"html_url": "https://github.com/yottadynamics/yottacode/pull/17",
		"title": "irrelevant for this assertion",
		"body": "also irrelevant",
		"state": "open"
	}`
	got := parsePATCHResponse(in)
	if got.URL != "https://github.com/yottadynamics/yottacode/pull/17" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.Number != 17 {
		t.Errorf("Number = %d", got.Number)
	}
}

func TestParsePATCHResponse_MalformedReturnsZero(t *testing.T) {
	// Deliberately swallows JSON errors — callers (UpdatePR)
	// surface "succeeded but no URL" as the failure mode the
	// model sees, matching the CreatePR shape. Same UX whether
	// gh succeeds with junk stdout or fails outright.
	got := parsePATCHResponse("not json")
	if got.URL != "" || got.Number != 0 {
		t.Errorf("expected zero value on malformed input, got %+v", got)
	}
}

func TestBuildPRDiffArgs(t *testing.T) {
	args := buildPRDiffArgs(ReadPRRequest{Ref: "feature/x"})
	want := []string{"pr", "diff", "feature/x"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v; want %v", args, want)
	}
}

func TestClassifyGhNotFound(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"branch with no PR", "no pull requests found for branch \"feature/x\"\n", true},
		{"explicit number missing", "Could not resolve to a PullRequest with the number of 999.\n", true},
		{"random gh error", "HTTP 500: server unavailable\n", false},
		{"empty stderr", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyGhNotFound(tc.body); got != tc.want {
				t.Errorf("got %v want %v for %q", got, tc.want, tc.body)
			}
		})
	}
}

// TestParsePRDetails_FullFixture exercises every field PRDetails
// surfaces against a hand-built JSON sample shaped like real gh
// output. Catches accidental field-name drift between PRDetails
// and ghPRViewJSON, and the labels-flatten and author-pluck
// transformations on the way out.
func TestParsePRDetails_FullFixture(t *testing.T) {
	in := `{
		"number": 17,
		"title": "Add PR review workflow",
		"body": "Long body",
		"state": "OPEN",
		"isDraft": false,
		"baseRefName": "main",
		"headRefName": "feature/review",
		"headRefOid": "abc123def456",
		"mergeable": "MERGEABLE",
		"author": {"login": "octocat"},
		"url": "https://github.com/o/r/pull/17",
		"labels": [{"name": "enhancement"}, {"name": "review"}]
	}`
	got, err := parsePRDetails(in)
	if err != nil {
		t.Fatalf("parsePRDetails: %v", err)
	}
	want := PRDetails{
		Number:    17,
		Title:     "Add PR review workflow",
		Body:      "Long body",
		State:     "OPEN",
		Draft:     false,
		BaseRef:   "main",
		HeadRef:   "feature/review",
		HeadSHA:   "abc123def456",
		Mergeable: "MERGEABLE",
		Author:    "octocat",
		URL:       "https://github.com/o/r/pull/17",
		Labels:    []string{"enhancement", "review"},
	}
	if got.Number != want.Number || got.Title != want.Title || got.State != want.State ||
		got.Draft != want.Draft || got.BaseRef != want.BaseRef || got.HeadRef != want.HeadRef ||
		got.HeadSHA != want.HeadSHA || got.Mergeable != want.Mergeable || got.Author != want.Author ||
		got.URL != want.URL {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if !slices.Equal(got.Labels, want.Labels) {
		t.Errorf("labels = %v; want %v", got.Labels, want.Labels)
	}
}

func TestParsePRDetails_LabelEmptyNamesDropped(t *testing.T) {
	// Defensive: a label with no name field shouldn't surface as
	// an empty string. Keeps downstream code from formatting
	// `[label: ]` rows.
	in := `{"number":1,"labels":[{"name":""},{"name":"good"}]}`
	got, err := parsePRDetails(in)
	if err != nil {
		t.Fatalf("parsePRDetails: %v", err)
	}
	if !slices.Equal(got.Labels, []string{"good"}) {
		t.Errorf("labels = %v; want [good]", got.Labels)
	}
}

func TestParsePRDetails_InvalidJSON(t *testing.T) {
	_, err := parsePRDetails("not json")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse pr view") {
		t.Errorf("expected wrapped parse error, got %v", err)
	}
}

// TestParsePRChecks covers the two-shape rollup: a CheckRun row
// (Status populated) and a StatusContext row (State populated).
// normalizeCheckState chooses whichever is non-empty so callers
// don't have to.
func TestParsePRChecks_TwoShapes(t *testing.T) {
	in := `{
		"statusCheckRollup": [
			{"name": "build", "status": "COMPLETED", "conclusion": "SUCCESS", "startedAt": "2024-01-02T15:00:00Z", "completedAt": "2024-01-02T15:05:00Z"},
			{"name": "ci", "state": "SUCCESS", "conclusion": "SUCCESS"}
		]
	}`
	checks, err := parsePRChecks(in)
	if err != nil {
		t.Fatalf("parsePRChecks: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("got %d checks; want 2", len(checks))
	}
	if checks[0].Name != "build" || checks[0].State != "COMPLETED" {
		t.Errorf("checks[0] = %+v", checks[0])
	}
	if checks[1].Name != "ci" || checks[1].State != "SUCCESS" {
		t.Errorf("checks[1] = %+v (StatusContext shape should fall through to State)", checks[1])
	}
	if checks[0].StartedAt.IsZero() {
		t.Errorf("expected StartedAt populated for completed check; got zero")
	}
	if !checks[1].StartedAt.IsZero() {
		t.Errorf("expected StartedAt zero for status-context with no time; got %v", checks[1].StartedAt)
	}
}

func TestParsePRChecks_EmptyRollupReturnsEmpty(t *testing.T) {
	// A PR with no CI configured returns an empty rollup, not an
	// error. Callers (review tool) format "no checks" when len==0.
	checks, err := parsePRChecks(`{"statusCheckRollup": []}`)
	if err != nil {
		t.Fatalf("parsePRChecks: %v", err)
	}
	if len(checks) != 0 {
		t.Errorf("expected empty slice, got %d", len(checks))
	}
}

func TestParseRFC3339_EmptyReturnsZero(t *testing.T) {
	if !parseRFC3339("").IsZero() {
		t.Error("expected zero time for empty string")
	}
	if !parseRFC3339("garbled-input").IsZero() {
		t.Error("expected zero time for unparseable string")
	}
	parsed := parseRFC3339("2024-01-02T15:04:05Z")
	if parsed.IsZero() {
		t.Error("expected non-zero time for valid RFC3339")
	}
}

func TestParsePRNumber(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"https://github.com/owner/repo/pull/42", 42},
		{"https://github.com/owner/repo/pull/9999", 9999},
		{"", 0},
		{"https://github.com/owner/repo/pull/", 0},
		{"https://github.com/owner/repo/pull/abc", 0},
	}
	for _, tc := range cases {
		if got := parsePRNumber(tc.url); got != tc.want {
			t.Errorf("parsePRNumber(%q) = %d, want %d", tc.url, got, tc.want)
		}
	}
}

