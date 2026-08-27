package documents

import (
	"encoding/json"
	"fmt"
	"strings"
)

// pyOCROutput mirrors pyhelpers/extract_pdf_ocr.py's stdout JSON shape
// exactly.
type pyOCROutput struct {
	Pages []struct {
		Page int    `json:"page"`
		Text string `json:"text"`
	} `json:"pages"`
}

// pageOCRText is one PDF page's OCR-recognized text, 1-indexed to
// match pdftotext's own page numbering used elsewhere in this file.
type pageOCRText struct {
	Page int
	Text string
}

// parsePythonOCRJSON parses extract_pdf_ocr.py's stdout. Pure
// function — no I/O, unit-testable with canned JSON.
func parsePythonOCRJSON(out []byte) ([]pageOCRText, error) {
	var parsed pyOCROutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("documents: parse OCR output: %w", err)
	}
	pages := make([]pageOCRText, 0, len(parsed.Pages))
	for _, p := range parsed.Pages {
		pages = append(pages, pageOCRText{Page: p.Page, Text: p.Text})
	}
	return pages, nil
}

// buildOCRSections renders every OCR-recognized page as a labeled
// DocumentSection: "page N (ocr)". A page whose recognized text is
// blank is skipped — pytesseract can return an empty string for a
// blank or unrecognizable page, and an empty section would be noise,
// not signal.
func buildOCRSections(pages []pageOCRText) []DocumentSection {
	var sections []DocumentSection
	for _, p := range pages {
		text := strings.TrimSpace(p.Text)
		if text == "" {
			continue
		}
		sections = append(sections, DocumentSection{
			Label: fmt.Sprintf("page %d (ocr)", p.Page),
			Text:  text,
		})
	}
	return sections
}

// buildPDFOCRCommand builds the shell command line extractOCR runs.
// Pure function (no I/O) so its output shape is unit-testable without
// python installed, mirroring buildTableExtractionCommand. lang is the
// Tesseract language code (e.g. "eng", "fra", or "+"-joined for
// multiple, "eng+fra") — an empty lang omits --lang entirely, which
// leaves pytesseract's own default (English) in effect rather than
// this package inventing a second place that default is spelled out.
func buildPDFOCRCommand(scriptPath, pdfPath string, startPage, endPage int, lang string) string {
	cmd := fmt.Sprintf("python3 %s %s --start %d --end %d",
		shellQuote(scriptPath), shellQuote(pdfPath), startPage, endPage)
	if lang != "" {
		cmd += " --lang " + shellQuote(lang)
	}
	return cmd
}
