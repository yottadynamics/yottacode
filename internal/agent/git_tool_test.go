package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// gitInit bootstraps a usable git repo in t.TempDir() with a deterministic
// identity so commits don't depend on global git config.
func gitInit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "yotta-test@example.com"},
		{"config", "user.name", "yotta-test"},
		{"config", "commit.gpgsign", "false"},
	} {
		c := exec.Command("git", args...)
		c.Dir = tmp
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return tmp
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", msg},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestGit_StatusInRepo(t *testing.T) {
	tmp := gitInit(t)
	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"args":["status"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=0") {
		t.Errorf("expected exit=0: %q", out)
	}
	if !strings.Contains(out, "branch main") && !strings.Contains(out, "On branch") {
		t.Errorf("expected branch info in: %q", out)
	}
}

func TestGit_StatusOutsideRepoReportsError(t *testing.T) {
	tmp := t.TempDir()
	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"args":["status"]}`)
	if err != nil {
		t.Fatalf("Execute (should not error on non-repo, only report exit): %v", err)
	}
	if strings.Contains(out, "exit=0") {
		t.Errorf("expected non-zero exit outside a repo: %q", out)
	}
	if !strings.Contains(out, "not a git repository") {
		t.Errorf("expected 'not a git repository' in stderr: %q", out)
	}
}

func TestGit_LogAfterCommit(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1")
	gitCommit(t, tmp, "first commit")

	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"args":["log","--oneline"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "first commit") {
		t.Errorf("expected commit message in log: %q", out)
	}
}

func TestGit_DiffShowsModification(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "add f")
	writeFile(t, tmp, "f.txt", "v2\n")

	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"args":["diff"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "-v1") || !strings.Contains(out, "+v2") {
		t.Errorf("expected diff markers in: %q", out)
	}
}

func TestGit_RequiresApproval(t *testing.T) {
	tool := &GitTool{}
	cases := []struct {
		argsJSON string
		want     bool
		desc     string
	}{
		{`{"args":["status"]}`, false, "status auto-executes"},
		{`{"args":["log","-n","5"]}`, false, "log auto-executes"},
		{`{"args":["diff","--cached"]}`, false, "diff auto-executes"},
		{`{"args":["show","HEAD"]}`, false, "show auto-executes"},
		{`{"args":["commit","-m","x"]}`, true, "commit prompts"},
		{`{"args":["push"]}`, true, "push prompts"},
		{`{"args":["push","--force"]}`, true, "destructive push prompts"},
		{`{"args":["branch","new"]}`, true, "branch w/ args prompts"},
		{`{"args":["checkout","main"]}`, true, "checkout prompts"},
		{`{"args":["reset","--hard"]}`, true, "reset prompts"},
		{`{"args":[]}`, true, "empty args prompts (safe default)"},
		{`malformed`, true, "malformed prompts (safe default)"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := tool.RequiresApproval(c.argsJSON); got != c.want {
				t.Errorf("RequiresApproval(%s) = %v, want %v", c.argsJSON, got, c.want)
			}
		})
	}
}

func TestGit_PreviewSurfacesDestructiveFlags(t *testing.T) {
	tool := &GitTool{}
	out := tool.PreviewCall(`{"args":["push","--force","origin","main"]}`)
	if !strings.Contains(out, "DESTRUCTIVE") {
		t.Errorf("preview missed destructive flag: %q", out)
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("preview missed --force annotation: %q", out)
	}
}

func TestGit_PreviewBenignCommand(t *testing.T) {
	tool := &GitTool{}
	out := tool.PreviewCall(`{"args":["status"]}`)
	if strings.Contains(out, "DESTRUCTIVE") {
		t.Errorf("benign command flagged destructive: %q", out)
	}
	if !strings.Contains(out, "git status") {
		t.Errorf("preview = %q, want 'git status'", out)
	}
}

func TestGit_BadJSON(t *testing.T) {
	tool := &GitTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Errorf("expected error on bad JSON")
	}
}

func TestGit_EmptyArgs(t *testing.T) {
	tool := &GitTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{"args":[]}`); err == nil {
		t.Errorf("expected error on empty args")
	}
}

func TestGit_NoShellInterpretation(t *testing.T) {
	// If args were going through /bin/sh -c, the `; touch SENTINEL` payload
	// would create a file as a side effect. With argv-style exec, git just
	// sees a literal ref token, errors out, and the file never appears.
	tmp := gitInit(t)
	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	payload := "; touch INJECTION_PROOF"
	out, err := tool.Execute(context.Background(), `{"args":["log","`+payload+`"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "exit=0") {
		t.Errorf("git accepted the bogus arg as valid? %q", out)
	}
	// stderr should echo the arg back (proof git saw it as one token).
	if !strings.Contains(out, payload) {
		t.Errorf("expected the literal payload to appear in git's error: %q", out)
	}
	// And the side-effect file from a real injection must not exist.
	if _, statErr := exec.Command("ls", tmp+"/INJECTION_PROOF").CombinedOutput(); statErr == nil {
		t.Errorf("INJECTION_PROOF file appeared — shell interpretation happened")
	}
}
