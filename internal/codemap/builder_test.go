package codemap

import (
	"context"
	"testing"
)

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
