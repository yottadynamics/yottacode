package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// TestCodeReviewDirective_NamesCompositeTool locks the contract: the
// directive must reference code_review_context by name. The
// deterministic diff/base resolution lives in that tool; if the prompt
// stops naming it we silently regress to whatever the model picks
// (likely raw `git diff` from run_bash).
func TestCodeReviewDirective_NamesCompositeTool(t *testing.T) {
	got := codeReviewDirective("medium")
	if !strings.Contains(got, "code_review_context") {
		t.Errorf("codeReviewDirective must name code_review_context; got:\n%s", got)
	}
}

// TestCodeReviewDirective_SplicesEffort pins that the normalized
// effort is embedded both in the Step-1 tool call and in the report
// header, so the subagents and the output agree on the scope.
func TestCodeReviewDirective_SplicesEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high"} {
		got := codeReviewDirective(effort)
		if !strings.Contains(got, `effort="`+effort+`"`) {
			t.Errorf("codeReviewDirective(%q) must splice effort=%q into the tool call; got:\n%s", effort, effort, got)
		}
		if !strings.Contains(got, "(effort: "+effort+")") {
			t.Errorf("codeReviewDirective(%q) must stamp the report header with effort: %s", effort, effort)
		}
	}
}

// TestCodeReviewDirective_PinsHardProhibitions pins the bright lines
// that keep this a review (read-only, scrollback) rather than a
// fix-up pass. An accidental relaxation requires a deliberate test
// edit.
func TestCodeReviewDirective_PinsHardProhibitions(t *testing.T) {
	got := codeReviewDirective("medium")
	mustContain := []string{
		"Do NOT call write_file",     // review, not fix-up
		"scrollback",                 // output target
		"Do NOT fabricate file:line", // no hallucinated locations
		"STOP",                       // explicit stop on the snapshot flags
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("codeReviewDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestCodeReviewDirective_EncodesAngleCounts pins the per-effort
// fan-out width. high names every fixed lens and caps the wave at 8;
// medium and low pin their exact finder counts. If these drift, the
// concurrency math (≤8 per background wave) silently breaks.
func TestCodeReviewDirective_EncodesAngleCounts(t *testing.T) {
	high := codeReviewDirective("high")
	for _, lens := range []string{"correctness", "reuse", "simplification", "efficiency", "altitude"} {
		if !strings.Contains(high, lens) {
			t.Errorf("high directive must name the %q lens; got:\n%s", lens, high)
		}
	}
	if !strings.Contains(high, "at most 8") {
		t.Errorf("high directive must cap the finder wave at 8; got:\n%s", high)
	}
	if med := codeReviewDirective("medium"); !strings.Contains(med, "Total 4 finders") {
		t.Errorf("medium directive must run 4 finders; got:\n%s", med)
	}
	low := codeReviewDirective("low")
	if !strings.Contains(low, "2 finders") {
		t.Errorf("low directive must run 2 finders; got:\n%s", low)
	}
	if !strings.Contains(low, "No diff-specific angles") {
		t.Errorf("low directive must skip diff-specific angles; got:\n%s", low)
	}
}

// TestCodeReviewDirective_VerificationGating pins the effort-scaled
// verification clause: low skips it, medium verifies only uncertain
// findings, high verifies every finding.
func TestCodeReviewDirective_VerificationGating(t *testing.T) {
	if low := codeReviewDirective("low"); !strings.Contains(low, "skip verification") {
		t.Errorf("low directive must skip verification; got:\n%s", low)
	}
	if med := codeReviewDirective("medium"); !strings.Contains(med, "uncertain") {
		t.Errorf("medium directive must verify only uncertain findings; got:\n%s", med)
	}
	if high := codeReviewDirective("high"); !strings.Contains(high, "one verifier per") {
		t.Errorf("high directive must verify one per finding; got:\n%s", high)
	}
}

// TestCodeReviewDirective_PinsConcurrencyDiscipline guards the two
// invariants that keep the run inside the 8-slot background cap and
// the per-turn iteration budget: spawn/collect in ONE message
// (parallel batches), and spawn verifiers in waves.
func TestCodeReviewDirective_PinsConcurrencyDiscipline(t *testing.T) {
	got := codeReviewDirective("medium")
	for _, frag := range []string{"ONE assistant message", "wave", "at most 8"} {
		if !strings.Contains(got, frag) {
			t.Errorf("codeReviewDirective must contain %q; got:\n%s", frag, got)
		}
	}
}

// TestCodeReviewDirective_PinsStopCases pins the typed STOP flags the
// orchestrator branches on before spawning anything — including the
// empty_repo and diff_err cases added so a no-commit repo / a git range
// failure are not reviewed as "looks good".
func TestCodeReviewDirective_PinsStopCases(t *testing.T) {
	got := codeReviewDirective("medium")
	for _, frag := range []string{"not_found_base", "diff_empty", "empty_repo=true", "diff_err=true"} {
		if !strings.Contains(got, frag) {
			t.Errorf("codeReviewDirective must handle the %q STOP case; got:\n%s", frag, got)
		}
	}
}

// TestCodeReviewDirective_FinderDiffRangeMatchesSnapshot pins that finders
// are directed to diff the SAME range the snapshot used — base=<diff_base>
// (the merge-base SHA) head="HEAD", a two-dot range identical to the
// snapshot's. Before this, finders were told to diff "against the base"
// (a different two-dot view against the base tip), so Step 6 silently
// dropped their findings as out-of-scope whenever the base had advanced.
func TestCodeReviewDirective_FinderDiffRangeMatchesSnapshot(t *testing.T) {
	got := codeReviewDirective("medium")
	for _, frag := range []string{`base="<diff_base>"`, `head="HEAD"`, "merge-base SHA"} {
		if !strings.Contains(got, frag) {
			t.Errorf("finder directive must pin the merge-base range %q; got:\n%s", frag, got)
		}
	}
	// list_git_changed_files must no longer be named as a way to diff
	// against a base (it has no base param) — only for working-tree work.
	if strings.Contains(got, "list_git_changed_files / git_diff_files\nagainst the base") {
		t.Errorf("directive must not tell finders to diff a base with list_git_changed_files; got:\n%s", got)
	}
}

// TestCodeReviewDirective_WorkingTreeFindersSeeStagedWork pins the
// working-tree leg of the same range bug: the snapshot is built from
// `git diff HEAD` (staged + unstaged, diff_base=HEAD), but finders were
// told to call git_diff_files "with no base" — plain `git diff`,
// unstaged-only. For the advertised "review before I commit" flow,
// where work is typically already staged, every finder saw an empty
// diff and reported NO FINDINGS against a snapshot full of changes.
func TestCodeReviewDirective_WorkingTreeFindersSeeStagedWork(t *testing.T) {
	got := codeReviewDirective("medium")
	for _, frag := range []string{
		"staged + unstaged",
		"read any untracked files",
	} {
		if !strings.Contains(got, frag) {
			t.Errorf("working-tree finder clause must pin the snapshot range (missing %q); got:\n%s", frag, got)
		}
	}
	if strings.Contains(got, "git_diff_files with no base") {
		t.Errorf("directive must not tell working-tree finders to diff with no base (unstaged-only); got:\n%s", got)
	}
	// The clause must direct finders at the published diff_base for the
	// working-tree source too, not just the branch-vs-base source.
	if !strings.Contains(got, `working-tree → tell the finder to call git_diff_files with
    base="<diff_base>"`) {
		t.Errorf("working-tree finders must diff base=\"<diff_base>\" (HEAD); got:\n%s", got)
	}
}

// TestCodeReviewDirective_ScopeCheckIsFileGranular pins that the dedup
// scope check gates on the FILE being in ## changed-files, not on the
// exact line surviving the truncated ## diff — otherwise real findings
// past the diff cap are wrongly dropped.
func TestCodeReviewDirective_ScopeCheckIsFileGranular(t *testing.T) {
	got := codeReviewDirective("medium")
	if !strings.Contains(got, "FILE granularity") {
		t.Errorf("Step 6 must scope-check at FILE granularity; got:\n%s", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("Step 6 must acknowledge the ## diff is truncated when scoping; got:\n%s", got)
	}
}

// TestCodeReviewDirective_VerifierWavesAreSlotSized pins that verifier
// waves are sized to the FREE background slots (abandoned finder
// stragglers still hold their slots), so the wave can't overshoot the
// 8-cap and get spawns rejected.
func TestCodeReviewDirective_VerifierWavesAreSlotSized(t *testing.T) {
	for _, effort := range []string{"medium", "high"} {
		got := codeReviewDirective(effort)
		if !strings.Contains(got, "slot-sized") {
			t.Errorf("%s directive must size verifier waves to free slots (slot-sized); got:\n%s", effort, got)
		}
	}
	// The hard constraints must explain that an abandoned straggler still
	// occupies a slot (the root cause of the overflow).
	if !strings.Contains(codeReviewDirective("medium"), "holds a slot") {
		t.Errorf("directive must note that an abandoned straggler still holds a background slot")
	}
}

// TestCodeReviewDirective_VerifiersUseCodeVerifier pins that verifiers
// dispatch to the dedicated read-only `code-verifier` vessel rather than
// the build/test `verification` agent. The vessel has no run_bash/write
// tools, so the directive no longer needs a "do NOT attempt run_bash"
// override — read-only-ness is enforced by the vessel, not the prompt.
func TestCodeReviewDirective_VerifiersUseCodeVerifier(t *testing.T) {
	got := codeReviewDirective("high")
	if !strings.Contains(got, `subagent_type="code-verifier"`) {
		t.Errorf("high directive must dispatch verifiers to the read-only code-verifier vessel; got:\n%s", got)
	}
}

// TestSlash_CodeReviewCommandRegistered verifies the built-in is in
// the slash registry under the exact /code-review slug Claude's
// surface uses (not a git- prefixed slug).
func TestSlash_CodeReviewCommandRegistered(t *testing.T) {
	if findSlash("code-review") == nil {
		t.Errorf("expected /code-review in the slash registry")
	}
	if findSlash("git-code-review") != nil {
		t.Errorf("/git-code-review should not be registered; the surface mirrors Claude's /code-review")
	}
}

// TestSlash_CodeReviewBailsWhenTurnActive guards the turn-active early
// return — checked before the background gate, same pattern as the
// other procedural commands.
func TestSlash_CodeReviewBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdCodeReview(m, nil)
	if cmd != nil {
		t.Errorf("cmdCodeReview should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}

// TestSlash_CodeReviewStartsWithoutBackgroundFlag proves /code-review is GA:
// it no longer refuses on a background_subagents experimental gate and reaches
// startTurnWithDisplay (which, with the test harness's nil adapter, surfaces
// the "no provider configured" bail).
func TestSlash_CodeReviewStartsWithoutBackgroundFlag(t *testing.T) {
	m := newTestModel(t)
	m.subagentTool = &agent.AgentTool{AllowBackground: false}
	out, _ := cmdCodeReview(m, nil)
	got := out.transcript.String()
	if strings.Contains(got, "background_subagents") {
		t.Errorf("/code-review should not mention the removed experimental gate; got: %q", got)
	}
	if !strings.Contains(got, "no provider configured") {
		t.Errorf("expected the command to reach startTurnWithDisplay; got: %q", got)
	}
}

// TestParseEffort pins the arg normalization: empty/whitespace/unknown
// fall to medium (unknown also returns a notice), and the three valid
// levels pass case-insensitively.
func TestParseEffort(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		want       string
		wantNotice bool
	}{
		{"nil defaults to medium", nil, "medium", false},
		{"empty arg defaults to medium", []string{""}, "medium", false},
		{"whitespace defaults to medium", []string{"   "}, "medium", false},
		{"low passes", []string{"low"}, "low", false},
		{"medium passes", []string{"medium"}, "medium", false},
		{"high passes", []string{"high"}, "high", false},
		{"case-insensitive", []string{"HIGH"}, "high", false},
		{"unknown falls to medium with notice", []string{"bogus"}, "medium", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effort, notice := parseEffort(tc.args)
			if effort != tc.want {
				t.Errorf("parseEffort(%v) effort = %q, want %q", tc.args, effort, tc.want)
			}
			if (notice != "") != tc.wantNotice {
				t.Errorf("parseEffort(%v) notice = %q, wantNotice = %v", tc.args, notice, tc.wantNotice)
			}
		})
	}
}
