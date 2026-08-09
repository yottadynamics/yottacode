package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// newMemoryTestModel is newTestModel with $YOTTACODE_HOME cleared, so the
// memory pickers and seed helpers resolve to the temp HOME and never read,
// write, or delete the developer's real ~/.yottacode/memory. The clear
// lives here rather than in the shared newTestModel because skills tests
// deliberately set the override to point installs at a temp dir.
func newMemoryTestModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("YOTTACODE_HOME", "")
	return newTestModel(t)
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
	m := newMemoryTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	if !m.memoryPickerOpen || m.memoryPicker == nil {
		t.Fatalf("/memory should open the picker; open=%v picker=%v",
			m.memoryPickerOpen, m.memoryPicker != nil)
	}
	if m.memoryPicker.cursor != 0 {
		t.Errorf("picker should open with cursor on row 0; got %d", m.memoryPicker.cursor)
	}
}

func TestSlash_MemoryIgnoresSearchArgs(t *testing.T) {
	m := newMemoryTestModel(t)
	m, _ = m.runSlash("/memory search database queue")
	if !m.memoryPickerOpen || m.memoryPicker == nil {
		t.Fatalf("/memory should open the picker even when extra args are present")
	}
	if m.memoryPicker.mode != memoryRootMode {
		t.Errorf("/memory no longer has a search subcommand; got mode %v", m.memoryPicker.mode)
	}
	if strings.Contains(m.transcript.String(), "queue-writes") || strings.Contains(stripANSI(m.View().Content), "database queue") || strings.Contains(stripANSI(m.View().Content), "Search memories") {
		t.Errorf("/memory args should not run a search or print results")
	}
}

func TestSlash_MemoryPickerViewIncludesAllRows(t *testing.T) {
	m := newMemoryTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	v := stripANSI(m.View().Content)
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
	m := newMemoryTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.memoryPickerOpen {
		t.Errorf("Esc should close the memory picker")
	}
	if m.memoryPicker != nil {
		t.Errorf("Esc should drop the picker pointer")
	}
}

func TestSlash_MemoryPickerArrowKeysClampToRange(t *testing.T) {
	m := newMemoryTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")

	rowCount := m.memoryPicker.rowCount()
	for i := 0; i < rowCount+2; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.memoryPicker.cursor != rowCount-1 {
		t.Errorf("cursor should clamp at last row (%d); got %d",
			rowCount-1, m.memoryPicker.cursor)
	}

	for i := 0; i < rowCount+2; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if m.memoryPicker.cursor != 0 {
		t.Errorf("cursor should clamp at 0 (Project context); got %d", m.memoryPicker.cursor)
	}
}

func TestSlash_MemoryPickerProjectRowOpensFile(t *testing.T) {
	m := newMemoryTestModel(t)
	m, _ = typeAndEnter(t, m, "/memory")
	m, cmd := applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m := newMemoryTestModel(t)
	seedUserMemoryFile(t, "alpha", "fact 1")
	seedUserMemoryFile(t, "bravo", "fact 2")

	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown}) // cursor → row 2
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})

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
	m := newMemoryTestModel(t)
	seedProjectMemoryFile(t, m.cwd, "p-fact", "project fact")

	m, _ = typeAndEnter(t, m, "/memory")
	for i := 0; i < 3; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})

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
	m := newMemoryTestModel(t)
	m.baseSystemPrompt = "BASE"
	path := seedUserMemoryFile(t, "drop-me", "fact")

	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // enter Browse user memories

	if m.memoryPicker.mode != memoryBrowseMode {
		t.Fatalf("expected browse mode")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "d"})

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
	m := newMemoryTestModel(t)
	seedUserMemoryFile(t, "alpha", "fact")

	m, _ = typeAndEnter(t, m, "/memory")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.memoryPicker.mode != memoryBrowseMode {
		t.Fatalf("expected browse mode")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.memoryPicker == nil || m.memoryPicker.mode != memoryRootMode {
		t.Errorf("esc from browse should return to root; got picker=%v", m.memoryPicker)
	}
	if !m.memoryPickerOpen {
		t.Errorf("picker should still be open after esc-to-root")
	}
}

func TestEmitMemorySizeWarnings_FiresAboveThreshold(t *testing.T) {
	m := newMemoryTestModel(t)
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
	m := newMemoryTestModel(t)
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
	// 5 base rows + 1 "Enable semantic search".
	st := &memoryPickerState{showEnableSemanticRow: true}
	if got := st.rowCount(); got != 6 {
		t.Errorf("rowCount with semantic row = %d, want 6", got)
	}
}

func TestMemoryPickerRowCount_WithEmbedClient(t *testing.T) {
	st := &memoryPickerState{showEnableSemanticRow: false}
	if got := st.rowCount(); got != 5 {
		t.Errorf("rowCount without semantic row = %d, want 5", got)
	}
}

var (
	_ = memory.ProjectSlug
	_ = seedUserFile
)
