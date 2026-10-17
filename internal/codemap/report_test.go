package codemap

import (
	"fmt"
	"strings"
	"testing"
)

func reportNodes(n int) []Node {
	out := make([]Node, n)
	for i := range out {
		out[i] = Node{ID: NodeID(fmt.Sprintf("file:f%02d.go", i)), Kind: NodeFile, RelPath: fmt.Sprintf("f%02d.go", i)}
	}
	return out
}

func TestReportRenderLabelsProvenanceAndCounts(t *testing.T) {
	r := Report{
		Mode: "impact", Target: "a.go",
		Info: IndexInfo{Files: 3, Version: "deadbeef"},
		Sections: []ReportSection{
			{Title: "direct dependents", Provenance: ProvImport, Nodes: reportNodes(2)},
			{Title: "likely tests", Provenance: ProvHeuristic},
		},
	}
	got := r.Render(0)
	for _, want := range []string{
		"code_impact impact a.go",
		"index: 3 files, version deadbeef",
		"direct dependents [import] (2)",
		"likely tests [heuristic] (0)",
		"  (none)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "PARTIAL") {
		t.Errorf("complete index rendered a PARTIAL notice:\n%s", got)
	}
}

func TestReportRenderStatesPartialIndex(t *testing.T) {
	r := Report{Mode: "impact", Target: "a.go", Info: IndexInfo{Files: 2000, MaxFiles: 2000, Capped: true, Version: "x"}}
	if got := r.Render(0); !strings.Contains(got, "PARTIAL") {
		t.Errorf("capped index must be flagged, got:\n%s", got)
	}
}

func TestReportRenderBudgetOmitsItemsButKeepsTruthfulCounts(t *testing.T) {
	r := Report{
		Mode: "dependents", Target: "a.go",
		Sections: []ReportSection{
			{Title: "direct dependents", Provenance: ProvImport, Nodes: reportNodes(30)},
			{Title: "transitive dependents", Provenance: ProvImport, Nodes: reportNodes(5)},
		},
	}
	got := r.Render(200)
	if len(got) > 400 {
		t.Errorf("output %d bytes is far over the 200-byte budget:\n%s", len(got), got)
	}
	for _, want := range []string{"direct dependents [import] (30)", "transitive dependents [import] (5)", "more omitted: output budget"} {
		if !strings.Contains(got, want) {
			t.Errorf("budgeted Render missing %q in:\n%s", want, got)
		}
	}
}

func TestReportRenderIsDeterministic(t *testing.T) {
	r := Report{Mode: "impact", Target: "a.go", Sections: []ReportSection{{Title: "t", Provenance: ProvImport, Nodes: reportNodes(10)}}}
	if r.Render(100) != r.Render(100) {
		t.Error("Render is not deterministic")
	}
}

func TestReportRenderAtLeastAndCycles(t *testing.T) {
	a, b := Node{RelPath: "a.go"}, Node{RelPath: "b.go"}
	r := Report{Mode: "cycles", Target: "a.go", Sections: []ReportSection{
		{Title: "cycles", Provenance: ProvImport, Cycles: [][]Node{{a, b}}, AtLeast: true},
	}}
	got := r.Render(0)
	if !strings.Contains(got, "cycles [import] (1+)") || !strings.Contains(got, "a.go -> b.go -> a.go") {
		t.Errorf("unexpected cycles render:\n%s", got)
	}
}
