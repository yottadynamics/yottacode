package filerefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_ExtractsAtTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty",
			input: "",
			want:  nil,
		},
		{
			name:  "no at sign",
			input: "explain main.go",
			want:  nil,
		},
		{
			name:  "single ref",
			input: "explain @main.go please",
			want:  []string{"main.go"},
		},
		{
			name:  "multiple refs",
			input: "compare @main.go and @internal/foo.go",
			want:  []string{"main.go", "internal/foo.go"},
		},
		{
			name:  "ref at start",
			input: "@main.go is the entry point",
			want:  []string{"main.go"},
		},
		{
			name:  "ref with trailing punctuation",
			input: "what about @main.go, can you explain?",
			want:  []string{"main.go"},
		},
		{
			name:  "ref in parens",
			input: "see (@main.go) for details",
			want:  []string{"main.go"},
		},
		{
			name:  "email is not a ref",
			input: "contact me at user@example.com about it",
			want:  nil,
		},
		{
			name:  "deduplication",
			input: "@main.go calls into @main.go again",
			want:  []string{"main.go"},
		},
		{
			name:  "dotfile",
			input: "check @.gitignore",
			want:  []string{".gitignore"},
		},
		{
			name:  "nested path",
			input: "@a/b/c/d.txt",
			want:  []string{"a/b/c/d.txt"},
		},
		{
			name:  "bare @ ignored",
			input: "what about @ here",
			want:  nil,
		},
		{
			name:  "lone dot ignored",
			input: "look at @. ignore",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			gotPaths := make([]string, len(got))
			for i, r := range got {
				gotPaths[i] = r.Path
			}
			if !equalStringSlices(gotPaths, tt.want) {
				t.Fatalf("Parse(%q) = %v, want %v", tt.input, gotPaths, tt.want)
			}
		})
	}
}

func TestLoad_ReadsFiles(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "hello.go"), "package main\n\nfunc main() {}\n")

	refs := Load([]Ref{{Token: "@hello.go", Path: "hello.go"}}, tmp)
	if len(refs) != 1 {
		t.Fatalf("len = %d", len(refs))
	}
	r := refs[0]
	if !r.Loaded {
		t.Fatalf("not loaded: %s", r.Error)
	}
	if !strings.Contains(r.Content, "package main") {
		t.Fatalf("unexpected content: %q", r.Content)
	}
}

func TestLoad_RejectsAbsolutePaths(t *testing.T) {
	tmp := t.TempDir()
	refs := Load([]Ref{{Token: "@/etc/passwd", Path: "/etc/passwd"}}, tmp)
	if refs[0].Loaded {
		t.Fatalf("should not have loaded absolute path")
	}
	if !strings.Contains(refs[0].Error, "absolute paths") {
		t.Fatalf("error = %q", refs[0].Error)
	}
}

func TestLoad_RejectsTraversal(t *testing.T) {
	tmp := t.TempDir()
	// Create a sibling file outside tmp; the ref tries to traverse to it.
	parent := filepath.Dir(tmp)
	outside := filepath.Join(parent, "outside-"+filepath.Base(tmp)+".txt")
	mustWrite(t, outside, "secret")
	t.Cleanup(func() { os.Remove(outside) })

	refs := Load([]Ref{{Token: "@../" + filepath.Base(outside), Path: "../" + filepath.Base(outside)}}, tmp)
	if refs[0].Loaded {
		t.Fatalf("should not have loaded out-of-cwd path")
	}
	if !strings.Contains(refs[0].Error, "outside") {
		t.Fatalf("error = %q", refs[0].Error)
	}
}

func TestLoad_RejectsSymlinks(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "real.txt")
	mustWrite(t, target, "hi")
	link := filepath.Join(tmp, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	refs := Load([]Ref{{Token: "@link.txt", Path: "link.txt"}}, tmp)
	if refs[0].Loaded {
		t.Fatalf("should not have followed symlink")
	}
	if !strings.Contains(refs[0].Error, "symlink") {
		t.Fatalf("error = %q", refs[0].Error)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	refs := Load([]Ref{{Token: "@nope.go", Path: "nope.go"}}, tmp)
	if refs[0].Loaded {
		t.Fatalf("missing file should not be loaded")
	}
	if !strings.Contains(refs[0].Error, "no such file") {
		t.Fatalf("error = %q", refs[0].Error)
	}
}

func TestLoad_TruncatesLargeFile(t *testing.T) {
	tmp := t.TempDir()
	big := strings.Repeat("x", MaxFileSize+500)
	mustWrite(t, filepath.Join(tmp, "big.txt"), big)

	refs := Load([]Ref{{Token: "@big.txt", Path: "big.txt"}}, tmp)
	if !refs[0].Loaded {
		t.Fatalf("not loaded: %s", refs[0].Error)
	}
	if !strings.Contains(refs[0].Content, "truncated") {
		t.Fatalf("expected truncation marker; got tail %q", tail(refs[0].Content, 60))
	}
}

func TestLoad_DirectoryListing(t *testing.T) {
	tmp := t.TempDir()
	mustMkdir(t, filepath.Join(tmp, "sub"))
	mustWrite(t, filepath.Join(tmp, "sub", "a.go"), "package sub\n")
	mustWrite(t, filepath.Join(tmp, "sub", "b.txt"), "hello\n")

	refs := Load([]Ref{{Token: "@sub", Path: "sub"}}, tmp)
	if !refs[0].Loaded {
		t.Fatalf("dir not loaded: %s", refs[0].Error)
	}
	if !refs[0].IsDir {
		t.Fatalf("expected IsDir=true")
	}
	if !strings.Contains(refs[0].Content, "a.go") || !strings.Contains(refs[0].Content, "b.txt") {
		t.Fatalf("dir listing missing entries: %q", refs[0].Content)
	}
}

func TestBuildSection_FormatsMarkdown(t *testing.T) {
	refs := []Ref{
		{Token: "@hello.go", Path: "hello.go", Loaded: true, Content: "package main\n"},
		{Token: "@missing.go", Path: "missing.go", Error: "no such file or directory"},
	}
	got := BuildSection(refs)
	if !strings.HasPrefix(got, Marker) {
		t.Fatalf("missing marker: %s", got)
	}
	if !strings.Contains(got, "### @hello.go") {
		t.Fatalf("missing hello header")
	}
	if !strings.Contains(got, "```go") {
		t.Fatalf("missing language fence")
	}
	if !strings.Contains(got, "could not load: no such file") {
		t.Fatalf("missing error notice")
	}
}

func TestBuildSection_EmptyRefs(t *testing.T) {
	if BuildSection(nil) != "" {
		t.Fatal("expected empty")
	}
}

func TestInject_AddsAndReplaces(t *testing.T) {
	base := "You are an assistant.\nFollow the rules."

	injected := Inject(base, []Ref{{Token: "@x", Path: "x", Loaded: true, Content: "hi"}})
	if !strings.HasPrefix(injected, "You are an assistant.") {
		t.Fatalf("base prompt not preserved")
	}
	if !strings.Contains(injected, Marker) {
		t.Fatalf("marker missing")
	}

	// Re-inject with different refs replaces the old block.
	again := Inject(injected, []Ref{{Token: "@y", Path: "y", Loaded: true, Content: "yo"}})
	if strings.Count(again, Marker) != 1 {
		t.Fatalf("expected exactly one marker, got %d", strings.Count(again, Marker))
	}
	if strings.Contains(again, "### @x") {
		t.Fatalf("old ref still present after re-inject")
	}
	if !strings.Contains(again, "### @y") {
		t.Fatalf("new ref missing")
	}

	// Inject with no refs strips the existing block.
	cleared := Inject(again, nil)
	if strings.Contains(cleared, Marker) {
		t.Fatalf("marker should have been stripped")
	}
	if !strings.HasPrefix(cleared, "You are an assistant.") {
		t.Fatalf("base prompt lost: %s", cleared)
	}
}

func TestRewrite_StripsAtForLoadedRefs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		refs  []Ref
		want  string
	}{
		{
			name:  "no refs noop",
			input: "explain @main.go",
			refs:  nil,
			want:  "explain @main.go",
		},
		{
			name:  "single loaded ref strips @",
			input: "Can you evaluate this feature @docs/implement_bulk_forget.md",
			refs:  []Ref{{Token: "@docs/implement_bulk_forget.md", Path: "docs/implement_bulk_forget.md", Loaded: true}},
			want:  "Can you evaluate this feature docs/implement_bulk_forget.md",
		},
		{
			name:  "ref at start of line",
			input: "@main.go is the entry",
			refs:  []Ref{{Token: "@main.go", Path: "main.go", Loaded: true}},
			want:  "main.go is the entry",
		},
		{
			name:  "failed ref keeps @",
			input: "what about @missing.go",
			refs:  []Ref{{Token: "@missing.go", Path: "missing.go", Loaded: false, Error: "no such file"}},
			want:  "what about @missing.go",
		},
		{
			name:  "mixed loaded and failed",
			input: "compare @loaded.go and @missing.go please",
			refs: []Ref{
				{Token: "@loaded.go", Path: "loaded.go", Loaded: true},
				{Token: "@missing.go", Path: "missing.go", Loaded: false},
			},
			want: "compare loaded.go and @missing.go please",
		},
		{
			name:  "trailing punctuation preserved",
			input: "look at @main.go, please",
			refs:  []Ref{{Token: "@main.go", Path: "main.go", Loaded: true}},
			want:  "look at main.go, please",
		},
		{
			name:  "ref in parens",
			input: "see (@main.go) for details",
			refs:  []Ref{{Token: "@main.go", Path: "main.go", Loaded: true}},
			want:  "see (main.go) for details",
		},
		{
			name:  "email never matched, never rewritten",
			input: "ping user@example.com about @main.go",
			refs:  []Ref{{Token: "@main.go", Path: "main.go", Loaded: true}},
			want:  "ping user@example.com about main.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Rewrite(tt.input, tt.refs)
			if got != tt.want {
				t.Fatalf("Rewrite\nwant: %q\ngot:  %q", tt.want, got)
			}
		})
	}
}

func TestStripSection_NoopWhenAbsent(t *testing.T) {
	in := "You are an assistant.\nFollow the rules."
	if got := StripSection(in); got != in {
		t.Fatalf("unexpected change: %q", got)
	}
}

// helpers

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
