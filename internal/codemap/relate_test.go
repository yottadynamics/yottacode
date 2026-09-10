package codemap

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestIsTestForPerLanguage(t *testing.T) {
	cases := []struct {
		name      string
		candidate string
		target    string
		want      bool
	}{
		{"go exact pair", "foo_test.go", "foo.go", true},
		{"go package-external test", "external_test.go", "foo.go", true},
		{"go different dir", "pkg2/foo_test.go", "pkg1/foo.go", false},
		{"go target is itself a test", "foo_test.go", "bar_test.go", false},
		{"ts .test sibling", "foo.test.ts", "foo.ts", true},
		{"ts .spec sibling cross-ext", "foo.spec.tsx", "foo.ts", true},
		{"ts unrelated", "bar.test.ts", "foo.js", false},
		{"python test_ prefix", "test_foo.py", "foo.py", true},
		{"python _test suffix", "foo_test.py", "foo.py", true},
		{"rust suffix", "foo_test.rs", "foo.rs", true},
		{"unsupported extension", "foo.md", "foo.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTestFor(tc.candidate, tc.target); got != tc.want {
				t.Fatalf("IsTestFor(%q, %q) = %v, want %v", tc.candidate, tc.target, got, tc.want)
			}
		})
	}
}

func TestLikelyDocsForMatchesByBaseNameAndDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/widget/widget.go", "package widget\nfunc New() {}\n")
	write(t, root, "docs/widget.md", "# Widget\nThe widget subsystem handles rendering.\n")
	write(t, root, "docs/unrelated.md", "# Something else entirely\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	docs := LikelyDocsFor(idx, "internal/widget/widget.go", 5)
	if len(docs) != 1 || docs[0].RelPath != "docs/widget.md" {
		t.Fatalf("LikelyDocsFor = %+v, want docs/widget.md", docs)
	}
}

func TestSuggestedContextRanksChangedFileFirstAndCombinesReasons(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() {}\n")
	write(t, root, "internal/app/app_test.go", "package app\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {}\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc Use() {}\n")
	write(t, root, "docs/app.md", "# App\nDocs for the app package.\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	items := SuggestedContext(idx, []string{"internal/app/app.go"}, 10)
	if len(items) == 0 {
		t.Fatal("expected suggested items")
	}
	if items[0].Node.RelPath != "internal/app/app.go" {
		t.Fatalf("first item = %+v, want the changed file ranked first", items[0])
	}
	byPath := map[string]SuggestedItem{}
	for _, it := range items {
		byPath[it.Node.RelPath] = it
	}
	if _, ok := byPath["internal/lib/lib.go"]; !ok {
		t.Fatalf("expected direct dependency in suggested context: %+v", items)
	}
	testItem, ok := byPath["internal/app/app_test.go"]
	if !ok || !containsReason(testItem.Reasons, ReasonTestFor) {
		t.Fatalf("expected test-for reason on app_test.go: %+v", items)
	}
	docItem, ok := byPath["docs/app.md"]
	if !ok || !containsReason(docItem.Reasons, ReasonDocsFor) {
		t.Fatalf("expected docs-for reason on docs/app.md: %+v", items)
	}
}

func TestSuggestedContextEmptyWithoutChangedFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := SuggestedContext(idx, nil, 10); got != nil {
		t.Fatalf("SuggestedContext with no changed files = %+v, want nil", got)
	}
}

func containsReason(reasons []Reason, want Reason) bool {
	return slices.Contains(reasons, want)
}

func TestBuildIndexesMarkdownFilesAsPlainNodes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "docs/readme.md", "# Hello\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	n, ok := findNode(idx, NodeFile, "docs/readme.md", "readme.md")
	if !ok {
		t.Fatalf("markdown file should be indexed: %+v", idx.Nodes())
	}
	if n.Language != "markdown" {
		t.Fatalf("markdown node language = %q, want markdown", n.Language)
	}
	if syms := idx.SymbolsForFile("docs/readme.md"); len(syms) != 0 {
		t.Fatalf("markdown file should have no symbols: %+v", syms)
	}
	if strings.Contains(n.Name, "/") {
		t.Fatalf("unexpected node name: %q", n.Name)
	}
}
