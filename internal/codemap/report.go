package codemap

import (
	"fmt"
	"strings"
)

// Provenance labels how a report section's claim was derived, so a reader can
// tell a resolved import edge from a name-matching guess.
type Provenance string

const (
	// ProvImport marks results read straight off resolved import edges.
	ProvImport Provenance = "import"
	// ProvHeuristic marks path/name-matching guesses (test and doc pairing).
	ProvHeuristic Provenance = "heuristic"
	// ProvLSP marks results from a live language-server query.
	ProvLSP Provenance = "lsp"
)

// ReportSection is one titled group of files (or cycles) in a Report.
type ReportSection struct {
	Title      string
	Provenance Provenance
	Nodes      []Node
	Cycles     [][]Node
	// AtLeast marks Nodes as capped by the query limit rather than exhausted.
	AtLeast bool
	// Raw holds pre-rendered lines (e.g. an LSP supplement) emitted verbatim.
	Raw []string
}

// Report is a provenance-labeled, byte-budgeted answer to one code_impact query.
type Report struct {
	Mode     string
	Target   string
	Info     IndexInfo
	Sections []ReportSection
}

// Render writes the report within budgetBytes. Item lines are dropped once the
// budget is spent, but every section keeps its title and true count, and the
// omission is stated explicitly — never silent. The output depends only on the
// report, so identical queries over an identical index render identically.
func (r Report) Render(budgetBytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "code_impact %s %s\n", r.Mode, r.Target)
	fmt.Fprintf(&b, "index: %s\n", r.Info.String())

	for _, sec := range r.Sections {
		count := len(sec.Nodes) + len(sec.Cycles) + len(sec.Raw)
		atLeast := ""
		if sec.AtLeast {
			atLeast = "+"
		}
		fmt.Fprintf(&b, "%s [%s] (%d%s)\n", sec.Title, sec.Provenance, count, atLeast)
		if count == 0 {
			b.WriteString("  (none)\n")
			continue
		}
		shown := 0
		emit := func(line string) bool {
			if budgetBytes > 0 && b.Len()+len(line)+1 > budgetBytes {
				return false
			}
			b.WriteString("  " + line + "\n")
			shown++
			return true
		}
		for _, n := range sec.Nodes {
			if !emit(n.RelPath) {
				break
			}
		}
		if shown == len(sec.Nodes) {
			for _, cycle := range sec.Cycles {
				if !emit(cycleText(cycle)) {
					break
				}
			}
		}
		if shown == len(sec.Nodes)+len(sec.Cycles) {
			for _, line := range sec.Raw {
				if !emit(line) {
					break
				}
			}
		}
		if omitted := count - shown; omitted > 0 {
			fmt.Fprintf(&b, "  …[%d more omitted: output budget]\n", omitted)
		}
	}
	return b.String()
}

func cycleText(cycle []Node) string {
	parts := make([]string, 0, len(cycle)+1)
	for _, n := range cycle {
		parts = append(parts, n.RelPath)
	}
	if len(cycle) > 0 {
		parts = append(parts, cycle[0].RelPath)
	}
	return strings.Join(parts, " -> ")
}
