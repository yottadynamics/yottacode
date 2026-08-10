package agent

import (
	"context"
	"encoding/json"
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
	tool := &CreateDocumentTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"format":"docx","output_path":"out.docx","content":{}}`)
	if err == nil || !strings.Contains(err.Error(), "content.blocks is required") {
		t.Fatalf("expected content.blocks validation error, got %v", err)
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
		Cwd:       NewCwdRef(tmp),
		WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)},
		Sandbox:   &fakeSandbox{label: "[podman-sandbox]", fail: true},
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
		Cwd:       NewCwdRef(tmp),
		WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)},
		Sandbox:   &fakeSandbox{label: "[podman-sandbox]", missing: "weasyprint"},
	}
	args := `{"format":"pdf","output_path":"out.pdf","content":{"blocks":[{"type":"paragraph","text":"hi"}]}}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "weasyprint not found") {
		t.Fatalf("expected 'weasyprint not found' error, got %v", err)
	}
}

func TestCreateDocumentSandboxedPandocRequiresWorkspaceOutput(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(t.TempDir(), "out.docx")
	cwd := NewCwdRef(tmp)
	tool := &CreateDocumentTool{
		Cwd:       cwd,
		WriteOpts: WritePathOptions{Cwd: cwd, AllowedPaths: []string{filepath.Dir(outside)}},
		Sandbox:   &fakeSandbox{label: "[podman-sandbox]"},
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
	for _, want := range []string{"sheets", "blocks", "formula", "number_format", "overwrite", "language"} {
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
