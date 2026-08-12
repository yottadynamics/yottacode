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
	"time"

	"github.com/yottadynamics/yottacode/internal/documents"
)

// CreateDocumentTool generates an xlsx, docx, pdf, or pptx file from
// structured content. xlsx and pptx are generated natively in Go; docx/pdf
// go through pandoc, routed through the same Sandbox seam RunBashTool uses
// — see roadmap/document-generation.md's "Sandbox integration".
//
// Always requires approval: it writes a new file, same trust class as
// media_render/media_compose.
type CreateDocumentTool struct {
	Cwd       *CwdRef
	WriteOpts WritePathOptions

	// DenyReadPaths is the credential-path denylist for docx/pdf image
	// blocks (the only input read path this tool has — output_path is a
	// write). Mirrors MediaRenderTool.DenyReadPaths; typically
	// DefaultDenyReadPaths(cwd).
	DenyReadPaths []string

	// Sandbox is nil-safe: a nil Sandbox behaves exactly like HostSandbox,
	// mirroring RunBashTool.Sandbox. Only consulted for docx/pdf — xlsx and
	// pptx are native Go paths and never shell out.
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
	return "Generate an xlsx, docx, pdf, or pptx file from structured content. " +
		"For format=xlsx, set content.sheets (native, no external tools required). " +
		"For format=docx or format=pdf, set content.blocks (heading/paragraph/list/table/code/image); " +
		"a heading/paragraph/list-item can use plain text or spans (bold/italic inline formatting) " +
		"and an image block references an existing local file by path (validated the same way read_file " +
		"validates a read path — denied credential paths, no path traversal). " +
		"generation runs pandoc, which must be reachable through the active command sandbox " +
		"(installed on the host when no sandbox is configured, or present in the sandbox's [sandbox].image). " +
		"pdf additionally requires weasyprint as pandoc's PDF engine. " +
		"For format=pptx, set content.slides (title/bullets/notes/layout/image per slide); " +
		"an image references an existing local file by path (validated the same way an image block is); " +
		"generation is native Go and needs no python3/python-pptx runtime. " +
		"Always requires approval; refuses to overwrite an existing file unless overwrite=true."
}

func (t *CreateDocumentTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"format": map[string]any{
				"type":        "string",
				"description": "Output document format: xlsx, docx, pdf, or pptx",
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
				"description": "Document content: set sheets for format=xlsx, blocks for format=docx/pdf, or slides for format=pptx",
				"properties": map[string]any{
					"sheets": createDocumentSheetsSchema(),
					"blocks": createDocumentBlocksSchema(),
					"slides": createDocumentSlidesSchema(),
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
			"type":       map[string]any{"type": "string", "description": "heading, paragraph, list, table, code, or image"},
			"level":      map[string]any{"type": "integer", "description": "heading only: level 1-6"},
			"text":       map[string]any{"type": "string", "description": "heading/paragraph/code plain text; ignored when spans is set"},
			"spans":      createDocumentSpansSchema("heading/paragraph only: inline-formatted text runs, overrides text"),
			"ordered":    map[string]any{"type": "boolean", "description": "list only: numbered (true) vs bulleted (false)"},
			"items":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "list only: plain-text items; ignored per-item where item_spans is set"},
			"item_spans": map[string]any{"type": "array", "items": createDocumentSpansSchema("inline-formatted runs for this list item, overrides the corresponding items entry"), "description": "list only: parallel to items, one spans array per item"},
			"header":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "table only: column headers"},
			"rows":       map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "description": "table only: body rows, one per header column"},
			"language":   map[string]any{"type": "string", "description": "code only: fenced code block language tag"},
			"path":       map[string]any{"type": "string", "description": "image only: local file path (absolute or cwd-relative), validated as a read path"},
			"alt":        map[string]any{"type": "string", "description": "image only: alt text"},
		}, "required": []string{"type"}},
	}
}

func createDocumentSpansSchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"text":   map[string]any{"type": "string"},
			"bold":   map[string]any{"type": "boolean"},
			"italic": map[string]any{"type": "boolean"},
		}, "required": []string{"text"}},
	}
}

func createDocumentSlidesSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "pptx only: one entry per slide, in order",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"title":     map[string]any{"type": "string", "description": "Slide title"},
			"bullets":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Bullet points for the slide body"},
			"notes":     map[string]any{"type": "string", "description": "Optional speaker notes"},
			"image":     map[string]any{"type": "string", "description": "Optional: local file path (absolute or cwd-relative) to an image for this slide, validated as a read path"},
			"image_alt": map[string]any{"type": "string", "description": "image only: alt text written to the picture description field"},
			"layout":    map[string]any{"type": "string", "description": "Slide layout hint: title, content, section, title_only, blank, or picture. Current Go renderer treats it as advisory and uses a fixed production-safe layout."},
		}},
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
	Slides []createDocumentSlideArg `json:"slides"`
}

type createDocumentSlideArg struct {
	Title    string   `json:"title"`
	Bullets  []string `json:"bullets"`
	Notes    string   `json:"notes"`
	Image    string   `json:"image"`
	ImageAlt string   `json:"image_alt"`
	Layout   string   `json:"layout"`
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
	Type      string                    `json:"type"`
	Level     int                       `json:"level"`
	Text      string                    `json:"text"`
	Spans     []createDocumentSpanArg   `json:"spans"`
	Ordered   bool                      `json:"ordered"`
	Items     []string                  `json:"items"`
	ItemSpans [][]createDocumentSpanArg `json:"item_spans"`
	Header    []string                  `json:"header"`
	Rows      [][]string                `json:"rows"`
	Language  string                    `json:"language"`
	Path      string                    `json:"path"`
	Alt       string                    `json:"alt"`
}

type createDocumentSpanArg struct {
	Text   string `json:"text"`
	Bold   bool   `json:"bold"`
	Italic bool   `json:"italic"`
}

func (t *CreateDocumentTool) PreviewCall(argsJSON string) string {
	var a createDocumentArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	preview := fmt.Sprintf("create_document(%s -> %s)", a.Format, a.OutputPath)
	if needsCommandSandbox(a.Format) {
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

// needsCommandSandbox reports whether format's generation path shells out
// (pandoc for docx/pdf) and therefore needs the sandbox-workspace-boundary
// check and sandbox-label preview prefix. xlsx and pptx are native Go and
// never shell out.
func needsCommandSandbox(format string) bool {
	return format == "docx" || format == "pdf"
}

func (t *CreateDocumentTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a createDocumentArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("create_document: invalid args: %w", err)
	}
	if a.Format != "xlsx" && a.Format != "pptx" && !needsCommandSandbox(a.Format) {
		return "", fmt.Errorf("create_document: format must be xlsx, docx, pdf, or pptx, got %q", a.Format)
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
	if needsCommandSandbox(a.Format) && sb.Label() != (HostSandbox{}).Label() {
		if err := checkSandboxWorkspaceBoundary(output, cwd, t.WriteOpts, sb.Label()); err != nil {
			return "", fmt.Errorf("create_document: %w", err)
		}
	}
	// Advisory only, not the safety guarantee: a fast pre-flight so an
	// obviously-doomed !overwrite call fails before paying for pandoc/
	// sandbox work, rather than after. The actual guarantee against a
	// concurrent writer racing this same path is placeOutputFile's O_EXCL
	// reservation right before each generator's final write.
	if !a.Overwrite {
		if _, err := os.Stat(output); err == nil {
			return "", fmt.Errorf("create_document: output %q already exists; set overwrite=true to replace it", output)
		}
	}

	switch a.Format {
	case "xlsx":
		return t.generateXLSX(a, output)
	case "pptx":
		return t.generatePPTX(a, output, cwd)
	default:
		return t.generateViaPandoc(ctx, a, output, cwd)
	}
}

// checkSandboxWorkspaceBoundary reports an error if output would land
// outside cwd — the one root a podman sandbox is guaranteed to have
// mounted. Deliberately narrower than ValidateWritePath's own containment
// check, which also honors WriteOpts.AllowedPaths: an --allow-paths root
// is a HOST-side write permission, not a container-side filesystem
// mount. PodmanSandbox's mountPaths validates every [sandbox].mounts
// entry as a subpath of the project root (see internal/sandbox/podman.go)
// — there is no config that extends the container's view to an arbitrary
// AllowedPaths root elsewhere on the host, so a docx/pdf write approved
// by ValidateWritePath via AllowedPaths could still be unwritable (or
// silently wrong) from inside the container. This check still honors
// WriteOpts.AllowSymlinks the same way ValidateWritePath's own container()
// helper does, so the two checks can't disagree about symlink resolution
// on the shared cwd root.
func checkSandboxWorkspaceBoundary(output, cwd string, wo WritePathOptions, sandboxLabel string) error {
	container := func(root string) string {
		if wo.AllowSymlinks {
			abs, err := filepath.Abs(root)
			if err != nil {
				return ""
			}
			return filepath.Clean(abs)
		}
		return canonicalExisting(root)
	}
	target := output
	if !wo.AllowSymlinks {
		target = resolveWriteTarget(output)
	}
	if pathUnder(target, container(cwd)) {
		return nil
	}
	return fmt.Errorf("output %q is outside the sandbox-mounted workspace %q; docx/pdf generation through %s must write inside the current workspace", output, cwd, sandboxLabel)
}

func (t *CreateDocumentTool) generateXLSX(a createDocumentArgs, output string) (string, error) {
	if len(a.Content.Sheets) == 0 {
		return "", errors.New("create_document: content.sheets is required for format=xlsx")
	}
	data, err := documents.GenerateXLSX(toSheetModel(a.Content.Sheets))
	if err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return "", fmt.Errorf("create_document: mkdir output dir: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(output), ".yc-docgen-*.xlsx")
	if err != nil {
		return "", fmt.Errorf("create_document: create temp output file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("create_document: write temp output file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("create_document: close temp output file: %w", err)
	}
	if err := placeOutputFile(tmpPath, output, a.Overwrite); err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	return "generated xlsx (native): " + output + "\n", nil
}

// placeOutputFile moves src (a freshly-generated temp file already
// confirmed complete) to the real output path. When overwrite is false,
// it first reserves the path with an O_EXCL create — closing the race
// where two concurrent create_document calls targeting the same
// !overwrite path could otherwise both pass an earlier os.Stat check and
// then both write. That earlier check's window could span an entire
// pandoc subprocess run (real time); this reservation-then-rename window
// is a couple of syscalls.
func placeOutputFile(src, dst string, overwrite bool) error {
	if !overwrite {
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("output %q already exists; set overwrite=true to replace it", dst)
			}
			return fmt.Errorf("reserve output path: %w", err)
		}
		f.Close()
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("move generated output into place: %w", err)
	}
	return nil
}

func (t *CreateDocumentTool) generateViaPandoc(ctx context.Context, a createDocumentArgs, output, cwd string) (string, error) {
	if len(a.Content.Blocks) == 0 {
		return "", fmt.Errorf("create_document: content.blocks is required for format=%s", a.Format)
	}
	blocks, err := resolveImageBlockPaths(a.Content.Blocks, cwd, t.DenyReadPaths)
	if err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	md, err := documents.RenderMarkdown(toDocAST(blocks))
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

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return "", fmt.Errorf("create_document: mkdir output dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(output), ".yc-docgen-*.md")
	if err != nil {
		return "", fmt.Errorf("create_document: create temp markdown file: %w", err)
	}
	tmpMDPath := tmpFile.Name()
	defer os.Remove(tmpMDPath)
	if _, err := tmpFile.WriteString(md); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("create_document: write temp markdown file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("create_document: close temp markdown file: %w", err)
	}

	// pandoc writes to a temp path beside the real output rather than
	// output itself — pandoc's own -o always overwrites unconditionally,
	// so the overwrite guard has to live at the placeOutputFile rename
	// below, not here.
	tmpOutput := fmt.Sprintf("%s.yc-docgen-%d%s", strings.TrimSuffix(output, filepath.Ext(output)), time.Now().UnixNano(), filepath.Ext(output))
	defer os.Remove(tmpOutput)

	cmdLine := buildPandocCommand(a.Format, tmpOutput, tmpMDPath)
	c := sb.Command(ctx, cmdLine, cwd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}
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

	if err := placeOutputFile(tmpOutput, output, a.Overwrite); err != nil {
		return "", fmt.Errorf("create_document: %w", err)
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

// generatePPTX builds a pptx from content.slides via the native Go
// documents.GeneratePPTX renderer. Unlike docx/pdf, this path does not
// shell out and does not need the command sandbox or the documents image.
func (t *CreateDocumentTool) generatePPTX(a createDocumentArgs, output, cwd string) (string, error) {
	if len(a.Content.Slides) == 0 {
		return "", errors.New("create_document: content.slides is required for format=pptx")
	}
	slides, err := resolveSlideImagePaths(a.Content.Slides, cwd, t.DenyReadPaths)
	if err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	data, err := documents.GeneratePPTX(toSlideModel(slides))
	if err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return "", fmt.Errorf("create_document: mkdir output dir: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(output), ".yc-docgen-*.pptx")
	if err != nil {
		return "", fmt.Errorf("create_document: create temp output file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("create_document: write temp output file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("create_document: close temp output file: %w", err)
	}
	if err := placeOutputFile(tmpPath, output, a.Overwrite); err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	return "generated pptx (native): " + output + "\n", nil
}

// checkCommandAvailable probes for name through sb rather than a host-only
// exec.LookPath — the whole point of routing document-tool subprocess
// calls through the Sandbox seam is that a podman-backed sandbox has its
// own PATH, independent of the host's. See
// roadmap/document-generation.md's "Sandbox integration".
func checkCommandAvailable(ctx context.Context, sb Sandbox, cwd, name string) error {
	c := sb.Command(ctx, "command -v "+shellQuoteSingle(name), cwd)
	var out bytes.Buffer
	c.Stdout = &cappedWriter{buf: &out}
	c.Stderr = &cappedWriter{buf: &out}
	err := c.Run()
	if err == nil {
		return nil
	}
	where := sb.Label()
	if where == (HostSandbox{}).Label() {
		where = "host PATH"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == podmanInfraExitCode && sb.Label() == podmanSandboxLabel {
		// Mirrors generateViaPandoc's own exit-125 handling: without this,
		// a dead/misconfigured sandbox container surfaces as an ordinary-
		// looking "not found", steering the model toward reinstalling a
		// tool that was never actually missing.
		return fmt.Errorf("create_document: NOTE: exit=125 is podman's own convention for a podman-level failure (not a missing-%s finding) — the sandbox container itself may need attention (see /sandbox). probe output: %s", name, strings.TrimSpace(out.String()))
	}
	if probe := strings.TrimSpace(out.String()); probe != "" {
		return fmt.Errorf("create_document: %s not found (checked via %s): %s; install it, or point [sandbox].image at an image that includes it — see docs/document-generation.md for a reference Containerfile", name, where, probe)
	}
	return fmt.Errorf("create_document: %s not found (checked via %s); install it, or point [sandbox].image at an image that includes it — see docs/document-generation.md for a reference Containerfile", name, where)
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

// resolveImageBlockPaths returns a copy of blocks with every "image"
// block's Path resolved against cwd and validated against denyReadPaths
// — the trust-boundary step internal/documents itself deliberately
// doesn't perform (see documents.Block.Path's doc comment: it expects an
// already-resolved, already-validated absolute path). Non-image blocks
// pass through unchanged. Mirrors MediaRenderTool.Execute's per-path
// ValidateReadPath loop for its optional captions/intro/outro inputs.
func resolveImageBlockPaths(blocks []createDocumentBlockArg, cwd string, denyReadPaths []string) ([]createDocumentBlockArg, error) {
	out := make([]createDocumentBlockArg, len(blocks))
	copy(out, blocks)
	for i, b := range out {
		if b.Type != "image" {
			continue
		}
		if strings.TrimSpace(b.Path) == "" {
			return nil, fmt.Errorf("block %d: image path is required", i)
		}
		resolved := resolvePath(cwd, b.Path)
		if err := ValidateReadPath(resolved, denyReadPaths); err != nil {
			return nil, fmt.Errorf("block %d: image path: %w", i, err)
		}
		out[i].Path = resolved
	}
	return out, nil
}

// resolveSlideImagePaths mirrors resolveImageBlockPaths for pptx slides.
// Unlike an image block, a slide's image is optional — most slides have
// none — so an empty Image is left alone rather than treated as a missing-
// required-field error; only a non-empty Image goes through the same
// deny-list + traversal check before it is handed to the native PPTX renderer.
func resolveSlideImagePaths(slides []createDocumentSlideArg, cwd string, denyReadPaths []string) ([]createDocumentSlideArg, error) {
	out := make([]createDocumentSlideArg, len(slides))
	copy(out, slides)
	for i, s := range out {
		if strings.TrimSpace(s.Image) == "" {
			continue
		}
		resolved := resolvePath(cwd, s.Image)
		if err := ValidateReadPath(resolved, denyReadPaths); err != nil {
			return nil, fmt.Errorf("slide %d: image path: %w", i, err)
		}
		out[i].Image = resolved
	}
	return out, nil
}

func toSlideModel(slides []createDocumentSlideArg) []documents.SlideModel {
	out := make([]documents.SlideModel, len(slides))
	for i, s := range slides {
		out[i] = documents.SlideModel{
			Title:    s.Title,
			Bullets:  s.Bullets,
			Notes:    s.Notes,
			Image:    s.Image,
			ImageAlt: s.ImageAlt,
			Layout:   s.Layout,
		}
	}
	return out
}

func toDocAST(blocks []createDocumentBlockArg) documents.DocAST {
	out := documents.DocAST{Blocks: make([]documents.Block, len(blocks))}
	for i, b := range blocks {
		out.Blocks[i] = documents.Block{
			Type:      b.Type,
			Level:     b.Level,
			Text:      b.Text,
			Spans:     toSpans(b.Spans),
			Ordered:   b.Ordered,
			Items:     b.Items,
			ItemSpans: toItemSpans(b.ItemSpans),
			Header:    b.Header,
			Rows:      b.Rows,
			Language:  b.Language,
			Path:      b.Path,
			Alt:       b.Alt,
		}
	}
	return out
}

func toSpans(spans []createDocumentSpanArg) []documents.Span {
	if len(spans) == 0 {
		return nil
	}
	out := make([]documents.Span, len(spans))
	for i, s := range spans {
		out[i] = documents.Span{Text: s.Text, Bold: s.Bold, Italic: s.Italic}
	}
	return out
}

func toItemSpans(itemSpans [][]createDocumentSpanArg) [][]documents.Span {
	if len(itemSpans) == 0 {
		return nil
	}
	out := make([][]documents.Span, len(itemSpans))
	for i, spans := range itemSpans {
		out[i] = toSpans(spans)
	}
	return out
}
