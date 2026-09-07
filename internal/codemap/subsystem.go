package codemap

import (
	"path/filepath"
	"sort"
)

// SubsystemOverview summarizes one directory: entry points, exported public
// surface, core types, tests, and key external dependencies. It answers
// "where do I start in this subsystem?" for `/map <dir>`.
type SubsystemOverview struct {
	Dir             Node
	EntryPoints     []Node
	PublicSurface   []Node
	CoreTypes       []Node
	Tests           []Node
	KeyDependencies []Node
}

var coreTypeSymbolKinds = map[string]bool{
	"type": true, "struct": true, "interface": true, "class": true, "enum": true, "trait": true,
}

// BuildSubsystemOverview reports a subsystem overview for one repo-relative
// directory. ok is false when dirRelPath doesn't match an indexed directory
// exactly. max bounds the public-surface, core-type, and tests sections;
// entry points and key dependencies use their own smaller caps.
func BuildSubsystemOverview(idx *CodeIndex, dirRelPath string, max int) (SubsystemOverview, bool) {
	if idx == nil {
		return SubsystemOverview{}, false
	}
	dirNodeID := idx.firstDirMatch(dirRelPath)
	if dirNodeID == "" {
		return SubsystemOverview{}, false
	}
	dir := idx.nodes[dirNodeID]
	if max <= 0 {
		max = 15
	}
	files := idx.filesInDir(dir.RelPath)

	entryPoints := capNodes(subsystemEntryPoints(idx, files), 5)

	var publicSurface []Node
	for _, f := range files {
		for _, sym := range idx.SymbolsForFile(f.RelPath) {
			if sym.Symbol.Exported {
				publicSurface = append(publicSurface, sym)
			}
		}
	}
	sort.SliceStable(publicSurface, func(i, j int) bool { return publicSurface[i].Name < publicSurface[j].Name })
	publicSurface = capNodes(publicSurface, max)

	var coreTypes []Node
	for _, sym := range publicSurface {
		if coreTypeSymbolKinds[sym.Symbol.Kind] {
			coreTypes = append(coreTypes, sym)
		}
	}
	coreTypes = capNodes(coreTypes, 8)

	tests := capNodes(subsystemTests(idx, dir.RelPath), max)
	keyDeps := capNodes(subsystemKeyDependencies(idx, dir.RelPath, files), 10)

	return SubsystemOverview{
		Dir:             dir,
		EntryPoints:     entryPoints,
		PublicSurface:   publicSurface,
		CoreTypes:       coreTypes,
		Tests:           tests,
		KeyDependencies: keyDeps,
	}, true
}

// subsystemEntryPoints prefers Go's `func main()` convention; languages
// without one fall back to files nothing in the repo imports, which are
// plausible entry points or standalone consumers.
func subsystemEntryPoints(idx *CodeIndex, files []Node) []Node {
	var mains []Node
	for _, f := range files {
		for _, sym := range idx.SymbolsForFile(f.RelPath) {
			if sym.Name == "main" && sym.Symbol.Kind == "function" {
				mains = append(mains, f)
				break
			}
		}
	}
	if len(mains) > 0 {
		return mains
	}
	var uncalled []Node
	for _, f := range files {
		if len(idx.Dependents(f.RelPath, 1)) == 0 {
			uncalled = append(uncalled, f)
		}
	}
	return uncalled
}

// subsystemTests walks every file under dir (recursively) and reports the
// ones that look like tests for a sibling source file in the same subtree.
func subsystemTests(idx *CodeIndex, dir string) []Node {
	descendants := idx.descendantFiles(dir)
	sources := make([]Node, 0, len(descendants))
	for _, f := range descendants {
		base := filepath.Base(f.RelPath)
		if !isTestBaseName(base, filepath.Ext(base)) {
			sources = append(sources, f)
		}
	}
	seen := map[NodeID]bool{}
	var tests []Node
	for _, candidate := range descendants {
		if seen[candidate.ID] {
			continue
		}
		for _, src := range sources {
			if IsTestFor(candidate.RelPath, src.RelPath) {
				seen[candidate.ID] = true
				tests = append(tests, candidate)
				break
			}
		}
	}
	return tests
}

// subsystemKeyDependencies aggregates the external (outside dir) import
// targets of every file directly in dir, ranked by how many of those files
// depend on each target.
func subsystemKeyDependencies(idx *CodeIndex, dir string, files []Node) []Node {
	count := map[NodeID]int{}
	node := map[NodeID]Node{}
	for _, f := range files {
		seenForFile := map[NodeID]bool{}
		for _, dep := range idx.Dependencies(f.RelPath, 50) {
			if parentRel(dep.RelPath) == dir || seenForFile[dep.ID] {
				continue
			}
			seenForFile[dep.ID] = true
			count[dep.ID]++
			node[dep.ID] = dep
		}
	}
	out := make([]Node, 0, len(node))
	for _, n := range node {
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if count[out[i].ID] != count[out[j].ID] {
			return count[out[i].ID] > count[out[j].ID]
		}
		return out[i].RelPath < out[j].RelPath
	})
	return out
}

func capNodes(nodes []Node, max int) []Node {
	if max > 0 && len(nodes) > max {
		return nodes[:max]
	}
	return nodes
}
