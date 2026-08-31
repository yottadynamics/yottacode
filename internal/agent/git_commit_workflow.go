package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Composite git-workflow tools. Where git_workflow_tools.go has
// single-step primitives (status, diff, stage, commit, log...), these
// bundle the multi-step shapes that today's markdown defaults
// (/git:commit-message, /git:create-pr) compose by prose — turning
// "gather context with a bash heredoc, then guess style, then commit
// and hope the model didn't skip a step" into "one atomic tool call
// with a typed envelope."
//
// The split between *_context (read-only, parallel-safe) and *_apply
// (mutator, approval-gated) keeps the model's job narrow: structured
// in, validated out, no in-tool bash parsing.

const (
	// commitContextDiffCap bounds the staged diff we splice into the
	// returned snapshot. The model already gets enough signal from
	// name-status + a truncated body; uncapped diffs (think generated
	// JSON dumps) would otherwise blow the prompt cache and pollute
	// context. 32 KiB is generous for hand-edited commits and small
	// enough that a typical synthesis call still fits in one cache hit.
	commitContextDiffCap = 32 * 1024
	// commitContextProseCap is smaller because prose deltas
	// (CHANGELOG/README/docs) are signal-dense for naming a commit —
	// every diff hunk matters. 8 KiB captures multi-paragraph diffs
	// without crowding out the source diff.
	commitContextProseCap = 8 * 1024
	// commitContextRecentSubjects caps the style-detection window.
	// Matches the magic number the legacy markdown directive used
	// (`git log -15 --format=%s`) so the inferred style doesn't shift
	// between procedural /commit and the legacy fallback during the
	// one-release coexistence window.
	commitContextRecentSubjects = 15
	// commitContextBranchCommits caps the local-only commit list (the
	// "your own commits on this branch" signal). Bigger than the style
	// window because branch-local subjects are often the strongest
	// hint for what a new commit is *about*.
	commitContextBranchCommits = 10
)

// CommitSubjectMaxLen is the hard cap on a commit subject line. 72 is
// the de-facto standard (git's own --column wraps stay sane below it,
// most CI lint setups enforce ≤72). Exported because /commit's
// prompt references the same number, so any future bump moves both
// sides at once.
const CommitSubjectMaxLen = 72

var (
	// conventionalCommitSubject — `type(scope)?: subject`. The legacy
	// directive enumerated specific types in prose; we match the
	// shape generically so unusual but valid types (e.g. `revert:`,
	// `wip:`) still register as conventional rather than falling
	// through to plain.
	conventionalCommitSubject = regexp.MustCompile(`^[a-z]+(\([^)]+\))?!?: `)
	// ticketPrefixSubject — `ABC-123 subject` (no colon). Common in
	// Jira/Linear shops where commits are gated on a ticket ID.
	ticketPrefixSubject = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+ `)
)

// GitCommitContextTool gathers the read-only snapshot a caller needs
// to draft a commit message: what's staged, what style the repo
// uses, what's untracked. Replaces the bash heredoc the legacy
// /git:commit-message directive ran as its first tool call.
type GitCommitContextTool struct{ Cwd *CwdRef }

func (t *GitCommitContextTool) Name() string { return "git_commit_context" }

func (t *GitCommitContextTool) Description() string {
	return "Gather the read-only context needed to compose a one-line commit " +
		"message: staged file list, staged diff (capped), branch name, " +
		"branch-local commits, recent subjects for style detection, " +
		"prose-file changes (CHANGELOG/README/docs), and unstaged/untracked " +
		"lists. Returns a structured snapshot keyed by section headers. " +
		"Pair with git_commit_apply to land the commit."
}

func (t *GitCommitContextTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *GitCommitContextTool) RequiresApproval(string) bool { return false }
func (t *GitCommitContextTool) ParallelSafe(string) bool     { return true }
func (t *GitCommitContextTool) PreviewCall(string) string    { return "git_commit_context()" }

func (t *GitCommitContextTool) Execute(ctx context.Context, _ string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git_commit_context: git binary not found in PATH")
	}
	snap, err := BuildCommitContext(ctx, t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("git_commit_context: %w", err)
	}
	return renderCommitContext(snap), nil
}

// CommitContext is the typed snapshot BuildCommitContext returns.
// Exported so the procedural /commit slash command can consume it
// directly (without a tool-call round trip) and the test suite can
// assert on each field rather than parsing the rendered string.
type CommitContext struct {
	Branch         string
	StagedEmpty    bool
	StagedNameStat string
	StagedDiff     string
	StagedDiffCap  bool
	RecentSubjects []string
	BranchCommits  []string
	ProseDiff      string
	ProseDiffCap   bool
	Unstaged       []string
	Untracked      []string
	DetectedStyle  string // "conventional" | "ticket-prefix" | "plain"
}

// BuildCommitContext is the deterministic core of git_commit_context.
// Returns a typed snapshot; the tool wrapper renders it to text for
// model consumption, and /commit calls it directly to drive a narrow
// synthesis prompt without a tool round trip.
func BuildCommitContext(ctx context.Context, cwd string) (CommitContext, error) {
	var snap CommitContext

	nameStat, err := gitOutput(ctx, cwd, "diff", "--cached", "--name-status")
	if err != nil {
		return snap, err
	}
	snap.StagedNameStat = strings.TrimRight(nameStat, "\n")
	snap.StagedEmpty = strings.TrimSpace(snap.StagedNameStat) == ""

	rawDiff, err := gitOutput(ctx, cwd, "diff", "--cached")
	if err != nil {
		return snap, err
	}
	snap.StagedDiff, snap.StagedDiffCap = capString(rawDiff, commitContextDiffCap)

	branch, _ := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	snap.Branch = strings.TrimSpace(branch)

	recent, _ := gitOutput(ctx, cwd, "log",
		fmt.Sprintf("-%d", commitContextRecentSubjects),
		"--format=%s")
	snap.RecentSubjects = splitNonEmptyLines(recent)
	snap.DetectedStyle = detectCommitStyle(snap.RecentSubjects)

	// branch-local commits = HEAD's history minus what's on any remote.
	// Falls back silently when no remotes are configured (fresh repos,
	// CI mirrors) — `git log --not --remotes=origin` errors only when
	// it can't parse the rev spec, which the trim catches.
	bc, _ := gitOutput(ctx, cwd, "log",
		fmt.Sprintf("-%d", commitContextBranchCommits),
		"--no-merges", "--not", "--remotes=origin", "--format=%s")
	snap.BranchCommits = splitNonEmptyLines(bc)

	prose, _ := gitOutput(ctx, cwd, "diff", "--cached", "--",
		"CHANGELOG.md", "README.md", "docs/*.md", "docs/**/*.md")
	snap.ProseDiff, snap.ProseDiffCap = capString(prose, commitContextProseCap)

	unstaged, _ := gitOutput(ctx, cwd, "diff", "--name-only")
	snap.Unstaged = splitNonEmptyLines(unstaged)
	untracked, _ := boundedUntrackedFiles(ctx, cwd)
	snap.Untracked = splitNonEmptyLines(untracked)

	return snap, nil
}

// detectCommitStyle picks the dominant pattern from a recent-subjects
// window. Majority rule: a style wins only when more than half the
// inspected subjects match. Below that threshold the repo is "plain"
// (imperative, no prefix) — which is also the safe fall-through when
// the log is empty (fresh repo, shallow clone).
//
// Why majority rather than "any match": commit logs are mixed. One
// `fix: typo` next to fourteen plain subjects shouldn't flip the
// whole repo into conventional-commits mode.
func detectCommitStyle(subjects []string) string {
	if len(subjects) == 0 {
		return "plain"
	}
	conv, ticket := 0, 0
	for _, s := range subjects {
		switch {
		case conventionalCommitSubject.MatchString(s):
			conv++
		case ticketPrefixSubject.MatchString(s):
			ticket++
		}
	}
	n := len(subjects)
	switch {
	case conv*2 > n:
		return "conventional"
	case ticket*2 > n:
		return "ticket-prefix"
	default:
		return "plain"
	}
}

// renderCommitContext flattens a CommitContext into the labeled-section
// shape git_commit_context emits as its tool result. Headers are
// stable so the model can pattern-match without ambiguity; section
// order matches the priority order the legacy directive used
// (PROSE → BRANCH-COMMITS → BRANCH → file list) so style-detection
// follow-on calls stay consistent across procedural and legacy paths.
func renderCommitContext(s CommitContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## state\nstaged_empty=%v\ndetected_style=%s\nbranch=%s\n",
		s.StagedEmpty, s.DetectedStyle, s.Branch)
	b.WriteString("\n## staged.name-status\n")
	if s.StagedNameStat == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(s.StagedNameStat)
		b.WriteString("\n")
	}
	b.WriteString("\n## staged.diff\n")
	if s.StagedDiff == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(s.StagedDiff)
		if !strings.HasSuffix(s.StagedDiff, "\n") {
			b.WriteString("\n")
		}
		if s.StagedDiffCap {
			fmt.Fprintf(&b, "[truncated at %d bytes]\n", commitContextDiffCap)
		}
	}
	b.WriteString("\n## recent.subjects\n")
	if len(s.RecentSubjects) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, line := range s.RecentSubjects {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n## branch.commits\n")
	if len(s.BranchCommits) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, line := range s.BranchCommits {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n## prose\n")
	if s.ProseDiff == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(s.ProseDiff)
		if !strings.HasSuffix(s.ProseDiff, "\n") {
			b.WriteString("\n")
		}
		if s.ProseDiffCap {
			fmt.Fprintf(&b, "[truncated at %d bytes]\n", commitContextProseCap)
		}
	}
	b.WriteString("\n## unstaged\n")
	if len(s.Unstaged) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, line := range s.Unstaged {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n## untracked\n")
	if len(s.Untracked) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, line := range s.Untracked {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// GitCommitApplyTool lands a one-line commit message produced by the
// model (or a procedural caller). The validation contract is the
// hard guarantee — empty staging, oversize subject, trailing period,
// and trailing newlines that would extend into a body all fail
// deterministically *before* invoking git. That's the reliability
// fix the legacy markdown directive could only ask for in prose.
//
// On hook failure the result envelope reports hook_error verbatim
// without auto-retry or auto-amend — the legacy directive's "hard
// prohibitions" become an unreachable code path rather than a model
// discipline ask.
type GitCommitApplyTool struct{ Cwd *CwdRef }

func (t *GitCommitApplyTool) Name() string { return "git_commit_apply" }

func (t *GitCommitApplyTool) Description() string {
	return "Validate a one-line commit subject and run `git commit` against " +
		"the currently staged changes. Empty staging, subjects longer than " +
		fmt.Sprintf("%d", CommitSubjectMaxLen) + " characters, subjects with a " +
		"trailing period, and multi-line messages are rejected deterministically " +
		"before invoking git. Returns a structured result with sha, hook_error " +
		"(if a pre-commit hook fails), and the post-commit unstaged/untracked " +
		"lists. The approval modal fires once with the message + git invocation."
}

func (t *GitCommitApplyTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type": "string",
				"description": fmt.Sprintf(
					"One-line subject (≤%d chars, imperative mood, no trailing period).",
					CommitSubjectMaxLen),
			},
		},
		"required": []string{"message"},
	}
}

func (t *GitCommitApplyTool) RequiresApproval(string) bool { return true }

func (t *GitCommitApplyTool) PreviewCall(argsJSON string) string {
	var a struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_commit_apply(%q)", a.Message)
}

// CommitResult is the typed envelope ApplyCommit returns. Rendering
// it to text is the tool's job; the procedural /commit slash command
// reads the struct directly for branching ("did the commit land?
// surface unstaged/untracked footer" vs "denied? quote it; bail").
type CommitResult struct {
	Committed     bool
	SHA           string
	StagedEmpty   bool
	ValidationErr string
	HookError     string
	Unstaged      []string
	Untracked     []string
}

func (t *GitCommitApplyTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_commit_apply: invalid args: %w", err)
	}
	res, err := ApplyCommit(ctx, t.Cwd.Get(), a.Message)
	if err != nil {
		return "", fmt.Errorf("git_commit_apply: %w", err)
	}
	return renderCommitResult(res), nil
}

// ApplyCommit is the deterministic core of git_commit_apply.
// Returns a typed CommitResult; the tool wrapper renders it for model
// consumption, and /commit's procedural path reads the struct
// directly. Errors return only on infrastructure failures (git binary
// missing, ctx canceled, fork failure); validation failures and hook
// rejections populate the result envelope so callers can branch
// without a stringy err = "..." check.
func ApplyCommit(ctx context.Context, cwd, message string) (CommitResult, error) {
	var res CommitResult

	if _, err := exec.LookPath("git"); err != nil {
		return res, errors.New("git binary not found in PATH")
	}

	if v := validateCommitSubject(message); v != "" {
		res.ValidationErr = v
		return res, nil
	}

	stagedStat, err := gitOutput(ctx, cwd, "diff", "--cached", "--name-status")
	if err != nil {
		return res, fmt.Errorf("staged-check: %w", err)
	}
	if strings.TrimSpace(stagedStat) == "" {
		res.StagedEmpty = true
		return res, nil
	}

	// git commit -F - reads the message from stdin so quotes, dollar
	// signs, backticks pass through unmangled — matches the legacy
	// directive's heredoc shape without invoking a shell.
	cmd := exec.CommandContext(ctx, "git", "commit", "-F", "-")
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(message)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		// Distinguish hook/lint rejections (expected, surfaced via
		// hook_error) from infrastructure failures (binary missing,
		// repo corruption — bubbled up as err). git's hook failures
		// come back as ExitError with the hook's stdout/stderr in `out`.
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.HookError = strings.TrimSpace(string(out))
			res.Unstaged = collectFilesLines(ctx, cwd, "diff", "--name-only")
			res.Untracked = splitNonEmptyLines(mustBoundedUntrackedFiles(ctx, cwd))
			return res, nil
		}
		return res, fmt.Errorf("commit: %w", runErr)
	}

	res.Committed = true
	sha, _ := gitOutput(ctx, cwd, "rev-parse", "HEAD")
	res.SHA = strings.TrimSpace(sha)
	res.Unstaged = collectFilesLines(ctx, cwd, "diff", "--name-only")
	res.Untracked = splitNonEmptyLines(mustBoundedUntrackedFiles(ctx, cwd))
	return res, nil
}

// validateCommitSubject enforces the hard guarantees the legacy
// directive could only ask the model to honor. Returns the empty
// string when the subject is acceptable; otherwise returns a short
// human-readable reason that callers surface as ValidationErr.
//
// We accept exactly one trailing newline (writers that auto-append
// "\n" to a one-liner shouldn't trip the multi-line check) but
// nothing else.
func validateCommitSubject(message string) string {
	msg := strings.TrimRight(message, "\n")
	if strings.TrimSpace(msg) == "" {
		return "message is empty"
	}
	if strings.Contains(msg, "\n") {
		return "message must be a single line (no body / footer)"
	}
	if len(msg) > CommitSubjectMaxLen {
		return fmt.Sprintf("subject is %d chars; max is %d", len(msg), CommitSubjectMaxLen)
	}
	if strings.HasSuffix(msg, ".") {
		return "subject must not end with a period"
	}
	return ""
}

// renderCommitResult shapes the result envelope into the tool's text
// payload. Field names match the CommitResult struct so a model
// parsing the output can map back without ambiguity ("committed=true
// sha=abc...") rather than picking apart prose.
func renderCommitResult(r CommitResult) string {
	var b strings.Builder
	switch {
	case r.ValidationErr != "":
		fmt.Fprintf(&b, "committed=false reason=validation\nerror=%s\n", r.ValidationErr)
	case r.StagedEmpty:
		b.WriteString("committed=false reason=staged_empty\n")
		b.WriteString("Hint: stage changes with git_stage_files before retrying.\n")
	case r.HookError != "":
		b.WriteString("committed=false reason=hook_error\n")
		b.WriteString("--- hook output ---\n")
		b.WriteString(r.HookError)
		if !strings.HasSuffix(r.HookError, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- end hook output ---\n")
		b.WriteString("Do NOT auto-retry, auto-stage, or auto-amend. Surface the error and stop.\n")
	case r.Committed:
		fmt.Fprintf(&b, "committed=true sha=%s\n", r.SHA)
	default:
		b.WriteString("committed=false reason=unknown\n")
	}
	if len(r.Unstaged) > 0 {
		b.WriteString("\n## unstaged\n")
		for _, p := range r.Unstaged {
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	if len(r.Untracked) > 0 {
		b.WriteString("\n## untracked\n")
		for _, p := range r.Untracked {
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// capString truncates s at limit bytes, returning the (possibly
// shorter) prefix and a flag indicating whether truncation occurred.
// We trim to the last newline below the limit so callers don't render
// a half-line followed by "[truncated]" — diff readers handle clean
// line boundaries far better than mid-hunk cuts.
func capString(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := s[:limit]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i+1]
	}
	return cut, true
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// collectFilesLines is a thin shim that swallows git errors (an empty
// repo, a freshly-cloned mirror with no untracked entries) and just
// returns whatever lines came back. The post-commit footer is best-
// effort cosmetics, not load-bearing — surface what we can without
// failing the commit result on a `git ls-files --others` hiccup.
func collectFilesLines(ctx context.Context, cwd string, args ...string) []string {
	out, _ := gitOutput(ctx, cwd, args...)
	return splitNonEmptyLines(out)
}

func mustBoundedUntrackedFiles(ctx context.Context, cwd string) string {
	out, _ := boundedUntrackedFiles(ctx, cwd)
	return out
}

func boundedUntrackedFiles(ctx context.Context, cwd string) (string, error) {
	kept, summary, err := collectBoundedUntrackedFiles(ctx, cwd)
	if err != nil {
		return "", err
	}
	if summary.count > 0 {
		kept = append(kept, summary.line())
	}
	if len(kept) == 0 {
		return "", nil
	}
	return strings.Join(kept, "\n") + "\n", nil
}

func collectBoundedUntrackedFiles(ctx context.Context, cwd string) ([]string, generatedArtifactSummary, error) {
	raw, err := gitOutput(ctx, cwd, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, generatedArtifactSummary{}, err
	}
	ignored, _ := gitOutput(ctx, cwd, "ls-files", "--others", "--ignored", "--exclude-standard")
	var kept []string
	var summary generatedArtifactSummary
	for _, line := range splitNonEmptyLines(raw) {
		if summary.add(line) {
			continue
		}
		kept = append(kept, line)
	}
	for _, line := range splitNonEmptyLines(ignored) {
		// Generated artifact directories are normally ignored after the repo-level
		// guardrail lands, but PR/readiness tools still need to know cleanup is
		// required instead of silently treating the working tree as PR-ready.
		summary.add(line)
	}
	return kept, summary, nil
}

type generatedArtifactSummary struct {
	count int
	roots map[string]bool
}

func (s *generatedArtifactSummary) add(path string) bool {
	root, ok := generatedArtifactRoot(path)
	if !ok {
		return false
	}
	if s.roots == nil {
		s.roots = map[string]bool{}
	}
	s.count++
	s.roots[root] = true
	return true
}

func (s generatedArtifactSummary) line() string {
	return fmt.Sprintf("omitted %d generated local artifact file(s) under %s", s.count, strings.Join(generatedArtifactRoots(s.roots), ", "))
}

func generatedArtifactRoot(path string) (string, bool) {
	slash := filepath.ToSlash(strings.TrimSpace(path))
	for _, root := range []string{".cache/", ".config/", "go/", ".local/", ".yottacode/tmp/go/", ".scratch/"} {
		if strings.HasPrefix(slash, root) {
			return strings.TrimSuffix(root, "/") + "/", true
		}
	}
	return "", false
}

func generatedArtifactRoots(seen map[string]bool) []string {
	ordered := []string{".cache/", ".config/", "go/", ".local/", ".yottacode/tmp/go/", ".scratch/"}
	var out []string
	for _, root := range ordered {
		if seen[root] {
			out = append(out, root)
		}
	}
	return out
}
