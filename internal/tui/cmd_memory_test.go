package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/memory"
)

func seedUserFile(t *testing.T, name, body string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	dir := filepath.Join(home, ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func seedUserMemoryFile(t *testing.T, name, body string) string {
	t.Helper()
	dir, err := memory.UserMemoryDir()
	if err != nil {
		t.Fatalf("UserMemoryDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name+".md")
	contents := "---\nname: " + name + "\ntype: reference\ndescription: x\n---\n" + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func seedProjectMemoryFile(t *testing.T, cwd, name, body string) string {
	t.Helper()
	dir, err := memory.ProjectMemoryDir(cwd)
	if err != nil {
		t.Fatalf("ProjectMemoryDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name+".md")
	contents := "---\nname: " + name + "\ntype: project\ndescription: x\n---\n" + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestSlash_MemoryOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	if !m.memoryPickerOpen || m.memoryPicker == nil {
		t.Fatalf("/memory should open the picker; open=%v picker=%v",
			m.memoryPickerOpen, m.memoryPicker != nil)
	}
	if m.memoryPicker.cursor != 0 {
		t.Errorf("picker should open with cursor on row 0; got %d", m.memoryPicker.cursor)
	}
}

// /memory search <query> ranks saved memories (BM25) and shows them in an
// interactive overlay (scroll + open) — results render in the overlay,
// NOT the session transcript. Only matching entries (score > 0) appear.
func TestSlash_MemorySearchOpensInteractiveResults(t *testing.T) {
	m := newTestModel(t)
	seedUserMemoryFile(t, "queue-writes", "database writes go behind a queue for durability")
	seedUserMemoryFile(t, "flush-left", "the TUI shares a column-0 left edge")

	m, _ = m.runSlash("/memory search database queue")

	if !m.memoryPickerOpen || m.memoryPicker == nil {
		t.Fatalf("/memory search should open the results overlay")
	}
	if m.memoryPicker.mode != memorySearchMode {
		t.Errorf("expected search mode; got %v", m.memoryPicker.mode)
	}
	// The user's requirement: results must NOT land in the session scrollback.
	if strings.Contains(m.transcript.String(), "queue-writes") {
		t.Errorf("search results must not be printed to the session; got:\n%s", m.transcript.String())
	}
	v := stripANSI(m.View())
	if !strings.Contains(v, "queue-writes") {
		t.Errorf("results overlay should list the matching memory; got:\n%s", v)
	}
	if strings.Contains(v, "flush-left") {
		t.Errorf("non-matching memory should be filtered out; got:\n%s", v)
	}
}

// Enter on a result opens the editor but keeps the picker open in search
// mode, so exiting the editor returns to the same results (query kept).
func TestSlash_MemorySearchEnterKeepsPickerOpen(t *testing.T) {
	m := newTestModel(t)
	seedUserMemoryFile(t, "queue-writes", "database writes go behind a queue")
	m, _ = m.runSlash("/memory search queue")
	if m.memoryPicker == nil || len(m.memoryPicker.searchResults) == 0 {
		t.Fatalf("expected results to open on")
	}
	m, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.memoryPickerOpen || m.memoryPicker == nil || m.memoryPicker.mode != memorySearchMode {
		t.Errorf("opening a result must keep the picker in search mode (so editor-exit returns here)")
	}
	if cmd == nil {
		t.Errorf("Enter on a result should dispatch a Cmd to launch the editor")
	}
}

// Esc from the results list returns to the picker root menu.
func TestSlash_MemorySearchEscReturnsToRoot(t *testing.T) {
	m := newTestModel(t)
	seedUserMemoryFile(t, "queue-writes", "database writes go behind a queue")
	m, _ = m.runSlash("/memory search queue")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.memoryPicker == nil || m.memoryPicker.mode != memoryRootMode {
		t.Errorf("Esc from results should return to root; got picker=%v", m.memoryPicker)
	}
}

// An empty query opens the picker root rather than printing to the session.
func TestSlash_MemorySearchEmptyQueryOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/memory search")
	if !m.memoryPickerOpen || m.memoryPicker == nil {
		t.Fatalf("empty search query should open the picker")
	}
	if m.memoryPicker.mode != memoryRootMode {
		t.Errorf("empty query should land on the root menu; got mode %v", m.memoryPicker.mode)
	}
	if strings.Contains(m.transcript.String(), "[memory]") {
		t.Errorf("must not print to the session transcript; got:\n%s", m.transcript.String())
	}
}

// The in-picker "Search memories" row (index 4) opens a query box; typing
// a query and pressing Enter shows the ranked results overlay.
func TestSlash_MemoryPickerSearchRowRunsSearch(t *testing.T) {
	m := newTestModel(t)
	seedUserMemoryFile(t, "queue-writes", "database writes go behind a queue")

	m, _ = typeAndEnter(t, m, "/memory")
	for i := 0; i < 4; i++ { // move cursor to the "Search memories" row
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.memoryPicker == nil || m.memoryPicker.mode != memorySearchInputMode {
		t.Fatalf("Search row should open the query input; got picker=%v", m.memoryPicker)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("queue")})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.memoryPicker.mode != memorySearchMode {
		t.Fatalf("Enter in the query box should run the search; got mode %v", m.memoryPicker.mode)
	}
	if len(m.memoryPicker.searchResults) == 0 {
		t.Errorf("expected at least one ranked result for 'queue'")
	}
	if !strings.Contains(stripANSI(m.View()), "queue-writes") {
		t.Errorf("results overlay should list queue-writes; got:\n%s", stripANSI(m.View()))
	}
}

func TestSlash_MemoryPickerViewIncludesAllRows(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	v := stripANSI(m.View())
	for _, want := range []string{
		"Memory",
		"Project context",
		"User preferences",
		"Browse user memories",
		"Browse project memories",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("picker view missing %q; got:\n%s", want, v)
		}
	}
}

func TestSlash_MemoryPickerEscClosesPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.memoryPickerOpen {
		t.Errorf("Esc should close the memory picker")
	}
	if m.memoryPicker != nil {
		t.Errorf("Esc should drop the picker pointer")
	}
}

func TestSlash_MemoryPickerArrowKeysClampToRange(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")

	rowCount := m.memoryPicker.rowCount()
	for i := 0; i < rowCount+2; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.memoryPicker.cursor != rowCount-1 {
		t.Errorf("cursor should clamp at last row (%d); got %d",
			rowCount-1, m.memoryPicker.cursor)
	}

	for i := 0; i < rowCount+2; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	}
	if m.memoryPicker.cursor != 0 {
		t.Errorf("cursor should clamp at 0 (Project context); got %d", m.memoryPicker.cursor)
	}
}

func TestSlash_MemoryPickerProjectRowOpensFile(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	m, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.memoryPickerOpen {
		t.Errorf("Enter on row 0 should close the picker")
	}
	if cmd == nil {
		t.Errorf("Enter on row 0 should dispatch a Cmd to launch vim")
	}
	expected := filepath.Join(m.cwd, ".yottacode", "YOTTACODE.md")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("project memory file should exist after dispatch (%s): %v", expected, err)
	}
}

func TestSlash_MemoryPickerBrowseUserRow(t *testing.T) {
	m := newTestModel(t)
	seedUserMemoryFile(t, "alpha", "fact 1")
	seedUserMemoryFile(t, "bravo", "fact 2")

	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown}) // cursor → row 2
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if !m.memoryPickerOpen {
		t.Errorf("Enter on Browse row should keep the picker open")
	}
	if m.memoryPicker == nil || m.memoryPicker.mode != memoryBrowseMode {
		t.Fatalf("expected browse mode; got picker=%v", m.memoryPicker)
	}
	if m.memoryPicker.browseScope != "user" {
		t.Errorf("expected user scope; got %q", m.memoryPicker.browseScope)
	}
	if got := len(m.memoryPicker.entries); got != 2 {
		t.Errorf("expected 2 entries; got %d", got)
	}
	wantNames := []string{"alpha", "bravo"}
	for i, want := range wantNames {
		if i >= len(m.memoryPicker.entries) || m.memoryPicker.entries[i].Name != want {
			t.Errorf("entry[%d] = %v, want %q", i, m.memoryPicker.entries, want)
		}
	}
}

func TestSlash_MemoryPickerBrowseProjectRow(t *testing.T) {
	m := newTestModel(t)
	seedProjectMemoryFile(t, m.cwd, "p-fact", "project fact")

	m, _ = typeAndEnter(t, m, "/memory")
	for i := 0; i < 3; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.memoryPicker == nil || m.memoryPicker.mode != memoryBrowseMode {
		t.Fatalf("expected browse mode; got picker=%v", m.memoryPicker)
	}
	if m.memoryPicker.browseScope != "project" {
		t.Errorf("expected project scope; got %q", m.memoryPicker.browseScope)
	}
	if got := len(m.memoryPicker.entries); got != 1 || m.memoryPicker.entries[0].Name != "p-fact" {
		t.Errorf("expected single p-fact entry; got %+v", m.memoryPicker.entries)
	}
}

func TestMemoryPicker_BrowseDeleteRemovesFile(t *testing.T) {
	m := newTestModel(t)
	m.baseSystemPrompt = "BASE"
	path := seedUserMemoryFile(t, "drop-me", "fact")

	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // enter Browse user memories

	if m.memoryPicker.mode != memoryBrowseMode {
		t.Fatalf("expected browse mode")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected memory file to be deleted; stat err = %v", err)
	}
	if !m.memoryPickerOpen {
		t.Errorf("picker should stay open after delete")
	}
	if got := len(m.memoryPicker.entries); got != 0 {
		t.Errorf("entries should refresh in place; got %d", got)
	}
	if !strings.Contains(m.memoryPicker.browseMessage, "deleted drop-me") {
		t.Errorf("picker should surface a 'deleted X' toast; got %q", m.memoryPicker.browseMessage)
	}
}

func TestMemoryPicker_BrowseEscReturnsToRoot(t *testing.T) {
	m := newTestModel(t)
	seedUserMemoryFile(t, "alpha", "fact")

	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.memoryPicker.mode != memoryBrowseMode {
		t.Fatalf("expected browse mode")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.memoryPicker == nil || m.memoryPicker.mode != memoryRootMode {
		t.Errorf("esc from browse should return to root; got picker=%v", m.memoryPicker)
	}
	if !m.memoryPickerOpen {
		t.Errorf("picker should still be open after esc-to-root")
	}
}

func TestEmitMemorySizeWarnings_FiresAboveThreshold(t *testing.T) {
	m := newTestModel(t)
	dir := filepath.Join(m.cwd, ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "YOTTACODE.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 60_900)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m.transcript.Reset()
	m.emitMemorySizeWarnings()
	out := m.transcript.String()
	if !strings.Contains(out, "Large YOTTACODE.md will impact performance") {
		t.Errorf("expected size warning; got %q", out)
	}
	if !strings.Contains(out, "60.9k") || !strings.Contains(out, "40.0k") {
		t.Errorf("warning should report sizes in 'NN.Nk' form; got %q", out)
	}
	if !strings.Contains(out, "/memory to edit") {
		t.Errorf("warning should hint at /memory; got %q", out)
	}
}

func TestEmitMemorySizeWarnings_SilentBelowThreshold(t *testing.T) {
	m := newTestModel(t)
	dir := filepath.Join(m.cwd, ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "YOTTACODE.md")
	if err := os.WriteFile(path, []byte("compact"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m.transcript.Reset()
	m.emitMemorySizeWarnings()
	if out := m.transcript.String(); strings.Contains(out, "Large") {
		t.Errorf("under-threshold file should not warn; got %q", out)
	}
}

func TestFormatMemorySizeWarning_MatchesExpectedShape(t *testing.T) {
	got := formatMemorySizeWarning("YOTTACODE.md", 60_900)
	want := "Large YOTTACODE.md will impact performance (60.9k chars > 40.0k) · /memory to edit"
	if got != want {
		t.Errorf("formatMemorySizeWarning = %q, want %q", got, want)
	}
}

func TestMemoryPickerRowCount_WithoutEmbedClient(t *testing.T) {
	// 6 base rows (incl. "Search memories") + 1 "Enable semantic search".
	st := &memoryPickerState{showEnableSemanticRow: true}
	if got := st.rowCount(); got != 7 {
		t.Errorf("rowCount with semantic row = %d, want 7", got)
	}
}

func TestMemoryPickerRowCount_WithEmbedClient(t *testing.T) {
	st := &memoryPickerState{showEnableSemanticRow: false}
	if got := st.rowCount(); got != 6 {
		t.Errorf("rowCount without semantic row = %d, want 6", got)
	}
}

var (
	_ = memory.ProjectSlug
	_ = seedUserFile
)
