package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyntaxRangeCanonicalExactSpans(t *testing.T) {
	cases := []struct {
		name, file, source string
		pos                Position
		want               map[SyntaxKind]string
	}{
		{"go", "sample.go", "package p\nimport (\"fmt\")\ntype T struct { Name string }\nfunc (t T) M() { fmt.Println(t.Name) }\n", Position{Line: 3, Character: 30}, map[SyntaxKind]string{SyntaxKindMethod: "func (t T) M() { fmt.Println(t.Name) }", SyntaxKindCall: "fmt.Println(t.Name)", SyntaxKindFile: ""}},
		{"typescript", "sample.ts", "import {x} from 'm';\nclass T { name = 'x'; m() { x(this.name); } }\n", Position{Line: 1, Character: 36}, map[SyntaxKind]string{SyntaxKindType: "class T { name = 'x'; m() { x(this.name); } }", SyntaxKindMethod: "m() { x(this.name); }", SyntaxKindCall: "x(this.name)", SyntaxKindFile: ""}},
		{"python", "sample.py", "from pkg import x\nclass T:\n    name = 'x'\n    def m(self):\n        x(self.name)\n", Position{Line: 4, Character: 12}, map[SyntaxKind]string{SyntaxKindType: "class T:\n    name = 'x'\n    def m(self):\n        x(self.name)", SyntaxKindMethod: "def m(self):\n        x(self.name)", SyntaxKindCall: "x(self.name)", SyntaxKindFile: ""}},
		{"rust", "sample.rs", "use crate::x;\nstruct T { name: String }\nimpl T { fn m(&self) { x(self.name); } }\n", Position{Line: 2, Character: 27}, map[SyntaxKind]string{SyntaxKindMethod: "fn m(&self) { x(self.name); }", SyntaxKindCall: "x(self.name)", SyntaxKindFile: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.source), 0o600); err != nil {
				t.Fatal(err)
			}
			lang, ok := ResolveFile(path)
			if !ok {
				t.Fatal("language not resolved")
			}
			result, ok, err := SyntaxFileRanges(context.Background(), lang, path, tc.pos)
			if err != nil || !ok {
				t.Fatalf("SyntaxFileRanges: ok=%v err=%v", ok, err)
			}
			for kind, want := range tc.want {
				var found *SyntaxRange
				for i := range result.Ranges {
					if result.Ranges[i].Kind == kind {
						found = &result.Ranges[i]
						break
					}
				}
				if found == nil {
					t.Fatalf("missing %s in %#v", kind, result.Ranges)
				}
				if found.StartByte < 0 || found.EndByte < found.StartByte || found.EndByte > len(result.Source) {
					t.Fatalf("bad byte span %#v", found)
				}
				got := string(result.Source[found.StartByte:found.EndByte])
				if kind == SyntaxKindFile {
					want = tc.source
				}
				if got != want {
					t.Errorf("%s slice = %q, want %q", kind, got, want)
				}
			}
		})
	}
}

func TestSyntaxRangeGoFieldExcludesParameter(t *testing.T) {
	src := "package p\ntype T struct { Name string }\nfunc f(Name string) {}\n"
	path := filepath.Join(t.TempDir(), "field.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	lang, _ := ResolveFile(path)
	fieldResult, _, err := SyntaxFileRanges(context.Background(), lang, path, Position{Line: 1, Character: 18})
	if err != nil {
		t.Fatal(err)
	}
	if !hasSyntaxKind(fieldResult.Ranges, SyntaxKindField) {
		t.Fatalf("struct field missing: %#v", fieldResult.Ranges)
	}
	paramResult, _, err := SyntaxFileRanges(context.Background(), lang, path, Position{Line: 2, Character: 8})
	if err != nil {
		t.Fatal(err)
	}
	if hasSyntaxKind(paramResult.Ranges, SyntaxKindField) {
		t.Fatalf("parameter classified as field: %#v", paramResult.Ranges)
	}
}

func TestSyntaxRangeMalformedWarningsPreserveFile(t *testing.T) {
	cases := map[string]string{"broken.go": "package p\nfunc ok() {}\nfunc broken( {\n", "broken.ts": "function ok() {}\nfunction broken() {\n"}
	for name, src := range cases {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		lang, _ := ResolveFile(path)
		result, _, err := SyntaxFileRanges(context.Background(), lang, path, Position{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(result.Warnings) == 0 {
			t.Errorf("%s: expected warning", name)
		}
		if !hasSyntaxKind(result.Ranges, SyntaxKindFile) {
			t.Errorf("%s: missing file range", name)
		}
	}
}

func hasSyntaxKind(ranges []SyntaxRange, kind SyntaxKind) bool {
	for _, r := range ranges {
		if r.Kind == kind {
			return true
		}
	}
	return false
}

func TestSyntaxRangeKindsAreCanonical(t *testing.T) {
	src := "package p\nconst answer = 42\nvar name = \"x\"\n"
	path := filepath.Join(t.TempDir(), "kinds.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	lang, _ := ResolveFile(path)
	result, _, err := SyntaxFileRanges(context.Background(), lang, path, Position{Line: 1, Character: 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result.Ranges {
		if !isCanonicalSyntaxKind(r.Kind) {
			t.Fatalf("non-canonical kind %q in %#v", r.Kind, result.Ranges)
		}
	}
}

func TestSyntaxRangeUTF16AndCRLF(t *testing.T) {
	src := "package p\r\nfunc f() { println(\"🐾\") }\r\n"
	path := filepath.Join(t.TempDir(), "unicode.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	lang, _ := ResolveFile(path)
	result, _, err := SyntaxFileRanges(context.Background(), lang, path, Position{Line: 1, Character: 24})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(result.Source), "\r\n") {
		t.Fatal("snapshot normalized CRLF")
	}
	for _, r := range result.Ranges {
		start, err1 := OffsetForPosition(string(result.Source), r.Range.Start)
		end, err2 := OffsetForPosition(string(result.Source), r.Range.End)
		if err1 != nil || err2 != nil || start != r.StartByte || end != r.EndByte {
			t.Fatalf("coordinate mismatch: %#v", r)
		}
	}
}
