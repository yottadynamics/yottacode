package agent

import (
	"strings"
	"testing"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

func TestFormatDiagnosticsSnapshotStates(t *testing.T) {
	if got := formatDiagnosticsSnapshot(lspci.DiagnosticsSnapshot{Published: true}); got != "(no diagnostics)\n" {
		t.Fatalf("published clean = %q", got)
	}
	if got := formatDiagnosticsSnapshot(lspci.DiagnosticsSnapshot{Published: false}); got != "(diagnostics not published before timeout)\n" {
		t.Fatalf("unpublished = %q", got)
	}
}

func TestFormatDiagnosticsSnapshotRichFields(t *testing.T) {
	got := formatDiagnosticsSnapshot(lspci.DiagnosticsSnapshot{Published: true, Diagnostics: []lspci.Diagnostic{{
		Path:      "main.go",
		Line:      1,
		Character: 2,
		Severity:  "warning",
		Source:    "gopls",
		Code:      "unusedparams",
		Tags:      []string{"unnecessary"},
		Message:   "unused parameter",
		Related:   []lspci.DiagnosticRelated{{Location: lspci.Location{Path: "other.go", Line: 3, Character: 4}, Message: "related note"}},
	}}})
	for _, want := range []string{"main.go:2:3", "source=gopls", "code=unusedparams", "tags=unnecessary", "related\tother.go:4:5\trelated note"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic output missing %q: %q", want, got)
		}
	}
}
