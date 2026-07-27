package codemap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
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
}

// Build walks the workspace and returns an immutable structure snapshot.
func Build(ctx context.Context, opts BuildOptions) (*CodeIndex, error) {
	root := opts.Root
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = defaultMaxFiles
	}
	modulePath := readGoModulePath(filepath.Join(absRoot, "go.mod"))
	source := opts.Source
	if source == nil {
		source = FallbackSource{}
	}

	rootID := NodeID("dir:.")
	nodes := map[NodeID]Node{
		rootID: {ID: rootID, Kind: NodeDirectory, Name: filepath.Base(absRoot), Path: absRoot, RelPath: "."},
	}
	children := map[NodeID][]NodeID{}
	goImports := map[string][]string{}
	goPackages := map[string]string{}
	seenFiles := 0

	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == absRoot {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if shouldSkipDir(name) {
				return filepath.SkipDir
			}
			rel := relPath(absRoot, path)
			id := dirID(rel)
			parent := dirID(parentRel(rel))
			nodes[id] = Node{ID: id, Parent: parent, Kind: NodeDirectory, Name: name, Path: path, RelPath: rel}
			children[parent] = appendUnique(children[parent], id)
			return nil
		}
		seenFiles++
		if seenFiles > opts.MaxFiles {
			return filepath.SkipAll
		}
		lang, ok := lsp.ResolveFile(path)
		if !ok {
			return nil
		}
		rel := relPath(absRoot, path)
		parent := dirID(parentRel(rel))
		fileID := fileID(rel)
		loc, _ := countLOC(path)
		nodes[fileID] = Node{ID: fileID, Parent: parent, Kind: NodeFile, Name: filepath.Base(path), Path: path, RelPath: rel, Language: lang.ID, Stats: Stats{LOC: loc, Files: 1}}
		children[parent] = appendUnique(children[parent], fileID)
		if lang.ID == "go" {
			imports, pkg, err := parseGoImports(path)
			if err == nil {
				goImports[rel] = imports
				if pkg != "" {
					goPackages[parentRel(rel)] = pkg
				}
			}
		}

		syms, sourceName, err := source.Symbols(ctx, path)
		if err != nil && !errors.Is(err, lsp.ErrUnsupportedCapability) {
			return nil
		}
		for idx, sym := range syms {
			rng := fromLSPRange(sym.Range, sym.Location)
			sid := symbolID(rel, sym.Name, rng, idx)
			exported := isExported(sym.Name)
			node := Node{ID: sid, Parent: fileID, Kind: NodeSymbol, Name: sym.Name, Path: path, RelPath: rel, Language: lang.ID, Symbol: SymbolInfo{Kind: sym.Kind, Container: containerDefault(sym.Container, sourceName), Exported: exported, Range: rng}, Stats: Stats{Symbols: 1}}
			if exported {
				node.Stats.Exported = 1
			} else {
				node.Stats.Private = 1
			}
			nodes[sid] = node
			children[fileID] = append(children[fileID], sid)
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return nil, err
	}

	aggregateStats(rootID, nodes, children)
	edges := buildGoImportEdges(goImports, goPackages, nodes, modulePath)
	return NewIndex(absRoot, rootID, nodes, children, edges), nil
}

func aggregateStats(id NodeID, nodes map[NodeID]Node, children map[NodeID][]NodeID) Stats {
	n := nodes[id]
	stats := n.Stats
	for _, childID := range children[id] {
		childStats := aggregateStats(childID, nodes, children)
		stats.LOC += childStats.LOC
		stats.Files += childStats.Files
		stats.Symbols += childStats.Symbols
		stats.Exported += childStats.Exported
		stats.Private += childStats.Private
	}
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
	for _, existing := range in {
		if existing == id {
			return in
		}
	}
	return append(in, id)
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

func parseGoImports(path string) ([]string, string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, "", err
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		imports = append(imports, strings.Trim(spec.Path.Value, "\"`"))
	}
	return imports, file.Name.Name, nil
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

func buildGoImportEdges(importsByFile map[string][]string, packages map[string]string, nodes map[NodeID]Node, modulePath string) []Edge {
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
	var edges []Edge
	seen := map[string]bool{}
	for rel, imports := range importsByFile {
		from := fileID(rel)
		fromDir := parentRel(rel)
		for _, imp := range imports {
			targetDir := resolveGoImportDir(imp, fromDir, pkgToDirs, modulePath)
			if targetDir == "" {
				continue
			}
			for id, node := range nodes {
				if node.Kind != NodeFile || parentRel(node.RelPath) != targetDir || node.ID == from {
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
