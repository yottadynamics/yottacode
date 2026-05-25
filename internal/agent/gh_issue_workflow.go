// Issue-workflow tools for the GitHub adapter.
//
// gh_issue_read fetches a single issue (metadata + comments).
// gh_issue_list returns lightweight summaries of open issues.
//
// Both are read-only, no approval. Mirror the gh_pr_read / shape
// so the model brief can describe issue and PR reads with the
// same template (state-flag header, typed snapshot body).

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/github"
)

// GHIssueReadTool wraps Interface.ReadIssue. Single API call (plus
// one comment fetch when MaxComments != -1). The /git-implement-issue
// slash command calls this as its first step; ad-hoc model use is
// the cheap default for any "what does issue 42 say" question.
//
// Read-only, no approval modal. Single API surface, so cheaper than
// gh_pr_review_context — there's no checks/diff equivalent to
// bundle in for issues.
type GHIssueReadTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHIssueReadTool) Name() string { return "gh_issue_read" }

func (t *GHIssueReadTool) Description() string {
	return "Fetch typed metadata for a single GitHub issue: number, " +
		"title, body, state, author, URL, labels, assignees, and the " +
		"most-recent comments (capped). Prefer this over shelling out " +
		"to `gh issue view <n> --json ...` from run_bash whenever the " +
		"goal is reading issue context: faster, no subprocess, " +
		"structured result with state flags the caller can branch on " +
		"(## state flags not_found / gh_unavailable so the caller can " +
		"surface a clean error and stop). Number is required. " +
		"max_comments caps the comment fetch (0 = default 20, -1 = " +
		"skip comments entirely)."
}

func (t *GHIssueReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"number": map[string]any{
				"type":        "integer",
				"description": "Issue number (required).",
			},
			"max_comments": map[string]any{
				"type":        "integer",
				"description": "Comment fetch cap. 0 = default (20). -1 = skip comments. Positive = cap at that many.",
			},
		},
		"required": []string{"number"},
	}
}

func (t *GHIssueReadTool) RequiresApproval(string) bool { return false }

func (t *GHIssueReadTool) PreviewCall(argsJSON string) string {
	var a struct {
		Number      int `json:"number"`
		MaxComments int `json:"max_comments"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Number == 0 {
		return "gh_issue_read()"
	}
	if a.MaxComments != 0 {
		return fmt.Sprintf("gh_issue_read(number=%d, max_comments=%d)", a.Number, a.MaxComments)
	}
	return fmt.Sprintf("gh_issue_read(number=%d)", a.Number)
}

func (t *GHIssueReadTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.GH == nil {
		return "", errors.New("gh_issue_read: no GitHub adapter configured")
	}
	var a struct {
		Number      int `json:"number"`
		MaxComments int `json:"max_comments"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	if a.Number <= 0 {
		return "", fmt.Errorf("gh_issue_read: number is required")
	}
	snap := BuildIssueReadContext(ctx, t.GH, a.Number, a.MaxComments)
	return renderIssueReadContext(snap), nil
}

// IssueReadContext is the typed snapshot gh_issue_read returns.
// State flags follow the same pattern as PR snapshots (NotFound,
// GhUnavailable, FetchErr) so callers branch on flags before
// reading the issue body.
type IssueReadContext struct {
	Number        int
	NotFound      bool
	GhUnavailable bool
	FetchErr      string

	Issue github.IssueDetails
}

// BuildIssueReadContext is the deterministic core of gh_issue_read.
// Wraps one ReadIssue call; folds typed errors into the snapshot.
// Doesn't return an error itself — every failure shape is captured
// in the snapshot so callers can branch on flags.
func BuildIssueReadContext(ctx context.Context, client github.Interface, number, maxComments int) IssueReadContext {
	snap := IssueReadContext{Number: number}
	issue, err := client.ReadIssue(ctx, github.ReadIssueRequest{
		Number:      number,
		MaxComments: maxComments,
	})
	if errors.Is(err, github.ErrGhUnavailable) {
		snap.GhUnavailable = true
		return snap
	}
	if errors.Is(err, github.ErrIssueNotFound) {
		snap.NotFound = true
		return snap
	}
	if err != nil {
		snap.FetchErr = err.Error()
		return snap
	}
	snap.Issue = issue
	return snap
}

// renderIssueReadContext mirrors renderPRReadContext's state-first
// layout so the model can apply the same parsing template to both
// tools.
func renderIssueReadContext(s IssueReadContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## state\nnumber=%d\nnot_found=%v\ngh_unavailable=%v\n",
		s.Number, s.NotFound, s.GhUnavailable)
	if s.FetchErr != "" {
		fmt.Fprintf(&b, "fetch_error=%s\n", s.FetchErr)
	}

	if s.NotFound || s.GhUnavailable {
		return strings.TrimRight(b.String(), "\n") + "\n"
	}

	b.WriteString("\n## issue\n")
	fmt.Fprintf(&b, "number=%d\nstate=%s\nauthor=%s\nurl=%s\n",
		s.Issue.Number, s.Issue.State, s.Issue.Author, s.Issue.URL)
	fmt.Fprintf(&b, "title=%s\n", s.Issue.Title)
	if len(s.Issue.Labels) > 0 {
		fmt.Fprintf(&b, "labels=%s\n", strings.Join(s.Issue.Labels, ","))
	}
	if len(s.Issue.Assignees) > 0 {
		fmt.Fprintf(&b, "assignees=%s\n", strings.Join(s.Issue.Assignees, ","))
	}
	if strings.TrimSpace(s.Issue.Body) != "" {
		b.WriteString("body=|\n")
		for _, line := range strings.Split(s.Issue.Body, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	if len(s.Issue.Comments) > 0 {
		b.WriteString("\n## comments\n")
		for _, c := range s.Issue.Comments {
			ts := ""
			if !c.Created.IsZero() {
				ts = c.Created.UTC().Format("2006-01-02")
			}
			fmt.Fprintf(&b, "- @%s (%s):\n", c.Author, ts)
			for _, line := range strings.Split(strings.TrimRight(c.Body, "\n"), "\n") {
				b.WriteString("  ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// GHIssueListTool wraps Interface.ListOpenIssues. Returns
// lightweight summaries — number, title, author, URL, labels,
// assignees. Bodies are deliberately not included; the model
// follows up with gh_issue_read on a specific issue when it
// needs more.
//
// Read-only, no approval. Filters are AND-ed (e.g., labels=[bug]
// AND assignee=octocat returns only issues matching both).
type GHIssueListTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHIssueListTool) Name() string { return "gh_issue_list" }

func (t *GHIssueListTool) Description() string {
	return "List open GitHub issues for the current repo, with " +
		"optional label / assignee / milestone filters (AND-ed). " +
		"Returns lightweight summaries (number, title, author, URL, " +
		"labels, assignees) — bodies and comments are dropped, " +
		"follow up with gh_issue_read for the full content. Prefer " +
		"this over shelling out to `gh issue list --json ...` from " +
		"run_bash whenever the goal is enumerating open issues: " +
		"faster, structured, and the filter fields map directly to " +
		"the GitHub API. Returns the first page only (GitHub's " +
		"default ~30 issues); refine filters if you need a narrower " +
		"set."
}

func (t *GHIssueListTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"labels": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Label names to match (AND-ed). Empty = no label filter.",
			},
			"assignee": map[string]any{
				"type":        "string",
				"description": "Assignee login. Empty = no assignee filter.",
			},
			"milestone": map[string]any{
				"type":        "string",
				"description": "Milestone title or number. Empty = no milestone filter.",
			},
		},
	}
}

func (t *GHIssueListTool) RequiresApproval(string) bool { return false }

func (t *GHIssueListTool) PreviewCall(argsJSON string) string {
	var a struct {
		Labels    []string `json:"labels"`
		Assignee  string   `json:"assignee"`
		Milestone string   `json:"milestone"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	var parts []string
	if len(a.Labels) > 0 {
		parts = append(parts, "labels="+strings.Join(a.Labels, "+"))
	}
	if a.Assignee != "" {
		parts = append(parts, "assignee="+a.Assignee)
	}
	if a.Milestone != "" {
		parts = append(parts, "milestone="+a.Milestone)
	}
	if len(parts) == 0 {
		return "gh_issue_list()"
	}
	return "gh_issue_list(" + strings.Join(parts, ", ") + ")"
}

func (t *GHIssueListTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.GH == nil {
		return "", errors.New("gh_issue_list: no GitHub adapter configured")
	}
	var a struct {
		Labels    []string `json:"labels"`
		Assignee  string   `json:"assignee"`
		Milestone string   `json:"milestone"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	snap := BuildIssueListContext(ctx, t.GH, github.ListIssuesRequest{
		Labels:    a.Labels,
		Assignee:  a.Assignee,
		Milestone: a.Milestone,
	})
	return renderIssueListContext(snap), nil
}

// IssueListContext is the typed snapshot gh_issue_list returns.
// GhUnavailable + FetchErr follow the same state-flag pattern as
// the other read tools. Empty Issues with no flags means "no open
// issues match" — a valid result, not an error.
type IssueListContext struct {
	GhUnavailable bool
	FetchErr      string

	Filter github.ListIssuesRequest
	Issues []github.IssueSummary
}

// BuildIssueListContext is the deterministic core of gh_issue_list.
// Folds typed errors into the snapshot's flags so callers branch on
// flags rather than err strings.
func BuildIssueListContext(ctx context.Context, client github.Interface, req github.ListIssuesRequest) IssueListContext {
	snap := IssueListContext{Filter: req}
	issues, err := client.ListOpenIssues(ctx, req)
	if errors.Is(err, github.ErrGhUnavailable) {
		snap.GhUnavailable = true
		return snap
	}
	if err != nil {
		snap.FetchErr = err.Error()
		return snap
	}
	snap.Issues = issues
	return snap
}

// renderIssueListContext mirrors the other render functions'
// state-first layout.
func renderIssueListContext(s IssueListContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## state\ngh_unavailable=%v\ncount=%d\n",
		s.GhUnavailable, len(s.Issues))
	if s.FetchErr != "" {
		fmt.Fprintf(&b, "fetch_error=%s\n", s.FetchErr)
	}
	if len(s.Filter.Labels) > 0 {
		fmt.Fprintf(&b, "filter_labels=%s\n", strings.Join(s.Filter.Labels, ","))
	}
	if s.Filter.Assignee != "" {
		fmt.Fprintf(&b, "filter_assignee=%s\n", s.Filter.Assignee)
	}
	if s.Filter.Milestone != "" {
		fmt.Fprintf(&b, "filter_milestone=%s\n", s.Filter.Milestone)
	}

	if s.GhUnavailable || s.FetchErr != "" {
		return strings.TrimRight(b.String(), "\n") + "\n"
	}

	b.WriteString("\n## issues\n")
	if len(s.Issues) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, i := range s.Issues {
			b.WriteString(formatIssueSummaryLine(i))
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// formatIssueSummaryLine renders one summary row. Pulled out so
// tests can pin the format independently of the full snapshot.
func formatIssueSummaryLine(i github.IssueSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- #%d %s", i.Number, i.Title)
	if i.Author != "" {
		fmt.Fprintf(&b, " (by @%s)", i.Author)
	}
	if len(i.Labels) > 0 {
		fmt.Fprintf(&b, " [%s]", strings.Join(i.Labels, ","))
	}
	if len(i.Assignees) > 0 {
		fmt.Fprintf(&b, " → @%s", strings.Join(i.Assignees, ", @"))
	}
	b.WriteString("\n")
	return b.String()
}
