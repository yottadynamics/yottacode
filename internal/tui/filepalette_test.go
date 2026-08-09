package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestExtractFileQuery(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantQuery string
		wantAt    int
		wantFound bool
	}{
		{"empty", "", "", -1, false},
		{"plain text", "hello world", "", -1, false},
		{"bare @", "look at @", "", 8, true},
		{"@ start", "@main", "main", 0, true},
		{"@ after space", "explain @main", "main", 8, true},
		{"@ with path", "see @internal/tui/foo", "internal/tui/foo", 4, true},
		{"@ in middle ignored",
			"explain @first then more text", "", -1, false}, // committed token, palette closed
		{"email not a ref", "ping user@example.com", "", -1, false},
		{"double @", "@@", "", -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, at, found := extractFileQuery(tt.input)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if q != tt.wantQuery {
				t.Fatalf("query = %q, want %q", q, tt.wantQuery)
			}
			if at != tt.wantAt {
				t.Fatalf("atIdx = %d, want %d", at, tt.wantAt)
			}
		})
	}
}

func TestWalkFiles_SkipsBlocklistedDirs(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "main.go"), "")
	mustMkdirAll(t, filepath.Join(root, ".git"))
	mustWriteFile(t, filepath.Join(root, ".git", "HEAD"), "")
	mustMkdirAll(t, filepath.Join(root, "node_modules", "pkg"))
	mustWriteFile(t, filepath.Join(root, "node_modules", "pkg", "x.js"), "")
	mustMkdirAll(t, filepath.Join(root, "src"))
	mustWriteFile(t, filepath.Join(root, "src", "lib.go"), "")

	entries := walkFiles(root)
	paths := pathsOf(entries)
	sort.Strings(paths)

	wantPresent := []string{"main.go", "src", "src/lib.go"}
	for _, w := range wantPresent {
		if !contains(paths, w) {
			t.Errorf("expected %q in walk, got %v", w, paths)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, ".git") {
			t.Errorf("blocklisted dir leaked: %q", p)
		}
		if strings.HasPrefix(p, "node_modules") {
			t.Errorf("blocklisted dir leaked: %q", p)
		}
	}
}

func TestFilterFilePalette_RanksBetterMatchesFirst(t *testing.T) {
	entries := []fileEntry{
		{Path: "internal/tui/main.go"},
		{Path: "main.go"},
		{Path: "cmd/yottacode/main_test.go"},
		{Path: "README.md"},
	}
	got := filterFilePalette("main", entries)
	if len(got) == 0 {
		t.Fatal("expected matches")
	}
	// Top hit should be the basename "main" prefix at the shallowest path.
	if got[0].Path != "main.go" {
		t.Fatalf("top hit = %q, want main.go (top of ranking)", got[0].Path)
	}
}

func TestFilterFilePalette_SubsequenceMatch(t *testing.T) {
	entries := []fileEntry{
		{Path: "internal/cli/options.go"},
		{Path: "completely/unrelated.go"},
	}
	got := filterFilePalette("iclop", entries)
	if len(got) != 1 || got[0].Path != "internal/cli/options.go" {
		t.Fatalf("expected subsequence hit, got %+v", got)
	}
}

func TestFilterFilePalette_ReturnsAllMatches(t *testing.T) {
	// Build a filtered set larger than the visible window. The palette
	// is windowed at render time, not truncated at filter time, so the
	// caller must receive every match — otherwise Up/Down can't reach
	// entries beyond filePaletteVisible.
	entries := make([]fileEntry, 0, filePaletteVisible*2)
	for i := 0; i < filePaletteVisible*2; i++ {
		entries = append(entries, fileEntry{Path: fmt.Sprintf("match_%02d.go", i)})
	}
	got := filterFilePalette("match", entries)
	if len(got) != len(entries) {
		t.Fatalf("filter returned %d matches; want %d (no truncation)", len(got), len(entries))
	}
}

func TestRenderFilePalette_ShowsOverflowHints(t *testing.T) {
	items := make([]fileEntry, 0, filePaletteVisible*2)
	for i := 0; i < filePaletteVisible*2; i++ {
		items = append(items, fileEntry{Path: fmt.Sprintf("file_%02d.go", i)})
	}
	// Window at the top: only "▼ N more" appears.
	out := renderFilePalette(items, 0, 0, 80)
	if !strings.Contains(out, "more") || strings.Contains(out, "▲") {
		t.Fatalf("top window: want ▼ overflow only, got:\n%s", out)
	}
	// Window in the middle: both arrows should appear.
	out = renderFilePalette(items, filePaletteVisible, filePaletteVisible-2, 80)
	if !strings.Contains(out, "▲") || !strings.Contains(out, "▼") {
		t.Fatalf("middle window: want both arrows, got:\n%s", out)
	}
	// Window at the bottom: only "▲ N more" appears.
	bottomOffset := len(items) - filePaletteVisible
	out = renderFilePalette(items, len(items)-1, bottomOffset, 80)
	if !strings.Contains(out, "▲") || strings.Contains(out, "▼") {
		t.Fatalf("bottom window: want ▲ overflow only, got:\n%s", out)
	}
	// Short list: no overflow hints.
	out = renderFilePalette(items[:3], 0, 0, 80)
	if strings.Contains(out, "more") {
		t.Fatalf("short list should not render overflow hints, got:\n%s", out)
	}
}

func TestModel_FilePaletteScrollsPastWindow(t *testing.T) {
	cwd := t.TempDir()
	// Seed enough files that the filtered set overflows the window.
	count := filePaletteVisible + 4
	for i := 0; i < count; i++ {
		mustWriteFile(t, filepath.Join(cwd, fmt.Sprintf("seedfile_%02d.go", i)), "")
	}
	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range []rune("@seedfile") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.filePaletteOpen {
		t.Fatalf("setup: palette should be open after typing @seedfile")
	}
	if got := len(m.filePaletteFiltered); got != count {
		t.Fatalf("filtered length = %d, want %d (no truncation)", got, count)
	}
	if m.filePaletteOffset != 0 {
		t.Fatalf("initial offset = %d, want 0", m.filePaletteOffset)
	}
	// Down past the visible window should advance the offset.
	for i := 0; i < filePaletteVisible; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.filePaletteIndex != filePaletteVisible {
		t.Fatalf("index after %d downs = %d, want %d", filePaletteVisible, m.filePaletteIndex, filePaletteVisible)
	}
	if m.filePaletteOffset != 1 {
		t.Fatalf("offset after scrolling past window = %d, want 1", m.filePaletteOffset)
	}
	// Up past the offset should pull it back.
	for i := 0; i < filePaletteVisible; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if m.filePaletteIndex != 0 || m.filePaletteOffset != 0 {
		t.Fatalf("after scrolling back to top, index=%d offset=%d, want 0/0", m.filePaletteIndex, m.filePaletteOffset)
	}
}

func TestModel_AtKeyOpensFilePalette(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "main.go"), "package main\n")
	mustWriteFile(t, filepath.Join(cwd, "README.md"), "# yo\n")

	m := newTestModel(t)
	m.cwd = cwd
	// Type "@" — should open the file palette.
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "@"})
	if !m.filePaletteOpen {
		t.Fatalf("typing @ should open file palette")
	}
	if len(m.filePaletteFiltered) == 0 {
		t.Fatalf("file palette should have entries; got 0")
	}
}

func TestModel_AtKeyDoesNotOpenPaletteOnEmail(t *testing.T) {
	cwd := t.TempDir()
	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range []rune("user@") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if m.filePaletteOpen {
		t.Fatalf("user@ should not open palette (no whitespace before @)")
	}
}

func TestModel_TabAcceptsFilePaletteChoice(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "main.go"), "")
	mustWriteFile(t, filepath.Join(cwd, "other.go"), "")

	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range []rune("@main") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.filePaletteOpen {
		t.Fatalf("file palette should be open after @main")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab})

	got := m.textInput.Value()
	if !strings.HasPrefix(got, "@main.go ") {
		t.Fatalf("Tab should splice chosen path; got %q", got)
	}
	if m.filePaletteOpen {
		t.Fatalf("file palette should close after picking a file")
	}
}

func TestModel_EscClosesFilePaletteWithoutWipingInput(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "main.go"), "")

	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range []rune("@ma") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.filePaletteOpen {
		t.Fatalf("setup: palette should be open")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.filePaletteOpen {
		t.Fatalf("Esc should close the palette")
	}
	if got := m.textInput.Value(); got != "@ma" {
		t.Fatalf("Esc should preserve input; got %q", got)
	}
}

// helpers

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func pathsOf(entries []fileEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
