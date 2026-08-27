//go:build integration

// Integration tests for PDFExtractor against real pdftotext/pdfinfo
// (and, for fixture generation, real pandoc+weasyprint) — the first use
// of the integration build tag in this repo, per roadmap/v0.4.0-release.md's
// "external-helper tests belong behind //go:build integration" convention.
// Skips gracefully if the required binaries aren't on PATH, so `go test
// ./...` (no -tags) never depends on them; run explicitly with
// `go test -tags integration ./internal/documents/...` when pandoc,
// weasyprint, pdftotext, and pdfinfo are all installed (e.g. inside a
// container built from infra/documents.Containerfile).
package documents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hostCommandRunner(t *testing.T) CommandRunner {
	t.Helper()
	return func(ctx context.Context, command string) ([]byte, []byte, error) {
		c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		var stdout, stderr strings.Builder
		c.Stdout = &stdout
		c.Stderr = &stderr
		err := c.Run()
		return []byte(stdout.String()), []byte(stderr.String()), err
	}
}

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH; skipping integration test", name)
	}
}

// requirePythonModule skips the test unless the python3 on PATH has
// module importable — the same skip-gracefully convention requireBinary
// uses, extended to a python module rather than a PATH binary since
// pdfplumber has no CLI of its own to LookPath for.
func requirePythonModule(t *testing.T, module string) {
	t.Helper()
	requireBinary(t, "python3")
	if err := exec.Command("python3", "-c", "import "+module).Run(); err != nil {
		t.Skipf("python module %q not importable; skipping integration test", module)
	}
}

// TestPDFExtractor_RealPdftotextRoundTrip generates a small real PDF via
// pandoc+weasyprint (the same tools create_document already depends on
// for the pdf format), then verifies PDFExtractor reads real, recognizable
// text back out of it via real pdftotext/pdfinfo.
func TestPDFExtractor_RealPdftotextRoundTrip(t *testing.T) {
	requireBinary(t, "pandoc")
	requireBinary(t, "weasyprint")
	requireBinary(t, "pdftotext")
	requireBinary(t, "pdfinfo")

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "fixture.md")
	pdfPath := filepath.Join(dir, "fixture.pdf")
	const marker = "PDFExtractorRoundTripMarkerText12345"
	if err := os.WriteFile(mdPath, []byte("# Heading\n\n"+marker+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture markdown: %v", err)
	}

	genCmd := exec.Command("pandoc", "-f", "markdown", "-t", "pdf", "--pdf-engine=weasyprint", "-o", pdfPath, mdPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture pdf: %v: %s", err, out)
	}

	e := &PDFExtractor{Run: hostCommandRunner(t)}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: pdfPath})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.Shape != "1 pages" {
		t.Errorf("Shape = %q, want %q", res.Metadata.Shape, "1 pages")
	}
	if len(res.Sections) == 0 {
		t.Fatal("expected at least one section")
	}
	found := false
	for _, sec := range res.Sections {
		if strings.Contains(sec.Text, marker) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected extracted text to contain the marker %q, got sections: %+v", marker, res.Sections)
	}
}

// TestPDFExtractor_RealPdfplumberTableRoundTrip generates a real PDF
// with a real table via pandoc+weasyprint (same tools
// TestPDFExtractor_RealPdftotextRoundTrip already depends on), then
// verifies PDFExtractor's optional table-extraction tier returns real,
// recognizable structured rows back out of it via the real
// extract_pdf_tables.py + pdfplumber — not a fake ResolveScript/Run.
func TestPDFExtractor_RealPdfplumberTableRoundTrip(t *testing.T) {
	requireBinary(t, "pandoc")
	requireBinary(t, "weasyprint")
	requireBinary(t, "pdftotext")
	requireBinary(t, "pdfinfo")
	requirePythonModule(t, "pdfplumber")

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "fixture.md")
	pdfPath := filepath.Join(dir, "fixture.pdf")
	const md = "# Report\n\n| Name | Qty |\n| --- | --- |\n| Widget | 3 |\n| Gadget | 1 |\n"
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write fixture markdown: %v", err)
	}
	genCmd := exec.Command("pandoc", "-f", "markdown", "-t", "pdf", "--pdf-engine=weasyprint", "-o", pdfPath, mdPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture pdf: %v: %s", err, out)
	}

	cacheDir := t.TempDir()
	e := &PDFExtractor{
		Run: hostCommandRunner(t),
		ResolveScript: func(script PyHelperScript) (string, error) {
			return ResolvePyHelperScript(script, false, cacheDir)
		},
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: pdfPath})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	found := false
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "table") && strings.Contains(sec.Text, "Widget") && strings.Contains(sec.Text, "3") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a table section containing the fixture's real data, got sections: %+v", res.Sections)
	}
}

// TestPDFExtractor_RealPdfplumberTwoTablesDoesNotBreakPlainText is the
// regression for a real, documented limitation: pdfplumber's text-
// alignment strategy can merge two separate tables that have only a
// small amount of prose between them into one garbled "table",
// fragmenting the prose into spurious cells (verified manually:
// "Second table:" split into "econd t"/"a"/"ble:") — see
// roadmap/document-generation.md's Phasing item 4 and
// docs/document-generation.md's Known limitations. This test does NOT
// assert the table tier produces clean output here — it asserts the
// "best-effort, additive, never harms the primary result" contract:
// Extract must still succeed, and the always-reliable pdftotext-based
// plain-text page section must still be intact and correct even when
// the bonus table tier gets confused.
func TestPDFExtractor_RealPdfplumberTwoTablesDoesNotBreakPlainText(t *testing.T) {
	requireBinary(t, "pandoc")
	requireBinary(t, "weasyprint")
	requireBinary(t, "pdftotext")
	requireBinary(t, "pdfinfo")
	requirePythonModule(t, "pdfplumber")

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "fixture.md")
	pdfPath := filepath.Join(dir, "fixture.pdf")
	const md = "# Report\n\nFirst table:\n\n| Name | Qty |\n| --- | --- |\n| Widget | 3 |\n\n" +
		"Some text between the tables.\n\nSecond table:\n\n| Color | Code |\n| --- | --- |\n| Red | R1 |\n"
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write fixture markdown: %v", err)
	}
	genCmd := exec.Command("pandoc", "-f", "markdown", "-t", "pdf", "--pdf-engine=weasyprint", "-o", pdfPath, mdPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture pdf: %v: %s", err, out)
	}

	cacheDir := t.TempDir()
	e := &PDFExtractor{
		Run: hostCommandRunner(t),
		ResolveScript: func(script PyHelperScript) (string, error) {
			return ResolvePyHelperScript(script, false, cacheDir)
		},
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: pdfPath})
	if err != nil {
		t.Fatalf("Extract must succeed even when the table tier gets confused by two close-together tables: %v", err)
	}

	found := false
	for _, sec := range res.Sections {
		if sec.Label == "page 1" {
			found = true
			for _, want := range []string{"First table", "Some text between the tables", "Second table"} {
				if !strings.Contains(sec.Text, want) {
					t.Errorf("primary plain-text section missing expected content %q; got: %s", want, sec.Text)
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected a primary %q section regardless of table-tier confusion, got sections: %+v", "page 1", res.Sections)
	}
}
