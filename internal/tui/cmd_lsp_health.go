package tui

import (
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// renderLSPAdvisory returns a non-blocking startup card for supported languages
// whose semantic server is missing. It teaches the user how to unlock the
// richer LSP tools without forcing the model to discover setup state.
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
			fmt.Sprintf("%s: install %s to unlock it", lang.Name, serverDisplayName(lang)),
			styleInlineCommand.Render("  "+installCommand(lang)),
		)
	}
	rows = append(rows, "", styleMeta.Render("yottacode may offer to run a command with approval."), styleMeta.Render("Everything works without them; they unlock go-to-def, live"), styleMeta.Render("diagnostics, and symbol-aware review."), "")

	return renderLSPAdvisoryBox("LSP Code Intelligence", fmt.Sprintf("%d languages", len(missing)), rows)
}

// renderSingleLSPAdvisory mirrors the startup card shape used by approvals and
// plan prompts: title on the left frame, language on the right, and detection-first
// copy that frames the missing server as an unlockable opportunity rather than an
// error state. The template is language-agnostic — lang.Name and server fill the
// slots for every supported language.
func renderSingleLSPAdvisory(lang lsp.DetectedLanguage) string {
	server := serverDisplayName(lang)
	rows := []string{
		"",
		fmt.Sprintf("%s detected in this workspace — richer", lang.Name),
		fmt.Sprintf("code intelligence is available by installing %s.", server),
		"yottacode can ask to run the install command",
		"below through normal approval.",
		"",
		styleInlineCommand.Render("  " + installCommand(lang)),
		"",
		styleMeta.Render("The session works without it; approving unlocks"),
		styleMeta.Render("go-to-def, live diagnostics, and symbol-aware review."),
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

func lspSetupReminder(langs []lsp.DetectedLanguage) string {
	missing := make([]string, 0, len(langs))
	for _, lang := range langs {
		if lang.FilesAvailable <= 0 || lang.ServerAvailable {
			continue
		}
		cmd := strings.TrimSpace(installCommand(lang))
		if cmd == "" {
			continue
		}
		missing = append(missing, fmt.Sprintf("- %s: %s missing; install_command=%s", lang.Name, serverDisplayName(lang), cmd))
	}
	if len(missing) == 0 {
		return ""
	}
	return "[system reminder — not from the user] The startup LSP Code Intelligence advisory found missing local language servers:\n" + strings.Join(missing, "\n") + "\nIf the user's request above is actively working in one of these languages, or would benefit from go-to-definition, diagnostics, references, or symbol-aware review, offer to run the matching install_command through normal run_bash approval. Never auto-install, never imply the server was installed unless the approved command succeeds, and continue with ordinary file reads/grep if the user declines or the server is not relevant."
}

func installCommand(lang lsp.DetectedLanguage) string {
	if lang.InstallCommand != "" {
		return lang.InstallCommand
	}
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
