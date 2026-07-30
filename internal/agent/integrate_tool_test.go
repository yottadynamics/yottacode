package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/worktree"
)

// gitListBranches returns short branch names matching a glob.
func gitListBranches(t *testing.T, repoRoot, glob string) []string {
	t.Helper()
	out, err := gitOutput(context.Background(), repoRoot, "branch", "--list", glob, "--format=%(refname:short)")
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	return nonEmptyLines(out)
}

// gitCommitFileOnBranch creates `branch` off `base`, writes `content` to
// `file`, commits, and returns to `base`. Identity comes from the repo
// config (set by dispatchTestRepo).
func gitCommitFileOnBranch(t *testing.T, repoRoot, branch, file, content, base string) {
	t.Helper()
	ctx := context.Background()
	for _, args := range [][]string{{"checkout", "-q", "-b", branch, base}} {
		if _, err := gitOutput(ctx, repoRoot, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", branch}, {"checkout", "-q", base}} {
		if _, err := gitOutput(ctx, repoRoot, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}

func TestIntegrate_Disabled(t *testing.T) {
	it := &IntegrateTool{Cwd: NewCwdRef(t.TempDir()), Enabled: false}
	out, _ := it.Execute(context.Background(), `{"branches":["x"]}`)
	if !strings.Contains(out, "experimental") || !strings.Contains(out, "--experimental dispatch") {
		t.Errorf("expected experimental gate message, got %q", out)
	}
}

func TestIntegrate_NoBranches(t *testing.T) {
	it := &IntegrateTool{Cwd: NewCwdRef(t.TempDir()), Enabled: true}
	out, _ := it.Execute(context.Background(), `{"branches":[]}`)
	if !strings.Contains(out, "at least one branch") {
		t.Errorf("got %q", out)
	}
}

// TestIntegrate_CleanMerge runs the full dispatch→integrate happy path: two
// disjoint write subtasks produce two branches, which integrate merges into
// one integration branch carrying both files.
func TestIntegrate_CleanMerge(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	_, err := d.Execute(context.Background(), `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	branches := gitListBranches(t, repoRoot, "worktree-dispatch-*")
	if len(branches) != 2 {
		t.Fatalf("expected 2 dispatch branches, got %v", branches)
	}

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	argsJSON := `{"branches":["` + branches[0] + `","` + branches[1] + `"]}`
	out, err := it.Execute(context.Background(), argsJSON)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "Integrated 2 branch") {
		t.Fatalf("expected clean integration, got:\n%s", out)
	}

	integ := gitListBranches(t, repoRoot, "dispatch-integration-*")
	if len(integ) != 1 {
		t.Fatalf("expected 1 integration branch, got %v", integ)
	}
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		content, e := gitOutput(context.Background(), repoRoot, "show", integ[0]+":"+f)
		if e != nil {
			t.Errorf("integration branch missing %s: %v", f, e)
			continue
		}
		if strings.TrimSpace(content) != "hello from "+f {
			t.Errorf("%s content = %q", f, content)
		}
	}
}

// TestIntegrate_CleansUpMergedWorktrees is the P3 regression: after a clean
// integration the merged task worktrees AND their worktree-* branches are
// reclaimed (they used to accumulate forever), while the integration branch
// still carries every merged file.
func TestIntegrate_CleansUpMergedWorktrees(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	_, err := d.Execute(ctx, `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	branches := gitListBranches(t, repoRoot, "worktree-dispatch-*")
	if len(branches) != 2 {
		t.Fatalf("expected 2 dispatch branches, got %v", branches)
	}

	// Record the source worktree dirs so we can assert they're gone.
	srcDirs := map[string]string{}
	infos, _ := worktree.List(ctx, repoRoot)
	for _, w := range infos {
		if strings.HasPrefix(w.Branch, "worktree-dispatch-") {
			srcDirs[w.Branch] = w.Path
		}
	}
	if len(srcDirs) != 2 {
		t.Fatalf("expected 2 source worktrees, got %v", srcDirs)
	}

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(ctx, `{"branches":["`+branches[0]+`","`+branches[1]+`"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "Reclaimed 2 task worktree") {
		t.Errorf("expected the cleanup note, got:\n%s", out)
	}
	// Source branches gone…
	if remain := gitListBranches(t, repoRoot, "worktree-dispatch-*"); len(remain) != 0 {
		t.Errorf("merged source branches should be deleted, still present: %v", remain)
	}
	// …and so are their worktree dirs.
	for br, dir := range srcDirs {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("worktree dir for %s should be removed: %s", br, dir)
		}
	}
	// The integration branch still carries both files.
	integ := gitListBranches(t, repoRoot, "dispatch-integration-*")
	if len(integ) != 1 {
		t.Fatalf("expected 1 integration branch, got %v", integ)
	}
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		if _, e := gitOutput(ctx, repoRoot, "show", integ[0]+":"+f); e != nil {
			t.Errorf("integration branch missing %s after cleanup: %v", f, e)
		}
	}
}

func TestIntegrate_KeepsDirtyMergedWorktree(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	branch := "worktree-dispatch-dirty"
	srcDir := worktree.Dir(repoRoot, "dispatch-dirty")
	if _, err := gitOutput(ctx, repoRoot, "worktree", "add", "-b", branch, srcDir, "HEAD"); err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "alpha.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(ctx, srcDir, "add", "alpha.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(ctx, srcDir, "commit", "-q", "-m", "dispatch dirty"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "scratch.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(ctx, `{"branches":["`+branch+`"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "Kept 1 task worktree") {
		t.Fatalf("expected dirty worktree keep note, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(srcDir, "scratch.txt")); err != nil {
		t.Fatalf("dirty source worktree should be kept: %v", err)
	}
}

func TestIntegrate_KeepsNonDispatchMergedWorktree(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	normalDir := worktree.Dir(repoRoot, "normal-linked")
	if _, err := gitOutput(ctx, repoRoot, "worktree", "add", "-b", "normal-feature", normalDir, "HEAD"); err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(normalDir, "normal.txt"), []byte("normal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(ctx, normalDir, "add", "normal.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(ctx, normalDir, "commit", "-q", "-m", "normal feature"); err != nil {
		t.Fatal(err)
	}

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(ctx, `{"branches":["normal-feature"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "Kept 1 task worktree") {
		t.Fatalf("expected non-dispatch keep note, got:\n%s", out)
	}
	if _, err := os.Stat(normalDir); err != nil {
		t.Fatalf("normal worktree should be kept: %v", err)
	}
}

func TestIntegrate_FinalConflictResumeWithEmptyBranches(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	gitCommitFileOnBranch(t, repoRoot, "feat-x", "base.txt", "x change\n", "main")
	gitCommitFileOnBranch(t, repoRoot, "feat-y", "base.txt", "y change\n", "main")

	const integBranch = "dispatch-integration-resume"
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(ctx, `{"branches":["feat-x","feat-y"],"integration_branch":"`+integBranch+`"}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "branches=[]") || !strings.Contains(out, "finalize") {
		t.Fatalf("conflict guidance should describe finalizing with empty branches, got:\n%s", out)
	}

	integDir := worktree.Dir(repoRoot, integBranch)
	if err := os.WriteFile(filepath.Join(integDir, "base.txt"), []byte("resolved change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "base.txt"}, {"commit", "-q", "-m", "resolve final conflict"}} {
		if _, err := gitOutput(ctx, integDir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	out, err = it.Execute(ctx, `{"branches":[],"integration_branch":"`+integBranch+`"}`)
	if err != nil {
		t.Fatalf("resume integrate: %v", err)
	}
	if !strings.Contains(out, "Integration branch "+integBranch+" is up to date") || !strings.Contains(out, "Open a PR") {
		t.Fatalf("expected finalized integration status, got:\n%s", out)
	}
}

func TestIntegrate_EmptyBranchesRequiresExistingIntegrationWorktree(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":[],"integration_branch":"dispatch-integration-mistyped"}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "does not exist") || !strings.Contains(out, "branches=[]") {
		t.Fatalf("expected missing-worktree error for empty branch finalize, got:\n%s", out)
	}
}

func TestIntegrate_RejectsTagThatIsNotLocalBranch(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	if _, err := gitOutput(ctx, repoRoot, "tag", "dispatch-tag"); err != nil {
		t.Fatalf("create tag: %v", err)
	}
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":["dispatch-tag"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "does not exist") || !strings.Contains(out, "dispatch-tag") {
		t.Fatalf("expected tag to be rejected as a missing local branch, got:\n%s", out)
	}
}

func TestIntegrate_AmbiguousTagDoesNotHideLocalBranch(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	branch := "ambiguous"
	gitCommitFileOnBranch(t, repoRoot, branch, "ambiguous.txt", "branch change\n", "main")
	if _, err := gitOutput(ctx, repoRoot, "tag", branch, "main"); err != nil {
		t.Fatalf("create ambiguous tag: %v", err)
	}
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":["`+branch+`"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "Integrated 1 branch") {
		t.Fatalf("expected local branch to be integrated despite same-named tag, got:\n%s", out)
	}
}

func TestIntegrate_RejectsRevisionSuffixAsBranchName(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	branch := "worktree-dispatch-suffix"
	gitCommitFileOnBranch(t, repoRoot, branch, "suffix.txt", "branch change\n", "main")
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":["`+branch+`~0"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "invalid branch") || !strings.Contains(out, "revision expression") {
		t.Fatalf("expected revision suffix branch expression to be rejected, got:\n%s", out)
	}
}

func TestIntegrate_ConflictResumeCleansMergedDispatchWorktrees(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	branchA := "worktree-dispatch-conflict-a"
	branchB := "worktree-dispatch-conflict-b"
	dirA := worktree.Dir(repoRoot, "dispatch-conflict-a")
	dirB := worktree.Dir(repoRoot, "dispatch-conflict-b")
	for _, setup := range []struct {
		branch  string
		dir     string
		content string
	}{
		{branchA, dirA, "a change\n"},
		{branchB, dirB, "b change\n"},
	} {
		if _, err := gitOutput(ctx, repoRoot, "worktree", "add", "-b", setup.branch, setup.dir, "HEAD"); err != nil {
			t.Fatalf("setup worktree %s: %v", setup.branch, err)
		}
		if err := os.WriteFile(filepath.Join(setup.dir, "base.txt"), []byte(setup.content), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "base.txt"}, {"commit", "-q", "-m", setup.branch}} {
			if _, err := gitOutput(ctx, setup.dir, args...); err != nil {
				t.Fatalf("git %v in %s: %v", args, setup.branch, err)
			}
		}
	}

	const integBranch = "dispatch-integration-cleanup-resume"
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(ctx, `{"branches":["`+branchA+`","`+branchB+`"],"integration_branch":"`+integBranch+`"}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "CONFLICT") {
		t.Fatalf("expected conflict, got:\n%s", out)
	}
	integDir := worktree.Dir(repoRoot, integBranch)
	if err := os.WriteFile(filepath.Join(integDir, "base.txt"), []byte("resolved change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "base.txt"}, {"commit", "-q", "-m", "resolve conflict"}} {
		if _, err := gitOutput(ctx, integDir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	out, err = it.Execute(ctx, `{"branches":[],"integration_branch":"`+integBranch+`"}`)
	if err != nil {
		t.Fatalf("resume integrate: %v", err)
	}
	if !strings.Contains(out, "Reclaimed 2 task worktree") {
		t.Fatalf("expected resumed cleanup to reclaim both dispatch worktrees, got:\n%s", out)
	}
	for _, dir := range []string{dirA, dirB} {
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("expected source worktree %s to be removed", dir)
		}
	}
}

func TestIntegrate_MissingNonDispatchBranchErrors(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":["normal-feature-typo"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "does not exist") || !strings.Contains(out, "normal-feature-typo") {
		t.Fatalf("expected missing branch error, got:\n%s", out)
	}
}

func TestIntegrate_MissingDispatchBranchErrors(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":["worktree-dispatch-typo-1"]}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "does not exist") || !strings.Contains(out, "worktree-dispatch-typo-1") {
		t.Fatalf("expected missing dispatch branch error, got:\n%s", out)
	}
}

func TestIntegrate_MissingBranchDoesNotCreateIntegrationWorktree(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	const integBranch = "dispatch-integration-typo"
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":["worktree-dispatch-typo-1"],"integration_branch":"`+integBranch+`"}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "does not exist") {
		t.Fatalf("expected missing branch error, got:\n%s", out)
	}
	if _, err := os.Stat(worktree.Dir(repoRoot, integBranch)); !os.IsNotExist(err) {
		t.Fatalf("missing branch validation should not create integration worktree, stat err=%v", err)
	}
}

func TestIntegrate_EmptyBranchDoesNotCreateIntegrationWorktree(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	const integBranch = "dispatch-integration-empty-branch"
	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}

	out, err := it.Execute(ctx, `{"branches":[""],"integration_branch":"`+integBranch+`"}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "empty branch name") {
		t.Fatalf("expected empty branch validation error, got:\n%s", out)
	}
	if _, err := os.Stat(worktree.Dir(repoRoot, integBranch)); !os.IsNotExist(err) {
		t.Fatalf("empty branch validation should not create integration worktree, stat err=%v", err)
	}
}

func TestIntegrate_FinalizeRejectsWrongBranchWorktree(t *testing.T) {
	ctx := context.Background()
	repoRoot := dispatchTestRepo(t)
	const integBranch = "dispatch-integration-finalize"
	integDir := worktree.Dir(repoRoot, integBranch)
	if _, err := gitOutput(ctx, repoRoot, "worktree", "add", "-b", "stale-other", integDir, "HEAD"); err != nil {
		t.Fatalf("setup wrong-branch worktree: %v", err)
	}

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(ctx, `{"branches":[],"integration_branch":"`+integBranch+`"}`)
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if !strings.Contains(out, "stale-other") || !strings.Contains(out, "not the integration branch") {
		t.Fatalf("expected wrong-branch finalize rejection, got:\n%s", out)
	}
}
