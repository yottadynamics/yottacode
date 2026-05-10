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
	m.paletteFiltered = filterPalette("/")
	m.paletteOpen = true
	m.paletteIndex = 0
	m.filePaletteOpen = false
}

// filterPalette returns the slash commands matching the typed prefix.
// "/" matches everything; "/mo" matches "/model". Case-insensitive.
func filterPalette(typed string) []slashCommand {
	typed = strings.TrimPrefix(typed, "/")
	typed = strings.ToLower(typed)
	if typed == "" {
		return allSlash
	}
	var out []slashCommand
	for _, c := range allSlash {
		if strings.HasPrefix(c.Name, typed) {
			out = append(out, c)
		}
	}
	return out
}

// renderPalette draws the slash command list as a bordered box. idx is the
// currently highlighted entry (Tab completes it; Up/Down navigate).
func renderPalette(items []slashCommand, idx, width int) string {
	if len(items) == 0 {
		return stylePaletteBox.Width(width).Render(stylePaletteEmpty.Render("(no matching commands)"))
	}
	// Compute column width from the visible items so the help text
	// always aligns regardless of which subset matched the prefix.
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
	var lines []string
	for i, c := range items {
		line := fmt.Sprintf(" %-*s   %s", colWidth, lefts[i], c.Help)
		if i == idx {
			line = stylePaletteSelected.Render(line)
		} else {
			line = stylePaletteItem.Render(line)
		}
		lines = append(lines, line)
	}
	return stylePaletteBox.Width(width).Render(strings.Join(lines, "\n"))
}
