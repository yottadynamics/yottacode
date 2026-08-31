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
// — see docs/document-generation.md's "Sandbox integration".
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

	// SubprocessFormatsEnabled gates format=docx and format=pdf, which
	// shell out to pandoc (pdf additionally needs weasyprint) — see
	// needsCommandSandbox. xlsx and pptx are pure Go with no subprocess
	// and are never gated by this field. document_generation graduated
	// to GA (see internal/experimental/features.go), so every real
	// caller wires this true unconditionally; the field stays as a
	// structural on/off switch for a caller that wants docx/pdf excluded
	// for reasons of its own (mirrors CoreToolDeps.EnableLSP's own
	// always-true-but-still-a-field shape). A missing pandoc/weasyprint
	// binary is reported separately by checkCommandAvailable, not by
	// this field.
	SubprocessFormatsEnabled bool
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
		"For format=pptx, set content.slides (title/bullets/notes/layout/image per slide); " +
		"an image references an existing local file by path (validated the same way a docx/pdf image " +
		"block is); generation is native Go and needs no python3/python-pptx runtime. " +
		"An image's placement defaults to a fixed right-half layout; set image_layout to left/right/full " +
		"for a preset, or all four of image_left/image_top/image_width/image_height (inches) for an exact " +
		"bounding box — the two are mutually exclusive, and an explicit box must fit within the 13.33in x 7.5in slide. " +
		"For format=docx or format=pdf, set content.blocks (heading/paragraph/list/table/code/image); " +
		"a heading/paragraph/list-item can use plain text or spans (bold/italic inline formatting) " +
		"and an image block references an existing local file by path (validated the same way read_file " +
		"validates a read path — denied credential paths, no path traversal). " +
		"generation runs pandoc, which must be reachable through the documents sandbox profile " +
		"(installed on the host when no sandbox is configured, or present in [sandbox].documents_image); " +
		"pdf additionally requires weasyprint as pandoc's PDF engine — a missing binary returns an " +
		"actionable error naming exactly where it looked, rather than failing silently. " +
		"For format=docx, set template (path to an existing docx) plus content.replacements " +
		"instead of content.blocks to fill an existing document in place rather than generating a new " +
		"one from scratch: every {{name}} token found in the template (including inside tables, headers, " +
		"and footers) is replaced with replacements[name], preserving formatting outside affected " +
		"paragraphs — a paragraph containing a replaced token loses formatting variation *within* that " +
		"paragraph specifically (collapses to its first run's style), everything else is untouched. " +
		"Runs python3+python-docx through the same documents sandbox profile as pandoc; a missing binary returns " +
		"the same kind of actionable error. " +
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
			"template": map[string]any{
				"type": "string",
				"description": "format=docx only: path to an existing docx file to fill in place of generating a new one from content.blocks. " +
					"Every {{name}} token found in the template's text (including inside tables, headers, and footers) is replaced " +
					"with content.replacements[name]; a paragraph with no matching token is left completely untouched. " +
					"Validated as a read path the same way an image block's path is (denied credential paths, no path traversal).",
			},
			"content": map[string]any{
				"type":        "object",
				"description": "Document content: set sheets for format=xlsx, blocks for format=docx/pdf (ignored when template is set), replacements for format=docx with template set, or slides for format=pptx",
				"properties": map[string]any{
					"sheets":       createDocumentSheetsSchema(),
					"blocks":       createDocumentBlocksSchema(),
					"slides":       createDocumentSlidesSchema(),
					"replacements": createDocumentReplacementsSchema(),
				},
			},
		},
		"required": []string{"format", "output_path", "content"},
	}
}

func createDocumentReplacementsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "format=docx with template set only: flat {name: value} map: every {{name}} token in the template is replaced with value.",
		"additionalProperties": map[string]any{"type": "string"},
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
			"title":        map[string]any{"type": "string", "description": "Slide title"},
			"bullets":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Bullet points for the slide body"},
			"notes":        map[string]any{"type": "string", "description": "Optional speaker notes"},
			"image":        map[string]any{"type": "string", "description": "Optional: local file path (absolute or cwd-relative) to an image for this slide, validated as a read path"},
			"image_alt":    map[string]any{"type": "string", "description": "image only: alt text written to the picture description field"},
			"image_layout": map[string]any{"type": "string", "description": "image only: placement preset — right (default, matches omitting this field), left, or full (edge-to-edge slide bleed). Mutually exclusive with image_left/image_top/image_width/image_height."},
			"image_left":   map[string]any{"type": "number", "description": "image only: explicit left offset in inches from the slide's left edge. Must be set together with image_top/image_width/image_height; mutually exclusive with image_layout."},
			"image_top":    map[string]any{"type": "number", "description": "image only: explicit top offset in inches from the slide's top edge. Must be set together with image_left/image_width/image_height."},
			"image_width":  map[string]any{"type": "number", "description": "image only: explicit width in inches. Must be set together with image_left/image_top/image_height; the image must fit within the slide (13.33in x 7.5in, 16:9 widescreen)."},
			"image_height": map[string]any{"type": "number", "description": "image only: explicit height in inches. Must be set together with image_left/image_top/image_width."},
			"layout":       map[string]any{"type": "string", "description": "Slide layout hint: title, content, section, title_only, blank, or picture. Current Go renderer treats it as advisory and uses a fixed production-safe layout."},
		}},
	}
}

func (t *CreateDocumentTool) RequiresApproval(string) bool { return true }

type createDocumentArgs struct {
	Format     string                    `json:"format"`
	OutputPath string                    `json:"output_path"`
	Overwrite  bool                      `json:"overwrite"`
	Template   string                    `json:"template"`
	Content    createDocumentContentArgs `json:"content"`
}

type createDocumentContentArgs struct {
	Sheets       []createDocumentSheetArg `json:"sheets"`
	Blocks       []createDocumentBlockArg `json:"blocks"`
	Slides       []createDocumentSlideArg `json:"slides"`
	Replacements map[string]string        `json:"replacements"`
}

type createDocumentSlideArg struct {
	Title       string   `json:"title"`
	Bullets     []string `json:"bullets"`
	Notes       string   `json:"notes"`
	Image       string   `json:"image"`
	ImageAlt    string   `json:"image_alt"`
	ImageLayout string   `json:"image_layout"`
	// ImageLeft/Top/Width/Height are pointers so an omitted field stays
	// distinguishable from an explicit 0 (e.g. image_top=0 is a valid
	// top-edge placement, not "unset") — same reasoning as
	// readDocumentArgs.HasHeader.
	ImageLeft   *float64 `json:"image_left"`
	ImageTop    *float64 `json:"image_top"`
	ImageWidth  *float64 `json:"image_width"`
	ImageHeight *float64 `json:"image_height"`
	Layout      string   `json:"layout"`
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
		if sb := t.sandbox(); LabelForProfile(sb, SandboxProfileDocuments) != (HostSandbox{}).Label() {
			preview = LabelForProfile(sb, SandboxProfileDocuments) + " " + preview
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
	if needsCommandSandbox(a.Format) && !t.SubprocessFormatsEnabled {
		return "", fmt.Errorf("create_document: format=%s is disabled in this configuration", a.Format)
	}
	if strings.TrimSpace(a.Template) != "" && a.Format != "docx" {
		return "", fmt.Errorf("create_document: template is only supported for format=docx, got format=%s", a.Format)
	}

	cwd := t.Cwd.Get()
	output := resolvePath(cwd, a.OutputPath)
	if err := ValidateWritePath(output, t.WriteOpts); err != nil {
		return "", fmt.Errorf("create_document: output: %w", err)
	}
	sb := t.sandbox()
	if needsCommandSandbox(a.Format) && LabelForProfile(sb, SandboxProfileDocuments) != (HostSandbox{}).Label() {
		if err := checkSandboxWorkspaceBoundary(output, cwd, t.WriteOpts, LabelForProfile(sb, SandboxProfileDocuments)); err != nil {
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

	switch {
	case a.Format == "xlsx":
		return t.generateXLSX(a, output)
	case a.Format == "pptx":
		return t.generatePPTX(a, output, cwd)
	case a.Format == "docx" && strings.TrimSpace(a.Template) != "":
		return t.generateDocxFromTemplate(ctx, a, output, cwd)
	default: // docx (no template) or pdf
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
	c := CommandInProfile(ctx, sb, SandboxProfileDocuments, cmdLine, cwd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}
	if err := c.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			note := ""
			if exitErr.ExitCode() == podmanInfraExitCode && isPodmanSandboxLabel(LabelForProfile(sb, SandboxProfileDocuments)) {
				note = "NOTE: exit=125 is podman's own convention for a podman-level failure (not pandoc's exit code) — the sandbox container itself may need attention (see /sandbox). "
			}
			return "", fmt.Errorf("create_document: %spandoc failed (exit=%d): %s", note, exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("create_document: pandoc: %w", err)
	}

	if err := placeOutputFile(tmpOutput, output, a.Overwrite); err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}

	label := LabelForProfile(sb, SandboxProfileDocuments)
	if label == (HostSandbox{}).Label() {
		label = "host"
	}
	return fmt.Sprintf("generated %s via pandoc (%s): %s\n", a.Format, label, output), nil
}

// resolvePyHelperScript implements documents.ScriptResolver, mirroring
// ReadDocumentTool.resolvePyHelperScript exactly (same cache directory,
// same podman-vs-host dispatch) so both tools materialize into the
// same place rather than each picking its own.
func (t *CreateDocumentTool) resolvePyHelperScript(script documents.PyHelperScript) (string, error) {
	cacheDir, err := pyHelperCacheDir()
	if err != nil {
		return "", err
	}
	isPodman := isPodmanSandboxLabel(LabelForProfile(t.sandbox(), SandboxProfileDocuments))
	return documents.ResolvePyHelperScript(script, isPodman, cacheDir)
}

// generateDocxFromTemplate fills {{name}} tokens in an existing docx via
// the fill_docx_template.py driver script (python-docx), instead of
// generating a new docx from content.blocks via pandoc — see
// docs/document-generation.md's "Python-in-container implementation
// plan". Reuses the same Sandbox/shell-quoting/atomic-placement/exit-125
// handling generateViaPandoc already established for docx/pdf; the only
// new trust boundary is Template itself, which is a read path validated
// exactly like an image block's path (ValidateReadPath + the credential
// deny-list) — this tool's second-ever read path after images.
func (t *CreateDocumentTool) generateDocxFromTemplate(ctx context.Context, a createDocumentArgs, output, cwd string) (string, error) {
	templatePath := resolvePath(cwd, a.Template)
	if err := ValidateReadPath(templatePath, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("create_document: template: %w", err)
	}

	sb := t.sandbox()
	if err := checkCommandAvailable(ctx, sb, cwd, "python3"); err != nil {
		return "", err
	}
	scriptPath, err := t.resolvePyHelperScript(documents.ScriptFillDocxTemplate)
	if err != nil {
		return "", fmt.Errorf("create_document: resolve docx template helper script: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return "", fmt.Errorf("create_document: mkdir output dir: %w", err)
	}

	replJSON, err := json.Marshal(a.Content.Replacements)
	if err != nil {
		return "", fmt.Errorf("create_document: marshal replacements: %w", err)
	}
	replFile, err := os.CreateTemp(filepath.Dir(output), ".yc-docgen-*.json")
	if err != nil {
		return "", fmt.Errorf("create_document: create temp replacements file: %w", err)
	}
	replPath := replFile.Name()
	defer os.Remove(replPath)
	if _, err := replFile.Write(replJSON); err != nil {
		replFile.Close()
		return "", fmt.Errorf("create_document: write temp replacements file: %w", err)
	}
	if err := replFile.Close(); err != nil {
		return "", fmt.Errorf("create_document: close temp replacements file: %w", err)
	}

	// Same reasoning as generateViaPandoc's tmpOutput: the script's own
	// output-path argument always overwrites unconditionally, so the
	// overwrite guard has to live at the placeOutputFile rename below.
	tmpOutput := fmt.Sprintf("%s.yc-docgen-%d%s", strings.TrimSuffix(output, filepath.Ext(output)), time.Now().UnixNano(), filepath.Ext(output))
	defer os.Remove(tmpOutput)

	cmdLine := buildDocxTemplateCommand(scriptPath, templatePath, tmpOutput, replPath)
	c := CommandInProfile(ctx, sb, SandboxProfileDocuments, cmdLine, cwd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}
	if err := c.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			note := ""
			if exitErr.ExitCode() == podmanInfraExitCode && isPodmanSandboxLabel(LabelForProfile(sb, SandboxProfileDocuments)) {
				note = "NOTE: exit=125 is podman's own convention for a podman-level failure (not the script's own exit code) — the sandbox container itself may need attention (see /sandbox). "
			}
			return "", fmt.Errorf("create_document: %sdocx template fill failed (exit=%d): %s", note, exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("create_document: docx template fill: %w", err)
	}

	if err := placeOutputFile(tmpOutput, output, a.Overwrite); err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}

	label := LabelForProfile(sb, SandboxProfileDocuments)
	if label == (HostSandbox{}).Label() {
		label = "host"
	}
	return fmt.Sprintf("generated docx from template via python-docx (%s): %s (%d replacements applied)\n",
		label, output, parseReplacementsAppliedCount(stdout.Bytes())), nil
}

// parseReplacementsAppliedCount best-effort parses
// fill_docx_template.py's stdout status JSON. A parse failure returns 0
// rather than an error — the docx was already generated successfully by
// this point; this count is informational only.
func parseReplacementsAppliedCount(stdout []byte) int {
	var status struct {
		ReplacementsApplied int `json:"replacements_applied"`
	}
	if err := json.Unmarshal(stdout, &status); err != nil {
		return 0
	}
	return status.ReplacementsApplied
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

// buildDocxTemplateCommand builds the /bin/sh -c command line
// generateDocxFromTemplate runs. Pure function (no I/O), mirroring
// buildPandocCommand's own shape, so its output is unit-testable
// without python installed. Argument order matches
// pyhelpers/fill_docx_template.py's own: template, output,
// replacements-JSON.
func buildDocxTemplateCommand(scriptPath, templatePath, output, replacementsPath string) string {
	return fmt.Sprintf("python3 %s %s %s %s",
		shellQuoteSingle(scriptPath), shellQuoteSingle(templatePath), shellQuoteSingle(output), shellQuoteSingle(replacementsPath))
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
	slideModels, err := toSlideModel(slides)
	if err != nil {
		return "", fmt.Errorf("create_document: %w", err)
	}
	data, err := documents.GeneratePPTX(slideModels)
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
// docs/document-generation.md's "Sandbox integration".
func checkCommandAvailable(ctx context.Context, sb Sandbox, cwd, name string) error {
	c := CommandInProfile(ctx, sb, SandboxProfileDocuments, "command -v "+shellQuoteSingle(name), cwd)
	var out bytes.Buffer
	c.Stdout = &cappedWriter{buf: &out}
	c.Stderr = &cappedWriter{buf: &out}
	err := c.Run()
	if err == nil {
		return nil
	}
	where := LabelForProfile(sb, SandboxProfileDocuments)
	if where == (HostSandbox{}).Label() {
		where = "host PATH"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == podmanInfraExitCode && isPodmanSandboxLabel(LabelForProfile(sb, SandboxProfileDocuments)) {
		// Mirrors generateViaPandoc's own exit-125 handling: without this,
		// a dead/misconfigured sandbox container surfaces as an ordinary-
		// looking "not found", steering the model toward reinstalling a
		// tool that was never actually missing.
		return fmt.Errorf("create_document: NOTE: exit=125 is podman's own convention for a podman-level failure (not a missing-%s finding) — the sandbox container itself may need attention (see /sandbox). probe output: %s", name, strings.TrimSpace(out.String()))
	}
	if probe := strings.TrimSpace(out.String()); probe != "" {
		return fmt.Errorf("create_document: %s not found (checked via %s): %s; install it, or point [sandbox].documents_image at an image that includes it — see docs/document-generation.md for a reference Containerfile", name, where, probe)
	}
	return fmt.Errorf("create_document: %s not found (checked via %s); install it, or point [sandbox].documents_image at an image that includes it — see docs/document-generation.md for a reference Containerfile", name, where)
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

func toSlideModel(slides []createDocumentSlideArg) ([]documents.SlideModel, error) {
	out := make([]documents.SlideModel, len(slides))
	for i, s := range slides {
		bounds, err := slideImageBounds(i, s)
		if err != nil {
			return nil, err
		}
		out[i] = documents.SlideModel{
			Title:       s.Title,
			Bullets:     s.Bullets,
			Notes:       s.Notes,
			Image:       s.Image,
			ImageAlt:    s.ImageAlt,
			Layout:      s.Layout,
			ImageBounds: bounds,
		}
	}
	return out, nil
}

// imageBoundsEpsilon absorbs float64 rounding in the inches<->EMU
// round trip (e.g. 13.333333333333334in back-converted from
// documents.SlideWidthEMU) so a layout that exactly fills the slide
// isn't rejected as "past the edge" by a fraction of an EMU.
const imageBoundsEpsilon = 1e-6

// slideImageBounds validates slide index i's image placement fields and
// converts them to documents.ImageBounds (EMU), or returns nil (meaning
// "use the built-in default placement") when none were set. Actionable,
// 1-indexed-in-message errors live here rather than in internal/documents
// because this is the schema/validation boundary — see resolveSlideImagePaths
// for the same layering on the image-path side.
func slideImageBounds(i int, s createDocumentSlideArg) (*documents.ImageBounds, error) {
	explicit := []*float64{s.ImageLeft, s.ImageTop, s.ImageWidth, s.ImageHeight}
	explicitCount := 0
	for _, v := range explicit {
		if v != nil {
			explicitCount++
		}
	}
	hasLayout := strings.TrimSpace(s.ImageLayout) != ""
	hasExplicit := explicitCount > 0

	msgSlide := i + 1

	if !hasLayout && !hasExplicit {
		return nil, nil
	}
	if hasLayout && hasExplicit {
		return nil, fmt.Errorf("slide %d: set either image_layout or explicit image_left/image_top/image_width/image_height, not both", msgSlide)
	}
	if strings.TrimSpace(s.Image) == "" {
		return nil, fmt.Errorf("slide %d: image_layout/image bounds are set but no image was given", msgSlide)
	}

	if hasExplicit {
		if explicitCount != 4 {
			return nil, fmt.Errorf("slide %d: image_left, image_top, image_width, and image_height must all be set together", msgSlide)
		}
		left, top, width, height := *s.ImageLeft, *s.ImageTop, *s.ImageWidth, *s.ImageHeight
		if width <= 0 || height <= 0 {
			return nil, fmt.Errorf("slide %d: image_width and image_height must be positive (got %g x %g)", msgSlide, width, height)
		}
		if left < 0 || top < 0 {
			return nil, fmt.Errorf("slide %d: image_left and image_top must be non-negative (got %g, %g)", msgSlide, left, top)
		}
		slideWidthIn := float64(documents.SlideWidthEMU) / float64(documents.EMUPerInch)
		slideHeightIn := float64(documents.SlideHeightEMU) / float64(documents.EMUPerInch)
		if left+width > slideWidthIn+imageBoundsEpsilon {
			return nil, fmt.Errorf("slide %d: image_left (%g) + image_width (%g) = %g exceeds the slide width of %.3gin", msgSlide, left, width, left+width, slideWidthIn)
		}
		if top+height > slideHeightIn+imageBoundsEpsilon {
			return nil, fmt.Errorf("slide %d: image_top (%g) + image_height (%g) = %g exceeds the slide height of %.3gin", msgSlide, top, height, top+height, slideHeightIn)
		}
		return &documents.ImageBounds{
			X:  int64(left * float64(documents.EMUPerInch)),
			Y:  int64(top * float64(documents.EMUPerInch)),
			CX: int64(width * float64(documents.EMUPerInch)),
			CY: int64(height * float64(documents.EMUPerInch)),
		}, nil
	}

	switch s.ImageLayout {
	case "right":
		return nil, nil // identical to the built-in default
	case "left":
		// Mirrors the built-in "right" preset: same margin (609600 EMU,
		// 0.667in) from the slide's left edge that "right" leaves from
		// the slide's right edge, same size.
		return &documents.ImageBounds{X: 609600, Y: 1463040, CX: 5029200, CY: 3429000}, nil
	case "full":
		return &documents.ImageBounds{X: 0, Y: 0, CX: documents.SlideWidthEMU, CY: documents.SlideHeightEMU}, nil
	default:
		return nil, fmt.Errorf("slide %d: unknown image_layout %q (must be left, right, or full)", msgSlide, s.ImageLayout)
	}
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
