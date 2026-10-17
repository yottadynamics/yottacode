package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/codemap"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

func staticImpactTool(idx *codemap.CodeIndex) *CodeImpactTool {
	return &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}}
}

func TestRegisterCoreCwdTools_CodeMapGate(t *testing.T) {
	reg := NewRegistry()
	cwd := NewCwdRef(t.TempDir())
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	if _, ok := reg.Get("code_impact"); ok {
		t.Fatal("code_impact should not register when EnableCodeMap is false")
	}

	reg = NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}, EnableCodeMap: true, CodeMapProvider: codemap.StaticProvider{Snapshot: emptyIndex(t)}})
	if _, ok := reg.Get("code_impact"); !ok {
		t.Fatal("code_impact should register when EnableCodeMap is true")
	}
	for _, gone := range []string{"code_map", "code_symbols", "code_structure_projection", "code_dependencies", "code_dependents", "code_cycles", "code_map_diagram"} {
		if _, ok := reg.Get(gone); ok {
			t.Errorf("%s was folded into code_impact and must not register", gone)
		}
	}
}

func TestCodeImpactDefaultModeLabelsProvenanceAndIndex(t *testing.T) {
	out, err := staticImpactTool(emptyIndex(t)).Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"code_impact impact main.go",
		"index: 2 files, version ",
		"direct dependents [import] (1)",
		"indirect dependents (depth 3) [import] (0)",
		"direct dependencies [import] (1)",
		"test directories to run (package level) [import] (0)",
		"likely docs/config [heuristic] (0)",
		"cycles [import] (1)",
		"dep.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCodeImpactModes(t *testing.T) {
	tool := staticImpactTool(emptyIndex(t))
	cases := []struct {
		args string
		want []string
		not  []string
	}{
		{`{"path":"main.go","mode":"dependents"}`, []string{"code_impact dependents main.go", "dependents [import] (1)", "dep.go"}, []string{"test directories"}},
		{`{"path":"main.go","mode":"dependencies"}`, []string{"dependencies [import] (1)", "dep.go"}, []string{"test directories"}},
		{`{"path":"main.go","mode":"cycles"}`, []string{"cycles [import] (1)", "main.go -> dep.go -> main.go"}, []string{"dependents"}},
	}
	for _, c := range cases {
		out, err := tool.Execute(context.Background(), c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.args, err)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q in:\n%s", c.args, w, out)
			}
		}
		for _, n := range c.not {
			if strings.Contains(out, n) {
				t.Errorf("%s: unexpected %q in:\n%s", c.args, n, out)
			}
		}
	}
}

func TestCodeImpactRejectsBadArgs(t *testing.T) {
	tool := staticImpactTool(emptyIndex(t))
	for _, args := range []string{`{}`, `{"path":"  "}`, `{"path":"main.go","mode":"diagram"}`, `{"path":"main.go","scope":"module"}`, `not json`} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("Execute(%s) should fail", args)
		}
	}
}

func TestCodeImpactUnmatchedPathIsAnswerNotError(t *testing.T) {
	out, err := staticImpactTool(emptyIndex(t)).Execute(context.Background(), `{"path":"nope.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no indexed file matches") || !strings.Contains(out, "index: ") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestCodeImpactDisabledProviderErrors(t *testing.T) {
	if _, err := (&CodeImpactTool{}).Execute(context.Background(), `{"path":"main.go"}`); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("err = %v, want a not-enabled error", err)
	}
}

// TestCodeImpactFlagsPartialIndex guards the silent-truncation defect: when the
// file cap stops the walk, "no dependents" must not read as authoritative.
func TestCodeImpactFlagsPartialIndex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package main\nfunc A() {}\n")
	writeFile(t, root, "b.go", "package main\nfunc B() {}\n")
	idx, err := codemap.Build(context.Background(), codemap.BuildOptions{Root: root, MaxFiles: 1})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, err := staticImpactTool(idx).Execute(context.Background(), `{"path":"a.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "PARTIAL") {
		t.Fatalf("capped index not flagged:\n%s", out)
	}
}

func TestCodeImpactBudgetOmitsWithCount(t *testing.T) {
	root := t.TempDir()
	idx := fanInIndex(t, root, 80)
	out, err := staticImpactTool(idx).Execute(context.Background(), `{"path":"core.go","mode":"dependents","budget":100}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "dependents [import] (80)") || !strings.Contains(out, "more omitted: output budget") {
		t.Fatalf("budget trim not reported with true count:\n%s", out)
	}
	if len(out) > 100*approxBytesPerToken+200 {
		t.Fatalf("output %d bytes exceeds the budget:\n%s", len(out), out)
	}
}

func TestCodeImpactBudgetClamped(t *testing.T) {
	if got := codeImpactBudgetBytes(0); got != codeImpactDefaultBudget*approxBytesPerToken {
		t.Errorf("default budget = %d", got)
	}
	if got := codeImpactBudgetBytes(1_000_000); got != codeImpactMaxBudget*approxBytesPerToken {
		t.Errorf("clamped budget = %d", got)
	}
}

func TestCodeImpactDeterministic(t *testing.T) {
	tool := staticImpactTool(emptyIndex(t))
	first, _ := tool.Execute(context.Background(), `{"path":"main.go"}`)
	second, _ := tool.Execute(context.Background(), `{"path":"main.go"}`)
	if first != second {
		t.Fatalf("same query rendered differently:\n%s\n---\n%s", first, second)
	}
}

func TestCodeImpactDepthLimitsTransitiveDependents(t *testing.T) {
	root := t.TempDir()
	idx := chainIndex(t, root, 6) // f0 <- f1 <- f2 ... <- f5 (f(n) imports f(n-1))
	tool := staticImpactTool(idx)
	shallow, _ := tool.Execute(context.Background(), `{"path":"f0.go","depth":1}`)
	deep, _ := tool.Execute(context.Background(), `{"path":"f0.go","depth":-1}`)
	if strings.Contains(shallow, "f4.go") {
		t.Errorf("depth 1 followed too far:\n%s", shallow)
	}
	if !strings.Contains(deep, "f5.go") || !strings.Contains(deep, "indirect dependents (depth all)") {
		t.Errorf("depth -1 should reach the whole chain:\n%s", deep)
	}
	def, _ := tool.Execute(context.Background(), `{"path":"f0.go"}`)
	if !strings.Contains(def, "(depth 3)") || strings.Contains(def, "f5.go") {
		t.Errorf("default depth should be 3:\n%s", def)
	}
}

func TestCodeImpactHidesDeniedFiles(t *testing.T) {
	idx := emptyIndex(t)
	deny := []string{filepath.Join(idx.RootPath(), "dep.go")}
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}, DenyReadPaths: deny}
	out, err := tool.Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "dep.go") {
		t.Fatalf("denied file leaked through the graph:\n%s", out)
	}
	if _, err := tool.Execute(context.Background(), `{"path":"dep.go"}`); err == nil {
		t.Fatal("a denied target must be refused")
	}
}

func TestCodeImpactIsReadOnlyAndParallelSafe(t *testing.T) {
	tool := staticImpactTool(emptyIndex(t))
	if tool.RequiresApproval(`{"path":"main.go"}`) || !tool.ParallelSafe(`{"path":"main.go"}`) {
		t.Fatal("code_impact is a read-only, parallel-safe query")
	}
	if got := tool.PreviewCall(`{"path":"main.go","mode":"cycles"}`); got != "code_impact(main.go, cycles)" {
		t.Errorf("PreviewCall = %q", got)
	}
}

func TestCodeImpactIncludeCallsSupplement(t *testing.T) {
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
	for _, want := range []string{"calls [lsp]", "caller caller.go", "callee callee.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("include_calls output missing %q: %q", want, out)
		}
	}
}

func TestCodeImpactIncludeCallsFalseByDefault(t *testing.T) {
	out, err := staticImpactTool(emptyIndex(t)).Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "[lsp]") {
		t.Fatalf("include_calls should default to off: %q", out)
	}
}

// TestCodeImpactIncludeCallsSkipsUnmatchedTarget guards against an LSP tail on
// a query that didn't match a file: there is no target to ask about.
func TestCodeImpactIncludeCallsSkipsUnmatchedTarget(t *testing.T) {
	out, err := staticImpactTool(emptyIndex(t)).Execute(context.Background(), `{"path":"does-not-exist.go","include_calls":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "[lsp]") {
		t.Fatalf("include_calls should be silent when the target didn't match: %q", out)
	}
}

func TestCodeImpactIncludeCallsDegradesWithoutServer(t *testing.T) {
	idx := emptyIndex(t)
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}, LSP: lspToolBase{Cwd: NewCwdRef(idx.RootPath()), Disabled: map[string]bool{"go": true}}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","include_calls":true}`)
	if err != nil {
		t.Fatalf("a missing language server must not fail the static answer: %v", err)
	}
	if !strings.Contains(out, "direct dependents [import]") || !strings.Contains(out, "calls [lsp]") || !strings.Contains(out, "disabled by config") {
		t.Fatalf("expected static answer plus an LSP-unavailable note:\n%s", out)
	}
}

// fanInIndex builds core.go imported by n files named by their on-disk layout.
func fanInIndex(t *testing.T, root string, n int) *codemap.CodeIndex {
	t.Helper()
	rootID := codemap.NodeID("dir:.")
	core := codemap.NodeID("file:core.go")
	nodes := map[codemap.NodeID]codemap.Node{
		rootID: {ID: rootID, Kind: codemap.NodeDirectory, Name: "repo", Path: root, RelPath: "."},
		core:   {ID: core, Parent: rootID, Kind: codemap.NodeFile, Name: "core.go", Path: filepath.Join(root, "core.go"), RelPath: "core.go"},
	}
	kids := []codemap.NodeID{core}
	var edges []codemap.Edge
	for i := 0; i < n; i++ {
		rel := fmt.Sprintf("pkg%02d/user_with_a_long_name.go", i)
		id := codemap.NodeID("file:" + rel)
		nodes[id] = codemap.Node{ID: id, Parent: rootID, Kind: codemap.NodeFile, Name: filepath.Base(rel), Path: filepath.Join(root, rel), RelPath: rel}
		kids = append(kids, id)
		edges = append(edges, codemap.Edge{From: id, To: core, Kind: codemap.EdgeImports})
	}
	return codemap.NewIndex(root, rootID, nodes, map[codemap.NodeID][]codemap.NodeID{rootID: kids}, edges)
}

// chainIndex builds f(n) imports f(n-1), so f0 has a linear chain of dependents.
func chainIndex(t *testing.T, root string, n int) *codemap.CodeIndex {
	t.Helper()
	rootID := codemap.NodeID("dir:.")
	nodes := map[codemap.NodeID]codemap.Node{rootID: {ID: rootID, Kind: codemap.NodeDirectory, Name: "repo", Path: root, RelPath: "."}}
	var kids []codemap.NodeID
	var edges []codemap.Edge
	for i := 0; i < n; i++ {
		rel := fmt.Sprintf("f%d.go", i)
		id := codemap.NodeID("file:" + rel)
		nodes[id] = codemap.Node{ID: id, Parent: rootID, Kind: codemap.NodeFile, Name: rel, Path: filepath.Join(root, rel), RelPath: rel}
		kids = append(kids, id)
		if i > 0 {
			edges = append(edges, codemap.Edge{From: id, To: codemap.NodeID(fmt.Sprintf("file:f%d.go", i-1)), Kind: codemap.EdgeImports})
		}
	}
	return codemap.NewIndex(root, rootID, nodes, map[codemap.NodeID][]codemap.NodeID{rootID: kids}, edges)
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

func goTestRepoIndex(t *testing.T) *codemap.CodeIndex {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "base/base.go", "package base\nfunc Base() {}\n")
	writeFile(t, root, "base/other.go", "package base\nfunc Other() {}\n")
	writeFile(t, root, "base/base_test.go", "package base\nimport \"testing\"\nfunc TestBase(t *testing.T) {}\n")
	writeFile(t, root, "mid/mid.go", "package mid\nimport \"example.com/m/base\"\nfunc Mid() { base.Base() }\n")
	writeFile(t, root, "mid/mid_test.go", "package mid\nimport \"testing\"\nfunc TestMid(t *testing.T) {}\n")
	writeFile(t, root, "side/side.go", "package side\nimport \"example.com/m/base\"\nfunc Side() { base.Other() }\n")
	writeFile(t, root, "side/side_test.go", "package side\nimport \"testing\"\nfunc TestSide(t *testing.T) {}\n")
	idx, err := codemap.Build(context.Background(), codemap.BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return idx
}

func TestCodeImpactTestsModeDefaultsToPackageClosure(t *testing.T) {
	out, err := staticImpactTool(goTestRepoIndex(t)).Execute(context.Background(), `{"path":"base/base.go","mode":"tests"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"code_impact tests base/base.go", "test directories to run (package level) [import] (3)", "base/ (1 test files)", "mid/ (1 test files)", "side/ (1 test files)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dependents") {
		t.Errorf("tests mode must return only the tests section:\n%s", out)
	}
}

func TestCodeImpactTestsFileScopeIsNarrower(t *testing.T) {
	out, err := staticImpactTool(goTestRepoIndex(t)).Execute(context.Background(), `{"path":"base/base.go","mode":"tests","scope":"file"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "test directories to run (file level, narrow) [import] (2)") || !strings.Contains(out, "base/ ") || !strings.Contains(out, "mid/ ") {
		t.Errorf("file scope should list base and mid:\n%s", out)
	}
	if strings.Contains(out, "side/") {
		t.Errorf("file scope must omit side (it only uses other.go):\n%s", out)
	}
}

func TestCodeImpactImpactModeIncludesTestDirs(t *testing.T) {
	out, err := staticImpactTool(goTestRepoIndex(t)).Execute(context.Background(), `{"path":"base/base.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "test directories to run (package level) [import] (3)") {
		t.Errorf("impact mode should carry the package-level tests section:\n%s", out)
	}
}

func TestCodeImpactTestsHideDeniedFiles(t *testing.T) {
	idx := goTestRepoIndex(t)
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}, DenyReadPaths: []string{filepath.Join(idx.RootPath(), "mid")}}
	out, err := tool.Execute(context.Background(), `{"path":"base/base.go","mode":"tests"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "mid/") {
		t.Errorf("denied directory leaked into test selection:\n%s", out)
	}
}

// TestCodeImpactAmbiguousFragmentListsCandidates guards the silent-guess defect:
// a fragment naming several files must not analyze an arbitrary one.
func TestCodeImpactAmbiguousFragmentListsCandidates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cmd/a/main.go", "package main\n")
	writeFile(t, root, "cmd/b/main.go", "package main\n")
	idx, err := codemap.Build(context.Background(), codemap.BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, err := staticImpactTool(idx).Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("an ambiguous path is an answer, not an error: %v", err)
	}
	for _, want := range []string{`"main.go" matches 2 files`, "cmd/a/main.go", "cmd/b/main.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "direct dependents") {
		t.Errorf("no analysis should run for an ambiguous path:\n%s", out)
	}
}

// TestCodeImpactListsEachFileOnce: a direct dependent must not be repeated under
// the indirect section.
func TestCodeImpactListsEachFileOnce(t *testing.T) {
	out, err := staticImpactTool(chainIndex(t, t.TempDir(), 4)).Execute(context.Background(), `{"path":"f0.go","depth":-1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if n := strings.Count(out, "f1.go"); n != 1 {
		t.Errorf("f1.go (a direct dependent) appears %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "indirect dependents (depth all) [import] (2)") {
		t.Errorf("want f2.go and f3.go as indirect:\n%s", out)
	}
}

func TestSymbolNamePosition(t *testing.T) {
	sym := func(name string, line int) codemap.Node {
		return codemap.Node{Name: name, Symbol: codemap.SymbolInfo{Range: codemap.Range{Start: codemap.Position{Line: line}}}}
	}
	cases := []struct {
		name  string
		lines []string
		sym   codemap.Node
		want  lspci.Position
	}{
		{"go func", []string{"package p", "func Run() {}"}, sym("Run", 1), lspci.Position{Line: 1, Character: 5}},
		{"go method", []string{"func (i *CodeIndex) FindFiles(q string) {}"}, sym("FindFiles", 0), lspci.Position{Line: 0, Character: 20}},
		{"method named like its receiver", []string{"func (r Run) Run() {}"}, sym("Run", 0), lspci.Position{Line: 0, Character: 13}},
		{"python def", []string{"class A:", "    def load(self):"}, sym("load", 1), lspci.Position{Line: 1, Character: 8}},
		{"ts export", []string{"export async function fetchAll<T>() {}"}, sym("fetchAll", 0), lspci.Position{Line: 0, Character: 22}},
		{"not inside a longer identifier", []string{"func RunAll() { Run() }"}, sym("Run", 0), lspci.Position{Line: 0, Character: 16}},
		{"utf16 columns", []string{"// héllo 😀 func Run() {}"}, sym("Run", 0), lspci.Position{Line: 0, Character: 17}},
		{"name not found keeps declaration start", []string{"func Other() {}"}, sym("Run", 0), lspci.Position{Line: 0, Character: 0}},
		{"line out of range", nil, sym("Run", 3), lspci.Position{Line: 3, Character: 0}},
	}
	for _, c := range cases {
		if got := symbolNamePosition(c.lines, c.sym); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestCodeImpactCallHierarchyAsksAtTheIdentifier is the regression for a live
// finding: the index records a symbol's range from the `func` keyword, and a
// language server answers nothing there, so include_calls was always empty.
func TestCodeImpactCallHierarchyAsksAtTheIdentifier(t *testing.T) {
	idx := emptyIndex(t)
	writeFile(t, idx.RootPath(), "main.go", "package main\nfunc Run() {}\n")
	client := &fakeLSPClient{}
	tool := &CodeImpactTool{
		Provider: codemap.StaticProvider{Snapshot: idx},
		LSP:      lspToolBase{Cwd: NewCwdRef(idx.RootPath()), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) { return client, nil }},
	}
	if _, err := tool.Execute(context.Background(), `{"path":"main.go","include_calls":true}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(client.callQueries) != 1 || client.callQueries[0] != (lspci.Position{Line: 1, Character: 5}) {
		t.Fatalf("CallHierarchy asked at %+v, want the identifier at line 1 column 5", client.callQueries)
	}
}

func TestCodeImpactCallLocationsAreWorkspaceRelative(t *testing.T) {
	idx := emptyIndex(t)
	writeFile(t, idx.RootPath(), "main.go", "package main\nfunc Run() {}\n")
	abs := filepath.Join(idx.RootPath(), "pkg", "caller.go")
	tool := &CodeImpactTool{
		Provider: codemap.StaticProvider{Snapshot: idx},
		LSP: lspToolBase{Cwd: NewCwdRef(idx.RootPath()), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{calls: []lspci.CallHierarchyItem{{Name: "caller", Kind: "function", Direction: "incoming", Location: lspci.Location{Path: abs, Line: 3, Character: 2}}}}, nil
		}},
	}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","include_calls":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, idx.RootPath()) || !strings.Contains(out, "pkg/caller.go") {
		t.Fatalf("call location should be workspace-relative, got:\n%s", out)
	}
}

// TestCodeImpactAmbiguityDoesNotLeakDeniedNames: a denied sibling must neither
// be listed as a candidate nor make an otherwise unique path look ambiguous.
func TestCodeImpactAmbiguityDoesNotLeakDeniedNames(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cmd/a/main.go", "package main\n")
	writeFile(t, root, "cmd/b/main.go", "package main\n")
	writeFile(t, root, "secret/main.go", "package main\n")
	idx, err := codemap.Build(context.Background(), codemap.BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	tool := &CodeImpactTool{Provider: codemap.StaticProvider{Snapshot: idx}, DenyReadPaths: []string{filepath.Join(root, "secret")}}

	out, err := tool.Execute(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("denied path leaked into the candidate list:\n%s", out)
	}
	if !strings.Contains(out, "matches 2 files") {
		t.Fatalf("denied files must not count toward ambiguity:\n%s", out)
	}

	// With the other candidate gone, the one visible file is unambiguous.
	only, err := tool.Execute(context.Background(), `{"path":"a/main.go"}`)
	if err != nil || !strings.Contains(only, "code_impact impact cmd/a/main.go") {
		t.Fatalf("unique visible file should resolve: %v\n%s", err, only)
	}
	if _, err := tool.Execute(context.Background(), `{"path":"secret/main.go"}`); err == nil {
		t.Fatal("a path that only matches denied files must be refused")
	}
}

func TestCodeImpactCallsOmitDeniedFiles(t *testing.T) {
	idx := emptyIndex(t)
	writeFile(t, idx.RootPath(), "main.go", "package main\nfunc Run() {}\n")
	secret := filepath.Join(idx.RootPath(), "secret", "hidden.go")
	ok := filepath.Join(idx.RootPath(), "pkg", "visible.go")
	tool := &CodeImpactTool{
		Provider:      codemap.StaticProvider{Snapshot: idx},
		DenyReadPaths: []string{filepath.Join(idx.RootPath(), "secret")},
		LSP: lspToolBase{Cwd: NewCwdRef(idx.RootPath()), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{calls: []lspci.CallHierarchyItem{
				{Name: "leaky", Kind: "function", Direction: "incoming", Location: lspci.Location{Path: secret, Line: 1}},
				{Name: "fine", Kind: "function", Direction: "incoming", Location: lspci.Location{Path: ok, Line: 1}},
			}}, nil
		}},
	}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","include_calls":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "hidden.go") || strings.Contains(out, "leaky") || !strings.Contains(out, "visible.go") {
		t.Fatalf("LSP call results must skip denied files:\n%s", out)
	}
}
