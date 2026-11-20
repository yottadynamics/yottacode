package filerefs

import (
	"bytes"
	"image"
	"image/png"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_QuotedEscapedAndURLPaths(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantPath  string
		wantToken string
	}{
		{"double quoted", `look at @"docs/my file.md" now`, "docs/my file.md", `@"docs/my file.md"`},
		{"single quoted", `look at @'docs/my file.md'.`, "docs/my file.md", `@'docs/my file.md'`},
		{"escaped space", `look at @docs/my\ file.md now`, "docs/my file.md", `@docs/my\ file.md`},
		{
			"file url",
			"see @file:///home/u/Pictures/Screenshot%20from%202026-11-20%2010-18-05.png ok",
			"/home/u/Pictures/Screenshot from 2026-11-20 10-18-05.png",
			"@file:///home/u/Pictures/Screenshot%20from%202026-11-20%2010-18-05.png",
		},
		{"absolute", "see @/etc/hosts", "/etc/hosts", "@/etc/hosts"},
		{"home", "see @~/notes.md,", "~/notes.md", "@~/notes.md"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			refs := Parse(tc.input)
			if len(refs) != 1 {
				t.Fatalf("refs = %+v", refs)
			}
			if refs[0].Path != tc.wantPath || refs[0].Token != tc.wantToken {
				t.Fatalf("got path=%q token=%q; want path=%q token=%q", refs[0].Path, refs[0].Token, tc.wantPath, tc.wantToken)
			}
		})
	}
}

func TestParse_UnterminatedQuoteFallsBackToPlain(t *testing.T) {
	refs := Parse(`see @"foo.go`)
	if len(refs) != 1 || refs[0].Path != `"foo.go` {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestParse_EmailStillIgnored(t *testing.T) {
	if refs := Parse(`mail me at a@"b c" or x@y.com`); len(refs) != 0 {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestRewrite_QuotedAndEscapedKeepPathStripAt(t *testing.T) {
	in := `a @"my dir/x y.md" b @plain.go, c @missing.go`
	refs := []Ref{
		{Path: "my dir/x y.md", Loaded: true},
		{Path: "plain.go", Loaded: true},
		{Path: "missing.go", Loaded: false},
	}
	got := Rewrite(in, refs)
	want := `a "my dir/x y.md" b plain.go, c @missing.go`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLoad_SpacedFileViaFileURL(t *testing.T) {
	cwd := t.TempDir()
	other := t.TempDir()
	p := filepath.Join(other, "Screenshot from 2026.txt")
	mustWrite(t, p, "shot")
	url := "file://" + strings.ReplaceAll(p, " ", "%20")
	refs := loadOut(Parse("look @"+url), cwd)
	if len(refs) != 1 || !refs[0].Loaded {
		t.Fatalf("refs = %+v", refs)
	}
}

// pngHeader is a valid tiny PNG used by attachment tests.
var pngHeader = func() []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		panic(err)
	}
	return buf.Bytes()
}()

func TestLoad_ImageIsAttachedNotInlined(t *testing.T) {
	cwd := t.TempDir()
	other := t.TempDir()
	p := filepath.Join(other, "Screenshot from now.png")
	if err := os.WriteFile(p, pngHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	refs := loadOut(Parse(`look at @"`+p+`"`), cwd)
	r := refs[0]
	if !r.Loaded || !r.IsImage || r.MediaType != "image/png" || len(r.Data) != len(pngHeader) {
		t.Fatalf("ref = %+v", r)
	}
	if r.Content != "" {
		t.Fatalf("image bytes leaked into Content: %q", r.Content)
	}
	imgs := Images(refs)
	if len(imgs) != 1 || imgs[0].MediaType != "image/png" || imgs[0].Name != "Screenshot from now.png" {
		t.Fatalf("images = %+v", imgs)
	}
	sec := BuildSection(refs)
	if !strings.Contains(sec, "attached to the user message") || strings.Contains(sec, "PNG") {
		t.Fatalf("section = %q", sec)
	}
}

func TestLoad_ImageInsideCwd(t *testing.T) {
	cwd := t.TempDir()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "a.JPG"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	refs := Load(Parse("@a.JPG"), cwd)
	if !refs[0].IsImage || refs[0].MediaType != "image/jpeg" {
		t.Fatalf("ref = %+v", refs[0])
	}
}

func TestLoad_ImageBeyondSourceCapRejected(t *testing.T) {
	cwd := t.TempDir()
	f, err := os.Create(filepath.Join(cwd, "big.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxImageSourceSize + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	refs := Load(Parse("@big.png"), cwd)
	if refs[0].Loaded || !strings.Contains(refs[0].Error, "limit") {
		t.Fatalf("ref = %+v", refs[0])
	}
}

func TestLoad_BinaryFileRejected(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "blob.bin"), []byte("ab\x00cd"), 0o600); err != nil {
		t.Fatal(err)
	}
	refs := Load(Parse("@blob.bin"), cwd)
	if refs[0].Loaded || !strings.Contains(refs[0].Error, "binary") {
		t.Fatalf("ref = %+v", refs[0])
	}
}

func TestRejectImages(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "a.png"), pngHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(cwd, "b.txt"), "text")
	refs := RejectImages(Load(Parse("@a.png @b.txt"), cwd), "no vision")
	if refs[0].Loaded || refs[0].Error != "no vision" || refs[0].Data != nil {
		t.Fatalf("image ref = %+v", refs[0])
	}
	if !refs[1].Loaded {
		t.Fatalf("text ref should be untouched: %+v", refs[1])
	}
	if len(Images(refs)) != 0 {
		t.Fatal("rejected image still returned")
	}
}
