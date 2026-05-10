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
	tool := &GitBranchStatusTool{Cwd: tmp}
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
	tool := &GitShowFileAtRevTool{Cwd: tmp}
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
	tool := &GitDiffFilesTool{Cwd: tmp}
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
	stage := &GitStageFilesTool{Cwd: tmp}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}
	commit := &GitCommitTool{Cwd: tmp}
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
	unstage := &GitUnstageFilesTool{Cwd: tmp}
	if _, err := unstage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	changed := &ListGitChangedFilesTool{Cwd: tmp}
	out, err = changed.Execute(context.Background(), `{"staged":true,"unstaged":false,"untracked":false}`)
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	if strings.Contains(out, "f.txt") {
		t.Errorf("file should be unstaged: %q", out)
	}
}

func TestGitLogFileTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	tool := &GitLogFileTool{Cwd: tmp}
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
	tool := &GitBlameLinesTool{Cwd: tmp}
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
	tool := &GitMergeBaseTool{Cwd: tmp}
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
