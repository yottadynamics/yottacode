package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// plansPickerState backs the `/plan list` overlay. Pattern mirrors
// the other inline pickers (model, provider, sessions): cursor index
// drives the highlighted row, Up/Down navigate, Enter resumes, Esc
// closes.
type plansPickerState struct {
	plans  []agent.PlanEntry
	cursor int
}

// openPlansPicker fetches the plans from disk and opens the inline
// overlay. Empty list short-circuits with a log line — no point
// showing an empty picker.
func (m *Model) openPlansPicker() {
	plans, err := agent.ListPlans()
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[plan] could not read plans dir: %v", err)))
		return
	}
	if len(plans) == 0 {
		m.appendLine(styleAuto.Render(PlanModeIcon + " no plans saved yet — /plan starts a fresh one"))
		return
	}
	m.plansPicker = &plansPickerState{plans: plans}
	m.plansPickerOpen = true
}

// updatePlansPicker handles keystrokes while the plans picker is open.
// Mirrors the navigation contract of the other inline overlays.
func (m Model) updatePlansPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.plansPicker == nil {
		m.plansPickerOpen = false
		return m, nil
	}
	switch msg.Code {
	case tea.KeyUp:
		if m.plansPicker.cursor > 0 {
			m.plansPicker.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.plansPicker.cursor < len(m.plansPicker.plans)-1 {
			m.plansPicker.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		chosen := m.plansPicker.plans[m.plansPicker.cursor]
		m.plansPickerOpen = false
		m.plansPicker = nil
		resumePlanFile(&m, chosen)
		return m, nil
	case tea.KeyEsc:
		m.plansPickerOpen = false
		m.plansPicker = nil
		return m, nil
	}
	return m, nil
}

// renderPlansPicker draws the overlay body. Each row is one plan with
// its slug + relative-time hint, the newest plan at the top.
func renderPlansPicker(state *plansPickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	header := renderMenuHeader(
		PlanModeIcon+" Resume a plan",
		"Newest first. Enter resumes; Esc closes.",
	)
	maxSlug := 24
	for _, p := range state.plans {
		if l := len(p.Slug); l > maxSlug {
			maxSlug = l
		}
	}
	body := header + "\n"
	for i, p := range state.plans {
		row := strings.Count(body, "\n")
		h.row(row, i)
		body += renderMenuItem(menuItemOpts{
			Label:      p.Slug,
			LabelWidth: maxSlug,
			Desc:       formatPlanAge(time.Since(p.Modified)),
			Cursor:     i == state.cursor,
		}) + "\n"
	}
	return body
}

// formatPlanAge renders the "N min ago" style descriptor seen in the
// picker. Bands match the rest of yottacode's age formatting in
// /recall so the surface feels consistent.
func formatPlanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%d weeks ago", int(d.Hours()/24/7))
	}
}

// resumePlanFile attaches an existing plan file to the session,
// activating plan mode if it isn't already. Symmetric counterpart to
// togglePlanMode's entry path — same two-line shape (headline + dim
// exit hint), same write-tool wiring. Idempotent against double-invokes.
//
// The headline embeds a truncated slug + size + age so the user can
// confirm WHICH plan they just resumed at a glance, without the full
// 80-char path drowning the scrollback.
func resumePlanFile(m *Model, entry agent.PlanEntry) {
	state := m.cfg.PlanMode
	if state == nil {
		m.appendLine(styleError.Render("[plan] plan mode is not available in this build"))
		return
	}
	state.PlanFile = entry.Path
	applyPlanFileToWriteTools(m.cfg.Registry, entry.Path)
	state.Active.Store(true)

	short := truncateSlug(entry.Slug, 30)
	meta := fmt.Sprintf("%s · %s", formatPlanSize(entry.Size), formatPlanAge(time.Since(entry.Modified)))
	m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon+" plan resumed") +
		" " + stylePlanBannerHint.Render(fmt.Sprintf("— %s.md (%s)", short, meta)))
	m.appendLine(stylePlanBannerHint.Render("  exit with /plan or Shift+Tab"))
	// Visual breather between the entry log and the live banner.
	m.appendLine("")
}

// truncateSlug shortens a long plan slug while preserving the
// hash-suffix on the right (the last 8 hex chars) so two plans that
// share an opening phrase remain distinguishable. Keeps the first
// (max - 9) chars and the last 8, joined by an ellipsis.
//
//	"i-want-to-implement-the-same-command-in-claude-context-here-f3fcc291bb20dae3"
//	→ "i-want-to-implem…0dae3" at max=24
//
// Returns the slug unchanged when it already fits.
func truncateSlug(slug string, max int) string {
	if max < 12 {
		max = 12
	}
	if len(slug) <= max {
		return slug
	}
	const tail = 8
	head := max - tail - 1 // 1 for the ellipsis rune
	if head < 4 {
		head = 4
	}
	return slug[:head] + "…" + slug[len(slug)-tail:]
}

// formatPlanSize renders a file size with a sensible unit so the
// resume headline reads as "1.2 KB" rather than "1247 B". Bands
// match the rest of yottacode's surface (no MB-padded zeros).
func formatPlanSize(bytes int64) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%d B", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
}
