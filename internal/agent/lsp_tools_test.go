package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

type fakeLSPClient struct {
	symbols []lspci.Symbol
	locs    []lspci.Location
}

func (f *fakeLSPClient) WorkspaceSymbols(context.Context, string) ([]lspci.Symbol, error) {
	return f.symbols, nil
}
func (f *fakeLSPClient) Definition(context.Context, string, lspci.Position) ([]lspci.Location, error) {
	return f.locs, nil
}
func (f *fakeLSPClient) References(context.Context, string, lspci.Position, bool) ([]lspci.Location, error) {
	return f.locs, nil
}
func (f *fakeLSPClient) Hover(context.Context, string, lspci.Position) (string, error) {
	return "hover text", nil
}
func (f *fakeLSPClient) Diagnostics(context.Context, string) ([]lspci.Diagnostic, error) {
	return []lspci.Diagnostic{{Path: "main.go", Line: 0, Character: 1, Severity: "error", Source: "test", Message: "bad"}}, nil
}
func (f *fakeLSPClient) CodeActions(context.Context, string, lspci.Position, lspci.Position) ([]lspci.CodeAction, error) {
	return []lspci.CodeAction{{Kind: "quickfix", Title: "Fix it"}}, nil
}
func (f *fakeLSPClient) CallHierarchy(context.Context, string, lspci.Position) ([]lspci.CallHierarchyItem, error) {
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
	tool := &LSPDefinitionTool{lspToolBase: lspToolBase{
		Cwd: NewCwdRef(tmp),
		NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{locs: []lspci.Location{{Path: "main.go", Line: 2, Character: 4}}}, nil
		},
	}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":0,"character":0}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(out) != "main.go:3:5" {
		t.Errorf("location output = %q, want main.go:3:5", out)
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
	for _, name := range []string{"lsp_status", "lsp_symbols", "lsp_definition", "lsp_references"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("%s should register when EnableLSP is true", name)
		}
	}
}
