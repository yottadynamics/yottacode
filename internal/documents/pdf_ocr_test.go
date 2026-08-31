package documents

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestBlankPageRanges(t *testing.T) {
	cases := []struct {
		name  string
		pages []string
		want  []blankPageRange
	}{
		{"none blank", []string{"text", "more text"}, nil},
		{"all blank", []string{"  ", "\n"}, []blankPageRange{{0, 1}}},
		{"leading blank run", []string{"  ", " ", "real text"}, []blankPageRange{{0, 1}}},
		{"trailing blank run", []string{"real text", "  ", " "}, []blankPageRange{{1, 2}}},
		{"blank sandwiched between text", []string{"text one", "  ", "text two"}, []blankPageRange{{1, 1}}},
		{"two separate blank runs", []string{"  ", "text", " ", "text", "  "}, []blankPageRange{{0, 0}, {2, 2}, {4, 4}}},
		{"empty input", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := blankPageRanges(c.pages)
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("range %d: got %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestParsePythonOCRJSON(t *testing.T) {
	out := []byte(`{"pages": [{"page": 1, "text": "Hello world"}, {"page": 2, "text": ""}]}`)
	pages, err := parsePythonOCRJSON(out)
	if err != nil {
		t.Fatalf("parsePythonOCRJSON: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if pages[0].Page != 1 || pages[0].Text != "Hello world" {
		t.Errorf("unexpected page 1: %+v", pages[0])
	}
	if pages[1].Page != 2 || pages[1].Text != "" {
		t.Errorf("unexpected page 2: %+v", pages[1])
	}
}

func TestParsePythonOCRJSON_MalformedInput(t *testing.T) {
	if _, err := parsePythonOCRJSON([]byte("not json")); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

func TestBuildOCRSections(t *testing.T) {
	pages := []pageOCRText{
		{Page: 1, Text: "Recognized text"},
		{Page: 2, Text: "   \n  "}, // blank must be skipped, not rendered as an empty section
	}
	sections := buildOCRSections(pages)
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1 (page 2's blank OCR text must be skipped)", len(sections))
	}
	if sections[0].Label != "page 1 (ocr)" {
		t.Errorf("Label = %q, want %q", sections[0].Label, "page 1 (ocr)")
	}
	if sections[0].Text != "Recognized text" {
		t.Errorf("Text = %q, want %q", sections[0].Text, "Recognized text")
	}
}

func TestBuildPDFOCRCommand(t *testing.T) {
	cmd := buildPDFOCRCommand("/opt/yottacode/doc-helpers/extract_pdf_ocr.py", "/tmp/a b.pdf", 2, 5, "")
	if !strings.HasPrefix(cmd, "python3 ") {
		t.Errorf("expected command to start with 'python3 ', got %q", cmd)
	}
	if !strings.Contains(cmd, "'/opt/yottacode/doc-helpers/extract_pdf_ocr.py'") {
		t.Errorf("expected quoted script path, got %q", cmd)
	}
	if !strings.Contains(cmd, `'/tmp/a b.pdf'`) {
		t.Errorf("expected quoted pdf path with embedded space preserved, got %q", cmd)
	}
	if !strings.Contains(cmd, "--start 2") || !strings.Contains(cmd, "--end 5") {
		t.Errorf("expected --start/--end flags, got %q", cmd)
	}
	if strings.Contains(cmd, "--lang") {
		t.Errorf("expected an empty lang to omit --lang entirely, got %q", cmd)
	}
}

func TestBuildPDFOCRCommand_WithLang(t *testing.T) {
	cmd := buildPDFOCRCommand("/opt/yottacode/doc-helpers/extract_pdf_ocr.py", "/tmp/a.pdf", 1, 1, "fra")
	if !strings.Contains(cmd, "--lang 'fra'") {
		t.Errorf("expected a quoted --lang fra flag, got %q", cmd)
	}
}

func TestBuildPDFOCRCommand_MultiLang(t *testing.T) {
	cmd := buildPDFOCRCommand("/opt/yottacode/doc-helpers/extract_pdf_ocr.py", "/tmp/a.pdf", 1, 1, "eng+fra")
	if !strings.Contains(cmd, "--lang 'eng+fra'") {
		t.Errorf("expected a quoted --lang eng+fra flag, got %q", cmd)
	}
}

// TestPDFExtractorOCRPartiallyScannedDocumentOnlyOCRsBlankPages is the
// regression for per-page OCR: a document with real text on some pages
// and no text layer on others must recover text only for the blank
// page, leaving the already-good pages untouched, and the warning must
// say so rather than implying the whole document was scanned.
func TestPDFExtractorOCRPartiallyScannedDocumentOnlyOCRsBlankPages(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          3\n"},
		"pdftotext": {stdout: "Real text one\f  \n\fReal text three\f"},
		"python3":   {stdout: `{"pages": [{"page": 2, "text": "Recovered text"}]}`},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_ocr.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	var labels []string
	for _, sec := range res.Sections {
		labels = append(labels, sec.Label)
	}
	wantLabel := func(label string) {
		if !slices.Contains(labels, label) {
			t.Errorf("expected a %q section, got labels: %v", label, labels)
		}
	}
	wantLabel("page 1")
	wantLabel("page 2 (ocr)")
	wantLabel("page 3")

	for _, sec := range res.Sections {
		if sec.Label == "page 1" && sec.Text != "Real text one" {
			t.Errorf("page 1 text = %q, should be untouched by the OCR tier", sec.Text)
		}
		if sec.Label == "page 3" && sec.Text != "Real text three" {
			t.Errorf("page 3 text = %q, should be untouched by the OCR tier", sec.Text)
		}
		if sec.Label == "page 2 (ocr)" && sec.Text != "Recovered text" {
			t.Errorf("page 2 (ocr) text = %q, want %q", sec.Text, "Recovered text")
		}
	}

	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "recovered via OCR") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an OCR-provenance warning, got %v", res.Warnings)
	}
}

// TestPDFExtractorOCRPartialRecoveryReportsCount covers the "recovered
// some but not all" wording: two blank pages, OCR only recovers one.
func TestPDFExtractorOCRPartialRecoveryReportsCount(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          2\n"},
		"pdftotext": {stdout: "  \n\f  \n\f"}, // both pages blank -> one contiguous range
		// Requested range covers pages 1-2, but the script only
		// recognizes text on one of them (page 2 stays unrecognizable
		// e.g. truly blank artwork) — buildOCRSections already drops a
		// page whose OCR text is itself blank, so only page 1 survives.
		"python3": {stdout: `{"pages": [{"page": 1, "text": "Recovered"}, {"page": 2, "text": ""}]}`},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_ocr.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "recovered text for 1 of them") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a partial-recovery warning naming the count, got %v", res.Warnings)
	}
}

func TestPDFExtractorOCRAppendedWhenBlankAndAvailable(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "  \n\f"}, // blank -> triggers the OCR tier
		"python3":   {stdout: `{"pages": [{"page": 1, "text": "Recognized text"}]}`},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_ocr.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := false
	for _, sec := range res.Sections {
		if sec.Label == "page 1 (ocr)" {
			found = true
			if sec.Text != "Recognized text" {
				t.Errorf("unexpected OCR section text: %q", sec.Text)
			}
		}
	}
	if !found {
		t.Fatalf("expected a %q section, got sections: %+v", "page 1 (ocr)", res.Sections)
	}
	foundWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "recovered via OCR") {
			foundWarning = true
		}
		if strings.Contains(w, "OCR is not supported") {
			t.Errorf("stale 'OCR is not supported' warning should not appear once OCR succeeded: %v", res.Warnings)
		}
	}
	if !foundWarning {
		t.Errorf("expected an OCR-provenance warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorOCRLangPassesThroughToCommand(t *testing.T) {
	path := writePDFFixture(t, 10)
	var seenCmd string
	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		if strings.HasPrefix(command, "pdfinfo") {
			return []byte("Pages:          1\n"), nil, nil
		}
		if strings.HasPrefix(command, "pdftotext") {
			return []byte("  \n\f"), nil, nil // blank -> triggers the OCR tier
		}
		seenCmd = command
		return []byte(`{"pages": [{"page": 1, "text": "texte reconnu"}]}`), nil, nil
	}
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_ocr.py", nil },
	}
	_, err := e.Extract(context.Background(), ExtractRequest{Path: path, OCRLang: "fra"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(seenCmd, "--lang 'fra'") {
		t.Errorf("expected ExtractRequest.OCRLang to reach the OCR command as --lang 'fra', got command: %q", seenCmd)
	}
}

func TestPDFExtractorOCREmptyLangOmitsFlag(t *testing.T) {
	path := writePDFFixture(t, 10)
	var seenCmd string
	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		if strings.HasPrefix(command, "pdfinfo") {
			return []byte("Pages:          1\n"), nil, nil
		}
		if strings.HasPrefix(command, "pdftotext") {
			return []byte("  \n\f"), nil, nil
		}
		seenCmd = command
		return []byte(`{"pages": [{"page": 1, "text": "recognized text"}]}`), nil, nil
	}
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_ocr.py", nil },
	}
	_, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(seenCmd, "--lang") {
		t.Errorf("expected an unset OCRLang to omit --lang, got command: %q", seenCmd)
	}
}

func TestPDFExtractorOCRSkippedWhenPrimaryTextPresent(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "Report\n\nSome text.\n"},
		// The table tier runs unconditionally (not gated on blank
		// text), so it also resolves/invokes "python3" here via the
		// same ResolveScript field below — stub it harmlessly rather
		// than leaving it unstubbed, so a fakeRunner panic there
		// doesn't mask what this test actually checks: that no OCR
		// section appears when the primary text is non-blank.
		"python3": {stdout: `{"pages": [{"page": 1, "tables": []}]}`},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_ocr.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "ocr") {
			t.Errorf("expected no OCR sections when the primary text is non-blank, got %+v", sec)
		}
	}
}

func TestPDFExtractorOCRSkippedWhenResolveScriptNil(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "  \n\f"},
	})
	e := &PDFExtractor{Run: run} // ResolveScript left nil
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "ocr") {
			t.Errorf("expected no OCR sections when ResolveScript is nil, got %+v", sec)
		}
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "scanned/image-only") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the scanned/image-only fallback warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorOCRSkippedOnScriptFailure(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "  \n\f"},
		"python3":   {err: errors.New("exit status 1"), stderr: "pytesseract/pdf2image not installed"},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_ocr.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract must still succeed when the OCR tier fails: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "ocr") {
			t.Errorf("expected no OCR sections when the script call fails, got %+v", sec)
		}
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "scanned/image-only") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the scanned/image-only fallback warning, got %v", res.Warnings)
	}
}
