package filerefs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQuote_RoundTripsThroughParse(t *testing.T) {
	for _, p := range []string{
		"plain/file.go",
		"my dir/file name.md",
		`has "dq" and space.md`,
		`has "dq" and 'sq' space.md`,
	} {
		refs := Parse("see @" + Quote(p) + " now")
		if len(refs) != 1 || refs[0].Path != p {
			t.Errorf("Quote(%q) = %q parsed back as %+v", p, Quote(p), refs)
		}
	}
}

func TestLoad_TrailingDotRetriedWithoutIt(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "b.go"), "package b\n")

	in := "explain @b.go."
	refs := Load(Parse(in), cwd)
	if !refs[0].Loaded || !strings.Contains(refs[0].Content, "package b") {
		t.Fatalf("trailing-dot ref not loaded: %+v", refs[0])
	}
	if got := Rewrite(in, refs); got != "explain b.go." {
		t.Fatalf("Rewrite = %q", got)
	}
	if sec := BuildSection(refs); !strings.Contains(sec, "```go") {
		t.Fatalf("language tag lost for trailing-dot ref: %q", sec)
	}
}

func TestLoad_ExistingDottedNameWinsOverTrim(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "v1."), "dotted")
	mustWrite(t, filepath.Join(cwd, "v1"), "bare")
	refs := Load(Parse("@v1."), cwd)
	if !refs[0].Loaded || refs[0].Content != "dotted" {
		t.Fatalf("existing path must not be reinterpreted: %+v", refs[0])
	}
}

func TestLoad_TrailingDotCannotBypassSensitiveCheck(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "id_rsa"), "PRIVATE")
	refs := Load(Parse("@id_rsa."), cwd)
	if refs[0].Loaded {
		t.Fatalf("sensitive file loaded through trailing-dot retry: %+v", refs[0])
	}
}
