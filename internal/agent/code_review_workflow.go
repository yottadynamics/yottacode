package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
		"headers (## summary, ## state, ## changed-files, ## diff, ## commit-log, " +
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
	EmptyRepo      bool   // repo has no commits yet (unborn HEAD) — a STOP flag, like NotFoundBase
	DiffSource     string // "branch-vs-base" | "working-tree"
	AheadCount     int
	AheadCountErr  bool // rev-list --count errored; AheadCount unreliable (0 may not mean "not ahead")
	FilesChanged   int
	Insertions     int
	Deletions      int
	DiffLines      int
	DiffEmpty      bool
	DiffErr        bool // a `git diff` call failed — distinct from a genuinely empty diff (must NOT read as "no changes")

	// MergeBase is the merge-base SHA of ResolvedBase and HEAD for the
	// branch-vs-base source (empty for working-tree, or when the two
	// histories share no common ancestor — see NoMergeBase). The diff is
	// built as MergeBase..HEAD (two-dot), identical to the three-dot
	// ResolvedBase...HEAD "Files changed" view, but resolving the
	// merge-base ourselves lets us (a) hand finders the exact base SHA so
	// they review the same range, and (b) detect the no-merge-base case
	// instead of letting three-dot's `fatal: no merge base` masquerade as
	// an empty diff.
	MergeBase   string
	NoMergeBase bool // ResolvedBase and HEAD share no common ancestor (orphan/grafted/unrelated history)
	// DiffBase is the left side of the two-dot range the snapshot was built
	// from — the ref finders must diff against (git_diff_files base=<DiffBase>,
	// head=HEAD) so their change set matches this snapshot exactly. MergeBase
	// for a normal branch, ResolvedBase when there is no merge-base, "HEAD"
	// for the working-tree source.
	DiffBase string

	ChangedFiles  string // name-status, capped
	ChangedCapped bool
	Diff          string // unified diff, capped to effortDiffCap(effort)
	DiffCap       int
	DiffCapped    bool
	// UntrackedFiles are new (untracked, non-ignored) files folded into the
	// working-tree review — `git diff HEAD` never shows them, so without this
	// a brand-new module would be invisible (and an untracked-only tree would
	// look empty). Empty for the branch-vs-base source.
	UntrackedFiles []string

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

	// A brand-new repo with no commits has an "unborn" HEAD, on which
	// `git rev-parse --abbrev-ref HEAD` below exits 128 with a raw git
	// fatal. Detect it first and surface a typed empty-repo STOP flag
	// instead of failing the whole call with raw git text. We only treat
	// it as empty-repo inside a genuine git work tree, so a non-repo cwd
	// still falls through to the hard error from the branch lookup
	// (preserving the "not a repo" failure path).
	if _, err := gitOutput(ctx, cwd, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		if _, repoErr := gitOutput(ctx, cwd, "rev-parse", "--is-inside-work-tree"); repoErr == nil {
			snap.EmptyRepo = true
			if b, err := gitOutput(ctx, cwd, "branch", "--show-current"); err == nil {
				snap.CurrentBranch = strings.TrimSpace(b)
			}
			return snap, nil
		}
	}

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
	// base, review that range. Otherwise fall back to the working tree
	// so "review my pending work before I commit" also works. A rev-list
	// error is recorded (AheadCountErr) rather than swallowed — otherwise
	// a failed count reads as "0 ahead" and silently mislabels the source
	// as working-tree.
	if cnt, err := gitOutput(ctx, cwd, "rev-list", "--count", snap.ResolvedBase+"..HEAD"); err == nil {
		snap.AheadCount, _ = strconv.Atoi(strings.TrimSpace(cnt))
	} else {
		snap.AheadCountErr = true
	}

	var changedRange, diffRange string
	if snap.AheadCount > 0 {
		snap.DiffSource = "branch-vs-base"
		// Build the diff from the merge-base (two-dot), NOT three-dot.
		// `git diff base...HEAD` is FATAL when base and HEAD share no
		// common ancestor (orphan/grafted/force-pushed/shallow history),
		// and that fatal was best-effort-swallowed below, masquerading as
		// an empty diff ("nothing to review"). Resolving the merge-base
		// ourselves lets us diff the equivalent mergeBase..HEAD, hand
		// finders that exact SHA so they review the same change set, and
		// fall back cleanly to base..HEAD (which needs no common ancestor)
		// when there is no merge-base at all.
		if mb, err := gitOutput(ctx, cwd, "merge-base", snap.ResolvedBase, "HEAD"); err == nil {
			snap.MergeBase = strings.TrimSpace(mb)
			snap.DiffBase = snap.MergeBase
		} else {
			snap.NoMergeBase = true
			snap.DiffBase = snap.ResolvedBase
		}
		changedRange = snap.DiffBase + "..HEAD"
		diffRange = snap.DiffBase + "..HEAD"
	} else {
		snap.DiffSource = "working-tree"
		snap.DiffBase = "HEAD"
		changedRange = "HEAD"
		diffRange = "HEAD"
	}

	// Changed files (name-status) and the diff body. A git error here is
	// recorded in DiffErr — NOT swallowed into a false-empty — so a range
	// git refuses to diff is never reported to the orchestrator as
	// "no changes to review".
	if names, err := gitOutput(ctx, cwd, "diff", "--name-status", changedRange); err == nil {
		snap.ChangedFiles, snap.ChangedCapped = capString(strings.TrimRight(names, "\n"), codeReviewChangedFilesCap)
	} else {
		snap.DiffErr = true
	}
	if diff, err := gitOutput(ctx, cwd, "diff", diffRange); err == nil {
		snap.Diff, snap.DiffCapped = capString(diff, snap.DiffCap)
	} else {
		snap.DiffErr = true
	}

	// Working-tree review must include brand-new (untracked, non-ignored)
	// files: `git diff HEAD` only shows TRACKED changes, so a new module
	// not yet `git add`ed would be invisible and an untracked-only tree
	// would look empty. Fold each untracked file into ## changed-files (as
	// an added entry) and its content into the diff (as a new-file hunk).
	// Read-only: foldUntracked synthesizes the per-file diff via
	// `git diff --no-index` and never touches the index (no `git add -N`).
	if snap.DiffSource == "working-tree" {
		if others, err := gitOutput(ctx, cwd, "ls-files", "--others", "--exclude-standard"); err == nil {
			snap.UntrackedFiles = splitNonEmptyLines(strings.TrimRight(others, "\n"))
		}
		snap.foldUntracked(ctx, cwd)
	}

	// diff_empty means "genuinely nothing to review" — NOT a diff that
	// errored (DiffErr) and NOT a tree whose only changes are untracked
	// new files (UntrackedFiles). Either of those would otherwise STOP the
	// review with a false "no changes".
	snap.DiffEmpty = !snap.DiffErr && strings.TrimSpace(snap.Diff) == "" && len(snap.UntrackedFiles) == 0

	// Commit log + style detection only make sense for the
	// branch-vs-base source; working-tree changes aren't committed yet.
	// Use DiffBase..HEAD so the log lists exactly the branch's own commits
	// (the same range the diff was built from).
	if snap.DiffSource == "branch-vs-base" {
		if logOut, err := gitOutput(ctx, cwd, "log",
			fmt.Sprintf("-%d", codeReviewLogCommits),
			"--format=%h %s", snap.DiffBase+"..HEAD"); err == nil {
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
	snap.computeSummaryStats()

	return snap, nil
}

// computeSummaryStats fills the cheap digest fields used by the TUI card. It
// derives counts from already-collected diff/name-status text so the review tool
// does not pay for another git command just to make scrollback readable.
func (snap *CodeReviewContext) computeSummaryStats() {
	for _, line := range splitNonEmptyLines(snap.ChangedFiles) {
		if strings.HasPrefix(line, "[truncated") {
			continue
		}
		snap.FilesChanged++
	}
	for _, line := range strings.Split(snap.Diff, "\n") {
		if strings.TrimSpace(line) != "" {
			snap.DiffLines++
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			snap.Insertions++
		}
		if strings.HasPrefix(line, "-") {
			snap.Deletions++
		}
	}
}

func (s CodeReviewContext) summaryLine() string {
	left := s.CurrentBranch
	if left == "" {
		left = "(unknown)"
	}
	right := s.ResolvedBase
	if right == "" {
		right = "(unresolved)"
	}
	diffKind := "working-tree diff"
	if s.DiffSource == "branch-vs-base" {
		diffKind = "branch diff"
	}
	return fmt.Sprintf("code_review_context(effort=%s) · %s → %s · %s · %d files changed (+%d/−%d) · %d lines",
		s.Effort, left, right, diffKind, s.FilesChanged, s.Insertions, s.Deletions, s.DiffLines)
}

// foldUntracked appends each untracked file to the changed-files list (as
// an added entry) and its content to the diff body (as a synthesized
// new-file hunk), bounded by the same caps. Read-only: the per-file diff
// comes from `git diff --no-index`, which never touches the repo index.
// Best-effort per file — a file that can't be diffed (binary, unreadable)
// is still listed in changed-files but contributes no hunk. Called only on
// the working-tree source (UntrackedFiles is empty otherwise).
func (snap *CodeReviewContext) foldUntracked(ctx context.Context, cwd string) {
	if len(snap.UntrackedFiles) == 0 {
		return
	}
	// changed-files: append an "A\t<path>" entry per untracked file.
	var cf strings.Builder
	cf.WriteString(strings.TrimRight(snap.ChangedFiles, "\n"))
	for _, f := range snap.UntrackedFiles {
		if cf.Len() > 0 {
			cf.WriteString("\n")
		}
		fmt.Fprintf(&cf, "A\t%s", f)
	}
	snap.ChangedFiles, snap.ChangedCapped = capString(cf.String(), codeReviewChangedFilesCap)

	// diff body: append a synthesized new-file hunk per untracked file,
	// stopping once we'd exceed the effort diff cap.
	var db strings.Builder
	db.WriteString(snap.Diff)
	truncated := snap.DiffCapped
	for _, f := range snap.UntrackedFiles {
		if db.Len() >= snap.DiffCap {
			truncated = true
			break
		}
		hunk, err := gitDiffNoIndex(ctx, cwd, f)
		if err != nil || strings.TrimSpace(hunk) == "" {
			continue
		}
		if db.Len() > 0 && !strings.HasSuffix(db.String(), "\n") {
			db.WriteString("\n")
		}
		db.WriteString(hunk)
	}
	var capped bool
	snap.Diff, capped = capString(db.String(), snap.DiffCap)
	snap.DiffCapped = truncated || capped
}

// gitDiffNoIndex synthesizes the added-file unified diff for a path that
// is not tracked by git (an untracked file), via `git diff --no-index
// /dev/null <path>`. That command exits 1 when the two inputs differ —
// always the case here — so exit 1 is treated as success and only a higher
// exit code is a real error. Read-only: --no-index never touches the repo
// or its index. The /dev/null left side is POSIX-only, which matches
// yottacode's supported platforms (linux + darwin).
func gitDiffNoIndex(ctx context.Context, cwd, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--", os.DevNull, path)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return string(out), nil
		}
		return "", err
	}
	return string(out), nil
}

// renderCodeReviewContext flattens the snapshot into the labeled
// sections the tool returns. ## state goes first so the orchestrator
// branches on the typed flags before reading anything else, and the
// render short-circuits after ## state on the STOP conditions
// (mirrors renderPRReviewContext).
func renderCodeReviewContext(s CodeReviewContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## summary\n%s\n\n", s.summaryLine())
	fmt.Fprintf(&b, "## state\neffort=%s\ncurrent_branch=%s\nresolved_base=%s\nbase_resolution=%s\nnot_found_base=%v\nempty_repo=%v\ndiff_empty=%v\ndiff_err=%v\ndiff_source=%s\nahead_count=%d\nahead_count_err=%v\nmerge_base=%s\ndiff_base=%s\nno_merge_base=%v\ndiff_cap_bytes=%d\nchanged_capped=%v\ndiff_capped=%v\n",
		s.Effort, s.CurrentBranch, s.ResolvedBase, s.BaseResolution,
		s.NotFoundBase, s.EmptyRepo, s.DiffEmpty, s.DiffErr, s.DiffSource,
		s.AheadCount, s.AheadCountErr, s.MergeBase, s.DiffBase, s.NoMergeBase, s.DiffCap,
		s.ChangedCapped, s.DiffCapped)

	// STOP conditions — render only ## state and let the orchestrator
	// surface the message. diff_err with an empty diff is a STOP too: a
	// git range failure must not fall through to an empty review that
	// reads as "looks good".
	if s.NotFoundBase || s.EmptyRepo || s.DiffEmpty || (s.DiffErr && strings.TrimSpace(s.Diff) == "") {
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
