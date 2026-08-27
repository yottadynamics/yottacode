package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/documents"
)

// fakeSectionExtractor returns a fixed ExtractResult regardless of the
// request, so a test can control exactly what sections search_document
// has to rank without depending on any particular real extractor's
// text-joining convention (e.g. HTML rejoins everything space-
// separated with no paragraph breaks at all, which would make a
// ranking test depend on hardSplit's exact character-window math
// instead of the ranking logic this test actually wants to cover).
type fakeSectionExtractor struct{ sections []documents.DocumentSection }

func (e *fakeSectionExtractor) Match(string) bool { return true }

func (e *fakeSectionExtractor) Extract(_ context.Context, _ documents.ExtractRequest) (documents.ExtractResult, error) {
	return documents.ExtractResult{
		Metadata: documents.DocumentMetadata{Kind: "fake"},
		Sections: e.sections,
	}, nil
}

func TestSearchDocumentTool_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "notes.html", "placeholder, never parsed — fakeSectionExtractor supplies the content")

	reg := &documents.Registry{}
	reg.Register(&fakeSectionExtractor{sections: []documents.DocumentSection{
		{Label: "page 1", Text: "Quarterly planning notes for the team."},
		{Label: "page 2", Text: "The quarterly revenue projection is climbing steadily this year."},
		{Label: "page 3", Text: "Unrelated section about office snacks and coffee."},
	}})

	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	out, err := tool.Execute(context.Background(), `{"path":"notes.html","query":"quarterly revenue projection"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "1. page 2") {
		t.Errorf("expected page 2 (the matching section) to be ranked first, got:\n%s", out)
	}
	if !strings.Contains(out, "revenue projection") {
		t.Errorf("expected the matching section's text to be returned as a snippet, got:\n%s", out)
	}
	if strings.Contains(out, "office snacks") {
		t.Errorf("expected the unrelated section (no shared query terms) to be ranked out entirely, got:\n%s", out)
	}
}

func TestSearchDocumentTool_NoMatches(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "notes.html", "<html><body><p>Completely unrelated content about gardening.</p></body></html>")

	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"notes.html","query":"quarterly revenue projection zephyr"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no matching text found") {
		t.Errorf("expected a friendly no-match string, got:\n%s", out)
	}
}

func TestSearchDocumentTool_MissingPath(t *testing.T) {
	tool := &SearchDocumentTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("expected an error for a missing path")
	}
}

func TestSearchDocumentTool_MissingQuery(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "notes.html", "<html><body><p>content</p></body></html>")
	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"path":"notes.html"}`); err == nil {
		t.Fatal("expected an error for a missing query")
	}
}

func TestSearchDocumentTool_RejectsDeniedPath(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "secret.html", "<html><body><p>classified content</p></body></html>")
	secret := filepath.Join(tmp, "secret.html")

	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp), DenyReadPaths: []string{secret}}
	if _, err := tool.Execute(context.Background(), `{"path":"secret.html","query":"classified"}`); err == nil {
		t.Fatal("search_document read a deny-listed path")
	}
}

func TestSearchDocumentTool_NoApprovalNeeded(t *testing.T) {
	tool := &SearchDocumentTool{}
	if tool.RequiresApproval("") {
		t.Error("search_document is read-only and must never require approval")
	}
	if !tool.ParallelSafe("") {
		t.Error("search_document should be parallel-safe like read_document")
	}
}

func TestSearchDocumentTool_PDFDisabledViaSubprocessGate(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.pdf", "not a real pdf, just needs to exist")
	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp)}
	_, err := tool.Execute(context.Background(), `{"path":"doc.pdf","query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "disabled in this configuration") {
		t.Fatalf("expected an error explaining PDF is disabled, got %v", err)
	}
}

// TestSearchDocumentTool_PassesOCRLangThrough covers the ocr_lang arg ->
// ExtractRequest.OCRLang plumbing, matching read_document's own
// TestReadDocumentTool_PassesOCRLangThrough.
func TestSearchDocumentTool_PassesOCRLangThrough(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	fake := &capturingExtractor{}
	reg := &documents.Registry{}
	reg.Register(fake)

	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	if _, err := tool.Execute(context.Background(), `{"path":"data.csv","query":"widget","ocr_lang":"fra"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.got.OCRLang != "fra" {
		t.Errorf("OCRLang = %q, want %q", fake.got.OCRLang, "fra")
	}
}

func TestSearchDocumentTool_UnsupportedFormat(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "notes.txt", "plain text")
	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"path":"notes.txt","query":"x"}`); err == nil {
		t.Fatal("expected an error for an unsupported extension")
	}
}

func TestRegisterCoreCwdTools_SearchDocumentRegistered(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())
	reg := NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	if _, ok := reg.Get("search_document"); !ok {
		t.Fatal("search_document should always be registered")
	}
}

// TestSearchDocumentTool_UsesSameRegistryAsReadDocument confirms this
// tool reuses ReadDocumentTool's format dispatch (a fake Registry
// wired through this tool's own field), rather than constructing its
// own separate one that could silently drift from read_document's.
func TestSearchDocumentTool_UsesSameRegistryAsReadDocument(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	fake := &capturingExtractor{}
	reg := &documents.Registry{}
	reg.Register(fake)

	tool := &SearchDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	if _, err := tool.Execute(context.Background(), `{"path":"data.csv","query":"widget"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.got.MaxBytes != documents.MaxAllowedBytes {
		t.Errorf("MaxBytes = %d, want the MaxAllowedBytes ceiling %d", fake.got.MaxBytes, documents.MaxAllowedBytes)
	}
}

func TestChunkSections_SingleChunkKeepsSectionLabel(t *testing.T) {
	sections := []documents.DocumentSection{{Label: "page 1", Text: "A short paragraph."}}
	entries := chunkSections(sections, 800, 100)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Name != "page 1" {
		t.Errorf("Name = %q, want %q (no part suffix for a single chunk)", entries[0].Name, "page 1")
	}
	if entries[0].Body != "A short paragraph." {
		t.Errorf("Body = %q, want %q", entries[0].Body, "A short paragraph.")
	}
}

func TestChunkSections_SplitsOnParagraphBoundaries(t *testing.T) {
	sections := []documents.DocumentSection{{Label: "body", Text: "First paragraph.\n\nSecond paragraph."}}
	entries := chunkSections(sections, 800, 100)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "body (part 1)" || entries[1].Name != "body (part 2)" {
		t.Errorf("unexpected labels: %q, %q", entries[0].Name, entries[1].Name)
	}
	if entries[0].Body != "First paragraph." || entries[1].Body != "Second paragraph." {
		t.Errorf("unexpected bodies: %q, %q", entries[0].Body, entries[1].Body)
	}
}

func TestChunkSections_HardSplitsOversizedParagraphWithOverlap(t *testing.T) {
	long := strings.Repeat("a", 25)
	sections := []documents.DocumentSection{{Label: "sheet", Text: long}}
	entries := chunkSections(sections, 10, 3)
	if len(entries) < 2 {
		t.Fatalf("expected an oversized paragraph to be hard-split into multiple chunks, got %d", len(entries))
	}
	// Reassemble via the known overlap and confirm no content was
	// dropped: every character of the original run of a's must appear.
	var rebuilt strings.Builder
	rebuilt.WriteString(entries[0].Body)
	for _, e := range entries[1:] {
		rebuilt.WriteString(e.Body[min(len(e.Body), 3):])
	}
	if rebuilt.Len() < len(long) {
		t.Errorf("hard split appears to have dropped content: rebuilt %d chars from %d-char source", rebuilt.Len(), len(long))
	}
}

func TestChunkSections_EmptySectionProducesNoChunks(t *testing.T) {
	sections := []documents.DocumentSection{{Label: "empty", Text: "   \n\n  "}}
	entries := chunkSections(sections, 800, 100)
	if len(entries) != 0 {
		t.Errorf("expected no chunks for a blank section, got %+v", entries)
	}
}
