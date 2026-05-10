package tui

import (
	"fmt"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/filerefs"
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
	if m.filePaletteEntries == nil {
		m.filePaletteEntries = walkFiles(m.cwd)
	}
	prevQuery := m.filePaletteQuery
	m.filePaletteFiltered = filterFilePalette(query, m.filePaletteEntries)
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
	suffix := "@" + choice.Path
	if choice.IsDir {
		suffix += "/"
	} else {
		suffix += " "
	}
	newVal := val[:m.filePaletteAt] + suffix
	m.textInput.SetValue(newVal)
	m.textInput.CursorEnd()
	if choice.IsDir {
		// Re-anchor for the directory's children — keep the palette
		// open so the user can keep tab-completing into it.
		m.filePaletteAt = len(val[:m.filePaletteAt])
		m.filePaletteQuery = choice.Path + "/"
		m.filePaletteFiltered = filterFilePalette(m.filePaletteQuery, m.filePaletteEntries)
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

// invalidateFilePaletteCache forces the next @-trigger to re-walk the
// cwd. Called from /clear and any state transition that may have
// added/removed files since the cache was built.
func (m *Model) invalidateFilePaletteCache() {
	m.filePaletteEntries = nil
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

// injectFileRefs rewrites the active session's system message so it
// contains the latest auto-injected `## Referenced files` block, and
// emits a muted user-visible notice for each ref. Called once per
// turn from startTurn when the user input contains @<path> tokens;
// the injection is idempotent across turns because filerefs.Inject
// strips any prior block before appending the new one.
//
// Load failures are surfaced as red notices but do NOT block the turn
// — the model still sees the failed-ref entry in the injected block
// (so it can ask a clarifying question) and the rest of the turn
// proceeds. This mirrors how the agent's own tools handle bad paths:
// report and continue.
func (m *Model) injectFileRefs(refs []filerefs.Ref) {
	if len(refs) == 0 {
		return
	}
	m.rewriteSystemPromptWithRefs(refs)
	for _, r := range refs {
		switch {
		case r.Loaded && r.IsDir:
			m.appendLine(styleAuto.Render(fmt.Sprintf("· attached %s (directory, %d entries)",
				r.Token, dirEntryCount(r.Content))))
		case r.Loaded:
			m.appendLine(styleAuto.Render(fmt.Sprintf("· attached %s (%s)",
				r.Token, humanBytes(r.Size))))
		default:
			m.appendLine(styleError.Render(fmt.Sprintf("· could not attach %s: %s",
				r.Token, r.Error)))
		}
	}
}

// clearFileRefs strips any auto-injected file-refs block from the
// system prompt without adding a new one. Called from startTurn on
// turns where the user did NOT reference any files — keeps the
// previous turn's references from leaking into the next one when the
// user has moved on.
func (m *Model) clearFileRefs() {
	m.rewriteSystemPromptWithRefs(nil)
}

// rewriteSystemPromptWithRefs is the shared mutator behind both
// injectFileRefs and clearFileRefs. Walks sess.Messages for the
// system role and replaces its content with the result of
// filerefs.Inject. No-op if no system message is present (which
// shouldn't happen in practice — Run always seeds one).
func (m *Model) rewriteSystemPromptWithRefs(refs []filerefs.Ref) {
	for i := range m.sess.Messages {
		if m.sess.Messages[i].Role == adapter.RoleSystem {
			m.sess.Messages[i].Content = filerefs.Inject(m.sess.Messages[i].Content, refs)
			return
		}
	}
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
