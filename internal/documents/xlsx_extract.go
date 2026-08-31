package documents

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// XLSXExtractor extracts bounded, sheet-labeled text from xlsx
// workbooks via excelize — the same library GenerateXLSX uses for
// writing, so reading and writing xlsx share one dependency.
type XLSXExtractor struct{}

func (e *XLSXExtractor) Match(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".xlsx")
}

func (e *XLSXExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	req = req.withDefaults()

	info, err := os.Stat(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	// xlsx is a zip archive; excelize.OpenFile decompresses it in one
	// shot rather than streaming, so (unlike the CSV/JSON/XML/HTML
	// extractors' io.LimitReader-based caps) the size cap has to be a
	// pre-open gate instead of an in-flight one.
	if info.Size() > req.MaxBytes {
		return ExtractResult{}, fmt.Errorf("documents: xlsx %q (%d bytes) exceeds the %d-byte read cap", req.Path, info.Size(), req.MaxBytes)
	}

	f, err := excelize.OpenFile(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: open xlsx: %w", err)
	}
	defer f.Close()

	sheetNames := f.GetSheetList()

	// Pass 1: load every sheet's rows once and get an accurate grand
	// total up front. excelize.OpenFile already decompressed the whole
	// (size-capped) workbook into memory, so GetRows reads from that —
	// this isn't a second disk read, just building the row totals a
	// single capped pass through the data can't produce without
	// double-counting whichever sheet a cap happens to interrupt.
	allRows := make([][][]string, len(sheetNames))
	totalRows := 0
	var warnings []string
	for i, name := range sheetNames {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, err
		}
		rows, err := f.GetRows(name)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("sheet %q: %v", name, err))
			continue
		}
		allRows[i] = rows
		totalRows += len(rows)
	}

	if req.Offset >= totalRows && totalRows > 0 {
		warnings = append(warnings, fmt.Sprintf("offset %d is past the last row (%d total)", req.Offset, totalRows))
	}

	// Pass 2: walk the already-loaded rows, applying offset/MaxRows/
	// MaxChars against the accurate totals pass 1 established. The inner
	// loop's break only ever exits the row loop (never the outer sheet
	// loop directly) so the current sheet's accumulated lines always get
	// flushed to sections below before capped is checked — a labeled
	// break out of both loops at once would skip that flush and silently
	// drop whatever the interrupted sheet had already accumulated.
	var sections []DocumentSection
	skipped, used, charsUsed := 0, 0, 0
	capped := false
	for i, name := range sheetNames {
		if capped {
			break
		}
		var lines []string
		var formulaLines []string
		for rowIdx, row := range allRows[i] {
			if skipped < req.Offset {
				skipped++
				continue
			}
			if used >= req.MaxRows {
				capped = true
				break
			}
			line := strings.Join(row, " | ")
			included := false
			switch {
			case charsUsed > 0 && charsUsed+1+len(line) > req.MaxChars:
				capped = true
			case charsUsed == 0 && len(line) > req.MaxChars:
				lines = append(lines, boundedString(line, req.MaxChars))
				used++
				capped = true
				included = true
			default:
				lines = append(lines, line)
				charsUsed += len(line) + 1
				used++
				included = true
			}
			if included {
				// GetRows only ever returns a cell's cached computed
				// value; a caller asking "what actually computes this
				// column" needs this separate per-cell GetCellFormula
				// lookup. Scoped to exactly the rows the preview above
				// just included, so formulas page in lockstep with the
				// data instead of exposing a wider or narrower window.
				formulaLines = append(formulaLines, sheetRowFormulas(f, name, rowIdx, len(row))...)
			}
			if capped {
				break
			}
		}
		if len(lines) > 0 {
			sections = append(sections, DocumentSection{Label: "sheet " + name, Text: strings.Join(lines, "\n")})
		}
		var metadataSections []DocumentSection
		if len(formulaLines) > 0 {
			metadataSections = append(metadataSections, DocumentSection{Label: "sheet " + name + " formulas", Text: strings.Join(formulaLines, "\n")})
		}
		// Sheet-structure metadata is independent of the visible row window:
		// image-only sheets and sheets skipped by offset still have useful
		// metadata, so do not gate these sections on len(lines). They still
		// share the same MaxChars preview budget as row/formula text, so a
		// workbook with many merges or image alt-text cannot bypass the cap.
		if sec, ok := sheetMergedCellSection(f, name); ok {
			metadataSections = append(metadataSections, sec)
		}
		metadataSections = append(metadataSections, sheetImageSections(f, name)...)
		sections, warnings = appendSectionsWithinCharCap(sections, warnings, metadataSections, req.MaxChars, "xlsx metadata section")
		if sectionsTextLen(sections) >= req.MaxChars {
			capped = true
		}
	}

	if capped {
		warnings = append(warnings, fmt.Sprintf("showing %d of %d total rows across %d sheets (row/character preview cap)", used, totalRows, len(sheetNames)))
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "xlsx",
			SizeBytes: info.Size(),
			RowCount:  totalRows,
			Shape:     fmt.Sprintf("%d sheets: %s", len(sheetNames), strings.Join(sheetNames, ", ")),
		},
		Sections: sections,
		Warnings: warnings,
	}, nil
}

// sheetRowFormulas returns "REF: =formula" lines for every cell in row
// rowIdx (0-based) of sheet name that has a formula. rowIdx is an
// index into the sheet's full row list (as returned by GetRows), so
// coordinates are computed directly — unaffected by how much of that
// row list the caller's offset/cap window actually kept.
func sheetRowFormulas(f *excelize.File, sheet string, rowIdx, cols int) []string {
	var out []string
	for c := range cols {
		ref, err := excelize.CoordinatesToCellName(c+1, rowIdx+1)
		if err != nil {
			continue
		}
		formula, err := f.GetCellFormula(sheet, ref)
		if err != nil || formula == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s: =%s", ref, formula))
	}
	return out
}

// sheetMergedCellSection renders sheet name's merged ranges (if any)
// as one "sheet <name> merged cells" DocumentSection — GetRows
// silently collapses a merge to its top-left cell's value with every
// other cell in the range blank, so without this a merged header
// reads as mostly-empty cells with no indication why. Reported
// whenever the sheet produced any visible data (see the caller),
// independent of the row offset/cap window — merges describe sheet
// structure, not a specific row range.
func sheetMergedCellSection(f *excelize.File, sheet string) (DocumentSection, bool) {
	merges, err := f.GetMergeCells(sheet)
	if err != nil || len(merges) == 0 {
		return DocumentSection{}, false
	}
	var lines []string
	for _, m := range merges {
		start, end := m.GetStartAxis(), m.GetEndAxis()
		if value := strings.TrimSpace(m.GetCellValue()); value != "" {
			lines = append(lines, fmt.Sprintf("%s:%s: %s", start, end, value))
		} else {
			lines = append(lines, fmt.Sprintf("%s:%s", start, end))
		}
	}
	return DocumentSection{Label: "sheet " + sheet + " merged cells", Text: strings.Join(lines, "\n")}, true
}

// sheetImageSections renders one "sheet <name> image <cell>-<n>"
// DocumentSection per embedded picture anchored on sheet name —
// extension, pixel dimensions, and alt text, never image bytes, same
// contract imageSectionText's callers already follow for pptx/docx.
// Pixel dimensions (not EMU-derived inches, unlike pptx/docx) come
// from decoding the picture's own header (image.DecodeConfig — header
// only, not a full decode): excelize's Picture/GraphicOptions expose
// placement offsets and a display scale factor for a read picture, but
// no absolute width/height of their own to convert from.
func sheetImageSections(f *excelize.File, sheet string) []DocumentSection {
	cells, err := f.GetPictureCells(sheet)
	if err != nil || len(cells) == 0 {
		return nil
	}
	var sections []DocumentSection
	for _, cell := range cells {
		pics, err := f.GetPictures(sheet, cell)
		if err != nil {
			continue
		}
		for i, pic := range pics {
			var parts []string
			if ext := strings.TrimPrefix(pic.Extension, "."); ext != "" {
				parts = append(parts, "type: "+ext)
			}
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(pic.File)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
				parts = append(parts, fmt.Sprintf("size: %dx%d px", cfg.Width, cfg.Height))
			}
			if pic.Format != nil {
				if alt := strings.TrimSpace(pic.Format.AltText); alt != "" {
					parts = append(parts, "alt: "+alt)
				}
			}
			if len(parts) == 0 {
				continue
			}
			sections = append(sections, DocumentSection{
				Label: fmt.Sprintf("sheet %s image %s-%d", sheet, cell, i+1),
				Text:  strings.Join(parts, ", "),
			})
		}
	}
	return sections
}
