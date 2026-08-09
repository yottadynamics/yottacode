package promptmacros

import "strings"

// ParseEffort folds the optional first arg to one of low|medium|high,
// defaulting to medium. An unrecognized non-empty value still maps to
// medium but returns a one-line notice so the caller knows the token
// was ignored rather than silently honored.
func ParseEffort(args []string) (effort, notice string) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "medium", ""
	}
	raw := strings.TrimSpace(args[0])
	switch strings.ToLower(raw) {
	case "low":
		return "low", ""
	case "medium":
		return "medium", ""
	case "high":
		return "high", ""
	default:
		return "medium", "[code-review] unknown effort " + raw + " — using medium (valid: low, medium, high)"
	}
}

// CodeReviewDirective is the prompt /code-review hands the
// orchestrator. Same shape as the other procedural directives: short,
// names the one composite tool the model invokes, surfaces the typed
// flags it branches on, and pins the output structure. The effort
// argument is already normalized (low|medium|high) by the caller; it
// swaps the finder set (Step 3) and the verification clause
// (Steps 7-8), and stamps the report header.
func CodeReviewDirective(effort string) string {
	return `Run a multi-agent code review of the current local diff at effort "` + effort + `".
You are the ORCHESTRATOR: you read the diff, craft review angles, fan
them out to read-only background subagents, dedup their candidate
findings, verify them, and synthesize one structured report. You do not
review the code yourself — you coordinate.

Step 1 — call code_review_context with effort="` + effort + `". It returns a
typed snapshot under section headers (## state, ## changed-files,
## diff, ## commit-log, ## style-context).

Step 2 — read ## state FIRST and handle the STOP cases before anything
else (spawn no subagents in any of them):
- not_found_base=true → surface "[code-review] could not resolve a base
  branch to diff against (no origin/HEAD and no main/master/develop)."
  and STOP.
- empty_repo=true → surface "[code-review] this repository has no commits
  yet — nothing to review." and STOP.
- diff_empty=true → surface "[code-review] no changes to review (diff is
  empty against <resolved_base>)." and STOP.
- diff_err=true AND the ## diff section is absent → surface "[code-review]
  could not compute the diff against <resolved_base> (git error)." and
  STOP. (A git range failure must never be reviewed as if it were clean.)
- no_merge_base=true is NOT a stop: it means <resolved_base> and this
  branch share no common ancestor, so the diff is base-tip..HEAD (broader
  than a normal PR view). Note it in the report header and review on.

Step 3 — read ## changed-files and ## diff and craft your angle set.
Two kinds of angle:
  Fixed quality lenses (what each means):
    - correctness — logic bugs, missed edge/error cases, off-by-one,
      nil/empty, races, resource leaks, and removed/changed behavior
      that breaks existing callers. The only lens that may yield a
      Blocker.
    - reuse — reimplements something the codebase already provides;
      duplicated logic that should call an existing helper.
    - simplification — needless complexity, dead branches, redundant
      state, control flow that can collapse.
    - efficiency — needless allocations, quadratic loops, N+1, repeated
      work that can be hoisted — hot paths only.
    - altitude — a change made at the wrong layer/abstraction boundary.
  Diff-specific angles you craft from the snapshot (examples):
    - removed-behavior audit (when the diff deletes/replaces logic),
    - cross-file tracer (when a signature/field/constant changed — trace
      every call site), and/or
    - test-coverage gap (when behavior changed without test changes).
  ` + finderClause(effort) + `

Step 4 — spawn ALL finders in ONE assistant message (one batched
parallel wave, at most 8). For each angle call the Agent tool with
subagent_type="review", run_in_background:true, a 3-5 word description,
and a SELF-CONTAINED prompt — the subagent cannot see this conversation,
so each prompt MUST state: the lens, the EXACT diff range, and the file
area to focus on. For the range, read diff_source from ## state:
  - branch-vs-base → tell the finder to call git_diff_files with
    base="<diff_base>" (the merge-base SHA from ## state) and head="HEAD".
    base..HEAD against that merge-base is the EXACT range this snapshot was
    built from. Do NOT tell it to diff the raw <resolved_base> branch tip,
    and do NOT name list_git_changed_files (it has no base and only sees
    uncommitted work — it returns nothing for an already-committed branch);
    either gives the finder a DIFFERENT change set than the snapshot, and
    Step 6 would then drop its findings as out-of-scope.
  - working-tree → tell the finder to call git_diff_files with
    base="<diff_base>" (HEAD for this source — staged + unstaged, the
    EXACT range this snapshot was built from; git_diff_files with NO
    base shows unstaged-only, so staged work would be invisible and its
    findings dropped), plus list_git_changed_files to enumerate the
    work, and to read any untracked files it names with read_file
    (untracked content appears in no git diff).
Tell it to read the surrounding code in the changed files, not just the
diff lines. End each finder prompt with: "Output a CANDIDATE LIST of
findings, one per line as ` + "`file:line — claim — severity(blocker|suggestion|nit) — confidence(high|uncertain)`" + `.
No prose, no narration. If you find nothing substantive, reply exactly:
NO FINDINGS." Record the task id each Agent call returns.

Step 5 — collect. In ONE assistant message, issue one
get_subagent_result call per finder task id (they run as a concurrent
parallel batch), each with wait_seconds=600. For any task that returns
"still running", re-issue get_subagent_result for just those ids in the
next message; do at most 2 re-collect rounds, then proceed without the
stragglers and mark their angle "timed out" in the report.

Step 6 — dedup in-context. Merge candidate findings that name the same
file:line and same root cause. Scope check at FILE granularity: keep a
finding only if its FILE appears in ## changed-files. Do NOT require the
exact line to be present in the ## diff text — that section is truncated
to diff_cap_bytes, so a real finding in a changed file beyond the cap (or
one a finder found by reading the full file, as Step 4 directs) would be
wrongly dropped if you gated on the diff body. Drop only findings whose
file is absent from ## changed-files entirely. Keep, per finding, its
claim, severity, and confidence.

` + verifyClause(effort) + `

Step 9 — emit the report to scrollback in exactly this structure:

  ## Code Review: <current_branch> (effort: ` + effort + `)
  <one line: N files changed vs <resolved_base>; F finders run;
   C confirmed / R refuted / U unverified>

  ### Angles run
  | angle | candidates | confirmed |
  | correctness | 3 | 2 |
  | ... one row per finder; note "timed out" for any that didn't return |

  ### Blockers
  <confirmed correctness findings that must be fixed. Each:
   file:line — claim — 1-2 sentence why it breaks. "(none)" if empty.>

  ### Suggestions
  <confirmed reuse / simplification / efficiency / altitude findings
   worth acting on. Same format. "(none)" if empty.>

  ### Nits
  <minor / cosmetic. "(none)" if empty.>

  ### Refuted / dropped
  <count, then one line each: claim → why (verifier refuted it /
   out-of-diff location / duplicate). "(0)" if none.>

Hard constraints:
- This is a REVIEW, not a fix. Do NOT call write_file / edit_file /
  apply_diff, and do NOT instruct any subagent to. The author owns the
  changes.
- Output to scrollback ONLY. Do NOT post the review to GitHub or
  anywhere else.
- Do NOT fabricate file:line refs. Cite only files the ## changed-files
  snapshot lists (a finder may legitimately cite a line beyond the
  truncated ## diff, as long as the FILE is in scope). Drop the rest.
- Never have more than 8 background subagents running at once — and a
  finder straggler you abandoned in Step 5 is STILL RUNNING and STILL
  holds a slot (you cannot free it; only the user can /subagents stop).
  Before each verifier wave, count everything still running (abandoned
  stragglers included) and size the wave to (8 − that count); if 0 slots
  are free, skip verifying the rest and tag those findings "unverified"
  rather than spawning Agent calls that get rejected. If any Agent call
  returns an "at most 8 … concurrent" error, you overshot — collect a
  wave to free slots before retrying.
- Every subagent prompt is self-contained — the subagent has no access
  to this conversation. Embed the base branch, file scope, and (for
  verifiers) the exact claim.`
}

// finderClause is the per-effort Step-3 instruction for how many
// finders to run and how to group the lenses. Kept separate from the
// shared lens definitions so the effort levels only differ in the
// fan-out width, not in what each lens means.
func finderClause(effort string) string {
	switch effort {
	case "low":
		return `For this effort run exactly 2 finders: (1) correctness,
  (2) reuse+simplification (merged). No diff-specific angles.`
	case "high":
		return `For this effort run up to 8 finders: one per fixed lens
  (correctness, reuse, simplification, efficiency, altitude) plus up to
  3 diff-specific angles you craft from the snapshot. Choose angles that
  fit THIS diff; total at most 8 finders.`
	default: // medium
		return `For this effort run exactly 4 finders: (1) correctness,
  (2) reuse+simplification, (3) efficiency+altitude, plus (4) one
  diff-specific angle you craft from the snapshot. Total 4 finders.`
	}
}

// verifyClause is the per-effort Steps 7-8 instruction. low skips
// verification; medium verifies only low-confidence findings; high
// verifies every deduped finding. Verifiers dispatch to the dedicated
// read-only `code-verifier` vessel (it has no run_bash/write tools, so
// nothing it does gets auto-denied in the background and its persona is
// "refute one finding from the code" — no run-the-tests instinct to
// override in the prompt).
func verifyClause(effort string) string {
	const collect = `Step 8 — collect verdicts the same way as Step 5 (one
get_subagent_result per verifier id in one message, wait_seconds=600,
re-collect stragglers ≤2 rounds). A finding is CONFIRMED on
VERDICT: PASS, REFUTED on VERDICT: FAIL; on VERDICT: PARTIAL or a
timed-out verifier, keep the finding but tag it "unverified".`

	const verifierPrompt = `For each finding call Agent with
subagent_type="code-verifier", run_in_background:true, and a
self-contained prompt: "A reviewer claims: <file:line — claim>. Try to
REFUTE it by reading that location and the surrounding code and tracing
callers. End with the verdict line VERDICT: PASS (claim stands),
FAIL (claim refuted), or PARTIAL." The code-verifier is read-only and
owns the verdict contract; you only supply the claim.`

	switch effort {
	case "low":
		return `Step 7 — skip verification entirely. Every surviving deduped
finding is a candidate finding; carry it straight to the report (tagged
"unverified").`
	case "high":
		return `Step 7 — verify. Spawn one verifier per deduped finding, in
slot-sized WAVES: a wave is at most (8 − finders still running). Any
finder straggler you abandoned in Step 5 is still running and still holds
a background slot, so if K stragglers remain a verifier wave is at most
(8 − K); if 0 slots are free, skip verifying the rest and tag those
findings "unverified". ` + verifierPrompt + ` Collect each wave fully
(Step 8), which frees its slots, before spawning the next.

` + collect
	default: // medium
		return `Step 7 — verify ONLY the deduped findings whose confidence is
"uncertain"; findings marked high-confidence skip verification. Spawn
verifiers in slot-sized waves — at most (8 − any finder stragglers still
running from Step 5) — and collect each wave (freeing its slots) before
the next.
` + verifierPrompt + `

` + collect
	}
}
