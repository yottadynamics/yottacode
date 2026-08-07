package documents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sniffPeekBytes bounds how much of the file's opening the delimiter
// sniffer inspects. It only ever needs the first line; the allowance is
// generous so a very wide header still fits.
const sniffPeekBytes = 64 * 1024

// candidateDelimiters are the separators sniffDelimiter considers for a
// .csv file. Comma is first so it wins ties as the declared default.
var candidateDelimiters = []rune{',', ';', '\t', '|'}

// CSVExtractor parses comma- or tab-delimited files into a header +
// sampled-rows preview. It uses encoding/csv rather than line-splitting
// so a quoted field containing an embedded delimiter or newline is
// parsed as one logical row instead of being sheared into a bogus extra
// row.
type CSVExtractor struct {
	// Delimiter is the field separator assumed from the extension: ','
	// for CSV, '\t' for TSV. For .csv it is only a starting point —
	// see sniff.
	Delimiter rune

	// sniff enables delimiter detection. It is on for .csv, whose
	// extension says nothing about the actual separator, and off for
	// .tsv, where the extension is unambiguous.
	sniff bool

	ext  string // file extension this instance matches, e.g. ".csv"
	kind string // DocumentMetadata.Kind value, e.g. "csv"
}

// NewCSVExtractor returns a .csv extractor that defaults to comma and
// sniffs for the separator actually in use.
func NewCSVExtractor() *CSVExtractor {
	return &CSVExtractor{Delimiter: ',', sniff: true, ext: ".csv", kind: "csv"}
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
	br := bufio.NewReaderSize(limited, sniffPeekBytes)

	var warnings []string

	stripBOMReader(br)

	delim := e.Delimiter
	if e.sniff {
		if d, ok := sniffDelimiter(br); ok && d != delim {
			warnings = append(warnings, fmt.Sprintf(
				"detected %q as the field delimiter, not %q", string(d), string(delim)))
			delim = d
		}
	}

	r := csv.NewReader(br)
	r.Comma = delim
	// Real-world exports are inconsistent about field counts (trailing
	// optional columns, ragged data rows) — don't fail the whole read
	// over that; the model can see the partial row in context.
	r.FieldsPerRecord = -1

	var (
		header    []string
		rows      [][]string
		rowCount  int
		firstSeen bool
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

		if !firstSeen {
			firstSeen = true
			if hasHeaderRow(record, req.HasHeader) {
				header = record
				continue
			}
			// Consuming a data row as column names both loses the record
			// and mislabels every column, so say what was decided.
			if req.HasHeader == nil {
				warnings = append(warnings, "no header row detected (the first row contains a number); treating every row as data")
			}
		}

		rowCount++
		// Rows before the window are parsed and dropped, never
		// retained — paging deep into a file costs time, not memory.
		if rowCount > req.Offset && len(rows) < req.MaxRows {
			rows = append(rows, record)
		}
	}

	text, rowsWritten, charTruncated := formatCSVRows(header, rows, req.MaxChars)

	switch {
	case req.Offset >= rowCount && rowCount > 0:
		// An empty window is otherwise indistinguishable from an empty
		// file, which would read as "there is no more data" and end the
		// paging loop one page early.
		warnings = append(warnings, fmt.Sprintf(
			"offset %d is past the last data row (%d rows)", req.Offset, rowCount))
	case rowsWritten < rowCount-req.Offset || charTruncated:
		var reasons []string
		if len(rows) < rowCount-req.Offset {
			reasons = append(reasons, fmt.Sprintf("%d-row sample cap", req.MaxRows))
		}
		if charTruncated {
			reasons = append(reasons, fmt.Sprintf("%d-character preview cap", req.MaxChars))
		}
		warnings = append(warnings, fmt.Sprintf("showing rows %d-%d of %d data rows read (%s)",
			req.Offset+1, req.Offset+rowsWritten, rowCount, strings.Join(reasons, ", ")))
	}
	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the %d-byte cap", req.MaxBytes, req.MaxBytes))
	}

	label := fmt.Sprintf("rows %d-%d", req.Offset+1, req.Offset+rowsWritten)
	if rowsWritten == 0 {
		label = "no data rows"
	}

	// Width comes from the first data row when the file has no header,
	// so a headerless export still reports its real column count
	// instead of the 0 that len(header) would give.
	cols := len(header)
	if cols == 0 && len(rows) > 0 {
		cols = len(rows[0])
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      e.kind,
			SizeBytes: info.Size(),
			Columns:   header,
			RowCount:  rowCount,
			Shape:     fmt.Sprintf("%d columns", cols),
		},
		Sections: []DocumentSection{{Label: label, Text: text}},
		Warnings: warnings,
	}, nil
}

// sniffDelimiter inspects the first buffered line and returns the
// separator appearing most often outside quotes. Semicolon-delimited
// .csv is what Excel writes in every European locale, and parsing one
// as comma-delimited collapses each line into a single column — a wrong
// answer that looks exactly like a right one, which is why the caller
// reports the choice rather than applying it silently.
func sniffDelimiter(br *bufio.Reader) (rune, bool) {
	// A short read (or EOF on a tiny file) still yields usable bytes,
	// so the error is deliberately ignored.
	buf, _ := br.Peek(sniffPeekBytes)
	if len(buf) == 0 {
		return 0, false
	}
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i]
	}

	var (
		best  rune
		bestN int
	)
	for _, d := range candidateDelimiters {
		if n := countOutsideQuotes(buf, byte(d)); n > bestN {
			best, bestN = d, n
		}
	}
	return best, bestN > 0
}

// countOutsideQuotes counts d in line, ignoring occurrences inside a
// quoted field so a header like `"last, first",age` doesn't inflate the
// comma count.
func countOutsideQuotes(line []byte, d byte) int {
	var (
		n        int
		inQuotes bool
	)
	for _, c := range line {
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case c == d && !inQuotes:
			n++
		}
	}
	return n
}

// hasHeaderRow decides whether the first record is column names.
// A non-nil forced value settles it; otherwise the call falls back to
// the numeric heuristic below.
func hasHeaderRow(rec []string, forced *bool) bool {
	if forced != nil {
		return *forced
	}
	return !firstRowLooksLikeData(rec)
}

// firstRowLooksLikeData reports whether the opening record reads as data
// rather than column names. Header cells are essentially never bare
// numbers, so a numeric field in row 1 is the strongest signal available
// that the export carries no header. The inverse case — a genuine header
// of bare years like `2024,2025` — is the known false positive, and the
// explicit has_header override exists for it.
func firstRowLooksLikeData(rec []string) bool {
	for _, f := range rec {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, err := strconv.ParseFloat(f, 64); err == nil {
			return true
		}
	}
	return false
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
