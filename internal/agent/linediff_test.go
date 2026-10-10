package agent

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func numberedLines(n int, prefix string) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "%s%d\n", prefix, i)
	}
	return sb.String()
}

func TestBoundedUnifiedDiff_NoChanges(t *testing.T) {
	got := boundedUnifiedDiff("f.txt", "same\n", "same\n", 2, 80)
	if got != "diff -- f.txt\n(no changes)\n" {
		t.Errorf("got %q", got)
	}
}

func TestBoundedUnifiedDiff_SingleChangeHasExactHeaderAndContext(t *testing.T) {
	got := boundedUnifiedDiff("f", "a\nb\nc\n", "a\nB\nc\n", 2, 80)
	want := "--- f\n+++ f\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// With an empty range the hunk start is the line *before* it (unified-diff
// convention), which is why an insertion at the top of a file starts at 0.
func TestBoundedUnifiedDiff_EmptyRangeHeaders(t *testing.T) {
	cases := []struct{ name, old, new, want string }{
		{"insert at start", "b\n", "a\nb\n", "--- f\n+++ f\n@@ -0,0 +1,1 @@\n+a\n"},
		{"delete only line", "a\n", "", "--- f\n+++ f\n@@ -1,1 +0,0 @@\n-a\n"},
		{"create from empty", "", "a\nb\n", "--- f\n+++ f\n@@ -0,0 +1,2 @@\n+a\n+b\n"},
	}
	for _, c := range cases {
		// Zero context keeps each expectation to the changed lines alone.
		if got := boundedUnifiedDiff("f", c.old, c.new, 0, 80); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Two edits far apart must produce two hunks that show both changes, not one
// giant hunk that reads as "everything in between was removed".
func TestBoundedUnifiedDiff_TwoDistantChangesProduceTwoHunks(t *testing.T) {
	oldText := numberedLines(200, "l")
	newText := strings.Replace(oldText, "l1\n", "L1\n", 1)
	newText = strings.Replace(newText, "l200\n", "L200\n", 1)
	got := boundedUnifiedDiff("f.txt", oldText, newText, 2, 80)

	for _, want := range []string{"-l1\n+L1\n", "-l200\n+L200\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff is missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Errorf("got %d hunks, want 2:\n%s", n, got)
	}
	if strings.Contains(got, "l100") {
		t.Errorf("unchanged middle of the file leaked into the diff:\n%s", got)
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("a two-line change must not be reported as truncated:\n%s", got)
	}
}

func TestBoundedUnifiedDiff_NearbyChangesMergeIntoOneHunk(t *testing.T) {
	oldText := numberedLines(20, "l")
	newText := strings.Replace(oldText, "l5\n", "L5\n", 1)
	newText = strings.Replace(newText, "l8\n", "L8\n", 1) // gap of 2 lines <= 2*context
	got := boundedUnifiedDiff("f", oldText, newText, 2, 80)
	if n := strings.Count(got, "@@ -"); n != 1 {
		t.Errorf("got %d hunks, want 1:\n%s", n, got)
	}
}

func TestBoundedUnifiedDiff_CapMarksTruncationWhenAddedLinesAreCut(t *testing.T) {
	// 1 line removed, 500 added: the removals fit under the cap, the additions
	// do not. That used to be cut silently.
	got := boundedUnifiedDiff("f", "keep\nold\nkeep2\n", "keep\n"+numberedLines(500, "added")+"keep2\n", 2, 80)
	if !strings.Contains(got, "…[truncated diff]") {
		t.Errorf("cut additions must be flagged as truncated")
	}
	if n := strings.Count(got, "\n+added"); n >= 500 {
		t.Errorf("cap did not apply: %d added lines shown", n)
	}
}

func TestBoundedUnifiedDiff_NoMarkerWhenExactlyAtTheCap(t *testing.T) {
	got := boundedUnifiedDiff("f", numberedLines(10, "a"), numberedLines(10, "b"), 0, 20) // 10 removed + 10 added = 20
	if strings.Contains(got, "truncated") {
		t.Errorf("output at exactly the cap is complete and must not be marked truncated:\n%s", got)
	}
}

func TestBoundedUnifiedDiff_OutputIsByteBounded(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	got := boundedUnifiedDiff("min.js", "a\n"+long+"\nz\n", "A\n"+long+"\nz\n", 2, 80)
	if len(got) > boundedDiffMaxBytes+512 {
		t.Errorf("diff of a 1-byte change beside a 1 MiB line is %d bytes", len(got))
	}
	if !strings.Contains(got, "-a\n+A\n") {
		t.Errorf("the actual change is missing:\n%.300s", got)
	}
}

func TestBoundedUnifiedDiff_ClipsLongLinesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("日", 500)
	got := boundedUnifiedDiff("f", "a\n"+long+"\n", "b\n"+long+"\n", 2, 80)
	if !utf8.ValidString(got) {
		t.Errorf("clipping split a rune")
	}
	if !strings.Contains(got, "…") {
		t.Errorf("a clipped line should end with an ellipsis")
	}
}

func TestBoundedUnifiedDiff_TrailingNewlineChangeIsVisible(t *testing.T) {
	got := boundedUnifiedDiff("f", "a\nb", "a\nb\n", 2, 80)
	if strings.Contains(got, "no changes") || !strings.Contains(got, "-b\n\\ No newline at end of file\n+b\n") {
		t.Errorf("adding a final newline must show as a change:\n%s", got)
	}
}

func TestBoundedUnifiedDiff_LineEndingChangeIsVisible(t *testing.T) {
	got := boundedUnifiedDiff("f", "a\r\nb\r\n", "a\nb\r\n", 2, 80)
	if !strings.Contains(got, "-a\r\n+a\n") {
		t.Errorf("CRLF -> LF on one line must show as a change:\n%q", got)
	}
}

func TestBoundedUnifiedDiff_LargeRewriteStaysBoundedAndReportsTrueSize(t *testing.T) {
	got := boundedUnifiedDiff("f", numberedLines(3000, "old"), numberedLines(3000, "new"), 2, 80)
	if len(got) > boundedDiffMaxBytes+512 {
		t.Errorf("rewrite diff is %d bytes", len(got))
	}
	if !strings.Contains(got, "@@ -1,3000 +1,3000 @@") {
		t.Errorf("header should report the true size of the change:\n%.200s", got)
	}
	if !strings.Contains(got, "…[truncated diff]") {
		t.Errorf("a 6000-line change under an 80-line cap must be marked truncated")
	}
}

// diffEdits must return a minimal edit script that really transforms a into b.
func TestDiffEdits_IsMinimalAndReproducesTarget(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a\n", "b\n", "c\n", "d\n"}
	gen := func() []string {
		out := make([]string, rng.Intn(13))
		for i := range out {
			out[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return out
	}
	for iter := 0; iter < 500; iter++ {
		a, b := gen(), gen()
		edits, ok := myersEdits(a, b, len(a)+len(b))
		if !ok {
			t.Fatalf("iter %d: myersEdits gave up on tiny input a=%q b=%q", iter, a, b)
		}
		var gotB, gotA []string
		i, j, changes := 0, 0, 0
		for _, k := range edits {
			switch k {
			case '=':
				if a[i] != b[j] {
					t.Fatalf("iter %d: '=' pairs unequal lines", iter)
				}
				gotA, gotB = append(gotA, a[i]), append(gotB, b[j])
				i, j = i+1, j+1
			case '-':
				gotA = append(gotA, a[i])
				i, changes = i+1, changes+1
			case '+':
				gotB = append(gotB, b[j])
				j, changes = j+1, changes+1
			}
		}
		if i != len(a) || j != len(b) || strings.Join(gotA, "") != strings.Join(a, "") || strings.Join(gotB, "") != strings.Join(b, "") {
			t.Fatalf("iter %d: script does not reproduce inputs a=%q b=%q edits=%q", iter, a, b, edits)
		}
		if want := len(a) + len(b) - 2*lcsLen(a, b); changes != want {
			t.Fatalf("iter %d: script has %d edits, minimal is %d (a=%q b=%q)", iter, changes, want, a, b)
		}
	}
}

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	return dp[len(a)][len(b)]
}
