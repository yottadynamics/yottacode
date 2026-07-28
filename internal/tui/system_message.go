package tui

import "strings"

// SysIcon is the closed icon set for one-line TUI system lifecycle rows.
// Keeping the icon choices centralized prevents ad-hoc bracket prefixes such
// as "[mcp]" or "lsp:" from drifting back into the transcript.
type SysIcon string

const (
	SysState    SysIcon = "○"
	SysSuccess  SysIcon = "✓"
	SysProgress SysIcon = "…"
	SysWarning  SysIcon = "⚠"
	SysFailure  SysIcon = "✕"
	SysQueue    SysIcon = "→"
	SysReturn   SysIcon = "↩"
	SysContext  SysIcon = "◇"
)

// SysMsg renders the only approved grammar for lightweight system messages:
//
//	<icon> <source> · <event> · <detail>
//
// Empty detail parts are skipped so callers can pass optional context without
// producing doubled separators. The returned string is plain; callers choose
// styleAuto/styleError/etc. based on severity.
func SysMsg(icon SysIcon, source, event string, detail ...string) string {
	parts := []string{strings.TrimSpace(string(icon) + " " + source), strings.TrimSpace(event)}
	for _, d := range detail {
		if trimmed := strings.TrimSpace(d); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}
