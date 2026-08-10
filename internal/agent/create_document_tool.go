package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/documents"
)

// CreateDocumentTool generates an xlsx, docx, or pdf file from structured
// content. xlsx is generated natively (excelize, no subprocess); docx/pdf
// go through pandoc, routed through the same Sandbox seam RunBashTool
// uses — see roadmap/document-generation.md's "Sandbox integration".
//
// Always requires approval: it writes a new file, same trust class as
// media_render/media_compose.
type CreateDocumentTool struct {
	Cwd       *CwdRef
	WriteOpts WritePathOptions

	// Sandbox is nil-safe: a nil Sandbox behaves exactly like HostSandbox,
	// mirroring RunBashTool.Sandbox. Only consulted for docx/pdf — the
	// xlsx path never shells out.
	Sandbox Sandbox
}

func (t *CreateDocumentTool) sandbox() Sandbox {
	if t.Sandbox != nil {
		return t.Sandbox
	}
	return HostSandbox{}
}

func (t *CreateDocumentTool) Name() string { return "create_document" }

func (t *CreateDocumentTool) Description() string {
	return "Generate an xlsx, docx, or pdf file from structured content. " +
		"For format=xlsx, set content.sheets (native, no external tools required). " +
		"For format=docx or format=pdf, set content.blocks (heading/paragraph/list/table/code); " +
		"generation runs pandoc, which must be reachable through the active command sandbox " +
		"(installed on the host when no sandbox is configured, or present in the sandbox's [sandbox].image). " +
		"pdf additionally requires weasyprint as pandoc's PDF engine. " +
		"Always requires approval; refuses to overwrite an existing file unless overwrite=true."
}

func (t *CreateDocumentTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"format": map[string]any{
				"type":        "string",
				"description": "Output document format: xlsx, docx, or pdf",
			},
			"output_path": map[string]any{
				"type":        "string",
				"description": "Path to write the generated document to",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "Allow replacing an existing output file (default false)",
			},
			"content": map[string]any{
				"type":        "object",
				"description": "Document content: set sheets for format=xlsx, or blocks for format=docx/pdf",
				"properties": map[string]any{
					"sheets": createDocumentSheetsSchema(),
					"blocks": createDocumentBlocksSchema(),
				},
			},
		},
		"required": []string{"format", "output_path", "content"},
	}
}

func createDocumentSheetsSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "xlsx only: one entry per sheet, in tab order",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Sheet tab name; defaults to Sheet1, Sheet2, ..."},
			"rows": map[string]any{
				"type":        "array",
				"description": "Rows top to bottom; each row is cells left to right",
				"items":       map[string]any{"type": "array", "items": createDocumentCellSchema()},
			},
		}},
	}
}

func createDocumentCellSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":         map[string]any{"type": []string{"string", "number", "boolean"}, "description": "Cell literal; ignored when formula is set"},
			"formula":       map[string]any{"type": "string", "description": "Excel formula without the leading '=', e.g. SUM(A1:A2); overrides value"},
			"bold":          map[string]any{"type": "boolean"},
			"italic":        map[string]any{"type": "boolean"},
			"number_format": map[string]any{"type": "string", "description": "Excel number format code, e.g. 0.00% or yyyy-mm-dd"},
		},
	}
}

func createDocumentBlocksSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "docx/pdf only: ordered content blocks",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"type":     map[string]any{"type": "string", "description": "heading, paragraph, list, table, or code"},
			"level":    map[string]any{"type": "integer", "description": "heading only: level 1-6"},
			"text":     map[string]any{"type": "string", "description": "heading/paragraph/code text"},
			"ordered":  map[string]any{"type": "boolean", "description": "list only: numbered (true) vs bulleted (false)"},
			"items":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "list only"},
			"header":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "table only: column headers"},
			"rows":     map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "description": "table only: body rows, one per header column"},
			"language": map[string]any{"type": "string", "description": "code only: fenced code block language tag"},
		}, "required": []string{"type"}},
	}
}

func (t *CreateDocumentTool) RequiresApproval(string) bool { return true }

type createDocumentArgs struct {
	Format     string                    `json:"format"`
	OutputPath string                    `json:"output_path"`
	Overwrite  bool                      `json:"overwrite"`
	Content    createDocumentContentArgs `json:"content"`
}

type createDocumentContentArgs struct {
	Sheets []createDocumentSheetArg `json:"sheets"`
	Blocks []createDocumentBlockArg `json:"blocks"`
}

type createDocumentSheetArg struct {
	Name string                    `json:"name"`
	Rows [][]createDocumentCellArg `json:"rows"`
}

type createDocumentCellArg struct {
	Value        any    `json:"value"`
	Formula      string `json:"formula"`
	Bold         bool   `json:"bold"`
	Italic       bool   `json:"italic"`
	NumberFormat string `json:"number_format"`
}

type createDocumentBlockArg struct {
	Type     string     `json:"type"`
	Level    int        `json:"level"`
	Text     string     `json:"text"`
	Ordered  bool       `json:"ordered"`
	Items    []string   `json:"items"`
	Header   []string   `json:"header"`
	Rows     [][]string `json:"rows"`
	Language string     `json:"language"`
}

func (t *CreateDocumentTool) PreviewCall(argsJSON string) string {
	var a createDocumentArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	preview := fmt.Sprintf("create_document(%s -> %s)", a.Format, a.OutputPath)
	if isPandocFormat(a.Format) {
		if sb := t.sandbox(); sb.Label() != (HostSandbox{}).Label() {
			preview = sb.Label() + " " + preview
		}
	}
	return preview
}

func (t *CreateDocumentTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a createDocumentArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || strings.TrimSpace(a.OutputPath) == "" {
		return nil
	}
	return []string{resolvePath(cwd, a.OutputPath)}
}

func isPandocFormat(format string) bool {
	return format == "docx" || format == "pdf"
}

func (t *CreateDocumentTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a createDocumentArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("create_document: invalid args: %w", err)
	}
	if a.Format != "xlsx" && !isPandocFormat(a.Format) {
		return "", fmt.Errorf("create_document: format must be xlsx, docx, or pdf, got %q", a.Format)
	}
	if strings.TrimSpace(a.OutputPath) == "" {
		return "", errors.New("create_document: output_path is required")
	}

	cwd := t.Cwd.Get()
	output := resolvePath(cwd, a.OutputPath)
	if err := ValidateWritePath(output, t.WriteOpts); err != nil {
		return "", fmt.Errorf("create_document: output: %w", err)
	}
	sb := t.sandbox()
	if isPandocFormat(a.Format) && sb.Label() != (HostSandbox{}).Label() && !pathUnder(resolveWriteTarget(output), canonicalExisting(cwd)) {
		return "", fmt.Errorf("create_document: output %q is outside the sandbox-mounted workspace %q; docx/pdf generation through %s must write inside the current workspace", output, cwd, sb.Label())
	}
	if !a.Overwrite {
		if _, err := os.Stat(output); err == nil {
			return "", fmt.Errorf("create_document: output %q already exists; set overwrite=true to replace it", output)
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return "", fmt.Errorf("create_document: mkdir output dir: %w", err)
	}

	if a.Format == "xlsx" {
		return t.generateXLSX(a, output)
	}
	return t.generateViaPandoc(ctx, a, output, cwd)
}

func (t *CreateDocumentTool) generateXLSX(a createDocumentArgs, output string) (string, error) {
	if len(a.Content.Sheets) == 0 {
		return "", errors.New("create_document: content.sheets is required for format=xlsx")
	}
	data, err := documents.GenerateXLSX(toSheetModel(a.Content.Sheets))
	if err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	if err := os.WriteFile(output, data, 0o644); err != nil {
		return "", fmt.Errorf("create_document: write output: %w", err)
	}
	return "generated xlsx (native): " + output + "\n", nil
}

// createDocumentMaxOutputBytes caps pandoc's captured stdout/stderr,
// matching the 256 KiB ceiling used elsewhere for subprocess output
// (mediaMaxOutputBytes, runBashMaxStreamBytes).
const createDocumentMaxOutputBytes = 256 * 1024

func (t *CreateDocumentTool) generateViaPandoc(ctx context.Context, a createDocumentArgs, output, cwd string) (string, error) {
	if len(a.Content.Blocks) == 0 {
		return "", fmt.Errorf("create_document: content.blocks is required for format=%s", a.Format)
	}
	md, err := documents.RenderMarkdown(toDocAST(a.Content.Blocks))
	if err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}

	sb := t.sandbox()
	if err := checkCommandAvailable(ctx, sb, cwd, "pandoc"); err != nil {
		return "", err
	}
	if a.Format == "pdf" {
		if err := checkCommandAvailable(ctx, sb, cwd, "weasyprint"); err != nil {
			return "", err
		}
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(output), ".yc-docgen-*.md")
	if err != nil {
		return "", fmt.Errorf("create_document: create temp markdown file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.WriteString(md); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("create_document: write temp markdown file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("create_document: close temp markdown file: %w", err)
	}

	cmdLine := buildPandocCommand(a.Format, output, tmpPath)
	c := sb.Command(ctx, cmdLine, cwd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &capped{buf: &stdout, max: createDocumentMaxOutputBytes}
	c.Stderr = &capped{buf: &stderr, max: createDocumentMaxOutputBytes}
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			note := ""
			if exitErr.ExitCode() == podmanInfraExitCode && sb.Label() == podmanSandboxLabel {
				note = "NOTE: exit=125 is podman's own convention for a podman-level failure (not pandoc's exit code) — the sandbox container itself may need attention (see /sandbox). "
			}
			return "", fmt.Errorf("create_document: %spandoc failed (exit=%d): %s", note, exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("create_document: pandoc: %w", err)
	}
	label := sb.Label()
	if label == (HostSandbox{}).Label() {
		label = "host"
	}
	return fmt.Sprintf("generated %s via pandoc (%s): %s\n", a.Format, label, output), nil
}

// buildPandocCommand builds the /bin/sh -c command line Sandbox.Command
// runs. Pure function (no I/O) so its output shape is unit-testable
// without pandoc installed.
func buildPandocCommand(format, output, mdPath string) string {
	switch format {
	case "docx":
		return fmt.Sprintf("pandoc -f markdown -t docx -o %s %s", shellQuoteSingle(output), shellQuoteSingle(mdPath))
	case "pdf":
		return fmt.Sprintf("pandoc -f markdown -t pdf --pdf-engine=weasyprint -o %s %s", shellQuoteSingle(output), shellQuoteSingle(mdPath))
	default:
		return ""
	}
}

// checkCommandAvailable probes for name through sb rather than a host-only
// exec.LookPath — the whole point of routing document-tool subprocess
// calls through the Sandbox seam is that a podman-backed sandbox has its
// own PATH, independent of the host's. See
// roadmap/document-generation.md's "Sandbox integration".
func checkCommandAvailable(ctx context.Context, sb Sandbox, cwd, name string) error {
	c := sb.Command(ctx, "command -v "+shellQuoteSingle(name), cwd)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	if err := c.Run(); err != nil {
		where := sb.Label()
		if where == (HostSandbox{}).Label() {
			where = "host PATH"
		}
		return fmt.Errorf("create_document: %s not found (checked via %s); install it, or point [sandbox].image at an image that includes it — see docs/document-generation.md for a reference Containerfile", name, where)
	}
	return nil
}

// shellQuoteSingle wraps s in POSIX single quotes so it is safe to embed
// as one argument in a Sandbox.Command shell string, escaping any embedded
// single quote as '\”  (close quote, escaped literal quote, reopen quote).
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func toSheetModel(sheets []createDocumentSheetArg) documents.SheetModel {
	out := documents.SheetModel{Sheets: make([]documents.Sheet, len(sheets))}
	for i, s := range sheets {
		rows := make([][]documents.Cell, len(s.Rows))
		for r, row := range s.Rows {
			cells := make([]documents.Cell, len(row))
			for c, cell := range row {
				cells[c] = documents.Cell{
					Value:        cell.Value,
					Formula:      cell.Formula,
					Bold:         cell.Bold,
					Italic:       cell.Italic,
					NumberFormat: cell.NumberFormat,
				}
			}
			rows[r] = cells
		}
		out.Sheets[i] = documents.Sheet{Name: s.Name, Rows: rows}
	}
	return out
}

func toDocAST(blocks []createDocumentBlockArg) documents.DocAST {
	out := documents.DocAST{Blocks: make([]documents.Block, len(blocks))}
	for i, b := range blocks {
		out.Blocks[i] = documents.Block{
			Type:     b.Type,
			Level:    b.Level,
			Text:     b.Text,
			Ordered:  b.Ordered,
			Items:    b.Items,
			Header:   b.Header,
			Rows:     b.Rows,
			Language: b.Language,
		}
	}
	return out
}
