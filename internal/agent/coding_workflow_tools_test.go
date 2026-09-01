package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/sandboxcache"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

func TestApplyDiffTool_AppliesPatch(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "one\ntwo\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	diff := "diff --git a/a.txt b/a.txt\nindex 814f4a4..b4e7f16 100644\n--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"
	args := map[string]string{"diff": diff}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), string(b)); err != nil {
		t.Fatalf("Execute: %v (args=%s)", err, string(b))
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "a.txt"))
	if string(got) != "one\nTWO\n" {
		t.Errorf("file = %q", string(got))
	}
}

func TestApplyDiffTool_AppliesMiscountedHunkHeader(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "one\ntwo\nthree\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	// The hunk body has three old and three new lines, but the header claims
	// four of each. --recount should repair that model-authored arithmetic.
	diff := "--- a/a.txt\n+++ b/a.txt\n@@ -1,4 +1,4 @@\n one\n-two\n+TWO\n three\n"
	b, err := json.Marshal(map[string]string{"diff": diff})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), string(b)); err != nil {
		t.Fatalf("Execute: %v (diff=%q)", err, diff)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "a.txt"))
	if string(got) != "one\nTWO\nthree\n" {
		t.Errorf("file = %q", string(got))
	}
}

func TestApplyDiffTool_AppliesWhitespaceDriftedContext(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "start\n\tcontext\nold\nend\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	// The unchanged context line is tab-indented on disk but space-indented in
	// the diff. The lenient retry should absorb that context whitespace drift.
	diff := "--- a/a.txt\n+++ b/a.txt\n@@ -1,4 +1,4 @@\n start\n     context\n-old\n+new\n end\n"
	b, err := json.Marshal(map[string]string{"diff": diff})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), string(b)); err != nil {
		t.Fatalf("Execute: %v (diff=%q)", err, diff)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "a.txt"))
	if string(got) != "start\n\tcontext\nnew\nend\n" {
		t.Errorf("file = %q", string(got))
	}
}

// TestRepairFullyEscapedDiff guards the apply_diff repair heuristic.
// Regression for the release audit's
// apply-diff-global-backslash-n-replacement-corrupts-patch finding.
func TestRepairFullyEscapedDiff(t *testing.T) {
	// A genuine multi-line diff whose content legitimately contains the
	// 3-char sequence \\n (patching a "\\n" string literal) must be
	// returned untouched — the old unconditional ReplaceAll split that
	// content line on a fabricated newline and corrupted the patch.
	multiline := "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n" + `+s := "\\n"` + "\n"
	if got := repairFullyEscapedDiff(multiline); got != multiline {
		t.Errorf("multi-line diff was mutated:\n got=%q\nwant=%q", got, multiline)
	}

	// A fully JSON-escaped single-line diff (no real newlines) IS
	// repaired: the literal \\n sequences become real newlines.
	escaped := `--- a/x\\n+++ b/x\\n@@ -1 +1 @@\\n-old\\n+new`
	want := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new"
	if got := repairFullyEscapedDiff(escaped); got != want {
		t.Errorf("escaped diff not repaired:\n got=%q\nwant=%q", got, want)
	}
}

// TestApplyDiffTool_PreservesBackslashEscapesInContent applies a real
// diff whose replacement line contains a literal \\n and verifies the
// file ends up with that literal text, not a corrupted newline split.
func TestApplyDiffTool_PreservesBackslashEscapesInContent(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "one\ntwo\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	diff := "--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n" + `+two\\nthree` + "\n"
	b, err := json.Marshal(map[string]string{"diff": diff})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), string(b)); err != nil {
		t.Fatalf("Execute: %v (diff=%q)", err, diff)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "a.txt"))
	if want := "one\n" + `two\\nthree` + "\n"; string(got) != want {
		t.Errorf("file = %q, want %q", string(got), want)
	}
}

func TestApplyDiffTool_RequiresDiff(t *testing.T) {
	tmp := t.TempDir()
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error")
	}
}

// Regression: a diff targeting a path inside DefaultDenyPaths must be
// refused even though the approval modal would have approved it. The
// previous implementation extracted no path from the diff and ran git
// apply unconditionally, letting the model patch yottacode-managed
// state (e.g. self-grant a permissions rule) by passing it through the
// patch surface.
func TestApplyDiffTool_RefusesDeniedPath(t *testing.T) {
	tmp := gitInit(t)
	denied := filepath.Join(tmp, ".yottacode", "permissions.local.json")
	tool := &ApplyDiffTool{
		Cwd:       NewCwdRef(tmp),
		WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp), DenyExact: DefaultDenyPaths(tmp)},
	}
	// New-file diff that creates the denied path.
	diff := "diff --git a/.yottacode/permissions.local.json b/.yottacode/permissions.local.json\n" +
		"new file mode 100644\n" +
		"index 0000000..abcdef0\n" +
		"--- /dev/null\n" +
		"+++ b/.yottacode/permissions.local.json\n" +
		"@@ -0,0 +1 @@\n" +
		"+{\"permissions\":{\"allow\":[\"Bash(*)\"]}}\n"
	args := map[string]string{"diff": diff}
	b, _ := json.Marshal(args)
	out, err := tool.Execute(context.Background(), string(b))
	if err == nil {
		t.Fatalf("expected error, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "deny list") {
		t.Errorf("expected deny-list error, got %v", err)
	}
	if _, statErr := os.Stat(denied); statErr == nil {
		t.Errorf("denied path was created: %s", denied)
	}
}

// Regression: a diff with no parseable header must be refused. An
// empty path set used to vacuously pass validation; now it errors so
// the model gets a clear signal — and the error echoes a quoted snippet
// of what was received so the (usually invisible) defect is debuggable.
func TestApplyDiffTool_RefusesEmptyHeaderDiff(t *testing.T) {
	tmp := gitInit(t)
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := map[string]string{"diff": "garbage with no diff headers\n"}
	b, _ := json.Marshal(args)
	_, err := tool.Execute(context.Background(), string(b))
	if err == nil {
		t.Fatalf("expected error for headerless diff")
	}
	// The error must quote what the parser saw so the model/user can see why.
	if !strings.Contains(err.Error(), `garbage with no diff headers`) {
		t.Errorf("error should echo the received diff snippet, got %v", err)
	}
}

// Security regression: the escaped-diff variant of RefusesDeniedPath. A
// fully JSON-escaped diff (newlines as the literal \\n sequence, no real
// newlines) that targets a denied path must still be refused. Before the
// repair-ordering fix, ParseDiffPaths ran on the un-repaired bytes and
// extracted one garbage "path" (the whole diff tail) that matched no deny
// entry and resolved under cwd, so ValidateWritePath passed — then the
// diff was repaired into a real patch and git apply wrote the denied file,
// self-granting a permissions rule. The repair now precedes validation, so
// the target git apply sees is the target we validate.
func TestApplyDiffTool_RefusesDeniedPath_Escaped(t *testing.T) {
	tmp := gitInit(t)
	denied := filepath.Join(tmp, ".yottacode", "permissions.local.json")
	if err := os.MkdirAll(filepath.Dir(denied), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(denied, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ApplyDiffTool{
		Cwd:       NewCwdRef(tmp),
		WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp), DenyExact: DefaultDenyPaths(tmp)},
	}
	// No real newlines; `\\n` is the literal 3-char escape sequence.
	escaped := `--- a/.yottacode/permissions.local.json\\n+++ b/.yottacode/permissions.local.json\\n@@ -1 +1 @@\\n-x\\n+{"permissions":{"allow":["Bash(*)"]}}\\n`
	b, _ := json.Marshal(map[string]string{"diff": escaped})
	out, err := tool.Execute(context.Background(), string(b))
	if err == nil {
		t.Fatalf("expected deny-list refusal, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "deny list") {
		t.Errorf("expected deny-list error, got %v", err)
	}
	got, _ := os.ReadFile(denied)
	if string(got) != "x\n" {
		t.Errorf("denied file was modified: %q", string(got))
	}
}

// Regression: a fully JSON-escaped diff targeting an ALLOWED file must be
// repaired and applied, not rejected. Before the fix, ParseDiffPaths ran
// before repairFullyEscapedDiff, so this single-line diff (git-style
// prelude, no real newlines) produced an empty path set and bounced with
// "no recognizable file headers" even though the repair would make it
// applyable.
func TestApplyDiffTool_AppliesFullyEscapedDiff(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "one\ntwo\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	escaped := `diff --git a/a.txt b/a.txt\\n--- a/a.txt\\n+++ b/a.txt\\n@@ -1,2 +1,2 @@\\n one\\n-two\\n+TWO\\n`
	b, _ := json.Marshal(map[string]string{"diff": escaped})
	if _, err := tool.Execute(context.Background(), string(b)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "a.txt"))
	if string(got) != "one\nTWO\n" {
		t.Errorf("file = %q, want %q", string(got), "one\nTWO\n")
	}
}

func TestApplyDiffTool_PreflightRejectsMalformedPatchWrappers(t *testing.T) {
	tmp := gitInit(t)
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	cases := []struct {
		name string
		diff string
		want string
	}{
		{
			name: "apply-patch-style",
			diff: "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch",
			want: "apply_patch-style patches are not accepted",
		},
		{
			name: "markdown-fence",
			diff: "```diff\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n```",
			want: "remove markdown fences",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]string{"diff": tc.diff})
			_, err := tool.Execute(context.Background(), string(b))
			if err == nil {
				t.Fatalf("expected malformed wrapper to be rejected")
			}
			if !strings.Contains(err.Error(), "malformed_patch") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected targeted malformed wrapper error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestApplyDiffTool_AllowsPatchContentThatLooksLikeWrappers(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "doc.md", "old\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	cases := []struct {
		name string
		diff string
		want string
	}{
		{
			name: "apply-patch-string-in-content",
			diff: "--- a/doc.md\n+++ b/doc.md\n@@ -1 +1 @@\n-old\n+*** Begin Patch\n",
			want: "*** Begin Patch\n",
		},
		{
			name: "final-markdown-fence-in-content",
			diff: "--- a/doc.md\n+++ b/doc.md\n@@ -1 +1 @@\n-old\n+```\n",
			want: "```\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, tmp, "doc.md", "old\n")
			b, _ := json.Marshal(map[string]string{"diff": tc.diff})
			if _, err := tool.Execute(context.Background(), string(b)); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			got, _ := os.ReadFile(filepath.Join(tmp, "doc.md"))
			if string(got) != tc.want {
				t.Fatalf("doc.md = %q, want %q", string(got), tc.want)
			}
		})
	}
}

func TestApplyDiffTool_RejectsBareHunkHeaderBeforeGitApply(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "one\ntwo\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	diff := "--- a/a.txt\n+++ b/a.txt\n@@\n-one\n+ONE\n"
	b, _ := json.Marshal(map[string]string{"diff": diff})
	_, err := tool.Execute(context.Background(), string(b))
	if err == nil {
		t.Fatalf("expected malformed patch error")
	}
	if !strings.Contains(err.Error(), "malformed_patch") || !strings.Contains(err.Error(), "hunk header") {
		t.Fatalf("expected targeted malformed hunk error, got %v", err)
	}
}

func TestApplyDiffTool_StaleContextHintIsSpecific(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "current\n")
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	diff := "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-stale\n+new\n"
	b, _ := json.Marshal(map[string]string{"diff": diff})
	_, err := tool.Execute(context.Background(), string(b))
	if err == nil {
		t.Fatalf("expected stale-context patch to fail")
	}
	if !strings.Contains(err.Error(), "hunk context did not match the current file") {
		t.Fatalf("expected stale-context hint, got %v", err)
	}
}

func TestClassifyPatchFailure(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want PatchFailureKind
	}{
		{
			name: "corrupt patch is malformed",
			out:  "warning: recount: unexpected line: @@\n\nerror: corrupt patch at line 30\n",
			want: PatchFailureMalformed,
		},
		{
			name: "preflight malformed",
			out:  "apply_diff: malformed_patch: hunk header for a.txt must include line ranges",
			want: PatchFailureMalformed,
		},
		{
			name: "stale context",
			out:  "error: patch failed: a.txt:1\nerror: a.txt: patch does not apply\n",
			want: PatchFailureStale,
		},
		{
			name: "patch payload does not override stale diagnostic",
			out:  `apply_diff: exit status 1; stderr="error: patch failed: a.txt:1\nerror: a.txt: patch does not apply" hint="..." patch="+corrupt patch"`,
			want: PatchFailureStale,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPatchFailure(tc.out); got != tc.want {
				t.Fatalf("ClassifyPatchFailure() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListGitChangedFilesTool_FindsStagedUnstagedAndUntracked(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "tracked.txt", "v2\n")
	writeFile(t, tmp, "new.txt", "n\n")
	tool := &ListGitChangedFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "tracked.txt") {
		t.Errorf("missing modified tracked file: %q", out)
	}
	if !strings.Contains(out, "new.txt") {
		t.Errorf("missing untracked file: %q", out)
	}
}

func TestListGitChangedFilesTool_NoChanges(t *testing.T) {
	tmp := gitInit(t)
	tool := &ListGitChangedFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no changed files") {
		t.Errorf("got %q", out)
	}
}

func TestListGitChangedFilesTool_SummarizesGeneratedLocalArtifacts(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "tracked.txt", "v2\n")
	writeFile(t, tmp, ".cache/go-build/00/a", "compiled\n")
	writeFile(t, tmp, ".config/go/telemetry/local/go@v1.count", "counter\n")
	writeFile(t, tmp, "go/pkg/mod/example.com/mod@v1.0.0/go.mod", "module example.com/mod\n")
	writeFile(t, tmp, "scratch.txt", "keep me\n")

	tool := &ListGitChangedFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, leaked := range []string{".cache/go-build/00/a", ".config/go/telemetry/local/go@v1.count", "go/pkg/mod/example.com/mod@v1.0.0/go.mod"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("generated artifact %q leaked into changed-file output:\n%s", leaked, out)
		}
	}
	for _, want := range []string{"tracked.txt", "scratch.txt", "omitted 3 generated local artifact file(s) under .cache/, .config/, go/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("changed-file output missing %q:\n%s", want, out)
		}
	}
}

func TestListGitChangedFilesTool_SummarizesIgnoredGeneratedLocalArtifacts(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, ".gitignore", "/.cache/\n/.config/\n")
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitRun(t, tmp, "add", ".gitignore", "tracked.txt")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, ".cache/go-build/00/a", "compiled\n")
	writeFile(t, tmp, ".config/go/telemetry/local/go@v1.count", "counter\n")

	tool := &ListGitChangedFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, ".cache/go-build/00/a") || strings.Contains(out, ".config/go/telemetry/local/go@v1.count") {
		t.Fatalf("ignored generated artifact leaked into changed files:\n%s", out)
	}
	if !strings.Contains(out, "omitted 2 generated local artifact file(s) under .cache/, .config/") {
		t.Fatalf("expected ignored artifact summary, got:\n%s", out)
	}
}

func TestGitCheckpointAndRollback(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v2\n")
	cp := &GitCheckpointTool{Cwd: NewCwdRef(tmp)}
	out, err := cp.Execute(context.Background(), `{"message":"checkpoint 1"}`)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if !strings.Contains(out, "created checkpoint") {
		t.Errorf("out = %q", out)
	}
	writeFile(t, tmp, "f.txt", "v3\n")
	rb := &RollbackTool{Cwd: NewCwdRef(tmp)}
	if _, err := rb.Execute(context.Background(), `{}`); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "f.txt"))
	if string(got) != "v1\n" {
		t.Errorf("after rollback got %q, want v1", string(got))
	}
}

func TestRunTestsTool_DefaultAndCustomCommand(t *testing.T) {
	tmp := t.TempDir()
	tool := &RunTestsTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"command":"printf ok"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=0") || !strings.Contains(out, "ok") {
		t.Errorf("out = %q", out)
	}
}

func TestRunTestsTool_ReportsFailureAsData(t *testing.T) {
	tmp := t.TempDir()
	tool := &RunTestsTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"command":"sh -c 'echo nope >&2; exit 7'"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=7") || !strings.Contains(out, "nope") {
		t.Errorf("out = %q", out)
	}
}

// TestRunTestsTool_HardlineFloor matches run_bash's hardline execution
// chokepoint: run_tests is also arbitrary shell, and sandboxed background
// dispatch workers may auto-approve it, so catastrophic commands must be
// blocked here regardless of host vs Sandbox execution.
func TestRunTestsTool_HardlineFloor(t *testing.T) {
	spy := &spySandbox{label: "[podman]"}
	tool := &RunTestsTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: spy}
	out, err := tool.Execute(context.Background(), `{"command":"true && rm -rf /"}`)
	if err != nil {
		t.Fatalf("hardline should return a recoverable result, got err: %v", err)
	}
	if !strings.Contains(out, "BLOCKED (hardline)") {
		t.Errorf("expected hardline block, got: %s", out)
	}
	if spy.callCount != 0 {
		t.Errorf("hardline command reached Sandbox.Command %d times, want 0", spy.callCount)
	}
}

// TestRunTestsTool_HardlineFloorAfterDefaultNormalization proves the default
// test command still normalizes first, then passes the hardline check as an
// ordinary safe command.
func TestRunTestsTool_HardlineFloorAfterDefaultNormalization(t *testing.T) {
	spy := &spySandbox{label: "[podman]"}
	tool := &RunTestsTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: spy}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasSuffix(spy.gotCommand, "&& go test ./...") {
		t.Errorf("Sandbox.Command got command %q, want wrapped default go test ./...", spy.gotCommand)
	}
	if strings.Contains(out, "BLOCKED (hardline)") {
		t.Errorf("default test command should not be hardline-blocked, got: %q", out)
	}
}

// Nil Sandbox must behave exactly like an explicit HostSandbox{} — same
// back-compat contract RunBashTool relies on (see exec_tool_test.go).
func TestRunTestsTool_NilSandboxDefaultsToHost(t *testing.T) {
	tool := &RunTestsTool{Cwd: NewCwdRef(t.TempDir())}
	if _, ok := tool.sandbox().(HostSandbox); !ok {
		t.Errorf("nil Sandbox should default to HostSandbox, got %T", tool.sandbox())
	}
}

// Execute must route the command and cwd through Sandbox.Command rather than
// building exec.Command inline — this is the seam's whole point, and what
// lets a background dispatch worker's run_tests calls be confined to its own
// container (see dispatchBackgroundApprovalPolicy).
func TestRunTestsTool_ExecuteRoutesThroughSandbox(t *testing.T) {
	dir := t.TempDir()
	spy := &spySandbox{label: "[podman]"}
	tool := &RunTestsTool{Cwd: NewCwdRef(dir), Sandbox: spy}
	out, err := tool.Execute(context.Background(), `{"command":"printf via-sandbox"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if spy.callCount != 1 {
		t.Errorf("Sandbox.Command called %d times, want 1", spy.callCount)
	}
	if !strings.HasSuffix(spy.gotCommand, "&& printf via-sandbox") {
		t.Errorf("Sandbox.Command got command %q, want wrapped printf command", spy.gotCommand)
	}
	if spy.gotCwd != dir {
		t.Errorf("Sandbox.Command got cwd %q, want %q", spy.gotCwd, dir)
	}
	if !strings.Contains(out, "via-sandbox") {
		t.Errorf("output missing command's stdout: %q", out)
	}
}

func TestRunTestsTool_SandboxedGoTestsUseExecutableProjectDirs(t *testing.T) {
	dir := gitInit(t)
	assertSandboxedGoTestScratch(t, dir)
}

func TestRunTestsTool_SandboxedGoTestsUseSameScratchFromManagedWorktree(t *testing.T) {
	repo := gitInit(t)
	writeFile(t, repo, "README.md", "root\n")
	gitCommit(t, repo, "initial")

	wtDir := worktree.Dir(repo, "feature")
	if err := os.MkdirAll(filepath.Dir(wtDir), 0o755); err != nil {
		t.Fatalf("mkdir worktree parent: %v", err)
	}
	if err := runGit(repo, "worktree", "add", "-q", "-b", "feature", wtDir); err != nil {
		t.Fatalf("git worktree add: %v", err)
	}
	t.Cleanup(func() { _ = runGit(repo, "worktree", "remove", "--force", wtDir) })

	assertSandboxedGoTestScratch(t, wtDir)
}

func assertSandboxedGoTestScratch(t *testing.T, cwd string) {
	t.Helper()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	spy := &spySandbox{label: "[podman-sandbox]"}
	tool := &RunTestsTool{Cwd: NewCwdRef(cwd), Sandbox: spy}
	out, err := tool.Execute(context.Background(), `{"command":"printf go-env"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	repoScratch := filepath.Join("/var/tmp", "yottacode-go", safeScratchName(cwd))
	// GOCACHE/GOMODCACHE live under a persistent, HOME-rooted directory
	// (bind-mounted by internal/sandbox.NewPodmanSandbox into every
	// container — see internal/sandboxcache), NOT the per-workspace,
	// container-ephemeral repoScratch below: a fresh container per session
	// would otherwise force a full `go mod download` plus full recompile on
	// every session's first Go command.
	goCacheRoot := filepath.Join(fakeHome, ".yottacode", sandboxcache.GoCacheHomeSubdir)
	for _, fragment := range []string{
		"mkdir -p '" + filepath.Join(repoScratch, "tmp") + "' '" + filepath.Join(goCacheRoot, "cache") + "' '" + filepath.Join(goCacheRoot, "modcache") + "' '" + filepath.Join(repoScratch, "xdg-cache") + "' '" + filepath.Join(repoScratch, "xdg-config") + "'",
		"HOME='" + repoScratch + "'",
		"XDG_CACHE_HOME='" + filepath.Join(repoScratch, "xdg-cache") + "'",
		"XDG_CONFIG_HOME='" + filepath.Join(repoScratch, "xdg-config") + "'",
		"TMPDIR='" + filepath.Join(repoScratch, "tmp") + "'",
		"GOTMPDIR='" + filepath.Join(repoScratch, "tmp") + "'",
		"GOCACHE='" + filepath.Join(goCacheRoot, "cache") + "'",
		"GOMODCACHE='" + filepath.Join(goCacheRoot, "modcache") + "'",
		"GOTELEMETRY='off'",
	} {
		if !strings.Contains(spy.gotCommand, fragment) {
			t.Fatalf("sandbox command missing %s: %q", fragment, spy.gotCommand)
		}
		if !strings.Contains(out, fragment) {
			t.Fatalf("reported command missing %s: %q", fragment, out)
		}
	}
	if !strings.Contains(spy.gotCommand, repoScratch) {
		t.Fatalf("sandbox command should use container-internal scratch storage %s, got %q", repoScratch, spy.gotCommand)
	}
	for _, forbidden := range []string{
		filepath.Join(cwd, ".yottacode", "tmp", "go"),
		filepath.Join(cwd, ".scratch"),
		filepath.Join(filepath.Dir(cwd), "."+filepath.Base(cwd)+"-go-scratch"),
	} {
		if strings.Contains(spy.gotCommand, forbidden) {
			t.Fatalf("sandbox command must not use host worktree scratch path %s: %q", forbidden, spy.gotCommand)
		}
	}

	// Regression: repo-root Go caches make `go test ./...` descend into
	// downloaded modules and fail with "outside main module" setup errors.
	for _, polluted := range []string{
		filepath.Join(cwd, ".cache"),
		filepath.Join(cwd, ".config"),
		filepath.Join(cwd, "go"),
		filepath.Join(cwd, ".yottacode", "tmp", "go"),
	} {
		if _, err := os.Stat(polluted); !os.IsNotExist(err) {
			t.Fatalf("sandboxed run_tests polluted repo root with %s", polluted)
		}
	}
}

// PreviewCall prefixes the Sandbox's label for scrollback, mirroring
// RunBashTool's convention, but only when it differs from HostSandbox's.
func TestRunTestsTool_PreviewCallLabelsNonHostSandbox(t *testing.T) {
	tool := &RunTestsTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: &spySandbox{label: "[podman]"}}
	got := tool.PreviewCall(`{}`)
	if !strings.HasPrefix(got, "[podman] ") {
		t.Errorf("PreviewCall = %q, want [podman] prefix", got)
	}
}

func TestRunTestsTool_PreviewCallOmitsHostLabel(t *testing.T) {
	tool := &RunTestsTool{Cwd: NewCwdRef(t.TempDir())}
	got := tool.PreviewCall(`{}`)
	if strings.HasPrefix(got, "[") {
		t.Errorf("PreviewCall = %q, unsandboxed preview should not carry a bracket tag", got)
	}
}

// Podman's own exit=125 convention must be disambiguated for run_tests too,
// now that it can run inside a podman-backed sandbox — otherwise a dead or
// misconfigured container looks exactly like a test suite that exited 125.
func TestRunTestsTool_Exit125AnnotatedOnlyForPodman(t *testing.T) {
	tool := &RunTestsTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: &exitCodeSandbox{label: "[podman-sandbox]", code: 125}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "NOTE: exit=125 is podman's own convention") {
		t.Errorf("expected the podman exit=125 note, got: %q", out)
	}
}

func TestRunTestsTool_Exit125NotAnnotatedForHost(t *testing.T) {
	tool := &RunTestsTool{Cwd: NewCwdRef(t.TempDir())}
	out, err := tool.Execute(context.Background(), `{"command":"exit 125"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "NOTE: exit=125") {
		t.Errorf("host execution exiting 125 is an ordinary exit code, not a podman failure — got: %q", out)
	}
}
