package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// renderLSPAdvisory returns a non-blocking startup card for supported languages
// whose semantic server is missing. It teaches the user how to unlock the
// experimental LSP tools without forcing the model to discover setup state.
func renderLSPAdvisory(langs []lsp.DetectedLanguage) string {
	missing := make([]lsp.DetectedLanguage, 0, len(langs))
	for _, lang := range langs {
		if lang.FilesAvailable > 0 && !lang.ServerAvailable {
			missing = append(missing, lang)
		}
	}
	if len(missing) == 0 {
		return ""
	}

	rows := []string{styleWarnIcon.Render("LSP Code Intelligence"), ""}
	if len(missing) == 1 {
		lang := missing[0]
		rows = append(rows,
			fmt.Sprintf("%s detected · semantic server missing", lang.Name),
			"",
			fmt.Sprintf("Install %s for richer diagnostics, definitions,", serverDisplayName(lang)),
			"references, hover, and code review:",
			"",
			styleInlineCommand.Render("  "+installCommand(lang)),
			"",
			styleMeta.Render("Continuing with normal file reads for now."),
		)
	} else {
		rows = append(rows, "Some detected languages can use richer code intelligence:", "")
		for _, lang := range missing {
			rows = append(rows,
				fmt.Sprintf("%s  missing %s", lang.Name, strings.Join(lang.Command, " ")),
				styleInlineCommand.Render("  "+installCommand(lang)),
			)
		}
		rows = append(rows, "", styleMeta.Render("Continuing with normal file reads for now."))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colorWarning).
		Padding(0, 1)
	if hasThemeBackground {
		box = box.Background(themeBackground)
	}
	return box.Render(strings.Join(rows, "\n"))
}

func serverDisplayName(lang lsp.DetectedLanguage) string {
	switch lang.ID {
	case "go":
		return "gopls"
	case "typescript":
		return "TypeScript language server"
	case "python":
		return "pyright"
	case "rust":
		return "rust-analyzer"
	default:
		if len(lang.Command) > 0 {
			return lang.Command[0]
		}
		return "the language server"
	}
}

func installCommand(lang lsp.DetectedLanguage) string {
	switch lang.ID {
	case "go":
		return "go install golang.org/x/tools/gopls@latest"
	case "typescript":
		return "npm install -g typescript typescript-language-server"
	case "python":
		return "npm install -g pyright"
	case "rust":
		return "rustup component add rust-analyzer"
	default:
		return lang.InstallHint
	}
}
