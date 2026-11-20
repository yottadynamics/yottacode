package tui

import (
	"fmt"
	"strings"
	"time"
)

// refreshFilePalette is the per-keystroke driver for the @-palette.
// It scans the current textarea value for an active `@<query>` token
// (one whose `@` sits at start-of-input or after whitespace and which
// extends to the end of the text), and:
//
//   - opens the palette and lazily walks the cwd the first time it's
//     needed in this session
//   - re-filters filePaletteEntries against the query
//   - clamps the selected index back into range when the filtered
//     list shrinks below the previous selection
//
// When no active `@` token is present, the palette is closed and its
// state cleared. The cached entry list is kept across open/close so
// re-typing `@` doesn't re-walk the directory tree.
func (m *Model) refreshFilePalette(value string) {
	query, atIdx, found := extractFileQuery(value)
	if !found {
		m.filePaletteOpen = false
		m.filePaletteIndex = 0
		m.filePaletteOffset = 0
		m.filePaletteQuery = ""
		m.filePaletteAt = -1
		m.filePaletteFiltered = nil
		return
	}
	prevQuery := m.filePaletteQuery
	if !m.filePaletteOpen && time.Since(m.filePaletteWalkedAt) > filePaletteCacheTTL {
		m.filePaletteEntries = nil // palette is (re)opening: drop a stale walk
	}
	m.filePaletteFiltered = m.filterPalette(query)
	m.filePaletteQuery = query
	m.filePaletteAt = atIdx
	m.filePaletteOpen = true
	// Any change to the query resets the cursor to the top of the new
	// match list — the prior selection isn't meaningful in the new
	// ranking.
	if query != prevQuery {
		m.filePaletteIndex = 0
		m.filePaletteOffset = 0
	}
	if m.filePaletteIndex >= len(m.filePaletteFiltered) {
		m.filePaletteIndex = 0
		m.filePaletteOffset = 0
	}
}

// filterPalette returns the candidates for the raw query typed after
// `@`. Project-relative queries fuzzy-filter the cached cwd walk (built
// lazily); absolute, `~/` and `../` queries browse that directory live.
func (m *Model) filterPalette(raw string) []fileEntry {
	q := decodePaletteQuery(raw)
	if isExternalQuery(q) {
		return listExternal(q, m.cwd)
	}
	if m.filePaletteEntries == nil {
		m.filePaletteEntries, m.filePaletteCapped = walkFilesCapped(m.cwd)
		m.filePaletteWalkedAt = time.Now()
	}
	// When the walk was capped, a path-style query ("big/dir/na") lists
	// that directory live so nothing the caps dropped is unreachable.
	if m.filePaletteCapped && strings.Contains(q, "/") {
		if live := listExternal(q, m.cwd); len(live) > 0 {
			return live
		}
	}
	return filterFilePalette(q, m.filePaletteEntries)
}

// filePaletteCacheTTL bounds how stale the cached project walk may be
// when the palette opens. Files created mid-session (a fresh export, a
// new test) show up after at most this long, while rapid re-opens and
// the per-keystroke refreshes never trigger a re-walk (which once made
// typing lag on large trees).
const filePaletteCacheTTL = 5 * time.Second

// acceptFilePaletteChoice splices the highlighted entry into the
// textarea, replacing the active `@<query>` span. Files are written
// as `@path ` (trailing space so the user can keep typing) and
// directories as `@path/` (no trailing space — natural to keep
// drilling in via tab-complete).
//
// After the splice we recompute the palette: for a directory the new
// query is "path/" which yields the directory's children; for a file
// the trailing space terminates the token and the palette closes.
func (m *Model) acceptFilePaletteChoice() {
	if len(m.filePaletteFiltered) == 0 {
		return
	}
	choice := m.filePaletteFiltered[m.filePaletteIndex]
	val := m.textInput.Value()
	if m.filePaletteAt < 0 || m.filePaletteAt > len(val) {
		// Defensive — shouldn't happen since refreshFilePalette set
		// the anchor before opening, but never panic on a stale state.
		return
	}
	active := val[m.filePaletteAt:]
	if !strings.HasPrefix(active, "@") || active[1:] != m.filePaletteQuery {
		m.dropFilePalette()
		return
	}
	insert := paletteInsert(choice)
	newVal := val[:m.filePaletteAt] + "@" + insert
	m.textInput.SetValue(newVal)
	m.textInput.CursorEnd()
	if choice.IsDir {
		// Re-anchor for the directory's children — keep the palette
		// open so the user can keep tab-completing into it. The query
		// is the raw text after `@` (it may carry an open quote).
		m.filePaletteAt = len(val[:m.filePaletteAt])
		m.filePaletteQuery = insert
		m.filePaletteFiltered = m.filterPalette(m.filePaletteQuery)
		m.filePaletteIndex = 0
		m.filePaletteOffset = 0
		m.filePaletteOpen = true
		return
	}
	// File chosen: close the palette. The trailing space we inserted
	// guarantees extractFileQuery won't find an active token at
	// end-of-input, but we close eagerly to make the intent explicit.
	m.filePaletteOpen = false
	m.filePaletteIndex = 0
	m.filePaletteOffset = 0
	m.filePaletteQuery = ""
	m.filePaletteAt = -1
	m.filePaletteFiltered = nil
}

// dropFilePalette is the unconditional close — used when the user
// submits a turn or runs a slash command, so a stray palette doesn't
// hang around after the input clears.
func (m *Model) dropFilePalette() {
	m.filePaletteOpen = false
	m.filePaletteIndex = 0
	m.filePaletteOffset = 0
	m.filePaletteQuery = ""
	m.filePaletteAt = -1
	m.filePaletteFiltered = nil
}

// dirEntryCount counts non-empty lines in a directory listing string
// produced by filerefs.Load (one entry per line, plus a one-line
// "(directory listing)" header). Used for the user-visible notice.
func dirEntryCount(listing string) int {
	count := 0
	for _, line := range splitLines(listing) {
		if line == "" || line == "(directory listing)" {
			continue
		}
		count++
	}
	return count
}

// splitLines is strings.Split(s, "\n") without importing strings here
// just for one call. Keeping it explicit avoids an import cycle worry
// and makes intent obvious.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// humanBytes formats a byte count for the muted in-line notice. Keeps
// notices visually compact: "742 B", "12.3 KB", "1.2 MB". The TUI
// already emits enough numeric noise on tool calls; we want the
// attached-file lines to read at a glance.
func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
