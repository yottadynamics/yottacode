package tui

import (
	"fmt"

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
	rows = append(rows, "", styleMeta.Render("Servers are local subprocesses and are never auto-installed."), styleMeta.Render("Everything works without them; they unlock deeper code intelligence."), "")

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
		"LSP servers are local subprocesses; yottacode",
		"never auto-installs or auto-enables them.",
		"",
		styleInlineCommand.Render("  " + installCommand(lang)),
		"",
		styleMeta.Render("Everything works without it; this just unlocks"),
		styleMeta.Render("deeper code intelligence."),
		"",
	}
	return renderLSPAdvisoryBox("LSP Code Intelligence", lang.Name, rows)
}

// renderLSPAdvisoryBox draws the startup card. Unlike the approval
// modal and plan-mode decision cards, there's no live terminal width
// available here — run.go builds this string into
// pendingStartupNotices before the Bubble Tea program starts, well
// before the first WindowSizeMsg. So instead of a live-width cap we
// wrap every body line at the shared labeledBoxCap ceiling: normal
// advisory copy is short and renders unchanged, but a pathological
// install hint or language count can no longer stretch the box wider
// than any real terminal.
func renderLSPAdvisoryBox(title, context string, bodyLines []string) string {
	leftLabel := " " + styleWarnIcon.Render(title) + " "
	rightLabel := " " + styleMeta.Render(context) + " "

	wrapped := make([]string, 0, len(bodyLines))
	for _, line := range bodyLines {
		for _, wrappedLine := range hardWrapLabeled(line, labeledBoxCap) {
			wrapped = append(wrapped, labeledBoxIndent+wrappedLine)
		}
	}

	return renderLabeledBox(leftLabel, rightLabel, wrapped, labeledBoxCap, colorWarning)
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
