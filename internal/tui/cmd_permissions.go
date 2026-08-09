package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// permissionsRowCount is the number of rows in the /permissions picker
// (shared, local). Centralized so the picker's cursor clamp and any
// future tests stay in sync if a third file ever joins the layout.
const permissionsRowCount = 2

// cmdPermissions opens the inline /permissions picker below the
// cmdline. Two rows — `shared` (~/.yottacode/permissions.json) and
// `local` (./.yottacode/permissions.local.json) — Up/Down navigates,
// Enter suspends to vim on the chosen file, Esc closes. The store
// re-reads both files on the next tool call, so there's no explicit
// reload step. Inline subcommands (edit/reload/add) were removed in
// favour of "open the file in your editor and the next tool call
// picks up the change".
func cmdPermissions(m Model, _ []string) (Model, tea.Cmd) {
	if m.perms == nil {
		m.appendLine(styleError.Render("[permissions] permissions store unavailable in this session"))
		return m, nil
	}
	m.permissionsOpen = true
	m.permissionsCursor = 0
	return m, nil
}

// updatePermissionsPicker handles keystrokes while the /permissions
// picker is the foreground modal. Mirrors updateMemoryPicker — Esc
// closes, ↑/↓ navigates the two rows, Enter dispatches to vim on the
// highlighted file. Unknown keys are no-ops (the textarea is hidden
// underneath).
func (m Model) updatePermissionsPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEsc:
		m.permissionsOpen = false
		m.permissionsCursor = 0
		m.openSlashPalette()
		return m, nil
	case tea.KeyUp:
		if m.permissionsCursor > 0 {
			m.permissionsCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.permissionsCursor < permissionsRowCount-1 {
			m.permissionsCursor++
		}
		return m, nil
	case tea.KeyEnter:
		path := m.permissionsRowPath(m.permissionsCursor)
		m.permissionsOpen = false
		m.permissionsCursor = 0
		if path == "" {
			m.appendLine(styleError.Render("[permissions] file path unavailable"))
			return m, nil
		}
		// Seed both files with the full {allow, ask, deny} skeleton before
		// vim opens, so a missing or 0-byte file (left behind by an earlier
		// "open and quit") never reappears as an empty buffer — and never
		// crashes the next startup on empty-JSON parse.
		if err := m.perms.EnsureFiles(); err != nil {
			m.appendLine(styleError.Render("[permissions] init: " + err.Error()))
		}
		return m.openInVim(path)
	}
	return m, nil
}

// permissionsRowPath returns the on-disk path for the picker row at
// index. Falls back to "" when the permissions store is unavailable
// (which the caller surfaces as an inline error rather than crashing).
func (m Model) permissionsRowPath(idx int) string {
	if m.perms == nil {
		return ""
	}
	switch idx {
	case 0:
		return m.perms.SharedPath()
	case 1:
		return m.perms.LocalPath()
	}
	return ""
}

// renderPermissionsOverlay draws the picker body. Same visual language
// as /model and /provider — `renderMenuHeader` for the title block,
// `renderMenuItem` for the rows, `styleFooter` for the hotkey row at the
// bottom. Deliberately borderless/uncentered here — popupBox (popup.go)
// supplies the single rounded border and composePopup does the
// centering, so adding either here would read as "modal floating on a
// modal".
func renderPermissionsOverlay(m Model) string {
	shared := ""
	local := ""
	if m.perms != nil {
		shared = tildeifyHome(m.perms.SharedPath())
		local = dotifyCwd(m.perms.LocalPath(), m.cwd)
	}

	rows := []struct {
		Label string
		Path  string
	}{
		{Label: "shared", Path: shared},
		{Label: "local", Path: local},
	}

	var b strings.Builder
	b.WriteString(renderMenuHeader("Permissions",
		"Edit a rule file in vim. The store re-reads on the next tool call."))
	b.WriteString("\n")
	for i, row := range rows {
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:      row.Label,
			LabelWidth: 8,
			Desc:       row.Path,
			Cursor:     i == m.permissionsCursor,
		}))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("↵ open in vim · esc back · ↑↓ navigate"))
	return strings.TrimRight(b.String(), "\n")
}

// tildeifyHome rewrites a $HOME-prefixed path with a leading ~ for display.
func tildeifyHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	sep := string(os.PathSeparator)
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+sep) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// dotifyCwd rewrites a cwd-prefixed path with a leading . for display.
func dotifyCwd(p, cwd string) string {
	if cwd == "" {
		return p
	}
	sep := string(os.PathSeparator)
	if p == cwd {
		return "."
	}
	if strings.HasPrefix(p, cwd+sep) {
		return "." + strings.TrimPrefix(p, cwd)
	}
	return p
}
