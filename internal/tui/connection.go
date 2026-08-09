package tui

import (
	"charm.land/lipgloss/v2"
)

// connState represents the connection health for the configured
// endpoint. Used by the status-bar dot to communicate state at a
// glance: green when healthy, amber when degraded, red when offline.
// Until the first probe returns we render a muted dot so the splash
// isn't misleading.
//
// connDegraded means the endpoint is reachable and the key is valid but
// the active diagnostics flagged something non-fatal — most often the
// configured model wasn't visible in the provider's model list. The
// connection works; only the model selection is in question. Emitted by
// probeConnectionState (commands.go).
type connState int

const (
	connUnknown connState = iota
	connOK
	connDegraded
	connDown
)

// connDotGlyph returns the single-cell glyph for a connection state. Color
// alone used to carry the state (every state rendered the same "●"), which
// is invisible under NO_COLOR or to a colorblind user — the shape now also
// distinguishes each state, mirroring the ✓/▸/· convention already used for
// plan-step and approval-toast status elsewhere in the TUI:
//
//	● filled  — connOK: fully connected
//	◐ half    — connDegraded: reachable, but something's off (e.g. model not
//	            in the provider's list)
//	○ hollow  — connDown: unreachable
//	· mid dot — connUnknown: no probe result yet
func connDotGlyph(state connState) string {
	switch state {
	case connOK:
		return "●"
	case connDegraded:
		return "◐"
	case connDown:
		return "○"
	default:
		return "·"
	}
}

// renderConnDot returns a single styled glyph whose shape AND color reflect
// state. Maps to the canonical state palette: Success (green) when healthy,
// Warning (amber) when degraded, Error (red) when offline, Dim otherwise.
func renderConnDot(state connState) string {
	style := lipgloss.NewStyle()
	switch state {
	case connOK:
		style = style.Foreground(colorSuccess)
	case connDegraded:
		style = style.Foreground(colorWarning)
	case connDown:
		style = style.Foreground(colorError)
	default:
		style = style.Foreground(colorMuted)
	}
	return style.Render(connDotGlyph(state))
}
