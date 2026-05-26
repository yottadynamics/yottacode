package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// isFreshSession reports whether the session has no user/assistant exchange
// yet (a system-only state). Used to decide whether to surface the welcome
// tip inside the startup card.
func (m Model) isFreshSession() bool {
	for _, msg := range m.sess.Messages {
		if msg.Role == adapter.RoleUser || msg.Role == adapter.RoleAssistant {
			return false
		}
	}
	return true
}

// shouldShowStartupCard reports whether the resize replay path should
// re-render the startup card. Fresh sessions still get the card so
// the user has the context summary above their first prompt. After
// the first user/assistant exchange the status bar carries the same
// model/provider/cwd info, so re-emitting on every resize would just
// take up scrollback.
func (m Model) shouldShowStartupCard() bool {
	return m.isFreshSession()
}

// startupTip returns the rotating tip line to embed inside the startup
// card on fresh sessions. Returns "" on resumed sessions where the
// user is past the onboarding moment.
//
// Tip rotation is deterministic per day (time.Now().YearDay() % len(pool))
// rather than random — gives a single tip per launch on a given day,
// cycles through the whole pool over a week, and stays testable.
// Priority order (highest-leverage onboarding wins):
//  1. memory not yet configured → memory tip
//  2. skills installed but none enabled → skills tip (self-retires once
//     the user picks any via /skills)
//  3. rotating pool by day-of-year
func (m Model) startupTip() string {
	if !m.isFreshSession() {
		return ""
	}
	var skillsToOnboard int
	if m.skillTool != nil && len(m.skills) > 0 && len(m.skillTool.Active()) == 0 {
		skillsToOnboard = len(m.skills)
	}
	return pickTip(m.memorySummary, skillsToOnboard, time.Now().YearDay())
}

// renderInlineCodeSpans walks a tip string and applies styleInlineCode
// to every backtick-delimited span; the surrounding prose is rendered
// with bodyStyle. Hard newlines in the source get replaced with a
// single space so the terminal can wrap to its width — hard-coded line
// breaks looked wrong on wide terminals and crowded on narrow ones.
func renderInlineCodeSpans(s string, bodyStyle lipgloss.Style) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = collapseSpaces(s)
	parts := strings.Split(s, "`")
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 {
			b.WriteString(styleInlineCode.Render(part))
		} else {
			b.WriteString(bodyStyle.Render(part))
		}
	}
	return b.String()
}

// collapseSpaces normalizes runs of whitespace to a single space.
// After we strip the hard newlines from a tip, indentation between
// the wrapped lines collapses cleanly without leaving "word    word"
// gaps inside the rendered output.
func collapseSpaces(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// pickTip is the rotation function. Split out so tests can pin the
// seed and assert specific tips without time-dependence.
//
// Priority: memory tip > skills tip > rotating pool. memorySummary == ""
// forces the memory tip (landing-page-equivalent for a brand-new install).
// skillsToOnboard > 0 means skills are installed but none are enabled
// yet — surface the skills onboarding line so the user discovers /skills.
// Past those gates, dayOfYear picks one from the rotating pool.
func pickTip(memorySummary string, skillsToOnboard int, dayOfYear int) string {
	if memorySummary == "" {
		return memoryTip
	}
	if skillsToOnboard > 0 {
		return fmt.Sprintf(skillsTipFmt, skillsToOnboard)
	}
	pool := tipPool()
	if len(pool) == 0 {
		return ""
	}
	idx := dayOfYear % len(pool)
	if idx < 0 {
		idx = 0
	}
	return pool[idx]
}

// memoryTip is hoisted so a test can assert that an unconfigured user
// always sees this exact line — it's the one tip we don't want to roll
// past randomly.
const memoryTip = "drop preferences into `~/.yottacode/USER.md` so they auto-load into every session."

// skillsTipFmt is the onboarding line shown when skills are installed
// but none are enabled for this session. The %d carries the install
// count so the user sees the surface area before opening the picker.
const skillsTipFmt = "%d skills installed — type `/skills` to enable any for this session."

// tipPool returns the rotating list shown to users who already have
// memory configured. Each entry highlights exactly one feature so the
// reader can act on it without parsing a list — multi-command tips
// dilute the call-to-action and made the panel too dense to scan.
// Keep entries to one sentence; the welcome panel auto-wraps on the
// terminal width.
func tipPool() []string {
	return []string{
		"`/recall <query>` searches every saved session — useful when you remember what you discussed but not where.",
		"`/permissions` lists the shared and local rule files; Enter opens the chosen one in vim so you can pre-approve common tool calls.",
		"`/sessions` opens the picker (Load / Resume / Rename / Export); Load shows recent sessions, Resume takes an id or name directly.",
		"`/memory` opens the picker for USER.md, YOTTACODE.md, and the agent-managed memory dirs (memory_save / memory_forget write here).",
		"`/summarize` compresses the transcript when the context window starts to fill up.",
		"`/init` scans the repo and drafts `.yottacode/YOTTACODE.md` — per-repo context that auto-injects into every turn.",
		"`/provider use <name>` swaps to a different configured provider mid-session, no restart needed.",
		"`/model <name>` switches model on the fly — auto-switches provider when the name belongs to a different profile.",
		"`/help` lists every slash command, and `?` opens the keyboard cheatsheet overlay.",
	}
}
