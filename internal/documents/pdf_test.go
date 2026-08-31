package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner builds a CommandRunner from a lookup keyed on the command
// prefix ("pdfinfo" or "pdftotext"), so a test can script exactly what
// each probe/extraction call returns without a real pdftotext install.
type fakeRunnerCall struct {
	stdout, stderr string
	err            error
}

func fakeRunner(t *testing.T, byPrefix map[string]fakeRunnerCall) CommandRunner {
	t.Helper()
	return func(ctx context.Context, command string) ([]byte, []byte, error) {
		for prefix, call := range byPrefix {
			if strings.HasPrefix(command, prefix) {
				return []byte(call.stdout), []byte(call.stderr), call.err
			}
		}
		t.Fatalf("fakeRunner: no stub for command %q", command)
		return nil, nil, nil
	}
}

func writePDFFixture(t *testing.T, size int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestPDFExtractorNilRunnerErrors(t *testing.T) {
	e := &PDFExtractor{}
	_, err := e.Extract(context.Background(), ExtractRequest{Path: writePDFFixture(t, 10)})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected an 'unavailable' error for a nil Run, got %v", err)
	}
}

func TestPDFExtractorMatch(t *testing.T) {
	e := &PDFExtractor{}
	if !e.Match("report.pdf") || !e.Match("REPORT.PDF") {
		t.Error("expected .pdf (any case) to match")
	}
	if e.Match("report.docx") {
		t.Error("expected .docx not to match")
	}
}

func TestPDFExtractorSplitsPagesWithLabels(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Title: x\nPages:          3\n"},
		"pdftotext": {stdout: "page one\fpage two\fpage three\f"},
	})
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.Shape != "3 pages" {
		t.Errorf("Shape = %q, want %q", res.Metadata.Shape, "3 pages")
	}
	if len(res.Sections) != 3 {
		t.Fatalf("got %d sections, want 3: %+v", len(res.Sections), res.Sections)
	}
	wantLabels := []string{"page 1", "page 2", "page 3"}
	for i, sec := range res.Sections {
		if sec.Label != wantLabels[i] {
			t.Errorf("section %d label = %q, want %q", i, sec.Label, wantLabels[i])
		}
	}
	if res.Sections[0].Text != "page one" {
		t.Errorf("section 0 text = %q, want %q", res.Sections[0].Text, "page one")
	}
}

func TestPDFExtractorMetadataFromPdfinfo(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Title:          Q3 Report\nAuthor:         Jane Doe\nCreationDate:   Mon Jan  1 00:00:00 2024\nPages:          1\n"},
		"pdftotext": {stdout: "page one\f"},
	})
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.Title != "Q3 Report" {
		t.Errorf("Metadata.Title = %q, want %q", res.Metadata.Title, "Q3 Report")
	}
	if res.Metadata.Author != "Jane Doe" {
		t.Errorf("Metadata.Author = %q, want %q", res.Metadata.Author, "Jane Doe")
	}
	if res.Metadata.CreationDate != "Mon Jan  1 00:00:00 2024" {
		t.Errorf("Metadata.CreationDate = %q, want %q", res.Metadata.CreationDate, "Mon Jan  1 00:00:00 2024")
	}
}

func TestPDFExtractorNoMetadataFieldsLeftEmpty(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "page one\f"},
	})
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.Title != "" || res.Metadata.Author != "" || res.Metadata.CreationDate != "" {
		t.Errorf("expected empty metadata fields when pdfinfo reports none, got %+v", res.Metadata)
	}
}

// TestPDFExtractorEncryptedDetectedViaPdfinfoShortCircuits confirms the
// robust path: when pdfinfo itself reports Encrypted: yes, Extract must
// return the warning immediately without ever calling pdftotext at all
// (a pdftotext stub that panics/fails would otherwise reveal a bug here).
func TestPDFExtractorEncryptedDetectedViaPdfinfoShortCircuits(t *testing.T) {
	path := writePDFFixture(t, 10)
	pdftotextCalled := false
	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		if strings.HasPrefix(command, "pdfinfo") {
			return []byte("Pages:          3\nEncrypted:      yes (print:no copy:no change:no addNotes:no)\n"), nil, nil
		}
		pdftotextCalled = true
		return nil, nil, errAny
	}
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("expected an encrypted PDF to be a warning, not an error: %v", err)
	}
	if pdftotextCalled {
		t.Errorf("expected pdftotext never to be called once pdfinfo reported Encrypted: yes")
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "encrypted") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an encrypted-PDF warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorOffsetSkipsPagesViaPageRange(t *testing.T) {
	path := writePDFFixture(t, 10)
	var seenCmd string
	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		if strings.HasPrefix(command, "pdfinfo") {
			return []byte("Pages:          5\n"), nil, nil
		}
		seenCmd = command
		return []byte("page two\f"), nil, nil
	}
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, Offset: 1, MaxPages: 1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(seenCmd, "-f 2 -l 2") {
		t.Errorf("expected pdftotext to be called with the absolute page range -f 2 -l 2, got command: %q", seenCmd)
	}
	if !strings.Contains(seenCmd, "-layout") {
		t.Errorf("expected pdftotext to be called with -layout for column/table-preserving output, got command: %q", seenCmd)
	}
	if len(res.Sections) != 1 || res.Sections[0].Label != "page 2" {
		t.Errorf("expected one section labeled 'page 2', got %+v", res.Sections)
	}
}

func TestPDFExtractorOffsetPastLastPageIsReported(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo": {stdout: "Pages:          2\n"},
	})
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, Offset: 5})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 0 {
		t.Errorf("expected no sections past the last page, got %+v", res.Sections)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "past the last page") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'past the last page' warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorPdfinfoUnavailableWarnsAndFallsBack(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		if strings.HasPrefix(command, "pdfinfo") {
			return nil, []byte("pdfinfo: not found"), errAny
		}
		return []byte("only page\f"), nil, nil
	}
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "could not determine") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'could not determine total page count' warning, got %v", res.Warnings)
	}
	if len(res.Sections) != 1 {
		t.Errorf("expected extraction to still proceed without a known total, got %+v", res.Sections)
	}
}

func TestPDFExtractorEncryptedPDFIsWarningNotError(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stderr: "Command Line Error: Incorrect password", err: errAny},
	})
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("expected an encrypted PDF to be a warning, not an error: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "encrypted") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an encrypted-PDF warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorScannedPDFWarnsOnBlankText(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          2\n"},
		"pdftotext": {stdout: "  \n\f \n\f"},
	})
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "scanned/image-only") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a scanned/image-only warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorMaxCharsTruncatesAcrossPages(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          2\n"},
		"pdftotext": {stdout: "0123456789\fabcdefghij\f"},
	})
	e := &PDFExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxChars: 15})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 2 {
		t.Fatalf("expected 2 sections (second truncated), got %+v", res.Sections)
	}
	if res.Sections[0].Text != "0123456789" {
		t.Errorf("page 1 text = %q, want full %q", res.Sections[0].Text, "0123456789")
	}
	if res.Sections[1].Text == "abcdefghij" {
		t.Errorf("page 2 text should have been truncated, got the full untruncated text")
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "character preview cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a character-preview-cap warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorRejectsOversizedFile(t *testing.T) {
	path := writePDFFixture(t, 100)
	e := &PDFExtractor{Run: fakeRunner(t, nil)}
	_, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxBytes: 10})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a size-cap error, got %v", err)
	}
}

func TestParsePDFInfo_Pages(t *testing.T) {
	cases := map[string]int{
		"Title: x\nPages:          12\nEncrypted: no\n": 12,
		"Pages: 1\n":           1,
		"no pages line here\n": 0,
		"":                     0,
		"Pages:abc\n":          0,
	}
	for in, want := range cases {
		if got := parsePDFInfo(in).Pages; got != want {
			t.Errorf("parsePDFInfo(%q).Pages = %d, want %d", in, got, want)
		}
	}
}

func TestParsePDFInfo_TitleAuthorCreationDate(t *testing.T) {
	out := "Title:          Q3 Report\nAuthor:         Jane Doe\nCreationDate:   Mon Jan  1 00:00:00 2024\nPages:          3\n"
	info := parsePDFInfo(out)
	if info.Title != "Q3 Report" {
		t.Errorf("Title = %q, want %q", info.Title, "Q3 Report")
	}
	if info.Author != "Jane Doe" {
		t.Errorf("Author = %q, want %q", info.Author, "Jane Doe")
	}
	if info.CreationDate != "Mon Jan  1 00:00:00 2024" {
		t.Errorf("CreationDate = %q, want %q", info.CreationDate, "Mon Jan  1 00:00:00 2024")
	}
}

func TestParsePDFInfo_EmptyFieldsOmitted(t *testing.T) {
	out := "Title:          \nSubject:        \nPages:          1\n"
	info := parsePDFInfo(out)
	if info.Title != "" {
		t.Errorf("Title = %q, want empty for a blank pdfinfo field", info.Title)
	}
}

func TestParsePDFInfo_Encrypted(t *testing.T) {
	cases := []struct {
		name          string
		out           string
		wantKnown     bool
		wantEncrypted bool
	}{
		{"reported no", "Pages: 1\nEncrypted:      no\n", true, false},
		{"reported yes with permission detail", "Pages: 1\nEncrypted:      yes (print:yes copy:no change:no addNotes:no)\n", true, true},
		{"not reported at all", "Pages: 1\n", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := parsePDFInfo(tc.out)
			if info.EncryptedKnown != tc.wantKnown {
				t.Errorf("EncryptedKnown = %v, want %v", info.EncryptedKnown, tc.wantKnown)
			}
			if info.Encrypted != tc.wantEncrypted {
				t.Errorf("Encrypted = %v, want %v", info.Encrypted, tc.wantEncrypted)
			}
		})
	}
}

func TestShellQuotePDF(t *testing.T) {
	if got := shellQuote("it's a/path.pdf"); got != `'it'\''s a/path.pdf'` {
		t.Errorf("shellQuote embedded quote = %q", got)
	}
}

// errAny is a stand-in non-nil error for stubbed CommandRunner failures
// where the exact error value doesn't matter, only that Run failed.
var errAny = &fakeRunnerError{}

type fakeRunnerError struct{}

func (*fakeRunnerError) Error() string { return "fake runner error" }
