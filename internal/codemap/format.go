package codemap

import (
	"fmt"
	"strings"
)

// FormatTree renders a bounded outline suitable for humans and model context.
func FormatTree(idx *CodeIndex, max int) string {
	if idx == nil {
		return "(code map unavailable)\n"
	}
	if max <= 0 {
		max = 200
	}
	var b strings.Builder
	count := 0
	var walk func(NodeID, int)
	walk = func(id NodeID, depth int) {
		if count >= max {
			return
		}
		n, ok := idx.Node(id)
		if !ok {
			return
		}
		fmt.Fprintf(&b, "%s%s\t%s\t%s\n", strings.Repeat("  ", depth), n.Kind, displayName(n), statsText(n))
		count++
		for _, child := range idx.Children(id) {
			walk(child, depth+1)
			if count >= max {
				break
			}
		}
	}
	walk(idx.Root(), 0)
	if count >= max && idx.Count() > max {
		fmt.Fprintf(&b, "…[truncated at %d nodes]\n", max)
	}
	return b.String()
}

// FormatNodes renders a bounded flat list of nodes, usually filter results.
func FormatNodes(nodes []Node, max int) string {
	if len(nodes) == 0 {
		return "(no matches)\n"
	}
	if max <= 0 {
		max = 50
	}
	var b strings.Builder
	for i, n := range nodes {
		if i >= max {
			fmt.Fprintf(&b, "…[truncated at %d results]\n", max)
			break
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", locationText(n), n.Kind, displayName(n), statsText(n))
	}
	return b.String()
}

// FormatDependencies renders dependency query results.
func FormatDependencies(title string, nodes []Node, max int) string {
	if len(nodes) == 0 {
		return "(no dependencies)\n"
	}
	if max <= 0 {
		max = 50
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", title)
	for i, n := range nodes {
		if i >= max {
			fmt.Fprintf(&b, "…[truncated at %d results]\n", max)
			break
		}
		fmt.Fprintf(&b, "%s\t%s\n", n.RelPath, statsText(n))
	}
	return b.String()
}

// FormatImpact renders a dependency blast-radius query with direction labels.
func FormatImpact(result ImpactResult, max int) string {
	if result.Target.ID == "" {
		return "(no impact target)\n"
	}
	if max <= 0 {
		max = 50
	}
	var b strings.Builder
	fmt.Fprintf(&b, "impact\t%s\n", result.Target.RelPath)
	writeSection := func(title string, nodes []Node) {
		fmt.Fprintf(&b, "%s\n", title)
		if len(nodes) == 0 {
			b.WriteString("  (none)\n")
			return
		}
		for i, n := range nodes {
			if i >= max {
				fmt.Fprintf(&b, "  …[truncated at %d results]\n", max)
				break
			}
			fmt.Fprintf(&b, "  %s\t%s\n", n.RelPath, statsText(n))
		}
	}
	writeSection("direct dependencies", result.DirectDependencies)
	writeSection("direct dependents", result.DirectDependents)
	writeSection("transitive dependents", result.TransitiveDependents)
	b.WriteString("cycles\n")
	if len(result.Cycles) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for i, cycle := range result.Cycles {
			if i >= max {
				fmt.Fprintf(&b, "  …[truncated at %d cycles]\n", max)
				break
			}
			parts := make([]string, 0, len(cycle)+1)
			for _, n := range cycle {
				parts = append(parts, n.RelPath)
			}
			if len(cycle) > 0 {
				parts = append(parts, cycle[0].RelPath)
			}
			fmt.Fprintf(&b, "  %s\n", strings.Join(parts, " -> "))
		}
	}
	return b.String()
}

// FormatCycles renders import cycles for humans and agents.
func FormatCycles(cycles [][]Node, max int) string {
	if len(cycles) == 0 {
		return "(no cycles)\n"
	}
	if max <= 0 {
		max = 50
	}
	var b strings.Builder
	b.WriteString("cycles\n")
	for i, cycle := range cycles {
		if i >= max {
			fmt.Fprintf(&b, "…[truncated at %d cycles]\n", max)
			break
		}
		parts := make([]string, 0, len(cycle)+1)
		for _, n := range cycle {
			parts = append(parts, n.RelPath)
		}
		if len(cycle) > 0 {
			parts = append(parts, cycle[0].RelPath)
		}
		fmt.Fprintf(&b, "%s\n", strings.Join(parts, " -> "))
	}
	return b.String()
}

// Projection returns a compact structure summary for agent context injection.
func Projection(idx *CodeIndex, max int) string {
	if idx == nil {
		return "(code map unavailable)\n"
	}
	if max <= 0 {
		max = 120
	}
	root, _ := idx.Node(idx.Root())
	var b strings.Builder
	fmt.Fprintf(&b, "root\t%s\tfiles=%d symbols=%d loc=%d exported=%d private=%d\n", idx.RootPath(), root.Stats.Files, root.Stats.Symbols, root.Stats.LOC, root.Stats.Exported, root.Stats.Private)
	written := 0
	for _, n := range idx.Nodes() {
		if n.Kind != NodeFile || n.Stats.Symbols == 0 {
			continue
		}
		if written >= max {
			fmt.Fprintf(&b, "…[truncated at %d files]\n", max)
			break
		}
		fmt.Fprintf(&b, "file\t%s\tloc=%d symbols=%d exported=%d\n", n.RelPath, n.Stats.LOC, n.Stats.Symbols, n.Stats.Exported)
		for _, sym := range idx.SymbolsForFile(n.RelPath) {
			fmt.Fprintf(&b, "  %s\t%s\t%s:%d\n", sym.Symbol.Kind, sym.Name, sym.RelPath, sym.Symbol.Range.Start.Line+1)
		}
		written++
	}
	return b.String()
}

func displayName(n Node) string {
	if n.Kind == NodeSymbol {
		if n.Symbol.Container != "" && n.Symbol.Container != "fallback" && n.Symbol.Container != "lsp" {
			return n.Symbol.Container + "." + n.Name
		}
		return n.Name
	}
	return n.RelPath
}

func statsText(n Node) string {
	switch n.Kind {
	case NodeDirectory:
		return fmt.Sprintf("files=%d symbols=%d loc=%d", n.Stats.Files, n.Stats.Symbols, n.Stats.LOC)
	case NodeFile:
		return fmt.Sprintf("loc=%d symbols=%d exported=%d private=%d", n.Stats.LOC, n.Stats.Symbols, n.Stats.Exported, n.Stats.Private)
	case NodeSymbol:
		exp := "private"
		if n.Symbol.Exported {
			exp = "exported"
		}
		return fmt.Sprintf("%s %s line=%d", n.Symbol.Kind, exp, n.Symbol.Range.Start.Line+1)
	default:
		return ""
	}
}

func locationText(n Node) string {
	if n.Kind == NodeDirectory {
		return n.RelPath
	}
	if n.Kind == NodeSymbol {
		return fmt.Sprintf("%s:%d:%d", n.RelPath, n.Symbol.Range.Start.Line+1, n.Symbol.Range.Start.Character+1)
	}
	return n.RelPath
}
