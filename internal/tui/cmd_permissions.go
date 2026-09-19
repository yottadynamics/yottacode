package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// permissionsRowCount is the number of policy sources shown in the picker.
const permissionsRowCount = 3

// cmdPermissions opens the inline /permissions picker below the cmdline.
// The system policy is read-only; project-shared and project-local files are
// shown separately so scope and ownership are explicit.
func cmdPermissions(m Model, _ []string) (Model, tea.Cmd) {
	if m.perms == nil {
		m.appendLine(styleError.Render("[permissions] permissions store unavailable in this session"))
		return m, nil
	}
	if err := m.perms.Reload(); err != nil {
		// Keep the existing in-memory policy active when a just-edited file is
		// malformed, but surface the parse/read failure immediately so the
		// user can reopen /permissions and fix it without guessing.
		m.appendLine(styleError.Render("[permissions] reload: " + err.Error()))
	}
	m.permissionsOpen = true
	m.permissionsCursor = 0
	return m, nil
}

// updatePermissionsPicker handles keystrokes while the /permissions
// picker is the foreground modal. Esc closes, ↑/↓ navigates the three
// policy sources, and Enter opens an editable project file in vim. The
// system policy row is read-only.
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
		row := m.permissionsCursor
		path := m.permissionsRowPath(row)
		m.permissionsOpen = false
		m.permissionsCursor = 0
		if path == "" {
			m.appendLine(styleError.Render("[permissions] file path unavailable"))
			return m, nil
		}
		if row == 0 {
			m.appendLine(styleNoticeWarn.Render("[permissions] system policy is administrator-managed and read-only"))
			return m, nil
		}
		// Seed the writable project files before vim opens. The system policy
		// is intentionally never created or modified here.
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
		return m.perms.SystemPath()
	case 1:
		return m.perms.SharedPath()
	case 2:
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
func renderPermissionsOverlay(m Model, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	global := ""
	shared := ""
	local := ""
	if m.perms != nil {
		global = tildeifyHome(m.perms.SystemPath())
		shared = dotifyCwd(m.perms.SharedPath(), m.cwd)
		local = dotifyCwd(m.perms.LocalPath(), m.cwd)
	}

	rows := []struct {
		Label string
		Path  string
	}{
		{Label: "system", Path: global},
		{Label: "shared", Path: shared},
		{Label: "local", Path: local},
	}

	width := m.popupWidth()
	var b strings.Builder
	b.WriteString(renderMenuHeader("Permissions",
		"View system policy; edit project shared or local rules in vim. Rules reload each time this picker opens.", width))
	b.WriteString("\n")
	warnings := m.permissionsWarnings()
	if len(warnings) > 0 {
		b.WriteString(styleNoticeWarn.Render("Policy warnings"))
		b.WriteString("\n")
		for _, warning := range warnings {
			b.WriteString(styleHint.Render("  • " + truncateDisplayMiddle(warning, width-4)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	for i, row := range rows {
		rowLine := strings.Count(b.String(), "\n")
		h.row(rowLine, i)
		path := truncateDisplayMiddle(row.Path, width-13)
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:      row.Label,
			LabelWidth: 8,
			Desc:       path,
			Cursor:     i == m.permissionsCursor,
		}))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("↵ open project file in vim · system is read-only · esc back · ↑↓ navigate"))
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) permissionsWarnings() []string {
	if m.perms == nil {
		return nil
	}
	warnings := m.perms.LintWarnings()
	const maxWarnings = 4
	if len(warnings) <= maxWarnings {
		return warnings
	}
	out := append([]string{}, warnings[:maxWarnings]...)
	out = append(out, fmt.Sprintf("%d more warning(s); open the files to review", len(warnings)-maxWarnings))
	return out
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
