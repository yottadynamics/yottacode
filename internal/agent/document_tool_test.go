package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/documents"
)

// capturingExtractor records the ExtractRequest it was handed, so a test
// can assert what the tool actually plumbed through from its JSON args
// rather than inferring it from formatted output.
type capturingExtractor struct{ got documents.ExtractRequest }

func (e *capturingExtractor) Match(string) bool { return true }

func (e *capturingExtractor) Extract(_ context.Context, req documents.ExtractRequest) (documents.ExtractResult, error) {
	e.got = req
	return documents.ExtractResult{Metadata: documents.DocumentMetadata{Kind: "fake"}}, nil
}

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

// TestReadDocumentTool_PassesCapsThrough covers the arg -> ExtractRequest
// plumbing. The caps are the tool's only tuning surface, so a dropped
// field would silently ignore what the model asked for.
func TestReadDocumentTool_PassesCapsThrough(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	fake := &capturingExtractor{}
	reg := &documents.Registry{}
	reg.Register(fake)

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	if _, err := tool.Execute(context.Background(),
		`{"path":"data.csv","max_rows":7,"max_chars":99,"max_bytes":4096,"offset":25,"has_header":false}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fake.got.Offset != 25 {
		t.Errorf("Offset = %d, want 25", fake.got.Offset)
	}
	if fake.got.HasHeader == nil || *fake.got.HasHeader {
		t.Errorf("HasHeader = %v, want an explicit false", fake.got.HasHeader)
	}

	if fake.got.MaxRows != 7 {
		t.Errorf("MaxRows = %d, want 7", fake.got.MaxRows)
	}
	if fake.got.MaxChars != 99 {
		t.Errorf("MaxChars = %d, want 99", fake.got.MaxChars)
	}
	if fake.got.MaxBytes != 4096 {
		t.Errorf("MaxBytes = %d, want 4096", fake.got.MaxBytes)
	}
}

// TestReadDocumentTool_OmittedHasHeaderStaysNil: an absent key must mean
// "auto-detect", which is a different instruction than an explicit
// false. Decoding into a plain bool would collapse the two.
func TestReadDocumentTool_OmittedHasHeaderStaysNil(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	fake := &capturingExtractor{}
	reg := &documents.Registry{}
	reg.Register(fake)

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	if _, err := tool.Execute(context.Background(), `{"path":"data.csv"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fake.got.HasHeader != nil {
		t.Errorf("HasHeader = %v, want nil when the key is omitted", *fake.got.HasHeader)
	}
}

// TestReadDocumentTool_ClampedMaxBytesIsReported: an over-ceiling
// max_bytes is clamped inside withDefaults, which on its own would leave
// the model believing it read further than it did. This tool's contract
// is that every limit is stated, so the clamp must surface as a warning.
func TestReadDocumentTool_ClampedMaxBytesIsReported(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(),
		fmt.Sprintf(`{"path":"data.csv","max_bytes":%d}`, documents.MaxAllowedBytes*4))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(out, "clamped") {
		t.Errorf("an over-ceiling max_bytes must be reported, not silently clamped; got:\n%s", out)
	}
}

// TestReadDocumentTool_InBoundsMaxBytesIsNotWarned guards the other
// direction: a legitimate override must not produce clamp noise.
func TestReadDocumentTool_InBoundsMaxBytesIsNotWarned(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"data.csv","max_bytes":1048576}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if strings.Contains(out, "clamped") {
		t.Errorf("an in-bounds max_bytes must not warn; got:\n%s", out)
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
