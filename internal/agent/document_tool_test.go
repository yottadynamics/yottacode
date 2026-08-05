package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDocumentTool_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n2,Gadget\n")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"data.csv"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "csv") || !strings.Contains(out, "id, name") {
		t.Errorf("output missing expected metadata: %q", out)
	}
	if !strings.Contains(out, "Widget") {
		t.Errorf("output missing extracted content: %q", out)
	}
}

func TestReadDocumentTool_AbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	path := writeFile(t, tmp, "abs.json", `{"a": 1}`)

	tool := &ReadDocumentTool{Cwd: NewCwdRef("/unused")}
	out, err := tool.Execute(context.Background(), `{"path":"`+path+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "json") {
		t.Errorf("output missing json kind: %q", out)
	}
}

func TestReadDocumentTool_MissingPath(t *testing.T) {
	tool := &ReadDocumentTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("expected an error for a missing path")
	}
}

func TestReadDocumentTool_UnsupportedFormat(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "notes.txt", "plain text")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"path":"notes.txt"}`); err == nil {
		t.Fatal("expected an error for an unsupported extension")
	}
}

// TestReadDocumentTool_RejectsDeniedPath is the same trust regression as
// read_file: the credential-path denylist must apply here too, since
// read_document shares ValidateReadPath rather than inventing its own
// trust boundary.
func TestReadDocumentTool_RejectsDeniedPath(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "secret.csv", "key,value\na,b\n")
	secret := filepath.Join(tmp, "secret.csv")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), DenyReadPaths: []string{secret}}
	if _, err := tool.Execute(context.Background(), `{"path":"secret.csv"}`); err == nil {
		t.Fatal("read_document read a deny-listed path")
	}
}

func TestReadDocumentTool_NoApprovalNeeded(t *testing.T) {
	tool := &ReadDocumentTool{}
	if tool.RequiresApproval("") {
		t.Error("read_document is read-only and must never require approval")
	}
	if !tool.ParallelSafe("") {
		t.Error("read_document should be parallel-safe like read_file")
	}
}

func TestRegisterCoreCwdTools_DocumentIngestionGate(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())
	reg := NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	if reg.Names()["read_document"] {
		t.Fatal("read_document should be absent when document_ingestion is disabled")
	}

	reg = NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}, EnableDocumentIngestion: true})
	if !reg.Names()["read_document"] {
		t.Fatal("read_document should be registered when document_ingestion is enabled")
	}
}
