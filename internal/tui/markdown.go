package tui

import "charm.land/glamour/v2"

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

	// width and monochrome record what this renderer was built with, so
	// a caller about to rebuild (refreshComponentStyles, on every theme
	// picker cursor step) can compare against the wanted values first
	// and skip the rebuild — glamour.NewTermRenderer recompiles a full
	// chroma style sheet, and outside the no-color theme this value
	// never actually changes between themes (see newMarkdownRenderer).
	width      int
	monochrome bool
}

// clampMarkdownWidth applies the same "too narrow to wrap sensibly"
// floor newMarkdownRenderer uses. Exported to this file's callers so a
// rebuild-skip comparison checks against the width glamour will
// actually use, not the raw pre-clamp input.
func clampMarkdownWidth(width int) int {
	if width < 20 {
		return 80
	}
	return width
}

func newMarkdownRenderer(width int) *markdownRenderer {
	width = clampMarkdownWidth(width)
	// themeMonochrome mirrors the active theme's Palette.Monochrome
	// (styles.go, set from buildStyles). Only "no-color" sets it —
	// without this check glamour always rendered assistant prose with
	// its baked-in "dark" style regardless of theme, so selecting
	// no-color muted code-block syntax highlighting (via the
	// Highlight: "bw" chroma style) but left headings/bold/links still
	// colored, breaking that theme's "every role renders as default
	// terminal foreground" contract. "notty" is glamour's dedicated
	// colorless style (identical to "ascii" except for the name).
	mono := themeMonochrome
	style := "dark"
	if mono {
		style = "notty"
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return &markdownRenderer{width: width, monochrome: mono}
	}
	return &markdownRenderer{r: r, width: width, monochrome: mono}
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
