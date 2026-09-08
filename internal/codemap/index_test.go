package codemap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

func TestCachedProviderReusesSnapshotUntilFilesChange(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	source := &countingSource{}
	provider := &CachedProvider{Options: BuildOptions{Root: root, Source: source}}
	first, err := provider.Index(context.Background())
	if err != nil {
		t.Fatalf("first Index: %v", err)
	}
	second, err := provider.Index(context.Background())
	if err != nil {
		t.Fatalf("second Index: %v", err)
	}
	if first != second {
		t.Fatal("unchanged workspace should reuse the cached snapshot")
	}
	if source.calls != 1 {
		t.Fatalf("source calls after cached read = %d, want 1", source.calls)
	}
	write(t, root, "a.go", "package main\nfunc A() {}\nfunc B() {}\n")
	third, err := provider.Index(context.Background())
	if err != nil {
		t.Fatalf("third Index: %v", err)
	}
	if third == second {
		t.Fatal("changed workspace should rebuild the snapshot")
	}
	if source.calls != 2 {
		t.Fatalf("source calls after rebuild = %d, want 2", source.calls)
	}
}

type countingSource struct{ calls int }

func (s *countingSource) Symbols(context.Context, string) ([]lsp.Symbol, string, error) {
	s.calls++
	return nil, "counting", nil
}

func TestMermaidDiagramFocusesImportEdges(t *testing.T) {
	root := t.TempDir()
	rootID := NodeID("dir:.")
	a := NodeID("file:a.go")
	b := NodeID("file:b.go")
	nodes := map[NodeID]Node{
		rootID: {ID: rootID, Kind: NodeDirectory, RelPath: "."},
		a:      {ID: a, Parent: rootID, Kind: NodeFile, Name: "a.go", RelPath: "a.go"},
		b:      {ID: b, Parent: rootID, Kind: NodeFile, Name: "b.go", RelPath: "b.go"},
	}
	idx := NewIndex(root, rootID, nodes, map[NodeID][]NodeID{rootID: {a, b}}, []Edge{{From: a, To: b, Kind: EdgeImports}})
	diagram := MermaidDiagram(idx, "a.go", 10)
	if !strings.Contains(diagram, "```mermaid") || !strings.Contains(diagram, "a.go") || !strings.Contains(diagram, "-->") {
		t.Fatalf("unexpected diagram: %q", diagram)
	}
}

func TestBuildFallbackIndex(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nconst Version = \"1\"\n\ntype Server struct{}\n\nfunc NewServer() *Server { return &Server{} }\nfunc helper() {}\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc Use() {}\n")
	write(t, root, "internal/alt/lib/lib.go", "package lib\n\nfunc Other() {}\n")
	write(t, root, ".git/ignored.go", "package ignored\nfunc Ignored() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if idx.Count() == 0 {
		t.Fatal("index should contain nodes")
	}
	file, ok := findNode(idx, NodeFile, "internal/app/app.go", "app.go")
	if !ok {
		t.Fatalf("file node missing: %+v", idx.Nodes())
	}
	if file.Stats.LOC == 0 || file.Stats.Symbols != 4 || file.Stats.Exported != 3 || file.Stats.Private != 1 {
		t.Fatalf("unexpected file stats: %+v", file.Stats)
	}
	syms := idx.SymbolsForFile("internal/app/app.go")
	if len(syms) != 4 {
		t.Fatalf("symbols = %d, want 4: %+v", len(syms), syms)
	}
	matches := idx.Filter("NewServer", 10)
	if len(matches) != 1 || matches[0].Name != "NewServer" {
		t.Fatalf("filter NewServer = %+v", matches)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("dependencies = %+v, want internal/lib/lib.go", deps)
	}
	dependents := idx.Dependents("internal/lib/lib.go", 10)
	if len(dependents) != 1 || dependents[0].RelPath != "internal/app/app.go" {
		t.Fatalf("dependents = %+v, want internal/app/app.go", dependents)
	}
	for _, n := range idx.Nodes() {
		if strings.Contains(n.RelPath, ".git") {
			t.Fatalf("skipped .git path was indexed: %+v", n)
		}
	}
}

func TestBuildCapsScannedFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	write(t, root, "b.go", "package main\nfunc B() {}\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root, MaxFiles: 1})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	files := 0
	for _, n := range idx.Nodes() {
		if n.Kind == NodeFile {
			files++
		}
	}
	if files != 1 {
		t.Fatalf("indexed files = %d, want 1", files)
	}
}

func TestImpactIncludesTransitiveDependentsAndCycles(t *testing.T) {
	root := t.TempDir()
	rootID := NodeID("dir:.")
	a := NodeID("file:a.go")
	b := NodeID("file:b.go")
	c := NodeID("file:c.go")
	d := NodeID("file:d.go")
	nodes := map[NodeID]Node{
		rootID: {ID: rootID, Kind: NodeDirectory, RelPath: "."},
		a:      {ID: a, Parent: rootID, Kind: NodeFile, Name: "a.go", RelPath: "a.go"},
		b:      {ID: b, Parent: rootID, Kind: NodeFile, Name: "b.go", RelPath: "b.go"},
		c:      {ID: c, Parent: rootID, Kind: NodeFile, Name: "c.go", RelPath: "c.go"},
		d:      {ID: d, Parent: rootID, Kind: NodeFile, Name: "d.go", RelPath: "d.go"},
	}
	children := map[NodeID][]NodeID{rootID: {a, b, c, d}}
	edges := []Edge{{From: b, To: a, Kind: EdgeImports}, {From: c, To: b, Kind: EdgeImports}, {From: a, To: d, Kind: EdgeImports}, {From: d, To: a, Kind: EdgeImports}}
	idx := NewIndex(root, rootID, nodes, children, edges)

	impact := idx.Impact("a.go", MaxDepthAll, 10)
	if impact.Target.RelPath != "a.go" {
		t.Fatalf("target = %+v, want a.go", impact.Target)
	}
	if got := relPaths(impact.DirectDependents); strings.Join(got, ",") != "b.go,d.go" {
		t.Fatalf("direct dependents = %v, want b.go,d.go", got)
	}
	if got := relPaths(impact.TransitiveDependents); strings.Join(got, ",") != "b.go,c.go,d.go" {
		t.Fatalf("transitive dependents = %v, want b.go,c.go,d.go", got)
	}
	if len(impact.Cycles) != 1 || strings.Join(relPaths(impact.Cycles[0]), ",") != "a.go,d.go" {
		t.Fatalf("cycles = %+v, want a.go,d.go", impact.Cycles)
	}
}

func TestSuggestedContextRanksRelatedFiles(t *testing.T) {
	root := t.TempDir()
	rootID := NodeID("dir:.")
	files := []string{"internal/app/app.go", "internal/app/app_test.go", "internal/lib/lib.go", "internal/use/use.go"}
	nodes := map[NodeID]Node{rootID: {ID: rootID, Kind: NodeDirectory, RelPath: "."}}
	children := map[NodeID][]NodeID{rootID: {}}
	for _, rel := range files {
		id := NodeID("file:" + rel)
		nodes[id] = Node{ID: id, Parent: rootID, Kind: NodeFile, Name: filepath.Base(rel), RelPath: rel}
		children[rootID] = append(children[rootID], id)
	}
	edges := []Edge{
		{From: "file:internal/app/app.go", To: "file:internal/lib/lib.go", Kind: EdgeImports},
		{From: "file:internal/use/use.go", To: "file:internal/app/app.go", Kind: EdgeImports},
	}
	idx := NewIndex(root, rootID, nodes, children, edges)

	got := idx.SuggestedContext([]string{"internal/app/app.go"}, 8)
	var rows []string
	for _, suggestion := range got {
		if suggestion.File.Kind != NodeFile {
			t.Fatalf("suggestion is not a file: %+v", suggestion)
		}
		rows = append(rows, suggestion.File.RelPath+":"+suggestion.Reason)
	}
	want := []string{
		"internal/app/app.go:changed",
		"internal/lib/lib.go:imported by target",
		"internal/use/use.go:imports target",
		"internal/app/app_test.go:test for target",
	}
	if strings.Join(rows, "|") != strings.Join(want, "|") {
		t.Fatalf("suggestions = %v, want %v", rows, want)
	}

	reversed := idx.SuggestedContext([]string{"internal/app", "internal/app/app.go"}, 3)
	if len(reversed) != 3 || reversed[0].File.RelPath != "internal/app/app.go" || reversed[1].File.RelPath != "internal/app/app_test.go" || reversed[2].File.RelPath != "internal/lib/lib.go" {
		t.Fatalf("directory suggestions = %+v", reversed)
	}
	if got := idx.SuggestedContext([]string{filepath.Join(root, "internal/app/app.go")}, 1); len(got) != 1 || got[0].File.RelPath != "internal/app/app.go" {
		t.Fatalf("absolute target suggestions = %+v", got)
	}
	if got := idx.SuggestedContext([]string{"app.go"}, 8); len(got) != 0 {
		t.Fatalf("non-exact target suggestions = %+v, want none", got)
	}
	deduped := idx.SuggestedContext([]string{"internal/app/app.go", "internal/lib/lib.go"}, 8)
	if len(deduped) < 2 || deduped[0].File.RelPath != "internal/app/app.go" || deduped[1].File.RelPath != "internal/lib/lib.go" || deduped[1].Reason != "changed" {
		t.Fatalf("deduplicated suggestions = %+v, want both changed files first", deduped)
	}
}

func TestSuggestedContextTestConventionsAndBounds(t *testing.T) {
	rootID := NodeID("dir:.")
	files := []string{"web/foo.ts", "web/foo.test.ts", "web/bar.ts", "web/bar.spec.ts", "tiny.go", "with space.go", "many/a.go", "many/b.go", "many/c.go", "many/d.go", "many/e.go", "many/f.go", "many/g.go", "many/h.go", "many/i.go"}
	nodes := map[NodeID]Node{rootID: {ID: rootID, Kind: NodeDirectory, RelPath: "."}}
	children := map[NodeID][]NodeID{rootID: {}}
	for _, rel := range files {
		id := NodeID("file:" + rel)
		nodes[id] = Node{ID: id, Parent: rootID, Kind: NodeFile, Name: filepath.Base(rel), RelPath: rel}
		children[rootID] = append(children[rootID], id)
	}
	idx := NewIndex(t.TempDir(), rootID, nodes, children)

	for _, tc := range []struct {
		target string
		want   string
	}{{"web/foo.ts", "web/foo.test.ts"}, {"web/bar.ts", "web/bar.spec.ts"}} {
		t.Run(tc.target, func(t *testing.T) {
			got := idx.SuggestedContext([]string{tc.target}, 8)
			if len(got) != 2 || got[1].File.RelPath != tc.want || got[1].Reason != "test for target" {
				t.Fatalf("suggestions = %+v, want paired test %s", got, tc.want)
			}
		})
	}
	if got := idx.SuggestedContext([]string{"tiny.go"}, 99); len(got) != 1 {
		t.Fatalf("sparse suggestions = %d, want 1", len(got))
	}
	if got := idx.SuggestedContext([]string{"with space.go"}, 8); len(got) != 0 {
		t.Fatalf("whitespace path suggestions = %+v, want none", got)
	}
	if got := idx.SuggestedContext([]string{"web"}, 2); len(got) != 2 {
		t.Fatalf("bounded suggestions = %d, want 2", len(got))
	}
	if got := idx.SuggestedContext([]string{"many"}, 0); len(got) != 8 {
		t.Fatalf("default suggestions = %d, want 8", len(got))
	}
	if got := idx.SuggestedContext([]string{"many"}, 99); len(got) != 8 {
		t.Fatalf("clamped suggestions = %d, want 8", len(got))
	}
}

func relPaths(nodes []Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.RelPath)
	}
	return out
}

func findNode(idx *CodeIndex, kind NodeKind, rel, name string) (Node, bool) {
	for _, n := range idx.Nodes() {
		if n.Kind == kind && n.RelPath == rel && n.Name == name {
			return n, true
		}
	}
	return Node{}, false
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
