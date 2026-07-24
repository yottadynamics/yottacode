package agent

import (
	"context"
	"strings"
	"testing"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

func TestLSPExtraToolsUseInjectedClient(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main\nfunc main() {}\n")
	base := lspToolBase{
		Cwd: NewCwdRef(tmp),
		NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{
				diagnostics: []lspci.Diagnostic{{Path: "main.go", Line: 1, Character: 6, Severity: "warning", Source: "gopls", Message: "unused"}},
				actions:     []lspci.CodeAction{{Kind: "quickfix", Title: "Remove unused"}},
				calls:       []lspci.CallHierarchyItem{{Direction: "outgoing", Name: "fmt.Println", Kind: "function", Detail: "fmt", Location: lspci.Location{Path: "main.go", Line: 1, Character: 12}}},
			}, nil
		},
	}
	cases := []struct {
		name string
		tool Tool
		args string
		want string
	}{
		{"hover", &LSPHoverTool{lspToolBase: base}, `{"path":"main.go","line":1,"character":5}`, "hover text"},
		{"diagnostics", &LSPDiagnosticsTool{lspToolBase: base}, `{"path":"main.go"}`, "main.go:2:7\twarning\tgopls\tunused"},
		{"code actions", &LSPCodeActionsTool{lspToolBase: base}, `{"path":"main.go","line":1,"character":0,"end_line":1,"end_character":4}`, "quickfix\tRemove unused"},
		{"call hierarchy", &LSPCallHierarchyTool{lspToolBase: base}, `{"path":"main.go","line":1,"character":5}`, "outgoing\tmain.go:2:13\tfunction\tfmt.Println\tfmt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output %q does not contain %q", out, tc.want)
			}
		})
	}
}

func TestLSPSymbolsFallbackWhenServerMissing(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main\nfunc FallbackTarget() {}\n")
	tool := &LSPSymbolsTool{lspToolBase: lspToolBase{
		Cwd:     NewCwdRef(tmp),
		Servers: map[string][]string{"go": {"definitely-missing-gopls"}},
	}}
	out, err := tool.Execute(context.Background(), `{"query":"FallbackTarget","path":"."}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "fallback:") || !strings.Contains(out, "FallbackTarget") {
		t.Fatalf("expected regex fallback symbol output, got %q", out)
	}
}

func TestLSPPositionToolRespectsServerOverride(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	tool := &LSPDefinitionTool{lspToolBase: lspToolBase{
		Cwd:     NewCwdRef(tmp),
		Servers: map[string][]string{"go": {"definitely-missing-gopls"}},
	}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":0,"character":0}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, `"definitely-missing-gopls" not found on PATH`) {
		t.Fatalf("expected override command in unavailable result, got %q", out)
	}
}
