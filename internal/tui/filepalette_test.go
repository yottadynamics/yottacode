package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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

func TestFilterFilePalette_PathQueryStaysWithinDirectory(t *testing.T) {
	entries := []fileEntry{
		{Path: "roadmap/plan.md"},
		{Path: "roadmap/implemented/release.md"},
		{Path: ".claude/worktrees/approval-readability/internal/codemap/builder.go"},
	}
	got := filterFilePalette("roadmap/", entries)
	if len(got) != 2 || got[0].Path != "roadmap/plan.md" || got[1].Path != "roadmap/implemented/release.md" {
		t.Fatalf("path query returned unrelated entries: %+v", got)
	}
}

func TestFilterFilePalette_PathQueryMatchesDirectoryPrefixCaseInsensitively(t *testing.T) {
	entries := []fileEntry{{Path: "Roadmap/Plan.md"}, {Path: "other/roadmap.md"}}
	got := filterFilePalette("roadmap/", entries)
	if len(got) != 1 || got[0].Path != "Roadmap/Plan.md" {
		t.Fatalf("path query = %+v, want only Roadmap/Plan.md", got)
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

func TestFilterFilePalette_FuzzyInitialsRankAboveLooseSubsequence(t *testing.T) {
	entries := []fileEntry{
		{Path: "internal/tui/mouse_picker.go"},
		{Path: "internal/tui/model_picker.go"},
		{Path: "internal/tui/mostly/possible.go"},
	}
	got := filterFilePalette("mp", entries)
	if len(got) < 2 {
		t.Fatalf("expected fuzzy file matches, got %+v", got)
	}
	if got[0].Path != "internal/tui/model_picker.go" && got[0].Path != "internal/tui/mouse_picker.go" {
		t.Fatalf("mp should prioritize word-initial fuzzy matches; got %+v", got)
	}
	if got[len(got)-1].Path != "internal/tui/mostly/possible.go" {
		t.Fatalf("loose subsequence should rank below word-initial matches; got %+v", got)
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
	mustWriteFile(t, filepath.Join(cwd, ".env"), "SECRET=x\n")
	mustWriteFile(t, filepath.Join(cwd, "server.pem"), "PRIVATE KEY\n")
	mustWriteFile(t, filepath.Join(cwd, "id_ed25519"), "PRIVATE KEY\n")

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
	for _, entry := range m.filePaletteEntries {
		if entry.Path == ".env" || entry.Path == "server.pem" || entry.Path == "id_ed25519" {
			t.Fatalf("sensitive file leaked into picker: %q", entry.Path)
		}
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

// Regression: the footer promises "Tab/Enter: attach", but Enter used to
// submit the half-typed "@ocp-deplop" text instead of attaching the
// highlighted file (which sat in a subdirectory).
func TestModel_EnterAcceptsFilePaletteChoice(t *testing.T) {
	cwd := t.TempDir()
	mustMkdirAll(t, filepath.Join(cwd, "ocp-deployment"))
	mustWriteFile(t, filepath.Join(cwd, "ocp-deployment", "RFC-sizing.md"), "# rfc\n")
	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range []rune("@ocp-deplo") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.filePaletteOpen {
		t.Fatal("setup: palette should be open")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.turnActive {
		t.Fatalf("Enter submitted a turn instead of accepting the choice")
	}
	got := m.textInput.Value()
	if got != "@ocp-deployment/" && got != "@ocp-deployment/RFC-sizing.md " {
		t.Fatalf("input after Enter = %q", got)
	}
}

func TestModel_EnterWithNoMatchStillSubmits(t *testing.T) {
	m := newTestModel(t)
	m.cwd = t.TempDir()
	for _, r := range []rune("@zzzz") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	m.filePaletteFiltered = nil
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.filePaletteOpen {
		t.Fatal("palette should be closed after Enter with no match")
	}
}

func TestModel_PaletteQuotesPathsWithSpaces(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "my notes.md"), "")
	m := newTestModel(t)
	m.cwd = cwd
	for _, r := range []rune("@my") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.textInput.Value(); got != `@"my notes.md" ` {
		t.Fatalf("input = %q", got)
	}
}

func TestExtractFileQuery_QuotedAndEscapedSpaces(t *testing.T) {
	tests := []struct {
		in, wantQuery string
		wantFound     bool
	}{
		{`look @"my dir/fi`, `"my dir/fi`, true},
		{`look @my\ dir/fi`, `my\ dir/fi`, true},
		{`look @"my dir/file.md" `, "", false},
		{`look @"my dir/file.md" and more`, "", false},
	}
	for _, tt := range tests {
		q, _, found := extractFileQuery(tt.in)
		if found != tt.wantFound || (found && q != tt.wantQuery) {
			t.Errorf("extractFileQuery(%q) = %q,%v; want %q,%v", tt.in, q, found, tt.wantQuery, tt.wantFound)
		}
	}
}

func TestListExternal_BrowsesOutsideCwdNewestFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shots := filepath.Join(home, "Pictures", "Screenshots")
	mustMkdirAll(t, shots)
	old := filepath.Join(shots, "Screenshot from old.png")
	newer := filepath.Join(shots, "Screenshot from new.png")
	mustWriteFile(t, old, "")
	mustWriteFile(t, newer, "")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(shots, ".hidden.png"), "")

	got := listExternal("~/Pictures/Screenshots/Screenshot", t.TempDir())
	if len(got) != 2 {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].Path != "~/Pictures/Screenshots/Screenshot from new.png" {
		t.Fatalf("newest first expected, got %+v", got)
	}
	if dirs := listExternal("~/Pic", t.TempDir()); len(dirs) != 1 || !dirs[0].IsDir || dirs[0].Path != "~/Pictures" {
		t.Fatalf("dir listing = %+v", dirs)
	}
}

func TestModel_PaletteBrowsesHomeAndAttachesSpacedFile(t *testing.T) {
	m := newTestModel(t)
	// newTestModel isolates HOME itself; set ours after it.
	home := t.TempDir()
	t.Setenv("HOME", home)
	shots := filepath.Join(home, "Pictures", "Screenshots")
	mustMkdirAll(t, shots)
	mustWriteFile(t, filepath.Join(shots, "Screenshot from 1.png"), "")
	m.cwd = t.TempDir()
	for _, r := range []rune("@~/Pictures/Screenshots/Scr") {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.filePaletteOpen || len(m.filePaletteFiltered) != 1 {
		t.Fatalf("palette open=%v filtered=%+v", m.filePaletteOpen, m.filePaletteFiltered)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	want := `@"~/Pictures/Screenshots/Screenshot from 1.png" `
	if got := m.textInput.Value(); got != want {
		t.Fatalf("input = %q, want %q", got, want)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
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
