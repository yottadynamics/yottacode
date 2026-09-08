package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/codemap"
	"github.com/yottadynamics/yottacode/internal/filerefs"
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

// A bracketed paste while the code-map picker is open must land in
// the filter buffer, not leak through to the hidden main chat
// textarea underneath the popup. Regression test for the bubbletea v2
// migration: pastes arrive as tea.PasteMsg, distinct from the
// msg.Text accumulation updateCodeMapPicker uses for typed runes.
func TestCodeMapPickerFilterAcceptsPaste(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeStructure, expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	m := newTestModel(t)
	m.codeMapPickerOpen = true
	m.codeMapPicker = p

	m, _ = applyMsg(m, tea.PasteMsg{Content: "Run"})
	if m.codeMapPicker.filter != "Run" {
		t.Fatalf("paste should fill the filter buffer; got %q", m.codeMapPicker.filter)
	}
	if got := m.textInput.Value(); got != "" {
		t.Errorf("paste should not leak into the main chat textarea; got %q", got)
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

// Here mode is intentionally a compact file-only list. SuggestedContext owns
// ranking and reasons; the picker must preserve both while advertising the
// bulk action and the command that opens this view.
func TestCodeMapPickerHereModeRendersSuggestedFilesAndReasons(t *testing.T) {
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeHere, hereFiles: []string{"main.go"}, expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	out := stripANSI(renderCodeMapPicker(p, 120))
	if !strings.Contains(out, "Suggested context") || !strings.Contains(out, "main.go") || !strings.Contains(out, "changed") || !strings.Contains(out, "dep.go") || !strings.Contains(out, "imported by target") {
		t.Fatalf("unexpected here render: %q", out)
	}
	if strings.Contains(out, "Run") || len(p.rows) > 8 || len(p.suggestions) != len(p.rows) {
		t.Fatalf("here mode should render at most eight file suggestions: rows=%+v output=%q", p.rows, out)
	}
	if !strings.Contains(out, "a attach all") || !strings.Contains(out, "/map here") {
		t.Fatalf("here mode should advertise attach-all and /map here: %q", out)
	}
}

// The picker requests the UI cap explicitly, even when the changed target is a
// directory with enough files to exceed it.
func TestCodeMapPickerHereSuggestionsAreBoundedToEight(t *testing.T) {
	rootID := codemap.NodeID("dir:.")
	nodes := map[codemap.NodeID]codemap.Node{rootID: {ID: rootID, Kind: codemap.NodeDirectory, RelPath: "."}}
	children := map[codemap.NodeID][]codemap.NodeID{rootID: {}}
	for i := 0; i < 10; i++ {
		rel := fmt.Sprintf("many/%02d.go", i)
		id := codemap.NodeID("file:" + rel)
		nodes[id] = codemap.Node{ID: id, Parent: rootID, Kind: codemap.NodeFile, Name: fmt.Sprintf("%02d.go", i), RelPath: rel}
		children[rootID] = append(children[rootID], id)
	}
	idx := codemap.NewIndex(t.TempDir(), rootID, nodes, children)
	p := &codeMapPickerState{index: idx, mode: codeMapModeHere, hereFiles: []string{"many"}}

	p.rebuildRows()
	if len(p.suggestions) != 8 || len(p.rows) != 8 {
		t.Fatalf("suggestions=%d rows=%d, want eight", len(p.suggestions), len(p.rows))
	}
	for _, suggestion := range p.suggestions {
		if suggestion.File.Kind != codemap.NodeFile || suggestion.Reason == "" {
			t.Fatalf("invalid file suggestion: %+v", suggestion)
		}
	}
}

// Attach-all keeps the rank order returned by SuggestedContext, ignores refs
// already present anywhere in the textarea, and leaves syntactically valid
// @refs separated by normalized whitespace.
func TestCodeMapPickerHereAttachAllOrderDedupeWhitespaceAndParseability(t *testing.T) {
	suggestions := []codemap.ContextSuggestion{
		{File: codemap.Node{Kind: codemap.NodeFile, RelPath: "first.go"}, Reason: "changed"},
		{File: codemap.Node{Kind: codemap.NodeFile, RelPath: "existing.go"}, Reason: "imported by target"},
		{File: codemap.Node{Kind: codemap.NodeFile, RelPath: "last.go"}, Reason: "imports target"},
	}
	m := newTestModel(t)
	m.codeMapPickerOpen = true
	m.codeMapPicker = &codeMapPickerState{mode: codeMapModeHere, suggestions: suggestions}
	m.textInput.SetValue("explain @existing.go   \n\t")

	m, _ = m.updateCodeMapPicker(tea.KeyPressMsg{Text: "a"})
	if got, want := m.textInput.Value(), "explain @existing.go @first.go @last.go "; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	refs := filerefs.Parse(m.textInput.Value())
	if len(refs) != 3 || refs[0].Path != "existing.go" || refs[1].Path != "first.go" || refs[2].Path != "last.go" {
		t.Fatalf("attached refs are not parseable or ordered: %+v", refs)
	}
	if m.codeMapPickerOpen || m.codeMapPicker != nil {
		t.Fatal("picker should close after attach-all")
	}
}

// Duplicate suggestions are also suppressed during insertion, even though the
// index normally guarantees uniqueness itself.
func TestCodeMapPickerHereAttachAllDedupesSuggestions(t *testing.T) {
	n := codemap.Node{Kind: codemap.NodeFile, RelPath: "same.go"}
	m := newTestModel(t)
	m.codeMapPickerOpen = true
	m.codeMapPicker = &codeMapPickerState{mode: codeMapModeHere, suggestions: []codemap.ContextSuggestion{{File: n}, {File: n}}}

	m, _ = m.updateCodeMapPicker(tea.KeyPressMsg{Text: "a"})
	if got := m.textInput.Value(); got != "@same.go " {
		t.Fatalf("input = %q, want one deduplicated ref", got)
	}
}

func TestCodeMapPickerHereAttachAllSkipsPathsWithWhitespace(t *testing.T) {
	m := newTestModel(t)
	m.codeMapPickerOpen = true
	m.codeMapPicker = &codeMapPickerState{mode: codeMapModeHere, suggestions: []codemap.ContextSuggestion{
		{File: codemap.Node{Kind: codemap.NodeFile, RelPath: "valid.go"}},
		{File: codemap.Node{Kind: codemap.NodeFile, RelPath: "with space.go"}},
		{File: codemap.Node{Kind: codemap.NodeFile, RelPath: "with\ttab.go"}},
	}}

	m, _ = m.updateCodeMapPicker(tea.KeyPressMsg{Text: "a"})
	if got := m.textInput.Value(); got != "@valid.go " {
		t.Fatalf("input = %q, want only attachable path", got)
	}
}

// An empty recommendation set gives feedback but remains open so the user can
// rebuild or leave normally rather than having a no-op key dismiss the picker.
func TestCodeMapPickerHereAttachAllEmptyRemainsOpen(t *testing.T) {
	m := newTestModel(t)
	m.codeMapPickerOpen = true
	m.codeMapPicker = &codeMapPickerState{mode: codeMapModeHere}

	m, _ = m.updateCodeMapPicker(tea.KeyPressMsg{Text: "a"})
	if !m.codeMapPickerOpen || m.codeMapPicker == nil {
		t.Fatal("empty attach-all should leave picker open")
	}
	if !strings.Contains(m.codeMapPicker.status, "no suggested context") {
		t.Fatalf("status = %q, want no-suggestions feedback", m.codeMapPicker.status)
	}
}

// Outside Here mode, "a" remains ordinary filter input and must not attach
// refs or close the picker.
func TestCodeMapPickerAttachAllKeyFiltersOutsideHereMode(t *testing.T) {
	idx := testCodeMapIndex(t)
	m := newTestModel(t)
	m.codeMapPickerOpen = true
	m.codeMapPicker = &codeMapPickerState{index: idx, mode: codeMapModeStructure, expanded: map[codemap.NodeID]bool{idx.Root(): true}}

	m, _ = m.updateCodeMapPicker(tea.KeyPressMsg{Text: "a"})
	if !m.codeMapPickerOpen || m.codeMapPicker.filter != "a" {
		t.Fatalf("non-Here 'a' should filter and remain open: %+v", m.codeMapPicker)
	}
	if got := m.textInput.Value(); got != "" {
		t.Fatalf("non-Here 'a' unexpectedly changed textarea: %q", got)
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
