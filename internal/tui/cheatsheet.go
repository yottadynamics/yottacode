package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// cheatsheetEntry pairs a key/binding with its action description.
type cheatsheetEntry struct {
	Key, Action string
}

var cheatsheet = []cheatsheetEntry{
	{"Enter", "submit message · execute slash command"},
	{"↑ / ↓", "browse input history at edge of textarea (cursor moves between lines mid-draft) · navigate palette when open"},
	{"Tab", "complete highlighted slash command"},
	{"/", "open the slash command palette"},
	{"Esc", "dismiss palette · dismiss cheatsheet"},
	{"?", "open this cheatsheet"},
	{"Ctrl+C", "cancel the in-flight turn (returns to prompt)"},
	{"Ctrl+D", "exit yottacode"},
	{"y / a / N", "approval response in modal (yes / always / no)"},
	{"@", "open file picker — type to filter, Tab/Enter to insert, Esc to close"},
	{"@<path>", "attach a file's contents to the next message (cwd-confined)"},
}

// renderCheatsheet returns a centered, bordered overlay listing every
// keyboard shortcut. Caller is responsible for placing it; we just render
// the box.
func renderCheatsheet(width int) string {
	var b strings.Builder
	b.WriteString(stylePathHeader.Render("Keyboard shortcuts") + "\n\n")
	for _, e := range cheatsheet {
		key := lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(e.Key)
		// Pad the key column for alignment.
		padded := lipgloss.PlaceHorizontal(12, lipgloss.Left, key)
		b.WriteString("  " + padded + " " + e.Action + "\n")
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("  press any key to close"))

	box := stylePaletteBox.Render(b.String())
	if width > 0 {
		return lipgloss.PlaceHorizontal(width, lipgloss.Center, box)
	}
	return box
}
