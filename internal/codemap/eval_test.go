//go:build codemap_eval

// Evaluation harness for the Code Map import graph. It is excluded from normal
// builds and runs on demand:
//
//	go test -tags codemap_eval -run 'TestEval' -timeout 40m -v ./internal/codemap/
//
// Environment:
//
//	CODEMAP_EVAL_N     number of Go mutation cases (default 12)
//	CODEMAP_EVAL_OUT   path to write the markdown report (default: test log only)
//	CODEMAP_EVAL_EXT   external repo for the Python/TS agreement check
//
// The Go case uses mutation testing as ground truth: rename one exported
// function across its package so the package still compiles, then let the
// compiler report every file in OTHER packages that no longer type-checks.
// That set is the real "what breaks if I change X" answer. Two arms are scored
// against it: code_impact's direct dependents of the defining file, and a
// word-boundary text search for the identifier (the grep baseline an agent has
// without Code Map). LSP references would be exact and is not scored here.
package codemap

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type evalReport struct{ b strings.Builder }

func (r *evalReport) f(format string, args ...any) { fmt.Fprintf(&r.b, format, args...) }

func (r *evalReport) flush(t *testing.T) {
	t.Helper()
	t.Log("\n" + r.b.String())
	if out := os.Getenv("CODEMAP_EVAL_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(r.b.String()), 0o644); err != nil {
			t.Errorf("write report: %v", err)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// copyTracked copies git-tracked and untracked-but-not-ignored files into dst so
// mutations never touch the real checkout, and uncommitted work is still included.
func copyTracked(t *testing.T, src, dst string) {
	t.Helper()
	out, err := exec.Command("git", "-C", src, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(src, rel))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			continue
		}
		to := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, data, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}

type mutationCase struct {
	file, dir, name string
}

func hashKey(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// pickMutationCases chooses, deterministically, files that declare an exported
// top-level function whose name is also used in at least one other package.
func pickMutationCases(t *testing.T, root string, texts map[string]string, n int) []mutationCase {
	t.Helper()
	var files []string
	for rel := range texts {
		if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") {
			files = append(files, rel)
		}
	}
	sort.Slice(files, func(i, j int) bool { return hashKey(files[i]) < hashKey(files[j]) })
	var cases []mutationCase
	for _, rel := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), texts[rel], 0)
		if err != nil || f.Name.Name == "main" {
			continue
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !fd.Name.IsExported() || len(fd.Name.Name) < 6 {
				continue
			}
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(fd.Name.Name) + `\b`)
			external := false
			for other, text := range texts {
				if strings.HasSuffix(other, ".go") && filepath.Dir(other) != filepath.Dir(rel) && re.MatchString(text) {
					external = true
					break
				}
			}
			if external {
				cases = append(cases, mutationCase{file: rel, dir: filepath.Dir(rel), name: fd.Name.Name})
				break
			}
		}
		if len(cases) >= n {
			break
		}
	}
	return cases
}

var compileErrRe = regexp.MustCompile(`(?m)^(?:vet: )?(\S+\.go):\d+:\d+:`)

// breakingFiles renames name across its package, runs the type-checker, restores
// the files, and returns every file outside the package that fails to compile.
// ok is false when the mutation itself broke the defining package.
func breakingFiles(t *testing.T, work string, c mutationCase) (broken map[string]bool, ok bool) {
	t.Helper()
	renamed := c.name + "EvalRenamed"
	re := regexp.MustCompile(`(^|[^.\w])` + regexp.QuoteMeta(c.name) + `\b`)
	entries, err := os.ReadDir(filepath.Join(work, c.dir))
	if err != nil {
		t.Fatal(err)
	}
	originals := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		p := filepath.Join(work, c.dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		originals[p] = data
		if err := os.WriteFile(p, re.ReplaceAll(data, []byte("${1}"+renamed)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for p, data := range originals {
			_ = os.WriteFile(p, data, 0o644)
		}
	}()

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = work
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run()

	broken = map[string]bool{}
	for _, m := range compileErrRe.FindAllStringSubmatch(out.String(), -1) {
		rel := strings.TrimPrefix(filepath.ToSlash(m[1]), "./")
		if filepath.IsAbs(rel) {
			if r, err := filepath.Rel(work, rel); err == nil {
				rel = filepath.ToSlash(r)
			}
		}
		if filepath.Dir(rel) == c.dir {
			return nil, false
		}
		broken[rel] = true
	}
	return broken, true
}

func setOf(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func overlap(a, truth map[string]bool) int {
	n := 0
	for k := range truth {
		if a[k] {
			n++
		}
	}
	return n
}

func TestEvalGoBlastRadius(t *testing.T) {
	n := 12
	if v, err := strconv.Atoi(os.Getenv("CODEMAP_EVAL_N")); err == nil && v > 0 {
		n = v
	}
	src := repoRoot(t)
	work := t.TempDir()
	copyTracked(t, src, work)

	// A mutation result is only meaningful against a clean baseline.
	base := exec.Command("go", "vet", "./...")
	base.Dir = work
	if out, err := base.CombinedOutput(); err != nil {
		t.Fatalf("baseline go vet failed in the copied tree, so mutation results would be meaningless:\n%s", out)
	}

	texts := map[string]string{}
	_ = filepath.WalkDir(work, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
			if data, rerr := os.ReadFile(p); rerr == nil {
				rel, _ := filepath.Rel(work, p)
				texts[filepath.ToSlash(rel)] = string(data)
			}
		}
		return nil
	})

	start := time.Now()
	idx, err := Build(context.Background(), BuildOptions{Root: work, MaxFiles: 100000, MaxDuration: 5 * time.Minute})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cold := time.Since(start)
	start = time.Now()
	if _, err := Build(context.Background(), BuildOptions{Root: work, MaxFiles: 100000, MaxDuration: 5 * time.Minute}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	warm := time.Since(start)

	var rep evalReport
	rep.f("# Code Map eval: Go blast radius\n\n")
	rep.f("Corpus: %s (%d tracked Go files). Index: %s. Cold build %s, repeat build %s.\n\n", filepath.Base(src), len(texts), idx.Info(), cold.Round(time.Millisecond), warm.Round(time.Millisecond))
	rep.f("Ground truth: files outside the defining package that fail `go vet` after renaming one exported function across its package. code_impact arm = direct dependents of the defining file; grep arm = files outside the package matching the identifier as a whole word.\n\n")
	rep.f("| function | file | truth | impact found | impact listed | grep found | grep listed |\n|---|---|---|---|---|---|---|\n")

	rdeps := reverseDeps(t, work)

	var sumTruth, sumImpactHit, sumImpactList, sumGrepHit, sumGrepList, used int
	var impactTransitive, sumPkgTruth, sumPkgHit, sumPkgListed int
	for _, c := range pickMutationCases(t, work, texts, n*2) {
		if used >= n {
			break
		}
		truth, ok := breakingFiles(t, work, c)
		if !ok || len(truth) == 0 {
			continue
		}
		used++

		var impact []string
		for _, d := range idx.Dependents(c.file, 100000) {
			impact = append(impact, d.RelPath)
		}
		res := idx.Impact(c.file, MaxDepthAll, 100000)
		impactTransitive += len(withoutDirect(res))
		predictedPkgs := map[string]bool{}
		for _, d := range append(append([]Node(nil), res.DirectDependents...), res.TransitiveDependents...) {
			predictedPkgs[filepath.ToSlash(filepath.Dir(d.RelPath))] = true
		}
		truthPkgs := rdeps[c.dir]
		sumPkgTruth += len(truthPkgs)
		sumPkgHit += overlap(predictedPkgs, truthPkgs)
		sumPkgListed += len(predictedPkgs)

		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(c.name) + `\b`)
		var grep []string
		for rel, text := range texts {
			if filepath.Dir(rel) != c.dir && re.MatchString(text) {
				grep = append(grep, rel)
			}
		}
		ih, gh := overlap(setOf(impact), truth), overlap(setOf(grep), truth)
		rep.f("| %s | %s | %d | %d | %d | %d | %d |\n", c.name, c.file, len(truth), ih, len(impact), gh, len(grep))
		sumTruth += len(truth)
		sumImpactHit += ih
		sumImpactList += len(impact)
		sumGrepHit += gh
		sumGrepList += len(grep)
	}
	if used == 0 {
		t.Fatal("no valid mutation cases")
	}
	pct := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return 100 * float64(a) / float64(b)
	}
	rep.f("\n%d cases. Recall of breaking files: code_impact %.0f%% (%d/%d), grep %.0f%% (%d/%d). ", used, pct(sumImpactHit, sumTruth), sumImpactHit, sumTruth, pct(sumGrepHit, sumTruth), sumGrepHit, sumTruth)
	rep.f("Files an agent would have to inspect per true breakage: code_impact %.1f, grep %.1f. ", float64(sumImpactList)/float64(max(sumImpactHit, 1)), float64(sumGrepList)/float64(max(sumGrepHit, 1)))
	rep.f("Mean indirect dependents code_impact adds beyond direct ones: %.1f (grep cannot produce these).\n", float64(impactTransitive)/float64(used))
	rep.f("\nTransitive packages (ground truth: `go list` reverse dependencies of the defining package, an upper bound because it ignores which file of the package is touched): code_impact covers %.0f%% (%d/%d) while listing %d packages.\n", pct(sumPkgHit, sumPkgTruth), sumPkgHit, sumPkgTruth, sumPkgListed)
	rep.flush(t)

	if r := pct(sumImpactHit, sumTruth); r < 50 {
		t.Errorf("code_impact recall %.0f%% is below the 50%% floor", r)
	}
}

const pyImportResolver = `
import ast, json, os, sys
root = sys.argv[1]
skip = {"node_modules", "__pycache__", "build", "dist", "target", "vendor", "coverage"}
files = []
for dp, dn, fn in os.walk(root):
    dn[:] = [d for d in dn if d not in skip and not d.startswith(".")]
    for f in fn:
        if f.endswith(".py"):
            files.append(os.path.relpath(os.path.join(dp, f), root).replace(os.sep, "/"))
fileset = set(files)
suffix = {}
for f in files:
    parts = f[:-3].split("/")
    if parts[-1] == "__init__":
        parts = parts[:-1]
    for i in range(len(parts)):
        suffix.setdefault(".".join(parts[i:]), set()).add(f)
def resolve_abs(mod):
    c = suffix.get(mod)
    return next(iter(c)) if c and len(c) == 1 else None
edges = set()
for f in files:
    try:
        tree = ast.parse(open(os.path.join(root, f), encoding="utf-8", errors="replace").read())
    except Exception:
        continue
    pkg = f.rsplit("/", 1)[0].split("/") if "/" in f else []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for a in node.names:
                t = resolve_abs(a.name)
                if t and t != f:
                    edges.add((f, t))
        elif isinstance(node, ast.ImportFrom):
            if node.level:
                base = pkg[: len(pkg) - (node.level - 1)] if node.level > 1 else pkg
                mod = ".".join(base + ([node.module] if node.module else []))
                cands = [mod] + [mod + "." + a.name for a in node.names]
                for c in cands:
                    cand = c.replace(".", "/")
                    for t in (cand + ".py", cand + "/__init__.py"):
                        if t in fileset and t != f:
                            edges.add((f, t))
            elif node.module:
                for c in [node.module] + [node.module + "." + a.name for a in node.names]:
                    t = resolve_abs(c)
                    if t and t != f:
                        edges.add((f, t))
print(json.dumps(sorted(edges)))
`

// TestEvalPythonImportAgreement scores the Python import edges against an
// independent ast-based resolver. The reference is itself approximate (it
// resolves dotted names by unique path suffix), so this measures agreement, not
// truth; a large disagreement still localizes bugs worth reading.
func TestEvalPythonImportAgreement(t *testing.T) {
	ext := os.Getenv("CODEMAP_EVAL_EXT")
	if ext == "" {
		ext = filepath.Join(os.Getenv("HOME"), "repos", "langflow")
	}
	if _, err := os.Stat(ext); err != nil {
		t.Skipf("external repo %s not present", ext)
	}
	start := time.Now()
	idx, err := Build(context.Background(), BuildOptions{Root: ext, MaxFiles: 100000, MaxDuration: 5 * time.Minute})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cold := time.Since(start)

	out, err := exec.Command("python3", "-I", "-c", pyImportResolver, ext).Output()
	if err != nil {
		t.Skipf("python reference resolver unavailable: %v", err)
	}
	var pairs [][2]string
	if err := jsonUnmarshal(out, &pairs); err != nil {
		t.Fatalf("decode reference edges: %v", err)
	}
	ref := map[[2]string]bool{}
	for _, p := range pairs {
		ref[p] = true
	}
	ours := map[[2]string]bool{}
	for _, e := range idx.Edges(EdgeImports) {
		from, to := idx.nodes[e.From], idx.nodes[e.To]
		if from.Language == "python" && to.Language == "python" {
			ours[[2]string{from.RelPath, to.RelPath}] = true
		}
	}
	both := 0
	for e := range ours {
		if ref[e] {
			both++
		}
	}
	var rep evalReport
	rep.f("# Code Map eval: Python import agreement\n\n")
	rep.f("Corpus: %s. Index: %s. Cold build %s.\n\n", filepath.Base(ext), idx.Info(), cold.Round(time.Millisecond))
	rep.f("Reference resolver edges: %d. Code Map edges: %d. Agreement: %d.\n\n", len(ref), len(ours), both)
	if len(ref) > 0 {
		rep.f("Code Map recall vs reference: %.0f%%.\n", 100*float64(both)/float64(len(ref)))
	}
	if len(ours) > 0 {
		rep.f("Code Map precision vs reference: %.0f%%.\n", 100*float64(both)/float64(len(ours)))
	}
	rep.flush(t)
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// reverseDeps maps each package directory (module-relative) to the set of other
// package directories that depend on it transitively, per `go list`.
func reverseDeps(t *testing.T, work string) map[string]map[string]bool {
	t.Helper()
	cmd := exec.Command("go", "list", "-m")
	cmd.Dir = work
	mod, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	prefix := strings.TrimSpace(string(mod)) + "/"
	cmd = exec.Command("go", "list", "-f", "{{.ImportPath}}|{{join .Deps \",\"}}", "./...")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list deps: %v", err)
	}
	dir := func(importPath string) (string, bool) {
		if !strings.HasPrefix(importPath, prefix) {
			return "", false
		}
		return strings.TrimPrefix(importPath, prefix), true
	}
	rdeps := map[string]map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, deps, _ := strings.Cut(line, "|")
		from, ok := dir(pkg)
		if !ok {
			continue
		}
		for _, d := range strings.Split(deps, ",") {
			if to, ok := dir(d); ok && to != from {
				if rdeps[to] == nil {
					rdeps[to] = map[string]bool{}
				}
				rdeps[to][from] = true
			}
		}
	}
	return rdeps
}

var tsAliasSpecRe = regexp.MustCompile(`from\s+['"](@/[^'"]+)['"]`)

// TestEvalTSAliasResolution checks tsconfig path-alias handling on a project that
// is known to compile: every `@/...` import in it points at a real file, so the
// share of such specifiers that become edges should be near 100%.
func TestEvalTSAliasResolution(t *testing.T) {
	ext := os.Getenv("CODEMAP_EVAL_EXT")
	if ext == "" {
		ext = filepath.Join(os.Getenv("HOME"), "repos", "langflow")
	}
	front := filepath.Join(ext, "src", "frontend")
	if _, err := os.Stat(front); err != nil {
		t.Skipf("frontend %s not present", front)
	}
	idx, err := Build(context.Background(), BuildOptions{Root: front, MaxFiles: 100000, MaxDuration: 5 * time.Minute})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	specs := map[[2]string]bool{}
	_ = filepath.WalkDir(front, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if !d.IsDir() && (strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")) {
			data, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(front, p)
			for _, m := range tsAliasSpecRe.FindAllStringSubmatch(string(data), -1) {
				specs[[2]string{filepath.ToSlash(rel), m[1]}] = true
			}
		}
		return nil
	})
	got := map[[2]string]bool{}
	for _, e := range idx.Edges(EdgeImports) {
		if strings.HasPrefix(e.Meta, "@/") {
			got[[2]string{idx.nodes[e.From].RelPath, e.Meta}] = true
		}
	}
	resolved := 0
	for k := range specs {
		if got[k] {
			resolved++
		}
	}
	var rep evalReport
	rep.f("# Code Map eval: TypeScript alias resolution\n\n")
	rep.f("Corpus: %s. Index: %s.\n\n", filepath.Join(filepath.Base(ext), "src/frontend"), idx.Info())
	rep.f("Distinct `@/` import specifiers: %d. Resolved to a file: %d (%.0f%%).\n", len(specs), resolved, 100*float64(resolved)/float64(max(len(specs), 1)))
	rep.flush(t)
}

// goTestGraph is the `go list` view needed to say which test packages a change
// to package dir P can affect: those that are P or depend on it, directly or
// through their own test imports.
type goTestGraph struct {
	hasTests map[string]bool
	closure  map[string]map[string]bool // test package -> every in-module package it (transitively) depends on
}

func loadGoTestGraph(t *testing.T, work string) goTestGraph {
	t.Helper()
	cmd := exec.Command("go", "list", "-m")
	cmd.Dir = work
	mod, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	prefix := strings.TrimSpace(string(mod)) + "/"
	cmd = exec.Command("go", "list", "-f", `{{.ImportPath}}|{{len .TestGoFiles}}|{{len .XTestGoFiles}}|{{join .Deps ","}}|{{join .TestImports ","}}|{{join .XTestImports ","}}`, "./...")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	rel := func(p string) (string, bool) {
		if !strings.HasPrefix(p, prefix) {
			return "", false
		}
		return strings.TrimPrefix(p, prefix), true
	}
	type row struct {
		hasTests          bool
		deps, testImports []string
	}
	rows := map[string]row{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "|")
		if len(f) != 6 {
			continue
		}
		dir, ok := rel(f[0])
		if !ok {
			continue
		}
		r := row{hasTests: f[1] != "0" || f[2] != "0"}
		r.deps = strings.Split(f[3], ",")
		r.testImports = append(strings.Split(f[4], ","), strings.Split(f[5], ",")...)
		rows[dir] = r
	}
	g := goTestGraph{hasTests: map[string]bool{}, closure: map[string]map[string]bool{}}
	for dir, r := range rows {
		if !r.hasTests {
			continue
		}
		g.hasTests[dir] = true
		set := map[string]bool{dir: true}
		add := func(importPath string) {
			if d, ok := rel(importPath); ok {
				set[d] = true
			}
		}
		for _, d := range r.deps {
			add(d)
		}
		for _, ti := range r.testImports {
			add(ti)
			if d, ok := rel(ti); ok {
				for _, dd := range rows[d].deps {
					add(dd)
				}
			}
		}
		g.closure[dir] = set
	}
	return g
}

// testsAffectedBy is the conservative ground truth: every test package that is
// P or depends on P.
func (g goTestGraph) testsAffectedBy(p string) map[string]bool {
	out := map[string]bool{}
	for q, c := range g.closure {
		if c[p] {
			out[q] = true
		}
	}
	return out
}

// TestEvalGoTestSelection scores "which tests should I run after editing file F"
// for the policies code_impact offers against the conservative `go list` truth
// (every test package that is, or depends on, F's package): the package's own
// tests only, the tool's scope=file output, and its default scope=package output.
// Run-everything is the reference cost.
func TestEvalGoTestSelection(t *testing.T) {
	n := 40
	if v, err := strconv.Atoi(os.Getenv("CODEMAP_EVAL_N")); err == nil && v > 0 {
		n = v
	}
	src := repoRoot(t)
	work := t.TempDir()
	copyTracked(t, src, work)
	texts := map[string]string{}
	_ = filepath.WalkDir(work, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
			if data, rerr := os.ReadFile(p); rerr == nil {
				rel, _ := filepath.Rel(work, p)
				texts[filepath.ToSlash(rel)] = string(data)
			}
		}
		return nil
	})
	idx, err := Build(context.Background(), BuildOptions{Root: work, MaxFiles: 100000, MaxDuration: 5 * time.Minute})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	g := loadGoTestGraph(t, work)
	dirSet := func(dirs []TestDir) map[string]bool {
		out := map[string]bool{}
		for _, d := range dirs {
			out[d.Dir] = true
		}
		return out
	}

	var sumTruth, ownHit, fileHit, fileSize, pkgHit, pkgSize, used int
	for _, c := range pickMutationCases(t, work, texts, n) {
		truth := g.testsAffectedBy(c.dir)
		target, ok := idx.ResolveFile(c.file)
		if len(truth) == 0 || !ok {
			continue
		}
		used++
		sumTruth += len(truth)
		if truth[c.dir] {
			ownHit++
		}
		file := dirSet(idx.TestDirsFromImpact(target, idx.Impact(c.file, MaxDepthAll, 100000), nil))
		fileHit += overlap(file, truth)
		fileSize += len(file)
		pkg := dirSet(idx.TestDirsToRun(target, nil, 0))
		pkgHit += overlap(pkg, truth)
		pkgSize += len(pkg)
	}
	if used == 0 {
		t.Fatal("no cases")
	}
	pct := func(a, b int) float64 { return 100 * float64(a) / float64(max(b, 1)) }
	var rep evalReport
	rep.f("# Code Map eval: Go test selection\n\n")
	rep.f("Corpus: %s, %d test packages. %d changed files. Truth: every test package that is, or depends on, the changed file's package (conservative upper bound; mean %.1f packages per case).\n\n", filepath.Base(src), len(g.hasTests), used, float64(sumTruth)/float64(used))
	rep.f("| policy | recall of affected test packages | mean packages to run |\n|---|---|---|\n")
	rep.f("| own package only | %.0f%% | 1.0 |\n", pct(ownHit, sumTruth))
	rep.f("| code_impact scope=file | %.0f%% | %.1f |\n", pct(fileHit, sumTruth), float64(fileSize)/float64(used))
	rep.f("| code_impact scope=package (default) | %.0f%% | %.1f |\n", pct(pkgHit, sumTruth), float64(pkgSize)/float64(used))
	rep.f("| run every test package | 100%% | %d |\n", len(g.hasTests))
	rep.flush(t)
	if r := pct(pkgHit, sumTruth); r < 99 {
		t.Errorf("scope=package recall %.0f%% should match go list (>=99%%)", r)
	}
}

// TestEvalImpactLatencyOnDocsHeavyRepo times repeated Impact calls, whose docs
// section used to re-read every markdown file per call.
func TestEvalImpactLatencyOnDocsHeavyRepo(t *testing.T) {
	ext := os.Getenv("CODEMAP_EVAL_EXT")
	if ext == "" {
		ext = filepath.Join(os.Getenv("HOME"), "repos", "langflow")
	}
	if _, err := os.Stat(ext); err != nil {
		t.Skipf("external repo %s not present", ext)
	}
	idx, err := Build(context.Background(), BuildOptions{Root: ext, MaxFiles: 100000, MaxDuration: 5 * time.Minute})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	docs := 0
	for _, n := range idx.Nodes() {
		if n.Kind == NodeFile && n.Language == "markdown" {
			docs++
		}
	}
	var target Node
	for _, n := range idx.Nodes() {
		if n.Kind == NodeFile && strings.HasSuffix(n.RelPath, ".py") && len(idx.Dependents(n.RelPath, 3)) > 0 {
			target = n
			break
		}
	}
	first := time.Now()
	idx.Impact(target.RelPath, 3, 500)
	firstCall := time.Since(first)
	start := time.Now()
	for range 20 {
		idx.Impact(target.RelPath, 3, 500)
	}
	t.Logf("%s: %d markdown files; first Impact %s, then %s per call", filepath.Base(ext), docs, firstCall.Round(time.Millisecond), (time.Since(start) / 20).Round(time.Microsecond))
}

// withoutDirect returns the transitive dependents that are not also direct ones.
func withoutDirect(res ImpactResult) []Node {
	direct := map[NodeID]bool{}
	for _, n := range res.DirectDependents {
		direct[n.ID] = true
	}
	var out []Node
	for _, n := range res.TransitiveDependents {
		if !direct[n.ID] {
			out = append(out, n)
		}
	}
	return out
}
