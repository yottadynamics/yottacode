package tui

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
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

// setBrowseVisible sizes the terminal so the browse popup shows exactly n
// entry rows; the window is derived from m.height, not stored directly.
func setBrowseVisible(m *Model, n int) {
	m.height = n + memoryBrowseChromeLines(m.memoryPicker, m.popupWidth()) + 2
	m.clampMemoryBrowseCursor()
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

func TestMemoryPicker_BrowseWindowKeepsCursorVisible(t *testing.T) {
	m := newMemoryTestModel(t)
	entries := make([]memory.MemoryEntry, 12)
	for i := range entries {
		entries[i].Name = fmt.Sprintf("memory-%02d", i)
	}
	m.memoryPicker = &memoryPickerState{mode: memoryBrowseMode, entries: entries}
	m.memoryPickerOpen = true
	setBrowseVisible(&m, 4)

	for i := 0; i < 7; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	p := m.memoryPicker
	if p.entryCursor != 7 || p.browseOffset != 4 {
		t.Fatalf("cursor/window = %d/%d, want 7/4", p.entryCursor, p.browseOffset)
	}
	view := stripANSI(renderMemoryBrowse(p, 80, nil))
	if strings.Contains(view, "memory-00") || !strings.Contains(view, "memory-07") || strings.Contains(view, "memory-11") {
		t.Fatalf("browse view does not show the bounded window:\n%s", view)
	}
	if !strings.Contains(view, "showing 5–8 of 12") {
		t.Fatalf("browse view missing range hint:\n%s", view)
	}
}

func TestMemoryPicker_BrowsePagingAndBoundaries(t *testing.T) {
	m := newMemoryTestModel(t)
	entries := make([]memory.MemoryEntry, 10)
	m.memoryPicker = &memoryPickerState{mode: memoryBrowseMode, entries: entries}
	m.memoryPickerOpen = true
	setBrowseVisible(&m, 3)

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.memoryPicker.entryCursor != 3 || m.memoryPicker.browseOffset != 1 {
		t.Fatalf("after PgDown cursor/window = %d/%d, want 3/1", m.memoryPicker.entryCursor, m.memoryPicker.browseOffset)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.memoryPicker.entryCursor != 9 || m.memoryPicker.browseOffset != 7 {
		t.Fatalf("after End cursor/window = %d/%d, want 9/7", m.memoryPicker.entryCursor, m.memoryPicker.browseOffset)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.memoryPicker.entryCursor != 0 || m.memoryPicker.browseOffset != 0 {
		t.Fatalf("after Home cursor/window = %d/%d, want 0/0", m.memoryPicker.entryCursor, m.memoryPicker.browseOffset)
	}
}

func TestMemoryPicker_BrowseWindowRecomputesOnResize(t *testing.T) {
	m := newMemoryTestModel(t)
	m.memoryPicker = &memoryPickerState{mode: memoryBrowseMode, entries: make([]memory.MemoryEntry, 20), entryCursor: 19, browseOffset: 17}
	m.memoryPickerOpen = true
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 12})
	// The header wraps with width, so the budget is measured, not fixed.
	want := max(1, 12-memoryBrowseChromeLines(m.memoryPicker, m.popupWidth())-2)
	if m.memoryPicker.browseVisible != want {
		t.Fatalf("browseVisible = %d, want %d", m.memoryPicker.browseVisible, want)
	}
	if m.memoryPicker.entryCursor != 19 || m.memoryPicker.browseOffset != 20-want {
		t.Fatalf("cursor/window = %d/%d, want 19/%d", m.memoryPicker.entryCursor, m.memoryPicker.browseOffset, 20-want)
	}
}

func TestMemoryPicker_BrowsePopupNeverExceedsTerminalHeight(t *testing.T) {
	for _, h := range []int{8, 12, 24, 40} {
		m := newMemoryTestModel(t)
		entries := make([]memory.MemoryEntry, 60)
		for i := range entries {
			entries[i].Name = fmt.Sprintf("memory-%02d", i)
			entries[i].Description = strings.Repeat("long description ", 20)
		}
		m.memoryPicker = &memoryPickerState{mode: memoryBrowseMode, browseScope: "user", browseDir: "/tmp/x", browseMessage: "deleted one", entries: entries}
		m.memoryPickerOpen = true
		m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: h})
		box := popupBox(renderMemoryPicker(m.memoryPicker, m.popupWidth()))
		got := strings.Count(box, "\n") + 1
		// Terminals below ~16 rows bottom out at one entry row; larger ones must fit.
		if h >= 20 && got > h {
			t.Errorf("height %d: popup is %d lines tall, exceeds terminal", h, got)
		}
	}
}

func TestMemoryPicker_BrowseWrappedDescriptionsKeepClicksAligned(t *testing.T) {
	m := newMemoryTestModel(t)
	entries := []memory.MemoryEntry{
		{Name: "first", Description: strings.Repeat("wraps across the popup ", 12)},
		{Name: "second", Description: "short"},
		{Name: "third", Description: "short"},
	}
	m.memoryPicker = &memoryPickerState{mode: memoryBrowseMode, entries: entries}
	m.memoryPickerOpen = true
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 40})
	hits := &pickerHits{}
	box := popupBox(renderMemoryPicker(m.memoryPicker, m.popupWidth(), hits))
	lines := strings.Split(stripANSI(box), "\n")
	// An unbounded description makes the popup wider than the terminal, which
	// the terminal then wraps, desynchronizing hit rows from drawn rows.
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > m.width {
			t.Fatalf("popup line is %d cells wide, terminal is %d: %q", w, m.width, l)
		}
	}
	for idx, name := range []string{"first", "second", "third"} {
		found := false
		for _, r := range hits.regions {
			if r.Kind != hitItem || r.Index != idx {
				continue
			}
			// body row r.Row sits below the top border line.
			if r.Row+1 < len(lines) && strings.Contains(lines[r.Row+1], name) {
				found = true
			}
		}
		if !found {
			t.Errorf("hit row for %q does not land on its rendered line:\n%s", name, strings.Join(lines, "\n"))
		}
	}
}
