package tui

import (
	"github.com/charmbracelet/lipgloss"
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

// renderConnDot returns a single styled `●` whose color reflects state.
// Maps to the canonical state palette: Success (green) when healthy,
// Warning (amber) when degraded, Error (red) when offline, Dim
// otherwise.
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
	return style.Render("●")
}
