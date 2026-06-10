package tui

import (
	"fmt"
	"strings"
)

// openSlashPalette pre-fills the textarea with `/`, opens the slash
// palette, and resets its cursor — used when a sub-picker is closed
// via Esc so the user lands back at the top-level command list rather
// than an empty prompt. Mirrors the state mutations the textarea
// Update path does when the user types `/` themselves (see
// model.go's default key branch).
func (m *Model) openSlashPalette() {
	m.textInput.SetValue("/")
	m.textInput.CursorEnd()
	m.paletteFiltered = m.filterPaletteAll("/")
	m.paletteOpen = true
	m.paletteIndex = 0
	m.paletteOffset = 0
	m.filePaletteOpen = false
}

// paletteMatchRank classifies how a command name matches the typed
// filter: 0 = prefix match, 1 = substring match elsewhere in the
// name, -1 = no match. Prefix matches rank above substring matches
// so muscle-memory filters ("/mo" → /model) keep their position at
// the top, while a mid-name search ("review" → /code-review,
// /git-review-pr) still finds everything — prefix-only matching
// made every /git-* and /code-* command invisible to the verb the
// user actually thinks in.
func paletteMatchRank(name, typed string) int {
	switch {
	case typed == "", strings.HasPrefix(name, typed):
		return 0
	case strings.Contains(name, typed):
		return 1
	}
	return -1
}

// filterPalette returns the built-in slash commands matching the
// typed filter — prefix matches first, then substring matches, each
// group in registration order. "/" matches everything; "/mo" matches
// "/model"; "/review" also surfaces "/code-review". Case-insensitive.
// Built-ins-only variant kept for tests that lock the built-in
// palette behavior; the dispatcher uses the Model-bound variant
// below so custom commands appear in the palette too.
func filterPalette(typed string) []slashCommand {
	typed = strings.ToLower(strings.TrimPrefix(typed, "/"))
	if typed == "" {
		return allSlash
	}
	var prefix, substr []slashCommand
	for _, c := range allSlash {
		switch paletteMatchRank(c.Name, typed) {
		case 0:
			prefix = append(prefix, c)
		case 1:
			substr = append(substr, c)
		}
	}
	return append(prefix, substr...)
}

// filterPalette (method) returns built-ins followed by the session's
// custom commands that match the typed filter, with the same
// prefix-before-substring ranking as the function variant (built-ins
// before custom commands within each rank). Used by the live TUI so
// users can see and tab-complete their custom commands.
func (m *Model) filterPaletteAll(typed string) []slashCommand {
	typed = strings.ToLower(strings.TrimPrefix(typed, "/"))
	var prefix, substr []slashCommand
	collect := func(cmds []slashCommand) {
		for _, c := range cmds {
			switch paletteMatchRank(c.Name, typed) {
			case 0:
				prefix = append(prefix, c)
			case 1:
				substr = append(substr, c)
			}
		}
	}
	collect(allSlash)
	collect(m.customSlash)
	return append(prefix, substr...)
}

// slashPaletteVisible caps how many entries the rendered slash palette
// shows at once. The full set of built-ins + custom commands easily
// reaches 30+ rows on a populated install, which used to consume the
// whole viewport — bounded window + scroll hints keeps the picker
// readable. Matches filePaletteVisible so both palettes feel the same.
const slashPaletteVisible = 12

// renderPalette draws the slash command list as a bordered box. idx is the
// currently highlighted entry (Tab completes it; Up/Down navigate). offset
// is the index of the first row in the visible window — the Model
// navigation code keeps offset in sync with idx so the selection stays
// inside [offset, offset+slashPaletteVisible).
//
// width is the TOTAL box width including the rounded border — callers
// pass the input frame's width so the palette and the cmdline box
// directly below it share both edges. lipgloss's Style.Width excludes
// the border, hence the -2 at the render calls; passing width straight
// through used to make the box overflow the terminal by two columns.
//
// When the filtered list is longer than slashPaletteVisible, we render
// only the window and add muted `↑ N more` / `↓ N more` hints above and
// below so the user knows there's more to scroll to.
func renderPalette(items []slashCommand, idx, offset, width int) string {
	if len(items) == 0 {
		return stylePaletteBox.Width(width - 2).Render(stylePaletteEmpty.Render("(no matching commands)"))
	}
	// Compute column width from the visible items so the help text
	// always aligns regardless of which subset matched the prefix.
	// Using the full item set (not just the visible window) keeps the
	// alignment stable as the user scrolls — otherwise the description
	// column would jump column-width every time the longest visible
	// "/cmd args" entry scrolled out of view.
	lefts := make([]string, len(items))
	colWidth := 0
	for i, c := range items {
		left := "/" + c.Name
		if c.Args != "" {
			left += " " + c.Args
		}
		lefts[i] = left
		if w := len(left); w > colWidth {
			colWidth = w
		}
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + slashPaletteVisible
	if end > len(items) {
		end = len(items)
	}
	var lines []string
	for i := offset; i < end; i++ {
		c := items[i]
		line := fmt.Sprintf(" %-*s   %s", colWidth, lefts[i], c.Help)
		if i == idx {
			line = stylePaletteSelected.Render(line)
		} else {
			line = stylePaletteItem.Render(line)
		}
		lines = append(lines, line)
	}
	return stylePaletteBox.Width(width - 2).Render(strings.Join(lines, "\n"))
}
