package documents

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CSVExtractor parses comma- or tab-delimited files into a header +
// sampled-rows preview. It uses encoding/csv rather than line-splitting
// so a quoted field containing an embedded delimiter or newline is
// parsed as one logical row instead of being sheared into a bogus extra
// row.
type CSVExtractor struct {
	// Delimiter is the field separator: ',' for CSV, '\t' for TSV.
	Delimiter rune

	ext  string // file extension this instance matches, e.g. ".csv"
	kind string // DocumentMetadata.Kind value, e.g. "csv"
}

// NewCSVExtractor returns a comma-delimited .csv extractor.
func NewCSVExtractor() *CSVExtractor {
	return &CSVExtractor{Delimiter: ',', ext: ".csv", kind: "csv"}
}

// NewTSVExtractor returns a tab-delimited .tsv extractor.
func NewTSVExtractor() *CSVExtractor {
	return &CSVExtractor{Delimiter: '\t', ext: ".tsv", kind: "tsv"}
}

func (e *CSVExtractor) Match(path string) bool {
	return strings.EqualFold(filepath.Ext(path), e.ext)
}

func (e *CSVExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	req = req.withDefaults()

	info, err := os.Stat(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}

	f, err := os.Open(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	defer f.Close()

	limited := io.LimitReader(f, req.MaxBytes)
	r := csv.NewReader(limited)
	r.Comma = e.Delimiter
	// Real-world exports are inconsistent about field counts (trailing
	// optional columns, ragged data rows) — don't fail the whole read
	// over that; the model can see the partial row in context.
	r.FieldsPerRecord = -1

	var (
		header   []string
		rows     [][]string
		warnings []string
		rowCount int
	)

	for {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, err
		}
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A malformed record (unterminated quote, etc.) stops the
			// stream here — encoding/csv can't resync mid-file. Report
			// what was read instead of failing the whole call.
			warnings = append(warnings, fmt.Sprintf("stopped parsing after row %d: %v", rowCount, err))
			break
		}
		if header == nil {
			header = record
			continue
		}
		rowCount++
		if len(rows) < req.MaxRows {
			rows = append(rows, record)
		}
	}

	text, rowsWritten, charTruncated := formatCSVRows(header, rows, req.MaxChars)

	if rowsWritten < rowCount || charTruncated {
		var reasons []string
		if len(rows) < rowCount {
			reasons = append(reasons, fmt.Sprintf("%d-row sample cap", req.MaxRows))
		}
		if charTruncated {
			reasons = append(reasons, fmt.Sprintf("%d-character preview cap", req.MaxChars))
		}
		warnings = append(warnings, fmt.Sprintf("showing the first %d of %d data rows read (%s)", rowsWritten, rowCount, strings.Join(reasons, ", ")))
	}
	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the %d-byte cap", req.MaxBytes, req.MaxBytes))
	}

	label := fmt.Sprintf("rows 1-%d", rowsWritten)
	if rowsWritten == 0 {
		label = "no data rows"
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      e.kind,
			SizeBytes: info.Size(),
			Columns:   header,
			RowCount:  rowCount,
			Shape:     fmt.Sprintf("%d columns", len(header)),
		},
		Sections: []DocumentSection{{Label: label, Text: text}},
		Warnings: warnings,
	}, nil
}

// formatCSVRows renders a header and sampled rows as a compact
// pipe-delimited preview, stopping before maxChars rather than cutting
// a row in half — except when a single row (typically the header)
// alone exceeds maxChars, in which case that row is rune-safely
// truncated via boundedString so the caller can still report it.
//
// Returns the rendered text, how many entries from rows were actually
// written (independent of len(rows): the character budget can stop
// this short of the full sample, and the caller needs the real count
// to keep its row-count label and RowCount metadata honest), and
// whether anything was cut so the caller can warn — the char-budget
// path used to cut silently, which is exactly the bug this return
// value exists to prevent.
func formatCSVRows(header []string, rows [][]string, maxChars int) (text string, rowsWritten int, truncated bool) {
	var b strings.Builder
	first := true
	writeLine := func(line string) bool {
		if !first {
			if b.Len()+1+len(line) > maxChars {
				return false
			}
			b.WriteByte('\n')
			b.WriteString(line)
			return true
		}
		first = false
		if len(line) > maxChars {
			b.WriteString(boundedString(line, maxChars))
			return false
		}
		b.WriteString(line)
		return true
	}

	if header != nil {
		if !writeLine(strings.Join(header, " | ")) {
			return b.String(), 0, true
		}
	}
	for _, row := range rows {
		if !writeLine(strings.Join(row, " | ")) {
			return b.String(), rowsWritten, true
		}
		rowsWritten++
	}
	return b.String(), rowsWritten, false
}
