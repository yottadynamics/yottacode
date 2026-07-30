package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/codemap"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

func TestLSPImpactCombinesSemanticAndCodeMapSections(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main\nfunc target() {}\n")
	rootID := codemap.NodeID("dir:.")
	mainID := codemap.NodeID("file:main.go")
	callerID := codemap.NodeID("file:caller.go")
	nodes := map[codemap.NodeID]codemap.Node{
		rootID:   {ID: rootID, Kind: codemap.NodeDirectory, RelPath: "."},
		mainID:   {ID: mainID, Parent: rootID, Kind: codemap.NodeFile, Name: "main.go", RelPath: "main.go"},
		callerID: {ID: callerID, Parent: rootID, Kind: codemap.NodeFile, Name: "caller.go", RelPath: "caller.go"},
	}
	idx := codemap.NewIndex(tmp, rootID, nodes, map[codemap.NodeID][]codemap.NodeID{rootID: {mainID, callerID}}, []codemap.Edge{{From: callerID, To: mainID, Kind: codemap.EdgeImports}})
	tool := &LSPImpactTool{
		lspToolBase: lspToolBase{Cwd: NewCwdRef(tmp), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{
				locs:        []lspci.Location{{Path: "main.go", Line: 1, Character: 5}, {Path: "caller.go", Line: 3, Character: 2}},
				calls:       []lspci.CallHierarchyItem{{Name: "caller", Kind: "function", Direction: "incoming", Location: lspci.Location{Path: "caller.go", Line: 3, Character: 2}}},
				diagnostics: []lspci.Diagnostic{{Path: "main.go", Line: 1, Character: 0, Severity: "warning", Source: "test", Message: "check me"}},
			}, nil
		}},
		CodeMapProvider: codemap.StaticProvider{Snapshot: idx},
	}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":1,"character":5,"max_results":1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"impact\t", "hover", "hover text", "definitions", "references", "calls", "diagnostics", "check me", "code_map_import_impact", "caller.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("impact output missing %q: %q", want, out)
		}
	}
	if !strings.Contains(out, "…[truncated at 1 results]") {
		t.Fatalf("impact output should cap sections: %q", out)
	}
}

func TestLSPImpactWorksWithoutCodeMapProvider(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main\nfunc target() {}\n")
	tool := &LSPImpactTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(tmp), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
		return &fakeLSPClient{locs: []lspci.Location{{Path: "main.go", Line: 1, Character: 5}}, diagnostics: []lspci.Diagnostic{}}, nil
	}}}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":1,"character":5}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "code_map_import_impact\n  unavailable: code map is not enabled") {
		t.Fatalf("expected code map unavailable section, got %q", out)
	}
}

func TestLSPImpactNilCodeMapIndexReportsConcreteReason(t *testing.T) {
	var b strings.Builder
	writeImpactCodeMap(context.Background(), &b, codemap.StaticProvider{}, "main.go", 10)
	if strings.Contains(b.String(), "<nil>") {
		t.Fatalf("nil index output should not expose <nil>: %q", b.String())
	}
	if !strings.Contains(b.String(), "code map index is unavailable") {
		t.Fatalf("nil index output should include concrete reason: %q", b.String())
	}
}

func TestTruncateImpactTextPreservesUTF8(t *testing.T) {
	in := "abc😀def"
	out := truncateImpactText(in, len("abc")+1)
	if out != "abc…" {
		t.Fatalf("truncateImpactText split rune: %q", out)
	}
}

func TestLSPImpactUnsupportedFileReturnsUnavailable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "README.md", "# hi")
	tool := &LSPImpactTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"README.md","line":0,"character":0}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "unavailable: no supported LSP language") {
		t.Fatalf("expected unsupported file unavailable result, got %q", out)
	}
}
