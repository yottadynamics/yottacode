package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/codemap"
)

func TestRegisterCoreCwdTools_CodeMapGate(t *testing.T) {
	reg := NewRegistry()
	cwd := NewCwdRef(t.TempDir())
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	if _, ok := reg.Get("code_map"); ok {
		t.Fatal("code_map should not register when EnableCodeMap is false")
	}

	reg = NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}, EnableCodeMap: true, CodeMapProvider: codemap.StaticProvider{Snapshot: emptyIndex(t)}})
	for _, name := range []string{"code_map", "code_symbols", "code_structure_projection", "code_dependencies", "code_dependents", "code_impact", "code_cycles", "code_map_diagram"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("%s should register when EnableCodeMap is true", name)
		}
	}
}

func TestCodeMapToolFormatsSnapshot(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeMapTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"max_results":10}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "directory") || !strings.Contains(out, "files=2") {
		t.Fatalf("unexpected code_map output: %q", out)
	}
}

func TestCodeSymbolsToolFiltersSymbols(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeSymbolsTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"query":"Run"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Run") || strings.Contains(out, "main.go\tfile") {
		t.Fatalf("unexpected code_symbols output: %q", out)
	}
}

func TestCodeDependenciesTool(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeDependenciesTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "dep.go") {
		t.Fatalf("unexpected code_dependencies output: %q", out)
	}
}

func TestCodeImpactTool(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "direct dependencies") || !strings.Contains(out, "dep.go") || !strings.Contains(out, "cycles") {
		t.Fatalf("unexpected code_impact output: %q", out)
	}
}

func TestCodeCyclesTool(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeCyclesTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "dep.go -> main.go -> dep.go") {
		t.Fatalf("unexpected code_cycles output: %q", out)
	}
}

func TestCodeMapDiagramTool(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeMapDiagramTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "```mermaid") || !strings.Contains(out, "main.go") || !strings.Contains(out, "-->") {
		t.Fatalf("unexpected code_map_diagram output: %q", out)
	}
}

func emptyIndex(t *testing.T) *codemap.CodeIndex {
	t.Helper()
	root := t.TempDir()
	rootID := codemap.NodeID("dir:.")
	fileID := codemap.NodeID("file:main.go")
	depID := codemap.NodeID("file:dep.go")
	symID := codemap.NodeID("sym:main.go:1:0:Run:0")
	nodes := map[codemap.NodeID]codemap.Node{
		rootID: {ID: rootID, Kind: codemap.NodeDirectory, Name: "repo", Path: root, RelPath: ".", Stats: codemap.Stats{Files: 2, Symbols: 1, LOC: 3, Exported: 1}},
		fileID: {ID: fileID, Parent: rootID, Kind: codemap.NodeFile, Name: "main.go", Path: root + "/main.go", RelPath: "main.go", Stats: codemap.Stats{Files: 1, Symbols: 1, LOC: 2, Exported: 1}},
		depID:  {ID: depID, Parent: rootID, Kind: codemap.NodeFile, Name: "dep.go", Path: root + "/dep.go", RelPath: "dep.go", Stats: codemap.Stats{Files: 1, LOC: 1}},
		symID:  {ID: symID, Parent: fileID, Kind: codemap.NodeSymbol, Name: "Run", Path: root + "/main.go", RelPath: "main.go", Symbol: codemap.SymbolInfo{Kind: "function", Exported: true, Range: codemap.Range{Start: codemap.Position{Line: 1}}}, Stats: codemap.Stats{Symbols: 1, Exported: 1}},
	}
	children := map[codemap.NodeID][]codemap.NodeID{rootID: {fileID, depID}, fileID: {symID}}
	edges := []codemap.Edge{{From: fileID, To: depID, Kind: codemap.EdgeImports, Meta: "dep"}, {From: depID, To: fileID, Kind: codemap.EdgeImports, Meta: "main"}}
	return codemap.NewIndex(root, rootID, nodes, children, edges)
}
