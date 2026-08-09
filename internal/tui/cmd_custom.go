package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/usercmd"
)

// buildCustomSlash converts user-authored commands (loaded by the
// usercmd package at startup) into the slashCommand shape the
// dispatcher and the palette already understand. Each command's Run
// is a closure that substitutes $ARGUMENTS / $1..$9 and hands the
// resolved prompt to startTurn — the same path /init takes. @<path>
// file refs in the body are picked up automatically by startTurn's
// existing filerefs.Parse call.
//
// One Run per command (captured `cmd` in the closure), so a registry
// of N custom commands produces N independent closures with no
// shared mutable state.
func buildCustomSlash(cmds []usercmd.Command) []slashCommand {
	if len(cmds) == 0 {
		return nil
	}
	out := make([]slashCommand, 0, len(cmds))
	for _, cmd := range cmds {
		help := cmd.Description
		if help == "" {
			// Without a frontmatter description, /help and the palette
			// would have an empty right column. Fall back to a generic
			// label so the row still reads as something rather than
			// blank, and surface the path so users can tell where to
			// look. The Source field below handles the path display
			// for /help; the help text mirrors that for the palette.
			help = "(custom command)"
		}
		out = append(out, slashCommand{
			Name:     cmd.Name,
			Args:     cmd.ArgHint,
			Help:     help,
			Source:   cmd.SourceFile,
			IsCustom: true,
			// Custom commands send a prompt to the agent — they're
			// stateful (will produce a new turn), so they cancel
			// like /clear or /model rather than preserving the turn.
			PreservesTurn: false,
			Run:           customCommandRun(cmd),
		})
	}
	return out
}

// customCommandRun returns the dispatcher closure for one custom
// command. Mirrors cmdInit: substitute args into the body, then
// hand the result to the agent as if the user had typed it — but
// renders only the compact "/cmd args" invocation in scrollback so
// the user's view isn't swamped by the 30-80 lines of directive
// prompt the agent needs to see.
//
// turnActive bail-out is identical to cmdInit's so a custom command
// invoked mid-turn surfaces a clear error rather than queueing or
// silently no-op-ing.
func customCommandRun(cmd usercmd.Command) func(Model, []string) (Model, tea.Cmd) {
	return func(m Model, args []string) (Model, tea.Cmd) {
		if m.turnActive {
			m.appendLine(styleError.Render("[" + cmd.Name + "] a turn is already running — wait for it to finish or press Esc to cancel"))
			return m, nil
		}
		prompt := usercmd.Substitute(cmd.Body, strings.Join(args, " "))
		if strings.TrimSpace(prompt) == "" {
			m.appendLine(styleError.Render("[" + cmd.Name + "] command body is empty after substitution"))
			return m, nil
		}
		display := "/" + cmd.Name
		if len(args) > 0 {
			display += " " + strings.Join(args, " ")
		}
		out, c := m.startTurnWithDisplay(prompt, display)
		return out.(Model), c
	}
}
