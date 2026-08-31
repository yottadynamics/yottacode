package documents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParsePythonTableJSON(t *testing.T) {
	out := []byte(`{"pages": [{"page": 1, "tables": [{"rows": [["a","b"],["c","d"]]}]}, {"page": 2, "tables": []}]}`)
	pages, err := parsePythonTableJSON(out)
	if err != nil {
		t.Fatalf("parsePythonTableJSON: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if pages[0].Page != 1 || len(pages[0].Tables) != 1 || len(pages[0].Tables[0]) != 2 {
		t.Errorf("unexpected page 1: %+v", pages[0])
	}
	if pages[0].Tables[0][0][0] != "a" || pages[0].Tables[0][1][1] != "d" {
		t.Errorf("unexpected cell values: %+v", pages[0].Tables[0])
	}
	if pages[1].Page != 2 || len(pages[1].Tables) != 0 {
		t.Errorf("unexpected page 2: %+v", pages[1])
	}
}

func TestParsePythonTableJSON_Images(t *testing.T) {
	out := []byte(`{"pages": [{"page": 1, "tables": [], "images": [
		{"width_pt": 144, "height_pt": 72, "src_width_px": 400, "src_height_px": 200},
		{"width_pt": 36, "height_pt": 36}
	]}]}`)
	pages, err := parsePythonTableJSON(out)
	if err != nil {
		t.Fatalf("parsePythonTableJSON: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Images) != 2 {
		t.Fatalf("unexpected pages: %+v", pages)
	}
	img0 := pages[0].Images[0]
	if img0.WidthPt != 144 || img0.HeightPt != 72 || img0.SrcWidthPx != 400 || img0.SrcHeightPx != 200 {
		t.Errorf("unexpected image 0: %+v", img0)
	}
	img1 := pages[0].Images[1]
	if img1.WidthPt != 36 || img1.HeightPt != 36 || img1.SrcWidthPx != 0 || img1.SrcHeightPx != 0 {
		t.Errorf("expected image 1's missing src size to default to 0: %+v", img1)
	}
}

func TestParsePythonTableJSON_MalformedInput(t *testing.T) {
	if _, err := parsePythonTableJSON([]byte("not json")); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

func TestBuildTableSections(t *testing.T) {
	pages := []pagePDFExtraction{
		{Page: 1, Tables: [][][]string{{{"Name", "Qty"}, {"Widget", "3"}}}},
		{Page: 2, Tables: [][][]string{{}}}, // empty table must be skipped, not rendered as a blank section
	}
	sections := buildTableSections(pages)
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1 (page 2's empty table must be skipped)", len(sections))
	}
	if sections[0].Label != "page 1 table 1" {
		t.Errorf("Label = %q, want %q", sections[0].Label, "page 1 table 1")
	}
	want := "Name | Qty\nWidget | 3"
	if sections[0].Text != want {
		t.Errorf("Text = %q, want %q", sections[0].Text, want)
	}
}

func TestBuildPDFImageSections(t *testing.T) {
	pages := []pagePDFExtraction{
		{Page: 1, Images: []pdfImage{
			{WidthPt: 144, HeightPt: 72, SrcWidthPx: 400, SrcHeightPx: 200},
			{WidthPt: 0, HeightPt: 0}, // no usable size must be skipped
		}},
		{Page: 2, Images: []pdfImage{{WidthPt: 36, HeightPt: 36}}}, // no src size known
	}
	sections := buildPDFImageSections(pages)
	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2 (page 1's zero-size image skipped): %+v", len(sections), sections)
	}
	if sections[0].Label != "page 1 image 1" {
		t.Errorf("Label = %q, want %q", sections[0].Label, "page 1 image 1")
	}
	if sections[0].Text != "size: 2.00in x 1.00in (400x200 px source)" {
		t.Errorf("Text = %q, want %q", sections[0].Text, "size: 2.00in x 1.00in (400x200 px source)")
	}
	if sections[1].Label != "page 2 image 1" {
		t.Errorf("Label = %q, want %q", sections[1].Label, "page 2 image 1")
	}
	if strings.Contains(sections[1].Text, "px source") {
		t.Errorf("expected no source-pixel suffix when srcsize is unknown, got %q", sections[1].Text)
	}
}

func TestBuildTableExtractionCommand(t *testing.T) {
	cmd := buildTableExtractionCommand("/opt/yottacode/doc-helpers/extract_pdf_tables.py", "/tmp/a b.pdf", 2, 5)
	if !strings.HasPrefix(cmd, "python3 ") {
		t.Errorf("expected command to start with 'python3 ', got %q", cmd)
	}
	if !strings.Contains(cmd, "'/opt/yottacode/doc-helpers/extract_pdf_tables.py'") {
		t.Errorf("expected quoted script path, got %q", cmd)
	}
	if !strings.Contains(cmd, `'/tmp/a b.pdf'`) {
		t.Errorf("expected quoted pdf path with embedded space preserved, got %q", cmd)
	}
	if !strings.Contains(cmd, "--start 2") || !strings.Contains(cmd, "--end 5") {
		t.Errorf("expected --start/--end flags, got %q", cmd)
	}
}

func TestPDFExtractorTablesAppendedWhenAvailable(t *testing.T) {
	path := writePDFFixture(t, 10)
	// fakeRunner (pdf_test.go) already matches by command prefix, so
	// stubbing "python3" alongside pdfinfo/pdftotext needs no changes
	// there — the table-extraction tier's command just starts with a
	// different prefix.
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "Report\n\nSome text.\n"},
		"python3":   {stdout: `{"pages": [{"page": 1, "tables": [{"rows": [["a","b"],["c","d"]]}]}]}`},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_tables.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := false
	for _, sec := range res.Sections {
		if sec.Label == "page 1 table 1" {
			found = true
			if sec.Text != "a | b\nc | d" {
				t.Errorf("unexpected table section text: %q", sec.Text)
			}
		}
	}
	if !found {
		t.Fatalf("expected a %q section, got sections: %+v", "page 1 table 1", res.Sections)
	}
}

// TestPDFExtractorImagesAppendedWhenAvailable is the end-to-end version
// of TestBuildPDFImageSections: a table+image extraction script response
// must surface image sections through Extract, from the same python3
// call the table tier already makes (fakeRunner only stubs "python3"
// once — a second call with a different command would fail the test via
// fakeRunner's own t.Fatalf-on-unstubbed-command behavior).
func TestPDFExtractorImagesAppendedWhenAvailable(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "Report\n\nSome text.\n"},
		"python3":   {stdout: `{"pages": [{"page": 1, "tables": [], "images": [{"width_pt": 72, "height_pt": 72, "src_width_px": 100, "src_height_px": 100}]}]}`},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_tables.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := false
	for _, sec := range res.Sections {
		if sec.Label == "page 1 image 1" {
			found = true
			if sec.Text != "size: 1.00in x 1.00in (100x100 px source)" {
				t.Errorf("unexpected image section text: %q", sec.Text)
			}
		}
	}
	if !found {
		t.Fatalf("expected a %q section, got sections: %+v", "page 1 image 1", res.Sections)
	}
}

// TestPDFExtractorBonusSectionsRespectMaxChars pins the bounded-preview
// contract for pdfplumber's additive table/image tier: bonus sections must not
// inflate the response beyond MaxChars after the primary page text is capped.
func TestPDFExtractorBonusSectionsRespectMaxChars(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "12345"},
		"python3":   {stdout: `{"pages": [{"page": 1, "tables": [{"rows": [["abcdefghij"]]}], "images": [{"width_pt": 72, "height_pt": 72}]}]}`},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_tables.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxChars: 8})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	total := 0
	for _, sec := range res.Sections {
		total += len(sec.Text)
	}
	if total > 8 {
		t.Fatalf("sections used %d chars, want <= 8: %+v", total, res.Sections)
	}
	if !containsWarning(res.Warnings, "preview cap") {
		t.Fatalf("expected a preview-cap warning, got %v", res.Warnings)
	}
}

func TestPDFExtractorTablesSkippedWhenResolveScriptNil(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "Report\n\nSome text.\n"},
	})
	e := &PDFExtractor{Run: run} // ResolveScript left nil
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "table") {
			t.Errorf("expected no table sections when ResolveScript is nil, got %+v", sec)
		}
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings from a missing-by-design table tier, got %v", res.Warnings)
	}
}

func TestPDFExtractorTablesSkippedOnScriptFailure(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "Report\n\nSome text.\n"},
		"python3":   {err: errors.New("exit status 1"), stderr: "pdfplumber not installed"},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "/opt/yottacode/doc-helpers/extract_pdf_tables.py", nil },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract must still succeed with the plain-text result when the table tier fails: %v", err)
	}
	if len(res.Sections) == 0 || !strings.Contains(res.Sections[0].Text, "Report") {
		t.Fatalf("expected the primary pdftotext result to survive a table-tier failure, got %+v", res.Sections)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "table") {
			t.Errorf("expected no table sections when the script call fails, got %+v", sec)
		}
	}
}

func TestPDFExtractorTablesSkippedOnResolveScriptError(t *testing.T) {
	path := writePDFFixture(t, 10)
	run := fakeRunner(t, map[string]fakeRunnerCall{
		"pdfinfo":   {stdout: "Pages:          1\n"},
		"pdftotext": {stdout: "Report\n\nSome text.\n"},
	})
	e := &PDFExtractor{
		Run:           run,
		ResolveScript: func(PyHelperScript) (string, error) { return "", errors.New("cache dir unwritable") },
	}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract must still succeed when ResolveScript errors: %v", err)
	}
	if len(res.Sections) == 0 || !strings.Contains(res.Sections[0].Text, "Report") {
		t.Fatalf("expected the primary pdftotext result to survive a ResolveScript failure, got %+v", res.Sections)
	}
}
