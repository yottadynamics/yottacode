package codemap

import (
	"path/filepath"
	"sort"
	"strings"
)

// IsTestFile reports whether a repo-relative path looks like a test source file:
// a per-language test file name, or a source file under a tests/test/__tests__
// directory. Path-only and approximate by design.
func IsTestFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	if isTestBaseName(base, ext) {
		return true
	}
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs":
	default:
		return false
	}
	for _, seg := range strings.Split(parentRel(rel), "/") {
		switch seg {
		case "tests", "test", "__tests__":
			return true
		}
	}
	return false
}

// TestDir is a directory holding tests that a change can affect.
type TestDir struct {
	Dir   string
	Files int
}

// dirGraph groups the import edges and test files by directory, once per
// snapshot, so test-selection queries do not rescan the whole graph.
type dirGraph struct {
	incoming map[string][]Edge // directory -> import edges landing in it
	tests    map[string][]Node // directory -> its test files
}

func (i *CodeIndex) dirs() *dirGraph {
	i.dirsOnce.Do(func() {
		g := &dirGraph{incoming: map[string][]Edge{}, tests: map[string][]Node{}}
		for _, id := range i.order {
			n := i.nodes[id]
			if n.Kind == NodeFile && n.Language != "markdown" && IsTestFile(n.RelPath) {
				dir := parentRel(n.RelPath)
				g.tests[dir] = append(g.tests[dir], n)
			}
		}
		for _, edges := range i.edgesIn {
			for _, e := range edges {
				if e.Kind != EdgeImports {
					continue
				}
				to := parentRel(i.nodes[e.To].RelPath)
				g.incoming[to] = append(g.incoming[to], e)
			}
		}
		i.dirGraph = g
	})
	return i.dirGraph
}

// TestDirsToRun returns the directories whose tests a change to target can
// affect: target's own directory plus every directory that imports it directly
// or transitively, kept when it contains test files. It deliberately works at
// directory (package) granularity, ignoring which file inside the target
// directory changed or which of its files an importer uses, because narrowing
// to files drops affected tests whenever behavior reaches an importer through
// package state or an intermediate package's other files. keep, when non-nil,
// excludes files (for example on a read deny list) from both the closure and the
// counts. The target's directory comes first, then the rest by path.
func (i *CodeIndex) TestDirsToRun(target Node, keep func(Node) bool, max int) []TestDir {
	if i == nil || target.ID == "" {
		return nil
	}
	g := i.dirs()
	start := parentRel(target.RelPath)
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.incoming[cur] {
			from := i.nodes[e.From]
			if keep != nil && !keep(from) {
				continue
			}
			if dir := parentRel(from.RelPath); !seen[dir] {
				seen[dir] = true
				queue = append(queue, dir)
			}
		}
	}
	return i.testDirs(g, seen, start, keep, max)
}

// TestDirsFromImpact is the narrow, file-level alternative to TestDirsToRun: only
// the directories of the target and of its file-level (symbol-narrowed) direct
// and transitive dependents. It runs fewer tests and recalls fewer affected
// ones, because a package that imports the target's package without using the
// changed file is dropped.
func (i *CodeIndex) TestDirsFromImpact(target Node, res ImpactResult, keep func(Node) bool) []TestDir {
	if i == nil || target.ID == "" {
		return nil
	}
	g := i.dirs()
	start := parentRel(target.RelPath)
	seen := map[string]bool{start: true}
	for _, group := range [][]Node{res.DirectDependents, res.TransitiveDependents} {
		for _, n := range group {
			if keep == nil || keep(n) {
				seen[parentRel(n.RelPath)] = true
			}
		}
	}
	return i.testDirs(g, seen, start, keep, 0)
}

// testDirs keeps the directories of seen that hold (kept) test files, the start
// directory first and the rest by path.
func (i *CodeIndex) testDirs(g *dirGraph, seen map[string]bool, start string, keep func(Node) bool, max int) []TestDir {
	var out []TestDir
	for dir := range seen {
		n := 0
		for _, f := range g.tests[dir] {
			if keep == nil || keep(f) {
				n++
			}
		}
		if n > 0 {
			out = append(out, TestDir{Dir: dir, Files: n})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if (out[a].Dir == start) != (out[b].Dir == start) {
			return out[a].Dir == start
		}
		return out[a].Dir < out[b].Dir
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}
