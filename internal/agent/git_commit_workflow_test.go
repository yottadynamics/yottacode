package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDetectCommitStyle(t *testing.T) {
	cases := []struct {
		name     string
		subjects []string
		want     string
	}{
		{"empty falls through to plain", nil, "plain"},
		{"plain wins when nothing matches", []string{"add foo", "fix bar", "improve baz"}, "plain"},
		{"conventional majority", []string{"feat: a", "fix: b", "chore: c", "add foo"}, "conventional"},
		{"conventional with scope and bang", []string{"feat(api)!: a", "fix(ui): b", "chore: c"}, "conventional"},
		{"ticket-prefix majority", []string{"PROJ-1 a", "PROJ-2 b", "ABC-3 c", "tidy"}, "ticket-prefix"},
		// Half-and-half splits don't flip the repo — keeps a stray
		// `fix: typo` from changing every future commit's shape.
		{"split conventional vs plain stays plain", []string{"feat: a", "fix: b", "add c", "add d"}, "plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectCommitStyle(tc.subjects); got != tc.want {
				t.Errorf("detectCommitStyle(%v) = %q, want %q", tc.subjects, got, tc.want)
			}
		})
	}
}

func TestValidateCommitSubject(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string // "" means valid
	}{
		{"happy", "add cache layer", ""},
		{"trailing newline tolerated", "add cache layer\n", ""},
		{"empty", "", "message is empty"},
		{"only whitespace", "   ", "message is empty"},
		{"multi-line rejected", "add cache\n\nbody here", "message must be a single line (no body / footer)"},
		{"trailing period rejected", "add cache.", "subject must not end with a period"},
		{"over the cap rejected", strings.Repeat("x", CommitSubjectMaxLen+1), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateCommitSubject(tc.message)
			if tc.name == "over the cap rejected" {
				if !strings.HasPrefix(got, "subject is ") {
					t.Errorf("expected length error, got %q", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("validateCommitSubject(%q) = %q, want %q", tc.message, got, tc.want)
			}
		})
	}
}

func TestGitCommitContextTool_EmptyStaging(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	tool := &GitCommitContextTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "staged_empty=true") {
		t.Errorf("expected staged_empty=true in:\n%s", out)
	}
	if !strings.Contains(out, "branch=main") {
		t.Errorf("expected branch=main in:\n%s", out)
	}
}

func TestGitCommitContextTool_StagedDiffAndStyle(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "feat: initial")
	// Build a conventional-commits history so the style detector picks
	// it up. Five conventional + one plain = majority conventional.
	for _, m := range []string{"feat: a", "fix: b", "chore: c", "docs: d", "refactor: e"} {
		writeFile(t, tmp, "f.txt", m+"\n")
		gitCommit(t, tmp, m)
	}

	// Stage a change so the snapshot has a diff to render.
	writeFile(t, tmp, "f.txt", "next\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}

	tool := &GitCommitContextTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "staged_empty=false") {
		t.Errorf("expected staged_empty=false in:\n%s", out)
	}
	if !strings.Contains(out, "detected_style=conventional") {
		t.Errorf("expected detected_style=conventional in:\n%s", out)
	}
	if !strings.Contains(out, "+next") {
		t.Errorf("expected staged diff body in:\n%s", out)
	}
	// recent.subjects window includes the conventional history.
	if !strings.Contains(out, "feat: a") {
		t.Errorf("expected recent subjects in:\n%s", out)
	}
}

func TestBuildCommitContext_UntrackedListed(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "fresh.txt", "new\n")
	writeFile(t, tmp, "tracked.txt", "v2\n") // unstaged modification

	snap, err := BuildCommitContext(context.Background(), tmp)
	if err != nil {
		t.Fatalf("BuildCommitContext: %v", err)
	}
	if !slices.Contains(snap.Untracked, "fresh.txt") {
		t.Errorf("expected fresh.txt in untracked, got %v", snap.Untracked)
	}
	if !slices.Contains(snap.Unstaged, "tracked.txt") {
		t.Errorf("expected tracked.txt in unstaged, got %v", snap.Unstaged)
	}
	if !snap.StagedEmpty {
		t.Errorf("expected staged-empty with no staged changes")
	}
}

func TestBuildCommitContext_SummarizesGeneratedLocalArtifacts(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, ".cache/go-build/00/a", "compiled\n")
	writeFile(t, tmp, ".config/go/telemetry/local/go@v1.count", "counter\n")
	writeFile(t, tmp, "go/pkg/mod/example.com/mod@v1.0.0/go.mod", "module example.com/mod\n")
	writeFile(t, tmp, "scratch.txt", "keep me\n")

	snap, err := BuildCommitContext(context.Background(), tmp)
	if err != nil {
		t.Fatalf("BuildCommitContext: %v", err)
	}
	rendered := renderCommitContext(snap)
	for _, leaked := range []string{".cache/go-build/00/a", ".config/go/telemetry/local/go@v1.count", "go/pkg/mod/example.com/mod@v1.0.0/go.mod"} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("generated artifact %q leaked into commit context:\n%s", leaked, rendered)
		}
	}
	for _, want := range []string{"scratch.txt", "omitted 3 generated local artifact file(s) under .cache/, .config/, go/"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("commit context missing %q:\n%s", want, rendered)
		}
	}
}

func TestBuildCommitContext_SummarizesIgnoredGeneratedLocalArtifacts(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, ".gitignore", "/.cache/\n/.config/\n/go/\n/.local/\n")
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitRun(t, tmp, "add", ".gitignore", "tracked.txt")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, ".cache/go-build/00/a", "compiled\n")
	writeFile(t, tmp, ".config/go/telemetry/local/go@v1.count", "counter\n")
	writeFile(t, tmp, ".local/state/gh/device-id", "id\n")

	snap, err := BuildCommitContext(context.Background(), tmp)
	if err != nil {
		t.Fatalf("BuildCommitContext: %v", err)
	}
	got := strings.Join(snap.Untracked, "\n")
	if !strings.Contains(got, "omitted 3 generated local artifact file(s) under .cache/, .config/, .local/") {
		t.Fatalf("expected ignored artifact summary, got %q", got)
	}
}

func TestApplyCommit_EmptyStaging(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	res, err := ApplyCommit(context.Background(), tmp, "add nothing")
	if err != nil {
		t.Fatalf("ApplyCommit: %v", err)
	}
	if !res.StagedEmpty {
		t.Errorf("expected StagedEmpty=true; got %+v", res)
	}
	if res.Committed {
		t.Errorf("must not commit with empty staging; got %+v", res)
	}
}

func TestApplyCommit_HappyPath(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v2\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}

	res, err := ApplyCommit(context.Background(), tmp, "bump f to v2")
	if err != nil {
		t.Fatalf("ApplyCommit: %v", err)
	}
	if !res.Committed {
		t.Errorf("expected Committed=true; got %+v", res)
	}
	if len(res.SHA) < 7 {
		t.Errorf("expected SHA, got %q", res.SHA)
	}
}

func TestApplyCommit_ValidationFailsBeforeGit(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v2\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}

	res, err := ApplyCommit(context.Background(), tmp, "bump f.")
	if err != nil {
		t.Fatalf("ApplyCommit: %v", err)
	}
	if res.ValidationErr == "" {
		t.Errorf("expected validation error for trailing period; got %+v", res)
	}
	if res.Committed {
		t.Errorf("must not commit on validation error; got %+v", res)
	}
	// Critically: git was NEVER invoked — the working tree still has
	// the staged change waiting. A failed validation must not flush
	// the index. Verify staging still holds f.txt.
	out, _ := gitOutput(context.Background(), tmp, "diff", "--cached", "--name-only")
	if !strings.Contains(out, "f.txt") {
		t.Errorf("expected f.txt still staged after validation failure, got %q", out)
	}
}

func TestApplyCommit_HookFailureSurfacedNotRetried(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")

	// Install a pre-commit hook that always fails. ApplyCommit must
	// surface the hook output via HookError and NOT auto-retry / amend.
	hookDir := filepath.Join(tmp, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookPath := filepath.Join(hookDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho 'hook rejected'\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	writeFile(t, tmp, "f.txt", "v2\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}

	res, err := ApplyCommit(context.Background(), tmp, "bump f")
	if err != nil {
		t.Fatalf("ApplyCommit: %v", err)
	}
	if res.Committed {
		t.Errorf("must not be committed when hook failed; got %+v", res)
	}
	if !strings.Contains(res.HookError, "hook rejected") {
		t.Errorf("expected hook output in HookError, got %q", res.HookError)
	}
	// HEAD must still point at the base commit — hook failure aborts.
	sha, _ := gitOutput(context.Background(), tmp, "log", "-1", "--format=%s")
	if strings.TrimSpace(sha) != "base" {
		t.Errorf("expected HEAD subject still 'base'; got %q", sha)
	}
}

func TestApplyCommit_MessageWithSpecialChars(t *testing.T) {
	// Single-line subject containing characters a naive shell-out
	// would mangle: backticks, dollar signs, double-quotes. The -F -
	// stdin path handles these unchanged.
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v2\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}

	tricky := "fix `foo` regression: handle $VAR and \"quotes\""
	res, err := ApplyCommit(context.Background(), tmp, tricky)
	if err != nil {
		t.Fatalf("ApplyCommit: %v", err)
	}
	if !res.Committed {
		t.Errorf("expected commit with special chars; got %+v", res)
	}
	subj, _ := gitOutput(context.Background(), tmp, "log", "-1", "--format=%s")
	if strings.TrimSpace(subj) != tricky {
		t.Errorf("subject mangled: got %q, want %q", strings.TrimSpace(subj), tricky)
	}
}

func TestGitCommitApplyTool_RoundsThroughTool(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "f.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "f.txt", "v2\n")
	stage := &GitStageFilesTool{Cwd: NewCwdRef(tmp)}
	if _, err := stage.Execute(context.Background(), `{"paths":["f.txt"]}`); err != nil {
		t.Fatalf("stage: %v", err)
	}

	tool := &GitCommitApplyTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"message":"bump f"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "committed=true sha=") {
		t.Errorf("expected committed=true with sha, got:\n%s", out)
	}
}

func TestCapString_TrimsToNewlineBoundary(t *testing.T) {
	s := "line one\nline two\nline three is long enough to trip the cap\nmore\n"
	got, capped := capString(s, 20)
	if !capped {
		t.Fatal("expected capped=true")
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected suffix \\n on capped output, got %q", got)
	}
	if strings.Contains(got, "line three") {
		t.Errorf("expected truncation before 'line three' line, got %q", got)
	}
}
