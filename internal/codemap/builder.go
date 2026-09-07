package codemap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

const defaultMaxFiles = 2000

// SymbolSource provides structural symbols for one source file. Implementations
// can use LSP, fallback parsers, or both.
type SymbolSource interface {
	Symbols(ctx context.Context, path string) ([]lsp.Symbol, string, error)
}

// BuildOptions controls a CodeIndex rebuild.
type BuildOptions struct {
	Root     string
	MaxFiles int
	Source   SymbolSource

	// WatchDebounce controls how long CachedProvider.StartWatch waits after
	// the last relevant filesystem event before applying pending changes.
	// Zero defaults to 300ms in production; tests can set a few
	// milliseconds so watch-triggered-rebuild tests aren't slow.
	WatchDebounce time.Duration
}

// Build walks the workspace and returns an immutable structure snapshot.
func Build(ctx context.Context, opts BuildOptions) (*CodeIndex, error) {
	st, err := newBuildState(opts)
	if err != nil {
		return nil, err
	}
	if err := st.walkAll(ctx); err != nil {
		return nil, err
	}
	return st.finalize(), nil
}

// buildState holds Build's exploded intermediate maps so a change to one
// file can be re-derived (see upsertFile/removeFile) and the graph
// re-finalized without re-walking or re-parsing the rest of the workspace —
// the basis for CachedProvider's incremental watch-triggered updates.
// "own" stats (a node's own contribution before summing children) are kept
// separate from the Stats field aggregateStats writes into Node, so
// finalize can run any number of times over the same persistent state
// without double-counting a directory's or file's already-aggregated
// totals from a previous run.
type buildState struct {
	absRoot    string
	rootID     NodeID
	modulePath string
	source     SymbolSource
	maxFiles   int
	fileCount  int

	nodes    map[NodeID]Node
	children map[NodeID][]NodeID
	own      map[NodeID]Stats

	goImports  map[string][]string
	goPackages map[string]string
	// goSelectors maps a Go file's rel path to, for each bare identifier
	// used as the receiver of a "x.Y" selector expression anywhere in the
	// file, the set of symbol names selected off it. Most such identifiers
	// are local variables/struct receivers, not package references — only
	// entries matching an actually-imported package's identifier are ever
	// consulted (see goImportTargets) — so collecting all of them here is
	// harmless. Used to narrow an import edge to the specific file(s) that
	// declare a referenced symbol instead of the whole target package.
	goSelectors map[string]map[string][]string
	tsImports   map[string][]string
	pyImports   map[string][]string
	rustImports map[string][]string
}

func newBuildState(opts BuildOptions) (*buildState, error) {
	root := opts.Root
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	source := opts.Source
	if source == nil {
		source = FallbackSource{}
	}
	rootID := NodeID("dir:.")
	return &buildState{
		absRoot:    absRoot,
		rootID:     rootID,
		modulePath: readGoModulePath(filepath.Join(absRoot, "go.mod")),
		source:     source,
		maxFiles:   maxFiles,
		nodes: map[NodeID]Node{
			rootID: {ID: rootID, Kind: NodeDirectory, Name: filepath.Base(absRoot), Path: absRoot, RelPath: "."},
		},
		children:    map[NodeID][]NodeID{},
		own:         map[NodeID]Stats{},
		goImports:   map[string][]string{},
		goPackages:  map[string]string{},
		goSelectors: map[string]map[string][]string{},
		tsImports:   map[string][]string{},
		pyImports:   map[string][]string{},
		rustImports: map[string][]string{},
	}, nil
}

func (st *buildState) walkAll(ctx context.Context) error {
	return st.walkSubtree(ctx, st.absRoot)
}

// walkSubtree walks root (the whole workspace for a fresh Build, or one
// directory for an incremental re-index — see CachedProvider.applyChanges)
// and upserts every directory/file found. Unlike Build's original inline
// WalkDir, the MaxFiles cap is enforced per-node (skip-and-stop for a fresh
// walk once a brand-new file would exceed it) rather than assumed to run
// only once over an empty state, so it stays correct when files already
// tracked from a previous build are encountered again.
func (st *buildState) walkSubtree(ctx context.Context, root string) error {
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == st.absRoot {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			st.upsertDir(path)
			return nil
		}
		if _, _, ok := classifyFile(path); !ok {
			return nil
		}
		rel := relPath(st.absRoot, path)
		if _, existed := st.nodes[fileID(rel)]; !existed && st.fileCount >= st.maxFiles {
			return filepath.SkipAll
		}
		st.upsertFile(ctx, path)
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return err
	}
	return nil
}

// classifyFile reports the language a path should be indexed under, and
// whether it's a plain doc file (markdown, no symbol extraction attempted).
// ok is false for extensions the index doesn't track at all.
func classifyFile(path string) (lang lsp.Language, isDoc bool, ok bool) {
	if lang, ok := lsp.ResolveFile(path); ok {
		return lang, false, true
	}
	if isDocFile(path) {
		return lsp.Language{ID: "markdown"}, true, true
	}
	return lsp.Language{}, false, false
}

func (st *buildState) upsertDir(path string) {
	rel := relPath(st.absRoot, path)
	id := dirID(rel)
	parent := dirID(parentRel(rel))
	st.nodes[id] = Node{ID: id, Parent: parent, Kind: NodeDirectory, Name: filepath.Base(path), Path: path, RelPath: rel}
	st.children[parent] = appendUnique(st.children[parent], id)
}

// upsertFile (re-)derives one file's node, symbol children, and
// per-language import list, replacing any prior contribution for the same
// path first. It enforces MaxFiles itself (not just the walker) so a
// direct single-file call from an incremental update — not routed through
// walkSubtree — still respects the cap for a genuinely new file; an
// existing file being refreshed is never turned away by the cap.
func (st *buildState) upsertFile(ctx context.Context, path string) {
	lang, isDoc, ok := classifyFile(path)
	if !ok {
		return
	}
	rel := relPath(st.absRoot, path)
	_, existed := st.nodes[fileID(rel)]
	if !existed && st.fileCount >= st.maxFiles {
		return
	}
	st.removeFile(rel)
	if !existed {
		st.fileCount++
	}
	parent := dirID(parentRel(rel))
	fid := fileID(rel)
	loc, _ := countLOC(path)
	st.nodes[fid] = Node{ID: fid, Parent: parent, Kind: NodeFile, Name: filepath.Base(path), Path: path, RelPath: rel, Language: lang.ID, Stats: Stats{LOC: loc, Files: 1}}
	st.own[fid] = Stats{LOC: loc, Files: 1}
	st.children[parent] = appendUnique(st.children[parent], fid)
	if isDoc {
		return
	}
	switch lang.ID {
	case "go":
		imports, pkg, selectors, err := parseGoImports(path)
		if err == nil {
			st.goImports[rel] = imports
			st.goSelectors[rel] = selectors
			// A _test.go file's own package clause must never define what
			// package a directory resolves to for an external importer: an
			// external test file (`package foo_test`) sharing a directory
			// with `package foo` would otherwise silently overwrite the
			// real package name here whenever it's the last Go file
			// WalkDir visits in that directory (alphabetical order), which
			// then makes every import of the real package unresolvable.
			if pkg != "" && !strings.HasSuffix(rel, "_test.go") {
				st.goPackages[parentRel(rel)] = pkg
			}
		}
	case "typescript":
		if imports, err := parseTSImports(path); err == nil && len(imports) > 0 {
			st.tsImports[rel] = imports
		}
	case "python":
		if imports, err := parsePythonImports(path); err == nil && len(imports) > 0 {
			st.pyImports[rel] = imports
		}
	case "rust":
		if imports, err := parseRustImports(path); err == nil && len(imports) > 0 {
			st.rustImports[rel] = imports
		}
	}

	syms, sourceName, err := st.source.Symbols(ctx, path)
	if err != nil && !errors.Is(err, lsp.ErrUnsupportedCapability) {
		return
	}
	for idx, sym := range syms {
		rng := fromLSPRange(sym.Range, sym.Location)
		sid := symbolID(rel, sym.Name, rng, idx)
		exported := isExported(sym.Name)
		node := Node{ID: sid, Parent: fid, Kind: NodeSymbol, Name: sym.Name, Path: path, RelPath: rel, Language: lang.ID, Symbol: SymbolInfo{Kind: sym.Kind, Container: containerDefault(sym.Container, sourceName), Exported: exported, Range: rng}, Stats: Stats{Symbols: 1}}
		own := Stats{Symbols: 1}
		if exported {
			node.Stats.Exported = 1
			own.Exported = 1
		} else {
			node.Stats.Private = 1
			own.Private = 1
		}
		st.nodes[sid] = node
		st.own[sid] = own
		st.children[fid] = append(st.children[fid], sid)
	}
}

// removeFile purges a file's node, symbol children, and per-language import
// entries. It is structural cleanup only — it does not adjust fileCount,
// since upsertFile calls it internally to clear stale data before
// re-adding the same file (a no-op count change), while removeFileTracked
// is what a genuine removal (the file is gone from disk) should call.
func (st *buildState) removeFile(rel string) {
	fid := fileID(rel)
	if old, ok := st.nodes[fid]; ok {
		for _, sid := range st.children[fid] {
			delete(st.nodes, sid)
			delete(st.own, sid)
		}
		delete(st.children, fid)
		delete(st.nodes, fid)
		delete(st.own, fid)
		st.children[old.Parent] = removeNodeID(st.children[old.Parent], fid)
	}
	delete(st.goImports, rel)
	delete(st.goSelectors, rel)
	delete(st.tsImports, rel)
	delete(st.pyImports, rel)
	delete(st.rustImports, rel)
}

// removeFileTracked removes a file that has genuinely disappeared from
// disk, decrementing fileCount so long-running watch sessions with lots of
// create/delete churn don't drift toward a phantom MaxFiles cap.
func (st *buildState) removeFileTracked(rel string) {
	if _, ok := st.nodes[fileID(rel)]; ok {
		st.fileCount--
	}
	st.removeFile(rel)
}

// removeDirRecursive purges a directory and everything under it — used when
// a watched directory is removed. It's a no-op if the directory isn't
// currently tracked.
func (st *buildState) removeDirRecursive(rel string) {
	id := dirID(rel)
	n, ok := st.nodes[id]
	if !ok {
		return
	}
	for _, child := range append([]NodeID(nil), st.children[id]...) {
		cn, ok := st.nodes[child]
		if !ok {
			continue
		}
		switch cn.Kind {
		case NodeFile:
			st.removeFileTracked(cn.RelPath)
		case NodeDirectory:
			st.removeDirRecursive(cn.RelPath)
		}
	}
	st.children[n.Parent] = removeNodeID(st.children[n.Parent], id)
	delete(st.children, id)
	delete(st.nodes, id)
}

// finalize re-derives cross-file import edges and aggregate stats — pure
// in-memory computation over already-collected per-file data, no file I/O
// — and returns a fresh immutable snapshot. Safe to call any number of
// times over the same persistent state (see the "own" stats note on
// buildState).
func (st *buildState) finalize() *CodeIndex {
	aggregateStats(st.rootID, st.nodes, st.children, st.own)
	edges := buildGoImportEdges(st.goImports, st.goPackages, st.goSelectors, st.nodes, st.modulePath)
	edges = append(edges, buildTSImportEdges(st.tsImports, st.nodes)...)
	edges = append(edges, buildPythonImportEdges(st.pyImports, st.nodes)...)
	edges = append(edges, buildRustImportEdges(st.rustImports, st.nodes)...)
	return NewIndex(st.absRoot, st.rootID, st.nodes, st.children, edges)
}

// aggregateStats writes each node's aggregated Stats (own contribution plus
// every descendant's) into nodes[id]. own is read-only input, never itself
// mutated by aggregation, which is what makes repeated calls over the same
// nodes/children maps safe — see buildState's doc comment.
func aggregateStats(id NodeID, nodes map[NodeID]Node, children map[NodeID][]NodeID, own map[NodeID]Stats) Stats {
	stats := own[id]
	for _, childID := range children[id] {
		childStats := aggregateStats(childID, nodes, children, own)
		stats.LOC += childStats.LOC
		stats.Files += childStats.Files
		stats.Symbols += childStats.Symbols
		stats.Exported += childStats.Exported
		stats.Private += childStats.Private
	}
	n := nodes[id]
	n.Stats = stats
	nodes[id] = n
	return stats
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" {
		return "."
	}
	return filepath.ToSlash(rel)
}

func parentRel(rel string) string {
	p := filepath.ToSlash(filepath.Dir(rel))
	if p == "" || p == "." {
		return "."
	}
	return p
}

func dirID(rel string) NodeID {
	if rel == "" || rel == "." {
		return "dir:."
	}
	return NodeID("dir:" + filepath.ToSlash(rel))
}

func fileID(rel string) NodeID { return NodeID("file:" + filepath.ToSlash(rel)) }

func symbolID(rel, name string, rng Range, ordinal int) NodeID {
	return NodeID(fmt.Sprintf("sym:%s:%d:%d:%s:%d", filepath.ToSlash(rel), rng.Start.Line, rng.Start.Character, name, ordinal))
}

func appendUnique(in []NodeID, id NodeID) []NodeID {
	if slices.Contains(in, id) {
		return in
	}
	return append(in, id)
}

func removeNodeID(ids []NodeID, target NodeID) []NodeID {
	out := ids[:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

var docExtensions = map[string]bool{".md": true, ".mdx": true}

func isDocFile(path string) bool {
	return docExtensions[strings.ToLower(filepath.Ext(path))]
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".yottacode", "node_modules", "vendor", "target", "build", "dist":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
}

func countLOC(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	lines := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		if strings.TrimSpace(s.Text()) != "" {
			lines++
		}
	}
	return lines, s.Err()
}

// parseGoImports parses one Go file's import paths, package name, and the
// set of symbol names selected off each bare identifier used as a "x.Y"
// expression's receiver anywhere in the file body. The latter lets
// buildGoImportEdges narrow an import edge to the specific file(s) in the
// target package that declare a symbol actually referenced — most
// identifiers this collects are local variables or struct receivers, not
// package references, but only entries matching an import's own package
// name are ever consulted, so collecting all of them is harmless. Full-body
// parsing (not parser.ImportsOnly) is required to see the body at all.
func parseGoImports(path string) (imports []string, pkgName string, usedSelectors map[string][]string, err error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, "", nil, err
	}
	imports = make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		imports = append(imports, strings.Trim(spec.Path.Value, "\"`"))
	}
	usedSelectors = map[string][]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok {
			usedSelectors[ident.Name] = append(usedSelectors[ident.Name], sel.Sel.Name)
		}
		return true
	})
	return imports, file.Name.Name, usedSelectors, nil
}

func readGoModulePath(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], "\"`")
		}
	}
	return ""
}

// buildGoImportEdges resolves each file's import paths to a target
// directory, then narrows the edge to the specific non-test file(s) in that
// directory that declare a symbol actually referenced via the package's
// identifier in the importing file's body (see goImportTargets) — falling
// back to every non-test file in the directory when no usage information
// is available (a blank/dot import, an aliased import, or a parse
// failure), so the precision narrowing is strictly additive: it can only
// shrink an edge set from what the previous file-level resolution
// produced, never lose an edge for an import that's genuinely used but
// whose usage this best-effort analysis couldn't pin down.
func buildGoImportEdges(importsByFile map[string][]string, packages map[string]string, selectorsByFile map[string]map[string][]string, nodes map[NodeID]Node, modulePath string) []Edge {
	if len(importsByFile) == 0 {
		return nil
	}
	pkgToDirs := map[string][]string{}
	for dir, pkg := range packages {
		if pkg == "" || pkg == "main" {
			continue
		}
		pkgToDirs[pkg] = append(pkgToDirs[pkg], dir)
	}

	// Precompute once (not per import): each directory's non-test file IDs,
	// and each directory's exported-symbol-name -> declaring-file-IDs map.
	dirFiles := map[string][]NodeID{}
	fileDir := map[NodeID]string{}
	for id, node := range nodes {
		if node.Kind != NodeFile || strings.HasSuffix(node.RelPath, "_test.go") {
			continue
		}
		dir := parentRel(node.RelPath)
		dirFiles[dir] = append(dirFiles[dir], id)
		fileDir[id] = dir
	}
	symbolFiles := map[string]map[string][]NodeID{}
	for _, node := range nodes {
		if node.Kind != NodeSymbol {
			continue
		}
		dir, ok := fileDir[node.Parent]
		if !ok {
			continue
		}
		if symbolFiles[dir] == nil {
			symbolFiles[dir] = map[string][]NodeID{}
		}
		symbolFiles[dir][node.Name] = append(symbolFiles[dir][node.Name], node.Parent)
	}

	var edges []Edge
	seen := map[string]bool{}
	for rel, imports := range importsByFile {
		from := fileID(rel)
		fromDir := parentRel(rel)
		selectors := selectorsByFile[rel]
		for _, imp := range imports {
			targetDir := resolveGoImportDir(imp, fromDir, pkgToDirs, modulePath)
			if targetDir == "" {
				continue
			}
			for _, id := range goImportTargets(targetDir, packages[targetDir], selectors, dirFiles, symbolFiles) {
				if id == from {
					continue
				}
				key := string(from) + "\x00" + string(id) + "\x00" + imp
				if seen[key] {
					continue
				}
				seen[key] = true
				edges = append(edges, Edge{From: from, To: id, Kind: EdgeImports, Meta: imp})
			}
		}
	}
	return edges
}

// goImportTargets narrows an import's edge targets to the files in
// targetDir that declare a symbol actually referenced via targetPkg (the
// target directory's own package name — the identifier an unaliased import
// uses in code) in the importing file's selector usages. It falls back to
// every non-test file in targetDir when there's no usage information for
// that identifier (nothing selected off it — a blank/dot import or an
// aliased import whose alias differs from targetPkg, which this best-effort
// analysis doesn't track) or when the referenced names don't match any
// known symbol in the package (e.g. a struct method rather than a
// package-level declaration) — either way, recall never regresses below
// what file-level resolution already provided.
func goImportTargets(targetDir, targetPkg string, selectors map[string][]string, dirFiles map[string][]NodeID, symbolFiles map[string]map[string][]NodeID) []NodeID {
	all := dirFiles[targetDir]
	usedSymbols := selectors[targetPkg]
	if len(usedSymbols) == 0 {
		return all
	}
	bySymbol := symbolFiles[targetDir]
	seen := map[NodeID]bool{}
	var matched []NodeID
	for _, sym := range usedSymbols {
		for _, id := range bySymbol[sym] {
			if !seen[id] {
				seen[id] = true
				matched = append(matched, id)
			}
		}
	}
	if len(matched) == 0 {
		return all
	}
	return matched
}

func resolveGoImportDir(imp, fromDir string, pkgToDirs map[string][]string, modulePath string) string {
	if strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../") {
		return filepath.ToSlash(filepath.Clean(filepath.Join(fromDir, imp)))
	}
	if modulePath != "" && imp == modulePath {
		return "."
	}
	if modulePath != "" && strings.HasPrefix(imp, modulePath+"/") {
		return filepath.ToSlash(strings.TrimPrefix(imp, modulePath+"/"))
	}
	base := filepath.Base(imp)
	candidates := pkgToDirs[base]
	if len(candidates) == 1 {
		return candidates[0]
	}
	for _, dir := range candidates {
		if strings.HasSuffix(imp, "/"+filepath.Base(dir)) || strings.HasSuffix(imp, filepath.ToSlash(dir)) {
			return dir
		}
	}
	return ""
}

func fromLSPRange(r lsp.TextRange, loc lsp.Location) Range {
	if r.Start == (lsp.Position{}) && r.End == (lsp.Position{}) {
		r.Start = lsp.Position{Line: loc.Line, Character: loc.Character}
		r.End = r.Start
	}
	return Range{Start: Position{Line: r.Start.Line, Character: r.Start.Character}, End: Position{Line: r.End.Line, Character: r.End.Character}}
}

func containerDefault(container, fallback string) string {
	if strings.TrimSpace(container) != "" {
		return container
	}
	return fallback
}

func isExported(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
}
