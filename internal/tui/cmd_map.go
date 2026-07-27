package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func cmdMap(m Model, args []string) (Model, tea.Cmd) {
	mode := codeMapModeStructure
	depth := codemapDefaultImpactDepth
	query := strings.TrimSpace(strings.Join(args, " "))
	if len(args) > 0 {
		switch args[0] {
		case "deps", "dependencies":
			mode = codeMapModeDependencies
			query = strings.TrimSpace(strings.Join(args[1:], " "))
		case "dependents", "reverse":
			mode = codeMapModeDependents
			query = strings.TrimSpace(strings.Join(args[1:], " "))
		case "impact":
			mode = codeMapModeImpact
			query, depth = parseMapImpactArgs(args[1:])
		case "cycles":
			mode = codeMapModeCycles
			query = strings.TrimSpace(strings.Join(args[1:], " "))
		case "diagram":
			mode = codeMapModeDiagram
			query = strings.TrimSpace(strings.Join(args[1:], " "))
		case "here":
			mode = codeMapModeHere
			query = strings.TrimSpace(strings.Join(args[1:], " "))
		}
	}
	return m.openCodeMapPicker(mode, query, depth)
}

func parseMapImpactArgs(args []string) (string, int) {
	depth := codemapDefaultImpactDepth
	kept := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--depth" && i+1 < len(args) {
			depth = parseImpactDepth(args[i+1], depth)
			i++
			continue
		}
		if strings.HasPrefix(a, "--depth=") {
			depth = parseImpactDepth(strings.TrimPrefix(a, "--depth="), depth)
			continue
		}
		kept = append(kept, a)
	}
	return strings.TrimSpace(strings.Join(kept, " ")), depth
}

func parseImpactDepth(raw string, fallback int) int {
	if raw == "all" || raw == "-1" {
		return codemapDefaultImpactDepth
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n < 1 {
		return fallback
	}
	return n
}
