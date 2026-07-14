package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/config"
)

// cmdProviderEntry dispatches /provider. The bare invocation now
// opens the sub-menu picker overlay (Add / Remove / List / Use).
// Subcommands stay as flag-driven shortcuts for muscle memory and
// scripting parity:
//
//	/provider                — open the sub-menu picker
//	/provider list           — flat-text list (same content the picker dumps)
//	/provider use <name>     — direct switch (skips the picker)
//	/provider remove <name>  — direct removal
//	/provider add <name> --kind … (covered by the legacy parseProviderFlags
//	                          path; M8 will route this through the picker)
//	/provider models <name>  — list one profile's catalog (flat text)
//
// The picker handlers live in provider_picker.go; this file is the
// thin slash-handler layer.
func cmdProviderEntry(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.openProviderPicker()
		return m, nil
	}
	switch strings.ToLower(args[0]) {
	case "list":
		return providerListShortcut(m)
	case "use":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /provider use <name>"))
			return m, nil
		}
		return providerUse(m, args[1])
	case "add":
		return providerAdd(m, args[1:])
	case "remove", "rm":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /provider remove <name>"))
			return m, nil
		}
		return providerRemove(m, args[1])
	case "models":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /provider models <name>"))
			return m, nil
		}
		cfg := loadConfigForCommand(m)
		p := cfg.FindProvider(args[1])
		if p == nil {
			m.appendLine(styleError.Render(fmt.Sprintf("provider %q not found", args[1])))
			return m, nil
		}
		m.appendLine(formatProviderModels(p, m.modelName))
		return m, nil
	default:
		m.appendLine(styleError.Render("usage: /provider [list|use|add|remove|models] ..."))
		return m, nil
	}
}

// providerListShortcut handles `/provider list`. Now opens the
// picker directly into the Use sub-screen (the "Switch provider"
// list with ✓ on the active row + Enter-to-switch) rather than
// dumping a flat-text table to scrollback. The shortcut name still
// reads as "list" for muscle-memory parity, but List + Use have
// collapsed into one inline screen — the user-facing affordance is
// the same: see configured providers, switch with Enter.
//
// Renders inline below the cmdline + status bar via the shared
// renderInlineOverlay helper, matching every other picker.
func providerListShortcut(m Model) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	if len(cfg.Providers) == 0 {
		m.appendLine(styleAuto.Render("no providers configured — try /provider add"))
		return m, nil
	}
	m.openProviderPicker(providerUsePickerMode)
	return m, nil
}

// loadConfigOrEmpty returns the on-disk config or a usable empty
// Default() if loading fails. Mirror of loadConfigForCommand kept
// here for callers that need it without going through the model.
//
// Currently unused; kept for symmetry with the picker layer's
// helpers. Remove if it's still unused after M9.
var _ = func() config.Config { return config.Default() }
