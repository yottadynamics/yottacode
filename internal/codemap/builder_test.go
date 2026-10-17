package codemap

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// TestBuildGoImportsFallsBackOnBodySyntaxError guards a regression the
// symbol-usage analysis introduced: parsing the full file body (needed to
// find selector usages) fails outright on a body syntax error, whereas the
// original imports-only parsing tolerated one just fine. A file mid-edit
// with a syntax error in its body is exactly the state the watcher's
// incremental re-index sees while a user is actively typing — it must
// still contribute its import edges (falling back to whole-package
// resolution, since selector usage can't be determined), not lose them
// entirely until the syntax error is fixed.
func TestBuildGoImportsFallsBackOnBodySyntaxError(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc Use() {}\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() {\n\tlib.Use(\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("dependencies = %+v, want internal/lib/lib.go even with a body syntax error", deps)
	}
}

// TestBuildGoImportEdgesNarrowToReferencedSymbol covers the core
// symbol-level precision case: a package with two files, where the
// importer only calls one of them's exported function. The edge should
// land on just that file, not the whole package directory.
func TestBuildGoImportEdgesNarrowToReferencedSymbol(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() { lib.Foo() }\n")
	write(t, root, "internal/lib/foo.go", "package lib\n\nfunc Foo() {}\n")
	write(t, root, "internal/lib/bar.go", "package lib\n\nfunc Bar() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/foo.go" {
		t.Fatalf("dependencies = %+v, want only internal/lib/foo.go (bar.go's Bar is never referenced)", deps)
	}
}

// TestBuildGoImportEdgesIncludeEveryReferencedFile covers referencing
// symbols from more than one file in the same package — both should be
// edge targets, and only those two.
func TestBuildGoImportEdgesIncludeEveryReferencedFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() {\n\tlib.Foo()\n\tlib.Bar()\n}\n")
	write(t, root, "internal/lib/foo.go", "package lib\n\nfunc Foo() {}\n")
	write(t, root, "internal/lib/bar.go", "package lib\n\nfunc Bar() {}\n")
	write(t, root, "internal/lib/baz.go", "package lib\n\nfunc Baz() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	byPath := map[string]bool{}
	for _, d := range deps {
		byPath[d.RelPath] = true
	}
	if len(deps) != 2 || !byPath["internal/lib/foo.go"] || !byPath["internal/lib/bar.go"] {
		t.Fatalf("dependencies = %+v, want exactly foo.go and bar.go (baz.go's Baz is never referenced)", deps)
	}
}

// TestBuildGoImportEdgesFallBackWhenReferencedNameUnmatched covers the
// safety net: when the referenced identifier doesn't match any known
// package-level symbol (e.g. it's a method call, which this best-effort
// analysis can't distinguish from a package-level function by name alone),
// the edge falls back to every file in the package rather than losing the
// dependency entirely.
func TestBuildGoImportEdgesFallBackWhenReferencedNameUnmatched(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() { lib.New().Method() }\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\ntype T struct{}\n\nfunc New() T { return T{} }\nfunc (T) Method() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("dependencies = %+v, want internal/lib/lib.go (New is a real package-level match)", deps)
	}
}

// TestBuildGoImportEdgesFallBackForBlankImport covers a blank import
// (`_ "pkg"`, used only for side effects): there's no selector usage to
// analyze at all, so this must fall back to the whole package rather than
// producing zero edges for an import that's genuinely there.
func TestBuildGoImportEdgesFallBackForBlankImport(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport _ \"example.com/test/internal/lib\"\n\nfunc Run() {}\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc init() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("dependencies = %+v, want internal/lib/lib.go (blank import has no selector usage to narrow by)", deps)
	}
}

// TestBuildGoImportEdgesExcludeTestFiles guards a real bug found via live
// testing against this repo: importing a package created an edge to EVERY
// file in its directory, _test.go files included — but _test.go files are
// never part of a package's importable surface (the Go compiler excludes
// them when compiling the package for an external importer), so they can
// never legitimately be a "depends on" target.
func TestBuildGoImportEdgesExcludeTestFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() {}\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc Use() {}\n")
	write(t, root, "internal/lib/lib_test.go", "package lib\n\nimport \"testing\"\n\nfunc TestUse(t *testing.T) {}\n")
	write(t, root, "internal/lib/lib_external_test.go", "package lib_test\n\nimport \"testing\"\n\nfunc TestExternal(t *testing.T) {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("dependencies = %+v, want only internal/lib/lib.go (no _test.go targets)", deps)
	}
}

// TestBuildGoExternalTestPackageDoesNotShadowRealPackageName guards the
// subtler half of the same bug: an external test file's `package foo_test`
// clause was recorded as "the" package for its directory whenever WalkDir
// visited it last (alphabetical order), silently breaking resolution for
// every real importer of that package once that happened.
func TestBuildGoExternalTestPackageDoesNotShadowRealPackageName(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() {}\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc Use() {}\n")
	// "zz_external_test.go" sorts after "lib.go" alphabetically, so WalkDir
	// visits it last in the directory — exactly the ordering that triggered
	// the bug.
	write(t, root, "internal/lib/zz_external_test.go", "package lib_test\n\nimport \"testing\"\n\nfunc TestExternal(t *testing.T) {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("dependencies = %+v, want internal/lib/lib.go (package name must not be shadowed by the external test package)", deps)
	}
}

func TestBuildResolvesTSRelativeImportEdges(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/app.ts", "import { helper } from './lib/helper';\nimport React from 'react';\n\nexport function run() { helper(); }\n")
	write(t, root, "src/lib/helper.ts", "export function helper() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("src/app.ts", 10)
	if len(deps) != 1 || deps[0].RelPath != "src/lib/helper.ts" {
		t.Fatalf("TS dependencies = %+v, want src/lib/helper.ts (react is external, should not resolve)", deps)
	}
}

// TestBuildResolvesTSMultiLineImport covers the Prettier-formatted
// multi-line destructured import shape — extremely common in real
// TypeScript codebases (`import {\n  a,\n  b,\n} from './x';`). The line
// carrying `from '...'` is what tsFromImportRe matches, regardless of which
// line the `import {` opened on, so this resolves without any special
// multi-line handling.
func TestBuildResolvesTSMultiLineImport(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/app.ts", "import {\n  helper,\n  other,\n} from './lib/helper';\n\nexport function run() { helper(); }\n")
	write(t, root, "src/lib/helper.ts", "export function helper() {}\nexport function other() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("src/app.ts", 10)
	if len(deps) != 1 || deps[0].RelPath != "src/lib/helper.ts" {
		t.Fatalf("multi-line TS import deps = %+v, want src/lib/helper.ts", deps)
	}
}

func TestBuildResolvesTSIndexImport(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/app.ts", "import { widget } from './widget';\n")
	write(t, root, "src/widget/index.ts", "export const widget = 1;\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("src/app.ts", 10)
	if len(deps) != 1 || deps[0].RelPath != "src/widget/index.ts" {
		t.Fatalf("TS index-resolution deps = %+v, want src/widget/index.ts", deps)
	}
}

func TestBuildResolvesPythonImportEdges(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app/main.py", "import os\nfrom app.lib import helper\nfrom . import sibling\nfrom .sibling import thing\n\ndef run():\n    pass\n")
	write(t, root, "app/lib.py", "def helper():\n    pass\n")
	write(t, root, "app/sibling.py", "thing = 1\n")
	write(t, root, "app/__init__.py", "")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("app/main.py", 10)
	if len(deps) != 2 {
		t.Fatalf("python dependencies = %+v, want app/lib.py and app/sibling.py (os is stdlib, external)", deps)
	}
	byPath := map[string]bool{}
	for _, d := range deps {
		byPath[d.RelPath] = true
	}
	if !byPath["app/lib.py"] || !byPath["app/sibling.py"] {
		t.Fatalf("python dependencies = %+v, want app/lib.py and app/sibling.py", deps)
	}
}

// TestBuildResolvesPythonMultiLineRelativeImport covers the common
// `__init__.py` re-export shape — a bare-dots relative import whose name
// list is parenthesized across multiple lines. Unlike the TS multi-line
// case, this genuinely needs multi-line accumulation (see
// parsePythonImports): the resolvable module path only exists once the
// names inside the parens are known.
func TestBuildResolvesPythonMultiLineRelativeImport(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app/__init__.py", "from . import (\n    sibling,\n    other,\n)\n")
	write(t, root, "app/sibling.py", "thing = 1\n")
	write(t, root, "app/other.py", "thing = 2\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("app/__init__.py", 10)
	byPath := map[string]bool{}
	for _, d := range deps {
		byPath[d.RelPath] = true
	}
	if !byPath["app/sibling.py"] || !byPath["app/other.py"] {
		t.Fatalf("multi-line relative python import deps = %+v, want app/sibling.py and app/other.py", deps)
	}
}

func TestBuildResolvesRustModAndUseEdges(t *testing.T) {
	root := t.TempDir()
	write(t, root, "crate/src/lib.rs", "mod widget;\n\nfn top() {}\n")
	write(t, root, "crate/src/widget.rs", "use crate::helper::assist;\n\npub fn make() { assist(); }\n")
	write(t, root, "crate/src/helper.rs", "pub fn assist() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	libDeps := idx.Dependencies("crate/src/lib.rs", 10)
	if len(libDeps) != 1 || libDeps[0].RelPath != "crate/src/widget.rs" {
		t.Fatalf("rust mod deps = %+v, want crate/src/widget.rs via `mod widget;`", libDeps)
	}
	widgetDeps := idx.Dependencies("crate/src/widget.rs", 10)
	if len(widgetDeps) != 1 || widgetDeps[0].RelPath != "crate/src/helper.rs" {
		t.Fatalf("rust crate:: deps = %+v, want crate/src/helper.rs via `use crate::helper::assist;`", widgetDeps)
	}
}

// TestBuildResolvesRustSuperUseToParentModuleItem covers the case that
// originally motivated leaving self::/super:: unresolved: a directory-based
// heuristic can't distinguish "top is a further submodule" from "top is an
// item defined directly in the parent file". resolveRustModulePath walks
// module segments and stops as soon as one fails to name a submodule,
// landing on the last confirmed module file — here, the declaring parent
// itself.
func TestBuildResolvesRustSuperUseToParentModuleItem(t *testing.T) {
	root := t.TempDir()
	write(t, root, "crate/src/lib.rs", "mod widget;\n\nfn top() {}\n")
	write(t, root, "crate/src/widget.rs", "use super::top;\n\npub fn make() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("crate/src/widget.rs", 10)
	if len(deps) != 1 || deps[0].RelPath != "crate/src/lib.rs" {
		t.Fatalf("super:: deps = %+v, want crate/src/lib.rs (top is an item in the parent module, not a submodule)", deps)
	}
}

// TestBuildResolvesRustSelfUseToSiblingSubmodule covers self:: referencing a
// submodule declared by the same file the use statement appears in.
func TestBuildResolvesRustSelfUseToSiblingSubmodule(t *testing.T) {
	root := t.TempDir()
	write(t, root, "crate/src/lib.rs", "mod helper;\nmod widget;\n\nuse self::helper::assist;\n\nfn run() { assist(); }\n")
	write(t, root, "crate/src/helper.rs", "pub fn assist() {}\n")
	write(t, root, "crate/src/widget.rs", "pub fn make() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("crate/src/lib.rs", 10)
	byPath := map[string]bool{}
	for _, d := range deps {
		byPath[d.RelPath] = true
	}
	if !byPath["crate/src/helper.rs"] {
		t.Fatalf("self:: deps = %+v, want crate/src/helper.rs via `use self::helper::assist;`", deps)
	}
}

// TestBuildDoesNotResolveSuperWithUnknownParent guards the remaining
// deliberate skip: super:: with no discoverable declaring parent (no `mod
// widget;` anywhere naming this file) has nothing to resolve against, so it
// stays unresolved rather than guessed.
func TestBuildDoesNotResolveSuperWithUnknownParent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "crate/src/lib.rs", "fn top() {}\n")
	write(t, root, "crate/src/widget.rs", "use super::top;\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if deps := idx.Dependencies("crate/src/widget.rs", 10); len(deps) != 0 {
		t.Fatalf("super:: with no declaring parent should not resolve, got %+v", deps)
	}
}

// TestBuildResolvesRustLeafFileOwnSubmodule covers the bug the original
// mod-resolution had before this fix: a non-root file's OWN `mod x;`
// declaration must resolve to a same-named sibling directory
// (crate/src/widget/inner.rs), not alongside the declaring file itself
// (crate/src/inner.rs) — only lib.rs/main.rs/mod.rs use their own directory
// for their submodules.
func TestBuildResolvesRustLeafFileOwnSubmodule(t *testing.T) {
	root := t.TempDir()
	write(t, root, "crate/src/lib.rs", "mod widget;\n")
	write(t, root, "crate/src/widget.rs", "mod inner;\n\npub fn make() {}\n")
	write(t, root, "crate/src/widget/inner.rs", "pub fn go() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	deps := idx.Dependencies("crate/src/widget.rs", 10)
	if len(deps) != 1 || deps[0].RelPath != "crate/src/widget/inner.rs" {
		t.Fatalf("leaf file's own mod deps = %+v, want crate/src/widget/inner.rs", deps)
	}
}

func TestBuildSkipsUnresolvedExternalImports(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app.py", "import numpy\nfrom collections import OrderedDict\n")
	write(t, root, "app.ts", "import lodash from 'lodash';\n")
	write(t, root, "app.rs", "use serde::Serialize;\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := idx.Edges(EdgeImports); len(got) != 0 {
		t.Fatalf("external-only imports should not produce edges, got %+v", got)
	}
}

func TestBuildExcludesGeneratedFilesAndCountsThem(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "real.go", "package main\nfunc Real() {}\n")
	write(t, root, "api.pb.go", "package main\nfunc Pb() {}\n")
	write(t, root, "stub.go", "// Code generated by mockgen. DO NOT EDIT.\npackage main\nfunc Stub() {}\n")
	write(t, root, "gen.ts", "// @generated by codegen\nexport const x = 1\n")
	write(t, root, "banner.py", "# Generated by the protocol buffer compiler.  DO NOT EDIT!\nx = 1\n")
	write(t, root, "bundle.min.js", "var a=1;")
	write(t, root, "hand.ts", "export const y = 2\n")
	write(t, root, "mention.go", "package main\n\n// Hand-written; the phrase DO NOT EDIT alone must not count as a generated marker.\nfunc M() {}\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, rel := range []string{"api.pb.go", "stub.go", "gen.ts", "banner.py", "bundle.min.js"} {
		if _, ok := idx.ResolveFile(rel); ok {
			t.Errorf("generated file %s was indexed", rel)
		}
	}
	for _, rel := range []string{"real.go", "hand.ts", "mention.go"} {
		if _, ok := idx.ResolveFile(rel); !ok {
			t.Errorf("hand-written file %s was excluded", rel)
		}
	}
	info := idx.Info()
	if info.Generated != 5 || !strings.Contains(info.String(), "5 generated files excluded") {
		t.Fatalf("Info() = %+v (%q), want 5 generated files reported", info, info.String())
	}
}

func TestBuildSkipsCacheAndCoverageDirs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app.py", "x = 1\n")
	write(t, root, "__pycache__/app.py", "x = 1\n")
	write(t, root, "coverage/report.js", "var a\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if info := idx.Info(); info.Files != 1 {
		t.Fatalf("Files = %d, want only app.py indexed", info.Files)
	}
}

// slowSource makes each file cost wall-clock time, standing in for an LSP
// server that is much slower than the parser fallback.
type slowSource struct{ per time.Duration }

func (s slowSource) Symbols(context.Context, string) ([]lsp.Symbol, string, error) {
	time.Sleep(s.per)
	return nil, "slow", nil
}

func TestBuildStopsAtTimeBudgetAndFlagsPartial(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		write(t, root, fmt.Sprintf("f%02d.go", i), "package main\n")
	}
	idx, err := Build(context.Background(), BuildOptions{Root: root, MaxDuration: 40 * time.Millisecond, Workers: 1, Source: slowSource{per: 15 * time.Millisecond}})
	if err != nil {
		t.Fatalf("Build must return the partial index, not an error: %v", err)
	}
	info := idx.Info()
	if !info.TimedOut || info.Capped {
		t.Fatalf("Info() = %+v, want TimedOut and not Capped", info)
	}
	if info.Files == 0 || info.Files >= 30 {
		t.Fatalf("Files = %d, want some but not all of 30", info.Files)
	}
	if s := info.String(); !strings.Contains(s, "PARTIAL") || !strings.Contains(s, "build budget") {
		t.Fatalf("String() = %q, want a PARTIAL build-budget notice", s)
	}
}

func TestBuildWithinTimeBudgetIsComplete(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root, MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if info := idx.Info(); info.TimedOut || info.Capped {
		t.Fatalf("Info() = %+v, want complete", info)
	}
}

func depPaths(idx *CodeIndex, rel string) []string {
	var out []string
	for _, d := range idx.Dependencies(rel, 50) {
		out = append(out, d.RelPath)
	}
	return out
}

func wantDeps(t *testing.T, idx *CodeIndex, rel string, want ...string) {
	t.Helper()
	got := depPaths(idx, rel)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dependencies of %s = %v, want %v", rel, got, want)
	}
}

func buildAt(t *testing.T, root string) *CodeIndex {
	t.Helper()
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return idx
}

func TestPythonAbsoluteImportsResolveUnderSrcLayout(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/backend/pkg/__init__.py", "")
	write(t, root, "src/backend/pkg/services/__init__.py", "")
	write(t, root, "src/backend/pkg/services/deps.py", "X = 1\n")
	write(t, root, "src/backend/pkg/services/base.py", "Y = 1\n")
	write(t, root, "src/backend/pkg/api.py", "import pkg.services.base\nfrom pkg.services import deps\nimport os\nimport json\n")
	idx := buildAt(t, root)
	// `from pkg.services import deps` also imports the package itself.
	wantDeps(t, idx, "src/backend/pkg/api.py", "src/backend/pkg/services/__init__.py", "src/backend/pkg/services/base.py", "src/backend/pkg/services/deps.py")
}

func TestPythonAbsoluteImportAmbiguityIsNotGuessed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/shared/util.py", "A = 1\n")
	write(t, root, "b/shared/util.py", "B = 1\n")
	write(t, root, "c/main.py", "import shared.util\n")
	wantDeps(t, buildAt(t, root), "c/main.py")
}

func TestPythonAmbiguityBrokenByProximity(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/shared/util.py", "A = 1\n")
	write(t, root, "b/shared/util.py", "B = 1\n")
	write(t, root, "b/app/main.py", "import shared.util\n")
	wantDeps(t, buildAt(t, root), "b/app/main.py", "b/shared/util.py")
}

// TestPythonStdlibNameDoesNotMatchUnrelatedModule: `import logging` must not
// become an edge to some repo module that happens to be named logging.py.
func TestPythonStdlibNameDoesNotMatchUnrelatedModule(t *testing.T) {
	root := t.TempDir()
	write(t, root, "tools/logging.py", "L = 1\n")
	write(t, root, "app/main.py", "import logging\n")
	wantDeps(t, buildAt(t, root), "app/main.py")
}

func TestPythonSingleSegmentImportResolvesInOwnDirectoryChain(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app/helpers.py", "H = 1\n")
	write(t, root, "app/main.py", "import helpers\n")
	wantDeps(t, buildAt(t, root), "app/main.py", "app/helpers.py")
}

func TestTSPathAliasesAndBaseURLResolve(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web/tsconfig.json", `{
  // comments and trailing commas are legal in tsconfig
  "compilerOptions": {
    "baseUrl": "src", /* base */
    "paths": {
      "@/*": ["*"],
      "@queries/*": ["controllers/queries/*"],
      "@cfg": ["config/index.ts"],
    },
  },
}`)
	write(t, root, "web/src/utils/fmt.ts", "export const f = 1\n")
	write(t, root, "web/src/controllers/queries/user.ts", "export const q = 1\n")
	write(t, root, "web/src/config/index.ts", "export const c = 1\n")
	write(t, root, "web/src/lib/thing.ts", "export const t = 1\n")
	write(t, root, "web/src/app.ts", "import { f } from '@/utils/fmt'\nimport { q } from '@queries/user'\nimport { c } from '@cfg'\nimport { t } from 'lib/thing'\nimport React from 'react'\n")
	wantDeps(t, buildAt(t, root), "web/src/app.ts", "web/src/config/index.ts", "web/src/controllers/queries/user.ts", "web/src/lib/thing.ts", "web/src/utils/fmt.ts")
}

func TestTSAliasUsesNearestConfigOnly(t *testing.T) {
	root := t.TempDir()
	write(t, root, "tsconfig.json", `{"compilerOptions":{"paths":{"@/*":["outer/*"]}}}`)
	write(t, root, "outer/x.ts", "export const x = 1\n")
	write(t, root, "pkg/tsconfig.json", `{"compilerOptions":{"paths":{"@/*":["inner/*"]}}}`)
	write(t, root, "pkg/inner/x.ts", "export const x = 1\n")
	write(t, root, "pkg/app.ts", "import { x } from '@/x'\n")
	wantDeps(t, buildAt(t, root), "pkg/app.ts", "pkg/inner/x.ts")
}

func TestTSBadTSConfigIsIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, root, "tsconfig.json", "{ not json")
	write(t, root, "a.ts", "import { b } from './b'\n")
	write(t, root, "b.ts", "export const b = 1\n")
	wantDeps(t, buildAt(t, root), "a.ts", "b.ts")
}

func TestTSExtendsInheritsPathsFromBaseConfig(t *testing.T) {
	root := t.TempDir()
	// Typical monorepo: aliases live in a base config at the root; each package
	// has its own tsconfig that extends it and sets nothing about paths.
	write(t, root, "tsconfig.base.json", `{"compilerOptions":{"baseUrl":".","paths":{"@shared/*":["libs/shared/src/*"]}}}`)
	write(t, root, "libs/shared/src/util.ts", "export const u = 1\n")
	write(t, root, "apps/web/tsconfig.json", `{"extends":"../../tsconfig.base","compilerOptions":{"jsx":"react"}}`)
	write(t, root, "apps/web/src/app.ts", "import { u } from '@shared/util'\n")
	wantDeps(t, buildAt(t, root), "apps/web/src/app.ts", "libs/shared/src/util.ts")
}

func TestTSExtendsPathsRelativeToDefiningConfigWithoutBaseURL(t *testing.T) {
	root := t.TempDir()
	write(t, root, "config/tsconfig.json", `{"compilerOptions":{"paths":{"@lib/*":["../lib/*"]}}}`)
	write(t, root, "lib/x.ts", "export const x = 1\n")
	write(t, root, "pkg/tsconfig.json", `{"extends":"../config/tsconfig.json"}`)
	write(t, root, "pkg/a.ts", "import { x } from '@lib/x'\n")
	wantDeps(t, buildAt(t, root), "pkg/a.ts", "lib/x.ts")
}

func TestTSChildPathsReplaceParentAndBaseURLResolvesAgainstDefiner(t *testing.T) {
	root := t.TempDir()
	write(t, root, "base.json", `{"compilerOptions":{"paths":{"@/*":["parent/*"]}}}`)
	write(t, root, "parent/x.ts", "export const x = 1\n")
	write(t, root, "pkg/tsconfig.json", `{"extends":"../base.json","compilerOptions":{"baseUrl":"src","paths":{"@/*":["*"]}}}`)
	write(t, root, "pkg/src/x.ts", "export const x = 2\n")
	write(t, root, "pkg/src/a.ts", "import { x } from '@/x'\n")
	wantDeps(t, buildAt(t, root), "pkg/src/a.ts", "pkg/src/x.ts")
}

func TestTSExtendsIgnoresPackageNamesCyclesAndEscapes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/tsconfig.json", `{"extends":"@tsconfig/node20/tsconfig.json","compilerOptions":{"baseUrl":"."}}`)
	write(t, root, "a/util.ts", "export const u = 1\n")
	write(t, root, "a/main.ts", "import { u } from 'util'\n")
	write(t, root, "c1.json", `{"extends":"./c2.json"}`)
	write(t, root, "c2.json", `{"extends":"./c1.json"}`)
	write(t, root, "cyc/tsconfig.json", `{"extends":"../c1.json","compilerOptions":{"baseUrl":"."}}`)
	write(t, root, "cyc/lib.ts", "export const l = 1\n")
	write(t, root, "cyc/main.ts", "import { l } from 'lib'\n")
	write(t, root, "esc/tsconfig.json", `{"extends":"../../outside.json","compilerOptions":{"baseUrl":"."}}`)
	write(t, root, "esc/lib.ts", "export const l = 1\n")
	write(t, root, "esc/main.ts", "import { l } from 'lib'\n")
	idx := buildAt(t, root) // must terminate despite the extends cycle
	wantDeps(t, idx, "a/main.ts", "a/util.ts")
	wantDeps(t, idx, "cyc/main.ts", "cyc/lib.ts")
	wantDeps(t, idx, "esc/main.ts", "esc/lib.ts")
}

// edgeSet returns every import edge as "from -> to" for order-independent comparison.
func edgeSet(idx *CodeIndex) map[string]bool {
	out := map[string]bool{}
	for _, e := range idx.Edges(EdgeImports) {
		out[idx.nodes[e.From].RelPath+" -> "+idx.nodes[e.To].RelPath] = true
	}
	return out
}

// TestGoImportsWithoutGoModResolveByLongestPathSuffix is the regression for a
// bug found indexing a +incompatible checkout: with no go.mod, an import matched
// the first directory sharing its last segment, in map-iteration order, so the
// same tree produced different edges on every build.
func TestGoImportsWithoutGoModResolveByLongestPathSuffix(t *testing.T) {
	root := t.TempDir()
	write(t, root, "api/types/container/types.go", "package container\n\ntype Config struct{}\n")
	write(t, root, "api/server/router/container/router.go", "package container\n\nfunc New() {}\n")
	write(t, root, "daemon/container/state.go", "package container\n\nfunc State() {}\n")
	write(t, root, "client/use.go", "package client\n\nimport \"example.org/proj/api/types/container\"\n\nvar _ container.Config\n")
	var first map[string]bool
	for i := 0; i < 25; i++ {
		idx := buildAt(t, root)
		wantDeps(t, idx, "client/use.go", "api/types/container/types.go")
		set := edgeSet(idx)
		if first == nil {
			first = set
			continue
		}
		if len(set) != len(first) {
			t.Fatalf("build %d produced %d edges, the first produced %d: resolution is not deterministic", i, len(set), len(first))
		}
	}
}

// TestGoStdlibImportNeverMatchesLocalPackageOfTheSameName: with a go.mod the
// module path decides what is local, so `net/http` is external even when the
// repository has a package called http.
func TestGoStdlibImportNeverMatchesLocalPackageOfTheSameName(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/m\n")
	write(t, root, "http/http.go", "package http\n\nfunc Local() {}\n")
	write(t, root, "errors/errors.go", "package errors\n\nfunc Local() {}\n")
	write(t, root, "app/app.go", "package app\n\nimport (\n\t\"net/http\"\n\t\"errors\"\n\t\"github.com/pkg/errors\"\n)\n\nvar _ = http.StatusOK\nvar _ = errors.New\n")
	write(t, root, "app/real.go", "package app\n\nimport \"example.com/m/http\"\n\nvar _ = http.Local\n")
	idx := buildAt(t, root)
	wantDeps(t, idx, "app/app.go")
	wantDeps(t, idx, "app/real.go", "http/http.go")
}

func TestGoNestedModulesResolveByTheirOwnModulePath(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/root\n")
	write(t, root, "svc/go.mod", "module example.com/svc\n")
	write(t, root, "svc/api/api.go", "package api\n\nfunc Serve() {}\n")
	write(t, root, "svc/cmd/main.go", "package main\n\nimport \"example.com/svc/api\"\n\nfunc main() { api.Serve() }\n")
	write(t, root, "tool/tool.go", "package tool\n\nimport \"example.com/svc/api\"\n\nvar _ = api.Serve\n")
	idx := buildAt(t, root)
	wantDeps(t, idx, "svc/cmd/main.go", "svc/api/api.go")
	wantDeps(t, idx, "tool/tool.go", "svc/api/api.go")
}
