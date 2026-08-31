//go:build integration

// Integration test for CreateDocumentTool's docx template-fill path
// against a real python3 + python-docx — the first use of the
// integration build tag in this package, mirroring
// internal/documents/pdf_integration_test.go's own convention. Skips
// gracefully if python-docx isn't importable, so `go test ./...` (no
// -tags) never depends on it; run explicitly with
// `go test -tags integration ./internal/agent/...` when python3 and
// python-docx are both installed (e.g. inside a container built from
// infra/documents.Containerfile, or the venv used to develop this
// feature).
package agent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requirePythonDocx(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; skipping integration test")
	}
	if err := exec.Command("python3", "-c", "import docx").Run(); err != nil {
		t.Skip("python-docx not importable; skipping integration test")
	}
}

// TestCreateDocumentDocxTemplate_RealPythonDocxRoundTrip exercises the
// real host path end to end: HostSandbox execs the real python3 +
// python-docx (via pyhelpers/fill_docx_template.py, materialized from
// the embed the same way a host-only production session would), filling
// a real template built with python-docx itself — the same library
// that's already required to be present for this test to run at all,
// so the fixture doesn't need pandoc as an extra dependency.
func TestCreateDocumentDocxTemplate_RealPythonDocxRoundTrip(t *testing.T) {
	requirePythonDocx(t)

	tmp := t.TempDir()
	templatePath := filepath.Join(tmp, "template.docx")
	buildTemplate := fmt.Sprintf(`
from docx import Document
doc = Document()
doc.add_paragraph("Dear {{name}}, your total is {{total}}.")
doc.add_paragraph("This paragraph is untouched.")
doc.save(%q)
`, templatePath)
	if out, err := exec.Command("python3", "-c", buildTemplate).CombinedOutput(); err != nil {
		t.Fatalf("build fixture template: %v: %s", err, out)
	}

	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		SubprocessFormatsEnabled: true,
	}
	args := fmt.Sprintf(
		`{"format":"docx","output_path":"out.docx","template":%q,"content":{"replacements":{"name":"Jane","total":"$10"}}}`,
		templatePath)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Both tokens are in the same paragraph, so exactly one paragraph
	// is touched — replacements_applied counts paragraphs, not tokens.
	if !strings.Contains(out, "(1 replacements applied)") {
		t.Errorf("unexpected result message: %q", out)
	}

	outputPath := filepath.Join(tmp, "out.docx")
	verify := fmt.Sprintf(`
from docx import Document
doc = Document(%q)
first = doc.paragraphs[0].text
second = doc.paragraphs[1].text
assert "Jane" in first and "$10" in first, first
assert second == "This paragraph is untouched.", second
`, outputPath)
	if out, err := exec.Command("python3", "-c", verify).CombinedOutput(); err != nil {
		t.Fatalf("verify output docx: %v: %s", err, out)
	}
}

// TestCreateDocumentDocxTemplate_TablesHeadersFooters is the regression
// for code paths fill_docx_template.py always had (it walks doc.tables
// and every section's header/footer paragraphs) but that were never
// exercised by a real fixture until now — every prior test only used
// plain body paragraphs. Runs through CreateDocumentTool.Execute end to
// end, not the script directly.
func TestCreateDocumentDocxTemplate_TablesHeadersFooters(t *testing.T) {
	requirePythonDocx(t)

	tmp := t.TempDir()
	templatePath := filepath.Join(tmp, "template.docx")
	buildTemplate := fmt.Sprintf(`
from docx import Document
doc = Document()
doc.add_paragraph("Body: {{body_token}}")
table = doc.add_table(rows=1, cols=1)
table.cell(0, 0).text = "{{table_token}}"
section = doc.sections[0]
section.header.paragraphs[0].text = "Header: {{header_token}}"
section.footer.paragraphs[0].text = "Footer: {{footer_token}}"
doc.save(%q)
`, templatePath)
	if out, err := exec.Command("python3", "-c", buildTemplate).CombinedOutput(); err != nil {
		t.Fatalf("build fixture template: %v: %s", err, out)
	}

	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		SubprocessFormatsEnabled: true,
	}
	args := fmt.Sprintf(
		`{"format":"docx","output_path":"out.docx","template":%q,"content":{"replacements":`+
			`{"body_token":"BODY_OK","table_token":"TABLE_OK","header_token":"HEADER_OK","footer_token":"FOOTER_OK"}}}`,
		templatePath)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "(4 replacements applied)") {
		t.Errorf("unexpected result message: %q", out)
	}

	outputPath := filepath.Join(tmp, "out.docx")
	verify := fmt.Sprintf(`
from docx import Document
doc = Document(%q)
assert doc.paragraphs[0].text == "Body: BODY_OK", doc.paragraphs[0].text
assert doc.tables[0].cell(0, 0).text == "TABLE_OK", doc.tables[0].cell(0, 0).text
assert doc.sections[0].header.paragraphs[0].text == "Header: HEADER_OK", doc.sections[0].header.paragraphs[0].text
assert doc.sections[0].footer.paragraphs[0].text == "Footer: FOOTER_OK", doc.sections[0].footer.paragraphs[0].text
`, outputPath)
	if out, err := exec.Command("python3", "-c", verify).CombinedOutput(); err != nil {
		t.Fatalf("verify output docx: %v: %s", err, out)
	}
}

// TestCreateDocumentDocxTemplate_SplitRunAndUnmatchedTokens covers two
// things this feature's documentation got wrong in an earlier draft
// (corrected after actually testing it): a placeholder split across
// multiple <w:r> runs by Word's own spell-check/autocorrect DOES match
// correctly, because matching runs against each paragraph's full
// concatenated text, not run by run. What genuinely doesn't match — and
// must degrade to literal untouched text, not an error or corruption —
// is a token with no corresponding replacements key, and malformed
// placeholder syntax.
func TestCreateDocumentDocxTemplate_SplitRunAndUnmatchedTokens(t *testing.T) {
	requirePythonDocx(t)

	tmp := t.TempDir()
	templatePath := filepath.Join(tmp, "template.docx")
	buildTemplate := fmt.Sprintf(`
from docx import Document
doc = Document()
p = doc.add_paragraph()
p.add_run("Dear {{na")
p.add_run("me}}, your total is ")
p.add_run("{{total}}.")
doc.add_paragraph("Known: {{known}}. Unknown: {{totally_unknown_token}}.")
doc.add_paragraph("Malformed: {{unclosed and {{}} empty braces.")
doc.save(%q)
`, templatePath)
	if out, err := exec.Command("python3", "-c", buildTemplate).CombinedOutput(); err != nil {
		t.Fatalf("build fixture template: %v: %s", err, out)
	}

	tool := &CreateDocumentTool{
		Cwd:                      NewCwdRef(tmp),
		WriteOpts:                WritePathOptions{Cwd: NewCwdRef(tmp)},
		SubprocessFormatsEnabled: true,
	}
	args := fmt.Sprintf(
		`{"format":"docx","output_path":"out.docx","template":%q,"content":{"replacements":`+
			`{"name":"Jane","total":"$99","known":"MATCHED"}}}`,
		templatePath)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	outputPath := filepath.Join(tmp, "out.docx")
	verify := fmt.Sprintf(`
from docx import Document
doc = Document(%q)
assert doc.paragraphs[0].text == "Dear Jane, your total is $99.", doc.paragraphs[0].text
assert doc.paragraphs[1].text == "Known: MATCHED. Unknown: {{totally_unknown_token}}.", doc.paragraphs[1].text
assert doc.paragraphs[2].text == "Malformed: {{unclosed and {{}} empty braces.", doc.paragraphs[2].text
`, outputPath)
	if out, err := exec.Command("python3", "-c", verify).CombinedOutput(); err != nil {
		t.Fatalf("verify output docx: %v: %s", err, out)
	}
}
