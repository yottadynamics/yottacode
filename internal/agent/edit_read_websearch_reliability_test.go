package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteFileSerializesWithEditFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := NewCwdRef(tmp)
	wo := WritePathOptions{Cwd: cwd}
	edit := &EditFileTool{Cwd: cwd, WriteOpts: wo}
	write := &WriteFileTool{Cwd: cwd, WriteOpts: wo}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = edit.Execute(context.Background(), `{"path":"x.txt","old_string":"old\n","new_string":"edited\n"}`)
	}()
	go func() {
		defer wg.Done()
		_, _ = write.Execute(context.Background(), `{"path":"x.txt","content":"written\n"}`)
	}()
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "edited\n" && string(got) != "written\n" {
		t.Fatalf("unexpected torn result %q", got)
	}
}

func TestApplyHashlineReceiptAcceptsAbsoluteAndRelativePaths(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := hashlineReceiptForTest("old\n")
	if err != nil {
		t.Fatal(err)
	}
	tool := &ApplyHashlineTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","receipt":{"path":"` + path + `","offset":0,"length":4,"hash":"` + hash + `","old":"old\n","complete":true},"new":"new\n"}`
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileUsesCapturedPath(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "x.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"x.txt"}`)
	if err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("read_file = %q, %v", out, err)
	}
}

func TestWebSearchReportsUpstreamBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer srv.Close()
	_, err := ddgSearchWithBase(context.Background(), "test", 5, srv.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("unexpected cancellation")
	}
}
