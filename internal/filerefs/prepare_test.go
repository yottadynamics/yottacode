package filerefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadOut is Load with outside-cwd references enabled (interactive TUI).
func loadOut(refs []Ref, cwd string) []Ref {
	return LoadWith(refs, cwd, LoadOptions{AllowOutside: true})
}

func TestLoad_ConfinedByDefault(t *testing.T) {
	cwd := t.TempDir()
	other := t.TempDir()
	abs := filepath.Join(other, "notes.txt")
	mustWrite(t, abs, "outside")
	mustWrite(t, filepath.Join(cwd, "in.txt"), "inside")

	for _, path := range []string{abs, "../" + filepath.Base(other) + "/notes.txt"} {
		r := Load([]Ref{{Token: "@" + path, Path: path}}, cwd)[0]
		if r.Loaded || !strings.Contains(r.Error, "outside the working directory") {
			t.Errorf("%q should be refused in confined mode: %+v", path, r)
		}
	}
	// An absolute path that resolves INSIDE cwd is fine.
	in := filepath.Join(cwd, "in.txt")
	if r := Load([]Ref{{Token: "@" + in, Path: in}}, cwd)[0]; !r.Loaded || r.Outside {
		t.Errorf("absolute path inside cwd should load: %+v", r)
	}
}

func TestLoad_OutsideRefIsFlagged(t *testing.T) {
	cwd, other := t.TempDir(), t.TempDir()
	p := filepath.Join(other, "a.txt")
	mustWrite(t, p, "x")
	r := loadOut([]Ref{{Token: "@" + p, Path: p}}, cwd)[0]
	if !r.Loaded || !r.Outside {
		t.Fatalf("ref = %+v", r)
	}
	if text, isErr := Describe(r); isErr || !strings.Contains(text, "outside project") {
		t.Fatalf("Describe = %q (err=%v)", text, isErr)
	}
}

func TestPrepare_AttachesToUserMessageAndStripsBack(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "a.go"), "package a\n")
	in := "explain @a.go and @missing.go"
	p := Prepare(in, PrepareOptions{Cwd: cwd, AllowImages: true})

	if p.Text != "explain a.go and @missing.go" {
		t.Fatalf("Text = %q", p.Text)
	}
	if !strings.HasPrefix(p.Content, p.Text+"\n\n"+Marker) || !strings.Contains(p.Content, "package a") {
		t.Fatalf("Content = %q", p.Content)
	}
	if !strings.Contains(p.Content, "could not load") {
		t.Fatalf("failed ref must be annotated: %q", p.Content)
	}
	if got := StripAttachment(p.Content); got != p.Text {
		t.Fatalf("StripAttachment = %q, want %q", got, p.Text)
	}
}

func TestPrepare_NoRefsIsIdentity(t *testing.T) {
	p := Prepare("just words, mail a@b.com", PrepareOptions{Cwd: t.TempDir()})
	if p.Content != "just words, mail a@b.com" || len(p.Refs) != 0 {
		t.Fatalf("prepared = %+v", p)
	}
}

func TestPrepare_ImagesRejectedWhenUnsupported(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "a.png"), pngHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	p := Prepare("see @a.png", PrepareOptions{Cwd: cwd, ImageRejection: "no vision"})
	if len(p.Images) != 0 || !strings.Contains(p.Content, "no vision") {
		t.Fatalf("prepared = %+v", p)
	}
	p = Prepare("see @a.png", PrepareOptions{Cwd: cwd, AllowImages: true})
	if len(p.Images) != 1 {
		t.Fatalf("images = %d", len(p.Images))
	}
}

func TestStripSection_LegacySystemPrompt(t *testing.T) {
	sys := "You are an assistant.\n\n" + Marker + "\n\n### @x\n```\nhi\n```"
	if got := StripSection(sys); got != "You are an assistant." {
		t.Fatalf("StripSection = %q", got)
	}
	if got := StripSection("no block"); got != "no block" {
		t.Fatalf("StripSection = %q", got)
	}
}

func TestLoadTimeout_ReturnsPromptlyAndLoadsFastRefs(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "a.txt"), "ok")
	refs := LoadTimeout([]Ref{{Token: "@a.txt", Path: "a.txt"}}, cwd, LoadOptions{}, time.Second)
	if !refs[0].Loaded {
		t.Fatalf("fast load failed: %+v", refs[0])
	}
}

func TestImageCaps(t *testing.T) {
	cwd := t.TempDir()
	// 4 MiB x 6: the 5th pushes the text total past 20 MiB.
	var toks []string
	for i := 0; i < 6; i++ {
		name := string(rune('a'+i)) + ".txt"
		f, err := os.Create(filepath.Join(cwd, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(strings.Repeat("x", 4<<20)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		toks = append(toks, "@"+name)
	}
	refs := Load(Parse(strings.Join(toks, " ")), cwd)
	loaded, rejected := 0, 0
	for _, r := range refs {
		if r.Loaded {
			loaded++
		} else if strings.Contains(r.Error, "total reference budget") {
			rejected++
		}
	}
	if loaded != 5 || rejected != 1 {
		t.Fatalf("loaded=%d rejected=%d refs=%+v", loaded, rejected, refs)
	}

	// Image count cap.
	cwd2 := t.TempDir()
	var toks2 []string
	for i := 0; i < MaxImagesPerTurn+1; i++ {
		name := string(rune('a'+i)) + ".png"
		if err := os.WriteFile(filepath.Join(cwd2, name), pngHeader, 0o600); err != nil {
			t.Fatal(err)
		}
		toks2 = append(toks2, "@"+name)
	}
	refs = Load(Parse(strings.Join(toks2, " ")), cwd2)
	if last := refs[len(refs)-1]; last.Loaded || !strings.Contains(last.Error, "at most") {
		t.Fatalf("last = %+v", last)
	}
}

func TestSensitiveList(t *testing.T) {
	for _, p := range []string{".env", ".env.local", "a/.npmrc", ".pypirc", ".git-credentials", ".pgpass",
		"infra/terraform.tfstate", "x.tfstate.backup", "id_rsa", "k.PEM"} {
		if !IsSensitivePath(p) {
			t.Errorf("%q should be sensitive", p)
		}
	}
	for _, p := range []string{"main.go", "env.go", "keys.md", "README.md"} {
		if IsSensitivePath(p) {
			t.Errorf("%q should not be sensitive", p)
		}
	}
}

func TestQuoteRoundTrip_SpecialNames(t *testing.T) {
	for _, p := range []string{
		"app/[id].tsx", "app/(group)/page.tsx", "report (1).pdf", "a@b.txt",
		`has "dq".md`, `it's.md`, `both "dq" and 'sq'.md`, `back\slash.md`,
		"app/[...slug]/page.tsx", "plain.go", "my notes.md",
	} {
		refs := Parse("see @" + Quote(p) + " and more")
		if len(refs) != 1 || refs[0].Path != p {
			t.Errorf("Quote(%q) = %q parsed back as %+v", p, Quote(p), refs)
		}
	}
}

func TestReadBounded_RefusesSymlinkAndFIFO(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t.txt")
	mustWrite(t, target, "x")
	link := filepath.Join(dir, "l.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, _, err := readBounded(link, 10); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink open err = %v", err)
	}
}
