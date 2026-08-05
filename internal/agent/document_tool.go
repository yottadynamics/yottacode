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
		"Prefer this over read_file for these formats: read_file's raw line-based view shears a CSV field's " +
		"embedded newline into a bogus extra row and returns HTML/XML markup noise verbatim. " +
		"Use max_rows/max_chars to raise or lower the default preview size."
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
		Path:     p,
		MaxRows:  a.MaxRows,
		MaxChars: a.MaxChars,
	})
	if err != nil {
		return "", fmt.Errorf("read_document: %w", err)
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
