package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/github"
)

// gitInitBare creates a bare repository in t.TempDir() so the
// push tests have a real remote to talk to without touching the
// network. Returns the absolute path of the bare repo, suitable
// for `git remote add origin <path>`.
func gitInitBare(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := t.TempDir()
	c := exec.Command("git", "init", "--bare", "-q", bare)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	return bare
}

func TestCurrentBranchOrEmpty_BranchSet(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	got, err := currentBranchOrEmpty(context.Background(), tmp)
	if err != nil {
		t.Fatalf("currentBranchOrEmpty: %v", err)
	}
	if got != "main" {
		t.Errorf("got %q; want main", got)
	}
}

func TestCurrentBranchOrEmpty_DetachedHead(t *testing.T) {
	// A detached HEAD must be reported as an empty branch name,
	// not bubbled up as an error. PushBranch's caller branches
	// on the empty-branch sentinel to populate res.Detached.
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	// Detach HEAD by checking out the commit SHA directly.
	shaOut, err := exec.Command("git", "-C", tmp, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse: %v: %s", err, shaOut)
	}
	sha := strings.TrimSpace(string(shaOut))
	if out, err := exec.Command("git", "-C", tmp, "checkout", "-q", sha).CombinedOutput(); err != nil {
		t.Fatalf("checkout sha: %v: %s", err, out)
	}

	got, err := currentBranchOrEmpty(context.Background(), tmp)
	if err != nil {
		t.Fatalf("currentBranchOrEmpty: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty (detached); got %q", got)
	}
}

func TestBranchHasUpstream_FreshRepoFalse(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	if branchHasUpstream(context.Background(), tmp, "main") {
		t.Errorf("expected no upstream on a fresh repo")
	}
}

func TestPushBranch_DetachedEarlyExit(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	shaOut, _ := exec.Command("git", "-C", tmp, "rev-parse", "HEAD").CombinedOutput()
	sha := strings.TrimSpace(string(shaOut))
	_, _ = exec.Command("git", "-C", tmp, "checkout", "-q", sha).CombinedOutput()

	res, err := PushBranch(context.Background(), tmp, nil)
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if !res.Detached {
		t.Errorf("expected Detached=true; got %+v", res)
	}
	if res.Pushed {
		t.Errorf("must not push on detached HEAD; got %+v", res)
	}
}

func TestPushBranch_NoRemoteGitError(t *testing.T) {
	// No `origin` configured → git push fails. Result envelope
	// populates GitError + GitOutput verbatim; no crash, no panic.
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	res, err := PushBranch(context.Background(), tmp, nil)
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if res.Pushed {
		t.Errorf("must not be Pushed without a remote; got %+v", res)
	}
	if res.GitError == "" {
		t.Errorf("expected GitError populated; got %+v", res)
	}
}

func TestPushBranch_HappyPathSetsUpstream(t *testing.T) {
	// Bare remote setup: a fresh push to a never-pushed branch
	// must (a) succeed and (b) flip SetUpstream=true. After the
	// push the local config should have the upstream wired so a
	// second push doesn't re-add -u.
	tmp := gitInit(t)
	bare := gitInitBare(t)
	if out, err := exec.Command("git", "-C", tmp, "remote", "add", "origin", bare).CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v: %s", err, out)
	}
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	res, err := PushBranch(context.Background(), tmp, nil)
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if !res.Pushed {
		t.Errorf("expected Pushed=true; got %+v", res)
	}
	if !res.SetUpstream {
		t.Errorf("expected SetUpstream=true on first push; got %+v", res)
	}
	if res.Branch != "main" {
		t.Errorf("expected Branch=main; got %q", res.Branch)
	}

	// Second push: upstream is now configured, so the tool
	// should NOT re-add -u.
	writeFile(t, tmp, "f.txt", "v2\n")
	gitCommit(t, tmp, "next")
	res2, err := PushBranch(context.Background(), tmp, nil)
	if err != nil {
		t.Fatalf("second PushBranch: %v", err)
	}
	if !res2.Pushed {
		t.Errorf("expected second push to succeed; got %+v", res2)
	}
	if res2.SetUpstream {
		t.Errorf("expected SetUpstream=false on second push; got %+v", res2)
	}
}

func TestPushBranch_LooksUpPRWhenClientPresent(t *testing.T) {
	// With a github.Interface configured, a successful push
	// should populate PRURL via ReadPR. ErrPRNotFound silently
	// leaves it empty (no failure surfaced).
	tmp := gitInit(t)
	bare := gitInitBare(t)
	_, _ = exec.Command("git", "-C", tmp, "remote", "add", "origin", bare).CombinedOutput()
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	gh := &fakeGH{
		readPRRes: github.PRDetails{Number: 42, URL: "https://github.com/o/r/pull/42"},
	}
	res, err := PushBranch(context.Background(), tmp, gh)
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if !res.Pushed {
		t.Errorf("expected Pushed=true; got %+v", res)
	}
	if res.PRURL != "https://github.com/o/r/pull/42" || res.PRNumber != 42 {
		t.Errorf("PR lookup not populated: %+v", res)
	}
	if gh.readPRReq.Ref != "main" {
		t.Errorf("ReadPR should be called with the branch name; got Ref=%q", gh.readPRReq.Ref)
	}
}

func TestPushBranch_PRNotFoundDoesNotFail(t *testing.T) {
	// A branch without an existing PR is the common case after
	// the first push — PRURL stays empty, no error propagates.
	tmp := gitInit(t)
	bare := gitInitBare(t)
	_, _ = exec.Command("git", "-C", tmp, "remote", "add", "origin", bare).CombinedOutput()
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	gh := &fakeGH{readPRErr: github.ErrPRNotFound}
	res, err := PushBranch(context.Background(), tmp, gh)
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if !res.Pushed {
		t.Errorf("expected Pushed=true despite no PR; got %+v", res)
	}
	if res.PRURL != "" {
		t.Errorf("expected empty PRURL on not-found; got %q", res.PRURL)
	}
}

func TestRenderPushResult_DetachedShape(t *testing.T) {
	out := renderPushResult(PushResult{Detached: true})
	if !strings.Contains(out, "pushed=false reason=detached_head") {
		t.Errorf("missing detached_head reason: %q", out)
	}
	if !strings.Contains(out, "switch to a branch") {
		t.Errorf("missing actionable hint: %q", out)
	}
}

func TestRenderPushResult_GitErrorShape(t *testing.T) {
	out := renderPushResult(PushResult{
		GitError:  "exit 1",
		GitOutput: "fatal: repository not found",
	})
	if !strings.Contains(out, "reason=git_error") {
		t.Errorf("missing git_error reason: %q", out)
	}
	if !strings.Contains(out, "repository not found") {
		t.Errorf("git output not surfaced verbatim: %q", out)
	}
	if !strings.Contains(out, "Do NOT auto-retry") {
		t.Errorf("missing no-auto-retry directive: %q", out)
	}
}

func TestRenderPushResult_PushedWithPR(t *testing.T) {
	out := renderPushResult(PushResult{
		Pushed: true, Branch: "feature/x", SetUpstream: true,
		PRURL: "https://github.com/o/r/pull/9", PRNumber: 9,
	})
	if !strings.Contains(out, "pushed=true") {
		t.Errorf("missing pushed=true: %q", out)
	}
	if !strings.Contains(out, "pr_number=9") || !strings.Contains(out, "pr_url=https://github.com/o/r/pull/9") {
		t.Errorf("missing PR fields: %q", out)
	}
}

func TestRenderPushResult_PushedNoPR(t *testing.T) {
	out := renderPushResult(PushResult{
		Pushed: true, Branch: "feature/x",
	})
	if !strings.Contains(out, "pushed=true") {
		t.Errorf("missing pushed=true: %q", out)
	}
	// When there's no PR, surface a hint pointing at /git-create-pr —
	// avoids the "I pushed; now what?" UX.
	if !strings.Contains(out, "/git-create-pr") {
		t.Errorf("expected hint pointing at /git-create-pr: %q", out)
	}
}

func TestGitPushTool_RoundsThroughTool(t *testing.T) {
	tmp := gitInit(t)
	bare := gitInitBare(t)
	_, _ = exec.Command("git", "-C", tmp, "remote", "add", "origin", bare).CombinedOutput()
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	tool := &GitPushTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "pushed=true") {
		t.Errorf("expected pushed=true in output:\n%s", out)
	}
}
