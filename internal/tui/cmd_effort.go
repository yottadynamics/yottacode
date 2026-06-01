package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdEffort dispatches /effort, the runtime reasoning-effort control.
//
//	(bare)            — open the picker (default · low · medium · high)
//	/effort <level>   — set the level directly; "default" (or off/none)
//	                    clears it back to the provider default
//
// The level applies to every provider that supports reasoning, each via
// its own knob (OpenAI/xAI effort enums; Anthropic/Gemini thinking
// budgets). It's session-only — restart falls back to --reasoning-effort
// / YOTTACODE_REASONING_EFFORT.
func cmdEffort(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.openEffortPicker()
		return m, nil
	}
	raw := strings.TrimSpace(args[0])
	level := normalizedEffort(raw)
	switch level {
	case "", "low", "medium", "high":
		return commitEffortChoice(m, level)
	default:
		m.appendLine(styleError.Render(fmt.Sprintf(
			"unknown effort %q — use default, low, medium, or high (or /effort for the picker)", raw)))
		return m, nil
	}
}
