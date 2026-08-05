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
		return e.extractJSONL(ctx, req, info)
	}
	return e.extractJSON(req, info)
}

// extractJSON parses a whole .json document. Structured parsing only
// happens when the file fits inside MaxBytes — a document truncated
// mid-stream at an arbitrary byte offset is not valid JSON, so trying
// to json.Unmarshal it would just surface a confusing syntax error
// instead of the real "the file is bigger than the cap" story. Past
// the cap, this falls back to a bounded raw-text preview instead.
func (e *JSONExtractor) extractJSON(req ExtractRequest, info os.FileInfo) (ExtractResult, error) {
	f, err := os.Open(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, req.MaxBytes))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}

	var warnings []string
	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the byte cap before parsing — shape not computed", req.MaxBytes))
		return ExtractResult{
			Metadata: DocumentMetadata{Kind: "json", SizeBytes: info.Size(), Shape: "unknown (exceeds byte cap)"},
			Sections: []DocumentSection{{Label: "raw preview (unparsed)", Text: boundedString(string(raw), req.MaxChars)}},
			Warnings: warnings,
		}, nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ExtractResult{}, fmt.Errorf("documents: invalid JSON: %w", err)
	}

	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		pretty = raw
	}
	if len(pretty) > req.MaxChars {
		warnings = append(warnings, fmt.Sprintf("showing the first %d of %d characters", req.MaxChars, len(pretty)))
	}

	return ExtractResult{
		Metadata: DocumentMetadata{Kind: "json", SizeBytes: info.Size(), Shape: summarizeJSONShape(v)},
		Sections: []DocumentSection{{Label: "document", Text: boundedString(string(pretty), req.MaxChars)}},
		Warnings: warnings,
	}, nil
}

// extractJSONL streams the file line by line so a huge log-style file
// only ever needs the sampled prefix in memory, not the whole thing.
func (e *JSONExtractor) extractJSONL(ctx context.Context, req ExtractRequest, info os.FileInfo) (ExtractResult, error) {
	f, err := os.Open(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	defer f.Close()

	limited := io.LimitReader(f, req.MaxBytes)
	reader := bufio.NewReader(limited)

	var (
		sections     []DocumentSection
		lineNo       int
		totalLines   int
		malformed    int
		charsUsed    int
		stoppedChars bool
	)

	for {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, err
		}
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\n")
		if strings.TrimSpace(trimmed) != "" {
			lineNo++
			totalLines++
			if len(sections) < req.MaxRows && !stoppedChars {
				var v any
				switch {
				case json.Unmarshal([]byte(trimmed), &v) != nil:
					malformed++
				case charsUsed+len(trimmed) > req.MaxChars:
					stoppedChars = true
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
			return ExtractResult{}, fmt.Errorf("documents: %w", readErr)
		}
	}

	var warnings []string
	if totalLines > len(sections) {
		warnings = append(warnings, fmt.Sprintf("showing %d of %d records", len(sections), totalLines))
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
	}, nil
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
