package agent

import (
	"context"
	"strings"
	"testing"
)

func TestEffortDiffCap(t *testing.T) {
	cases := []struct {
		effort string
		want   int
	}{
		{"low", codeReviewDiffCapLow},
		{"medium", codeReviewDiffCapMedium},
		{"high", codeReviewDiffCapHigh},
		{"", codeReviewDiffCapMedium},
		{"bogus", codeReviewDiffCapMedium},
	}
	for _, tc := range cases {
		if got := effortDiffCap(tc.effort); got != tc.want {
			t.Errorf("effortDiffCap(%q) = %d, want %d", tc.effort, got, tc.want)
		}
	}
}

func TestNormalizeEffort(t *testing.T) {
	cases := map[string]string{
		"":       "medium",
		"low":    "low",
		"LOW":    "low",
		" high ": "high",
		"medium": "medium",
		"bogus":  "medium",
	}
	for in, want := range cases {
		if got := normalizeEffort(in); got != want {
			t.Errorf("normalizeEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCodeReviewContext_UnresolvedBase: a repo with no main/master/
// develop and no origin/HEAD resolves no base, so the snapshot flags
// not_found_base and the render short-circuits after ## state.
func TestCodeReviewContext_UnresolvedBase(t *testing.T) {
	tmp := gitInit(t) // inits on -b main …
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	// Rename away from any base-candidate name so resolveBaseBranch
	// finds nothing (no origin remote in a bare local init).
	gitRun(t, tmp, "branch", "-m", "feature/x")

	snap, err := BuildCodeReviewContext(context.Background(), tmp, "medium")
	if err != nil {
		t.Fatalf("BuildCodeReviewContext: %v", err)
	}
	if !snap.NotFoundBase {
		t.Errorf("expected NotFoundBase=true, got base=%q resolution=%q", snap.ResolvedBase, snap.BaseResolution)
	}
	out := renderCodeReviewContext(snap)
	if !strings.Contains(out, "not_found_base=true") {
		t.Errorf("render must flag not_found_base=true; got:\n%s", out)
	}
	if strings.Contains(out, "## diff") {
		t.Errorf("render must short-circuit after ## state on no-base; got:\n%s", out)
	}
}

// TestCodeReviewContext_EmptyDiff: on the base branch with a clean
// tree there is nothing to review, so diff_empty is flagged and the
// render short-circuits.
func TestCodeReviewContext_EmptyDiff(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base") // on main, clean tree, no commits ahead

	snap, err := BuildCodeReviewContext(context.Background(), tmp, "medium")
	if err != nil {
		t.Fatalf("BuildCodeReviewContext: %v", err)
	}
	if snap.NotFoundBase {
		t.Fatalf("base should resolve to main; got resolution=%q", snap.BaseResolution)
	}
	if !snap.DiffEmpty {
		t.Errorf("expected DiffEmpty=true on a clean tree at base; got diff=%q", snap.Diff)
	}
	out := renderCodeReviewContext(snap)
	if !strings.Contains(out, "diff_empty=true") {
		t.Errorf("render must flag diff_empty=true; got:\n%s", out)
	}
}

// TestCodeReviewContext_BranchVsBase: a feature branch one commit
// ahead of main reviews the branch range — changed files listed,
// non-empty diff, commit log populated.
func TestCodeReviewContext_BranchVsBase(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	gitRun(t, tmp, "checkout", "-q", "-b", "feature/x")
	writeFile(t, tmp, "f.txt", "v1\nv2\n")
	writeFile(t, tmp, "g.txt", "new\n")
	gitCommit(t, tmp, "feat: add g and extend f")

	snap, err := BuildCodeReviewContext(context.Background(), tmp, "medium")
	if err != nil {
		t.Fatalf("BuildCodeReviewContext: %v", err)
	}
	if snap.DiffSource != "branch-vs-base" {
		t.Errorf("DiffSource = %q, want branch-vs-base", snap.DiffSource)
	}
	if snap.AheadCount != 1 {
		t.Errorf("AheadCount = %d, want 1", snap.AheadCount)
	}
	if snap.ResolvedBase != "main" {
		t.Errorf("ResolvedBase = %q, want main", snap.ResolvedBase)
	}
	out := renderCodeReviewContext(snap)
	for _, want := range []string{"## changed-files", "g.txt", "## diff", "v2", "## commit-log", "feat: add g"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got:\n%s", want, out)
		}
	}
}

// TestCodeReviewContext_WorkingTreeFallback: on the base branch with
// an uncommitted edit (no commits ahead), the tool reviews the
// working tree and notes the changes aren't committed.
func TestCodeReviewContext_WorkingTreeFallback(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v1\nuncommitted\n") // dirty, not committed

	snap, err := BuildCodeReviewContext(context.Background(), tmp, "medium")
	if err != nil {
		t.Fatalf("BuildCodeReviewContext: %v", err)
	}
	if snap.DiffSource != "working-tree" {
		t.Errorf("DiffSource = %q, want working-tree", snap.DiffSource)
	}
	if snap.DiffEmpty {
		t.Errorf("expected a non-empty working-tree diff")
	}
	out := renderCodeReviewContext(snap)
	if !strings.Contains(out, "uncommitted") {
		t.Errorf("render must show the uncommitted edit in the diff; got:\n%s", out)
	}
	if !strings.Contains(out, "not yet committed") {
		t.Errorf("render must note the working-tree source under ## commit-log; got:\n%s", out)
	}
}

// TestCodeReviewContext_DiffTruncation: a diff larger than the (low)
// cap is truncated with a marker.
func TestCodeReviewContext_DiffTruncation(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "seed\n")
	gitCommit(t, tmp, "base")
	gitRun(t, tmp, "checkout", "-q", "-b", "feature/big")
	// Build a payload comfortably larger than the low cap (32 KiB).
	var sb strings.Builder
	for range 4000 {
		sb.WriteString("this is a line of added content to blow the diff cap\n")
	}
	writeFile(t, tmp, "big.txt", sb.String())
	gitCommit(t, tmp, "add big file")

	snap, err := BuildCodeReviewContext(context.Background(), tmp, "low")
	if err != nil {
		t.Fatalf("BuildCodeReviewContext: %v", err)
	}
	if !snap.DiffCapped {
		t.Errorf("expected DiffCapped=true for a >32KiB diff at low effort")
	}
	out := renderCodeReviewContext(snap)
	if !strings.Contains(out, "truncated at") {
		t.Errorf("render must mark the truncation; got tail:\n%s", out[max(0, len(out)-200):])
	}
}

// TestCodeReviewContext_GoldenHeaderOrder pins the section order so
// the orchestrator can pattern-match the snapshot deterministically.
func TestCodeReviewContext_GoldenHeaderOrder(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	gitRun(t, tmp, "checkout", "-q", "-b", "feature/x")
	writeFile(t, tmp, "f.txt", "v1\nv2\n")
	gitCommit(t, tmp, "feat: extend f")

	out := renderCodeReviewContext(mustBuild(t, tmp, "medium"))
	headers := []string{"## state", "## changed-files", "## diff", "## commit-log", "## style-context"}
	prev := -1
	for _, h := range headers {
		idx := strings.Index(out, h)
		if idx < 0 {
			t.Errorf("missing header %q in:\n%s", h, out)
			continue
		}
		if idx < prev {
			t.Errorf("header %q out of order", h)
		}
		prev = idx
	}
}

// TestCodeReviewContext_StyleContext: a conventional-commit history
// surfaces detected_commit_style=conventional.
func TestCodeReviewContext_StyleContext(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	gitRun(t, tmp, "checkout", "-q", "-b", "feature/x")
	writeFile(t, tmp, "f.txt", "v1\nv2\n")
	gitCommit(t, tmp, "feat: extend f")
	writeFile(t, tmp, "f.txt", "v1\nv2\nv3\n")
	gitCommit(t, tmp, "fix: another tweak")

	out := renderCodeReviewContext(mustBuild(t, tmp, "medium"))
	if !strings.Contains(out, "detected_commit_style=conventional") {
		t.Errorf("expected conventional style detection; got:\n%s", out)
	}
}

func mustBuild(t *testing.T, cwd, effort string) CodeReviewContext {
	t.Helper()
	snap, err := BuildCodeReviewContext(context.Background(), cwd, effort)
	if err != nil {
		t.Fatalf("BuildCodeReviewContext: %v", err)
	}
	return snap
}
