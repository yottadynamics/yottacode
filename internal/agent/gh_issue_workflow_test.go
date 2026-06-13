package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/github"
)

func TestBuildIssueReadContext_HappyPath(t *testing.T) {
	gh := &fakeGH{
		readIssueRes: github.IssueDetails{
			Number: 42, Title: "Add caching", State: "OPEN",
			Author: "reporter", URL: "https://github.com/o/r/issues/42",
			Body:      "We need a cache.\nDetails inline.",
			Labels:    []string{"bug", "priority-high"},
			Assignees: []string{"octocat"},
			Comments: []github.IssueComment{
				{Author: "alice", Body: "Confirmed locally", Created: time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)},
			},
		},
	}
	snap := BuildIssueReadContext(context.Background(), gh, 42, 0)
	if snap.NotFound || snap.GhUnavailable {
		t.Errorf("expected found+available; got %+v", snap)
	}
	if snap.Issue.Number != 42 || snap.Issue.Title != "Add caching" {
		t.Errorf("issue metadata not populated: %+v", snap.Issue)
	}
	if gh.readIssueReq.Number != 42 {
		t.Errorf("Number not propagated to request: %d", gh.readIssueReq.Number)
	}
}

func TestBuildIssueReadContext_NotFoundSurfaced(t *testing.T) {
	gh := &fakeGH{readIssueErr: github.ErrIssueNotFound}
	snap := BuildIssueReadContext(context.Background(), gh, 999, 0)
	if !snap.NotFound {
		t.Errorf("expected NotFound=true; got %+v", snap)
	}
}

func TestBuildIssueReadContext_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{readIssueErr: github.ErrGhUnavailable}
	snap := BuildIssueReadContext(context.Background(), gh, 42, 0)
	if !snap.GhUnavailable {
		t.Errorf("expected GhUnavailable=true; got %+v", snap)
	}
}

func TestBuildIssueReadContext_GenericErrorSurfaced(t *testing.T) {
	gh := &fakeGH{readIssueErr: errors.New("api: 500")}
	snap := BuildIssueReadContext(context.Background(), gh, 42, 0)
	if snap.NotFound || snap.GhUnavailable {
		t.Errorf("generic error should not flip typed flags: %+v", snap)
	}
	if !strings.Contains(snap.FetchErr, "500") {
		t.Errorf("FetchErr should carry the message; got %q", snap.FetchErr)
	}
}

func TestBuildIssueReadContext_MaxCommentsPropagates(t *testing.T) {
	gh := &fakeGH{}
	BuildIssueReadContext(context.Background(), gh, 42, 5)
	if gh.readIssueReq.MaxComments != 5 {
		t.Errorf("MaxComments not propagated: %d", gh.readIssueReq.MaxComments)
	}
}

func TestGHIssueReadTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{
		readIssueRes: github.IssueDetails{
			Number: 42, Title: "issue title", State: "OPEN", Body: "body text",
			Labels: []string{"bug"},
		},
	}
	tool := &GHIssueReadTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	out, err := tool.Execute(context.Background(), `{"number":42}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"## state", "## issue", "number=42", "title=issue title", "labels=bug", "body=|"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, out)
		}
	}
}

func TestGHIssueReadTool_RendersCommentsWhenPresent(t *testing.T) {
	gh := &fakeGH{
		readIssueRes: github.IssueDetails{
			Number: 42, Title: "t", State: "OPEN",
			Comments: []github.IssueComment{
				{Author: "alice", Body: "first comment", Created: time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)},
				{Author: "bob", Body: "second\nmultiline", Created: time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)},
			},
		},
	}
	tool := &GHIssueReadTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	out, err := tool.Execute(context.Background(), `{"number":42}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"## comments", "@alice", "first comment", "@bob", "multiline"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, out)
		}
	}
}

func TestGHIssueReadTool_RejectsMissingNumber(t *testing.T) {
	gh := &fakeGH{}
	tool := &GHIssueReadTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "number is required") {
		t.Errorf("expected number-required error; got %v", err)
	}
	if gh.readIssueReq.Number != 0 {
		t.Errorf("ReadIssue should not have been called with missing number")
	}
}

func TestGHIssueReadTool_PreviewCall(t *testing.T) {
	tool := &GHIssueReadTool{}
	if got := tool.PreviewCall(""); got != "gh_issue_read()" {
		t.Errorf("empty args preview = %q", got)
	}
	if got := tool.PreviewCall(`{"number":42}`); got != "gh_issue_read(number=42)" {
		t.Errorf("number-only preview = %q", got)
	}
	if got := tool.PreviewCall(`{"number":42,"max_comments":5}`); got != "gh_issue_read(number=42, max_comments=5)" {
		t.Errorf("with max_comments preview = %q", got)
	}
}

func TestGHIssueReadTool_NotApprovalRequired(t *testing.T) {
	tool := &GHIssueReadTool{}
	if tool.RequiresApproval("") {
		t.Errorf("gh_issue_read is read-only; must not require approval")
	}
}

func TestGHIssueReadTool_DescriptionNudgesAwayFromBash(t *testing.T) {
	tool := &GHIssueReadTool{}
	desc := tool.Description()
	for _, want := range []string{"run_bash", "gh issue view"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description missing nudge phrase %q\nfull: %s", want, desc)
		}
	}
}

func TestBuildIssueListContext_HappyPath(t *testing.T) {
	gh := &fakeGH{
		listIssuesRes: []github.IssueSummary{
			{Number: 1, Title: "first issue", Author: "alice", Labels: []string{"bug"}},
			{Number: 2, Title: "second issue", Author: "bob", Assignees: []string{"carol"}},
		},
	}
	snap := BuildIssueListContext(context.Background(), gh, github.ListIssuesRequest{
		Labels: []string{"bug"},
	})
	if snap.GhUnavailable {
		t.Errorf("expected available; got %+v", snap)
	}
	if len(snap.Issues) != 2 {
		t.Errorf("expected 2 issues; got %d", len(snap.Issues))
	}
	if !sliceEqual(gh.listIssuesReq.Labels, []string{"bug"}) {
		t.Errorf("Labels not propagated: %v", gh.listIssuesReq.Labels)
	}
}

func TestBuildIssueListContext_EmptyResultIsValid(t *testing.T) {
	gh := &fakeGH{listIssuesRes: nil}
	snap := BuildIssueListContext(context.Background(), gh, github.ListIssuesRequest{})
	if snap.GhUnavailable || snap.FetchErr != "" {
		t.Errorf("empty result should not set flags: %+v", snap)
	}
	if len(snap.Issues) != 0 {
		t.Errorf("expected 0 issues; got %d", len(snap.Issues))
	}
}

func TestBuildIssueListContext_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{listIssuesErr: github.ErrGhUnavailable}
	snap := BuildIssueListContext(context.Background(), gh, github.ListIssuesRequest{})
	if !snap.GhUnavailable {
		t.Errorf("expected GhUnavailable=true; got %+v", snap)
	}
}

func TestGHIssueListTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{
		listIssuesRes: []github.IssueSummary{
			{Number: 1, Title: "first", Author: "alice", Labels: []string{"bug"}},
		},
	}
	tool := &GHIssueListTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	out, err := tool.Execute(context.Background(), `{"labels":["bug"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"## state", "count=1", "filter_labels=bug", "## issues", "#1 first", "@alice", "[bug]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, out)
		}
	}
}

func TestGHIssueListTool_RendersEmptyResultExplicitly(t *testing.T) {
	gh := &fakeGH{listIssuesRes: nil}
	tool := &GHIssueListTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "(none)") {
		t.Errorf("expected explicit '(none)' marker for empty list:\n%s", out)
	}
}

func TestGHIssueListTool_PreviewCall(t *testing.T) {
	tool := &GHIssueListTool{}
	if got := tool.PreviewCall(""); got != "gh_issue_list()" {
		t.Errorf("empty args preview = %q", got)
	}
	if got := tool.PreviewCall(`{"labels":["bug","urgent"],"assignee":"octocat"}`); got != "gh_issue_list(labels=bug+urgent, assignee=octocat)" {
		t.Errorf("filter preview = %q", got)
	}
}

func TestGHIssueListTool_NotApprovalRequired(t *testing.T) {
	tool := &GHIssueListTool{}
	if tool.RequiresApproval("") {
		t.Errorf("gh_issue_list is read-only; must not require approval")
	}
}

func TestGHIssueListTool_DescriptionNudgesAwayFromBash(t *testing.T) {
	tool := &GHIssueListTool{}
	desc := tool.Description()
	for _, want := range []string{"run_bash", "gh issue list"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description missing nudge phrase %q\nfull: %s", want, desc)
		}
	}
}

func TestFormatIssueSummaryLine(t *testing.T) {
	cases := []struct {
		name string
		in   github.IssueSummary
		want string
	}{
		{
			name: "full fixture",
			in: github.IssueSummary{
				Number: 1, Title: "fix it", Author: "alice",
				Labels: []string{"bug"}, Assignees: []string{"bob", "carol"},
			},
			want: "- #1 fix it (by @alice) [bug] → @bob, @carol\n",
		},
		{
			name: "minimal fixture",
			in:   github.IssueSummary{Number: 2, Title: "minimal"},
			want: "- #2 minimal\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatIssueSummaryLine(tc.in)
			if got != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- /git-create-issue: BuildIssueContext + CreateIssue -------------

func TestBuildIssueContext_LoadsIssueTemplate(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	gitRun(t, tmp, "remote", "add", "origin", "git@github.com:octo/widgets.git")
	if err := os.MkdirAll(filepath.Join(tmp, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	template := "## Summary\n\n## Checklist\n"
	if err := os.WriteFile(filepath.Join(tmp, ".github", "ISSUE_TEMPLATE.md"), []byte(template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	snap, err := BuildIssueContext(context.Background(), tmp)
	if err != nil {
		t.Fatalf("BuildIssueContext: %v", err)
	}
	if snap.Owner != "octo" || snap.Repo != "widgets" {
		t.Errorf("owner/repo = %q/%q; want octo/widgets", snap.Owner, snap.Repo)
	}
	if snap.IssueTemplatePath != ".github/ISSUE_TEMPLATE.md" {
		t.Errorf("template path = %q", snap.IssueTemplatePath)
	}
	if !strings.Contains(snap.IssueTemplate, "## Checklist") {
		t.Errorf("expected template body in snapshot: %q", snap.IssueTemplate)
	}
	// GhAvailable is intentionally not asserted — it reflects the
	// host's token chain, which varies across dev machines and CI.
}

func TestBuildIssueContext_NoTemplate(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	gitRun(t, tmp, "remote", "add", "origin", "git@github.com:octo/widgets.git")

	snap, err := BuildIssueContext(context.Background(), tmp)
	if err != nil {
		t.Fatalf("BuildIssueContext: %v", err)
	}
	if snap.IssueTemplate != "" || snap.IssueTemplatePath != "" {
		t.Errorf("expected empty template fields: %+v", snap)
	}
}

func TestBuildIssueContext_NoOriginErrors(t *testing.T) {
	// Unlike BuildPRContext (which degrades informationally), issue
	// context is meaningless without owner/repo — a missing origin
	// remote is a hard error the tool surfaces directly.
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	if _, err := BuildIssueContext(context.Background(), tmp); err == nil {
		t.Errorf("expected error with no origin remote")
	}
}

func TestRenderIssueContext_StateAndTemplate(t *testing.T) {
	out := renderIssueContext(IssueContext{
		Owner: "octo", Repo: "widgets", GhAvailable: false,
		IssueTemplate: "## Summary\nbody", IssueTemplatePath: ".github/ISSUE_TEMPLATE/bug_report.md",
		IssueTemplateChoices: []string{"bug_report.md", "feature_request.md"},
	})
	for _, want := range []string{
		"## state",
		"owner=octo",
		"repo=widgets",
		"gh_available=false", // the flag the directive's draft-only branch reads
		"## template",
		"path=.github/ISSUE_TEMPLATE/bug_report.md",
		"choices=bug_report.md,feature_request.md",
		"  ## Summary",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderIssueContext_SingleTemplateNoChoicesLine(t *testing.T) {
	out := renderIssueContext(IssueContext{
		Owner: "octo", Repo: "widgets",
		IssueTemplate: "body", IssueTemplatePath: "ISSUE_TEMPLATE.md",
		IssueTemplateChoices: []string{"ISSUE_TEMPLATE.md"},
	})
	if strings.Contains(out, "choices=") {
		t.Errorf("choices= must only render when there are alternatives:\n%s", out)
	}
}

func TestLoadIssueTemplate_PrefersTemplateDirOverLegacy(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".github", "ISSUE_TEMPLATE")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "bug_report.md", "## Bug body\n")
	writeFile(t, filepath.Join(tmp, ".github"), "ISSUE_TEMPLATE.md", "## Legacy body\n")

	path, content, _ := loadIssueTemplate(tmp)
	if path != filepath.Join(".github", "ISSUE_TEMPLATE", "bug_report.md") {
		t.Errorf("path = %q; want the template-dir file", path)
	}
	if !strings.Contains(content, "Bug body") {
		t.Errorf("content = %q; want the template-dir body", content)
	}
}

func TestLoadIssueTemplate_AlphabeticalPickListsChoices(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".github", "ISSUE_TEMPLATE")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "feature_request.md", "## Feature\n")
	writeFile(t, dir, "bug_report.md", "## Bug\n")

	path, _, choices := loadIssueTemplate(tmp)
	if path != filepath.Join(".github", "ISSUE_TEMPLATE", "bug_report.md") {
		t.Errorf("path = %q; want alphabetical first (bug_report.md)", path)
	}
	if !sliceEqual(choices, []string{"bug_report.md", "feature_request.md"}) {
		t.Errorf("choices = %v", choices)
	}
}

func TestLoadIssueTemplate_SupportsMarkdownAndYAMLForms(t *testing.T) {
	// GitHub's chooser can mix Markdown templates and YAML issue forms. The
	// context tool must expose both as selectable issue-creation targets instead
	// of silently dropping the forms.
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".github", "ISSUE_TEMPLATE")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "bug_report.yml", "name: Bug report\ndescription: Report a bug\ntitle: '[Bug]: '\nlabels: [bug]\nbody:\n  - type: textarea\n    id: what-happened\n    attributes:\n      label: What happened?\n      description: Tell us what broke.\n    validations:\n      required: true\n")
	writeFile(t, dir, "feature_request.md", "---\nname: Feature\n---\n\n## Feature body\n")

	templates, _, _ := loadIssueTemplates(tmp)
	if len(templates) != 2 {
		t.Fatalf("expected 2 templates; got %d: %+v", len(templates), templates)
	}
	if templates[0].Kind != "issue_form" || templates[0].Name != "Bug report" {
		t.Errorf("first template = %+v; want rendered YAML bug form", templates[0])
	}
	if !sliceEqual(templates[0].Labels, []string{"bug"}) {
		t.Errorf("labels = %v", templates[0].Labels)
	}
	if !strings.Contains(templates[0].Content, "## What happened?") || !strings.Contains(templates[0].Content, "Required") {
		t.Errorf("YAML form was not rendered into useful Markdown:\n%s", templates[0].Content)
	}
	if templates[1].Kind != "markdown" || !strings.Contains(templates[1].Content, "Feature body") {
		t.Errorf("second template = %+v; want Markdown feature template", templates[1])
	}
}

func TestLoadIssueTemplate_ConfigContactLinksAndBlankIssuePolicy(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".github", "ISSUE_TEMPLATE")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "config.yml", "blank_issues_enabled: false\ncontact_links:\n  - name: Documentation\n    url: https://example.com/docs\n    about: Read first\n")

	templates, links, blank := loadIssueTemplates(tmp)
	if len(templates) != 0 {
		t.Errorf("config-only layout should not create templates: %+v", templates)
	}
	if blank {
		t.Errorf("blank issues should be disabled by config")
	}
	if len(links) != 1 || links[0].Name != "Documentation" || links[0].URL != "https://example.com/docs" {
		t.Errorf("contact links not parsed: %+v", links)
	}
}

func TestLoadIssueTemplate_YAMLFormsOnlyNowYieldTemplate(t *testing.T) {
	// GitHub issue forms are field specs, but yottacode can render them into
	// Markdown and pass that body through the normal Create Issue API.
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".github", "ISSUE_TEMPLATE")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "config.yml", "blank_issues_enabled: false\n")
	writeFile(t, dir, "bug_report.yml", "name: Bug report\nbody:\n  - type: input\n    id: version\n    attributes:\n      label: Version\n")
	writeFile(t, dir, "feature_request.yml", "name: Feature\nlabels: enhancement\nbody:\n  - type: dropdown\n    id: contribute\n    attributes:\n      label: Contribute?\n      options:\n        - Yes\n        - No\n")

	path, content, choices := loadIssueTemplate(tmp)
	if path != filepath.Join(".github", "ISSUE_TEMPLATE", "bug_report.yml") {
		t.Errorf("path = %q; want first YAML form", path)
	}
	if !strings.Contains(content, "## Version") {
		t.Errorf("content = %q; want rendered YAML form", content)
	}
	if !sliceEqual(choices, []string{"bug_report.yml", "feature_request.yml"}) {
		t.Errorf("choices = %v", choices)
	}
}

func TestRenderIssueContext_RendersTemplatesAndContactLinks(t *testing.T) {
	out := renderIssueContext(IssueContext{
		Owner: "octo", Repo: "widgets", GhAvailable: true,
		IssueTemplates: []IssueTemplate{{
			Name: "Bug report", Kind: "issue_form", Path: ".github/ISSUE_TEMPLATE/bug_report.yml",
			Description: "Report a bug", TitlePrefix: "[Bug]: ", Labels: []string{"bug"}, Content: "## What happened?\n",
		}},
		BlankIssuesEnabled: false,
		ContactLinks:       []IssueContactLink{{Name: "Security", URL: "https://example.com/security", About: "Report privately"}},
	})
	for _, want := range []string{
		"## templates",
		"name=Bug report",
		"kind=issue_form",
		"title_prefix=[Bug]: ",
		"labels=bug",
		"## blank_issue",
		"enabled=false",
		"## contact_links",
		"Report privately",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestLoadIssueTemplate_LowercaseLegacyPath(t *testing.T) {
	// Regression: GitHub matches legacy template filenames
	// case-insensitively; on case-sensitive filesystems the lookup
	// must try both casings. The path is asserted case-insensitively
	// because on a case-insensitive filesystem (macOS APFS — the CI
	// runners) the uppercase candidate probe legitimately opens this
	// lowercase file first, so the reported casing differs by OS. A
	// single-casing candidate list would return "" on Linux, which
	// still fails this assertion — the regression stays covered.
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(tmp, ".github"), "issue_template.md", "## Lowercase body\n")

	path, content, _ := loadIssueTemplate(tmp)
	if !strings.EqualFold(path, ".github/issue_template.md") {
		t.Errorf("path = %q; want .github/issue_template.md (any casing)", path)
	}
	if !strings.Contains(content, "Lowercase body") {
		t.Errorf("content = %q", content)
	}
}

func TestStripTemplateFrontmatter(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"strips block",
			"---\nname: Bug report\nlabels: [bug]\n---\n\n## Describe the bug\n",
			"## Describe the bug\n"},
		{"no frontmatter untouched",
			"## Describe the bug\n",
			"## Describe the bug\n"},
		{"unterminated block untouched",
			"---\nname: Bug report\n",
			"---\nname: Bug report\n"},
		{"horizontal rule mid-body untouched",
			"intro\n---\nrest\n",
			"intro\n---\nrest\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripTemplateFrontmatter(tc.in); got != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}
}

func TestCreateIssue_ValidationFailsBeforeNetwork(t *testing.T) {
	gh := &fakeGH{}
	res, err := CreateIssue(context.Background(), gh, github.CreateIssueRequest{Title: "fix."})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if res.Created {
		t.Errorf("must not be created on validation failure: %+v", res)
	}
	if res.ValidationErr == "" {
		t.Errorf("expected validation error")
	}
	// Critical: the client was NEVER dialed. Validation failure must
	// not produce a network call.
	if gh.createIssueCalls != 0 {
		t.Errorf("expected 0 client calls on validation fail; got %d", gh.createIssueCalls)
	}
}

func TestCreateIssue_HappyPath(t *testing.T) {
	gh := &fakeGH{createIssueRes: github.CreateIssueResult{URL: "https://github.com/o/r/issues/7", Number: 7}}
	res, err := CreateIssue(context.Background(), gh, github.CreateIssueRequest{
		Title: "add caching", Body: "details",
		Labels: []string{"bug"}, Assignees: []string{"octocat"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if !res.Created {
		t.Errorf("expected Created=true: %+v", res)
	}
	if res.URL != "https://github.com/o/r/issues/7" || res.Number != 7 {
		t.Errorf("URL/Number = %q/%d", res.URL, res.Number)
	}
	// Request fields must reach the adapter unmodified.
	req := gh.createIssueReq
	if req.Title != "add caching" || req.Body != "details" ||
		!sliceEqual(req.Labels, []string{"bug"}) ||
		!sliceEqual(req.Assignees, []string{"octocat"}) {
		t.Errorf("request not propagated: %+v", req)
	}
}

func TestCreateIssue_GhUnavailableSurfaced(t *testing.T) {
	gh := &fakeGH{createIssueErr: github.ErrGhUnavailable}
	res, err := CreateIssue(context.Background(), gh, github.CreateIssueRequest{Title: "add caching"})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if !res.GhUnavailable {
		t.Errorf("expected GhUnavailable=true: %+v", res)
	}
	if res.Created {
		t.Errorf("must not be Created when gh unavailable: %+v", res)
	}
}

func TestCreateIssue_GenericGhErrorSurfaced(t *testing.T) {
	gh := &fakeGH{createIssueErr: errors.New("rate limited")}
	res, err := CreateIssue(context.Background(), gh, github.CreateIssueRequest{Title: "add caching"})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if !strings.Contains(res.GhError, "rate limited") {
		t.Errorf("GhError should carry the message; got %q", res.GhError)
	}
	if res.Created || res.GhUnavailable {
		t.Errorf("generic error must not flip other flags: %+v", res)
	}
}

func TestRenderIssueCreateResult_Envelopes(t *testing.T) {
	// The directive branches on these exact discriminators; they
	// must stay in lockstep with renderPRCreateResult's shape.
	cases := []struct {
		name string
		res  IssueCreateResult
		want []string
	}{
		{"created", IssueCreateResult{Created: true, URL: "https://github.com/o/r/issues/7", Number: 7},
			[]string{"created=true url=https://github.com/o/r/issues/7 number=7"}},
		{"validation", IssueCreateResult{ValidationErr: "title is empty"},
			[]string{"created=false reason=validation", "error=title is empty"}},
		{"gh_unavailable", IssueCreateResult{GhUnavailable: true},
			[]string{"created=false reason=gh_unavailable"}},
		{"gh_error", IssueCreateResult{GhError: "api: 502"},
			[]string{"created=false reason=gh_error", "--- gh output ---", "api: 502"}},
		{"unknown", IssueCreateResult{},
			[]string{"created=false reason=unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderIssueCreateResult(tc.res)
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("envelope missing %q\nfull:\n%s", want, out)
				}
			}
		})
	}
}

func TestGHIssueCreateTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{createIssueRes: github.CreateIssueResult{URL: "https://github.com/o/r/issues/3", Number: 3}}
	tool := &GHIssueCreateTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	out, err := tool.Execute(context.Background(),
		`{"title":"add caching","body":"why","labels":["bug"],"assignees":["octocat"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"created=true", "url=https://github.com/o/r/issues/3", "number=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull:\n%s", want, out)
		}
	}
	if gh.createIssueReq.Title != "add caching" || !sliceEqual(gh.createIssueReq.Labels, []string{"bug"}) {
		t.Errorf("args not propagated to request: %+v", gh.createIssueReq)
	}
}

func TestGHIssueCreateTool_RequiresApproval(t *testing.T) {
	tool := &GHIssueCreateTool{}
	if !tool.RequiresApproval("{}") {
		t.Errorf("gh_issue_create must always require approval")
	}
}

func TestGHIssueCreateTool_PreviewCall(t *testing.T) {
	tool := &GHIssueCreateTool{}
	got := tool.PreviewCall(`{"title":"add caching","labels":["bug","ui"]}`)
	if !strings.Contains(got, `title="add caching"`) || !strings.Contains(got, "labels=bug+ui") {
		t.Errorf("preview = %q", got)
	}
}

func TestGHIssueContextTool_RoundsThroughTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	gitRun(t, tmp, "remote", "add", "origin", "git@github.com:octo/widgets.git")

	tool := &GHIssueContextTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// gh_available's value depends on the host's token chain, so
	// assert only that the field is present.
	for _, want := range []string{"## state", "owner=octo", "repo=widgets", "gh_available="} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull:\n%s", want, out)
		}
	}
}
