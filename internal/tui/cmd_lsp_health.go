package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

	if len(missing) == 1 {
		return renderSingleLSPAdvisory(missing[0])
	}

	rows := []string{"", "Some detected languages can use richer code intelligence:", ""}
	for _, lang := range missing {
		rows = append(rows,
			fmt.Sprintf("%s not found: %s", serverDisplayName(lang), lang.Name),
			styleInlineCommand.Render("  "+installCommand(lang)),
		)
	}
	rows = append(rows, "", styleMeta.Render("Everything works without them; they unlock deeper code intelligence."), "")

	return renderLSPAdvisoryBox("LSP Code Intelligence", fmt.Sprintf("%d languages", len(missing)), rows)
}

// renderSingleLSPAdvisory mirrors the startup card shape used by approvals and
// plan prompts: title on the left frame, language on the right, and plain copy
// that says exactly what is missing while reassuring users the session continues.
func renderSingleLSPAdvisory(lang lsp.DetectedLanguage) string {
	server := serverDisplayName(lang)
	rows := []string{
		"",
		fmt.Sprintf("%s not found — running without go-to-def,", server),
		"live diagnostics, and symbol-aware review.",
		"",
		styleInlineCommand.Render("  " + installCommand(lang)),
		"",
		styleMeta.Render("Everything works without it; this just unlocks"),
		styleMeta.Render("deeper code intelligence."),
		"",
	}
	return renderLSPAdvisoryBox("LSP Code Intelligence", lang.Name, rows)
}

func renderLSPAdvisoryBox(title, context string, bodyLines []string) string {
	leftLabel := " " + styleWarnIcon.Render(title) + " "
	rightLabel := " " + styleMeta.Render(context) + " "

	innerW := 0
	for _, line := range bodyLines {
		if w := ansi.StringWidth(line); w > innerW {
			innerW = w
		}
	}
	headW := ansi.StringWidth(leftLabel) + ansi.StringWidth(rightLabel) + 2
	if headW > innerW {
		innerW = headW
	}

	border := lipgloss.NewStyle().Foreground(colorWarning)
	fill := innerW - ansi.StringWidth(leftLabel) - ansi.StringWidth(rightLabel)
	if fill < 1 {
		fill = 1
	}
	top := border.Render("┌─") + leftLabel +
		border.Render(strings.Repeat("─", fill)) +
		rightLabel + border.Render("─┐")

	sideL := border.Render("│ ")
	sideR := border.Render(" │")
	rows := []string{top}
	for _, line := range bodyLines {
		pad := innerW - ansi.StringWidth(line)
		if pad < 0 {
			pad = 0
		}
		rows = append(rows, sideL+line+strings.Repeat(" ", pad)+sideR)
	}
	rows = append(rows, border.Render("└"+strings.Repeat("─", innerW+2)+"┘"))
	return strings.Join(rows, "\n")
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
