//go:build integration

// End-to-end create_document -> read_document round trips against real
// pandoc, weasyprint, and poppler. These are the only tests that prove the
// GA docx/pdf claim; the unit tests fake the subprocess. Run with
// `go test -tags integration ./internal/agent/ -run DocumentRoundTrip`.
//
// Missing binaries skip locally. Set YC_REQUIRE_DOC_TOOLS=1 (CI does) to
// turn a skip into a failure so a broken runner can't silently pass.
package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireDocTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		if os.Getenv("YC_REQUIRE_DOC_TOOLS") == "1" {
			t.Fatalf("%s not on PATH but YC_REQUIRE_DOC_TOOLS=1", name)
		}
		t.Skipf("%s not on PATH; skipping integration test", name)
	}
}

func roundTripTools(tmp string) (*CreateDocumentTool, *ReadDocumentTool) {
	cwd := NewCwdRef(tmp)
	return &CreateDocumentTool{Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}, SubprocessFormatsEnabled: true},
		&ReadDocumentTool{Cwd: cwd, SubprocessFormatsEnabled: true}
}

const roundTripContent = `{"blocks":[
	{"type":"heading","level":1,"text":"Quarterly Report"},
	{"type":"paragraph","text":"Revenue grew steadily across regions."},
	{"type":"list","items":["alpha item","beta item"]},
	{"type":"table","header":["Region","Sales"],"rows":[["North","1200"],["South","3400"]]}
]}`

func TestDocumentRoundTripDocx(t *testing.T) {
	requireDocTool(t, "pandoc")
	tmp := t.TempDir()
	create, read := roundTripTools(tmp)

	if _, err := create.Execute(context.Background(), `{"format":"docx","output_path":"report.docx","content":`+roundTripContent+`}`); err != nil {
		t.Fatalf("create docx: %v", err)
	}
	if info, err := os.Stat(filepath.Join(tmp, "report.docx")); err != nil || info.Size() == 0 {
		t.Fatalf("docx not written: %v", err)
	}
	out, err := read.Execute(context.Background(), `{"path":"report.docx"}`)
	if err != nil {
		t.Fatalf("read docx: %v", err)
	}
	for _, want := range []string{"Quarterly Report", "Revenue grew steadily", "alpha item", "North", "3400"} {
		if !strings.Contains(out, want) {
			t.Errorf("docx round trip lost %q; got:\n%s", want, out)
		}
	}
}

func TestDocumentRoundTripPDF(t *testing.T) {
	for _, bin := range []string{"pandoc", "weasyprint", "pdftotext", "pdfinfo"} {
		requireDocTool(t, bin)
	}
	tmp := t.TempDir()
	create, read := roundTripTools(tmp)

	if _, err := create.Execute(context.Background(), `{"format":"pdf","output_path":"report.pdf","content":`+roundTripContent+`}`); err != nil {
		t.Fatalf("create pdf: %v", err)
	}
	out, err := read.Execute(context.Background(), `{"path":"report.pdf"}`)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	for _, want := range []string{"Quarterly Report", "Revenue grew steadily", "alpha item", "North", "3400"} {
		if !strings.Contains(out, want) {
			t.Errorf("pdf round trip lost %q; got:\n%s", want, out)
		}
	}
}
