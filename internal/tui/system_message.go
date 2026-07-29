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
	SysThought  SysIcon = "◦"
	SysReturn   SysIcon = "↩"
	SysContext  SysIcon = "◇"
)

const sysMsgSourceWidth = 8

// SysMsg renders the only approved grammar for lightweight system messages:
//
//	<icon> <source> · <event> · <detail>
//
// Empty detail parts are skipped so callers can pass optional context without
// producing doubled separators. The returned string is plain; callers choose
// styleAuto/styleError/etc. based on severity.
func SysMsg(icon SysIcon, source, event string, detail ...string) string {
	return sysMsg(icon, source, event, 0, detail...)
}

// SysMsgAligned renders the same grammar as SysMsg, but right-pads the source
// label so adjacent lifecycle rows line up as a small status cluster.
func SysMsgAligned(icon SysIcon, source, event string, detail ...string) string {
	return sysMsg(icon, source, event, sysMsgSourceWidth, detail...)
}

func sysMsg(icon SysIcon, source, event string, sourceWidth int, detail ...string) string {
	source = strings.TrimSpace(source)
	if sourceWidth > 0 && source != "" && len([]rune(source)) < sourceWidth {
		source += strings.Repeat(" ", sourceWidth-len([]rune(source)))
	}
	first := strings.TrimSpace(string(icon) + " " + source)
	if sourceWidth > 0 && source != "" {
		first = string(icon) + " " + source
	}
	parts := []string{first, strings.TrimSpace(event)}
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
