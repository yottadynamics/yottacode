package tui

import (
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// connState represents the connection health for the configured
// endpoint. Used by the status-bar dot to communicate state at a
// glance: green when healthy, amber when degraded, red when offline.
// Until the first probe returns we render a muted dot so the splash
// isn't misleading.
//
// connDegraded is reserved for future router-health plumbing —
// when the multi-provider router demotes a candidate but the
// endpoint is still reachable. No code paths emit it yet; the
// constant exists so the rendering path is ready when that
// plumbing lands.
type connState int

const (
	connUnknown connState = iota
	connOK
	connDegraded
	connDown
)

// connectionStatusMsg is delivered to the Model.Update loop when a probe
// finishes.
type connectionStatusMsg struct{ state connState }

// pingEndpoint runs as a tea.Cmd: GET <baseURL>/models with a short timeout.
// Returns connOK on 2xx, connDown on anything else (including timeout).
func pingEndpoint(baseURL string) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 2 * time.Second}
		url := strings.TrimRight(baseURL, "/") + "/models"
		resp, err := client.Get(url)
		if err != nil {
			return connectionStatusMsg{state: connDown}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return connectionStatusMsg{state: connOK}
		}
		return connectionStatusMsg{state: connDown}
	}
}

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
