// Package codemap builds a compact, read-only structural index of a workspace.
// The first slice is outline-first: directory, file, and symbol nodes. Dependency
// and call edges can layer onto the same stable node IDs later.
package codemap

import (
	"path/filepath"
	"sort"
	"strings"
)

// NodeID is stable within one workspace snapshot. IDs are repo-relative where
// possible so TUI state and agent output remain readable and deterministic.
type NodeID string

// NodeKind identifies the coarse graph layer a node belongs to.
type NodeKind string

type EdgeKind string

const (
	EdgeImports EdgeKind = "imports"
)

const (
	NodeDirectory NodeKind = "directory"
	NodeFile      NodeKind = "file"
	NodeSymbol    NodeKind = "symbol"
)

// Position is a zero-based source position. It mirrors LSP coordinates but
// keeps the codemap package independent from a specific symbol provider.
type Position struct {
	Line      int
	Character int
}

// Range is a half-open source range in zero-based line/character coordinates.
type Range struct {
	Start Position
	End   Position
}

// Node is one immutable item in a CodeIndex snapshot.
type Node struct {
	ID       NodeID
	Parent   NodeID
	Kind     NodeKind
	Name     string
	Path     string
	RelPath  string
	Language string
	Symbol   SymbolInfo
	Stats    Stats
}

// SymbolInfo stores metadata only symbol nodes need.
type SymbolInfo struct {
	Kind      string
	Container string
	Exported  bool
	Range     Range
}

// Stats aggregates cheap structure metrics for display and projections.
type Stats struct {
	LOC      int
	Files    int
	Symbols  int
	Exported int
	Private  int
}

// Edge is a directed relationship between two indexed nodes.
type Edge struct {
	From NodeID
	To   NodeID
	Kind EdgeKind
	Meta string
}

// ImpactResult is a bounded blast-radius query over import edges.
type ImpactResult struct {
	Target               Node
	DirectDependencies   []Node
	DirectDependents     []Node
	TransitiveDependents []Node
	Cycles               [][]Node
}

// MaxDepthAll means dependency traversal should continue until exhausted.
const MaxDepthAll = -1

// CodeIndex is an immutable structural snapshot. Mutating rebuilds publish a
// new instance instead of editing this one in place.
type CodeIndex struct {
	root     string
	rootID   NodeID
	nodes    map[NodeID]Node
	children map[NodeID][]NodeID
	edgesOut map[NodeID][]Edge
	edgesIn  map[NodeID][]Edge
	order    []NodeID
}

// NewIndex constructs an immutable snapshot from prebuilt nodes and edges.
func NewIndex(root string, rootID NodeID, nodes map[NodeID]Node, children map[NodeID][]NodeID, edges ...[]Edge) *CodeIndex {
	idx := &CodeIndex{root: root, rootID: rootID, nodes: map[NodeID]Node{}, children: map[NodeID][]NodeID{}, edgesOut: map[NodeID][]Edge{}, edgesIn: map[NodeID][]Edge{}}
	for id, node := range nodes {
		idx.nodes[id] = node
		idx.order = append(idx.order, id)
	}
	for id, kids := range children {
		cp := append([]NodeID(nil), kids...)
		sort.Slice(cp, func(i, j int) bool { return lessNode(idx.nodes[cp[i]], idx.nodes[cp[j]]) })
		idx.children[id] = cp
	}
	if len(edges) > 0 {
		for _, edge := range edges[0] {
			idx.edgesOut[edge.From] = append(idx.edgesOut[edge.From], edge)
			idx.edgesIn[edge.To] = append(idx.edgesIn[edge.To], edge)
		}
	}
	for id := range idx.edgesOut {
		sort.Slice(idx.edgesOut[id], func(i, j int) bool { return edgeLess(idx, idx.edgesOut[id][i], idx.edgesOut[id][j]) })
	}
	for id := range idx.edgesIn {
		sort.Slice(idx.edgesIn[id], func(i, j int) bool { return edgeLess(idx, idx.edgesIn[id][i], idx.edgesIn[id][j]) })
	}
	sort.Slice(idx.order, func(i, j int) bool { return string(idx.order[i]) < string(idx.order[j]) })
	return idx
}

func lessNode(a, b Node) bool {
	if a.Kind != b.Kind {
		return kindRank(a.Kind) < kindRank(b.Kind)
	}
	if a.RelPath != b.RelPath {
		return a.RelPath < b.RelPath
	}
	if a.Symbol.Range.Start.Line != b.Symbol.Range.Start.Line {
		return a.Symbol.Range.Start.Line < b.Symbol.Range.Start.Line
	}
	if a.Symbol.Range.Start.Character != b.Symbol.Range.Start.Character {
		return a.Symbol.Range.Start.Character < b.Symbol.Range.Start.Character
	}
	return a.Name < b.Name
}

func edgeLess(idx *CodeIndex, a, b Edge) bool {
	an := idx.nodes[a.To]
	bn := idx.nodes[b.To]
	if an.RelPath != bn.RelPath {
		return an.RelPath < bn.RelPath
	}
	if a.Meta != b.Meta {
		return a.Meta < b.Meta
	}
	return a.Kind < b.Kind
}

func kindRank(k NodeKind) int {
	switch k {
	case NodeDirectory:
		return 0
	case NodeFile:
		return 1
	case NodeSymbol:
		return 2
	default:
		return 9
	}
}

// Root returns the root node ID for tree rendering.
func (i *CodeIndex) Root() NodeID {
	if i == nil {
		return ""
	}
	return i.rootID
}

// RootPath returns the absolute workspace root captured by this snapshot.
func (i *CodeIndex) RootPath() string {
	if i == nil {
		return ""
	}
	return i.root
}

// Node returns a node by ID.
func (i *CodeIndex) Node(id NodeID) (Node, bool) {
	if i == nil {
		return Node{}, false
	}
	n, ok := i.nodes[id]
	return n, ok
}

// Children returns a copy of a node's ordered child IDs.
func (i *CodeIndex) Children(id NodeID) []NodeID {
	if i == nil {
		return nil
	}
	return append([]NodeID(nil), i.children[id]...)
}

// Nodes returns every node in deterministic ID order.
func (i *CodeIndex) Nodes() []Node {
	if i == nil {
		return nil
	}
	out := make([]Node, 0, len(i.order))
	for _, id := range i.order {
		out = append(out, i.nodes[id])
	}
	return out
}

// SymbolsForFile returns symbol children for one repo-relative or absolute path.
func (i *CodeIndex) SymbolsForFile(path string) []Node {
	if i == nil {
		return nil
	}
	key := filepath.Clean(path)
	if filepath.IsAbs(key) {
		if rel, err := filepath.Rel(i.root, key); err == nil {
			key = rel
		}
	}
	var fileID NodeID
	for _, node := range i.nodes {
		if node.Kind == NodeFile && filepath.Clean(node.RelPath) == key {
			fileID = node.ID
			break
		}
	}
	if fileID == "" {
		return nil
	}
	var out []Node
	for _, id := range i.children[fileID] {
		if n := i.nodes[id]; n.Kind == NodeSymbol {
			out = append(out, n)
		}
	}
	return out
}

// Filter returns nodes whose path, name, kind, or container matches query. It is
// intentionally simple and deterministic; TUI fuzzy ranking can layer on later.
func (i *CodeIndex) Filter(query string, max int) []Node {
	if i == nil {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return i.Nodes()
	}
	if max <= 0 {
		max = 200
	}
	out := make([]Node, 0, min(max, len(i.order)))
	for _, id := range i.order {
		n := i.nodes[id]
		hay := strings.ToLower(strings.Join([]string{n.Name, n.RelPath, string(n.Kind), n.Symbol.Kind, n.Symbol.Container}, " "))
		if strings.Contains(hay, query) {
			out = append(out, n)
			if len(out) >= max {
				break
			}
		}
	}
	return out
}

// Dependencies returns outgoing import edges for a matching file node.
func (i *CodeIndex) Dependencies(pathOrQuery string, max int) []Node {
	return i.edgeNodes(pathOrQuery, true, max)
}

// Dependents returns incoming import edges for a matching file node.
func (i *CodeIndex) Dependents(pathOrQuery string, max int) []Node {
	return i.edgeNodes(pathOrQuery, false, max)
}

// Impact returns direct dependencies, direct dependents, transitive dependents,
// and import cycles involving the target file. Depth limits only the transitive
// reverse traversal; use MaxDepthAll for an unbounded walk.
func (i *CodeIndex) Impact(pathOrQuery string, depth, max int) ImpactResult {
	if max <= 0 {
		max = 100
	}
	start := i.firstFileMatch(pathOrQuery)
	if start == "" {
		return ImpactResult{}
	}
	target, _ := i.nodes[start]
	return ImpactResult{
		Target:               target,
		DirectDependencies:   i.Dependencies(pathOrQuery, max),
		DirectDependents:     i.Dependents(pathOrQuery, max),
		TransitiveDependents: i.walkImportEdges(start, false, depth, max),
		Cycles:               i.Cycles(pathOrQuery, max),
	}
}

func (i *CodeIndex) walkImportEdges(start NodeID, outgoing bool, depth, max int) []Node {
	if i == nil || start == "" || max == 0 {
		return nil
	}
	if max < 0 {
		max = 100
	}
	type item struct {
		id    NodeID
		depth int
	}
	seen := map[NodeID]bool{start: true}
	queue := []item{{id: start, depth: 0}}
	out := make([]Node, 0)
	for len(queue) > 0 && len(out) < max {
		cur := queue[0]
		queue = queue[1:]
		if depth != MaxDepthAll && cur.depth >= depth {
			continue
		}
		edges := i.edgesOut[cur.id]
		if !outgoing {
			edges = i.edgesIn[cur.id]
		}
		for _, edge := range edges {
			next := edge.To
			if !outgoing {
				next = edge.From
			}
			if seen[next] {
				continue
			}
			seen[next] = true
			if node, ok := i.nodes[next]; ok {
				out = append(out, node)
				if len(out) >= max {
					break
				}
			}
			queue = append(queue, item{id: next, depth: cur.depth + 1})
		}
	}
	return sortedNodes(out)
}

func sortedNodes(nodes []Node) []Node {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].RelPath != nodes[j].RelPath {
			return nodes[i].RelPath < nodes[j].RelPath
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes
}

func (i *CodeIndex) edgeNodes(pathOrQuery string, outgoing bool, max int) []Node {
	if i == nil {
		return nil
	}
	if max <= 0 {
		max = 100
	}
	start := i.firstFileMatch(pathOrQuery)
	if start == "" {
		return nil
	}
	edges := i.edgesOut[start]
	if !outgoing {
		edges = i.edgesIn[start]
	}
	out := make([]Node, 0, min(max, len(edges)))
	seen := map[NodeID]bool{}
	for _, edge := range edges {
		id := edge.To
		if !outgoing {
			id = edge.From
		}
		if seen[id] {
			continue
		}
		if node, ok := i.nodes[id]; ok {
			out = append(out, node)
			seen[id] = true
			if len(out) >= max {
				break
			}
		}
	}
	return sortedNodes(out)
}

func (i *CodeIndex) firstFileMatch(pathOrQuery string) NodeID {
	key := filepath.Clean(strings.TrimSpace(pathOrQuery))
	if key == "." || key == "" {
		return ""
	}
	if filepath.IsAbs(key) {
		if rel, err := filepath.Rel(i.root, key); err == nil {
			key = filepath.ToSlash(rel)
		}
	}
	query := strings.ToLower(filepath.ToSlash(key))
	for _, id := range i.order {
		n := i.nodes[id]
		if n.Kind != NodeFile {
			continue
		}
		rel := strings.ToLower(filepath.ToSlash(n.RelPath))
		if rel == query || strings.Contains(rel, query) || strings.Contains(strings.ToLower(n.Name), query) {
			return id
		}
	}
	return ""
}

// Cycles returns import cycles, optionally narrowed to cycles involving a
// matching file/path query. The result is capped and deterministic for the same
// indexed graph.
func (i *CodeIndex) Cycles(pathOrQuery string, max int) [][]Node {
	if i == nil {
		return nil
	}
	if max <= 0 {
		max = 20
	}
	target := i.firstFileMatch(pathOrQuery)
	var starts []NodeID
	if target != "" {
		starts = []NodeID{target}
	} else if strings.TrimSpace(pathOrQuery) != "" {
		return nil
	} else {
		for _, id := range i.order {
			if i.nodes[id].Kind == NodeFile {
				starts = append(starts, id)
			}
		}
	}
	seenCycles := map[string]bool{}
	var out [][]Node
	for _, start := range starts {
		i.findCyclesFrom(start, target, seenCycles, &out, max)
		if len(out) >= max {
			break
		}
	}
	return out
}

func (i *CodeIndex) findCyclesFrom(start, required NodeID, seen map[string]bool, out *[][]Node, max int) {
	path := []NodeID{start}
	inPath := map[NodeID]int{start: 0}
	var dfs func(NodeID)
	dfs = func(cur NodeID) {
		if len(*out) >= max {
			return
		}
		for _, edge := range i.edgesOut[cur] {
			next := edge.To
			if pos, ok := inPath[next]; ok {
				cycleIDs := append([]NodeID(nil), path[pos:]...)
				if required != "" && !containsNodeID(cycleIDs, required) {
					continue
				}
				key := cycleKey(cycleIDs)
				if seen[key] {
					continue
				}
				seen[key] = true
				cycle := make([]Node, 0, len(cycleIDs))
				for _, id := range cycleIDs {
					cycle = append(cycle, i.nodes[id])
				}
				*out = append(*out, cycle)
				continue
			}
			if _, ok := i.nodes[next]; !ok {
				continue
			}
			inPath[next] = len(path)
			path = append(path, next)
			dfs(next)
			path = path[:len(path)-1]
			delete(inPath, next)
		}
	}
	dfs(start)
}

func containsNodeID(ids []NodeID, want NodeID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func cycleKey(ids []NodeID) string {
	if len(ids) == 0 {
		return ""
	}
	minIdx := 0
	for i := 1; i < len(ids); i++ {
		if ids[i] < ids[minIdx] {
			minIdx = i
		}
	}
	rotated := make([]string, 0, len(ids))
	for i := range ids {
		rotated = append(rotated, string(ids[(minIdx+i)%len(ids)]))
	}
	return strings.Join(rotated, " -> ")
}

// Edges returns a copy of all edges, filtered by kind when kind is non-empty.
func (i *CodeIndex) Edges(kind EdgeKind) []Edge {
	if i == nil {
		return nil
	}
	var out []Edge
	for _, id := range i.order {
		for _, edge := range i.edgesOut[id] {
			if kind == "" || edge.Kind == kind {
				out = append(out, edge)
			}
		}
	}
	return out
}

// Count returns the number of indexed nodes.
func (i *CodeIndex) Count() int {
	if i == nil {
		return 0
	}
	return len(i.nodes)
}
