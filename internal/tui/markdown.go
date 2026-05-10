package tui

import "github.com/charmbracelet/glamour"

// markdownRenderer wraps glamour with a preconfigured style and width. The
// width is rebuilt on every WindowSizeMsg so wrapping respects the terminal.
//
// We render *finalized* assistant messages through glamour — the live stream
// stays plain text because mid-stream content is often syntactically invalid
// markdown (half-open code fences, unclosed bold, etc.) which glamour will
// either drop or render badly. After the assistant message is committed, the
// transcript replaces the plain text with the rendered version.
type markdownRenderer struct {
	r *glamour.TermRenderer
}

func newMarkdownRenderer(width int) *markdownRenderer {
	if width < 20 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return &markdownRenderer{}
	}
	return &markdownRenderer{r: r}
}

// render returns the rendered output, or the original text if glamour fails
// or wasn't constructed.
func (m *markdownRenderer) render(text string) string {
	if m == nil || m.r == nil {
		return text
	}
	out, err := m.r.Render(text)
	if err != nil {
		return text
	}
	return out
}
