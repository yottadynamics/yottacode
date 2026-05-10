package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyDiffTool_AppliesPatch(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "a.txt", "one\ntwo\n")
	tool := &ApplyDiffTool{Cwd: tmp, WriteOpts: WritePathOptions{Cwd: tmp}}
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

func TestApplyDiffTool_RequiresDiff(t *testing.T) {
	tmp := t.TempDir()
	tool := &ApplyDiffTool{Cwd: tmp, WriteOpts: WritePathOptions{Cwd: tmp}}
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
		Cwd:       tmp,
		WriteOpts: WritePathOptions{Cwd: tmp, DenyExact: DefaultDenyPaths(tmp)},
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
// the model gets a clear signal.
func TestApplyDiffTool_RefusesEmptyHeaderDiff(t *testing.T) {
	tmp := gitInit(t)
	tool := &ApplyDiffTool{Cwd: tmp, WriteOpts: WritePathOptions{Cwd: tmp}}
	args := map[string]string{"diff": "garbage with no diff headers\n"}
	b, _ := json.Marshal(args)
	if _, err := tool.Execute(context.Background(), string(b)); err == nil {
		t.Errorf("expected error for headerless diff")
	}
}

func TestListGitChangedFilesTool_FindsStagedUnstagedAndUntracked(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "tracked.txt", "v2\n")
	writeFile(t, tmp, "new.txt", "n\n")
	tool := &ListGitChangedFilesTool{Cwd: tmp}
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
	tool := &ListGitChangedFilesTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no changed files") {
		t.Errorf("got %q", out)
	}
}

func TestGitCheckpointAndRollback(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v2\n")
	cp := &GitCheckpointTool{Cwd: tmp}
	out, err := cp.Execute(context.Background(), `{"message":"checkpoint 1"}`)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if !strings.Contains(out, "created checkpoint") {
		t.Errorf("out = %q", out)
	}
	writeFile(t, tmp, "f.txt", "v3\n")
	rb := &RollbackTool{Cwd: tmp}
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
	tool := &RunTestsTool{Cwd: tmp}
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
	tool := &RunTestsTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"command":"sh -c 'echo nope >&2; exit 7'"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=7") || !strings.Contains(out, "nope") {
		t.Errorf("out = %q", out)
	}
}
