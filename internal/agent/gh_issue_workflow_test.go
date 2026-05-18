package agent

import (
	"context"
	"errors"
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
	tool := &GHIssueReadTool{Cwd: t.TempDir(), GH: gh}
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
	tool := &GHIssueReadTool{Cwd: t.TempDir(), GH: gh}
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
	tool := &GHIssueReadTool{Cwd: t.TempDir(), GH: gh}
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
	tool := &GHIssueListTool{Cwd: t.TempDir(), GH: gh}
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
	tool := &GHIssueListTool{Cwd: t.TempDir(), GH: gh}
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
