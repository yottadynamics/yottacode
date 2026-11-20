package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/filerefs"
)

var specialNames = []string{
	"app/[id].tsx", "app/(group)/page.tsx", "report (1).pdf", "a@b.txt",
	`has "dq" and space.md`, "app/[...slug]/page.tsx", "my notes.md", "plain.go",
}

// Regression: the picker inserted "app/[id].tsx" unquoted and the parser
// silently truncated it to "app/" (attaching a directory listing).
func TestPaletteInsert_FilesRoundTripThroughParser(t *testing.T) {
	for _, name := range specialNames {
		ins := paletteInsert(fileEntry{Path: name})
		refs := filerefs.Parse("@" + ins)
		if len(refs) != 1 || refs[0].Path != name {
			t.Errorf("insert %q for %q parsed as %+v", ins, name, refs)
		}
	}
}

func TestPaletteInsert_DirsStayOpenForDrilling(t *testing.T) {
	for _, name := range []string{"app", "my dir", "(group)", "[id]", `q"d dir`} {
		ins := paletteInsert(fileEntry{Path: name, IsDir: true})
		if !strings.HasSuffix(ins, "/") {
			t.Errorf("dir insert %q should end with /", ins)
		}
		if got := decodePaletteQuery(ins); got != name+"/" {
			t.Errorf("decodePaletteQuery(%q) = %q, want %q", ins, got, name+"/")
		}
		// The raw text must still be one active token for the picker.
		if q, _, found := extractFileQuery("look @" + ins); !found || q != ins {
			t.Errorf("extractFileQuery lost the token for %q: q=%q found=%v", ins, q, found)
		}
	}
}

func TestExtractFileQuery_BracketsAndParensInsideToken(t *testing.T) {
	tests := []struct {
		in, wantQuery string
		wantFound     bool
	}{
		{"@app/[id", "app/[id", true},
		{"look @app/(group)/pa", "app/(group)/pa", true},
		{`look @app/\(group\)/pa`, `app/\(group\)/pa`, true},
		{`look @"app/(group)/pa`, `"app/(group)/pa`, true},
		{`look @"app/(group)/page.tsx" `, "", false},
		{"(@foo", "foo", true},
		{"see [@foo", "foo", true},
		{"@a,@b", "b", true},
	}
	for _, tt := range tests {
		q, _, found := extractFileQuery(tt.in)
		if found != tt.wantFound || (found && q != tt.wantQuery) {
			t.Errorf("extractFileQuery(%q) = %q,%v; want %q,%v", tt.in, q, found, tt.wantQuery, tt.wantFound)
		}
	}
}

// End to end through the model: type a bracketed path, accept, and the
// resulting reference parses back to exactly that file.
func TestModel_PaletteAcceptsBracketedRouteFile(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "app", "[id].tsx"), "")
	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range "@app/[id" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.filePaletteOpen || len(m.filePaletteFiltered) == 0 {
		t.Fatalf("palette should stay open while typing a bracketed path: open=%v", m.filePaletteOpen)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab})
	refs := filerefs.Parse(m.textInput.Value())
	if len(refs) != 1 || refs[0].Path != "app/[id].tsx" {
		t.Fatalf("input %q parsed as %+v", m.textInput.Value(), refs)
	}
}

func TestWalkFiles_PerDirCapKeepsSiblingsAndFlagsCapped(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < fileWalkMaxPerDir+50; i++ {
		mustWriteFile(t, filepath.Join(root, "big", fmt.Sprintf("f%03d.txt", i)), "")
	}
	mustWriteFile(t, filepath.Join(root, "zdir", "keep.go"), "")

	entries, capped := walkFilesCapped(root)
	if !capped {
		t.Fatal("a 450-file directory should flag the walk as capped")
	}
	inBig, sawKeep := 0, false
	for _, e := range entries {
		if strings.HasPrefix(e.Path, "big/") {
			inBig++
		}
		if e.Path == "zdir/keep.go" {
			sawKeep = true
		}
	}
	if inBig != fileWalkMaxPerDir || !sawKeep {
		t.Fatalf("big=%d (want %d) zdir/keep.go=%v", inBig, fileWalkMaxPerDir, sawKeep)
	}
}

// Files the caps dropped stay reachable: a path-style query lists that
// directory live.
func TestPalette_CappedWalkFallsBackToLiveListing(t *testing.T) {
	cwd := t.TempDir()
	for i := 0; i < fileWalkMaxPerDir+50; i++ {
		mustWriteFile(t, filepath.Join(cwd, "big", fmt.Sprintf("f%03d.txt", i)), "")
	}
	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range "@big/f449" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !hasPaletteEntry(m, "big/f449.txt") {
		t.Fatalf("file beyond the per-dir cap not reachable: %+v", m.filePaletteFiltered)
	}
}

// Regression: with a slash in the query only whole-directory prefixes
// matched, so "@docs/RE" never found docs/README.md.
func TestFilterFilePalette_PartialSegmentAfterSlashPrefixMatches(t *testing.T) {
	entries := []fileEntry{
		{Path: "docs", IsDir: true}, {Path: "docs/README.md"}, {Path: "docs/guide.md"},
		{Path: "docs-old/readme.md"}, {Path: "src/docs/README.md"},
	}
	got := filterFilePalette("docs/RE", entries)
	if len(got) != 1 || got[0].Path != "docs/README.md" {
		t.Fatalf("docs/RE = %+v", got)
	}
	// A trailing slash still scopes to that directory's descendants only.
	got = filterFilePalette("docs/", entries)
	for _, e := range got {
		if strings.HasPrefix(e.Path, "docs-old") || strings.HasPrefix(e.Path, "src/") {
			t.Fatalf("docs/ leaked %q", e.Path)
		}
	}
}

func TestWalkFiles_BreadthFirstShedsDeepestFirst(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "top.go"), "")
	mustWriteFile(t, filepath.Join(root, "a", "b", "c", "deep.go"), "")
	entries := walkFiles(root)
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Path] = true
	}
	for _, p := range []string{"top.go", "a", "a/b", "a/b/c", "a/b/c/deep.go"} {
		if !have[p] {
			t.Errorf("missing %q in %+v", p, entries)
		}
	}
}

// The picker must not offer what attaching would refuse.
func TestListExternal_HidesCredentialLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(home, ".ssh", "config"), "")
	mustWriteFile(t, filepath.Join(home, ".aws", "credentials"), "")
	mustWriteFile(t, filepath.Join(home, ".npmrc"), "")
	mustWriteFile(t, filepath.Join(home, ".bashrc"), "")
	mustWriteFile(t, filepath.Join(home, "notes.txt"), "")

	got := map[string]bool{}
	for _, e := range listExternal("~/.", t.TempDir()) {
		got[e.Path] = true
	}
	for _, hidden := range []string{"~/.ssh", "~/.aws", "~/.npmrc"} {
		if got[hidden] {
			t.Errorf("%s must not be offered", hidden)
		}
	}
	if !got["~/.bashrc"] {
		t.Errorf("benign dotfile should be offered: %v", got)
	}
}

// Both Tab and Enter accept the highlighted entry while a turn runs too.
func TestModel_MidTurnPaletteAcceptsWithEnterAndTab(t *testing.T) {
	for _, key := range []rune{tea.KeyEnter, tea.KeyTab} {
		cwd := t.TempDir()
		mustWriteFile(t, filepath.Join(cwd, "main.go"), "")
		m := newTestModel(t)
		m.cwd = cwd
		m.turnActive = true
		for _, r := range "@main" {
			m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
		}
		if !m.filePaletteOpen {
			t.Fatalf("setup: palette should open mid-turn")
		}
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: key})
		if got := m.textInput.Value(); got != "@main.go " {
			t.Fatalf("key %v: input = %q", key, got)
		}
	}
}

func TestDiscardQueuedImages_SaysSoInsteadOfDroppingSilently(t *testing.T) {
	m := newTestModel(t)
	m.pastedImages = map[string]adapter.ImageBlock{"[Image #1: a.png]": {Data: []byte{1}}}
	m.discardQueuedImages()
	if m.pastedImages != nil {
		t.Fatal("pasted images should be cleared")
	}
	if got := m.transcript.String(); !strings.Contains(got, "queued mid-turn") {
		t.Fatalf("no notice: %q", got)
	}
	// And no notice when there was nothing to drop.
	m2 := newTestModel(t)
	m2.discardQueuedImages()
	if strings.Contains(m2.transcript.String(), "queued mid-turn") {
		t.Fatal("spurious notice")
	}
}
