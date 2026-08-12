package documents

import (
	"context"
	"fmt"
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
		return ExtractResult{
			Metadata: DocumentMetadata{
				Kind:      "xlsx",
				SizeBytes: info.Size(),
				RowCount:  totalRows,
				Shape:     fmt.Sprintf("%d sheets: %s", len(sheetNames), strings.Join(sheetNames, ", ")),
			},
			Warnings: warnings,
		}, nil
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
		for _, row := range allRows[i] {
			if skipped < req.Offset {
				skipped++
				continue
			}
			if used >= req.MaxRows {
				capped = true
				break
			}
			line := strings.Join(row, " | ")
			switch {
			case charsUsed > 0 && charsUsed+1+len(line) > req.MaxChars:
				capped = true
			case charsUsed == 0 && len(line) > req.MaxChars:
				lines = append(lines, boundedString(line, req.MaxChars))
				used++
				capped = true
			default:
				lines = append(lines, line)
				charsUsed += len(line) + 1
				used++
			}
			if capped {
				break
			}
		}
		if len(lines) > 0 {
			sections = append(sections, DocumentSection{Label: "sheet " + name, Text: strings.Join(lines, "\n")})
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
