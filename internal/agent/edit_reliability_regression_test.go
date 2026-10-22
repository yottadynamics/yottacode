package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEditFileReceiptRebasesUniqueMovedTarget(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(path, []byte("prefix\ntarget\nsuffix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := "target\n"
	hash, err := hashlineReceiptForTest(old)
	if err != nil {
		t.Fatal(err)
	}
	receipt := `{"path":"x.txt","offset":7,"length":7,"hash":"` + hash + `","old":"target\n","ends_with_newline":true,"complete":true}`
	if err := os.WriteFile(path, []byte("inserted\nprefix\ntarget\nsuffix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{"path":"x.txt","new_string":"TARGET\n","receipt":`+receipt+`}`); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "inserted\nprefix\nTARGET\nsuffix\n" {
		t.Fatalf("content = %q", got)
	}
}
