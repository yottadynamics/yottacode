package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/github"
)

// Composite PR-workflow tools — the GitHub-side sibling to
// git_commit_workflow.go. Same split as commit: a read-only context
// tool builds a typed snapshot, a mutator tool validates and acts.
// Reliability work that the legacy /git:create-pr directive asked
// the model to perform in prose ("if AHEAD-COUNT is 0, stop"; "if
// GH-AVAILABLE is no, fall back to draft-only") moves into Go: the
// snapshot fields are typed flags, the create tool refuses to act
// when validation fails.
//
// The github.Interface dependency keeps the create tool decoupled
// from the gh CLI. v0.5.0's typed go-github client implements the
// same Interface — swap is a registration change in run.go.

const (
	// prContextDiffStatCap bounds the diffstat blob spliced into the
	// snapshot. Diffstat is dense (file path + +/- count per line) so
	// 16 KiB covers all but the largest branches; oversize is rare
	// enough that the truncation marker pointing the model at
	// git_diff_files for full hunks is a fine fallback.
	prContextDiffStatCap = 16 * 1024
	// prContextLogCommits is the cap on commits surfaced in
	// ## commits.log. Long-running branches with hundreds of commits
	// don't get a useful PR summary from the full log — the first ~25
	// usually carry the narrative.
	prContextLogCommits = 25
	// prTemplateCap protects against pathological README-sized
	// templates polluting the snapshot. Real PR templates are < 1 KiB.
	prTemplateCap = 8 * 1024
)

// PRTitleMaxLen is the cap GH PR titles are validated against.
// GitHub itself allows ~256, but reviewer-friendly UIs (notification
// emails, Slack/Linear embeds, the PR list view) all start truncating
// in the 70-80 char range. Matching the commit-subject cap keeps both
// halves of the workflow visually consistent.
const PRTitleMaxLen = 72

// prTemplateCandidates is the lookup order for a repo-local PR
// template. Matches the conventions GitHub itself honors plus a
// docs/ fallback that some repos use.
var prTemplateCandidates = []string{
	".github/pull_request_template.md",
	".github/PULL_REQUEST_TEMPLATE.md",
	"docs/pull_request_template.md",
	"PULL_REQUEST_TEMPLATE.md",
}

// baseCandidates is the deterministic fallback chain when neither
// the caller nor `origin/HEAD` names a base branch. Order matches
// the de-facto convention: most repos pick the first of these that
// actually exists locally.
var baseCandidates = []string{"main", "master", "develop"}

// GHPRContextTool produces the read-only snapshot a caller needs to
// draft a PR title + body and decide whether to push or fall through
// to draft-only. Replaces the multi-step bash heredoc the legacy
// /git:create-pr directive used as its first tool call.
type GHPRContextTool struct{ Cwd *CwdRef }

func (t *GHPRContextTool) Name() string { return "pr_context" }

func (t *GHPRContextTool) Description() string {
	return "Gather the read-only context needed to open a pull request: " +
		"resolved base branch, current branch, ahead-count, three-dot " +
		"diffstat (capped), two-dot commit log (capped), whether the " +
		"branch is pushed to origin, whether gh is installed and " +
		"authenticated, and the contents of a local PR template if one " +
		"exists. Returns a structured snapshot keyed by section headers. " +
		"Pair with pr_create to open the PR."
}

func (t *GHPRContextTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"base": map[string]any{
				"type":        "string",
				"description": "Optional base branch override. Empty falls through to origin/HEAD, then main/master/develop.",
			},
		},
	}
}

func (t *GHPRContextTool) RequiresApproval(string) bool { return false }
func (t *GHPRContextTool) PreviewCall(argsJSON string) string {
	var a struct {
		Base string `json:"base"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Base) == "" {
		return "pr_context()"
	}
	return fmt.Sprintf("pr_context(base=%s)", a.Base)
}

func (t *GHPRContextTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("pr_context: git binary not found in PATH")
	}
	var a struct {
		Base string `json:"base"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	snap, err := BuildPRContext(ctx, t.Cwd.Get(), strings.TrimSpace(a.Base))
	if err != nil {
		return "", fmt.Errorf("pr_context: %w", err)
	}
	return renderPRContext(snap), nil
}

// PRContext is the typed snapshot BuildPRContext returns. Same
// rationale as CommitContext: structured fields the procedural
// /create-pr can consume directly without a tool-call round trip,
// and a typed shape tests can assert against rather than parsing
// the rendered string.
type PRContext struct {
	ResolvedBase      string
	BaseResolution    string // "explicit" | "origin-head" | "fallback:<name>" | "unresolved"
	CurrentBranch     string
	BaseEqualsCurrent bool
	AheadCount        int
	AheadCountErr     string
	DiffStat          string
	DiffStatCapped    bool
	CommitLog         []string // each entry: "<short-sha> <subject>"
	PushedToOrigin    bool
	GhAvailable       bool
	PRTemplate        string
	PRTemplatePath    string // relative to cwd
	PRTemplateCapped  bool
	ArtifactSummary   string
	ArtifactCleanup   bool
}

// BuildPRContext is the deterministic core of pr_context. Returns
// a typed snapshot the tool wrapper renders to text, and the
// procedural /create-pr reads directly. Errors return only on
// infrastructure failures (git binary missing, etc.); informational
// gaps (ahead-count not computable because base doesn't exist
// locally, no PR template found) populate fields rather than failing.
func BuildPRContext(ctx context.Context, cwd, explicitBase string) (PRContext, error) {
	var snap PRContext

	branch, err := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return snap, fmt.Errorf("current branch: %w", err)
	}
	snap.CurrentBranch = strings.TrimSpace(branch)

	snap.ResolvedBase, snap.BaseResolution = resolveBaseBranch(ctx, cwd, explicitBase)
	snap.BaseEqualsCurrent = snap.ResolvedBase != "" &&
		snap.ResolvedBase == snap.CurrentBranch

	if snap.ResolvedBase != "" && !snap.BaseEqualsCurrent {
		count, err := gitOutput(ctx, cwd, "rev-list", "--count",
			snap.ResolvedBase+"..HEAD")
		if err != nil {
			snap.AheadCountErr = err.Error()
		} else {
			n, parseErr := strconv.Atoi(strings.TrimSpace(count))
			if parseErr == nil {
				snap.AheadCount = n
			} else {
				snap.AheadCountErr = "could not parse ahead-count: " + parseErr.Error()
			}
		}

		// Three-dot diffstat = changes on HEAD relative to the merge
		// base with ResolvedBase. Matches what GitHub itself shows in
		// the PR's "Files changed" tab.
		stat, _ := gitOutput(ctx, cwd, "diff", "--stat",
			snap.ResolvedBase+"...HEAD")
		snap.DiffStat, snap.DiffStatCapped = capString(stat, prContextDiffStatCap)

		// Two-dot log = commits in HEAD not in ResolvedBase. Matches
		// "what this PR proposes to land."
		log, _ := gitOutput(ctx, cwd, "log",
			fmt.Sprintf("-%d", prContextLogCommits),
			"--format=%h %s",
			snap.ResolvedBase+"..HEAD")
		snap.CommitLog = splitNonEmptyLines(log)
	}

	snap.PushedToOrigin = branchOnOrigin(ctx, cwd, snap.CurrentBranch)
	snap.GhAvailable = github.IsGhAvailable(ctx)

	untracked, _ := boundedUntrackedFiles(ctx, cwd)
	for _, line := range splitNonEmptyLines(untracked) {
		if strings.HasPrefix(line, "omitted ") {
			snap.ArtifactSummary = line
			snap.ArtifactCleanup = true
		}
	}

	snap.PRTemplatePath, snap.PRTemplate, snap.PRTemplateCapped =
		loadPRTemplate(cwd)

	return snap, nil
}

// resolveBaseBranch picks the base in priority order: caller-supplied
// argument → `origin/HEAD` symbolic ref → first of main/master/develop
// that exists locally. The string return is the chosen base; the
// resolution-source return is "explicit"/"origin-head"/"fallback:<name>"
// /"unresolved" so the snapshot surfaces *why* a base was chosen,
// which matters when the caller wants to nudge the user ("found
// `develop` locally — pass --base if you meant something else").
func resolveBaseBranch(ctx context.Context, cwd, explicit string) (base, source string) {
	if explicit != "" {
		return explicit, "explicit"
	}
	if head, err := gitOutput(ctx, cwd, "symbolic-ref", "--short",
		"refs/remotes/origin/HEAD"); err == nil {
		s := strings.TrimSpace(head)
		s = strings.TrimPrefix(s, "origin/")
		if s != "" {
			return s, "origin-head"
		}
	}
	for _, candidate := range baseCandidates {
		if _, err := gitOutput(ctx, cwd, "rev-parse", "--verify",
			"--quiet", candidate); err == nil {
			return candidate, "fallback:" + candidate
		}
	}
	return "", "unresolved"
}

// branchOnOrigin reports whether the named branch exists on the
// `origin` remote. Safe with a missing remote (returns false) so
// fresh-init repos don't crash this step.
//
// Uses `git ls-remote --heads --exit-code` which only touches the
// network when origin actually has a URL — a no-network local config
// returns quickly without surprising the caller.
func branchOnOrigin(ctx context.Context, cwd, branch string) bool {
	if strings.TrimSpace(branch) == "" {
		return false
	}
	_, err := gitOutput(ctx, cwd, "ls-remote", "--heads",
		"--exit-code", "origin", branch)
	return err == nil
}

// loadPRTemplate returns the first PR template found at one of the
// well-known paths (relative to cwd). Returns empty strings + false
// when none exists — callers render "(none)" in that case.
//
// path is reported relative to cwd so the snapshot reads as
// `.github/pull_request_template.md`, not the user's full
// filesystem path.
func loadPRTemplate(cwd string) (path, body string, capped bool) {
	for _, candidate := range prTemplateCandidates {
		abs := filepath.Join(cwd, candidate)
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		body, capped = capString(string(data), prTemplateCap)
		return candidate, body, capped
	}
	return "", "", false
}

// renderPRContext flattens a PRContext into the labeled-section text
// the tool returns. Section headers match the field names so callers
// pattern-match without ambiguity, and the ## state block leads with
// the fields a procedural caller branches on first (BaseEqualsCurrent,
// AheadCount, PushedToOrigin, GhAvailable).
func renderPRContext(s PRContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## state\nresolved_base=%s\nbase_resolution=%s\ncurrent_branch=%s\nbase_equals_current=%v\nahead_count=%d\npushed_to_origin=%v\ngh_available=%v\nartifact_cleanup_required=%v\n",
		s.ResolvedBase, s.BaseResolution, s.CurrentBranch,
		s.BaseEqualsCurrent, s.AheadCount, s.PushedToOrigin, s.GhAvailable, s.ArtifactCleanup)
	if s.AheadCountErr != "" {
		fmt.Fprintf(&b, "ahead_count_error=%s\n", s.AheadCountErr)
	}
	if s.ArtifactSummary != "" {
		fmt.Fprintf(&b, "generated_artifacts=%s\n", s.ArtifactSummary)
		b.WriteString("cleanup=remove repo-local generated artifact directories before opening a PR\n")
	}

	b.WriteString("\n## diff.stat\n")
	if strings.TrimSpace(s.DiffStat) == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(s.DiffStat)
		if !strings.HasSuffix(s.DiffStat, "\n") {
			b.WriteString("\n")
		}
		if s.DiffStatCapped {
			fmt.Fprintf(&b, "[truncated at %d bytes]\n", prContextDiffStatCap)
		}
	}

	b.WriteString("\n## commits.log\n")
	if len(s.CommitLog) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, line := range s.CommitLog {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## pr.template\n")
	if s.PRTemplate == "" {
		b.WriteString("(none found)\n")
	} else {
		fmt.Fprintf(&b, "path=%s\n", s.PRTemplatePath)
		b.WriteString(s.PRTemplate)
		if !strings.HasSuffix(s.PRTemplate, "\n") {
			b.WriteString("\n")
		}
		if s.PRTemplateCapped {
			fmt.Fprintf(&b, "[truncated at %d bytes]\n", prTemplateCap)
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// GHPRCreateTool is the typed mutator that opens the PR. Validates
// title length and required fields in Go *before* invoking
// github.Interface.CreatePR; oversize titles, missing bodies, and
// blank bases can't reach the network. Hooks back into the
// github.Interface so v0.5.0's typed go-github client replaces the
// shell-out without touching this file.
type GHPRCreateTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHPRCreateTool) Name() string { return "pr_create" }

func (t *GHPRCreateTool) Description() string {
	return "Open a GitHub pull request with the supplied base, title, " +
		"body, and optional draft flag. Title is validated to be a " +
		"single line of at most " + strconv.Itoa(PRTitleMaxLen) + " " +
		"characters with no trailing period; body must be non-empty. " +
		"Returns a typed result with the PR URL and number on success, " +
		"or a github_unavailable / validation / github_error reason on failure. " +
		"The approval modal fires once showing the full title + body + " +
		"base before the call lands."
}

func (t *GHPRCreateTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"base": map[string]any{
				"type":        "string",
				"description": "Base branch the PR merges into (required).",
			},
			"title": map[string]any{
				"type": "string",
				"description": fmt.Sprintf(
					"PR title (≤%d chars, no trailing period, single line).",
					PRTitleMaxLen),
			},
			"body": map[string]any{
				"type":        "string",
				"description": "PR body / description (required, multi-line allowed).",
			},
			"draft": map[string]any{
				"type":        "boolean",
				"description": "Open as draft. Default false.",
			},
		},
		"required": []string{"base", "title", "body"},
	}
}

func (t *GHPRCreateTool) RequiresApproval(string) bool { return true }

func (t *GHPRCreateTool) PreviewCall(argsJSON string) string {
	var a struct {
		Base, Title string
		Draft       bool
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Draft {
		return fmt.Sprintf("pr_create(base=%s, title=%q, draft=true)", a.Base, a.Title)
	}
	return fmt.Sprintf("pr_create(base=%s, title=%q)", a.Base, a.Title)
}

// PRCreateResult is the typed envelope CreatePR returns. Same shape
// rationale as CommitResult: callers branch on typed fields, not
// stringy err checks. Reason discriminates the failure mode so the
// procedural /create-pr handles each branch differently (validation
// → re-prompt; github_unavailable → fall through to draft-only; github_error
// → surface verbatim and stop).
type PRCreateResult struct {
	Created           bool
	URL               string
	Number            int
	ValidationErr     string
	GitHubUnavailable bool
	GitHubError       string
}

func (t *GHPRCreateTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Base  string `json:"base"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("pr_create: invalid args: %w", err)
	}
	if t.GH == nil {
		return "", errors.New("pr_create: no GitHub adapter configured")
	}

	head, err := livePRBranchRef(ctx, t.Cwd)
	if err != nil {
		return "", fmt.Errorf("pr_create: detect head branch: %w", err)
	}

	res, err := CreatePR(ctx, t.GH, github.CreatePRRequest{
		Base:  a.Base,
		Head:  head,
		Title: a.Title,
		Body:  a.Body,
		Draft: a.Draft,
	})
	if err != nil {
		return "", fmt.Errorf("pr_create: %w", err)
	}
	return renderPRCreateResult(res), nil
}

// livePRBranchRef resolves the branch from the session's live cwd.
// The typed GitHub client also has a cwd, but that value is fixed at
// session startup; after enter_worktree it may still point at main.
func livePRBranchRef(ctx context.Context, cwd *CwdRef) (string, error) {
	branch, err := gitOutput(ctx, cwd.Get(), "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(branch), nil
}

// livePRRefOrExplicit preserves caller-supplied PR refs while making
// empty-ref ergonomics use the same live cwd as local git tools.
func livePRRefOrExplicit(ctx context.Context, cwd *CwdRef, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" {
		return ref, nil
	}
	return livePRBranchRef(ctx, cwd)
}

// CreatePR is the deterministic core of pr_create. Validates
// title and required fields *before* dialing the Interface, so an
// oversize title or empty body never reaches the network. Returns a
// typed PRCreateResult; the tool wrapper renders it for model
// consumption, /create-pr reads it directly.
//
// The Interface returns ErrGitHubUnavailable when the local environment
// can't make the call; we surface that as GitHubUnavailable=true so the
// caller can fall through to draft-only instead of treating it as
// an opaque error.
func CreatePR(ctx context.Context, client github.Interface, req github.CreatePRRequest) (PRCreateResult, error) {
	var res PRCreateResult

	if v := validatePRTitle(req.Title); v != "" {
		res.ValidationErr = v
		return res, nil
	}
	if strings.TrimSpace(req.Body) == "" {
		res.ValidationErr = "body is empty"
		return res, nil
	}
	if strings.TrimSpace(req.Base) == "" {
		res.ValidationErr = "base is empty"
		return res, nil
	}

	out, err := client.CreatePR(ctx, req)
	if errors.Is(err, github.ErrGitHubUnavailable) {
		res.GitHubUnavailable = true
		return res, nil
	}
	if err != nil {
		res.GitHubError = err.Error()
		return res, nil
	}
	res.Created = true
	res.URL = out.URL
	res.Number = out.Number
	return res, nil
}

// validatePRTitle mirrors validateCommitSubject: rejects empty,
// multi-line, oversize, and trailing-period titles. Same cap as
// commit subjects keeps the two halves of the workflow visually
// consistent in the approval modal.
func validatePRTitle(title string) string {
	t := strings.TrimRight(title, "\n")
	if strings.TrimSpace(t) == "" {
		return "title is empty"
	}
	if strings.Contains(t, "\n") {
		return "title must be a single line"
	}
	if len(t) > PRTitleMaxLen {
		return fmt.Sprintf("title is %d chars; max is %d", len(t), PRTitleMaxLen)
	}
	if strings.HasSuffix(t, ".") {
		return "title must not end with a period"
	}
	return ""
}

// GHPRUpdateTool is the typed mutator that rewrites an existing
// PR's title and body. Paired with /git-update-pr for the
// "follow-up commits made the original description stale"
// workflow. Title validation reuses validatePRTitle (same rules
// as create-pr: ≤72 chars, no trailing period, single line);
// body must be non-empty because an empty body would clobber the
// existing description, which is almost never intended.
//
// Other PR-level edits (labels, base, reviewers, draft toggle)
// are intentionally out of scope. When a concrete workflow asks
// for them, they grow Interface.UpdatePR's request type rather
// than spawning a new tool.
type GHPRUpdateTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHPRUpdateTool) Name() string { return "pr_update" }

func (t *GHPRUpdateTool) Description() string {
	return "Rewrite an existing pull request's title and body. Ref is " +
		"the PR number or branch name; empty defaults to the current " +
		"branch's PR. Title is validated to be a single line of at most " +
		strconv.Itoa(PRTitleMaxLen) + " characters with no trailing " +
		"period; body must be non-empty. Returns a typed result with " +
		"the PR URL on success, or a github_unavailable / not_found / " +
		"validation / github_error reason on failure. The approval modal " +
		"fires once showing the new title + body + ref before the call " +
		"lands."
}

func (t *GHPRUpdateTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "PR number (\"17\") or branch name. Empty defaults to current branch.",
			},
			"title": map[string]any{
				"type": "string",
				"description": fmt.Sprintf(
					"New PR title (≤%d chars, no trailing period, single line).",
					PRTitleMaxLen),
			},
			"body": map[string]any{
				"type":        "string",
				"description": "New PR body / description (required, multi-line allowed). Empty would clobber the existing body — not accepted.",
			},
		},
		"required": []string{"title", "body"},
	}
}

func (t *GHPRUpdateTool) RequiresApproval(string) bool { return true }

func (t *GHPRUpdateTool) PreviewCall(argsJSON string) string {
	var a struct {
		Ref, Title string
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Ref == "" {
		return fmt.Sprintf("pr_update(title=%q)", a.Title)
	}
	return fmt.Sprintf("pr_update(ref=%s, title=%q)", a.Ref, a.Title)
}

// PRUpdateResult is the typed envelope UpdatePR returns. Same
// shape rationale as PRCreateResult: callers branch on typed
// fields. Reason discriminates the failure mode so the
// procedural /git-update-pr handles each branch differently.
type PRUpdateResult struct {
	Updated           bool
	URL               string
	Number            int
	ValidationErr     string
	GitHubUnavailable bool
	NotFound          bool
	GitHubError       string
}

func (t *GHPRUpdateTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Ref   string `json:"ref"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("pr_update: invalid args: %w", err)
	}
	if t.GH == nil {
		return "", errors.New("pr_update: no GitHub adapter configured")
	}
	ref, err := livePRRefOrExplicit(ctx, t.Cwd, a.Ref)
	if err != nil {
		return "", fmt.Errorf("pr_update: detect current branch: %w", err)
	}
	res, err := UpdatePR(ctx, t.GH, github.UpdatePRRequest{
		Ref:   ref,
		Title: a.Title,
		Body:  a.Body,
	})
	if err != nil {
		return "", fmt.Errorf("pr_update: %w", err)
	}
	return renderPRUpdateResult(res), nil
}

// UpdatePR is the deterministic core of pr_update. Validates
// title and body *before* dialing the Interface, so oversize
// titles and empty bodies never reach the network. Returns a
// typed PRUpdateResult; the tool wrapper renders it for model
// consumption.
//
// The Interface's ErrPRNotFound and ErrGitHubUnavailable get folded
// into typed envelope fields so callers branch on flags rather
// than err strings.
func UpdatePR(ctx context.Context, client github.Interface, req github.UpdatePRRequest) (PRUpdateResult, error) {
	var res PRUpdateResult

	if v := validatePRTitle(req.Title); v != "" {
		res.ValidationErr = v
		return res, nil
	}
	if strings.TrimSpace(req.Body) == "" {
		res.ValidationErr = "body is empty (would clobber the existing PR description)"
		return res, nil
	}

	out, err := client.UpdatePR(ctx, req)
	if errors.Is(err, github.ErrGitHubUnavailable) {
		res.GitHubUnavailable = true
		return res, nil
	}
	if errors.Is(err, github.ErrPRNotFound) {
		res.NotFound = true
		return res, nil
	}
	if err != nil {
		res.GitHubError = err.Error()
		return res, nil
	}
	res.Updated = true
	res.URL = out.URL
	res.Number = out.Number
	return res, nil
}

// renderPRUpdateResult shapes the result envelope for the model.
// Symmetric with renderPRCreateResult; the difference is the
// NotFound branch (create can't 404 — it makes new) and the
// "(no auto-edit beyond title+body)" guidance.
func renderPRUpdateResult(r PRUpdateResult) string {
	var b strings.Builder
	switch {
	case r.ValidationErr != "":
		fmt.Fprintf(&b, "updated=false reason=validation\nerror=%s\n", r.ValidationErr)
	case r.GitHubUnavailable:
		b.WriteString("updated=false reason=github_unavailable\n")
		b.WriteString("Hint: install gh and run `gh auth login`.\n")
	case r.NotFound:
		b.WriteString("updated=false reason=not_found\n")
		b.WriteString("Hint: no PR matches the ref. Run /git-create-pr to open one instead.\n")
	case r.GitHubError != "":
		b.WriteString("updated=false reason=github_error\n")
		b.WriteString("--- gh output ---\n")
		b.WriteString(r.GitHubError)
		if !strings.HasSuffix(r.GitHubError, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- end gh output ---\n")
		b.WriteString("Do NOT auto-retry, auto-edit labels/reviewers, or auto-merge. Surface the error and stop.\n")
	case r.Updated:
		fmt.Fprintf(&b, "updated=true url=%s number=%d\n", r.URL, r.Number)
	default:
		b.WriteString("updated=false reason=unknown\n")
	}
	return b.String()
}

// prCommentBodyCap bounds the comment body the model can post in
// one call. GitHub itself accepts up to ~65k characters, but
// model-generated comments that long are almost always a mistake
// (looping output, runaway template). Cap pre-network so we fail
// fast on validation rather than burning a round trip.
const prCommentBodyCap = 16 * 1024

// GHPRAddCommentTool posts a top-level conversation comment on a
// PR. Approval-gated — every comment goes through the modal so the
// user reads the body before it lands publicly on GitHub.
//
// Companion to pr_create / pr_update for the write surface.
// Validates body length in Go before dialing the Interface so
// runaway template output can't reach the network.
type GHPRAddCommentTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHPRAddCommentTool) Name() string { return "pr_add_comment" }

func (t *GHPRAddCommentTool) Description() string {
	return "Post a top-level conversation comment on a pull request. " +
		"Body is the comment Markdown — required, non-empty, capped at " +
		strconv.Itoa(prCommentBodyCap) + " characters. Use ref to " +
		"target a specific PR (number or branch name); empty ref " +
		"defaults to the current branch. Approval-gated: the modal " +
		"renders the full body so the user reads it before it lands. " +
		"Used to cross-link related issues (`Refs #42`), post follow-" +
		"up notes after a /git-review-pr run, or surface structured " +
		"summaries. NOT for inline review comments on specific lines " +
		"— that's a different GitHub API surface and out of scope here."
}

func (t *GHPRAddCommentTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "PR number (\"17\") or branch name. Empty defaults to current branch.",
			},
			"body": map[string]any{
				"type": "string",
				"description": fmt.Sprintf(
					"Comment Markdown (required, ≤%d chars).", prCommentBodyCap),
			},
		},
		"required": []string{"body"},
	}
}

func (t *GHPRAddCommentTool) RequiresApproval(string) bool { return true }

func (t *GHPRAddCommentTool) PreviewCall(argsJSON string) string {
	var a struct {
		Ref  string `json:"ref"`
		Body string `json:"body"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	excerpt := previewExcerpt(a.Body, 80)
	if strings.TrimSpace(a.Ref) == "" {
		return fmt.Sprintf("pr_add_comment(body=%q)", excerpt)
	}
	return fmt.Sprintf("pr_add_comment(ref=%s, body=%q)", a.Ref, excerpt)
}

// PRAddCommentResult is the typed envelope AddPRComment returns
// through the tool wrapper. Reason discriminates the failure mode
// — same pattern as PRCreateResult / PRUpdateResult so the model
// brief and slash commands can branch on typed flags rather than
// stringy error checks.
type PRAddCommentResult struct {
	Posted            bool
	URL               string
	ID                int64
	ValidationErr     string
	GitHubUnavailable bool
	NotFound          bool
	GitHubError       string
}

func (t *GHPRAddCommentTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Ref  string `json:"ref"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("pr_add_comment: invalid args: %w", err)
	}
	if t.GH == nil {
		return "", errors.New("pr_add_comment: no GitHub adapter configured")
	}

	ref, err := livePRRefOrExplicit(ctx, t.Cwd, a.Ref)
	if err != nil {
		return "", fmt.Errorf("pr_add_comment: detect current branch: %w", err)
	}
	res, err := AddPRComment(ctx, t.GH, github.AddPRCommentRequest{
		Ref:  ref,
		Body: a.Body,
	})
	if err != nil {
		return "", fmt.Errorf("pr_add_comment: %w", err)
	}
	return renderPRAddCommentResult(res), nil
}

// AddPRComment is the deterministic core of pr_add_comment.
// Validates body length and presence in Go before dialing the
// Interface; folds typed errors (ErrPRNotFound, ErrGitHubUnavailable)
// into the typed result envelope so callers branch on flags rather
// than err strings.
func AddPRComment(ctx context.Context, client github.Interface, req github.AddPRCommentRequest) (PRAddCommentResult, error) {
	var res PRAddCommentResult
	body := strings.TrimSpace(req.Body)
	if body == "" {
		res.ValidationErr = "body is empty"
		return res, nil
	}
	if len(body) > prCommentBodyCap {
		res.ValidationErr = fmt.Sprintf(
			"body is %d chars; cap is %d", len(body), prCommentBodyCap)
		return res, nil
	}

	posted, err := client.AddPRComment(ctx, req)
	if errors.Is(err, github.ErrGitHubUnavailable) {
		res.GitHubUnavailable = true
		return res, nil
	}
	if errors.Is(err, github.ErrPRNotFound) {
		res.NotFound = true
		return res, nil
	}
	if err != nil {
		res.GitHubError = err.Error()
		return res, nil
	}
	res.Posted = true
	res.URL = posted.URL
	res.ID = posted.ID
	return res, nil
}

// renderPRAddCommentResult shapes the result envelope for the
// model. Same shape rationale as renderPRUpdateResult.
func renderPRAddCommentResult(r PRAddCommentResult) string {
	var b strings.Builder
	switch {
	case r.ValidationErr != "":
		fmt.Fprintf(&b, "posted=false reason=validation\nerror=%s\n", r.ValidationErr)
	case r.GitHubUnavailable:
		b.WriteString("posted=false reason=github_unavailable\n")
		b.WriteString("Hint: install gh and run `gh auth login`, or set $GITHUB_TOKEN.\n")
	case r.NotFound:
		b.WriteString("posted=false reason=not_found\n")
		b.WriteString("Hint: no PR matches the ref. Verify the number or branch name.\n")
	case r.GitHubError != "":
		b.WriteString("posted=false reason=github_error\n")
		b.WriteString("--- gh output ---\n")
		b.WriteString(r.GitHubError)
		if !strings.HasSuffix(r.GitHubError, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- end gh output ---\n")
		b.WriteString("Do NOT auto-retry. Surface the error and stop.\n")
	case r.Posted:
		fmt.Fprintf(&b, "posted=true url=%s id=%d\n", r.URL, r.ID)
	default:
		b.WriteString("posted=false reason=unknown\n")
	}
	return b.String()
}

// previewExcerpt collapses a multi-line body into a single-line
// excerpt for tool preview rendering, capped at `cap` characters
// with an ellipsis suffix when truncated. Pulled out so the
// add-comment preview and any future preview that wants a body
// excerpt can share the same implementation.
func previewExcerpt(body string, cap int) string {
	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.Join(strings.Fields(body), " ")
	if len(body) <= cap {
		return body
	}
	return body[:cap] + "…"
}

// prReviewDiffCap bounds the diff body the review snapshot
// surfaces. Bigger than the pr_context diffstat cap (16 KiB)
// because reviewers need the actual hunks, not just file names —
// but bounded enough that a long-running branch's accumulated
// diff doesn't blow the prompt cache. 64 KiB covers most
// reasonable PRs; larger PRs surface a truncation marker that
// points the model at running gh pr diff for the rest.
const prReviewDiffCap = 64 * 1024

// GHPRReviewContextTool fetches everything /git-review-pr needs in
// one composite call: PR metadata, full diff (capped), and the
// status check rollup. Counterpart to pr_context (which gathers
// local pre-PR state); this one talks to an existing PR via the
// github.Interface.
//
// Read-only — no approval modal — but does touch the network and so
// is NOT marked parallel-safe. The fetch fans out to three
// Interface calls (ReadPR + ListPRChecks + ReadPRDiff). When the
// caller only needs PR metadata (title, body, state, labels), the
// cheaper pr_read tool is the right choice — see its Description
// for the selection rule.
type GHPRReviewContextTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHPRReviewContextTool) Name() string { return "pr_review_context" }

func (t *GHPRReviewContextTool) Description() string {
	return "Fetch everything needed to review an existing pull request: " +
		"PR metadata (title, body, state, draft, base, head, mergeable, " +
		"author, labels, URL) + the full unified diff (capped) + every " +
		"check-run with state and conclusion + a failing-checks summary. " +
		"Use this for any PR review, audit, or 'what changed and is CI " +
		"green' question — three API calls, one snapshot. Prefer this " +
		"over shelling out to `gh pr view`, `gh pr diff`, or `gh pr " +
		"checks` from run_bash: the typed call is faster, handles auth " +
		"directly, and returns structured data the model can branch on " +
		"(## state flags for not_found / github_unavailable; ## " +
		"checks.summary for CI rollup). If you only need PR metadata " +
		"(no diff, no checks), use pr_read instead — single API call, " +
		"same metadata shape. Ref is the PR number or branch name; " +
		"empty defaults to the current branch."
}

func (t *GHPRReviewContextTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "PR number (\"17\") or branch name. Empty defaults to current branch.",
			},
		},
	}
}

func (t *GHPRReviewContextTool) RequiresApproval(string) bool { return false }

func (t *GHPRReviewContextTool) PreviewCall(argsJSON string) string {
	var a struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Ref) == "" {
		return "pr_review_context()"
	}
	return fmt.Sprintf("pr_review_context(ref=%s)", a.Ref)
}

func (t *GHPRReviewContextTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.GH == nil {
		return "", errors.New("pr_review_context: no GitHub adapter configured")
	}
	var a struct {
		Ref string `json:"ref"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	ref, err := livePRRefOrExplicit(ctx, t.Cwd, a.Ref)
	if err != nil {
		return "", fmt.Errorf("pr_review_context: detect current branch: %w", err)
	}
	snap, err := BuildPRReviewContext(ctx, t.GH, ref)
	if err != nil {
		return "", fmt.Errorf("pr_review_context: %w", err)
	}
	return renderPRReviewContext(snap), nil
}

// PRReviewContext is the typed snapshot the review tool returns.
// Same shape rationale as CommitContext / PRContext: callers
// branch on typed fields, and the rendered string is for the
// model's consumption rather than the only access path.
type PRReviewContext struct {
	Ref               string
	NotFound          bool   // true when gh couldn't resolve the ref
	GitHubUnavailable bool   // true when gh is missing / unauthed
	FetchErr          string // any other Interface error, surfaced verbatim

	PR            github.PRDetails
	Checks        []github.CheckRun
	Diff          string
	DiffCapped    bool
	FailingChecks []string
}

// BuildPRReviewContext is the deterministic core of
// pr_review_context. Fans out the three Interface calls
// (ReadPR, ListPRChecks, ReadPRDiff), folds the typed errors into
// the snapshot's NotFound / GitHubUnavailable flags so the caller can
// branch without parsing err strings, and computes the
// FailingChecks list for the slash command to surface at the top
// of the review.
func BuildPRReviewContext(ctx context.Context, client github.Interface, ref string) (PRReviewContext, error) {
	snap := PRReviewContext{Ref: ref}

	req := github.ReadPRRequest{Ref: ref}
	pr, err := client.ReadPR(ctx, req)
	if errors.Is(err, github.ErrGitHubUnavailable) {
		snap.GitHubUnavailable = true
		return snap, nil
	}
	if errors.Is(err, github.ErrPRNotFound) {
		snap.NotFound = true
		return snap, nil
	}
	if err != nil {
		snap.FetchErr = err.Error()
		return snap, nil
	}
	snap.PR = pr

	checks, err := client.ListPRChecks(ctx, req)
	if err != nil && !errors.Is(err, github.ErrPRNotFound) {
		// Checks fetch failure is soft — the PR metadata is the
		// load-bearing piece. Surface as FetchErr so the snapshot
		// hints at the gap without blocking the review.
		snap.FetchErr = "checks: " + err.Error()
	}
	snap.Checks = checks
	for _, c := range checks {
		if isFailingCheck(c) {
			snap.FailingChecks = append(snap.FailingChecks, c.Name)
		}
	}

	diff, err := client.ReadPRDiff(ctx, req)
	if err != nil && !errors.Is(err, github.ErrPRNotFound) {
		// Same soft-fail rationale as checks: the review can run
		// against metadata alone if the diff fetch hiccups.
		if snap.FetchErr != "" {
			snap.FetchErr += "; diff: " + err.Error()
		} else {
			snap.FetchErr = "diff: " + err.Error()
		}
	}
	snap.Diff, snap.DiffCapped = capString(diff, prReviewDiffCap)

	return snap, nil
}

// isFailingCheck classifies a check run as broken. We surface
// FAILURE / CANCELLED / TIMED_OUT / ACTION_REQUIRED as failing
// because each represents a state a human reviewer would want
// flagged. NEUTRAL and SKIPPED are non-blocking; PENDING /
// IN_PROGRESS / QUEUED are still in flight (the snapshot's
// rendered output covers them separately).
func isFailingCheck(c github.CheckRun) bool {
	switch strings.ToUpper(c.Conclusion) {
	case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED":
		return true
	}
	// Some Status-API rollups don't populate Conclusion separately
	// from State — handle the State==FAILURE shape too.
	switch strings.ToUpper(c.State) {
	case "FAILURE", "ERROR":
		return true
	}
	return false
}

// renderPRReviewContext flattens the snapshot into the labeled
// sections the model reads. Order: ## state header (typed flags
// the caller branches on first) → ## pr metadata → ## checks
// summary + list → ## diff. Failing-check names are duplicated
// in ## state and the summary so a model scanning either
// surface picks them up.
func renderPRReviewContext(s PRReviewContext) string {
	var b strings.Builder

	// ## state goes first so the caller branches on the typed
	// flags before doing any rendering work.
	fmt.Fprintf(&b, "## state\nref=%s\nnot_found=%v\ngithub_unavailable=%v\n",
		s.Ref, s.NotFound, s.GitHubUnavailable)
	if s.FetchErr != "" {
		fmt.Fprintf(&b, "fetch_error=%s\n", s.FetchErr)
	}
	if len(s.FailingChecks) > 0 {
		fmt.Fprintf(&b, "failing_checks=%s\n", strings.Join(s.FailingChecks, ","))
	}

	if s.NotFound || s.GitHubUnavailable {
		// Nothing more to render — the slash command surfaces a
		// "no PR found" / "gh unavailable" message and stops.
		return strings.TrimRight(b.String(), "\n") + "\n"
	}

	b.WriteString("\n## pr\n")
	fmt.Fprintf(&b, "number=%d\nstate=%s\ndraft=%v\nbase=%s\nhead=%s\nhead_sha=%s\nmergeable=%s\nauthor=%s\nurl=%s\n",
		s.PR.Number, s.PR.State, s.PR.Draft, s.PR.BaseRef, s.PR.HeadRef,
		s.PR.HeadSHA, s.PR.Mergeable, s.PR.Author, s.PR.URL)
	fmt.Fprintf(&b, "title=%s\n", s.PR.Title)
	if len(s.PR.Labels) > 0 {
		fmt.Fprintf(&b, "labels=%s\n", strings.Join(s.PR.Labels, ","))
	}
	if strings.TrimSpace(s.PR.Body) != "" {
		b.WriteString("body=|\n")
		for _, line := range strings.Split(s.PR.Body, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## checks.summary\n")
	renderChecksSummaryBody(&b, s.Checks)
	renderChecksList(&b, s.Checks)

	b.WriteString("\n## diff\n")
	if strings.TrimSpace(s.Diff) == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(s.Diff)
		if !strings.HasSuffix(s.Diff, "\n") {
			b.WriteString("\n")
		}
		if s.DiffCapped {
			fmt.Fprintf(&b, "[truncated at %d bytes — run gh pr diff %s for the full diff]\n",
				prReviewDiffCap, s.Ref)
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

const (
	prWatchDefaultTimeout      = 15 * time.Minute
	prWatchDefaultPollInterval = 15 * time.Second
	prWatchDefaultLogTailLines = 240
	prWatchMaxLogTailLines     = 1000
	prWatchMaxTimeout          = 2 * time.Hour
)

// PRWatchChecksTool waits for a PR's checks to reach a useful terminal state.
// It replaces shell pipelines such as `gh run watch ...; gh run view
// --log-failed | tail` with a bounded, read-only typed GitHub workflow.
type PRWatchChecksTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *PRWatchChecksTool) Name() string { return "pr_watch_checks" }

func (t *PRWatchChecksTool) Description() string {
	return "Watch a pull request's checks until they pass, fail, or time out, " +
		"then return a typed CI snapshot and capped failed-job log tails. " +
		"Use this instead of shelling out to `gh run watch`, `gh pr checks`, " +
		"or `gh run view --log-failed | tail` from run_bash when the task is " +
		"to wait for PR CI. Ref is the PR number or branch name; empty " +
		"defaults to the current branch's PR. No approval; read-only network " +
		"tool."
}

func (t *PRWatchChecksTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"ref":                   map[string]any{"type": "string", "description": "PR number or branch name. Empty defaults to current branch."},
		"timeout_seconds":       map[string]any{"type": "integer", "description": "Maximum seconds to wait. Default 900; bounded to 1..7200."},
		"poll_interval_seconds": map[string]any{"type": "integer", "description": "Seconds between check polls. Default 15; bounded to 1..300."},
		"log_tail_lines":        map[string]any{"type": "integer", "description": "Failed-job log tail lines per job. Default 240; bounded to 0..1000."},
	}}
}

func (t *PRWatchChecksTool) RequiresApproval(string) bool { return false }

func (t *PRWatchChecksTool) PreviewCall(argsJSON string) string {
	var a struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Ref) == "" {
		return "pr_watch_checks()"
	}
	return fmt.Sprintf("pr_watch_checks(ref=%s)", a.Ref)
}

func (t *PRWatchChecksTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.GH == nil {
		return "", errors.New("pr_watch_checks: no GitHub adapter configured")
	}
	var a struct {
		Ref                 string `json:"ref"`
		TimeoutSeconds      int    `json:"timeout_seconds"`
		PollIntervalSeconds int    `json:"poll_interval_seconds"`
		LogTailLines        int    `json:"log_tail_lines"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	ref, err := livePRRefOrExplicit(ctx, t.Cwd, a.Ref)
	if err != nil {
		return "", fmt.Errorf("pr_watch_checks: detect current branch: %w", err)
	}
	snap, err := WatchPRChecks(ctx, t.GH, PRWatchOptions{
		Ref:          ref,
		Timeout:      secondsOrDefault(a.TimeoutSeconds, prWatchDefaultTimeout, prWatchMaxTimeout),
		PollInterval: secondsOrDefault(a.PollIntervalSeconds, prWatchDefaultPollInterval, 5*time.Minute),
		LogTailLines: boundedInt(a.LogTailLines, prWatchDefaultLogTailLines, 0, prWatchMaxLogTailLines),
	})
	if err != nil {
		return "", fmt.Errorf("pr_watch_checks: %w", err)
	}
	return renderPRWatchChecks(snap), nil
}

type PRWatchOptions struct {
	Ref          string
	Timeout      time.Duration
	PollInterval time.Duration
	LogTailLines int
}

type PRWatchChecksSnapshot struct {
	Ref                string
	NotFound           bool
	GitHubUnavailable  bool
	TimedOut           bool
	AllSuccess         bool
	Failed             bool
	FetchErr           string
	FailedLogsFetchErr string
	PR                 github.PRDetails
	Checks             []github.CheckRun
	FailingChecks      []string
	FailedLogs         github.FailedWorkflowLogsResult
}

func WatchPRChecks(ctx context.Context, client github.Interface, opts PRWatchOptions) (PRWatchChecksSnapshot, error) {
	snap := PRWatchChecksSnapshot{Ref: opts.Ref}
	if opts.Timeout <= 0 {
		opts.Timeout = prWatchDefaultTimeout
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = prWatchDefaultPollInterval
	}
	req := github.ReadPRRequest{Ref: opts.Ref}
	pr, err := client.ReadPR(ctx, req)
	if errors.Is(err, github.ErrGitHubUnavailable) {
		snap.GitHubUnavailable = true
		return snap, nil
	}
	if errors.Is(err, github.ErrPRNotFound) {
		snap.NotFound = true
		return snap, nil
	}
	if err != nil {
		snap.FetchErr = err.Error()
		return snap, nil
	}
	snap.PR = pr
	deadline := time.Now().Add(opts.Timeout)
	// Poll until a terminal state or the exact timeout. Empty check lists
	// are treated as pending because GitHub can lag between a push and
	// check-suite creation.
	for {
		checks, err := client.ListPRChecks(ctx, req)
		if errors.Is(err, github.ErrGitHubUnavailable) {
			snap.GitHubUnavailable = true
			return snap, nil
		}
		if errors.Is(err, github.ErrPRNotFound) {
			snap.NotFound = true
			return snap, nil
		}
		if err != nil {
			snap.FetchErr = err.Error()
			return snap, nil
		}
		snap.Checks = checks
		snap.FailingChecks = failingCheckNames(checks)
		snap.Failed = len(snap.FailingChecks) > 0
		snap.AllSuccess = len(checks) > 0 && allChecksTerminalSuccess(checks)
		if snap.Failed || snap.AllSuccess {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			snap.TimedOut = true
			break
		}
		sleepFor := opts.PollInterval
		if sleepFor > remaining {
			sleepFor = remaining
		}
		select {
		case <-ctx.Done():
			return snap, ctx.Err()
		case <-time.After(sleepFor):
		}
	}
	if snap.Failed && strings.TrimSpace(pr.HeadSHA) != "" && opts.LogTailLines > 0 {
		logs, err := client.ListFailedWorkflowJobLogTails(ctx, github.FailedWorkflowLogsRequest{HeadSHA: pr.HeadSHA, TailLines: opts.LogTailLines, MaxRuns: 10})
		if err != nil {
			snap.FailedLogsFetchErr = err.Error()
		} else {
			snap.FailedLogs = logs
		}
	}
	return snap, nil
}

func secondsOrDefault(seconds int, def, max time.Duration) time.Duration {
	if seconds <= 0 {
		return def
	}
	d := time.Duration(seconds) * time.Second
	if d > max {
		return max
	}
	return d
}

func boundedInt(v, def, min, max int) int {
	if v == 0 {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func failingCheckNames(checks []github.CheckRun) []string {
	var out []string
	for _, c := range checks {
		if isFailingCheck(c) {
			out = append(out, c.Name)
		}
	}
	return out
}

func allChecksTerminalSuccess(checks []github.CheckRun) bool {
	for _, c := range checks {
		if !isTerminalSuccessfulCheck(c) {
			return false
		}
	}
	return true
}

func isTerminalSuccessfulCheck(c github.CheckRun) bool {
	conclusion := strings.ToUpper(strings.TrimSpace(c.Conclusion))
	state := strings.ToUpper(strings.TrimSpace(c.State))
	if conclusion == "SUCCESS" || conclusion == "NEUTRAL" || conclusion == "SKIPPED" {
		return true
	}
	return conclusion == "" && state == "SUCCESS"
}

func renderPRWatchChecks(s PRWatchChecksSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## state\nref=%s\nnot_found=%v\ngithub_unavailable=%v\ntimed_out=%v\nall_success=%v\nfailed=%v\n", s.Ref, s.NotFound, s.GitHubUnavailable, s.TimedOut, s.AllSuccess, s.Failed)
	if len(s.FailingChecks) > 0 {
		fmt.Fprintf(&b, "failing_checks=%s\n", strings.Join(s.FailingChecks, ","))
	}
	if s.FetchErr != "" {
		fmt.Fprintf(&b, "fetch_error=%s\n", s.FetchErr)
	}
	if s.FailedLogsFetchErr != "" {
		fmt.Fprintf(&b, "failed_logs_fetch_error=%s\n", s.FailedLogsFetchErr)
	}
	if s.NotFound || s.GitHubUnavailable {
		return strings.TrimRight(b.String(), "\n") + "\n"
	}
	b.WriteString("\n## pr\n")
	fmt.Fprintf(&b, "number=%d\nstate=%s\ndraft=%v\nbase=%s\nhead=%s\nhead_sha=%s\nurl=%s\n", s.PR.Number, s.PR.State, s.PR.Draft, s.PR.BaseRef, s.PR.HeadRef, s.PR.HeadSHA, s.PR.URL)
	fmt.Fprintf(&b, "title=%s\n", s.PR.Title)
	renderChecksSummary(&b, s.Checks)
	renderChecksList(&b, s.Checks)
	b.WriteString("\n## failed_logs\n")
	if len(s.FailedLogs.Jobs) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, job := range s.FailedLogs.Jobs {
			fmt.Fprintf(&b, "### %s / %s [%s]\n", job.WorkflowName, job.JobName, job.Conclusion)
			if job.JobURL != "" {
				fmt.Fprintf(&b, "url=%s\n", job.JobURL)
			}
			if job.LogError != "" {
				fmt.Fprintf(&b, "log_error=%s\n", job.LogError)
				continue
			}
			for _, line := range job.LogTail {
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderChecksSummary(b *strings.Builder, checks []github.CheckRun) {
	b.WriteString("\n## checks.summary\n")
	renderChecksSummaryBody(b, checks)
}

func renderChecksSummaryBody(b *strings.Builder, checks []github.CheckRun) {
	if len(checks) == 0 {
		b.WriteString("(no checks)\n")
		return
	}
	pass, fail, pending := 0, 0, 0
	for _, c := range checks {
		switch {
		case isFailingCheck(c):
			fail++
		case strings.ToUpper(c.Conclusion) == "SUCCESS" || strings.ToUpper(c.State) == "SUCCESS":
			pass++
		default:
			pending++
		}
	}
	fmt.Fprintf(b, "total=%d pass=%d fail=%d pending=%d\n", len(checks), pass, fail, pending)
}

func renderChecksList(b *strings.Builder, checks []github.CheckRun) {
	b.WriteString("\n## checks\n")
	if len(checks) == 0 {
		b.WriteString("(none)\n")
		return
	}
	for _, c := range checks {
		state := c.State
		if c.Conclusion != "" {
			state = c.State + "/" + c.Conclusion
		}
		fmt.Fprintf(b, "- %s [%s]\n", c.Name, state)
	}
}

// GHPRReadTool is the cheap counterpart to GHPRReviewContextTool —
// a single ReadPR call returning just the PR metadata (no diff, no
// checks). Exists so the model has a body-only read path that
// doesn't pull megabytes of diff + every check-run when the task
// is "fetch the PR description" or "what's the title of #29".
//
// Read-only, no approval modal, single API call. The cheap default
// for any PR-metadata question; pr_review_context stays the
// right choice when the model also needs the diff or check status.
type GHPRReadTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GHPRReadTool) Name() string { return "pr_read" }

func (t *GHPRReadTool) Description() string {
	return "Fetch typed metadata for a single pull request: number, " +
		"title, body, state, draft flag, base/head refs, head SHA, " +
		"mergeable state, author, labels, URL. ONE API call — no diff, " +
		"no check-runs. Prefer this over shelling out to `gh pr view " +
		"--json ...` from run_bash whenever the goal is reading PR " +
		"metadata: faster, no subprocess, structured result. Use " +
		"pr_review_context instead when the diff or CI status also " +
		"matters (PR review, audit). Ref is the PR number or branch " +
		"name; empty defaults to the current branch. Returns a typed " +
		"snapshot keyed by section headers (## state then ## pr); " +
		"## state flags not_found / github_unavailable so the caller can " +
		"branch without parsing free-text errors."
}

func (t *GHPRReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "PR number (\"17\") or branch name. Empty defaults to current branch.",
			},
		},
	}
}

func (t *GHPRReadTool) RequiresApproval(string) bool { return false }

func (t *GHPRReadTool) PreviewCall(argsJSON string) string {
	var a struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Ref) == "" {
		return "pr_read()"
	}
	return fmt.Sprintf("pr_read(ref=%s)", a.Ref)
}

func (t *GHPRReadTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.GH == nil {
		return "", errors.New("pr_read: no GitHub adapter configured")
	}
	var a struct {
		Ref string `json:"ref"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	ref, err := livePRRefOrExplicit(ctx, t.Cwd, a.Ref)
	if err != nil {
		return "", fmt.Errorf("pr_read: detect current branch: %w", err)
	}
	snap := BuildPRReadContext(ctx, t.GH, ref)
	return renderPRReadContext(snap), nil
}

// PRReadContext is the typed snapshot pr_read returns. Same
// state-flag pattern as PRReviewContext (NotFound / GitHubUnavailable
// / FetchErr) so the model branches on typed flags before reading
// the metadata. PR is zero-valued when the read failed.
type PRReadContext struct {
	Ref               string
	NotFound          bool
	GitHubUnavailable bool
	FetchErr          string

	PR github.PRDetails
}

// BuildPRReadContext is the deterministic core of pr_read.
// Wraps a single Interface.ReadPR call and folds the typed errors
// into the snapshot's flags. Doesn't return an error itself —
// every failure shape is captured in the snapshot so callers can
// branch on flags rather than err strings.
func BuildPRReadContext(ctx context.Context, client github.Interface, ref string) PRReadContext {
	snap := PRReadContext{Ref: ref}
	pr, err := client.ReadPR(ctx, github.ReadPRRequest{Ref: ref})
	if errors.Is(err, github.ErrGitHubUnavailable) {
		snap.GitHubUnavailable = true
		return snap
	}
	if errors.Is(err, github.ErrPRNotFound) {
		snap.NotFound = true
		return snap
	}
	if err != nil {
		snap.FetchErr = err.Error()
		return snap
	}
	snap.PR = pr
	return snap
}

// renderPRReadContext mirrors renderPRReviewContext's state-first
// layout so the model can apply the same parsing template to both
// tools. Diff and checks sections intentionally omitted — that's
// the entire point of pr_read.
func renderPRReadContext(s PRReadContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## state\nref=%s\nnot_found=%v\ngithub_unavailable=%v\n",
		s.Ref, s.NotFound, s.GitHubUnavailable)
	if s.FetchErr != "" {
		fmt.Fprintf(&b, "fetch_error=%s\n", s.FetchErr)
	}

	if s.NotFound || s.GitHubUnavailable {
		return strings.TrimRight(b.String(), "\n") + "\n"
	}

	b.WriteString("\n## pr\n")
	fmt.Fprintf(&b, "number=%d\nstate=%s\ndraft=%v\nbase=%s\nhead=%s\nhead_sha=%s\nmergeable=%s\nauthor=%s\nurl=%s\n",
		s.PR.Number, s.PR.State, s.PR.Draft, s.PR.BaseRef, s.PR.HeadRef,
		s.PR.HeadSHA, s.PR.Mergeable, s.PR.Author, s.PR.URL)
	fmt.Fprintf(&b, "title=%s\n", s.PR.Title)
	if len(s.PR.Labels) > 0 {
		fmt.Fprintf(&b, "labels=%s\n", strings.Join(s.PR.Labels, ","))
	}
	if strings.TrimSpace(s.PR.Body) != "" {
		b.WriteString("body=|\n")
		for _, line := range strings.Split(s.PR.Body, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderPRCreateResult shapes the result envelope for the model.
// Field names match PRCreateResult so a model parsing the output
// maps back without ambiguity. The "draft preview" instruction at
// the bottom of the github_unavailable branch is the procedural
// fallback the legacy directive named — surfacing it inside the
// tool result keeps it discoverable without forcing the prompt to
// repeat the guidance.
func renderPRCreateResult(r PRCreateResult) string {
	var b strings.Builder
	switch {
	case r.ValidationErr != "":
		fmt.Fprintf(&b, "created=false reason=validation\nerror=%s\n", r.ValidationErr)
	case r.GitHubUnavailable:
		b.WriteString("created=false reason=github_unavailable\n")
		b.WriteString("Hint: install gh and run `gh auth login`, or surface the drafted title + body to the user so they can paste them into GitHub manually.\n")
	case r.GitHubError != "":
		b.WriteString("created=false reason=github_error\n")
		b.WriteString("--- gh output ---\n")
		b.WriteString(r.GitHubError)
		if !strings.HasSuffix(r.GitHubError, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- end gh output ---\n")
		b.WriteString("Do NOT auto-retry, auto-edit, or auto-merge. Surface the error and stop.\n")
	case r.Created:
		fmt.Fprintf(&b, "created=true url=%s number=%d\n", r.URL, r.Number)
	default:
		b.WriteString("created=false reason=unknown\n")
	}
	return b.String()
}
