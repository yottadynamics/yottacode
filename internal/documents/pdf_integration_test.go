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
