package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Composite code-review tool — the local-diff sibling to
// gh_pr_workflow.go. Where gh_pr_review_context talks to an existing
// PR via the github.Interface, this one reviews the *local* diff
// (the current branch against its resolved base, or — when there are
// no commits ahead — the uncommitted working tree). That makes it a
// pure-local, Cwd-only tool: no GitHub client, no network.
//
// It is the Layer-1 read for /code-review. The slash directive turns
// the orchestrator loose to fan a batch of read-only review subagents
// at the diff and dedup/verify their findings; this tool's job is the
// deterministic part: resolve the base, pick the diff source, cap the
// blob to the effort level, and surface typed STOP flags
// (not_found_base / diff_empty) so the orchestrator branches without
// parsing free-text git errors.

const (
	// codeReviewDiffCap{Low,Medium,High} bound the diff body the review
	// snapshot surfaces, scaled by effort. Medium matches
	// prReviewDiffCap (64 KiB — covers most reasonable changes); low
	// tightens it for a quick scan; high widens it for a deep audit.
	// A larger diff surfaces a truncation marker rather than blowing
	// the prompt cache.
	codeReviewDiffCapLow    = 32 * 1024
	codeReviewDiffCapMedium = 64 * 1024
	codeReviewDiffCapHigh   = 128 * 1024

	// codeReviewChangedFilesCap bounds the name-status list. Generous
	// enough that the orchestrator sees every touched path on any
	// realistic change, capped so a vendored bulk-move can't dominate.
	codeReviewChangedFilesCap = 16 * 1024

	// codeReviewLogCommits caps the commit subjects surfaced for
	// style detection + context. Mirrors prContextLogCommits.
	codeReviewLogCommits = 25
)

// effortDiffCap maps an effort level to its diff byte cap. Unknown /
// empty levels fall through to medium — the same default the handler
// normalizes to, so the tool stays robust if called directly.
func effortDiffCap(effort string) int {
	switch effort {
	case "low":
		return codeReviewDiffCapLow
	case "high":
		return codeReviewDiffCapHigh
	default:
		return codeReviewDiffCapMedium
	}
}

// CodeReviewContextTool gathers everything /code-review needs to fan
// out a multi-angle review of the local diff in one composite call:
// the resolved base, the changed-file list, the capped diff, the
// commit log, and detected commit style. Read-only, no approval
// modal. Counterpart to gh_pr_review_context (which reviews an
// existing PR); this one reviews uncommitted/local work, so it needs
// no github.Interface.
type CodeReviewContextTool struct {
	Cwd *CwdRef
}

func (t *CodeReviewContextTool) Name() string { return "code_review_context" }

func (t *CodeReviewContextTool) Description() string {
	return "Gather the read-only context needed to review the current " +
		"branch's local diff: resolved base branch, changed-file list, " +
		"the unified diff (capped to the effort level), commit log, and " +
		"detected commit style. Reviews the branch against its base when " +
		"there are commits ahead, else the uncommitted working tree — so " +
		"it covers both 'review this branch' and 'review my pending work " +
		"before I commit'. Prefer this over shelling out to `git diff` / " +
		"`git log` from run_bash: one snapshot, structured, with typed " +
		"## state flags (not_found_base / diff_empty) the caller branches " +
		"on without parsing git errors. This is the Layer-1 read for the " +
		"/code-review command. effort is low|medium|high (default medium) " +
		"and only scales the diff cap. Returns a snapshot keyed by section " +
		"headers (## state, ## changed-files, ## diff, ## commit-log, " +
		"## style-context)."
}

func (t *CodeReviewContextTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"effort": map[string]any{
				"type":        "string",
				"description": "Review depth: low (quick scan), medium (standard), high (deep audit). Only scales the diff cap. Empty defaults to medium.",
			},
		},
	}
}

func (t *CodeReviewContextTool) RequiresApproval(string) bool { return false }

// ParallelSafe: the tool only reads git state (no network, no
// mutation), so it can ride a parallel tool batch like the other
// read-only git context tools.
func (t *CodeReviewContextTool) ParallelSafe(string) bool { return true }

func (t *CodeReviewContextTool) PreviewCall(argsJSON string) string {
	effort := normalizeEffort(parseEffortArg(argsJSON))
	return fmt.Sprintf("code_review_context(effort=%s)", effort)
}

func (t *CodeReviewContextTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	effort := normalizeEffort(parseEffortArg(argsJSON))
	snap, err := BuildCodeReviewContext(ctx, t.Cwd.Get(), effort)
	if err != nil {
		return "", fmt.Errorf("code_review_context: %w", err)
	}
	return renderCodeReviewContext(snap), nil
}

func parseEffortArg(argsJSON string) string {
	var a struct {
		Effort string `json:"effort"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	return strings.TrimSpace(a.Effort)
}

// normalizeEffort folds any input to one of low|medium|high, with
// medium as the default for empty / unrecognized values. Lives here
// (not just in the TUI handler) so the tool is correct when invoked
// directly by the model with an off-spec effort string.
func normalizeEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return "low"
	case "high":
		return "high"
	default:
		return "medium"
	}
}

// CodeReviewContext is the typed snapshot the review tool returns.
// Same shape rationale as PRReviewContext: callers branch on the
// typed flags, and the rendered string is the model's view rather
// than the only access path.
type CodeReviewContext struct {
	Effort         string
	CurrentBranch  string
	ResolvedBase   string
	BaseResolution string // "origin-head" | "fallback:<name>" | "unresolved"
	NotFoundBase   bool   // BaseResolution == "unresolved"
	DiffSource     string // "branch-vs-base" | "working-tree"
	AheadCount     int
	DiffEmpty      bool

	ChangedFiles  string // name-status, capped
	ChangedCapped bool
	Diff          string // unified diff, capped to effortDiffCap(effort)
	DiffCap       int
	DiffCapped    bool

	CommitLog     []string // "<short-sha> <subject>"; empty for working-tree source
	DetectedStyle string
}

// BuildCodeReviewContext is the deterministic core of
// code_review_context. It resolves the base (reusing
// resolveBaseBranch — the same logic gh_pr_context uses), decides the
// diff source, and folds the result into typed STOP flags. A missing
// git binary or a non-repo cwd surfaces as the error from the first
// gitOutput call.
func BuildCodeReviewContext(ctx context.Context, cwd, effort string) (CodeReviewContext, error) {
	snap := CodeReviewContext{Effort: effort, DiffCap: effortDiffCap(effort)}

	branch, err := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return snap, fmt.Errorf("current branch: %w", err)
	}
	snap.CurrentBranch = strings.TrimSpace(branch)

	snap.ResolvedBase, snap.BaseResolution = resolveBaseBranch(ctx, cwd, "")
	snap.NotFoundBase = snap.BaseResolution == "unresolved"
	if snap.NotFoundBase {
		// No base to diff against — the orchestrator surfaces the
		// message and stops; nothing else is meaningful.
		return snap, nil
	}

	// Pick the diff source. When the branch has commits ahead of its
	// base, review that range (three-dot, matching what a PR's
	// "Files changed" shows). Otherwise fall back to the working tree
	// so "review my pending work before I commit" also works.
	if cnt, err := gitOutput(ctx, cwd, "rev-list", "--count", snap.ResolvedBase+"..HEAD"); err == nil {
		snap.AheadCount, _ = strconv.Atoi(strings.TrimSpace(cnt))
	}

	var changedRange, diffRange string
	if snap.AheadCount > 0 {
		snap.DiffSource = "branch-vs-base"
		changedRange = snap.ResolvedBase + "...HEAD"
		diffRange = snap.ResolvedBase + "...HEAD"
	} else {
		snap.DiffSource = "working-tree"
		changedRange = "HEAD"
		diffRange = "HEAD"
	}

	// Changed files (name-status) and the diff body. Both git calls
	// are best-effort: an unusual repo state yields an empty section
	// rather than failing the whole snapshot.
	if names, err := gitOutput(ctx, cwd, "diff", "--name-status", changedRange); err == nil {
		snap.ChangedFiles, snap.ChangedCapped = capString(strings.TrimRight(names, "\n"), codeReviewChangedFilesCap)
	}
	if diff, err := gitOutput(ctx, cwd, "diff", diffRange); err == nil {
		snap.Diff, snap.DiffCapped = capString(diff, snap.DiffCap)
	}
	snap.DiffEmpty = strings.TrimSpace(snap.Diff) == ""

	// Commit log + style detection only make sense for the
	// branch-vs-base source; working-tree changes aren't committed yet.
	if snap.DiffSource == "branch-vs-base" {
		if logOut, err := gitOutput(ctx, cwd, "log",
			fmt.Sprintf("-%d", codeReviewLogCommits),
			"--format=%h %s", snap.ResolvedBase+"..HEAD"); err == nil {
			snap.CommitLog = splitNonEmptyLines(logOut)
		}
	}
	subjects := make([]string, 0, len(snap.CommitLog))
	for _, line := range snap.CommitLog {
		// drop the "<short-sha> " prefix before style detection
		if _, subject, ok := strings.Cut(line, " "); ok {
			subjects = append(subjects, subject)
		}
	}
	snap.DetectedStyle = detectCommitStyle(subjects)

	return snap, nil
}

// renderCodeReviewContext flattens the snapshot into the labeled
// sections the tool returns. ## state goes first so the orchestrator
// branches on the typed flags before reading anything else, and the
// render short-circuits after ## state on the two STOP conditions
// (mirrors renderPRReviewContext).
func renderCodeReviewContext(s CodeReviewContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## state\neffort=%s\ncurrent_branch=%s\nresolved_base=%s\nbase_resolution=%s\nnot_found_base=%v\ndiff_empty=%v\ndiff_source=%s\nahead_count=%d\ndiff_cap_bytes=%d\n",
		s.Effort, s.CurrentBranch, s.ResolvedBase, s.BaseResolution,
		s.NotFoundBase, s.DiffEmpty, s.DiffSource, s.AheadCount, s.DiffCap)

	if s.NotFoundBase || s.DiffEmpty {
		// Nothing more to render — the orchestrator surfaces the
		// "no base" / "no changes" message and stops.
		return strings.TrimRight(b.String(), "\n") + "\n"
	}

	b.WriteString("\n## changed-files\n")
	if strings.TrimSpace(s.ChangedFiles) == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(s.ChangedFiles)
		if !strings.HasSuffix(s.ChangedFiles, "\n") {
			b.WriteString("\n")
		}
		if s.ChangedCapped {
			fmt.Fprintf(&b, "[truncated at %d bytes]\n", codeReviewChangedFilesCap)
		}
	}

	b.WriteString("\n## diff\n")
	b.WriteString(s.Diff)
	if !strings.HasSuffix(s.Diff, "\n") {
		b.WriteString("\n")
	}
	if s.DiffCapped {
		fmt.Fprintf(&b, "[truncated at %d bytes — narrow the review or raise effort]\n", s.DiffCap)
	}

	b.WriteString("\n## commit-log\n")
	if s.DiffSource == "working-tree" {
		b.WriteString("(working-tree changes — not yet committed)\n")
	} else if len(s.CommitLog) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, line := range s.CommitLog {
			fmt.Fprintf(&b, "%s\n", line)
		}
	}

	fmt.Fprintf(&b, "\n## style-context\ndetected_commit_style=%s\n", s.DetectedStyle)

	return strings.TrimRight(b.String(), "\n") + "\n"
}
