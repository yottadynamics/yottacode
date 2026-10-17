package codemap

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

func buildTSImportEdges(importsByFile map[string][]string, nodes map[NodeID]Node, configs []tsConfig) []Edge {
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
			target := resolveTSImportFile(spec, fromDir, fileSet, configs)
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

// resolveTSImportFile resolves relative specifiers ("./x", "../x") by file
// path, and bare specifiers through the nearest tsconfig/jsconfig `paths` and
// `baseUrl` aliases. Bare specifiers no alias claims are external packages and
// stay unresolved.
func resolveTSImportFile(spec, fromDir string, fileSet map[string]bool, configs []tsConfig) string {
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return tsResolveBase(filepath.ToSlash(filepath.Clean(filepath.Join(fromDir, spec))), fileSet)
	}
	for _, cfg := range configs {
		if !pathWithin(cfg.dir, fromDir) {
			continue
		}
		for _, target := range cfg.expand(spec) {
			if resolved := tsResolveBase(target, fileSet); resolved != "" {
				return resolved
			}
		}
		// The nearest config owns the file; do not fall through to an outer one.
		return ""
	}
	return ""
}

// tsResolveBase tries a module path as an exact file, with each known extension,
// then as a directory index.
func tsResolveBase(base string, fileSet map[string]bool) string {
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

// pathWithin reports whether dir is root or lies under it (both repo-relative).
func pathWithin(root, dir string) bool {
	if root == "." || root == "" {
		return true
	}
	return dir == root || strings.HasPrefix(dir, root+"/")
}

// tsConfig is the alias-relevant part of one tsconfig.json / jsconfig.json.
type tsConfig struct {
	dir      string // repo-relative directory holding the config
	baseURL  string // repo-relative base directory, or "" when unset
	pathsDir string // repo-relative directory of the config that defines paths
	paths    []tsPathAlias
}

type tsPathAlias struct {
	prefix, suffix string // pattern split around its single "*"; no star means exact
	wildcard       bool
	targets        []string
}

// expand maps a bare specifier to candidate repo-relative module paths, longest
// matching alias first, then the baseUrl fallback.
func (c tsConfig) expand(spec string) []string {
	var out []string
	best := -1
	var bestAlias tsPathAlias
	var captured string
	for _, a := range c.paths {
		switch {
		case !a.wildcard && spec == a.prefix:
			best, bestAlias, captured = len(a.prefix)+1<<20, a, ""
		case a.wildcard && len(spec) >= len(a.prefix)+len(a.suffix) && strings.HasPrefix(spec, a.prefix) && strings.HasSuffix(spec, a.suffix) && len(a.prefix) > best:
			best, bestAlias, captured = len(a.prefix), a, spec[len(a.prefix):len(spec)-len(a.suffix)]
		}
	}
	if best >= 0 {
		for _, t := range bestAlias.targets {
			out = append(out, filepath.ToSlash(filepath.Clean(filepath.Join(c.baseOrDir(), strings.Replace(t, "*", captured, 1)))))
		}
	}
	if c.baseURL != "" {
		out = append(out, filepath.ToSlash(filepath.Clean(filepath.Join(c.baseURL, spec))))
	}
	return out
}

// baseOrDir is what `paths` targets are relative to: baseUrl when any config in
// the chain sets it, otherwise the directory of the config that defines paths.
func (c tsConfig) baseOrDir() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	if c.pathsDir != "" {
		return c.pathsDir
	}
	return c.dir
}

// maxTSExtendsDepth bounds an `extends` chain, which also stops a cycle.
const maxTSExtendsDepth = 8

// tsOptions is the alias-relevant compilerOptions after following `extends`.
type tsOptions struct {
	baseURL  string // repo-relative, from the nearest config in the chain that sets it
	paths    map[string][]string
	pathsDir string // repo-relative directory of the config that set paths
}

// readTSOptions parses one config and layers it over its `extends` parent, as
// the TypeScript compiler does: a child's baseUrl and paths replace the parent's
// wholesale, each resolved against the config that defines it. Only relative
// `extends` paths inside the repository are followed; package-name extends
// (`@tsconfig/node20`) and unreadable parents are ignored.
func readTSOptions(absRoot, rel string, depth int) (tsOptions, bool) {
	data, err := os.ReadFile(filepath.Join(absRoot, filepath.FromSlash(rel)))
	if err != nil {
		return tsOptions{}, false
	}
	var raw struct {
		Extends         string `json:"extends"`
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(stripJSONC(data), &raw) != nil {
		return tsOptions{}, false
	}
	var opts tsOptions
	dir := parentRel(rel)
	if ext := raw.Extends; depth < maxTSExtendsDepth && (strings.HasPrefix(ext, "./") || strings.HasPrefix(ext, "../")) {
		parent := filepath.ToSlash(filepath.Clean(filepath.Join(dir, ext)))
		if filepath.Ext(parent) != ".json" {
			parent += ".json"
		}
		if !strings.HasPrefix(parent, "../") {
			if inherited, ok := readTSOptions(absRoot, parent, depth+1); ok {
				opts = inherited
			}
		}
	}
	if raw.CompilerOptions.BaseURL != "" {
		opts.baseURL = filepath.ToSlash(filepath.Clean(filepath.Join(dir, raw.CompilerOptions.BaseURL)))
	}
	if raw.CompilerOptions.Paths != nil {
		opts.paths, opts.pathsDir = raw.CompilerOptions.Paths, dir
	}
	return opts, true
}

// loadTSConfigs parses every collected tsconfig/jsconfig. Unreadable or invalid
// files are skipped. Configs are returned deepest directory first so the
// nearest one wins.
func loadTSConfigs(absRoot string, rels []string) []tsConfig {
	var out []tsConfig
	for _, rel := range rels {
		opts, ok := readTSOptions(absRoot, rel, 0)
		if !ok {
			continue
		}
		cfg := tsConfig{dir: parentRel(rel), baseURL: opts.baseURL, pathsDir: opts.pathsDir}
		keys := make([]string, 0, len(opts.paths))
		for k := range opts.paths {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			a := tsPathAlias{targets: opts.paths[k]}
			if i := strings.Index(k, "*"); i >= 0 {
				a.wildcard, a.prefix, a.suffix = true, k[:i], k[i+1:]
			} else {
				a.prefix = k
			}
			cfg.paths = append(cfg.paths, a)
		}
		out = append(out, cfg)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].dir) > len(out[j].dir) })
	return out
}

// stripJSONC removes // and /* */ comments and trailing commas so tsconfig files,
// which are JSON with comments, decode with encoding/json. String contents are
// left untouched.
func stripJSONC(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inStr := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case inStr:
			out = append(out, c)
			if c == '\\' && i+1 < len(in) {
				i++
				out = append(out, in[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return trailingComma.ReplaceAll(out, []byte("$1"))
}

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

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
			// `from pkg import name` may name a submodule, so offer pkg.name as
			// well; names that are not modules simply do not resolve.
			specs = append(specs, module)
			for _, name := range splitPythonImportNames(names) {
				if isPythonIdent(name) {
					specs = append(specs, module+"."+name)
				}
			}
		}
	}
	return specs, s.Err()
}

func isPythonIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
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
	suffixes := pythonSuffixIndex(relToID)
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
			if !ok && !strings.HasPrefix(spec, ".") {
				to, ok = resolvePythonBySuffix(dir, fromDir, suffixes, relToID)
			}
			if !ok || to == from {
				continue
			}
			addEdgeOnce(&edges, seen, from, to, spec)
		}
	}
	return edges
}

// pythonSuffixIndex maps every trailing path of each Python module ("a/b/c",
// "b/c", "c" for a/b/c.py or a/b/c/__init__.py) to the files it could name. It
// is how absolute imports resolve in src/ and namespaced layouts, where the
// import root is a subdirectory rather than the repository root.
func pythonSuffixIndex(relToID map[string]NodeID) map[string][]string {
	out := map[string][]string{}
	for rel := range relToID {
		if !strings.HasSuffix(rel, ".py") {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(rel, ".py"), "/")
		if parts[len(parts)-1] == "__init__" {
			parts = parts[:len(parts)-1]
		}
		for i := range parts {
			key := strings.Join(parts[i:], "/")
			out[key] = append(out[key], rel)
		}
	}
	return out
}

// resolvePythonBySuffix resolves an absolute import path whose root-relative form
// did not exist. Several matches are broken by closeness to the importing file;
// a tie is left unresolved rather than guessed. A one-segment name ("json",
// "logging") only resolves to a module in the importer's own directory chain,
// since matching it anywhere would turn standard-library imports into edges.
func resolvePythonBySuffix(dir, fromDir string, suffixes map[string][]string, relToID map[string]NodeID) (NodeID, bool) {
	var best string
	bestScore, tied := -1, false
	for _, rel := range suffixes[dir] {
		modDir := parentRel(rel)
		if strings.HasSuffix(rel, "/__init__.py") || rel == "__init__.py" {
			modDir = parentRel(modDir)
		}
		if !strings.Contains(dir, "/") && !pathWithin(modDir, fromDir) {
			continue
		}
		score := commonDirDepth(modDir, fromDir)
		switch {
		case score > bestScore:
			best, bestScore, tied = rel, score, false
		case score == bestScore:
			tied = true
		}
	}
	if best == "" || tied {
		return "", false
	}
	id, ok := relToID[best]
	return id, ok
}

func commonDirDepth(a, b string) int {
	if a == "." || b == "." {
		return 0
	}
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
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
