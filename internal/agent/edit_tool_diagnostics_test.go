package agent

import (
	"context"
	"strings"
	"testing"
)

func runEdit(t *testing.T, body, args string) (string, error) {
	t.Helper()
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", body)
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	return tool.Execute(context.Background(), args)
}

func TestEditFileAmbiguousErrorListsMatchLines(t *testing.T) {
	_, err := runEdit(t, "x\nfoo()\ny\nfoo()\nz\nfoo()\n", `{"path":"f.txt","old_string":"foo()","new_string":"bar()"}`)
	if err == nil || !strings.Contains(err.Error(), "appears 3 times") || !strings.Contains(err.Error(), "lines 2, 4, 6") {
		t.Fatalf("err = %v", err)
	}
	// Classifier must still see this as a stale/ambiguous edit failure.
	if got := ClassifyEditFailure("edit_file", "error: "+err.Error()); got != EditFailureStale {
		t.Errorf("classified as %v", got)
	}
}

func TestMissingPathErrorsNameTheCwd(t *testing.T) {
	tmp := t.TempDir()
	cwd := NewCwdRef(tmp)
	_, err := (&EditFileTool{Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}).Execute(context.Background(), `{"path":"nope/a.go","old_string":"x","new_string":"y"}`)
	if err == nil || !strings.Contains(err.Error(), "no such file") || !strings.Contains(err.Error(), "resolved against cwd "+tmp) {
		t.Errorf("edit_file err = %v", err)
	}
	_, err = (&ReadFileTool{Cwd: cwd}).Execute(context.Background(), `{"path":"nope/a.go"}`)
	if err == nil || !strings.Contains(err.Error(), "no such file") || !strings.Contains(err.Error(), "resolved against cwd "+tmp) {
		t.Errorf("read_file err = %v", err)
	}
}

func TestEditFileAlreadyAppliedIsNamed(t *testing.T) {
	_, err := runEdit(t, "keep\nreturn computeTotal(a, b)\nkeep\n", `{"path":"f.txt","old_string":"return compute(a, b)","new_string":"return computeTotal(a, b)"}`)
	if err == nil || !strings.Contains(err.Error(), "already present") || !strings.Contains(err.Error(), "old_string not found") {
		t.Fatalf("err = %v", err)
	}
	if got := ClassifyEditFailure("edit_file", "error: "+err.Error()); got != EditFailureStale {
		t.Errorf("classified as %v", got)
	}
}

// A tiny new_string such as "}" is present in nearly every file, so it must
// not be reported as "already applied".
func TestEditFileShortNewStringIsNotClaimedAsApplied(t *testing.T) {
	_, err := runEdit(t, "a\n}\nb\n", `{"path":"f.txt","old_string":"zzz","new_string":"}"}`)
	if err == nil || strings.Contains(err.Error(), "already present") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditFileNotFoundShowsCurrentText(t *testing.T) {
	_, err := runEdit(t, "func run() {\n\treturn compute(a, b)\n}\n", `{"path":"f.txt","old_string":"\treturn compute(a, c)","new_string":"\treturn compute(a, d)"}`)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"old_string not found", "closest line is 2", "Current text there:", "return compute(a, b)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}
