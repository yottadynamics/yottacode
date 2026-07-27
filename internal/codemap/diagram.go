package codemap

import (
	"fmt"
	"regexp"
	"strings"
)

var mermaidIDRe = regexp.MustCompile(`[^A-Za-z0-9_]`)

// MermaidDiagram renders a bounded import graph. When focus is non-empty, the
// graph is narrowed to direct dependencies/dependents of the matching file.
func MermaidDiagram(idx *CodeIndex, focus string, maxEdges int) string {
	if idx == nil {
		return "```mermaid\ngraph TD\n```\n"
	}
	if maxEdges <= 0 {
		maxEdges = 80
	}
	edges := idx.Edges(EdgeImports)
	if strings.TrimSpace(focus) != "" {
		if target := idx.firstFileMatch(focus); target != "" {
			filtered := make([]Edge, 0)
			for _, edge := range edges {
				if edge.From == target || edge.To == target {
					filtered = append(filtered, edge)
				}
			}
			edges = filtered
		} else {
			edges = nil
		}
	}
	var b strings.Builder
	b.WriteString("```mermaid\ngraph TD\n")
	if len(edges) == 0 {
		b.WriteString("  empty[\"no import edges\"]\n")
		b.WriteString("```\n")
		return b.String()
	}
	written := 0
	for _, edge := range edges {
		if written >= maxEdges {
			fmt.Fprintf(&b, "  truncated[\"truncated at %d edges\"]\n", maxEdges)
			break
		}
		from, okFrom := idx.Node(edge.From)
		to, okTo := idx.Node(edge.To)
		if !okFrom || !okTo {
			continue
		}
		fmt.Fprintf(&b, "  %s[\"%s\"] --> %s[\"%s\"]\n", mermaidID(from), escapeMermaidLabel(from.RelPath), mermaidID(to), escapeMermaidLabel(to.RelPath))
		written++
	}
	b.WriteString("```\n")
	return b.String()
}

func mermaidID(n Node) string {
	id := mermaidIDRe.ReplaceAllString(n.RelPath, "_")
	id = strings.Trim(id, "_")
	if id == "" {
		return "root"
	}
	return "n_" + id
}

func escapeMermaidLabel(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}
