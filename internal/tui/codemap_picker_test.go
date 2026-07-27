package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/codemap"
)

func TestMapCommandRegisteredAsSingleSlash(t *testing.T) {
	if findSlash("map") == nil {
		t.Fatal("/map should be registered")
	}
	if findSlash("structure") != nil || findSlash("outline") != nil {
		t.Fatal("code-map features should have one slash command: /map")
	}
}

func TestRenderCodeMapPicker(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeStructure, expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	out := stripANSI(renderCodeMapPicker(p, 100))
	if !strings.Contains(out, "Code map") || !strings.Contains(out, "main.go") || !strings.Contains(out, "file · go") {
		t.Fatalf("unexpected render: %q", out)
	}
}

func TestCodeMapPickerFilter(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeStructure, expanded: map[codemap.NodeID]bool{idx.Root(): true}, filter: "Run"}
	p.rebuildRows()
	if len(p.rows) != 1 {
		t.Fatalf("filtered rows = %d, want 1", len(p.rows))
	}
	n, _ := idx.Node(p.rows[0].id)
	if n.Name != "Run" {
		t.Fatalf("filtered row = %+v", n)
	}
}

func TestCodeMapPickerDependencyMode(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeDependencies, filter: "main.go", expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	if len(p.rows) != 1 {
		t.Fatalf("impact rows = %+v, want one depends-on row", p.rows)
	}
	n, _ := idx.Node(p.rows[0].id)
	if n.RelPath != "dep.go" {
		t.Fatalf("impact target = %+v, want dep.go", n)
	}
	out := stripANSI(renderCodeMapPicker(p, 100))
	if !strings.Contains(out, "dep.go") {
		t.Fatalf("unexpected impact render: %q", out)
	}
}

func TestCodeMapPickerHereModeShowsChangedNeighborhood(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeHere, hereFiles: []string{"main.go"}, expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	out := stripANSI(renderCodeMapPicker(p, 120))
	if !strings.Contains(out, "changed") || !strings.Contains(out, "imports") || !strings.Contains(out, "Run") {
		t.Fatalf("unexpected here render: %q", out)
	}
}

func TestCodeMapEnterInsertsFileRef(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeStructure, expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	for i, row := range p.rows {
		n, _ := idx.Node(row.id)
		if n.Kind == codemap.NodeFile && n.RelPath == "main.go" {
			p.cursor = i
			break
		}
	}
	m := newTestModel(t)
	m.codeMapPickerOpen = true
	m.codeMapPicker = p
	m.textInput.SetValue("explain")
	m = m.acceptCodeMapSelection()
	if got := m.textInput.Value(); got != "explain @main.go " {
		t.Fatalf("input = %q, want file ref", got)
	}
	if m.codeMapPickerOpen || m.codeMapPicker != nil {
		t.Fatal("picker should close after inserting a file ref")
	}
}

func TestOpenCodeMapPickerRequiresProvider(t *testing.T) {
	m := Model{transcript: &strings.Builder{}}
	out, cmd := cmdMap(m, nil)
	if cmd != nil {
		t.Fatal("missing provider should not start load command")
	}
	mm := out
	if mm.codeMapPickerOpen {
		t.Fatal("picker should not open without provider")
	}
}

func TestOpenCodeMapPickerLoadsProvider(t *testing.T) {
	idx := testCodeMapIndex(t)
	m := Model{codeMapProvider: codemap.StaticProvider{Snapshot: idx}}
	out, cmd := cmdMap(m, []string{"Run"})
	if cmd == nil {
		t.Fatal("expected load command")
	}
	mm := out
	if !mm.codeMapPickerOpen || mm.codeMapPicker == nil || !mm.codeMapPicker.loading {
		t.Fatalf("picker should open loading: %+v", mm.codeMapPicker)
	}
	msg := cmd().(codeMapLoadedMsg)
	if msg.err != nil || msg.idx == nil || msg.filter != "Run" || msg.mode != codeMapModeStructure {
		t.Fatalf("unexpected load msg: %+v", msg)
	}
	loaded, _ := mm.handleCodeMapLoaded(msg)
	lm := loaded
	if lm.codeMapPicker.loading || len(lm.codeMapPicker.rows) != 1 {
		t.Fatalf("picker should show filtered result: %+v", lm.codeMapPicker)
	}
	_ = context.Background()
}

func TestCodeMapPickerDiagramMode(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeDiagram, filter: "main.go", expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	out := stripANSI(renderCodeMapPicker(p, 120))
	if !strings.Contains(out, "```mermaid") || !strings.Contains(out, "main.go") || !strings.Contains(out, "-->") {
		t.Fatalf("unexpected diagram render: %q", out)
	}
}

func TestMapCommandParsesImpactDepthAndCycles(t *testing.T) {
	idx := testCodeMapIndex(t)
	m := Model{codeMapProvider: codemap.StaticProvider{Snapshot: idx}}
	out, cmd := cmdMap(m, []string{"impact", "--depth", "2", "main.go"})
	if cmd == nil {
		t.Fatal("expected impact load command")
	}
	msg := cmd().(codeMapLoadedMsg)
	if msg.mode != codeMapModeImpact || msg.filter != "main.go" || msg.depth != 2 {
		t.Fatalf("impact msg = %+v, want mode impact filter main.go depth 2", msg)
	}
	out, cmd = cmdMap(out, []string{"cycles"})
	if cmd == nil {
		t.Fatal("expected cycles load command")
	}
	msg = cmd().(codeMapLoadedMsg)
	if msg.mode != codeMapModeCycles || msg.filter != "" {
		t.Fatalf("cycles msg = %+v", msg)
	}
	_, cmd = cmdMap(out, []string{"diagram", "main.go"})
	if cmd == nil {
		t.Fatal("expected diagram load command")
	}
	msg = cmd().(codeMapLoadedMsg)
	if msg.mode != codeMapModeDiagram || msg.filter != "main.go" {
		t.Fatalf("diagram msg = %+v", msg)
	}
	_, cmd = cmdMap(out, []string{"here", "internal/tui"})
	if cmd == nil {
		t.Fatal("expected here load command")
	}
	msg = cmd().(codeMapLoadedMsg)
	if msg.mode != codeMapModeHere || msg.filter != "internal/tui" {
		t.Fatalf("here msg = %+v", msg)
	}
}

func testCodeMapIndex(t *testing.T) *codemap.CodeIndex {
	t.Helper()
	root := t.TempDir()
	rootID := codemap.NodeID("dir:.")
	fileID := codemap.NodeID("file:main.go")
	depID := codemap.NodeID("file:dep.go")
	symID := codemap.NodeID("sym:main.go:1:0:Run:0")
	nodes := map[codemap.NodeID]codemap.Node{
		rootID: {ID: rootID, Kind: codemap.NodeDirectory, Name: "repo", Path: root, RelPath: ".", Stats: codemap.Stats{Files: 2, Symbols: 1, LOC: 3, Exported: 1}},
		fileID: {ID: fileID, Parent: rootID, Kind: codemap.NodeFile, Name: "main.go", Path: root + "/main.go", RelPath: "main.go", Language: "go", Stats: codemap.Stats{Files: 1, Symbols: 1, LOC: 2, Exported: 1}},
		depID:  {ID: depID, Parent: rootID, Kind: codemap.NodeFile, Name: "dep.go", Path: root + "/dep.go", RelPath: "dep.go", Language: "go", Stats: codemap.Stats{Files: 1, LOC: 1}},
		symID:  {ID: symID, Parent: fileID, Kind: codemap.NodeSymbol, Name: "Run", Path: root + "/main.go", RelPath: "main.go", Symbol: codemap.SymbolInfo{Kind: "function", Exported: true, Range: codemap.Range{Start: codemap.Position{Line: 1}}}, Stats: codemap.Stats{Symbols: 1, Exported: 1}},
	}
	children := map[codemap.NodeID][]codemap.NodeID{rootID: {fileID, depID}, fileID: {symID}}
	edges := []codemap.Edge{{From: fileID, To: depID, Kind: codemap.EdgeImports, Meta: "dep"}, {From: depID, To: fileID, Kind: codemap.EdgeImports, Meta: "main"}}
	return codemap.NewIndex(root, rootID, nodes, children, edges)
}
