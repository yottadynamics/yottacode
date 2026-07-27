package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

// cmdThemes is the dispatcher for /theme.
//
//	(bare)          — open the interactive picker (list + live preview)
//	set <name>      — bypass the picker; apply + persist directly
//	<name>          — positional shortcut; same as `set <name>`
//
// list / show / preview are intentionally NOT subcommands — they're
// all subsumed by the picker, which renders the full list, marks the
// active theme, and live-previews on every cursor move. Scripting
// callers that need a non-interactive entry point use the bare `set`
// form.
//
// `set` and the positional shortcut DO cancel an active turn (the
// default cancel-on-slash behavior) because switching palettes
// mid-stream produces a half-styled view; the picker itself is
// PreservesTurn-safe because it only inspects state until Enter.
//
// Function name keeps the plural cmdThemes for git history continuity
// — the slash command is /theme (singular) but the Go symbol stays
// stable so blame walks aren't disrupted.
func cmdThemes(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.openThemePicker()
		return m, nil
	}
	switch args[0] {
	case "set", "use":
		return cmdThemesSet(m, args[1:])
	default:
		// Positional shortcut — /themes <name> behaves like
		// /themes set <name>, mirroring /model <name>. Anything that
		// isn't a known theme name is rejected so a typo doesn't
		// silently fall through to the picker.
		if themes.IsValid(args[0]) {
			return cmdThemesSet(m, args)
		}
		m.appendLine(styleError.Render(fmt.Sprintf(
			"unknown theme or subcommand %q — try /theme (no args) for the picker, or /theme set <name>",
			args[0])))
		return m, nil
	}
}

// cmdThemesSet applies <name>, rebuilds component-captured styles on
// the running Model (spinner, textarea prompts), and persists the
// choice to config.toml. Persistence is best-effort: a write failure
// reports the error but leaves the in-memory theme applied so the
// session continues styled correctly.
//
// Exposed as a separate entrypoint (vs. inlined in cmdThemes) so the
// picker's Enter path can share the same persistence flow via
// commitThemeChoice in themes_picker.go — keeping all the "write +
// validate + log" steps in one shape.
func cmdThemesSet(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendLine(styleError.Render("usage: /theme set <name> — or just /theme for the picker"))
		return m, nil
	}
	name := args[0]
	if !themes.IsValid(name) {
		m.appendLine(styleError.Render(fmt.Sprintf(
			"unknown theme %q — try one of: %s",
			name, strings.Join(themes.Names(), ", "))))
		return m, nil
	}
	ApplyTheme(name)
	m = refreshComponentStyles(m)

	cfg := loadConfigForCommand(m)
	cfg.Theme.Name = name
	if err := config.Validate(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("validation: %v", err)))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("write config: %v", err)))
		// Don't return — the theme is still applied in-memory.
	}
	m.fileCfg = cfg

	m.appendLine(styleAuto.Render(fmt.Sprintf("[theme] switched to %q (persisted)", name)))
	return m, nil
}

// refreshComponentStyles replays styles into sub-components that
// captured them by value at construction time. Bubble Tea's
// textarea and spinner deep-copy the lipgloss.Style they're given,
// and glamour bakes its style choice in at construction too, so
// reassigning the package var after construction wouldn't reach any
// of them. Called from the picker (on every cursor move and on
// commit) and from cmdThemesSet so the switch lands on every visible
// surface, not just the render-time lookups.
//
// Returns the Model unchanged in field shape — only sub-component
// internals are mutated.
func refreshComponentStyles(m Model) Model {
	m.textInput.FocusedStyle.Prompt = styleInputPrompt
	m.textInput.BlurredStyle.Prompt = styleInputPrompt
	m.spinner.Style = styleSpinner
	m.md = newMarkdownRenderer(m.width - 4)
	return m
}
