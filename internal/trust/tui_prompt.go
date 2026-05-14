package trust

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// styleTrustSelector / styleTrustLabel keep the picker plain.
// Green caret on the selected row, no border, no footer hint —
// per the user's "nothing fancy" guidance.
var (
	styleTrustSelector  = lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true)
	styleTrustSelected  = lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true)
	styleTrustUnsel     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleTrustTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true)
	styleTrustRule      = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	styleTrustBody      = lipgloss.NewStyle()
	styleTrustHint      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// trustPickerModel is the tiny Bubbletea model that backs the
// first-launch workspace-trust dialog. Two options, Up/Down to
// move, Enter to accept, Esc or Ctrl+C to decline. No alt-screen
// so the prompt stays in the user's scrollback after dismissal.
type trustPickerModel struct {
	cwd      string
	index    int  // 0 = Yes, 1 = No
	answered bool // set true on Enter so View() can be suppressed post-quit
	result   PromptResult
}

// errTrustPromptAborted is the sentinel the runner uses to
// distinguish "user said No" from "tea exited abnormally". The
// runner unwraps it before returning to callers, so the public
// PromptInteractive surface only ever returns nil + PromptResult.
var errTrustPromptAborted = errors.New("trust prompt aborted")

func (m trustPickerModel) Init() tea.Cmd { return nil }

func (m trustPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.index > 0 {
				m.index--
			}
		case "down", "j":
			if m.index < 1 {
				m.index++
			}
		case "enter":
			m.answered = true
			if m.index == 0 {
				m.result = PromptYes
			} else {
				m.result = PromptNo
			}
			return m, tea.Quit
		case "ctrl+c", "esc", "q":
			m.answered = true
			m.result = PromptNo
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m trustPickerModel) View() string {
	if m.answered {
		// Hide the picker once the user has answered. Bubbletea
		// re-renders one last time before exiting; returning an
		// empty View leaves a clean shell prompt below.
		return ""
	}
	const indent = "  "
	var b strings.Builder
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, styleTrustRule.Render(strings.Repeat("─", 72)))
	fmt.Fprintln(&b, indent+styleTrustTitle.Render("Accessing workspace:"))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, indent+m.cwd)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, indent+styleTrustBody.Render("Quick safety check: is this a project you created or one you trust?"))
	fmt.Fprintln(&b, indent+styleTrustHint.Render("(Your own code, a well-known open-source project, or work from your team.)"))
	fmt.Fprintln(&b, indent+styleTrustHint.Render("If not, take a moment to review what's in this folder first."))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, indent+styleTrustBody.Render("yottacode will be able to read, edit, and execute files here."))
	fmt.Fprintln(&b)
	for i, label := range []string{"1. Yes, I trust this folder", "2. No, exit"} {
		if i == m.index {
			// Selector pokes out at column 0 so the label stays aligned
			// at the same indent as every other body line.
			fmt.Fprintln(&b, styleTrustSelector.Render("> ")+styleTrustSelected.Render(label))
		} else {
			fmt.Fprintln(&b, indent+styleTrustUnsel.Render(label))
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, styleTrustHint.Render("Enter to confirm · Esc to cancel"))
	return b.String()
}

// PromptInteractive opens the Bubbletea trust dialog and returns
// the user's choice. Caller is responsible for guarding with
// IsInteractiveStream — Bubbletea against a piped stdin/stdout
// produces garbled output. On Ctrl+C / Esc the result is
// PromptNo (mirrors the text Prompt).
func PromptInteractive(cwd string) (PromptResult, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return PromptNo, err
	}
	prog := tea.NewProgram(trustPickerModel{cwd: abs})
	final, err := prog.Run()
	if err != nil {
		return PromptNo, err
	}
	m, ok := final.(trustPickerModel)
	if !ok {
		return PromptNo, errTrustPromptAborted
	}
	return m.result, nil
}
