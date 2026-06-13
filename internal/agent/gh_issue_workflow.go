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
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yottadynamics/yottacode/internal/github"
	"gopkg.in/yaml.v3"
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

// issueTemplateCandidates is the lookup order for a repo-local
// single-file issue template — the legacy convention GitHub still
// honors in `.github/`, the repo root, and `docs/`. GitHub matches
// these case-insensitively; both casings are listed because most
// hosts yottacode runs on have case-sensitive filesystems. The
// modern multi-template directory (`.github/ISSUE_TEMPLATE/*.md`)
// is globbed separately by loadIssueTemplate and takes precedence,
// matching GitHub's own chooser.
var issueTemplateCandidates = []string{
	".github/ISSUE_TEMPLATE.md",
	".github/issue_template.md",
	"ISSUE_TEMPLATE.md",
	"issue_template.md",
	"docs/ISSUE_TEMPLATE.md",
	"docs/issue_template.md",
}

// GHIssueContextTool produces the read-only snapshot a caller needs to
// draft an issue title + body. Mirrors GHPRContextTool for issues.
type GHIssueContextTool struct{ Cwd *CwdRef }

func (t *GHIssueContextTool) Name() string { return "gh_issue_context" }

func (t *GHIssueContextTool) Description() string {
	return "Gather the read-only context needed to open a GitHub issue: " +
		"resolved repo (owner/repo), whether GitHub auth is available, " +
		"and the contents of a local issue template if one exists. " +
		"Returns a structured snapshot keyed by section headers. " +
		"Pair with gh_issue_create to open the issue."
}

func (t *GHIssueContextTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *GHIssueContextTool) RequiresApproval(string) bool { return false }

func (t *GHIssueContextTool) PreviewCall(argsJSON string) string {
	return "gh_issue_context()"
}

func (t *GHIssueContextTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	snap, err := BuildIssueContext(ctx, t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("gh_issue_context: %w", err)
	}
	return renderIssueContext(snap), nil
}

// IssueContext is the typed snapshot BuildIssueContext returns.
type IssueContext struct {
	Owner                string
	Repo                 string
	GhAvailable          bool
	IssueTemplate        string
	IssueTemplatePath    string   // relative to cwd
	IssueTemplateChoices []string // all issue template names when the template dir offers several
	IssueTemplates       []IssueTemplate
	ContactLinks         []IssueContactLink
	BlankIssuesEnabled   bool
}

// IssueTemplate is a normalized issue-creation target discovered from
// `.github/ISSUE_TEMPLATE`. Markdown templates keep their fillable body;
// YAML issue forms are rendered into an equivalent Markdown body because
// GitHub's public issue API creates normal issue bodies, not form submissions.
type IssueTemplate struct {
	Name        string
	Description string
	Path        string
	Kind        string
	TitlePrefix string
	Labels      []string
	Assignees   []string
	Content     string
}

// IssueContactLink mirrors GitHub chooser contact links. These are not
// issue-creation targets, but surfacing them keeps `/git-create-issue` from
// trying to turn docs, discussions, or security reports into public issues.
type IssueContactLink struct {
	Name  string `yaml:"name"`
	URL   string `yaml:"url"`
	About string `yaml:"about"`
}

// BuildIssueContext is the deterministic core of gh_issue_context.
// Owner/repo come from the cwd's origin remote, GhAvailable reports
// whether the GitHub auth token chain resolves (same signal
// BuildPRContext exposes — the /git-create-issue directive branches
// to draft-only on gh_available=false), and the template fields
// carry a repo-local issue template when one exists.
func BuildIssueContext(ctx context.Context, cwd string) (IssueContext, error) {
	var snap IssueContext

	// Get owner/repo from git remote
	owner, repo, err := github.DetectRepo(ctx, cwd)
	if err != nil {
		return snap, err
	}
	snap.Owner = owner
	snap.Repo = repo

	snap.GhAvailable = github.IsGhAvailable(ctx)

	snap.BlankIssuesEnabled = true
	snap.IssueTemplates, snap.ContactLinks, snap.BlankIssuesEnabled = loadIssueTemplates(cwd)
	if len(snap.IssueTemplates) > 0 {
		// Preserve the legacy fields so older prompt text and tests still see
		// the default template while newer flows can inspect all choices under
		// the richer ## templates section.
		first := snap.IssueTemplates[0]
		snap.IssueTemplatePath = first.Path
		snap.IssueTemplate = first.Content
		for _, tmpl := range snap.IssueTemplates {
			snap.IssueTemplateChoices = append(snap.IssueTemplateChoices, filepath.Base(tmpl.Path))
		}
	}

	return snap, nil
}

// loadIssueTemplate keeps the old single-template helper shape for tests and
// compatibility. New callers should use loadIssueTemplates so they can choose
// among Markdown templates, YAML issue forms, blank issues, and contact links.
func loadIssueTemplate(cwd string) (path, content string, choices []string) {
	templates, _, _ := loadIssueTemplates(cwd)
	if len(templates) == 0 {
		return "", "", nil
	}
	for _, tmpl := range templates {
		choices = append(choices, filepath.Base(tmpl.Path))
	}
	return templates[0].Path, templates[0].Content, choices
}

// loadIssueTemplates finds repo-local issue creation targets. Directory-style
// templates take precedence over legacy single-file templates, matching
// GitHub's chooser. Both Markdown templates and YAML issue forms are returned;
// config.yml contributes blank-issue policy and contact links only.
func loadIssueTemplates(cwd string) ([]IssueTemplate, []IssueContactLink, bool) {
	blankIssuesEnabled := true
	var contactLinks []IssueContactLink
	configPath := filepath.Join(cwd, ".github", "ISSUE_TEMPLATE", "config.yml")
	if raw, err := os.ReadFile(configPath); err == nil {
		blankIssuesEnabled, contactLinks = parseIssueTemplateConfig(raw)
	}

	// os.ReadDir returns entries sorted by filename, so the first supported hit
	// is already the alphabetical pick.
	entries, _ := os.ReadDir(filepath.Join(cwd, ".github", "ISSUE_TEMPLATE"))
	var templates []IssueTemplate
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		rel := filepath.Join(".github", "ISSUE_TEMPLATE", name)
		raw, err := os.ReadFile(filepath.Join(cwd, rel))
		if err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(lower, ".md"):
			templates = append(templates, markdownIssueTemplate(rel, raw))
		case lower != "config.yml" && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")):
			if tmpl, ok := yamlIssueTemplate(rel, raw); ok {
				templates = append(templates, tmpl)
			}
		}
	}
	if len(templates) > 0 {
		return templates, contactLinks, blankIssuesEnabled
	}

	for _, candidate := range issueTemplateCandidates {
		raw, err := os.ReadFile(filepath.Join(cwd, candidate))
		if err != nil {
			continue
		}
		return []IssueTemplate{markdownIssueTemplate(candidate, raw)}, contactLinks, blankIssuesEnabled
	}
	return nil, contactLinks, blankIssuesEnabled
}

func markdownIssueTemplate(path string, raw []byte) IssueTemplate {
	return IssueTemplate{
		Name:    strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Path:    path,
		Kind:    "markdown",
		Content: stripTemplateFrontmatter(string(raw)),
	}
}

type issueTemplateConfigYAML struct {
	BlankIssuesEnabled *bool              `yaml:"blank_issues_enabled"`
	ContactLinks       []IssueContactLink `yaml:"contact_links"`
}

func parseIssueTemplateConfig(raw []byte) (bool, []IssueContactLink) {
	cfg := issueTemplateConfigYAML{BlankIssuesEnabled: boolPtr(true)}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return true, nil
	}
	return *cfg.BlankIssuesEnabled, cfg.ContactLinks
}

func boolPtr(v bool) *bool { return &v }

type issueFormYAML struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Title       string          `yaml:"title"`
	Labels      yamlStringList  `yaml:"labels"`
	Assignees   yamlStringList  `yaml:"assignees"`
	Body        []issueFormItem `yaml:"body"`
}

type issueFormItem struct {
	Type       string `yaml:"type"`
	ID         string `yaml:"id"`
	Attributes struct {
		Label       string         `yaml:"label"`
		Description string         `yaml:"description"`
		Placeholder string         `yaml:"placeholder"`
		Value       string         `yaml:"value"`
		Render      string         `yaml:"render"`
		Options     []checkboxItem `yaml:"options"`
	} `yaml:"attributes"`
	Validations struct {
		Required bool `yaml:"required"`
	} `yaml:"validations"`
}

type checkboxItem struct {
	Label    string `yaml:"label"`
	Required bool   `yaml:"required"`
}

func (i *checkboxItem) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		i.Label = strings.TrimSpace(value.Value)
		return nil
	}
	type alias checkboxItem
	var out alias
	if err := value.Decode(&out); err != nil {
		return err
	}
	*i = checkboxItem(out)
	return nil
}

// yamlStringList accepts GitHub's common `labels: [bug]` form and the string
// shorthand `labels: bug, needs-triage` used by some issue templates.
type yamlStringList []string

func (l *yamlStringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		for _, node := range value.Content {
			*l = append(*l, strings.TrimSpace(node.Value))
		}
	case yaml.ScalarNode:
		for _, part := range strings.Split(value.Value, ",") {
			if s := strings.TrimSpace(part); s != "" {
				*l = append(*l, s)
			}
		}
	}
	return nil
}

func yamlIssueTemplate(path string, raw []byte) (IssueTemplate, bool) {
	var form issueFormYAML
	if err := yaml.Unmarshal(raw, &form); err != nil || strings.TrimSpace(form.Name) == "" {
		return IssueTemplate{}, false
	}
	return IssueTemplate{
		Name:        form.Name,
		Description: form.Description,
		Path:        path,
		Kind:        "issue_form",
		TitlePrefix: form.Title,
		Labels:      []string(form.Labels),
		Assignees:   []string(form.Assignees),
		Content:     renderIssueFormMarkdown(form),
	}, true
}

func renderIssueFormMarkdown(form issueFormYAML) string {
	var b strings.Builder
	for _, item := range form.Body {
		label := strings.TrimSpace(item.Attributes.Label)
		switch item.Type {
		case "markdown":
			if value := strings.TrimSpace(item.Attributes.Value); value != "" {
				b.WriteString(value)
				b.WriteString("\n\n")
			}
		case "checkboxes":
			if label != "" {
				fmt.Fprintf(&b, "## %s\n\n", label)
			}
			for _, opt := range item.Attributes.Options {
				marker := " "
				if opt.Required {
					marker = "!"
				}
				fmt.Fprintf(&b, "- [%s] %s\n", marker, opt.Label)
			}
			b.WriteString("\n")
		default:
			if label == "" {
				label = item.ID
			}
			if label == "" {
				continue
			}
			fmt.Fprintf(&b, "## %s\n\n", label)
			writeIssueFormHint(&b, item)
			if item.Attributes.Render != "" {
				fmt.Fprintf(&b, "```%s\n\n```\n\n", item.Attributes.Render)
			} else {
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func writeIssueFormHint(b *strings.Builder, item issueFormItem) {
	var hints []string
	if item.Validations.Required {
		hints = append(hints, "Required")
	}
	if item.Attributes.Description != "" {
		hints = append(hints, item.Attributes.Description)
	}
	if item.Attributes.Placeholder != "" {
		hints = append(hints, "Placeholder: "+strings.ReplaceAll(item.Attributes.Placeholder, "\n", " "))
	}
	if len(item.Attributes.Options) > 0 {
		var opts []string
		for _, opt := range item.Attributes.Options {
			opts = append(opts, opt.Label)
		}
		hints = append(hints, "Options: "+strings.Join(opts, "; "))
	}
	if len(hints) > 0 {
		fmt.Fprintf(b, "<!-- %s -->\n\n", strings.Join(hints, " | "))
	}
}

// stripTemplateFrontmatter removes a leading YAML frontmatter block
// (`--- ... ---`) from a directory-style issue template. GitHub
// uses the frontmatter only to seed title/labels in its own UI —
// just the markdown body below it pre-fills the issue text, so
// that's what the composition step should see. Returns the input
// unchanged when no complete frontmatter block is present (safer
// to show extra metadata than to over-strip a body).
func stripTemplateFrontmatter(s string) string {
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return s
	}
	_, body, found := strings.Cut(rest, "\n---\n")
	if !found {
		return s
	}
	return strings.TrimLeft(body, "\n")
}

func renderIssueContext(s IssueContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## state\nowner=%s\nrepo=%s\ngh_available=%v\n",
		s.Owner, s.Repo, s.GhAvailable)

	if s.IssueTemplate != "" {
		b.WriteString("\n## template\n")
		b.WriteString("path=" + s.IssueTemplatePath + "\n")
		if len(s.IssueTemplateChoices) > 1 {
			b.WriteString("choices=" + strings.Join(s.IssueTemplateChoices, ",") + "\n")
		}
		b.WriteString("content=|\n")
		for _, line := range strings.Split(s.IssueTemplate, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	if len(s.IssueTemplates) > 0 {
		b.WriteString("\n## templates\n")
		for i, tmpl := range s.IssueTemplates {
			fmt.Fprintf(&b, "- index=%d name=%s kind=%s path=%s\n", i+1, tmpl.Name, tmpl.Kind, tmpl.Path)
			if tmpl.Description != "" {
				fmt.Fprintf(&b, "  description=%s\n", tmpl.Description)
			}
			if tmpl.TitlePrefix != "" {
				fmt.Fprintf(&b, "  title_prefix=%s\n", tmpl.TitlePrefix)
			}
			if len(tmpl.Labels) > 0 {
				fmt.Fprintf(&b, "  labels=%s\n", strings.Join(tmpl.Labels, ","))
			}
			if len(tmpl.Assignees) > 0 {
				fmt.Fprintf(&b, "  assignees=%s\n", strings.Join(tmpl.Assignees, ","))
			}
			b.WriteString("  content=|\n")
			for _, line := range strings.Split(tmpl.Content, "\n") {
				b.WriteString("    ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}
	if !s.BlankIssuesEnabled {
		b.WriteString("\n## blank_issue\nenabled=false\n")
	}
	if len(s.ContactLinks) > 0 {
		b.WriteString("\n## contact_links\n")
		for _, link := range s.ContactLinks {
			fmt.Fprintf(&b, "- name=%s url=%s\n", link.Name, link.URL)
			if link.About != "" {
				fmt.Fprintf(&b, "  about=%s\n", link.About)
			}
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// GHIssueCreateTool is the typed mutator that opens the issue. Validates
// the title in Go *before* invoking github.Interface.CreateIssue;
// empty, multi-line, oversize, and trailing-period titles can't
// reach the network.
type GHIssueCreateTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHIssueCreateTool) Name() string { return "gh_issue_create" }

func (t *GHIssueCreateTool) Description() string {
	return "Open a GitHub issue with the supplied title, body, labels, and assignees. " +
		"Title is validated to be a single line of at most " + strconv.Itoa(PRTitleMaxLen) + " " +
		"characters with no trailing period. " +
		"Returns a typed result with the issue URL and number on success, " +
		"or a gh_unavailable / validation / gh_error reason on failure. " +
		"The approval modal shows only the invocation summary (title, labels, assignees) — " +
		"NOT the body. Print the full drafted title and body as plain text BEFORE calling " +
		"this tool, so the user approves what will actually be posted."
}

func (t *GHIssueCreateTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{
				"type": "string",
				"description": fmt.Sprintf(
					"Issue title (≤%d chars, no trailing period, single line).",
					PRTitleMaxLen),
			},
			"body": map[string]any{
				"type":        "string",
				"description": "Issue body / description (optional, multi-line allowed).",
			},
			"labels": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Labels to apply to the issue (optional).",
			},
			"assignees": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Assignees to assign to the issue (optional).",
			},
		},
		"required": []string{"title"},
	}
}

func (t *GHIssueCreateTool) RequiresApproval(string) bool { return true }

func (t *GHIssueCreateTool) PreviewCall(argsJSON string) string {
	var a struct {
		Title     string   `json:"title"`
		Labels    []string `json:"labels"`
		Assignees []string `json:"assignees"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	var parts []string
	parts = append(parts, fmt.Sprintf("title=%q", a.Title))
	if len(a.Labels) > 0 {
		parts = append(parts, "labels="+strings.Join(a.Labels, "+"))
	}
	if len(a.Assignees) > 0 {
		parts = append(parts, "assignees="+strings.Join(a.Assignees, "+"))
	}
	return "gh_issue_create(" + strings.Join(parts, ", ") + ")"
}

// IssueCreateResult is the typed envelope CreateIssue returns. Same shape
// rationale as PRCreateResult: callers branch on typed fields, not
// stringy err checks. Reason discriminates the failure mode so the
// procedural /create-issue handles each branch differently (validation
// → re-prompt; gh_unavailable → fall through to draft-only; gh_error
// → surface verbatim and stop).
type IssueCreateResult struct {
	Created       bool
	URL           string
	Number        int
	ValidationErr string
	GhUnavailable bool
	GhError       string
}

func (t *GHIssueCreateTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Labels    []string `json:"labels"`
		Assignees []string `json:"assignees"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("gh_issue_create: invalid args: %w", err)
	}
	if t.GH == nil {
		return "", errors.New("gh_issue_create: no GitHub adapter configured")
	}

	res, err := CreateIssue(ctx, t.GH, github.CreateIssueRequest{
		Title:     a.Title,
		Body:      a.Body,
		Labels:    a.Labels,
		Assignees: a.Assignees,
	})
	if err != nil {
		return "", fmt.Errorf("gh_issue_create: %w", err)
	}
	return renderIssueCreateResult(res), nil
}

// CreateIssue is the deterministic core of gh_issue_create. Validates
// title *before* dialing the Interface, so an oversize title never
// reaches the network. Returns a typed IssueCreateResult; the tool
// wrapper renders it for model consumption, /create-issue reads it directly.
//
// The Interface returns ErrGhUnavailable when the local environment
// can't make the call; we surface that as GhUnavailable=true so the
// caller can fall through to draft-only instead of treating it as
// an opaque error.
func CreateIssue(ctx context.Context, client github.Interface, req github.CreateIssueRequest) (IssueCreateResult, error) {
	var res IssueCreateResult

	if v := validatePRTitle(req.Title); v != "" {
		res.ValidationErr = v
		return res, nil
	}

	out, err := client.CreateIssue(ctx, req)
	if errors.Is(err, github.ErrGhUnavailable) {
		res.GhUnavailable = true
		return res, nil
	}
	if err != nil {
		res.GhError = err.Error()
		return res, nil
	}
	res.Created = true
	res.URL = out.URL
	res.Number = out.Number
	return res, nil
}

// renderIssueCreateResult shapes the result envelope for the model.
// Same layout as renderPRCreateResult so the two create flows are
// directive-compatible: the model branches on the same
// `created=false reason=<...>` discriminators in both.
func renderIssueCreateResult(r IssueCreateResult) string {
	var b strings.Builder
	switch {
	case r.ValidationErr != "":
		fmt.Fprintf(&b, "created=false reason=validation\nerror=%s\n", r.ValidationErr)
	case r.GhUnavailable:
		b.WriteString("created=false reason=gh_unavailable\n")
		b.WriteString("Hint: install gh and run `gh auth login`, or surface the drafted title + body to the user so they can paste them into GitHub manually.\n")
	case r.GhError != "":
		b.WriteString("created=false reason=gh_error\n")
		b.WriteString("--- gh output ---\n")
		b.WriteString(r.GhError)
		if !strings.HasSuffix(r.GhError, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- end gh output ---\n")
		b.WriteString("Do NOT auto-retry, auto-edit, or auto-assign. Surface the error and stop.\n")
	case r.Created:
		fmt.Fprintf(&b, "created=true url=%s number=%d\n", r.URL, r.Number)
	default:
		b.WriteString("created=false reason=unknown\n")
	}
	return b.String()
}
