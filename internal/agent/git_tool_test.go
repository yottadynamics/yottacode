package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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

func TestGit_AcceptsStringArgs(t *testing.T) {
	tmp := gitInit(t)
	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"args":"git status --short"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "$ git status --short") || !strings.Contains(out, "exit=0") {
		t.Errorf("string args were not parsed as git argv: %q", out)
	}
}

func TestGit_StringArgsPreserveQuotedValues(t *testing.T) {
	args, err := parseGitArgs(`{"args":"commit -m \"fix bug\""}`)
	if err != nil {
		t.Fatalf("parseGitArgs: %v", err)
	}
	want := []string{"commit", "-m", "fix bug"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestGit_StringArgsRejectShellOperators(t *testing.T) {
	tool := &GitTool{Cwd: NewCwdRef(t.TempDir())}
	_, err := tool.Execute(context.Background(), `{"args":"status; touch NOPE"}`)
	if err == nil {
		t.Fatalf("expected shell operator rejection")
	}
	if !strings.Contains(err.Error(), "shell control operator") {
		t.Fatalf("err = %v, want shell control operator", err)
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

		// Flag-aware read-only tier: listings auto-execute…
		{`{"args":["branch"]}`, false, "bare branch lists"},
		{`{"args":["branch","--show-current"]}`, false, "branch --show-current reads"},
		{`{"args":["branch","--list"]}`, false, "branch --list reads"},
		{`{"args":["branch","--list","feat/*"]}`, false, "branch --list with pattern reads"},
		{`{"args":["branch","-vv"]}`, false, "branch -vv lists verbose"},
		{`{"args":["branch","-a","--sort=-committerdate"]}`, false, "branch -a sorted lists"},
		{`{"args":["branch","--contains","HEAD~3"]}`, false, "branch --contains reads"},
		{`{"args":["tag"]}`, false, "bare tag lists"},
		{`{"args":["tag","-l","v1.*"]}`, false, "tag -l with pattern reads"},
		{`{"args":["tag","-n5"]}`, false, "tag -n5 lists with annotations"},
		{`{"args":["remote"]}`, false, "bare remote lists"},
		{`{"args":["remote","-v"]}`, false, "remote -v reads"},
		{`{"args":["remote","get-url","origin"]}`, false, "remote get-url reads"},
		{`{"args":["stash","list"]}`, false, "stash list reads"},
		{`{"args":["stash","show","-p","stash@{0}"]}`, false, "stash show reads"},
		{`{"args":["reflog"]}`, false, "bare reflog reads"},
		{`{"args":["reflog","show"]}`, false, "reflog show reads"},

		// …while the mutating spellings of the same subcommands still prompt.
		{`{"args":["branch","-d","old"]}`, true, "branch -d prompts"},
		{`{"args":["branch","-D","old"]}`, true, "branch -D prompts"},
		{`{"args":["branch","-m","a","b"]}`, true, "branch rename prompts"},
		{`{"args":["branch","--set-upstream-to=origin/main"]}`, true, "branch upstream change prompts"},
		{`{"args":["tag","v1.0"]}`, true, "tag creation prompts"},
		{`{"args":["tag","-d","v1.0"]}`, true, "tag deletion prompts"},
		{`{"args":["tag","-a","v1.0","-m","x"]}`, true, "annotated tag creation prompts"},
		{`{"args":["remote","add","origin","u"]}`, true, "remote add prompts"},
		{`{"args":["remote","show","origin"]}`, true, "remote show (network) prompts"},
		{`{"args":["stash"]}`, true, "bare stash is a push — prompts"},
		{`{"args":["stash","pop"]}`, true, "stash pop prompts"},
		{`{"args":["stash","drop"]}`, true, "stash drop prompts"},
		{`{"args":["reflog","expire","--expire=now","--all"]}`, true, "reflog expire prompts"},
		{`{"args":["reflog","delete","HEAD@{1}"]}`, true, "reflog delete prompts"},

		// Guards that close auto-exec side doors.
		{`{"args":["diff","--output=/tmp/x"]}`, true, "diff --output writes a file — prompts"},
		{`{"args":["log","--output","x"]}`, true, "log --output writes a file — prompts"},
		{`{"args":["stash","list","--output=x"]}`, true, "stash list --output prompts"},
		{`{"args":["-c","core.pager=evil","status"]}`, true, "global -c flag prompts"},
		{`{"args":["-C","/elsewhere","status"]}`, true, "global -C flag prompts"},
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
	// push --force is in the classified high-risk set now — it gets the
	// specific reason line, which is a strictly stronger warning.
	out := tool.PreviewCall(`{"args":["push","--force","origin","main"]}`)
	if !strings.Contains(out, "HIGH RISK") || !strings.Contains(out, "rewrites remote history") {
		t.Errorf("force-push should carry the high-risk reason: %q", out)
	}
	// A dangerous flag outside the classified set still falls back to
	// the generic flag-highlight line.
	out = tool.PreviewCall(`{"args":["fetch","--prune"]}`)
	if !strings.Contains(out, "DESTRUCTIVE") || !strings.Contains(out, "--prune") {
		t.Errorf("unclassified dangerous flag should keep the flag-list warning: %q", out)
	}
}

// TestGit_PreviewHighRiskReasons pins the per-invocation classification:
// history-rewriting / worktree-destructive commands carry a specific
// "what this destroys" line; ordinary mutations carry no warning at all.
func TestGit_PreviewHighRiskReasons(t *testing.T) {
	tool := &GitTool{}
	high := []struct{ argsJSON, wantFrag string }{
		{`{"args":["reset","--hard","HEAD~1"]}`, "discards every uncommitted change"},
		{`{"args":["clean","-fd"]}`, "deletes untracked files"},
		{`{"args":["clean","--force"]}`, "deletes untracked files"},
		{`{"args":["rebase","-i","main"]}`, "rewrites commit history"},
		{`{"args":["filter-branch","--all"]}`, "rewrites commit history"},
		{`{"args":["checkout","--","."]}`, "overwrites working-tree files"},
		{`{"args":["checkout","-f","main"]}`, "discards local changes"},
		{`{"args":["switch","--discard-changes","main"]}`, "discards local changes"},
		{`{"args":["restore","main.go"]}`, "overwrites uncommitted file contents"},
		{`{"args":["restore","--staged","--worktree","main.go"]}`, "overwrites uncommitted file contents"},
		{`{"args":["branch","-D","old"]}`, "force-deletes a branch"},
		{`{"args":["tag","-d","v1.0"]}`, "deletes tag"},
		{`{"args":["push","--force-with-lease"]}`, "rewrites remote history"},
		{`{"args":["push","origin",":dead-branch"]}`, "deletes a remote ref"},
		{`{"args":["reflog","expire","--expire=now"]}`, "destroys reflog"},
	}
	for _, c := range high {
		out := tool.PreviewCall(c.argsJSON)
		if !strings.Contains(out, "HIGH RISK") || !strings.Contains(out, c.wantFrag) {
			t.Errorf("PreviewCall(%s) = %q, want HIGH RISK + %q", c.argsJSON, out, c.wantFrag)
		}
	}
	ordinary := []string{
		`{"args":["add","-A"]}`,
		`{"args":["commit","-m","x"]}`,
		`{"args":["push"]}`,
		`{"args":["restore","--staged","main.go"]}`,
		`{"args":["branch","-d","merged-branch"]}`,
		`{"args":["checkout","main"]}`,
		`{"args":["merge","feature/x"]}`,
	}
	for _, argsJSON := range ordinary {
		out := tool.PreviewCall(argsJSON)
		if strings.Contains(out, "HIGH RISK") {
			t.Errorf("ordinary mutation flagged high-risk: PreviewCall(%s) = %q", argsJSON, out)
		}
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

// TestGit_AmendWithoutMessage_NeverLaunchesRealEditor reproduces a real
// production incident: an agent resolving merge conflicts ran a git
// command that needed a commit message (a bare commit, or a
// rebase/merge/cherry-pick --continue that resolves to one) with no -m
// supplied, git launched $EDITOR, and the resulting process hung for
// over 20 minutes — unresponsive even to Ctrl+C, because the orphaned
// editor survived exec.CommandContext's default (single-PID) Cancel
// and kept git's stdout/stderr pipe open, which Cmd.Wait() blocks on
// indefinitely (see hardenGitCmd's doc comment in
// git_workflow_tools.go). hardenGitCmd's GIT_EDITOR=true prevents this
// at the source: git never launches a real editor at all.
//
// The test deliberately sets GIT_EDITOR=false in its own environment
// before calling Execute — "false" is a real, always-available command
// that exits nonzero, so if hardenGitCmd's own GIT_EDITOR=true didn't
// actually override it, the amend would fail with a "problem with the
// editor" error. This makes the test deterministic regardless of
// whatever the ambient shell already has set (this dev sandbox, for
// instance, already exports GIT_EDITOR=true globally, which would
// otherwise mask the absence of the fix).
//
// `git commit --amend` with no -m is the deterministic way to exercise
// this: it opens with the EXISTING (non-empty) commit message
// pre-filled, so GIT_EDITOR=true's "accept the buffer as-is" behavior
// produces a real, successful amend rather than aborting on an empty
// message.
func TestGit_AmendWithoutMessage_NeverLaunchesRealEditor(t *testing.T) {
	tmp := gitInit(t)
	writeTestFile(t, tmp, "a.txt", "hello")
	gitCommit(t, tmp, "initial commit")
	t.Setenv("GIT_EDITOR", "false")

	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tool.Execute(ctx, `{"args":["commit","--amend"]}`)
	if ctx.Err() != nil {
		t.Fatalf("hit the test's own 5s timeout — the amend blocked waiting on an editor")
	}
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=0") {
		t.Fatalf("expected the amend to succeed (hardenGitCmd's GIT_EDITOR=true should override the sabotaged GIT_EDITOR=false in the ambient env); got %q", out)
	}

	logCmd := exec.Command("git", "log", "-1", "--format=%s")
	logCmd.Dir = tmp
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v: %s", err, logOut)
	}
	if got := strings.TrimSpace(string(logOut)); got != "initial commit" {
		t.Errorf("amended commit message = %q, want unchanged %q", got, "initial commit")
	}
}

// TestGit_InteractiveRebase_WarnsToDoListWasNotEdited covers the
// correctness tradeoff hardenGitCmd's GIT_SEQUENCE_EDITOR=true makes:
// `rebase -i` completes instantly (no hang) instead of opening a real
// editor, but the todo list is accepted unchanged — nothing reorders,
// squashes, or drops. Without the warning note GitTool.Execute appends
// for this case, a model asked to "clean up history" via interactive
// rebase would see exit=0 and could report success despite no actual
// restructuring having happened.
func TestGit_InteractiveRebase_WarnsToDoListWasNotEdited(t *testing.T) {
	tmp := gitInit(t)
	writeTestFile(t, tmp, "a.txt", "one")
	gitCommit(t, tmp, "commit 1")
	writeTestFile(t, tmp, "b.txt", "two")
	gitCommit(t, tmp, "commit 2")

	tool := &GitTool{Cwd: NewCwdRef(tmp)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tool.Execute(ctx, `{"args":["rebase","-i","HEAD~1"]}`)
	if ctx.Err() != nil {
		t.Fatalf("hit the test's own 5s timeout — the interactive rebase blocked waiting on an editor")
	}
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=0") {
		t.Fatalf("expected the rebase to succeed; got %q", out)
	}
	if !strings.Contains(out, "interactive rebase editing is disabled") {
		t.Errorf("expected the interactive-rebase warning note in the output; got %q", out)
	}

	logCmd := exec.Command("git", "log", "--format=%s")
	logCmd.Dir = tmp
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v: %s", err, logOut)
	}
	want := "commit 2\ncommit 1\n"
	if got := string(logOut); got != want {
		t.Errorf("history was reordered despite the warning claiming otherwise: got %q, want %q", got, want)
	}

	// A plain, non-interactive rebase must NOT carry the warning — it
	// never touches GIT_SEQUENCE_EDITOR at all.
	out2, err := tool.Execute(context.Background(), `{"args":["rebase","main"]}`)
	if err != nil {
		t.Fatalf("Execute (plain rebase): %v", err)
	}
	if strings.Contains(out2, "interactive rebase editing is disabled") {
		t.Errorf("plain (non-interactive) rebase must not carry the interactive-rebase warning; got %q", out2)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestHostSandbox_Command_BoundsWaitOnSurvivingGrandchild reproduces
// the general (non-git) shape of the same hang class hardenGitCmd
// fixes for git specifically: a command that forks a background child
// inheriting its stdout/stderr PIPES, then the parent process exits —
// exactly what an orphaned $EDITOR, a stray backgrounded server, or
// any other forked-and-abandoned process does. Without WaitDelay,
// Cmd.Run() blocks until that grandchild ALSO exits (here, ~999s),
// even though the parent shell itself returned almost instantly.
//
// Stdout/Stderr must be set to something other than *os.File (a
// bytes.Buffer, matching every real caller's cappedWriter) for this to
// reproduce anything at all: exec.Cmd only allocates the OS pipe (and
// therefore only has anything for a surviving child to hold open) when
// the target isn't already a plain file descriptor.
func TestHostSandbox_Command_BoundsWaitOnSurvivingGrandchild(t *testing.T) {
	// hostExecKillTimeout is a var precisely so this test doesn't have
	// to pay the real 5s production timeout on every run — restored
	// automatically via t.Cleanup.
	orig := hostExecKillTimeout
	hostExecKillTimeout = 300 * time.Millisecond
	t.Cleanup(func() { hostExecKillTimeout = orig })

	sb := HostSandbox{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := sb.Command(ctx, "sleep 999 & exit 0", t.TempDir())
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	_ = cmd.Run()
	elapsed := time.Since(start)
	if elapsed > hostExecKillTimeout+3*time.Second {
		t.Fatalf("Run() took %s — WaitDelay did not bound the wait on the surviving grandchild's held pipe (want at most ~%s)", elapsed, hostExecKillTimeout)
	}
}
