package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// ExportMarkdown serializes a Session into a human-readable markdown
// document — useful for sharing a working session in a PR description,
// an issue, or just archiving. We deliberately omit the system message
// (boilerplate plus injected memory, not interesting to a reader) and
// fence tool output for clarity.
//
// Living in the session package (rather than internal/tui) lets the
// non-interactive cobra subcommands call it without dragging the
// bubbletea/lipgloss dependency tree into the CLI binary's
// non-interactive paths.
func ExportMarkdown(s *Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# yottacode session\n\n")
	fmt.Fprintf(&b, "- **id:** %s\n", s.ID)
	if s.Name != "" {
		fmt.Fprintf(&b, "- **name:** %s\n", s.Name)
	}
	fmt.Fprintf(&b, "- **model:** `%s`\n", s.Model)
	fmt.Fprintf(&b, "- **created:** %s\n", s.Created.Format(time.RFC3339))
	fmt.Fprintf(&b, "- **cwd:** `%s`\n", s.Cwd)
	b.WriteString("\n---\n\n")

	for _, msg := range s.Messages {
		switch msg.Role {
		case adapter.RoleSystem:
			// Skipped: the base prompt + injected memory is internal context.
		case adapter.RoleUser:
			fmt.Fprintf(&b, "## User\n\n%s\n\n", strings.TrimRight(msg.Content, "\n"))
		case adapter.RoleAssistant:
			b.WriteString("## Assistant\n\n")
			if msg.Content != "" {
				fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(msg.Content, "\n"))
			}
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&b, "**Tool call:** `%s(%s)`\n\n", tc.Name, tc.ArgsJSON)
			}
			if len(msg.Citations) > 0 {
				b.WriteString("**Sources**\n\n")
				for _, c := range msg.Citations {
					if label := citationLabel(c); label != "" {
						fmt.Fprintf(&b, "- %s\n", label)
					}
				}
				b.WriteString("\n")
			}
		case adapter.RoleTool:
			fmt.Fprintf(&b, "**Tool output**\n\n```\n%s\n```\n\n",
				strings.TrimRight(msg.Content, "\n"))
		}
	}
	return b.String()
}

// citationLabel renders one Citation into the short form that appears
// under "**Sources**" in the export. Mirrors the TUI's render-time
// helper of the same name (kept private in each package — they may
// diverge if the TUI ever adds inline styling).
func citationLabel(c adapter.Citation) string {
	switch {
	case c.Title != "" && c.URL != "":
		return c.Title + " (" + c.URL + ")"
	case c.Title != "":
		return c.Title
	case c.Filename != "" && c.FileID != "":
		return c.Filename + " (" + c.FileID + ")"
	case c.Filename != "":
		return c.Filename
	case c.URL != "":
		return c.URL
	case c.FileID != "":
		return c.FileID
	default:
		return ""
	}
}
