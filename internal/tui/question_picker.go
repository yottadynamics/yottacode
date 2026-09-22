package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

const questionFreeTextMaxLen = 500

type questionAnswerState struct {
	selected      map[int]bool
	cursor        int
	usingFreeText bool
	freeText      string
	editingOther  bool
	freeTextBuf   string
}

type questionPickerState struct {
	questions []agent.Question
	answers   []questionAnswerState
	activeTab int
}

func newQuestionPickerState(questions []agent.Question) questionPickerState {
	p := questionPickerState{questions: questions, answers: make([]questionAnswerState, len(questions))}
	for i, q := range questions {
		p.answers[i].selected = map[int]bool{}
		if idx, ok := q.RecommendedIndex(); ok {
			p.answers[i].selected[idx] = true
			p.answers[i].cursor = idx
		}
	}
	return p
}

func (p *questionPickerState) onSubmitTab() bool { return p.activeTab == len(p.questions) }
func (p *questionPickerState) answered(i int) bool {
	a := p.answers[i]
	return a.usingFreeText || len(a.selected) > 0
}
func (p *questionPickerState) allAnswered() bool {
	for i := range p.questions {
		if !p.answered(i) {
			return false
		}
	}
	return true
}
func (p *questionPickerState) nextTab() {
	if p.activeTab < len(p.questions) {
		p.activeTab++
	}
}
func (p *questionPickerState) prevTab() {
	if p.activeTab > 0 {
		p.activeTab--
	}
}
func (p *questionPickerState) otherRowIndex() int { return len(p.questions[p.activeTab].Options) }
func (p *questionPickerState) moveCursor(delta int) {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	a.cursor += delta
	if a.cursor < 0 {
		a.cursor = 0
	}
	if max := p.otherRowIndex(); a.cursor > max {
		a.cursor = max
	}
}
func (p *questionPickerState) onOtherRow() bool {
	return !p.onSubmitTab() && p.answers[p.activeTab].cursor == p.otherRowIndex()
}
func (p *questionPickerState) isEditingOther() bool {
	return !p.onSubmitTab() && p.answers[p.activeTab].editingOther
}

func (p *questionPickerState) selectCurrent() {
	if p.onSubmitTab() || p.onOtherRow() {
		return
	}
	q := p.questions[p.activeTab]
	a := &p.answers[p.activeTab]
	if q.MultiSelect {
		a.usingFreeText = false
		if a.selected[a.cursor] {
			delete(a.selected, a.cursor)
		} else {
			a.selected[a.cursor] = true
		}
		return
	}
	a.selected = map[int]bool{a.cursor: true}
	a.usingFreeText = false
}

func (p *questionPickerState) startEditingOther() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	a.editingOther = true
	a.freeTextBuf = a.freeText
}
func (p *questionPickerState) appendToOther(s string) {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	remaining := questionFreeTextMaxLen - len([]rune(a.freeTextBuf))
	if remaining <= 0 {
		return
	}
	r := []rune(s)
	if len(r) > remaining {
		r = r[:remaining]
	}
	a.freeTextBuf += string(r)
}
func (p *questionPickerState) backspaceOther() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	if a.freeTextBuf == "" {
		return
	}
	r := []rune(a.freeTextBuf)
	a.freeTextBuf = string(r[:len(r)-1])
}

func (p *questionPickerState) commitOther() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	text := strings.TrimSpace(a.freeTextBuf)
	a.editingOther = false
	if text == "" {
		return
	}
	a.freeText = text
	a.usingFreeText = true
	if !p.questions[p.activeTab].MultiSelect {
		a.selected = map[int]bool{}
	}
}
func (p *questionPickerState) cancelEditingOther() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	a.editingOther = false
	a.freeTextBuf = ""
}

func (p *questionPickerState) buildAnswer() *agent.QuestionAnswer {
	reply := &agent.QuestionAnswer{Selections: make([]agent.QuestionSelection, len(p.questions))}
	for i, q := range p.questions {
		a := p.answers[i]
		sel := agent.QuestionSelection{Header: q.Header}
		labels := make([]string, 0, len(a.selected))
		for j, opt := range q.Options {
			if a.selected[j] {
				labels = append(labels, opt.Label)
			}
		}
		sel.Labels = labels
		if a.usingFreeText {
			sel.FreeText = a.freeText
		}
		reply.Selections[i] = sel
	}
	return reply
}

func renderQuestionPicker(p *questionPickerState, width int) string {
	title, desc := p.titleAndDescription()
	contentWidth := width - 8
	if contentWidth < 20 {
		contentWidth = 20
	}
	desc = truncateDisplay(desc, contentWidth)
	var b strings.Builder
	b.WriteString(renderMenuHeader(title, desc))
	if len(p.questions) > 1 {
		b.WriteString(renderQuestionTabStrip(p))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if p.onSubmitTab() {
		b.WriteString(renderSubmitTab(p))
	} else {
		b.WriteString(renderQuestionOptions(p, contentWidth))
	}
	b.WriteString("\n")
	b.WriteString(styleMeta.Render(p.footerHint()))
	return b.String()
}
func (p *questionPickerState) titleAndDescription() (string, string) {
	if p.onSubmitTab() {
		if p.allAnswered() {
			return "Submit", fmt.Sprintf("All %d question(s) answered. Review with ←/→, or submit below.", len(p.questions))
		}
		return "Submit", "Answer every question to enable submit."
	}
	q := p.questions[p.activeTab]
	return q.Header, q.Question
}
func renderQuestionTabStrip(p *questionPickerState) string {
	activeStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	doneStyle := lipgloss.NewStyle().Foreground(colorSuccess)
	disabledStyle := lipgloss.NewStyle().Foreground(colorMuted).Faint(true)
	tabs := make([]string, 0, len(p.questions)+1)
	for i, q := range p.questions {
		prefix := ""
		if p.answered(i) {
			prefix = "✓ "
		}
		switch {
		case i == p.activeTab:
			tabs = append(tabs, activeStyle.Render("[ "+prefix+q.Header+" ]"))
		case p.answered(i):
			tabs = append(tabs, doneStyle.Render(prefix+q.Header))
		default:
			tabs = append(tabs, mutedStyle.Render(q.Header))
		}
	}
	switch {
	case p.onSubmitTab():
		tabs = append(tabs, activeStyle.Render("[ Submit ]"))
	case p.allAnswered():
		tabs = append(tabs, mutedStyle.Render("Submit"))
	default:
		tabs = append(tabs, disabledStyle.Render("Submit"))
	}
	return "  " + strings.Join(tabs, "   ")
}
func renderQuestionOptions(p *questionPickerState, width int) string {
	q := p.questions[p.activeTab]
	a := p.answers[p.activeTab]
	var b strings.Builder
	labelWidth := 22
	if width < 60 {
		labelWidth = 16
	}
	for i, opt := range q.Options {
		b.WriteString(renderMenuItem(menuItemOpts{Label: truncateDisplay(opt.Label, labelWidth), LabelWidth: labelWidth, Desc: truncateDisplay(opt.Description, width-labelWidth-8), Cursor: a.cursor == i, Checked: a.selected[i]}))
		b.WriteString("\n")
	}
	label := "Other…"
	if a.editingOther {
		label = "Other: " + truncateDisplay(a.freeTextBuf, width-10) + "█"
	} else if a.usingFreeText {
		label = "Other: " + truncateDisplay(a.freeText, width-10)
	}
	b.WriteString(renderMenuItem(menuItemOpts{Label: label, Cursor: a.cursor == p.otherRowIndex(), Checked: a.usingFreeText}))
	return b.String()
}
func renderSubmitTab(p *questionPickerState) string {
	if !p.allAnswered() {
		return styleEmpty.Render("  answer every question to enable submit")
	}
	return renderMenuItem(menuItemOpts{Label: "Submit answers", Cursor: true})
}
func (p *questionPickerState) summaryLine() string {
	reply := p.buildAnswer()
	parts := make([]string, 0, len(reply.Selections))
	for _, sel := range reply.Selections {
		vals := strings.Join(sel.Labels, ", ")
		if sel.FreeText != "" {
			if vals != "" {
				vals += ", "
			}
			vals += sel.FreeText
		}
		parts = append(parts, sel.Header+": "+vals)
	}
	return "✓ " + strings.Join(parts, " · ")
}

func (m Model) updateQuestionPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := &m.questionPicker
	if p.isEditingOther() {
		switch {
		case msg.Code == tea.KeyEsc:
			p.cancelEditingOther()
		case msg.Code == tea.KeyEnter:
			p.commitOther()
		case msg.Code == tea.KeyBackspace:
			p.backspaceOther()
		case msg.Text != "":
			p.appendToOther(msg.Text)
		}
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		if m.questionReq.Reply != nil {
			m.questionReq.Reply.Cancelled = true
		}
		if !m.sendDecision(agent.Deny) {
			return m, nil
		}
		m.appendLine(styleError.Render("✗ question cancelled"))
		return m, m.finishDecisionUI()
	case tea.KeyLeft:
		p.prevTab()
	case tea.KeyRight:
		p.nextTab()
	case tea.KeyTab:
		if msg.Mod == tea.ModShift {
			p.prevTab()
		} else {
			p.nextTab()
		}
	case tea.KeyUp:
		p.moveCursor(-1)
	case tea.KeyDown:
		p.moveCursor(1)
	case tea.KeySpace:
		if p.onOtherRow() {
			p.startEditingOther()
		} else {
			p.selectCurrent()
		}
	case tea.KeyEnter:
		switch {
		case p.onSubmitTab():
			if !p.allAnswered() {
				return m, nil
			}
			summary := p.summaryLine()
			if m.questionReq.Reply != nil {
				*m.questionReq.Reply = *p.buildAnswer()
			}
			if !m.sendDecision(agent.AllowOnce) {
				return m, nil
			}
			m.appendLine(styleAuto.Render(summary))
			return m, m.finishDecisionUI()
		case p.onOtherRow():
			p.startEditingOther()
		default:
			p.selectCurrent()
		}
	}
	return m, nil
}
func updateQuestionPickerPaste(m Model, msg tea.PasteMsg) (Model, tea.Cmd) {
	if m.questionPicker.isEditingOther() {
		content := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(msg.Content)
		m.questionPicker.appendToOther(content)
	}
	return m, nil
}
func (p *questionPickerState) footerHint() string {
	if p.onSubmitTab() {
		nav := fmt.Sprintf("←/→ review a question (%d)", len(p.questions))
		if p.allAnswered() {
			return "  " + nav + " · enter submit · esc cancel all"
		}
		return "  " + nav + " · esc cancel all"
	}
	if p.isEditingOther() {
		return "  type your answer · enter confirm · esc cancel edit"
	}
	action := "enter select"
	if p.questions[p.activeTab].MultiSelect {
		action = "space toggle · enter confirm"
	}
	nav := ""
	if len(p.questions) > 1 {
		nav = fmt.Sprintf("←/→ switch question (%d/%d) · ", p.activeTab+1, len(p.questions))
	}
	return "  " + nav + "↑/↓ move · " + action + " · esc cancel all"
}
