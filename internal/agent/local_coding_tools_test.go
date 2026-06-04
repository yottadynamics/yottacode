package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteFileTool_DeletesFile(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "hi")
	tool := &DeleteFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"x.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "deleted file") {
		t.Errorf("out = %q", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, "x.txt")); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err = %v", err)
	}
}

func TestDeleteFileTool_DeletesEmptyDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "empty"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &DeleteFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"empty"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "deleted directory") {
		t.Errorf("out = %q", out)
	}
}

func TestDeleteFileTool_RejectsMissingPath(t *testing.T) {
	tool := &DeleteFileTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error")
	}
}

func TestMoveFileTool_MovesFile(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha")
	tool := &MoveFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"src":"a.txt","dst":"nested/b.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "moved") {
		t.Errorf("out = %q", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("src should be gone, stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "nested", "b.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "alpha" {
		t.Errorf("dst content = %q", string(got))
	}
}

func TestMoveFileTool_RequiresBothPaths(t *testing.T) {
	tool := &MoveFileTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{"src":"a"}`); err == nil {
		t.Errorf("expected error")
	}
}

func TestMkdirTool_CreatesParents(t *testing.T) {
	tmp := t.TempDir()
	tool := &MkdirTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"a/b/c"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "created directory") {
		t.Errorf("out = %q", out)
	}
	if info, err := os.Stat(filepath.Join(tmp, "a", "b", "c")); err != nil || !info.IsDir() {
		t.Errorf("expected created dir, stat err=%v info=%v", err, info)
	}
}

func TestMkdirTool_RequiresPath(t *testing.T) {
	tool := &MkdirTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error")
	}
}

func TestCopyFileTool_CopiesFile(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "src.txt", "payload")
	tool := &CopyFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"src":"src.txt","dst":"dst/out.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "copied") {
		t.Errorf("out = %q", out)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "dst", "out.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("copied content = %q", string(got))
	}
	orig, _ := os.ReadFile(filepath.Join(tmp, "src.txt"))
	if string(orig) != "payload" {
		t.Errorf("src mutated: %q", string(orig))
	}
}

// TestCopyFileTool_RejectsDeniedSource is a regression for the release
// follow-up: copy_file is an auto-approved read+write, so its SOURCE must
// clear the credential read deny-list — otherwise it's a back door that
// copies ~/.ssh/id_rsa into a readable file.
func TestCopyFileTool_RejectsDeniedSource(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "secret.key", "PRIVATE KEY")
	secret := filepath.Join(tmp, "secret.key")
	tool := &CopyFileTool{
		Cwd:           NewCwdRef(tmp),
		WriteOpts:     WritePathOptions{Cwd: NewCwdRef(tmp)},
		DenyReadPaths: []string{secret},
	}
	if _, err := tool.Execute(context.Background(), `{"src":"secret.key","dst":"exfil.txt"}`); err == nil {
		t.Fatal("copy_file copied a deny-listed source")
	}
	if _, err := os.Stat(filepath.Join(tmp, "exfil.txt")); err == nil {
		t.Error("copy_file wrote the destination despite a denied source")
	}
}

func TestCopyFileTool_RejectsDirectorySource(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "dirsrc"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &CopyFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{"src":"dirsrc","dst":"out"}`); err == nil {
		t.Errorf("expected error")
	}
}

func TestReadManyFilesTool_ReadsMultipleFiles(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "b.txt", "bravo")
	writeFile(t, tmp, "a.txt", "alpha")
	tool := &ReadManyFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"paths":["b.txt","a.txt"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "==> a.txt <==") || !strings.Contains(out, "alpha") {
		t.Errorf("missing a.txt section: %q", out)
	}
	if !strings.Contains(out, "==> b.txt <==") || !strings.Contains(out, "bravo") {
		t.Errorf("missing b.txt section: %q", out)
	}
	if strings.Index(out, "==> a.txt <==") > strings.Index(out, "==> b.txt <==") {
		t.Errorf("paths should be sorted for stable output: %q", out)
	}
}

// TestReadManyFilesTool_EmptyFileInBatch is a regression for the release
// audit's read-many-files-empty-file-aborts-batch finding: io.ReadFull
// returns io.EOF for an empty file, and the old code turned that into a
// hard error that aborted the entire batch — losing every other file's
// content. An empty file must read as an empty section, like read_file.
func TestReadManyFilesTool_EmptyFileInBatch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "empty.txt", "")
	writeFile(t, tmp, "full.txt", "hello")
	tool := &ReadManyFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"paths":["empty.txt","full.txt"]}`)
	if err != nil {
		t.Fatalf("Execute aborted the batch over an empty file: %v", err)
	}
	if !strings.Contains(out, "==> empty.txt <==") {
		t.Errorf("missing empty.txt section: %q", out)
	}
	if !strings.Contains(out, "==> full.txt <==") || !strings.Contains(out, "hello") {
		t.Errorf("non-empty file dropped from batch: %q", out)
	}
}

func TestReadManyFilesTool_OffsetLimitAndTruncate(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "0123456789")
	tool := &ReadManyFilesTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"paths":["x.txt"],"offset":3,"limit":4}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "3456") {
		t.Errorf("missing sliced content: %q", out)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("expected truncation marker: %q", out)
	}
}

func TestReadManyFilesTool_RequiresPaths(t *testing.T) {
	tool := &ReadManyFilesTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error")
	}
}

func TestReadManyFilesTool_RejectsTooManyPaths(t *testing.T) {
	tool := &ReadManyFilesTool{Cwd: NewCwdRef(t.TempDir())}
	var parts []string
	for i := 0; i < defaultReadManyMaxFiles+1; i++ {
		parts = append(parts, `"f`+string(rune('a'+i))+`.txt"`)
	}
	args := `{"paths":[` + strings.Join(parts, ",") + `]}`
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Errorf("expected error")
	}
}
