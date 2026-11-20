package tui

import (
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func hasPaletteEntry(m Model, path string) bool {
	for _, e := range m.filePaletteFiltered {
		if e.Path == path {
			return true
		}
	}
	return false
}

func TestPalette_StaleWalkIsRefreshedOnOpen(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "old.go"), "")
	m := newTestModel(t)
	m.cwd = cwd

	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "@"})
	if !hasPaletteEntry(m, "old.go") || hasPaletteEntry(m, "fresh.png") {
		t.Fatalf("setup: filtered = %+v", m.filePaletteFiltered)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m.textInput.SetValue("")
	m.refreshFilePalette("")

	// A file appears after the first walk. Within the TTL the cache is
	// kept (no per-open re-walk cost)...
	mustWriteFile(t, filepath.Join(cwd, "fresh.png"), "")
	m.textInput.SetValue("@")
	m.refreshFilePalette("@")
	if hasPaletteEntry(m, "fresh.png") {
		t.Fatalf("walk re-ran inside the TTL")
	}

	// ...and once stale, reopening picks it up.
	m.textInput.SetValue("")
	m.refreshFilePalette("")
	m.filePaletteWalkedAt = time.Now().Add(-2 * filePaletteCacheTTL)
	m.textInput.SetValue("@")
	m.refreshFilePalette("@")
	if !hasPaletteEntry(m, "fresh.png") {
		t.Fatalf("stale walk not refreshed: %+v", m.filePaletteFiltered)
	}
}

func TestInsertCodeMapRef_QuotesPathsWithSpaces(t *testing.T) {
	m := newTestModel(t)
	m.insertCodeMapRef("docs/my notes.md")
	m.insertCodeMapRef("main.go")
	if got, want := m.textInput.Value(), `@"docs/my notes.md" @main.go `; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}
