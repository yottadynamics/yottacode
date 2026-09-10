package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/codemap"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
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

func TestCodeImpactToolSummaryFormat(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","format":"summary"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "impact summary") || !strings.Contains(out, "dep.go") || strings.Contains(out, "direct dependencies\n") {
		t.Fatalf("unexpected code_impact summary output: %q", out)
	}
}

func TestCodeImpactToolIncludeCallsSupplement(t *testing.T) {
	idx := emptyIndex(t)
	writeFile(t, idx.RootPath(), "main.go", "package main\nfunc Run() {}\n")
	tool := &CodeImpactTool{
		Provider: codemap.StaticProvider{Snapshot: idx},
		LSP: lspToolBase{Cwd: NewCwdRef(idx.RootPath()), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{calls: []lspci.CallHierarchyItem{
				{Name: "caller", Kind: "function", Direction: "incoming", Location: lspci.Location{Path: "caller.go", Line: 3, Character: 2}},
				{Name: "callee", Kind: "function", Direction: "outgoing", Location: lspci.Location{Path: "callee.go", Line: 5, Character: 1}},
			}}, nil
		}},
	}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","include_calls":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"callers (LSP)", "caller.go", "callees (LSP)", "callee.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("include_calls output missing %q: %q", want, out)
		}
	}
}

func TestCodeImpactToolIncludeCallsFalseByDefault(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "callers (LSP)") {
		t.Fatalf("include_calls should default to off: %q", out)
	}
}

// TestCodeImpactToolIncludeCallsSkipsUnmatchedTarget guards against a noisy
// "callers (LSP)\n  (none)\ncallees (LSP)\n  (none)" tail on a query that
// didn't even match a file — include_calls should be silent when there's no
// target to query call hierarchy for in the first place.
func TestCodeImpactToolIncludeCallsSkipsUnmatchedTarget(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}}
	out, err := tool.Execute(context.Background(), `{"path":"does-not-exist.go","include_calls":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "callers (LSP)") || strings.Contains(out, "callees (LSP)") {
		t.Fatalf("include_calls should be silent when the target didn't match: %q", out)
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
	if tool.RequiresApproval(`{"path":"main.go"}`) {
		t.Fatal("a plain read should not require approval")
	}
	if !tool.ParallelSafe(`{"path":"main.go"}`) {
		t.Fatal("a plain read should be parallel-safe")
	}
}

// TestCodeMapDiagramToolToFileRequiresPath guards a real bug found via live
// testing: to_file with no path focus rendered the FULL unfocused import
// graph to disk (24k+ edges, several MB, on this repo) — exactly the
// "full-repo hairball graph" docs/code-map.md's non-goals section says this
// tool never produces. to_file must require a focus path rather than
// silently writing that.
func TestCodeMapDiagramToolToFileRequiresPath(t *testing.T) {
	idx := emptyIndex(t)
	cwd := NewCwdRef(t.TempDir())
	tool := &CodeMapDiagramTool{Provider: codemap.StaticProvider{Snapshot: idx}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	if _, err := tool.Execute(context.Background(), `{"to_file":"out/diagram.md"}`); err == nil {
		t.Fatal("to_file with no path should be rejected, not silently write the full unfocused graph")
	}
	if _, err := os.Stat(filepath.Join(cwd.Get(), "out/diagram.md")); err == nil {
		t.Fatal("no file should have been written when the request was rejected")
	}
}

func TestCodeMapDiagramToolToFileWritesFullOutput(t *testing.T) {
	idx := emptyIndex(t)
	cwd := NewCwdRef(t.TempDir())
	tool := &CodeMapDiagramTool{Provider: codemap.StaticProvider{Snapshot: idx}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	argsJSON := `{"path":"main.go","to_file":"out/diagram.md"}`

	if !tool.RequiresApproval(argsJSON) {
		t.Fatal("to_file should require approval like any other write")
	}
	if tool.ParallelSafe(argsJSON) {
		t.Fatal("to_file should not be parallel-safe")
	}
	paths := tool.PathsToSnapshot(cwd.Get(), argsJSON)
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "out/diagram.md") {
		t.Fatalf("PathsToSnapshot = %v, want out/diagram.md", paths)
	}

	out, err := tool.Execute(context.Background(), argsJSON)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "written to") || !strings.Contains(out, "diagram.md") {
		t.Fatalf("unexpected confirmation: %q", out)
	}
	written, err := os.ReadFile(filepath.Join(cwd.Get(), "out/diagram.md"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(written), "```mermaid") {
		t.Fatalf("written diagram missing mermaid fence: %q", written)
	}
}

func TestCodeMapDiagramToolToFileRespectsDenyList(t *testing.T) {
	idx := emptyIndex(t)
	cwd := NewCwdRef(t.TempDir())
	tool := &CodeMapDiagramTool{Provider: codemap.StaticProvider{Snapshot: idx}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd, DenyExact: []string{filepath.Join(cwd.Get(), "permissions.json")}}}
	if _, err := tool.Execute(context.Background(), `{"path":"main.go","to_file":"permissions.json"}`); err == nil {
		t.Fatal("to_file should go through the normal write-path validator and refuse a denied path")
	}
}

func TestCodeStructureProjectionToolToFile(t *testing.T) {
	idx := emptyIndex(t)
	cwd := NewCwdRef(t.TempDir())
	tool := &CodeStructureProjectionTool{Provider: codemap.StaticProvider{Snapshot: idx}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	out, err := tool.Execute(context.Background(), `{"to_file":"projection.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "written to") {
		t.Fatalf("unexpected confirmation: %q", out)
	}
	if _, err := os.Stat(filepath.Join(cwd.Get(), "projection.txt")); err != nil {
		t.Fatalf("expected projection.txt to exist: %v", err)
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
