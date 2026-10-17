package codemap

import (
	"context"
	"strings"
	"testing"
)

func TestIsTestFile(t *testing.T) {
	for rel, want := range map[string]bool{
		"pkg/a_test.go": true, "pkg/a.go": false, "web/a.test.ts": true, "web/a.spec.tsx": true, "web/a.ts": false,
		"app/test_a.py": true, "app/a.py": false, "tests/conftest.py": true, "src/tests/helpers.rs": true,
		"web/__tests__/x.js": true, "docs/tests/notes.md": false, "src/latest/a.go": false,
	} {
		if got := IsTestFile(rel); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", rel, got, want)
		}
	}
}

func goTestRepo(t *testing.T) *CodeIndex {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/m\n")
	write(t, root, "base/base.go", "package base\nfunc Base() {}\n")
	write(t, root, "base/other.go", "package base\nfunc Other() {}\n")
	write(t, root, "base/base_test.go", "package base\nimport \"testing\"\nfunc TestBase(t *testing.T) {}\n")
	write(t, root, "mid/mid.go", "package mid\nimport \"example.com/m/base\"\nfunc Mid() { base.Base() }\n")
	write(t, root, "mid/mid_test.go", "package mid\nimport \"testing\"\nfunc TestMid(t *testing.T) {}\n")
	// uses only base.Other, so file-level narrowing drops it from base.go's dependents
	write(t, root, "side/side.go", "package side\nimport \"example.com/m/base\"\nfunc Side() { base.Other() }\n")
	write(t, root, "side/side_test.go", "package side\nimport \"testing\"\nfunc TestSide(t *testing.T) {}\n")
	write(t, root, "top/top.go", "package top\nimport \"example.com/m/mid\"\nfunc Top() { mid.Mid() }\n")
	write(t, root, "top/top_test.go", "package top\nimport \"testing\"\nfunc TestTop(t *testing.T) {}\n")
	write(t, root, "unrelated/u.go", "package unrelated\nfunc U() {}\n")
	write(t, root, "unrelated/u_test.go", "package unrelated\nimport \"testing\"\nfunc TestU(t *testing.T) {}\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return idx
}

func TestDirsToRunIsPackageLevelClosure(t *testing.T) {
	idx := goTestRepo(t)
	target, ok := idx.ResolveFile("base/base.go")
	if !ok {
		t.Fatal("target not found")
	}
	var dirs []string
	for _, d := range idx.TestDirsToRun(target, nil, 0) {
		dirs = append(dirs, d.Dir)
	}
	// own dir first, then every importer transitively (side included although it
	// does not use base.go), never the unrelated package.
	if got := strings.Join(dirs, ","); got != "base,mid,side,top" {
		t.Fatalf("TestDirsToRun = %s, want base,mid,side,top", got)
	}
}

func TestDirsFromImpactIsNarrowerThanClosure(t *testing.T) {
	idx := goTestRepo(t)
	target, _ := idx.ResolveFile("base/base.go")
	var got []string
	for _, d := range idx.TestDirsFromImpact(target, idx.Impact(target.RelPath, MaxDepthAll, 100), nil) {
		got = append(got, d.Dir)
	}
	// side uses only base/other.go, so file-level narrowing drops it; top is
	// reached through mid.
	if joined := strings.Join(got, ","); joined != "base,mid,top" {
		t.Fatalf("file-level test dirs = %s, want base,mid,top", joined)
	}
}

func TestDirsToRunHonorsKeepFilter(t *testing.T) {
	idx := goTestRepo(t)
	target, _ := idx.ResolveFile("base/base.go")
	keep := func(n Node) bool { return !strings.HasPrefix(n.RelPath, "mid/") }
	var dirs []string
	for _, d := range idx.TestDirsToRun(target, keep, 0) {
		dirs = append(dirs, d.Dir)
	}
	// mid is filtered, so top (reachable only through mid) is not reached either.
	if got := strings.Join(dirs, ","); got != "base,side" {
		t.Fatalf("filtered TestDirsToRun = %s, want base,side", got)
	}
}

func TestFindFilesAmbiguityAndPrecedence(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main\n")
	write(t, root, "cmd/a/main.go", "package main\n")
	write(t, root, "cmd/b/main.go", "package main\n")
	write(t, root, "domain.go", "package main\n")
	write(t, root, "pkg/only.go", "package pkg\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := idx.FindFiles("main.go"); len(got) != 1 || got[0].RelPath != "main.go" {
		t.Errorf("exact path must win over suffix matches, got %+v", got)
	}
	if got := idx.FindFiles("a/main.go"); len(got) != 1 || got[0].RelPath != "cmd/a/main.go" {
		t.Errorf("unique suffix = %+v", got)
	}
	if got := idx.FindFiles("cmd/main.go"); len(got) != 0 {
		t.Errorf("non-matching = %+v", got)
	}
	if got := idx.FindFiles("only"); len(got) != 1 || got[0].RelPath != "pkg/only.go" {
		t.Errorf("unique substring = %+v", got)
	}
	if got := idx.FindFiles("main"); len(got) < 3 {
		t.Errorf("fragment shared by several files must return all of them, got %+v", got)
	}
	if _, ok := idx.ResolveFile("main"); ok {
		t.Error("ResolveFile must refuse an ambiguous fragment")
	}
	// A fragment that lands on no path boundary falls back to substring matching.
	if got := idx.FindFiles("n.go"); len(got) < 4 {
		t.Errorf("a fragment with no path-boundary match falls back to substring, got %+v", got)
	}
}
