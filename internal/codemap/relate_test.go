package codemap

import (
	"context"
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
