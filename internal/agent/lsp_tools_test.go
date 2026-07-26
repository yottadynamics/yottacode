package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

type fakeLSPClient struct {
	symbols     []lspci.Symbol
	docSymbols  []lspci.Symbol
	locs        []lspci.Location
	signatures  lspci.SignatureHelp
	diagnostics []lspci.Diagnostic
	actions     []lspci.CodeAction
	calls       []lspci.CallHierarchyItem
}

func (f *fakeLSPClient) WorkspaceSymbols(context.Context, string) ([]lspci.Symbol, error) {
	return f.symbols, nil
}
func (f *fakeLSPClient) DocumentSymbols(context.Context, string) ([]lspci.Symbol, error) {
	return f.docSymbols, nil
}
func (f *fakeLSPClient) Definition(context.Context, string, lspci.Position) ([]lspci.Location, error) {
	return f.locs, nil
}
func (f *fakeLSPClient) TypeDefinition(context.Context, string, lspci.Position) ([]lspci.Location, error) {
	return f.locs, nil
}
func (f *fakeLSPClient) Implementation(context.Context, string, lspci.Position) ([]lspci.Location, error) {
	return f.locs, nil
}
func (f *fakeLSPClient) References(context.Context, string, lspci.Position, bool) ([]lspci.Location, error) {
	return f.locs, nil
}
func (f *fakeLSPClient) Hover(context.Context, string, lspci.Position) (string, error) {
	return "hover text", nil
}
func (f *fakeLSPClient) SignatureHelp(context.Context, string, lspci.Position) (lspci.SignatureHelp, error) {
	if len(f.signatures.Signatures) > 0 {
		return f.signatures, nil
	}
	return lspci.SignatureHelp{Signatures: []lspci.SignatureInformation{{Label: "fmt.Println(a ...any)", Parameters: []lspci.ParameterInformation{{Label: "a ...any"}}}}}, nil
}
func (f *fakeLSPClient) Diagnostics(context.Context, string) (lspci.DiagnosticsSnapshot, error) {
	if f.diagnostics != nil {
		return lspci.DiagnosticsSnapshot{Published: true, Diagnostics: f.diagnostics}, nil
	}
	return lspci.DiagnosticsSnapshot{Published: true, Diagnostics: []lspci.Diagnostic{{Path: "main.go", Line: 0, Character: 1, Severity: "error", Source: "test", Message: "bad"}}}, nil
}
func (f *fakeLSPClient) CodeActions(context.Context, string, lspci.Position, lspci.Position) ([]lspci.CodeAction, error) {
	if f.actions != nil {
		return f.actions, nil
	}
	return []lspci.CodeAction{{Kind: "quickfix", Title: "Fix it"}}, nil
}
func (f *fakeLSPClient) CodeActionPreview(_ context.Context, path string, _ lspci.Position, _ lspci.Position, _ string, _ int) (lspci.WorkspaceEdit, error) {
	if len(f.actions) > 0 && f.actions[0].HasEdit {
		return lspci.WorkspaceEdit{Edits: []lspci.TextEdit{{Path: path, Range: lspci.TextRange{Start: lspci.Position{Line: 1, Character: 0}, End: lspci.Position{Line: 1, Character: 0}}, NewText: "// fixed\n"}}}, nil
	}
	return lspci.WorkspaceEdit{}, errors.New("no edit")
}
func (f *fakeLSPClient) RenamePreview(_ context.Context, path string, _ lspci.Position, _ string) (lspci.WorkspaceEdit, error) {
	return lspci.WorkspaceEdit{Edits: []lspci.TextEdit{{Path: path, Range: lspci.TextRange{Start: lspci.Position{Line: 0, Character: 0}, End: lspci.Position{Line: 0, Character: 0}}, NewText: "// renamed\n"}}}, nil
}
func (f *fakeLSPClient) FormatPreview(_ context.Context, path string) (lspci.WorkspaceEdit, error) {
	return lspci.WorkspaceEdit{Edits: []lspci.TextEdit{{Path: path, Range: lspci.TextRange{Start: lspci.Position{Line: 0, Character: 0}, End: lspci.Position{Line: 0, Character: 0}}, NewText: "// formatted\n"}}}, nil
}
func (f *fakeLSPClient) CallHierarchy(context.Context, string, lspci.Position) ([]lspci.CallHierarchyItem, error) {
	if f.calls != nil {
		return f.calls, nil
	}
	return []lspci.CallHierarchyItem{{Name: "caller", Kind: "function", Direction: "incoming", Location: lspci.Location{Path: "main.go", Line: 1, Character: 2}}}, nil
}
func (f *fakeLSPClient) Close() error { return nil }

func TestLSPStatusDetectsLanguagesAndHints(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	tool := &LSPStatusTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Go") || !strings.Contains(out, "server=gopls") {
		t.Errorf("status should report Go/gopls: %q", out)
	}
	if strings.Contains(out, "status=missing") && !strings.Contains(out, "go install golang.org/x/tools/gopls") {
		t.Errorf("missing gopls should include install hint: %q", out)
	}
}

func TestLSPSymbolsUsesInjectedClient(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	tool := &LSPSymbolsTool{lspToolBase: lspToolBase{
		Cwd: NewCwdRef(tmp),
		NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{symbols: []lspci.Symbol{{Name: "RegisterCoreCwdTools", Kind: "function", Container: "agent", Location: lspci.Location{Path: "internal/agent/toolset.go", Line: 35, Character: 5}}}}, nil
		},
	}}
	out, err := tool.Execute(context.Background(), `{"query":"Register","path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "internal/agent/toolset.go:36:6") || !strings.Contains(out, "RegisterCoreCwdTools") {
		t.Errorf("unexpected symbols output: %q", out)
	}
}

func TestLSPSymbolsFiltersDependencyResults(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	tool := &LSPSymbolsTool{lspToolBase: lspToolBase{
		Cwd: NewCwdRef(tmp),
		NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{symbols: []lspci.Symbol{
				{Name: "LocalNewManager", Kind: "function", Container: "local", Location: lspci.Location{Path: filepath.Join(tmp, "internal/lsp/manager.go"), Line: 50, Character: 5}},
				{Name: "DependencyNewManager", Kind: "function", Container: "dep", Location: lspci.Location{Path: filepath.Join(os.TempDir(), "pkg/mod/example/manager.go"), Line: 10, Character: 1}},
				{Name: "HomeModule", Kind: "function", Container: "dep", Location: lspci.Location{Path: "~/go/pkg/mod/example/manager.go", Line: 20, Character: 1}},
			}}, nil
		},
	}}
	out, err := tool.Execute(context.Background(), `{"query":"NewManager","path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "LocalNewManager") {
		t.Fatalf("workspace symbol missing from output: %q", out)
	}
	if strings.Contains(out, "DependencyNewManager") || strings.Contains(out, "HomeModule") {
		t.Fatalf("dependency symbols should be filtered from output: %q", out)
	}
}

func TestLSPDefinitionUnsupportedFileReturnsUnavailable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "README.md", "# hi")
	tool := &LSPDefinitionTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"README.md","line":0,"character":0}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "unavailable: no supported LSP language") {
		t.Errorf("expected unsupported-language unavailable result, got %q", out)
	}
}

func TestLSPDefinitionFormatsLocations(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	base := lspToolBase{
		Cwd: NewCwdRef(tmp),
		NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{locs: []lspci.Location{{Path: "main.go", Line: 2, Character: 4}}}, nil
		},
	}
	for _, tc := range []struct {
		name string
		tool Tool
	}{
		{"definition", &LSPDefinitionTool{lspToolBase: base}},
		{"type definition", &LSPTypeDefinitionTool{lspToolBase: base}},
		{"implementation", &LSPImplementationTool{lspToolBase: base}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.tool.Execute(context.Background(), `{"path":"main.go","line":0,"character":0}`)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if strings.TrimSpace(out) != "main.go:3:5" {
				t.Errorf("location output = %q, want main.go:3:5", out)
			}
		})
	}
}

func TestLSPClientStartErrorIncludesInstallHint(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	tool := &LSPSymbolsTool{lspToolBase: lspToolBase{
		Cwd: NewCwdRef(tmp),
		NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return nil, errors.New("boom")
		},
	}}
	out, err := tool.Execute(context.Background(), `{"query":"x","path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "could not start") || !strings.Contains(out, "Install gopls") {
		t.Errorf("start failure should include install hint: %q", out)
	}
}

func TestRegisterCoreCwdTools_LSPGate(t *testing.T) {
	reg := NewRegistry()
	cwd := NewCwdRef(t.TempDir())
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	if _, ok := reg.Get("lsp_status"); ok {
		t.Fatal("lsp_status should not register when EnableLSP is false")
	}
	reg = NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}, EnableLSP: true})
	for _, name := range []string{"lsp_status", "lsp_symbols", "lsp_document_symbols", "lsp_definition", "lsp_type_definition", "lsp_implementation", "lsp_references", "lsp_diagnostics", "lsp_changed_files_diagnostics", "lsp_hover", "lsp_signature_help", "lsp_code_actions", "lsp_code_action_preview", "lsp_rename_preview", "lsp_format_preview", "lsp_apply_workspace_edit", "lsp_call_hierarchy"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("%s should register when EnableLSP is true", name)
		}
	}
}
