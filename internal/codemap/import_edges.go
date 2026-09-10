package codemap

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file extends import-edge extraction beyond Go to TypeScript/JavaScript,
// Python, and Rust. Each language gets a conservative line-scan parser
// (mirroring parseGoImports) and a resolver that only creates an edge for
// specifiers it can confidently map to an indexed in-repo file — bare/package
// specifiers that don't resolve are simply skipped, exactly like Go's
// resolveGoImportDir returning "".

// --- TypeScript / JavaScript ---

var (
	tsFromImportRe = regexp.MustCompile(`from\s+['"]([^'"]+)['"]`)
	tsRequireRe    = regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`)
	tsBareImportRe = regexp.MustCompile(`^\s*import\s+['"]([^'"]+)['"]`)
)

var tsResolveExts = []string{".ts", ".tsx", ".d.ts", ".js", ".jsx"}

func parseTSImports(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var specs []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			specs = append(specs, s)
		}
	}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if m := tsFromImportRe.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := tsRequireRe.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := tsBareImportRe.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
	}
	return specs, s.Err()
}

func buildTSImportEdges(importsByFile map[string][]string, nodes map[NodeID]Node) []Edge {
	if len(importsByFile) == 0 {
		return nil
	}
	fileSet, relToID := fileLookups(nodes)
	var edges []Edge
	seen := map[string]bool{}
	for rel, specs := range importsByFile {
		from := fileID(rel)
		fromDir := parentRel(rel)
		for _, spec := range specs {
			target := resolveTSImportFile(spec, fromDir, fileSet)
			if target == "" {
				continue
			}
			to, ok := relToID[target]
			if !ok || to == from {
				continue
			}
			addEdgeOnce(&edges, seen, from, to, spec)
		}
	}
	return edges
}

// resolveTSImportFile only resolves relative specifiers ("./x", "../x") —
// TS module resolution for relative paths is file-based, unlike Go's
// package-based resolution. Bare/package specifiers are external.
func resolveTSImportFile(spec, fromDir string, fileSet map[string]bool) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return ""
	}
	base := filepath.ToSlash(filepath.Clean(filepath.Join(fromDir, spec)))
	if fileSet[base] {
		return base
	}
	for _, ext := range tsResolveExts {
		if fileSet[base+ext] {
			return base + ext
		}
	}
	for _, ext := range tsResolveExts {
		if fileSet[base+"/index"+ext] {
			return base + "/index" + ext
		}
	}
	return ""
}

// --- Python ---

var (
	pyImportRe     = regexp.MustCompile(`^\s*import\s+([A-Za-z_][\w.]*)`)
	pyFromImportRe = regexp.MustCompile(`^\s*from\s+(\.*[A-Za-z_][\w.]*|\.+)\s+import\s+(.+)`)
)

// parsePythonImports collects module-path specs for `import x.y` and
// `from x.y import ...`. For a purely-relative `from . import name` /
// `from .. import name` form — the common way to reference a submodule —
// each imported name is folded into a per-name relative spec (e.g. `.name`)
// so resolvePythonImportDir's existing dots-plus-suffix handling resolves
// them as submodules, rather than only ever landing on the package's
// __init__.py. A parenthesized name list left open at end of line (common
// in `__init__.py` re-export blocks: `from . import (\n    a,\n    b,\n)`)
// is accumulated across lines until its closing paren, so this multi-line
// form resolves the same as the single-line one.
func parsePythonImports(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var specs []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if m := pyImportRe.FindStringSubmatch(line); m != nil {
			specs = append(specs, m[1])
		}
		m := pyFromImportRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		module, names := m[1], m[2]
		if strings.Contains(names, "(") && !strings.Contains(names, ")") {
			for s.Scan() {
				cont := s.Text()
				names += "\n" + cont
				if strings.Contains(cont, ")") {
					break
				}
			}
		}
		if isDotsOnly(module) {
			for _, name := range splitPythonImportNames(names) {
				specs = append(specs, module+name)
			}
		} else {
			specs = append(specs, module)
		}
	}
	return specs, s.Err()
}

func isDotsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '.' {
			return false
		}
	}
	return true
}

func splitPythonImportNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.TrimSuffix(raw, ")")
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if idx := strings.Index(part, " as "); idx >= 0 {
			part = strings.TrimSpace(part[:idx])
		}
		if part == "" || part == "*" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func buildPythonImportEdges(importsByFile map[string][]string, nodes map[NodeID]Node) []Edge {
	if len(importsByFile) == 0 {
		return nil
	}
	_, relToID := fileLookups(nodes)
	resolve := func(dir string) (NodeID, bool) {
		dir = filepath.ToSlash(filepath.Clean(dir))
		if dir == "." {
			if id, ok := relToID["__init__.py"]; ok {
				return id, true
			}
			return "", false
		}
		if id, ok := relToID[dir+".py"]; ok {
			return id, true
		}
		if id, ok := relToID[dir+"/__init__.py"]; ok {
			return id, true
		}
		return "", false
	}
	var edges []Edge
	seen := map[string]bool{}
	for rel, specs := range importsByFile {
		from := fileID(rel)
		fromDir := parentRel(rel)
		for _, spec := range specs {
			dir := resolvePythonImportDir(spec, fromDir)
			if dir == "" {
				continue
			}
			to, ok := resolve(dir)
			if !ok || to == from {
				continue
			}
			addEdgeOnce(&edges, seen, from, to, spec)
		}
	}
	return edges
}

// resolvePythonImportDir maps an import spec to a repo-relative module path
// (dots become slashes; the caller tries both "<path>.py" and
// "<path>/__init__.py"). Absolute specifiers are resolved relative to the
// repo root — the common layout when a project has no separate src/ prefix.
// Relative specifiers use dot-count semantics: one leading dot means the
// current package, each additional dot walks up one directory.
func resolvePythonImportDir(spec, fromDir string) string {
	if !strings.HasPrefix(spec, ".") {
		return strings.ReplaceAll(spec, ".", "/")
	}
	dots := 0
	for dots < len(spec) && spec[dots] == '.' {
		dots++
	}
	rest := spec[dots:]
	base := fromDir
	for i := 0; i < dots-1; i++ {
		base = parentRel(base)
	}
	if rest == "" {
		return base
	}
	sub := strings.ReplaceAll(rest, ".", "/")
	if base == "." {
		return sub
	}
	return base + "/" + sub
}

// --- Rust ---

var (
	rustUseRe = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?use\s+((?:crate|self|super)(?:::\w+)*)`)
	rustModRe = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?mod\s+(\w+)\s*;`)
)

func parseRustImports(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var specs []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if m := rustUseRe.FindStringSubmatch(line); m != nil {
			specs = append(specs, m[1])
		}
		if m := rustModRe.FindStringSubmatch(line); m != nil {
			specs = append(specs, "mod:"+m[1])
		}
	}
	return specs, s.Err()
}

// buildRustImportEdges resolves `mod x;` and `use crate::`/`self::`/`super::`
// paths against the crate's actual module tree, in two passes:
//
//  1. `mod` declarations are unambiguous (a name always names a direct
//     submodule of the declaring file — see rustSubmoduleBaseDir for the
//     leaf-file-vs-root-file distinction that makes this so) and double as
//     the declared-by graph pass 2 needs to answer "what is this file's
//     parent module" for `super::`.
//  2. `use` paths are walked one module segment at a time from the
//     appropriate starting point (see resolveRustUsePath), so `super::top`
//     resolving to an item defined directly in the parent file — not a
//     further submodule — works the same as a deeper nested path.
func buildRustImportEdges(importsByFile map[string][]string, nodes map[NodeID]Node) []Edge {
	if len(importsByFile) == 0 {
		return nil
	}
	fileSet, relToID := fileLookups(nodes)
	var edges []Edge
	seen := map[string]bool{}

	declaredBy := map[string]string{} // child rel -> declaring (parent) rel
	for rel, specs := range importsByFile {
		from := fileID(rel)
		for _, spec := range specs {
			name, ok := strings.CutPrefix(spec, "mod:")
			if !ok {
				continue
			}
			target := resolveRustModulePath(rustSubmoduleBaseDir(rel), []string{name}, fileSet, "")
			if target == "" || target == rel {
				continue
			}
			declaredBy[target] = rel
			if to, ok := relToID[target]; ok && to != from {
				addEdgeOnce(&edges, seen, from, to, spec)
			}
		}
	}

	for rel, specs := range importsByFile {
		from := fileID(rel)
		for _, spec := range specs {
			if strings.HasPrefix(spec, "mod:") {
				continue
			}
			target := resolveRustUsePath(spec, rel, declaredBy, fileSet)
			if target == "" || target == rel {
				continue
			}
			to, ok := relToID[target]
			if !ok || to == from {
				continue
			}
			addEdgeOnce(&edges, seen, from, to, spec)
		}
	}
	return edges
}

// resolveRustUsePath resolves a crate::/self::/super:: use spec (as parsed
// by parseRustImports) to a repo-relative file, or "" if it can't be
// resolved confidently. declaredBy maps a file to whichever file declared it
// as a submodule via `mod x;` — built in buildRustImportEdges's first pass.
func resolveRustUsePath(spec, fromRel string, declaredBy map[string]string, fileSet map[string]bool) string {
	var baseDir, ownerFile, rest string
	switch {
	case strings.HasPrefix(spec, "self::"):
		baseDir = rustSubmoduleBaseDir(fromRel)
		ownerFile = fromRel
		rest = strings.TrimPrefix(spec, "self::")
	case strings.HasPrefix(spec, "super::"):
		parent, ok := declaredBy[fromRel]
		if !ok {
			return "" // no known declaring parent, e.g. the crate root itself
		}
		baseDir = rustSubmoduleBaseDir(parent)
		ownerFile = parent
		rest = strings.TrimPrefix(spec, "super::")
	case strings.HasPrefix(spec, "crate::"):
		root := rustCrateRoot(fromRel)
		if root == "" {
			return ""
		}
		baseDir = root
		rest = strings.TrimPrefix(spec, "crate::")
	default:
		return ""
	}
	return resolveRustModulePath(baseDir, strings.Split(rest, "::"), fileSet, ownerFile)
}

// resolveRustModulePath walks a module path one "::" segment at a time
// starting from baseDir, descending into a submodule file whenever one
// exists there. The first segment that doesn't name a submodule — and
// everything after it — is treated as an item inside the last-confirmed
// module file rather than a further nested module, so the walk stops and
// returns that file. ownerFile is the starting point's own file (used when
// the very first segment already fails to resolve, meaning the whole path
// names an item directly in that starting module); it is "" for crate::,
// which has no single file owning the crate root itself.
func resolveRustModulePath(baseDir string, segments []string, fileSet map[string]bool, ownerFile string) string {
	owner := ownerFile
	for _, seg := range segments {
		next := seg
		if baseDir != "" && baseDir != "." {
			next = baseDir + "/" + seg
		}
		switch {
		case fileSet[next+".rs"]:
			owner = next + ".rs"
			baseDir = rustSubmoduleBaseDir(owner)
		case fileSet[next+"/mod.rs"]:
			owner = next + "/mod.rs"
			baseDir = next // mod.rs is root-like for its own directory
		default:
			return owner
		}
	}
	return owner
}

// rustSubmoduleBaseDir returns the directory a Rust file's OWN submodules
// (declared via `mod x;` inside it, or referenced via `self::x`) live under.
// lib.rs, main.rs, and mod.rs are root-like: their submodules live in their
// own directory. Any other named file "foo.rs" is a leaf: per Rust 2018+
// module resolution, its submodules live in a same-named sibling directory
// "foo/", not alongside foo.rs itself.
func rustSubmoduleBaseDir(rel string) string {
	dir := parentRel(rel)
	stem := strings.TrimSuffix(filepath.Base(rel), ".rs")
	if stem == "lib" || stem == "main" || stem == "mod" {
		return dir
	}
	if dir == "." {
		return stem
	}
	return dir + "/" + stem
}

func rustCrateRoot(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if p == "src" {
			return strings.Join(parts[:i+1], "/")
		}
	}
	return ""
}

// --- shared helpers ---

func fileLookups(nodes map[NodeID]Node) (fileSet map[string]bool, relToID map[string]NodeID) {
	fileSet = map[string]bool{}
	relToID = map[string]NodeID{}
	for id, n := range nodes {
		if n.Kind == NodeFile {
			fileSet[n.RelPath] = true
			relToID[n.RelPath] = id
		}
	}
	return fileSet, relToID
}

func addEdgeOnce(edges *[]Edge, seen map[string]bool, from, to NodeID, meta string) {
	key := string(from) + "\x00" + string(to) + "\x00" + meta
	if seen[key] {
		return
	}
	seen[key] = true
	*edges = append(*edges, Edge{From: from, To: to, Kind: EdgeImports, Meta: meta})
}
