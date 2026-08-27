package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// fakeSandbox lets tests control command-availability detection without a
// real podman container or a real pandoc install. Every command it builds
// runs on the host via /bin/sh -c, same as HostSandbox, except Command can
// be forced to fail (simulating "pandoc not found").
type fakeSandbox struct {
	label   string
	fail    bool
	missing string
}

func (f *fakeSandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	if f.fail {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}
	if f.missing != "" && strings.Contains(command, "command -v '"+f.missing+"'") {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}
	if strings.HasPrefix(command, "command -v ") {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf '/usr/bin/fake-tool\\n'")
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}
func (f *fakeSandbox) Label() string { return f.label }
func (f *fakeSandbox) Close() error  { return nil }

func TestCreateDocumentRejectsUnknownFormat(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"format":"odt","output_path":"out.odt","content":{}}`)
	if err == nil || !strings.Contains(err.Error(), "format must be") {
		t.Fatalf("expected format validation error, got %v", err)
	}
}

func TestCreateDocumentRequiresOutputPath(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"format":"xlsx","content":{"sheets":[]}}`)
	if err == nil || !strings.Contains(err.Error(), "output_path is required") {
		t.Fatalf("expected output_path validation error, got %v", err)
	}
}

func TestCreateDocumentXLSXRequiresSheets(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"format":"xlsx","output_path":"out.xlsx","content":{}}`)
	if err == nil || !strings.Contains(err.Error(), "content.sheets is required") {
		t.Fatalf("expected content.sheets validation error, got %v", err)
	}
}

func TestCreateDocumentDocxRequiresBlocks(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}, SubprocessFormatsEnabled: true}
	_, err := tool.Execute(context.Background(), `{"format":"docx","output_path":"out.docx","content":{}}`)
	if err == nil || !strings.Contains(err.Error(), "content.blocks is required") {
		t.Fatalf("expected content.blocks validation error, got %v", err)
	}
}

// TestCreateDocumentDocxPdfDisabledViaSubprocessGate is the regression
// for the field itself: create_document is fully GA (document_generation
// graduated — see internal/experimental/features.go), so every real
// caller wires SubprocessFormatsEnabled true unconditionally, but the
// field remains a real on/off switch for a caller that constructs the
// tool directly with it left false. xlsx and pptx must be unaffected —
// see TestCreateDocumentXLSXGeneratesRealFile and
// TestCreateDocumentPptxGeneratesRealFile, neither of which sets this
// field yet both already succeed.
func TestCreateDocumentDocxPdfDisabledViaSubprocessGate(t *testing.T) {
	for _, format := range []string{"docx", "pdf"} {
		tmp := t.TempDir()
		tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
		args := `{"format":"` + format + `","output_path":"out.` + format + `","content":{"blocks":[{"type":"paragraph","text":"hi"}]}}`
		_, err := tool.Execute(context.Background(), args)
		if err == nil {
			t.Fatalf("format=%s: expected an error when SubprocessFormatsEnabled is false", format)
		}
		if !strings.Contains(err.Error(), "disabled in this configuration") {
			t.Errorf("format=%s: expected the error to explain the format is disabled, got %v", format, err)
		}
	}
}

func TestCreateDocumentRejectsPathOutsideWorkspace(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"format":"xlsx","output_path":"/etc/out.xlsx","content":{"sheets":[{"rows":[[{"value":"x"}]]}]}}`)
	if err == nil {
		t.Fatal("expected an error writing outside the workspace")
	}
}

func TestCreateDocumentXLSXGeneratesRealFile(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"format":"xlsx","output_path":"report.xlsx","content":{"sheets":[{"name":"Data","rows":[[{"value":"Item"},{"value":"Qty"}],[{"value":"Widget"},{"value":3}]]}]}}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "generated xlsx") {
		t.Errorf("unexpected result: %q", out)
	}
	f, err := excelize.OpenFile(filepath.Join(tmp, "report.xlsx"))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()
	got, err := f.GetCellValue("Data", "A1")
	if err != nil || got != "Item" {
		t.Errorf("A1 = %q, %v; want Item", got, err)
	}
}

func TestCreateDocumentXLSXOverwriteGuard(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"format":"xlsx","output_path":"report.xlsx","content":{"sheets":[{"rows":[[{"value":"x"}]]}]}}`
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected overwrite guard error, got %v", err)
	}
	overwriteArgs := `{"format":"xlsx","output_path":"report.xlsx","overwrite":true,"content":{"sheets":[{"rows":[[{"value":"y"}]]}]}}`
	if _, err := tool.Execute(context.Background(), overwriteArgs); err != nil {
		t.Fatalf("overwrite Execute: %v", err)
	}
}

func TestCreateDocumentDocxMissingPandoc(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		Sandbox:                  &fakeSandbox{label: "[podman-sandbox]", fail: true},
		SubprocessFormatsEnabled: true,
	}
	args := `{"format":"docx","output_path":"out.docx","content":{"blocks":[{"type":"paragraph","text":"hi"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "pandoc not found") {
		t.Fatalf("expected 'pandoc not found' error, got %v", err)
	}
	if !strings.Contains(err.Error(), "[podman-sandbox]") {
		t.Fatalf("expected error to name the sandbox that was checked, got %v", err)
	}
}

func TestCreateDocumentPDFMissingWeasyprint(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		Sandbox:                  &fakeSandbox{label: "[podman-sandbox]", missing: "weasyprint"},
		SubprocessFormatsEnabled: true,
	}
	args := `{"format":"pdf","output_path":"out.pdf","content":{"blocks":[{"type":"paragraph","text":"hi"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "weasyprint not found") {
		t.Fatalf("expected 'weasyprint not found' error, got %v", err)
	}
}

// TestCreateDocumentTemplateRejectedForNonDocxFormats prevents mixed-mode
// tool calls from silently ignoring template. The schema advertises template as
// docx-only, so invalid combinations should fail before any generator runs.
func TestCreateDocumentTemplateRejectedForNonDocxFormats(t *testing.T) {
	for _, format := range []string{"xlsx", "pdf", "pptx"} {
		tmp := t.TempDir()
		tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}, SubprocessFormatsEnabled: true}
		args := `{"format":"` + format + `","output_path":"out.` + format + `","template":"template.docx","content":{"sheets":[{"rows":[[{"value":"x"}]]}],"blocks":[{"type":"paragraph","text":"hi"}],"slides":[{"title":"hi"}]}}`
		_, err := tool.Execute(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "template is only supported for format=docx") {
			t.Fatalf("format=%s: expected docx-only template validation error, got %v", format, err)
		}
	}
}

func TestCreateDocumentPptxRequiresSlides(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"format":"pptx","output_path":"out.pptx","content":{}}`)
	if err == nil || !strings.Contains(err.Error(), "content.slides is required") {
		t.Fatalf("expected content.slides validation error, got %v", err)
	}
}

func TestCreateDocumentPptxGeneratesRealFile(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"format":"pptx","output_path":"deck.pptx","content":{"slides":[{"title":"Hi","bullets":["one","two"],"notes":"speaker notes"}]}}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "generated pptx (native)") {
		t.Errorf("unexpected result: %q", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, "deck.pptx")); err != nil {
		t.Fatalf("expected pptx output: %v", err)
	}
}

// TestCreateDocumentPptxDoesNotRequireSandboxWorkspaceOutput pins that
// pptx generation is native Go. Unlike docx/pdf, an AllowedPaths output
// outside cwd is valid because no container needs to see the path.
func TestCreateDocumentPptxDoesNotRequireSandboxWorkspaceOutput(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(t.TempDir(), "out.pptx")
	cwd := NewCwdRef(tmp)
	tool := &CreateDocumentTool{
		Cwd:       cwd,
		WriteOpts: WritePathOptions{Cwd: cwd, AllowedPaths: []string{filepath.Dir(outside)}},
		Sandbox:   &fakeSandbox{label: "[podman-sandbox]"},
	}
	args := `{"format":"pptx","output_path":"` + outside + `","content":{"slides":[{"title":"Hi"}]}}`
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("native pptx generation should be able to write an allowed host path: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("expected pptx output outside cwd: %v", err)
	}
}

func TestCreateDocumentSlideContentMarshalsExpectedShape(t *testing.T) {
	slides := []createDocumentSlideArg{
		{Title: "Intro", Bullets: []string{"a", "b"}, Notes: "speaker notes", Layout: "content"},
		{Title: "Section 2"},
		{Title: "Chart", Image: "/abs/chart.png", ImageAlt: "Growth chart"},
	}
	data, err := json.Marshal(slides)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("got %d slides, want 3", len(decoded))
	}
	if decoded[0]["title"] != "Intro" || decoded[0]["layout"] != "content" {
		t.Errorf("unexpected first slide: %+v", decoded[0])
	}
	bullets, ok := decoded[0]["bullets"].([]any)
	if !ok || len(bullets) != 2 {
		t.Errorf("expected 2 bullets in first slide, got %+v", decoded[0]["bullets"])
	}
	// Second slide has no bullets set (nil slice) — must marshal to a
	// JSON key the Python script's `spec.get("bullets") or []` handles
	// gracefully (null), not be omitted or cause an unmarshal error.
	if _, ok := decoded[1]["bullets"]; !ok {
		t.Errorf("expected 'bullets' key present (even if null) in second slide: %+v", decoded[1])
	}
	if decoded[2]["image"] != "/abs/chart.png" || decoded[2]["image_alt"] != "Growth chart" {
		t.Errorf("unexpected third slide image fields: %+v", decoded[2])
	}
}

// TestCreateDocumentImageBlockRejectsDeniedPath is the regression for
// stage 4: an image block is create_document's first ever READ path, so
// it must go through the same credential-path denylist read_file/
// read_document share (ValidateReadPath) — not a blanket workspace
// check, mirroring TestReadDocumentTool_RejectsDeniedPath and
// TestMediaComposeValidation's per-segment path checks.
func TestCreateDocumentImageBlockRejectsDeniedPath(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret.png")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		DenyReadPaths:            []string{secret},
		SubprocessFormatsEnabled: true,
	}
	args := `{"format":"docx","output_path":"out.docx","content":{"blocks":[{"type":"image","path":"secret.png","alt":"x"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "image path") {
		t.Fatalf("expected an image-path denial error, got %v", err)
	}
}

func TestCreateDocumentImageBlockRequiresPath(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}, SubprocessFormatsEnabled: true}
	args := `{"format":"docx","output_path":"out.docx","content":{"blocks":[{"type":"image","alt":"missing path"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "image path is required") {
		t.Fatalf("expected an 'image path is required' error, got %v", err)
	}
}

// TestCreateDocumentImageBlockValidPathPassesValidation confirms a
// legitimate in-workspace image path clears resolveImageBlockPaths and
// reaches the pandoc-availability check — not rejected as a read-path
// violation. pandoc isn't installed on the test host, so this stops at
// the "pandoc not found" error, same posture as the other generation
// tests that don't assert a real pandoc invocation succeeded.
func TestCreateDocumentImageBlockValidPathPassesValidation(t *testing.T) {
	tmp := t.TempDir()
	img := filepath.Join(tmp, "logo.png")
	if err := os.WriteFile(img, []byte("fake png bytes"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}, SubprocessFormatsEnabled: true}
	args := `{"format":"docx","output_path":"out.docx","content":{"blocks":[{"type":"image","path":"logo.png","alt":"Logo"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "pandoc not found") {
		t.Fatalf("expected to get past image validation to the pandoc-availability check, got %v", err)
	}
}

func TestResolveImageBlockPaths(t *testing.T) {
	tmp := t.TempDir()
	img := filepath.Join(tmp, "logo.png")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	blocks := []createDocumentBlockArg{
		{Type: "paragraph", Text: "unaffected"},
		{Type: "image", Path: "logo.png", Alt: "Logo"},
	}
	resolved, err := resolveImageBlockPaths(blocks, tmp, nil)
	if err != nil {
		t.Fatalf("resolveImageBlockPaths: %v", err)
	}
	if resolved[0].Text != "unaffected" {
		t.Errorf("non-image block should pass through unchanged, got %+v", resolved[0])
	}
	if resolved[1].Path != img {
		t.Errorf("expected image path resolved to absolute %q, got %q", img, resolved[1].Path)
	}
	// Original slice must be untouched (resolveImageBlockPaths returns a
	// copy, per its own doc comment).
	if blocks[1].Path != "logo.png" {
		t.Errorf("resolveImageBlockPaths mutated the input slice: %+v", blocks[1])
	}
}

// TestResolveSlideImagePaths mirrors TestResolveImageBlockPaths for pptx
// slides. Unlike an image block, a slide's image is optional — a slide
// with no image must pass through unchanged rather than error.
func TestResolveSlideImagePaths(t *testing.T) {
	tmp := t.TempDir()
	img := filepath.Join(tmp, "chart.png")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	slides := []createDocumentSlideArg{
		{Title: "no image", Bullets: []string{"unaffected"}},
		{Title: "with image", Image: "chart.png", ImageAlt: "Chart"},
	}
	resolved, err := resolveSlideImagePaths(slides, tmp, nil)
	if err != nil {
		t.Fatalf("resolveSlideImagePaths: %v", err)
	}
	if resolved[0].Image != "" {
		t.Errorf("slide without an image should pass through unchanged, got %+v", resolved[0])
	}
	if resolved[1].Image != img {
		t.Errorf("expected image path resolved to absolute %q, got %q", img, resolved[1].Image)
	}
	if resolved[1].ImageAlt != "Chart" {
		t.Errorf("image_alt should pass through unchanged, got %+v", resolved[1])
	}
	// Original slice must be untouched, same contract as resolveImageBlockPaths.
	if slides[1].Image != "chart.png" {
		t.Errorf("resolveSlideImagePaths mutated the input slice: %+v", slides[1])
	}
}

// TestResolveSlideImagePaths_DeniedPath is the slide-image regression for
// the same credential-denylist enforcement TestCreateDocumentImageBlockRejectsDeniedPath
// locks for docx/pdf image blocks.
func TestResolveSlideImagePaths_DeniedPath(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret.png")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	slides := []createDocumentSlideArg{{Title: "x", Image: "secret.png"}}
	if _, err := resolveSlideImagePaths(slides, tmp, []string{secret}); err == nil {
		t.Error("expected an error for a denied slide image path")
	}
}

// TestCreateDocumentPptxImageRejectsDeniedPath is the end-to-end version:
// a slide image is create_document's read path for pptx, same as an image
// block is for docx/pdf, so it must go through Execute's full validation,
// not just the resolver in isolation.
func TestCreateDocumentPptxImageRejectsDeniedPath(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret.png")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	tool := &CreateDocumentTool{
		Cwd:           NewCwdRef(tmp),
		WriteOpts:     WritePathOptions{Cwd: NewCwdRef(tmp)},
		DenyReadPaths: []string{secret},
	}
	args := `{"format":"pptx","output_path":"out.pptx","content":{"slides":[{"title":"x","image":"secret.png"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "image path") {
		t.Fatalf("expected an image-path denial error, got %v", err)
	}
}

// TestCreateDocumentPptxImagePassesValidation confirms a legitimate
// in-workspace slide image clears resolveSlideImagePaths and reaches the
// native PPTX renderer — not rejected as a read-path violation.
func TestCreateDocumentPptxImagePassesValidation(t *testing.T) {
	tmp := t.TempDir()
	img := filepath.Join(tmp, "chart.png")
	if err := os.WriteFile(img, minimalPNG(), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	tool := &CreateDocumentTool{
		Cwd:       NewCwdRef(tmp),
		WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)},
	}
	args := `{"format":"pptx","output_path":"out.pptx","content":{"slides":[{"title":"x","image":"chart.png","image_alt":"Chart"}]}}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("expected native pptx image generation to succeed, got %v", err)
	}
	if !strings.Contains(out, "generated pptx (native)") {
		t.Fatalf("unexpected result: %q", out)
	}
}

func minimalPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0xcc, 0x59,
		0xe7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
}

func TestToSpansAndToItemSpans(t *testing.T) {
	spans := toSpans([]createDocumentSpanArg{{Text: "a", Bold: true}, {Text: "b", Italic: true}})
	if len(spans) != 2 || !spans[0].Bold || !spans[1].Italic {
		t.Errorf("unexpected spans: %+v", spans)
	}
	if toSpans(nil) != nil {
		t.Errorf("expected nil for empty input, got %+v", toSpans(nil))
	}

	itemSpans := toItemSpans([][]createDocumentSpanArg{{{Text: "x"}}, nil})
	if len(itemSpans) != 2 || len(itemSpans[0]) != 1 || itemSpans[0][0].Text != "x" {
		t.Errorf("unexpected item spans: %+v", itemSpans)
	}
	if toItemSpans(nil) != nil {
		t.Errorf("expected nil for empty input, got %+v", toItemSpans(nil))
	}
}

func TestCreateDocumentSandboxedPandocRequiresWorkspaceOutput(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(t.TempDir(), "out.docx")
	cwd := NewCwdRef(tmp)
	tool := &CreateDocumentTool{
		Cwd:                      cwd,
		WriteOpts:                WritePathOptions{Cwd: cwd, AllowedPaths: []string{filepath.Dir(outside)}},
		Sandbox:                  &fakeSandbox{label: "[podman-sandbox]"},
		SubprocessFormatsEnabled: true,
	}
	args := `{"format":"docx","output_path":"` + outside + `","content":{"blocks":[{"type":"paragraph","text":"hi"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "outside the sandbox-mounted workspace") {
		t.Fatalf("expected sandbox workspace-boundary error, got %v", err)
	}
}

func TestCreateDocumentPathsToSnapshot(t *testing.T) {
	cwd := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(cwd)}
	paths := tool.PathsToSnapshot(cwd, `{"format":"xlsx","output_path":"out/report.xlsx"}`)
	if len(paths) != 1 || !strings.HasSuffix(paths[0], filepath.Join("out", "report.xlsx")) {
		t.Fatalf("unexpected paths: %#v", paths)
	}
}

func TestCreateDocumentPreviewCallSandboxLabel(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), Sandbox: &fakeSandbox{label: "[podman-sandbox]"}}

	docxPreview := tool.PreviewCall(`{"format":"docx","output_path":"out.docx"}`)
	if !strings.HasPrefix(docxPreview, "[podman-sandbox] create_document(") {
		t.Errorf("expected docx preview to carry sandbox label, got %q", docxPreview)
	}

	xlsxPreview := tool.PreviewCall(`{"format":"xlsx","output_path":"out.xlsx"}`)
	if strings.Contains(xlsxPreview, "[podman-sandbox]") {
		t.Errorf("expected xlsx preview to omit sandbox label, got %q", xlsxPreview)
	}
}

func TestCreateDocumentSchemaHasExpectedFields(t *testing.T) {
	schema := (&CreateDocumentTool{}).Schema()
	b, _ := json.Marshal(schema)
	for _, want := range []string{"sheets", "blocks", "slides", "formula", "number_format", "overwrite", "language", "bullets", "layout", "spans", "item_spans", "path", "alt"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("schema missing %q: %s", want, b)
		}
	}
}

func TestBuildPandocCommand(t *testing.T) {
	docx := buildPandocCommand("docx", "/tmp/a b.docx", "/tmp/a b.md")
	if !strings.Contains(docx, "pandoc -f markdown -t docx -o") || !strings.Contains(docx, `'/tmp/a b.docx'`) {
		t.Errorf("unexpected docx command: %s", docx)
	}
	pdf := buildPandocCommand("pdf", "/tmp/out.pdf", "/tmp/in.md")
	if !strings.Contains(pdf, "--pdf-engine=weasyprint") {
		t.Errorf("expected pdf command to select weasyprint, got: %s", pdf)
	}
	if buildPandocCommand("xlsx", "a", "b") != "" {
		t.Errorf("expected empty command for a non-pandoc format")
	}
}

func TestShellQuoteSingle(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"":            "''",
		"has space":   "'has space'",
		"it's":        `'it'\''s'`,
		"$(rm -rf /)": "'$(rm -rf /)'",
		"`backtick`":  "'`backtick`'",
	}
	for in, want := range cases {
		if got := shellQuoteSingle(in); got != want {
			t.Errorf("shellQuoteSingle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPlaceOutputFile_ExclGuardRejectsExistingFile is the regression for
// review finding #8: called directly (not through Execute's earlier
// os.Stat pre-flight), placeOutputFile's own O_EXCL reservation must
// still refuse to replace an existing file when overwrite is false —
// this is the actual atomic guard; the pre-flight check is only a fast
// advisory rejection, not the safety guarantee.
func TestPlaceOutputFile_ExclGuardRejectsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "out.xlsx")
	if err := os.WriteFile(dst, []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}
	src := filepath.Join(tmp, "src.tmp")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatalf("seed src file: %v", err)
	}

	if err := placeOutputFile(src, dst, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected an 'already exists' error, got %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "existing" {
		t.Errorf("existing file must be untouched, got %q, %v", got, err)
	}
}

func TestPlaceOutputFile_OverwriteTrueReplaces(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "out.xlsx")
	if err := os.WriteFile(dst, []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}
	src := filepath.Join(tmp, "src.tmp")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatalf("seed src file: %v", err)
	}
	if err := placeOutputFile(src, dst, true); err != nil {
		t.Fatalf("placeOutputFile with overwrite=true: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "new" {
		t.Errorf("expected the destination to be replaced, got %q, %v", got, err)
	}
}

// TestCheckCommandAvailable_PodmanExitCodeGetsSpecificNote is the
// regression for review finding #9's first half: an exit=125 from a
// podman-labeled sandbox must be reported as a podman infra failure, not
// misdiagnosed as "pandoc not found" the way run_bash's own exit-125
// handling (exec_tool.go) already avoids. Reuses exitCodeSandbox from
// exec_tool_test.go — same package, same test double.
func TestCheckCommandAvailable_PodmanExitCodeGetsSpecificNote(t *testing.T) {
	sb := &exitCodeSandbox{label: podmanSandboxLabel, code: podmanInfraExitCode}
	err := checkCommandAvailable(context.Background(), sb, t.TempDir(), "pandoc")
	if err == nil || !strings.Contains(err.Error(), "podman-level failure") {
		t.Fatalf("expected a podman-infra-failure note, got %v", err)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("exit=125 must not be misreported as a missing binary, got %v", err)
	}
}

// probeOutputSandbox is a minimal Sandbox whose Command echoes fixed text
// to stderr before exiting non-zero, for verifying checkCommandAvailable
// surfaces the probe's captured output instead of discarding it.
type probeOutputSandbox struct{ label string }

func (s *probeOutputSandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", "echo custom probe diagnostic >&2; exit 2")
}
func (s *probeOutputSandbox) Label() string { return s.label }
func (s *probeOutputSandbox) Close() error  { return nil }

// TestCheckCommandAvailable_IncludesProbeOutput is the regression for
// review finding #9's second half: the probe's captured output must be
// included in the returned error, not silently discarded.
func TestCheckCommandAvailable_IncludesProbeOutput(t *testing.T) {
	sb := &probeOutputSandbox{label: "[custom-sandbox]"}
	err := checkCommandAvailable(context.Background(), sb, t.TempDir(), "pandoc")
	if err == nil || !strings.Contains(err.Error(), "custom probe diagnostic") {
		t.Fatalf("expected the probe's captured output in the error, got %v", err)
	}
}

func TestBuildDocxTemplateCommand(t *testing.T) {
	cmd := buildDocxTemplateCommand(
		"/opt/yottacode/doc-helpers/fill_docx_template.py",
		"/tmp/a b.docx", "/tmp/out.docx", "/tmp/repl.json")
	if !strings.HasPrefix(cmd, "python3 ") {
		t.Errorf("expected command to start with 'python3 ', got %q", cmd)
	}
	for _, want := range []string{
		"'/opt/yottacode/doc-helpers/fill_docx_template.py'",
		`'/tmp/a b.docx'`,
		"'/tmp/out.docx'",
		"'/tmp/repl.json'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("expected command to contain %q, got %q", want, cmd)
		}
	}
}

func TestParseReplacementsAppliedCount(t *testing.T) {
	if got := parseReplacementsAppliedCount([]byte(`{"replacements_applied": 3}`)); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
	if got := parseReplacementsAppliedCount([]byte("not json")); got != 0 {
		t.Errorf("expected 0 for malformed input, got %d", got)
	}
}

// TestCreateDocumentDocxTemplateRejectsDeniedPath is the template-path
// regression for the same credential-denylist enforcement
// TestCreateDocumentImageBlockRejectsDeniedPath locks for docx/pdf image
// blocks — template is create_document's third-ever read path.
func TestCreateDocumentDocxTemplateRejectsDeniedPath(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret.docx")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		DenyReadPaths:            []string{secret},
		SubprocessFormatsEnabled: true,
	}
	args := `{"format":"docx","output_path":"out.docx","template":"secret.docx","content":{"replacements":{"x":"y"}}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("expected a template-path denial error, got %v", err)
	}
}

// TestCreateDocumentDocxTemplateMissingPython3 confirms the template
// path is deterministically gated on python3 the same way the pandoc
// path is gated on pandoc — using fakeSandbox rather than real host
// state so the test doesn't depend on whether python3 happens to be
// installed on the machine running it.
func TestCreateDocumentDocxTemplateMissingPython3(t *testing.T) {
	tmp := t.TempDir()
	template := filepath.Join(tmp, "template.docx")
	if err := os.WriteFile(template, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		Sandbox:                  &fakeSandbox{label: "[podman-sandbox]", missing: "python3"},
		SubprocessFormatsEnabled: true,
	}
	args := `{"format":"docx","output_path":"out.docx","template":"template.docx","content":{"replacements":{"x":"y"}}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "python3 not found") {
		t.Fatalf("expected a 'python3 not found' error, got %v", err)
	}
}

// TestCreateDocumentDocxTemplateValidPathReachesPython3Check confirms a
// legitimate in-workspace template path clears validation and reaches
// the python3-availability check — not rejected as a read-path
// violation. Mirrors TestCreateDocumentImageBlockValidPathPassesValidation.
func TestCreateDocumentDocxTemplateValidPathReachesPython3Check(t *testing.T) {
	tmp := t.TempDir()
	template := filepath.Join(tmp, "template.docx")
	if err := os.WriteFile(template, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		Sandbox:                  &fakeSandbox{label: "[podman-sandbox]", missing: "python3"},
		SubprocessFormatsEnabled: true,
	}
	args := `{"format":"docx","output_path":"out.docx","template":"template.docx","content":{"replacements":{"x":"y"}}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "python3 not found") {
		t.Fatalf("expected to get past template validation to the python3-availability check, got %v", err)
	}
}

// TestCreateDocumentDocxTemplateEmptyContentBlocksIsNotAnError confirms
// setting template takes create_document down the template-fill branch
// instead of the pandoc content.blocks branch, which would otherwise
// fail on a different, misleading validation error (content.blocks is
// required) since no blocks are set here at all.
func TestCreateDocumentDocxTemplateEmptyContentBlocksIsNotAnError(t *testing.T) {
	tmp := t.TempDir()
	template := filepath.Join(tmp, "template.docx")
	if err := os.WriteFile(template, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		Sandbox:                  &fakeSandbox{label: "[podman-sandbox]", missing: "python3"},
		SubprocessFormatsEnabled: true,
	}
	// No content.blocks at all -- must not trip generateViaPandoc's
	// "content.blocks is required" check, since template routes to a
	// different method entirely.
	args := `{"format":"docx","output_path":"out.docx","template":"template.docx","content":{"replacements":{"x":"y"}}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || strings.Contains(err.Error(), "content.blocks is required") {
		t.Fatalf("expected the template branch to be taken (not the pandoc content.blocks branch), got %v", err)
	}
}
