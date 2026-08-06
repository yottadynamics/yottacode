package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/documents"
)

// ReadDocumentTool extracts bounded, provenance-labeled text from CSV,
// TSV, JSON, JSONL, XML, and HTML files — a structured alternative to
// read_file for these formats. Where read_file returns a raw cat -n
// dump (which shears a CSV field's embedded newline into a bogus extra
// row, and dumps HTML/XML markup noise verbatim), read_document parses
// the format properly and returns a structure summary plus a bounded,
// row/record-aligned preview.
//
// Read-only, no approval — same trust posture as read_file. Path
// validation and the credential-path denylist happen here, at the
// agent-tool boundary; internal/documents itself is a pure content
// library with no notion of cwd or trust.
type ReadDocumentTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string

	// Registry is the format dispatch table. Nil uses the production
	// Phase A registry (CSV/TSV/JSON/JSONL/XML/HTML); tests can inject
	// a fake.
	Registry *documents.Registry
}

func (t *ReadDocumentTool) registry() *documents.Registry {
	if t.Registry != nil {
		return t.Registry
	}
	return documents.NewRegistry()
}

func (t *ReadDocumentTool) Name() string { return "read_document" }

func (t *ReadDocumentTool) Description() string {
	return "Extract bounded, structured text from a CSV, TSV, JSON, JSONL, XML, or HTML file. " +
		"Returns a structure summary (columns, JSON shape, XML/HTML title and headings) plus a capped content preview, " +
		"with every truncation reported explicitly rather than silently cut. " +
		"Use this when you need to ANALYZE the data — read_file's raw line-based view shears a CSV field's " +
		"embedded newline into a bogus extra row and returns HTML/XML markup noise verbatim. " +
		"Keep using read_file when you intend to EDIT the file: only read_file's cat -n output feeds " +
		"exact-string edit_file and anchored edit_anchored, and this tool's reformatted preview cannot be used that way. " +
		"That matters most for .json/.xml/.html, which are often source files you are editing rather than data you are reading. " +
		"Handles real-world export quirks: a UTF-8 BOM is stripped, a semicolon/tab/pipe-delimited .csv is detected rather than collapsed into one column, " +
		"and a file with no header row is recognized instead of losing its first record to the column names. " +
		"Use max_rows/max_chars to resize the preview, offset to page past the rows/records you have already seen, " +
		"max_bytes to read further into a file that hit the byte cap, " +
		"and has_header to override header detection when it guessed wrong."
}

func (t *ReadDocumentTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or cwd-relative path to a .csv, .tsv, .json, .jsonl, .xml, .html, or .htm file",
			},
			"max_rows": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum CSV/TSV/JSONL rows or records to sample (default %d).", documents.DefaultMaxRows),
			},
			"max_chars": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum characters of extracted text to return (default %d).", documents.DefaultMaxChars),
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Where the preview window starts (default 0). Counted in data rows for CSV/TSV, records for JSONL, and characters for JSON/XML/HTML. Page through a large file by repeating the call with offset advanced by the number of rows/records you got back; every result labels the window it returned.",
			},
			"has_header": map[string]any{
				"type":        "boolean",
				"description": "CSV/TSV only: whether the first row holds column names. Omit to auto-detect — a first row containing a number is read as data, not a header. Set it explicitly when the result's header looks wrong.",
			},
			"max_bytes": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf(
					"Maximum bytes to read from the source file (default %d, ceiling %d). Raise this when a result warns that the file exceeded the byte cap.",
					documents.DefaultMaxBytes, documents.MaxAllowedBytes),
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadDocumentTool) RequiresApproval(string) bool { return false }
func (t *ReadDocumentTool) ParallelSafe(string) bool     { return true }

type readDocumentArgs struct {
	Path     string `json:"path"`
	MaxRows  int    `json:"max_rows"`
	MaxChars int    `json:"max_chars"`
	MaxBytes int64  `json:"max_bytes"`
	Offset   int    `json:"offset"`

	// HasHeader is a pointer so an omitted key stays distinguishable
	// from an explicit false: absent means "auto-detect", which is a
	// different instruction than "this file has no header".
	HasHeader *bool `json:"has_header"`
}

func (t *ReadDocumentTool) PreviewCall(argsJSON string) string {
	var a readDocumentArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("read_document(%s)", a.Path)
}

func (t *ReadDocumentTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a readDocumentArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("read_document: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("read_document: path is required")
	}

	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("read_document: %w", err)
	}

	ex := t.registry().Lookup(p)
	if ex == nil {
		return "", fmt.Errorf("read_document: %w", documents.ErrUnsupported)
	}

	res, err := ex.Extract(ctx, documents.ExtractRequest{
		Path:      p,
		MaxRows:   a.MaxRows,
		MaxChars:  a.MaxChars,
		MaxBytes:  a.MaxBytes,
		Offset:    a.Offset,
		HasHeader: a.HasHeader,
	})
	if err != nil {
		return "", fmt.Errorf("read_document: %w", err)
	}

	// withDefaults silently clamps an over-ceiling MaxBytes, which would
	// otherwise leave the model believing it got the allowance it asked
	// for. Say so — the whole point of this tool is that limits are
	// stated, not inferred from a short result.
	if a.MaxBytes > documents.MaxAllowedBytes {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"requested max_bytes %d exceeds the %d-byte ceiling; clamped to the ceiling",
			a.MaxBytes, documents.MaxAllowedBytes))
	}

	return formatDocumentResult(p, res), nil
}

// formatDocumentResult renders an ExtractResult as the tool's text
// output: a metadata header, each labeled section, then any warnings
// last so truncation/degradation is never buried above the fold.
func formatDocumentResult(path string, res documents.ExtractResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %d bytes)\n", path, res.Metadata.Kind, res.Metadata.SizeBytes)
	if res.Metadata.Shape != "" {
		fmt.Fprintf(&b, "shape: %s\n", res.Metadata.Shape)
	}
	if len(res.Metadata.Columns) > 0 {
		fmt.Fprintf(&b, "columns: %s\n", strings.Join(res.Metadata.Columns, ", "))
	}
	if res.Metadata.RowCount > 0 {
		fmt.Fprintf(&b, "rows: %d\n", res.Metadata.RowCount)
	}

	for _, sec := range res.Sections {
		fmt.Fprintf(&b, "\n[%s]\n%s\n", sec.Label, sec.Text)
	}

	if len(res.Warnings) > 0 {
		b.WriteString("\nwarnings:\n")
		for _, w := range res.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
