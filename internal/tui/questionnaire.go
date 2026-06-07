package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// questionnaire.go renders the ask_user_question tool's tabbed
// questionnaire: one tab per question (picker-style tab strip, same
// visual language as the /model provider strip), menu rows for the
// options, and a synthetic free-text "Other" row backed by a
// textinput. Lifecycle is approval-like — the agent goroutine is
// blocked inside the tool's Execute on Model.answersCh while the
// questionnaire is up — but the presentation is deliberately
// picker-like, not a bordered modal card.

// questionnaireState owns one in-flight ask_user_question round-trip.
// Created by handleAgentEvent on UserQuestionsNeeded, dropped on
// submit/decline (and defensively on turnEndedMsg).
type questionnaireState struct {
	questions []agent.UserQuestion
	// tab is the active question index (like providerIdx in the model
	// picker's tab strip).
	tab int
	// cursor is the per-question highlighted row. Rows 0..len(options)-1
	// are the model's options; row len(options) is the synthetic Other.
	cursor []int
	// chosen is the per-question committed option index for
	// single-select questions (-1 = none yet); rendered with the ✔
	// marker so revisiting a tab shows what was picked.
	chosen []int
	// selected holds the per-question toggle set for multiSelect
	// questions.
	selected []map[int]bool
	// otherText is the committed free-text answer per question
	// ("" = none).
	otherText []string
	// answered marks questions with a committed answer; all true =
	// Enter on the last one submits.
	answered []bool
	// otherFocus routes keystrokes to otherInput while the user types
	// a free-text answer.
	otherFocus bool
	otherInput textinput.Model
}

func newQuestionnaireState(qs []agent.UserQuestion) *questionnaireState {
	in := textinput.New()
	in.Placeholder = "type your answer"
	in.Prompt = ""
	in.CharLimit = 300
	st := &questionnaireState{
		questions: qs,
		cursor:    make([]int, len(qs)),
		chosen:    make([]int, len(qs)),
		selected:  make([]map[int]bool, len(qs)),
		otherText: make([]string, len(qs)),
		answered:  make([]bool, len(qs)),
		otherInput: in,
	}
	for i := range qs {
		st.chosen[i] = -1
		st.selected[i] = map[int]bool{}
	}
	return st
}

// otherRow is the index of the synthetic Other row for question i.
func (q *questionnaireState) otherRow(i int) int { return len(q.questions[i].Options) }

// nextUnanswered returns the first unanswered question scanning
// forward from (after) tab, wrapping; -1 when all are answered.
func (q *questionnaireState) nextUnanswered(after int) int {
	n := len(q.questions)
	for d := 1; d <= n; d++ {
		i := (after + d) % n
		if !q.answered[i] {
			return i
		}
	}
	return -1
}

// buildQuestionnaireAnswers assembles the reply payload in question
// order. Free-text Other answers join a multi-select's toggled labels;
// a lone Other answer is flagged IsOther so the tool result reads
// "A (other): …".
func buildQuestionnaireAnswers(q *questionnaireState) agent.QuestionnaireAnswers {
	answers := make([]agent.QuestionAnswer, len(q.questions))
	for i, uq := range q.questions {
		a := agent.QuestionAnswer{Question: uq.Question, Header: uq.Header}
		var labels []string
		if uq.MultiSelect {
			for j, opt := range uq.Options {
				if q.selected[i][j] {
					labels = append(labels, opt.Label)
				}
			}
		} else if q.chosen[i] >= 0 && q.chosen[i] < len(uq.Options) {
			labels = append(labels, uq.Options[q.chosen[i]].Label)
		}
		if q.otherText[i] != "" {
			if len(labels) == 0 {
				a.IsOther = true
			}
			labels = append(labels, q.otherText[i])
		}
		a.Selected = labels
		answers[i] = a
	}
	return agent.QuestionnaireAnswers{Answers: answers}
}

// updateQuestionnaire handles keystrokes while the questionnaire is
// foreground. Routed from handleKeyPress ahead of the mid-turn
// textarea/Ctrl+C blocks, so Esc here never cancels the turn — it
// declines the questions and the turn continues with the tool result.
func (m Model) updateQuestionnaire(msg tea.KeyMsg) (Model, tea.Cmd) {
	q := m.questionnaire
	if q == nil {
		return m, nil
	}

	// Free-text mode: the Other textinput owns every key except the
	// commit/back/abort controls.
	if q.otherFocus {
		switch msg.Type {
		case tea.KeyEnter:
			text := strings.TrimSpace(q.otherInput.Value())
			q.otherFocus = false
			q.otherInput.Blur()
			if text == "" {
				return m, nil // nothing typed — back to the list, not an answer
			}
			q.otherText[q.tab] = text
			if !q.questions[q.tab].MultiSelect {
				q.chosen[q.tab] = -1 // free text replaces a single-select pick
			}
			return m.commitQuestionnaireAnswer()
		case tea.KeyEsc:
			q.otherFocus = false
			q.otherInput.Blur()
			return m, nil
		case tea.KeyCtrlC:
			return m.declineQuestionnaire()
		default:
			var cmd tea.Cmd
			q.otherInput, cmd = q.otherInput.Update(msg)
			return m, cmd
		}
	}

	uq := q.questions[q.tab]
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		// Decline the whole set. Deliberately NOT a turn-cancel: the
		// agent goroutine is blocked on answersCh, and a decline
		// unblocks it with a "proceed on your judgment" result. The
		// user can Esc/Ctrl+C again afterward to kill the turn itself.
		return m.declineQuestionnaire()
	case tea.KeyUp:
		if q.cursor[q.tab] > 0 {
			q.cursor[q.tab]--
		}
		return m, nil
	case tea.KeyDown:
		if q.cursor[q.tab] < q.otherRow(q.tab) {
			q.cursor[q.tab]++
		}
		return m, nil
	case tea.KeyHome:
		q.cursor[q.tab] = 0
		return m, nil
	case tea.KeyEnd:
		q.cursor[q.tab] = q.otherRow(q.tab)
		return m, nil
	case tea.KeyLeft, tea.KeyShiftTab:
		q.tab = (q.tab - 1 + len(q.questions)) % len(q.questions)
		return m, nil
	case tea.KeyRight, tea.KeyTab:
		q.tab = (q.tab + 1) % len(q.questions)
		return m, nil
	case tea.KeySpace:
		if uq.MultiSelect && q.cursor[q.tab] < q.otherRow(q.tab) {
			j := q.cursor[q.tab]
			q.selected[q.tab][j] = !q.selected[q.tab][j]
		}
		return m, nil
	case tea.KeyEnter:
		return m.selectQuestionnaireRow(q.cursor[q.tab])
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			r := msg.Runes[0]
			if r >= '1' && r <= '9' {
				row := int(r - '1')
				if row <= q.otherRow(q.tab) {
					q.cursor[q.tab] = row
					return m.selectQuestionnaireRow(row)
				}
			}
		}
		return m, nil
	}
	return m, nil
}

// selectQuestionnaireRow acts on the given row of the active question:
// Other opens the free-text input; a multi-select option toggles (the
// answer commits via Enter on a toggled set); a single-select option
// commits immediately and advances.
func (m Model) selectQuestionnaireRow(row int) (Model, tea.Cmd) {
	q := m.questionnaire
	uq := q.questions[q.tab]
	if row == q.otherRow(q.tab) {
		q.otherInput.SetValue(q.otherText[q.tab])
		q.otherInput.CursorEnd()
		q.otherInput.Focus()
		q.otherFocus = true
		return m, textinput.Blink
	}
	if uq.MultiSelect {
		// Enter on an option row with nothing toggled yet means "pick
		// this one": toggle it on and commit. With an existing toggle
		// set, Enter confirms the set as-is (Space is the toggler).
		if len(activeToggles(q, q.tab)) == 0 {
			q.selected[q.tab][row] = true
		}
		if len(activeToggles(q, q.tab)) == 0 && q.otherText[q.tab] == "" {
			return m, nil // nothing to commit
		}
		return m.commitQuestionnaireAnswer()
	}
	q.chosen[q.tab] = row
	q.otherText[q.tab] = "" // an option pick replaces an earlier free text
	return m.commitQuestionnaireAnswer()
}

// activeToggles returns the toggled option indices for question i.
func activeToggles(q *questionnaireState, i int) []int {
	var out []int
	for j, on := range q.selected[i] {
		if on {
			out = append(out, j)
		}
	}
	return out
}

// commitQuestionnaireAnswer marks the active question answered, then
// auto-advances to the next unanswered question — or submits the whole
// set when none remain (Claude Code behavior: answering the last
// question IS the submit).
func (m Model) commitQuestionnaireAnswer() (Model, tea.Cmd) {
	q := m.questionnaire
	q.answered[q.tab] = true
	if next := q.nextUnanswered(q.tab); next >= 0 {
		q.tab = next
		return m, nil
	}
	return m.submitQuestionnaire(buildQuestionnaireAnswers(q))
}

// declineQuestionnaire dismisses the whole set: the tool returns the
// "user declined" result and the turn continues.
func (m Model) declineQuestionnaire() (Model, tea.Cmd) {
	return m.submitQuestionnaire(agent.QuestionnaireAnswers{Declined: true})
}

// submitQuestionnaire delivers the reply to the blocked tool Execute
// and resumes the event loop. Non-blocking send: answersCh is buffered
// (cap 1) and the tool is the only receiver, so a full buffer can only
// mean the turn already died — drop rather than wedge the UI.
func (m Model) submitQuestionnaire(ans agent.QuestionnaireAnswers) (Model, tea.Cmd) {
	select {
	case m.answersCh <- ans:
	default:
	}
	m.questionnaire = nil
	return m, waitForEvent(m.eventsCh, m.turnErrCh)
}

// renderQuestionnaireTabs draws the question switcher in the provider
// tab strip's visual language: active question bracketed accent-bold,
// inactive muted, answered questions carry the ✔ marker, with the
// ←/→ hint inline. Single-question calls skip the strip entirely.
func renderQuestionnaireTabs(q *questionnaireState) string {
	if len(q.questions) <= 1 {
		return ""
	}
	activeStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	tabs := make([]string, 0, len(q.questions))
	for i, uq := range q.questions {
		if i == q.tab {
			tabs = append(tabs, activeStyle.Render("[ "+uq.Header+" ]")+tabSuffix(q, i))
		} else {
			tabs = append(tabs, mutedStyle.Render(uq.Header)+tabSuffix(q, i))
		}
	}
	hint := mutedStyle.Render(fmt.Sprintf("←/→ switch (%d/%d)", q.tab+1, len(q.questions)))
	return "  " + strings.Join(tabs, "   ") + "    " + hint
}

// tabSuffix appends the answered ✔ outside the bracket/label styling
// so the marker keeps its success color on both active and inactive
// tabs.
func tabSuffix(q *questionnaireState, i int) string {
	if !q.answered[i] {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colorSuccess).Render(" ✔")
}

// renderQuestionnaire draws the questionnaire body for the live
// footer: header, tab strip, the active question's text, its option
// rows (+ Other), and the key-hint footer. Inline-overlay shape — no
// outer border, flush-left with the 2-space picker indent.
func renderQuestionnaire(q *questionnaireState, width int) string {
	var b strings.Builder
	answeredCount := 0
	for _, a := range q.answered {
		if a {
			answeredCount++
		}
	}
	desc := "the model needs your input to continue"
	if len(q.questions) > 1 {
		desc = fmt.Sprintf("the model needs your input to continue · %d/%d answered", answeredCount, len(q.questions))
	}
	b.WriteString(renderMenuHeader("Questions", desc))
	if tabs := renderQuestionnaireTabs(q); tabs != "" {
		b.WriteString(tabs)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	uq := q.questions[q.tab]
	question := uq.Question
	if uq.MultiSelect {
		question += stylePaletteEmpty.Render("  (multi-select)")
	}
	b.WriteString("  " + stylePaletteItem.Render(question))
	b.WriteString("\n\n")

	// Option labels pad to a shared column so descriptions align,
	// mirroring the skills picker's row layout.
	maxLabel := 5 // "Other"
	for _, opt := range uq.Options {
		if l := runeCount(opt.Label); l > maxLabel {
			maxLabel = l
		}
	}
	if maxLabel > 32 {
		maxLabel = 32
	}

	for j, opt := range uq.Options {
		label := truncateRunes(opt.Label, maxLabel)
		label += strings.Repeat(" ", maxLabel-runeCount(label))
		if uq.MultiSelect {
			mark := "[ ]"
			if q.selected[q.tab][j] {
				mark = "[x]"
			}
			label = mark + " " + label
		}
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:   label,
			Desc:    truncateForRender(opt.Description, 80),
			Cursor:  j == q.cursor[q.tab],
			Checked: !uq.MultiSelect && q.chosen[q.tab] == j,
		}))
		b.WriteString("\n")
	}

	// Synthetic Other row. While focused it renders the live textinput
	// in the description slot; once committed, the typed answer shows
	// there (dimmed) so revisiting the tab displays the saved text.
	otherLabel := truncateRunes("Other", maxLabel)
	otherLabel += strings.Repeat(" ", maxLabel-runeCount(otherLabel))
	if uq.MultiSelect {
		otherLabel = "    " + otherLabel
	}
	onOther := q.cursor[q.tab] == q.otherRow(q.tab)
	switch {
	case q.otherFocus && onOther:
		cursorArrow := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("❯ ")
		b.WriteString(cursorArrow + stylePaletteSelected.Render(otherLabel) + "   " + q.otherInput.View())
		b.WriteString("\n")
	default:
		otherDesc := "type your own answer"
		if q.otherText[q.tab] != "" {
			otherDesc = q.otherText[q.tab]
		}
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:   otherLabel,
			Desc:    truncateForRender(otherDesc, 80),
			Cursor:  onOther,
			Checked: q.otherText[q.tab] != "",
		}))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	var footer string
	switch {
	case q.otherFocus:
		footer = "type your answer · ↵ confirm · esc back"
	case uq.MultiSelect:
		footer = "↑↓ move · space toggle · ↵ confirm · ←/→ question · esc skip all"
	default:
		footer = "↑↓ move · ↵ select · 1-9 jump · ←/→ question · esc skip all"
	}
	b.WriteString(styleFooter.Render(footer))
	_ = width // reserved for future per-row truncation, matching renderModelPicker
	return strings.TrimRight(b.String(), "\n")
}
