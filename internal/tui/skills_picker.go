package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/skills"
)

// skillsPickerState backs the /skills inline overlay. Lifetime is one
// /skills invocation. Multi-select via spacebar — the user can toggle
// any number of rows in either direction before pressing `c` to
// commit. Esc cancels without applying changes.
//
// Pattern mirrors subagentsPickerState but with one important
// addition: a per-row toggle map. The original SkillTool enablement
// state is the source of truth at open; the picker mutates a working
// copy and only writes back on commit.
type skillsPickerState struct {
	// rows is the full universe (built-in + user + project), sorted
	// by name. Stable across navigation; commit flushes the
	// enablement set against this list.
	rows []skills.Skill

	// enabled is the working copy of which skills are turned on. Keys
	// are skill names; missing == disabled. Initialized from
	// SkillTool.IsEnabled at open. The Tool's own enablement map is
	// only updated on commit, so Esc-cancel restores cleanly.
	enabled map[string]bool

	cursor int
	status string
}

// openSkillsPicker captures a snapshot of the SkillTool's universe
// and enablement state and installs the picker on m. Empty universe
// still opens the picker with an empty-state hint so the user can
// confirm "no skills configured" without re-typing the command.
func (m *Model) openSkillsPicker() {
	if m.skillTool == nil {
		m.appendLine(styleError.Render("skills are not wired in this session (Skill tool unavailable)"))
		return
	}
	all := append([]skills.Skill(nil), m.skillTool.All...)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	enabled := make(map[string]bool, len(all))
	for _, sk := range all {
		enabled[sk.Name] = m.skillTool.IsEnabled(sk.Name)
	}
	m.skillsPicker = &skillsPickerState{
		rows:    all,
		enabled: enabled,
	}
	m.skillsPickerOpen = true
}

// updateSkillsPicker handles keystrokes while the picker is open.
// Navigation: Up/Down to move. Space toggles the cursor row's
// enablement. `a` enables all; `n` disables all. Enter opens the
// body in $PAGER for the cursor row. `c` commits the toggles and
// closes; Esc cancels.
func (m Model) updateSkillsPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.skillsPicker == nil {
		m.skillsPickerOpen = false
		return m, nil
	}
	p := m.skillsPicker
	switch msg.Type {
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		p.status = ""
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.rows)-1 {
			p.cursor++
		}
		p.status = ""
		return m, nil
	case tea.KeyEnter:
		// Enter views the body in $PAGER (parity with other pickers'
		// "open the artifact" semantics — checkpoints picker, sessions
		// picker, subagents transcript view).
		if len(p.rows) == 0 {
			return m, nil
		}
		sk := p.rows[p.cursor]
		path, err := stageSkillBodyForPager(sk)
		if err != nil {
			p.status = "could not open body: " + err.Error()
			return m, nil
		}
		m.skillsPickerOpen = false
		m.skillsPicker = nil
		return m.openTranscriptInPager(path, sk.Name, false)
	case tea.KeyEsc:
		m.skillsPickerOpen = false
		m.skillsPicker = nil
		return m, nil
	case tea.KeySpace:
		if len(p.rows) == 0 {
			return m, nil
		}
		name := p.rows[p.cursor].Name
		p.enabled[name] = !p.enabled[name]
		p.status = ""
		return m, nil
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "a":
			for _, sk := range p.rows {
				p.enabled[sk.Name] = true
			}
			p.status = "enabled all"
			return m, nil
		case "n":
			for _, sk := range p.rows {
				p.enabled[sk.Name] = false
			}
			p.status = "disabled all"
			return m, nil
		case "c":
			return commitSkillsPicker(m, p)
		}
	}
	return m, nil
}

// commitSkillsPicker pushes the working enablement set into the live
// SkillTool, recomposes the system prompt so the next turn reflects
// the new "Available skills" section, and closes the overlay. The
// per-session enablement is not persisted to disk — opening a new
// session starts everything enabled again.
func commitSkillsPicker(m Model, p *skillsPickerState) (Model, tea.Cmd) {
	if m.skillTool == nil {
		m.skillsPickerOpen = false
		m.skillsPicker = nil
		return m, nil
	}
	// Build the SetEnabled map: include only entries that are on so
	// SkillTool.Active() filters cleanly. Passing the raw map (which
	// includes false entries) would also work — SetEnabled drops
	// falses — but the explicit on-only set is easier to reason about
	// when debugging.
	on := make(map[string]bool, len(p.enabled))
	enabledCount := 0
	for name, isOn := range p.enabled {
		if isOn {
			on[name] = true
			enabledCount++
		}
	}
	m.skillTool.SetEnabled(on)
	// Recompose the session's system prompt so the next turn sees the
	// updated "Available skills" section. Reuses the same composition
	// path memory reload uses, sharing one builder ensures the two
	// can't drift.
	m, _ = recomposeSystemPromptWithSkills(m, m.skillTool.Active())
	m.skillsPickerOpen = false
	m.skillsPicker = nil
	total := len(p.rows)
	m.appendLine(styleAuto.Render(fmt.Sprintf("[skills] %d of %d enabled (next turn will see the updated set)", enabledCount, total)))
	return m, nil
}

// stageSkillBodyForPager writes the skill body to a temp file so it
// can be opened in $PAGER. Built-in skills don't have a real disk
// path (their SourcePath is "embed:..."), so we always stage to a
// temp file rather than try to open in place. Returns the staged
// path.
func stageSkillBodyForPager(sk skills.Skill) (string, error) {
	dir, err := os.MkdirTemp("", "yottacode-skill-")
	if err != nil {
		return "", err
	}
	// File name carries the skill slug so the pager title bar is
	// meaningful.
	path := filepath.Join(dir, sk.Name+".md")
	body := fmt.Sprintf("# %s\n\n_%s — %s_\n\n%s\n", sk.Name, sk.Source, sk.SourcePath, strings.TrimSpace(sk.Body))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// renderSkillsPicker draws the overlay body. Each row is one skill
// with a check/uncheck glyph reflecting the working enablement
// state, the skill name, its source tag, and a truncated description.
// Cursor highlighting is handled by renderMenuItem.
func renderSkillsPicker(state *skillsPickerState, _ int) string {
	header := renderMenuHeader(
		"Skills",
		"Space toggles · Enter views body · a/n enable/disable all · c commits · Esc cancels",
	)
	if len(state.rows) == 0 {
		return header + "\n" +
			stylePaletteEmpty.Render("  no skills configured — drop a SKILL.md into ~/.yottacode/skills/<slug>/") +
			"\n"
	}
	// Compute the longest name across rows so columns align. Cap is
	// generous (32) — longest builtin today is 30
	// ("verification-before-completion"), longest plausible
	// community-skill name is ~30. The cap exists only to prevent a
	// pathological 64-char name from pushing the description column
	// off the screen.
	maxName := 6
	for _, sk := range state.rows {
		if l := runeCount(sk.Name); l > maxName {
			maxName = l
		}
	}
	if maxName > 32 {
		maxName = 32
	}
	const sourceW = 10 // "[built-in]" — widest of "[built-in]", "[project]", "[user]"

	body := header + "\n"
	for i, sk := range state.rows {
		mark := "[ ]"
		if state.enabled[sk.Name] {
			mark = "[x]"
		}
		// Pad by visual column count (rune count), not byte length.
		// Names that would overflow maxName are truncated by rune
		// position with an ellipsis suffix; this keeps the source
		// column aligned without bleeding into bytes-vs-runes confusion
		// downstream. We deliberately do NOT pass LabelWidth into
		// menuItemOpts — that path would re-truncate by len(label)
		// bytes and chop the source column when the name's ellipsis
		// is multi-byte.
		name := truncateRunes(sk.Name, maxName)
		namePad := strings.Repeat(" ", maxName-runeCount(name))
		source := "[" + string(sk.Source) + "]"
		sourcePad := strings.Repeat(" ", sourceW-runeCount(source))
		label := mark + " " + name + namePad + " " + source + sourcePad
		body += renderMenuItem(menuItemOpts{
			Label:  label,
			Desc:   truncateForRender(sk.Description, 80),
			Cursor: i == state.cursor,
		}) + "\n"
	}
	if state.status != "" {
		body += "\n" + stylePaletteEmpty.Render("  "+state.status)
	}
	return body
}

// runeCount returns the number of Unicode codepoints in s. Using
// utf8.RuneCountInString here means our column math agrees with how
// the terminal renders the string, even when the string contains
// multi-byte runes like the ellipsis (`…` = 3 bytes / 1 column).
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// truncateRunes returns at most n runes of s, suffixed with "…" when
// truncation happens. Distinct from truncateForRender (which counts
// bytes) so the picker's column layout doesn't get thrown off when a
// truncated string carries a multi-byte ellipsis.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	end := 0
	for i := range s {
		if count == n {
			break
		}
		count++
		end = i + 1
		// Advance end past the current rune. range on a string yields
		// the byte index of each rune's first byte; we need the byte
		// position one past the last byte of the nth rune.
		_ = i
	}
	if count < n || end == len(s) {
		return s
	}
	// Replace the last rune with "…" to make truncation visible.
	last := 0
	count = 0
	for i := range s {
		if count == n-1 {
			last = i
			break
		}
		count++
	}
	return s[:last] + "…"
}

// recomposeSystemPromptWithSkills rebuilds the session's system
// message so it reflects the currently-active skills. Mirrors the
// reloadMemoryNow path (cmd_memory.go): reload memory, compose the
// base prompt + provider note + active skills section + memory, and
// patch the session's first system message in place. Errors fall
// soft — we surface them to the user but never block the picker
// commit, since the SkillTool's enablement set has already been
// applied and will be respected on the next turn regardless.
func recomposeSystemPromptWithSkills(m Model, active []skills.Skill) (Model, tea.Cmd) {
	mem, err := memory.Load(m.cwd)
	if err != nil {
		m.appendLine(styleError.Render("[skills] memory reload failed: " + err.Error()))
		return m, nil
	}
	base := appendSkillsSection(composeSystemPrompt(m.baseSystemPrompt, m.providerProfile), active)
	newSys := memory.SystemPrompt(base, mem)
	for i := range m.sess.Messages {
		if m.sess.Messages[i].Role == adapter.RoleSystem {
			m.sess.Messages[i].Content = newSys
			break
		}
	}
	return m, nil
}
