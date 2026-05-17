package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/skills"
)

// cmdSkills opens the /skills inline picker. The user multi-selects
// which skills are exposed to the model for the rest of the session.
// Empty universe still opens the picker with an empty-state hint so
// the user can confirm "no skills configured" without re-typing.
func cmdSkills(m Model, _ []string) (Model, tea.Cmd) {
	m.openSkillsPicker()
	return m, nil
}

// buildSkillSlash converts loaded Agent Skills into the slashCommand
// shape the dispatcher and palette understand. Only skills with
// metadata.slash != "false" get an entry — skills can opt out of the
// palette when they're meant purely as model-side description-matched
// playbooks. Each Run closure injects the skill body as a synthetic
// user message so the model picks it up and continues the turn,
// mirroring what would happen if the model called the `Skill` tool.
func buildSkillSlash(loaded []skills.Skill) []slashCommand {
	if len(loaded) == 0 {
		return nil
	}
	out := make([]slashCommand, 0, len(loaded))
	for _, sk := range loaded {
		if !sk.SlashEnabled() {
			continue
		}
		out = append(out, slashCommand{
			Name:   sk.Name,
			Help:   sk.Description,
			Source: sk.SourcePath,
			// IsCustom stays false — skills are a distinct category
			// from custom commands. /help renders them under their
			// own "Skills:" header (see cmdHelp).
			PreservesTurn: false,
			Run:           skillCommandRun(sk),
		})
	}
	return out
}

// skillCommandRun returns the dispatcher closure for one skill.
// Mirrors customCommandRun's shape: render the compact "/<name>"
// invocation in scrollback, hand the full skill body to the agent as
// a synthetic user message. The model treats the body as guidance
// for the next reply, equivalent to having called the `Skill` tool
// with this skill's name. Any args after `/<name>` are appended so
// the user can pass context ("/remote-ops on prod-app-01").
func skillCommandRun(sk skills.Skill) func(Model, []string) (Model, tea.Cmd) {
	return func(m Model, args []string) (Model, tea.Cmd) {
		if m.turnActive {
			m.appendLine(styleError.Render("[/" + sk.Name + "] a turn is already running — wait for it to finish or press Esc to cancel"))
			return m, nil
		}
		// Frame the body the same way Claude Code's system reminder
		// frames an invoked skill: explicit "follow this skill" header
		// so the model knows the next paragraph is a playbook, not a
		// task description. Append user-supplied args underneath so
		// "/remote-ops tail logs on prod-app-01" carries the user's
		// intent through.
		var b strings.Builder
		b.WriteString("Apply the following skill (")
		b.WriteString(sk.Name)
		b.WriteString("):\n\n")
		b.WriteString(strings.TrimSpace(sk.Body))
		if extra := strings.TrimSpace(strings.Join(args, " ")); extra != "" {
			b.WriteString("\n\nUser request: ")
			b.WriteString(extra)
		}
		display := "/" + sk.Name
		if len(args) > 0 {
			display += " " + strings.Join(args, " ")
		}
		out, c := m.startTurnWithDisplay(b.String(), display)
		return out.(Model), c
	}
}
