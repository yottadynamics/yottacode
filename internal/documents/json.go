package documents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// JSONExtractor handles both .json (one document: object, array, or
// scalar) and .jsonl (one independent JSON value per line). JSON gets a
// top-level shape summary before the content preview; JSONL samples
// records and labels each with its source line number so the model can
// cite an exact line back to the user.
type JSONExtractor struct{}

func (e *JSONExtractor) Match(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".json" || ext == ".jsonl"
}

func (e *JSONExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	req = req.withDefaults()

	info, err := os.Stat(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}

	if strings.EqualFold(filepath.Ext(req.Path), ".jsonl") {
		res, _, err := e.extractJSONL(ctx, req, info)
		return res, err
	}
	return e.extractJSON(ctx, req, info)
}

// retryAsJSONL re-reads the file line-oriented after whole-document
// parsing failed. A .json holding one JSON value per line is a common
// mislabelled export, and read as a single document it doesn't merely
// degrade — it errors out, so the entire file reads as unusable.
//
// The bar for accepting the reinterpretation is deliberately strict:
// every non-blank line must parse and there must be more than one of
// them. Anything less and the original "invalid JSON" error is the more
// useful answer, so it stands. Sections are not part of the test —
// a large Offset can legitimately empty the window of a real JSONL file.
func (e *JSONExtractor) retryAsJSONL(ctx context.Context, req ExtractRequest, info os.FileInfo) (ExtractResult, bool) {
	res, malformed, err := e.extractJSONL(ctx, req, info)
	if err != nil || malformed > 0 || res.Metadata.RowCount < 2 {
		return ExtractResult{}, false
	}
	res.Warnings = append([]string{
		"parsed as JSONL: the .json extension implies a single document, but the file holds one JSON value per line",
	}, res.Warnings...)
	return res, true
}

// extractJSON parses a whole .json document. Structured parsing only
// happens when the file fits inside MaxBytes — a document truncated
// mid-stream at an arbitrary byte offset is not valid JSON, so trying
// to json.Unmarshal it would just surface a confusing syntax error
// instead of the real "the file is bigger than the cap" story. Past
// the cap, this falls back to a bounded raw-text preview instead.
func (e *JSONExtractor) extractJSON(ctx context.Context, req ExtractRequest, info os.FileInfo) (ExtractResult, error) {
	f, err := os.Open(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, req.MaxBytes))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	raw = stripBOMBytes(raw)

	var warnings []string
	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the byte cap before parsing — shape not computed", req.MaxBytes))
		// The window applies on this path too. Rendering raw without it
		// would hand back page 1 no matter what Offset was asked for.
		text, label := windowText(string(raw), req.Offset, req.MaxChars, "raw preview (unparsed)")
		if notice := textWindowNotice(req.Offset, req.MaxChars, len(raw), "raw preview"); notice != "" {
			warnings = append(warnings, notice)
		}
		return ExtractResult{
			Metadata: DocumentMetadata{Kind: "json", SizeBytes: info.Size(), Shape: "unknown (exceeds byte cap)"},
			Sections: []DocumentSection{{Label: label, Text: text}},
			Warnings: warnings,
		}, nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		if alt, ok := e.retryAsJSONL(ctx, req, info); ok {
			return alt, nil
		}
		return ExtractResult{}, fmt.Errorf("documents: invalid JSON: %w", err)
	}

	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		pretty = raw
	}

	// A single JSON document has no rows to page through, so the window
	// is measured in characters of the pretty-printed form — the same
	// unit the preview is already capped in.
	text, label := windowText(string(pretty), req.Offset, req.MaxChars, "document")
	if notice := textWindowNotice(req.Offset, req.MaxChars, len(pretty), "document"); notice != "" {
		warnings = append(warnings, notice)
	}

	return ExtractResult{
		Metadata: DocumentMetadata{Kind: "json", SizeBytes: info.Size(), Shape: summarizeJSONShape(v)},
		Sections: []DocumentSection{{Label: label, Text: text}},
		Warnings: warnings,
	}, nil
}

// extractJSONL streams the file line by line so a huge log-style file
// only ever needs the sampled prefix in memory, not the whole thing.
// extractJSONL also returns how many lines failed to parse, which
// retryAsJSONL uses to decide whether a .json file is really JSONL.
func (e *JSONExtractor) extractJSONL(ctx context.Context, req ExtractRequest, info os.FileInfo) (ExtractResult, int, error) {
	f, err := os.Open(req.Path)
	if err != nil {
		return ExtractResult{}, 0, fmt.Errorf("documents: %w", err)
	}
	defer f.Close()

	limited := io.LimitReader(f, req.MaxBytes)
	reader := bufio.NewReader(limited)
	stripBOMReader(reader)

	var (
		sections   []DocumentSection
		lineNo     int
		totalLines int
		malformed  int
		charsUsed  int
		oversized  int
	)

	for {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, 0, err
		}
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\n")
		if strings.TrimSpace(trimmed) != "" {
			lineNo++
			totalLines++
			// Records before the window are still parsed for their line
			// numbers (labels stay absolute) but never retained.
			if totalLines > req.Offset && len(sections) < req.MaxRows {
				var v any
				switch {
				case json.Unmarshal([]byte(trimmed), &v) != nil:
					malformed++
				case charsUsed+len(trimmed) > req.MaxChars:
					// Skip this one and keep going. Stopping outright
					// here meant a single fat record early in the file
					// suppressed every later one, even when dozens
					// would have fit — labels are absolute line
					// numbers, so the resulting gap is visible rather
					// than misleading.
					oversized++
				default:
					sections = append(sections, DocumentSection{
						Label: fmt.Sprintf("JSONL line %d", lineNo),
						Text:  trimmed,
					})
					charsUsed += len(trimmed)
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return ExtractResult{}, 0, fmt.Errorf("documents: %w", readErr)
		}
	}

	var warnings []string
	switch {
	case req.Offset >= totalLines && totalLines > 0:
		warnings = append(warnings, fmt.Sprintf(
			"offset %d is past the last record (%d records)", req.Offset, totalLines))
	case totalLines > len(sections)+req.Offset:
		// Name which cap stopped the sample, the way the CSV extractor
		// does. "showing 0 of 400 records" with no reason is exactly the
		// silent-truncation failure this package exists to avoid — and
		// the char cap can produce that on a single oversized first
		// record. Malformed lines get their own warning below, so they
		// are deliberately not repeated here.
		var reasons []string
		if len(sections) >= req.MaxRows {
			reasons = append(reasons, fmt.Sprintf("%d-record sample cap", req.MaxRows))
		}
		if oversized > 0 {
			reasons = append(reasons, fmt.Sprintf(
				"%d-character preview cap — %d record(s) too large to include", req.MaxChars, oversized))
		}
		msg := fmt.Sprintf("showing %d of %d records", len(sections), totalLines)
		if req.Offset > 0 {
			msg = fmt.Sprintf("showing records %d-%d of %d",
				req.Offset+1, req.Offset+len(sections), totalLines)
		}
		if len(reasons) > 0 {
			msg += fmt.Sprintf(" (%s)", strings.Join(reasons, ", "))
		}
		warnings = append(warnings, msg)
	}
	if malformed > 0 {
		warnings = append(warnings, fmt.Sprintf("%d malformed line(s) skipped", malformed))
	}
	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the byte cap", req.MaxBytes))
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "jsonl",
			SizeBytes: info.Size(),
			RowCount:  totalLines,
			Shape:     fmt.Sprintf("%d records sampled of %d total", len(sections), totalLines),
		},
		Sections: sections,
		Warnings: warnings,
	}, malformed, nil
}

// summarizeJSONShape renders a one-line structure summary for the
// decoded top-level value: an object's key set, an array's length and
// element type, or a bare scalar's type.
func summarizeJSONShape(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		more := ""
		if len(keys) > 20 {
			keys = keys[:20]
			more = ", …"
		}
		return fmt.Sprintf("object, %d top-level key(s): %s%s", len(t), strings.Join(keys, ", "), more)
	case []any:
		if len(t) == 0 {
			return "empty array"
		}
		return fmt.Sprintf("array of %d element(s) (%s)", len(t), jsonTypeName(t[0]))
	default:
		return fmt.Sprintf("scalar (%s)", jsonTypeName(v))
	}
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}
