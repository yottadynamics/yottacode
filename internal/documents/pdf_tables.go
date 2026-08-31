package documents

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ScriptResolver resolves a PyHelperScript to an invocable filesystem
// path, given whatever Sandbox-awareness the caller has — mirrors
// CommandRunner's own caller-supplies-the-seam shape (see pdf.go's
// CommandRunner doc comment for the dependency-direction reasoning: this
// package defines the seam, internal/agent supplies the implementation
// via documents.ResolvePyHelperScript). A nil ScriptResolver means the
// optional tier that needs it is unavailable, same as a nil
// CommandRunner today.
type ScriptResolver func(script PyHelperScript) (string, error)

// pyTableExtractionOutput mirrors pyhelpers/extract_pdf_tables.py's
// stdout JSON shape exactly.
type pyTableExtractionOutput struct {
	Pages []struct {
		Page   int `json:"page"`
		Tables []struct {
			Rows [][]string `json:"rows"`
		} `json:"tables"`
		Images []struct {
			WidthPt     float64 `json:"width_pt"`
			HeightPt    float64 `json:"height_pt"`
			SrcWidthPx  int     `json:"src_width_px"`
			SrcHeightPx int     `json:"src_height_px"`
		} `json:"images"`
	} `json:"pages"`
}

// pdfImage is one image pdfplumber found on a PDF page: its placed
// size on the page (WidthPt/HeightPt, PDF points) and, when
// pdfplumber could determine it, the source image's own intrinsic
// pixel resolution (SrcWidthPx/SrcHeightPx, both 0 when unknown).
// Never the image bytes themselves.
type pdfImage struct {
	WidthPt, HeightPt       float64
	SrcWidthPx, SrcHeightPx int
}

// pagePDFExtraction is one PDF page's detected tables and images,
// 1-indexed to match pdftotext's own page numbering used elsewhere in
// this file. Each table is plain rows — pdfplumber doesn't distinguish
// a header row from body rows, so unlike Block's table type (built for
// generation, where the caller supplies an explicit header) this has
// none to preserve. Tables and images travel together because both
// come from one extract_pdf_tables.py invocation (see that script's
// own doc comment for why they ride in one subprocess call).
type pagePDFExtraction struct {
	Page   int
	Tables [][][]string
	Images []pdfImage
}

// parsePythonTableJSON parses extract_pdf_tables.py's stdout. Pure
// function — no I/O, unit-testable with canned JSON.
func parsePythonTableJSON(out []byte) ([]pagePDFExtraction, error) {
	var parsed pyTableExtractionOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("documents: parse table-extraction output: %w", err)
	}
	pages := make([]pagePDFExtraction, 0, len(parsed.Pages))
	for _, p := range parsed.Pages {
		tables := make([][][]string, 0, len(p.Tables))
		for _, t := range p.Tables {
			tables = append(tables, t.Rows)
		}
		images := make([]pdfImage, 0, len(p.Images))
		for _, img := range p.Images {
			images = append(images, pdfImage{
				WidthPt: img.WidthPt, HeightPt: img.HeightPt,
				SrcWidthPx: img.SrcWidthPx, SrcHeightPx: img.SrcHeightPx,
			})
		}
		pages = append(pages, pagePDFExtraction{Page: p.Page, Tables: tables, Images: images})
	}
	return pages, nil
}

// buildTableSections renders every detected table across pages as
// labeled DocumentSections, one per table: "page N table M". Row cells
// are pipe-joined, matching xlsx_extract.go's own row-formatting
// convention so read_document's table output reads consistently across
// formats. Pages/tables with zero rows are skipped — pdfplumber can
// return an empty table for a false-positive detection, and an empty
// section would be noise, not signal.
func buildTableSections(pages []pagePDFExtraction) []DocumentSection {
	var sections []DocumentSection
	for _, p := range pages {
		for i, rows := range p.Tables {
			if len(rows) == 0 {
				continue
			}
			var b strings.Builder
			for r, row := range rows {
				if r > 0 {
					b.WriteString("\n")
				}
				b.WriteString(strings.Join(row, " | "))
			}
			sections = append(sections, DocumentSection{
				Label: fmt.Sprintf("page %d table %d", p.Page, i+1),
				Text:  b.String(),
			})
		}
	}
	return sections
}

// buildPDFImageSections renders every image pdfplumber found across
// pages as a labeled DocumentSection: "page N image M". Size is
// reported in inches (points / 72), matching the unit pptx/docx image
// sections already use; the source image's own pixel resolution is
// appended when pdfplumber could determine it. Metadata only — never
// image bytes, the same contract imageSectionText's callers follow for
// docx/pptx/xlsx.
func buildPDFImageSections(pages []pagePDFExtraction) []DocumentSection {
	var sections []DocumentSection
	for _, p := range pages {
		for i, img := range p.Images {
			var parts []string
			if img.WidthPt > 0 && img.HeightPt > 0 {
				text := fmt.Sprintf("size: %.2fin x %.2fin", img.WidthPt/72, img.HeightPt/72)
				if img.SrcWidthPx > 0 && img.SrcHeightPx > 0 {
					text += fmt.Sprintf(" (%dx%d px source)", img.SrcWidthPx, img.SrcHeightPx)
				}
				parts = append(parts, text)
			}
			if len(parts) == 0 {
				continue
			}
			sections = append(sections, DocumentSection{
				Label: fmt.Sprintf("page %d image %d", p.Page, i+1),
				Text:  strings.Join(parts, ", "),
			})
		}
	}
	return sections
}

// buildTableExtractionCommand builds the shell command line
// extractTablesAndImages runs. Pure function (no I/O) so its output shape is
// unit-testable without python installed, mirroring
// internal/agent's buildPandocCommand.
func buildTableExtractionCommand(scriptPath, pdfPath string, startPage, endPage int) string {
	return fmt.Sprintf("python3 %s %s --start %d --end %d",
		shellQuote(scriptPath), shellQuote(pdfPath), startPage, endPage)
}
