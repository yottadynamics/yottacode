package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/browser"
)

// cmdBrowser is /browser: with no argument it reports the persistent browser
// profile; "clear" closes the browser session and deletes the profile's
// cookies and logins.
func cmdBrowser(m Model, args []string) (Model, tea.Cmd) {
	if m.browserManager == nil {
		m.appendLine(styleError.Render("[browser] the browser manager is not available in this session"))
		return m, nil
	}
	dir := m.browserManager.PersistentProfileDir()
	if dir == "" {
		m.appendLine(styleAuto.Render("[browser] persistent profile is off: every session uses a fresh throwaway profile. Opt in with `[browser] persistent_profile = true` in ~/.yottacode/config.toml."))
		return m, nil
	}
	switch {
	case len(args) == 0:
		_, size := browser.ProfileUsage(dir)
		m.appendLine(styleAuto.Render(fmt.Sprintf("[browser] persistent profile: %s (%s). `/browser clear` deletes its cookies and logins.", dir, humanBytes(int(size)))))
	case len(args) == 1 && args[0] == "clear":
		freed, err := m.browserManager.ClearPersistentProfile()
		if err != nil {
			m.appendLine(styleError.Render("[browser] " + err.Error()))
			return m, nil
		}
		m.appendLine(styleAuto.Render(fmt.Sprintf("[browser] cleared the persistent profile (%s freed); the browser session was closed and the next browser action starts fresh.", humanBytes(int(freed)))))
	default:
		m.appendLine(styleError.Render("usage: /browser [clear]"))
	}
	return m, nil
}
