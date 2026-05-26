package agent

import (
	"context"
	"strings"
	"testing"
)

func TestGitBranchStatusTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	tool := &GitBranchStatusTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "branch=main") {
		t.Errorf("out = %q", out)
	}
	if !strings.Contains(out, "dirty=false") {
		t.Errorf("out = %q", out)
	}
}

func TestGitShowFileAtRevTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	tool := &GitShowFileAtRevTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "v1\n" {
		t.Errorf("out = %q", out)
	}
}

func TestGitDiffFilesTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v2\n")
	tool := &GitDiffFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"paths":["f.txt"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "-v1") || !strings.Contains(out, "+v2") {
		t.Errorf("out = %q", out)
	}
}

func TestGitStageUnstageAndCommitTools(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}
	commit := &GitCommitTool{Cwd: NewCwdRef(tmp)}
	out, err := commit.Execute(context.Background(), `{"message":"add f"}`)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !strings.Contains(out, "created commit") {
		t.Errorf("out = %q", out)
	}
	writeFile(t, tmp, "f.txt", "v2\n")
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage2: %v", err)
	}
	unstage := &GitUnstageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := unstage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	changed := &ListGitChangedFilesTool{Cwd: NewCwdRef(tmp)}
	out, err = changed.Execute(context.Background(), `{"staged":true,"unstaged":false,"untracked":false}`)
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	if strings.Contains(out, "f.txt") {
		t.Errorf("file should be unstaged: %q", out)
	}
}

func TestGitStageFilesTool_AllMode(t *testing.T) {
	tmp := gitInit(t)
	// Seed an initial commit so HEAD exists and the "tracked-modified"
	// case is reachable.
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitCommit(t, tmp, "base")
	// Modify the tracked file and add a fresh untracked one. `git add -A`
	// must pick up both.
	writeFile(t, tmp, "tracked.txt", "v2\n")
	writeFile(t, tmp, "untracked.txt", "new\n")

	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := stage.Execute(context.Background(), `{"all":true}`)
	if err != nil {
		t.Fatalf("stage all: %v", err)
	}
	if !strings.Contains(out, "staged all changes") {
		t.Errorf("out = %q, want it to mention 'staged all changes'", out)
	}
	staged, err := gitOutput(context.Background(), tmp, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if !strings.Contains(staged, "tracked.txt") {
		t.Errorf("expected tracked.txt staged; got %q", staged)
	}
	if !strings.Contains(staged, "untracked.txt") {
		t.Errorf("expected untracked.txt staged; got %q", staged)
	}
}

func TestGitStageFilesTool_AllAndPathsMutuallyExclusive(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	_, err := stage.Execute(context.Background(), `{"paths":["f.txt"],"all":true}`)
	if err == nil {
		t.Fatalf("expected error when both paths and all are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v, want 'mutually exclusive'", err)
	}
}

func TestGitStageFilesTool_NeitherPathsNorAll(t *testing.T) {
	tmp := gitInit(t)
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	_, err := stage.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("expected error when neither paths nor all is set")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("err = %v, want it to mention 'required'", err)
	}
}

func TestGitCreateBranchTool_HappyPath(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	tool := &GitCreateBranchTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"name":"feat/test"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "created=true") || !strings.Contains(out, "branch=feat/test") {
		t.Errorf("out = %q", out)
	}
	cur, err := gitOutput(context.Background(), tmp, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if strings.TrimSpace(cur) != "feat/test" {
		t.Errorf("HEAD = %q, want feat/test", strings.TrimSpace(cur))
	}
}

func TestGitCreateBranchTool_FromStartPoint(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "first")
	firstSHA, err := gitOutput(context.Background(), tmp, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse first: %v", err)
	}
	firstSHA = strings.TrimSpace(firstSHA)
	writeFile(t, tmp, "f.txt", "v2\n")
	gitCommit(t, tmp, "second")

	tool := &GitCreateBranchTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"name":"feat/from-first","start_point":"`+firstSHA+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	head, err := gitOutput(context.Background(), tmp, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if strings.TrimSpace(head) != firstSHA {
		t.Errorf("HEAD = %q, want %q (first commit)", strings.TrimSpace(head), firstSHA)
	}
	branch, err := gitOutput(context.Background(), tmp, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse abbrev: %v", err)
	}
	if strings.TrimSpace(branch) != "feat/from-first" {
		t.Errorf("branch = %q, want feat/from-first", strings.TrimSpace(branch))
	}
}

func TestGitCreateBranchTool_AlreadyExists(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	tool := &GitCreateBranchTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"name":"feat/dup"}`); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Switch back to main so the second create-attempt isn't blocked by
	// "current branch already named X" before the existence check runs.
	if _, err := gitOutput(context.Background(), tmp, "switch", "main"); err != nil {
		t.Fatalf("switch main: %v", err)
	}
	headBefore, _ := gitOutput(context.Background(), tmp, "rev-parse", "--abbrev-ref", "HEAD")

	_, err := tool.Execute(context.Background(), `{"name":"feat/dup"}`)
	if err == nil {
		t.Fatalf("expected error on duplicate branch")
	}
	if !strings.Contains(err.Error(), "branch_exists") {
		t.Errorf("err = %v, want it to mention 'branch_exists'", err)
	}
	headAfter, _ := gitOutput(context.Background(), tmp, "rev-parse", "--abbrev-ref", "HEAD")
	if strings.TrimSpace(headAfter) != strings.TrimSpace(headBefore) {
		t.Errorf("HEAD moved: before=%q after=%q", strings.TrimSpace(headBefore), strings.TrimSpace(headAfter))
	}
}

func TestGitCreateBranchTool_InvalidName(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	tool := &GitCreateBranchTool{Cwd: NewCwdRef(tmp)}
	_, err := tool.Execute(context.Background(), `{"name":"foo..bar"}`)
	if err == nil {
		t.Fatalf("expected error for invalid branch name")
	}
	if !strings.Contains(err.Error(), "invalid branch name") {
		t.Errorf("err = %v, want 'invalid branch name'", err)
	}
}

func TestGitCreateBranchTool_EmptyName(t *testing.T) {
	tmp := gitInit(t)
	tool := &GitCreateBranchTool{Cwd: NewCwdRef(tmp)}
	_, err := tool.Execute(context.Background(), `{"name":""}`)
	if err == nil {
		t.Fatalf("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("err = %v, want 'name is required'", err)
	}
}

func TestGitLogFileTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	tool := &GitLogFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "base") {
		t.Errorf("out = %q", out)
	}
}

func TestGitBlameLinesTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\nv2\n")
	gitCommit(t, tmp, "base")
	tool := &GitBlameLinesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","start":1,"end":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "yotta-test") && !strings.Contains(out, "base") {
		t.Errorf("out = %q", out)
	}
}

func TestGitMergeBaseTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	tool := &GitMergeBaseTool{Cwd: NewCwdRef(tmp)}
	head, err := gitOutput(context.Background(), tmp, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	out, err := tool.Execute(context.Background(), `{"base":"HEAD","head":"HEAD"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(out) != strings.TrimSpace(head) {
		t.Errorf("out=%q head=%q", out, head)
	}
}
