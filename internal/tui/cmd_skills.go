package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/skills"
	"github.com/yottadynamics/yottacode/internal/usercmd"
)

// cmdSkills is the /skills entry point. No args opens the inline
// picker (the original behavior — multi-select to enable for the
// session). With args, dispatches to install / list / show /
// uninstall — the slash mirror of the `yottacode skills` CLI tree, so
// users don't have to leave the TUI to manage what's installed.
func cmdSkills(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.openSkillsMenu()
		return m, nil
	}
	switch args[0] {
	case "install":
		return cmdSkillsInstall(m, args[1:])
	case "list", "ls":
		// Catalog covers what "list" used to do (loaded skills with
		// source + description) and adds tabs + filter + per-row
		// actions on top. The flat-text /skills list dump was
		// redundant *and* badly wrapped on long descriptions in
		// narrow terminals; redirecting preserves user muscle
		// memory while routing them to the better view. The CLI
		// `yottacode skills list` remains for scripting use.
		return skillsMenuOpenCatalog(m)
	case "show":
		return cmdSkillsShow(m, args[1:])
	case "uninstall", "remove", "rm":
		return cmdSkillsUninstall(m, args[1:])
	case "check":
		return cmdSkillsCheck(m, args[1:])
	case "update":
		return cmdSkillsUpdate(m, args[1:])
	default:
		m.appendLine(styleError.Render(
			"[skills] unknown subcommand " + args[0] +
				" (try: install <source> [--force], show <name>, uninstall <name>, check [name], update [name] [--force])"))
		return m, nil
	}
}

// cmdSkillsInstall parses the source + optional --force flag, runs
// the installer, and refreshes the session's loaded-skills state so
// the new skill is immediately visible to /skills, /help, and the
// slash dispatcher.
func cmdSkillsInstall(m Model, args []string) (Model, tea.Cmd) {
	var source string
	var force bool
	for _, a := range args {
		switch a {
		case "--force", "-f":
			force = true
		default:
			if source == "" {
				source = a
			}
		}
	}
	if source == "" {
		m.appendLine(styleError.Render("usage: /skills install <source> [--force]"))
		return m, nil
	}
	res, err := skills.Install(skills.InstallOptions{Source: source, Force: force})
	if err != nil {
		m.appendLine(styleError.Render("[skills] install: " + err.Error()))
		return m, nil
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf(
		"[skills] installed %s (%s) at %s", res.Skill.Name, res.SourceType, res.Dir)))
	for _, w := range res.Warnings {
		m.appendLine(styleError.Render("[skills] warning: " + w))
	}
	// Auto-enable the just-installed skill so it's immediately
	// visible with a checkmark in the picker and exposed to the
	// model on the next turn. reloadSkillsRegistry's recompose call
	// picks up the new enablement.
	if m.skillTool != nil {
		m.skillTool.Enable(res.Skill.Name)
	}
	return reloadSkillsRegistry(m), nil
}

// cmdSkillsCheck runs the drift check and dumps one line per skill.
// Read-only — no registry reload needed.
func cmdSkillsCheck(m Model, args []string) (Model, tea.Cmd) {
	opts := skills.CheckOptions{}
	if len(args) > 0 {
		opts.Name = args[0]
	}
	results, err := skills.Check(opts)
	if err != nil {
		m.appendLine(styleError.Render("[skills] check: " + err.Error()))
		return m, nil
	}
	if len(results) == 0 {
		m.appendLine("(no user-installed skills)")
		return m, nil
	}
	maxName := 4
	for _, r := range results {
		if l := len(r.Name); l > maxName {
			maxName = l
		}
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "  %-*s  %s", maxName, r.Name, r.Status)
		if r.Error != "" {
			b.WriteString("  (")
			b.WriteString(r.Error)
			b.WriteString(")")
		}
		b.WriteByte('\n')
	}
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return m, nil
}

// cmdSkillsUpdate parses [name] [--force], runs the updater, prints
// per-skill status, and reloads the in-session registry so any newly
// installed bytes flow to the next turn.
func cmdSkillsUpdate(m Model, args []string) (Model, tea.Cmd) {
	opts := skills.UpdateOptions{}
	for _, a := range args {
		switch a {
		case "--force", "-f":
			opts.Force = true
		default:
			if opts.Name == "" {
				opts.Name = a
			}
		}
	}
	results, err := skills.Update(opts)
	if err != nil {
		m.appendLine(styleError.Render("[skills] update: " + err.Error()))
		return m, nil
	}
	if len(results) == 0 {
		m.appendLine("(no tracked skills to update)")
		return m, nil
	}
	maxName := 4
	for _, r := range results {
		if l := len(r.Name); l > maxName {
			maxName = l
		}
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "  %-*s  %s", maxName, r.Name, r.Status)
		if r.Message != "" {
			b.WriteString("  (")
			b.WriteString(r.Message)
			b.WriteString(")")
		}
		b.WriteByte('\n')
	}
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return reloadSkillsRegistry(m), nil
}

// cmdSkillsShow prints the full body of one skill to the transcript.
// The Markdown isn't rendered through a styler — keeping it raw mirrors
// what the model receives via Skill(skill="<name>"), which is the
// useful comparison when authoring or debugging a skill.
func cmdSkillsShow(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendLine(styleError.Render("usage: /skills show <name>"))
		return m, nil
	}
	if m.skillTool == nil {
		m.appendLine(styleError.Render("[skills] not available in this session"))
		return m, nil
	}
	name := args[0]
	sk := skills.Find(m.skillTool.All, name)
	if sk == nil {
		m.appendLine(styleError.Render("[skills] no skill named " + name))
		return m, nil
	}
	m.appendLine(renderSkillForTranscript(*sk))
	return m, nil
}

// renderSkillForTranscript formats a Skill for inline display in the
// transcript. Same shape as the CLI `show` output: header line +
// optional Author/Source metadata block + body. Empty metadata
// fields are dropped so built-ins (which usually don't populate
// them) stay compact.
func renderSkillForTranscript(sk skills.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n_%s — %s_\n", sk.Name, sk.Source, sk.SourcePath)
	if author := strings.TrimSpace(sk.Metadata["author"]); author != "" {
		fmt.Fprintf(&b, "Author: %s\n", author)
	}
	if src := strings.TrimSpace(sk.Metadata["source-url"]); src != "" {
		fmt.Fprintf(&b, "Source: %s\n", src)
	}
	b.WriteByte('\n')
	b.WriteString(sk.Body)
	return b.String()
}

// cmdSkillsUninstall removes a user-scope skill and refreshes the
// session. Built-in and project skills are out of scope (the installer
// package enforces this) — the error from skills.Uninstall is the
// user-facing signal.
func cmdSkillsUninstall(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendLine(styleError.Render("usage: /skills uninstall <name>"))
		return m, nil
	}
	path, err := skills.Uninstall(args[0])
	if err != nil {
		m.appendLine(styleError.Render("[skills] uninstall: " + err.Error()))
		return m, nil
	}
	m.appendLine(styleAuto.Render("[skills] removed " + path))
	return reloadSkillsRegistry(m), nil
}

// reloadSkillsRegistry re-runs the disk scan and rewires every
// in-session structure that holds a copy of the loaded set:
//
//   - m.skillTool.All — the model-facing universe (preserved
//     enablement is dropped intentionally; an installed-then-removed
//     skill shouldn't leave a dangling "enabled" entry).
//   - m.skills — the welcome-card / /help mirror.
//   - m.skillSlash — the /<skill-name> slash dispatch list.
//
// The system prompt is also recomposed so any newly enabled skill
// reaches the next turn. Warnings from LoadAll are surfaced into the
// transcript so a malformed skill installed by the same command
// doesn't fail silently.
func reloadSkillsRegistry(m Model) Model {
	res, err := skills.LoadAll(m.cwd, usercmd.Reserved)
	if err != nil {
		m.appendLine(styleError.Render("[skills] reload: " + err.Error()))
		return m
	}
	for _, w := range res.Warnings {
		m.appendLine(styleError.Render("[skills] " + w))
	}
	if m.skillTool != nil {
		// SetAll, not a direct field write: the agent goroutine may be
		// reading All via Active() while this reload runs.
		m.skillTool.SetAll(res.Skills)
	}
	m.skills = res.Skills
	m.skillSlash = buildSkillSlash(res.Skills)
	if m.skillTool != nil {
		m, _ = recomposeSystemPromptWithSkills(m, m.skillTool.Active())
	}
	return m
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
