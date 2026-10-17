package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyHashlinePerHunkReceipt(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := hashlineReceiptForTest("beta\n")
	if err != nil {
		t.Fatal(err)
	}
	args := `{"path":"x.txt","hunks":[{"receipt":{"path":"x.txt","offset":6,"length":5,"hash":"` + hash + `","old":"beta\n","complete":true},"new":"BETA\n"}]}`
	tool := &ApplyHashlineTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha\nBETA\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditFileReceiptRejectsAmbiguousRebase(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(path, []byte("target\nother\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, _ := hashlineReceiptForTest("target\n")
	if err := os.WriteFile(path, []byte("inserted\ntarget\ninserted\ntarget\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","new_string":"TARGET\n","receipt":{"path":"x.txt","offset":0,"length":7,"hash":"` + hash + `","old":"target\n","complete":true}}`
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected ambiguous receipt failure")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "inserted\ntarget\ninserted\ntarget\n" {
		t.Fatalf("file changed: %q", got)
	}
}
