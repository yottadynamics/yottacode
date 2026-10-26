package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

const questionFreeTextMaxLen = 500

// questionSummaryValueMax bounds one answer in the post-submit transcript line.
const questionSummaryValueMax = 60

// questionAnswerVisibleLines keeps the modal height fixed while still showing
// enough of a long answer or note to confirm that input was captured.
const questionAnswerVisibleLines = 4

type questionAnswerState struct {
	selected      map[int]bool
	cursor        int
	usingFreeText bool
	freeText      string
	editingOther  bool
	freeTextBuf   string
	skipped       bool
	note          string
	editingNote   bool
	noteBuf       string
	// visited is set once the user has looked at the question. A
	// pre-selected recommendation is "answered" for submission but only
	// shows ✓ in the tab strip after the user has seen it.
	visited bool
}

type questionPickerState struct {
	questions []agent.Question
	answers   []questionAnswerState
	activeTab int
	// escArmed: the first Esc only arms cancellation; a second one cancels
	// every question, so a stray keypress can't discard all answers.
	escArmed bool
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
	if len(p.answers) > 0 {
		p.answers[0].visited = true
	}
	return p
}

func (p *questionPickerState) onSubmitTab() bool { return p.activeTab == len(p.questions) }
func (p *questionPickerState) answered(i int) bool {
	a := p.answers[i]
	return a.skipped || a.usingFreeText || len(a.selected) > 0
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
		p.markVisited()
	}
}
func (p *questionPickerState) prevTab() {
	if p.activeTab > 0 {
		p.activeTab--
		p.markVisited()
	}
}
func (p *questionPickerState) markVisited() {
	if p.activeTab < len(p.answers) {
		p.answers[p.activeTab].visited = true
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

// isEditing reports whether a text field (Other or note) owns the keyboard.
func (p *questionPickerState) isEditing() bool {
	return !p.onSubmitTab() && (p.answers[p.activeTab].editingOther || p.answers[p.activeTab].editingNote)
}

// editBuf returns the buffer of the text field currently being edited.
func (a *questionAnswerState) editBuf() *string {
	if a.editingNote {
		return &a.noteBuf
	}
	return &a.freeTextBuf
}

// skipCurrent declines the active question: any selection is dropped.
func (p *questionPickerState) skipCurrent() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	a.selected = map[int]bool{}
	a.usingFreeText = false
	a.skipped = true
}

func (p *questionPickerState) startEditingNote() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	a.editingNote = true
	a.noteBuf = a.note
}

func (p *questionPickerState) selectCurrent() {
	if p.onSubmitTab() || p.onOtherRow() {
		return
	}
	q := p.questions[p.activeTab]
	a := &p.answers[p.activeTab]
	a.skipped = false
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

// quickPick moves the cursor to option idx and selects (or, for multi-select,
// toggles) it. Out-of-range digits are ignored.
func (p *questionPickerState) quickPick(idx int) bool {
	if p.onSubmitTab() || idx < 0 || idx >= len(p.questions[p.activeTab].Options) {
		return false
	}
	p.answers[p.activeTab].cursor = idx
	p.selectCurrent()
	return true
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
	buf := a.editBuf()
	remaining := questionFreeTextMaxLen - len([]rune(*buf))
	if remaining <= 0 {
		return
	}
	r := []rune(s)
	if len(r) > remaining {
		r = r[:remaining]
	}
	*buf += string(r)
}
func (p *questionPickerState) backspaceOther() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	buf := a.editBuf()
	if *buf == "" {
		return
	}
	r := []rune(*buf)
	*buf = string(r[:len(r)-1])
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
	a.skipped = false
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
	a.editingNote = false
	a.noteBuf = ""
}

// commitNote stores the note; an empty buffer clears an existing one.
func (p *questionPickerState) commitNote() {
	if p.onSubmitTab() {
		return
	}
	a := &p.answers[p.activeTab]
	a.note = strings.TrimSpace(a.noteBuf)
	a.editingNote = false
	a.noteBuf = ""
}

func (p *questionPickerState) buildAnswer() *agent.QuestionAnswer {
	reply := &agent.QuestionAnswer{Selections: make([]agent.QuestionSelection, len(p.questions))}
	for i, q := range p.questions {
		a := p.answers[i]
		sel := agent.QuestionSelection{Header: q.Header, Note: a.note, Skipped: a.skipped}
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

// renderQuestionPicker keeps one stable questionnaire frame for the entire call.
// It reserves the maximum question-text height across tabs so changing tabs
// never moves or resizes the popup.
func renderQuestionPicker(p *questionPickerState, width int) string {
	contentWidth := width - 8
	if contentWidth < 20 {
		contentWidth = 20
	}
	maxQuestionLines := 1
	for tab := 0; tab <= len(p.questions); tab++ {
		probe := *p
		probe.activeTab = tab
		_, d := probe.titleAndDescription()
		maxQuestionLines = max(maxQuestionLines, lineCount(wrapQuestionText(strings.TrimSpace(d), contentWidth)))
	}
	// Body and footer heights are measured across every tab (Review
	// included) and padded to the maximum, so the popup never resizes while
	// you move between tabs.
	maxBody, maxFoot := 0, 0
	for tab := 0; tab <= len(p.questions); tab++ {
		probe := *p
		probe.activeTab = tab
		maxBody = max(maxBody, lineCount(probe.renderBody(contentWidth)))
		maxFoot = max(maxFoot, lineCount(wrapQuestionText(probe.footerHintAt(), contentWidth)))
		probe.escArmed = true
		maxFoot = max(maxFoot, lineCount(wrapQuestionText(probe.footerHintAt(), contentWidth)))
	}
	title := fmt.Sprintf("Questions · %d/%d", min(p.activeTab+1, len(p.questions)), len(p.questions))
	if p.onSubmitTab() {
		title = "Questions · review"
	}
	_, desc := p.titleAndDescription()
	descLines := strings.Split(wrapQuestionText(strings.TrimSpace(desc), contentWidth), "\n")
	for len(descLines) < maxQuestionLines {
		descLines = append(descLines, "\u200b")
	}
	var b strings.Builder
	b.WriteString(renderMenuHeader(title, strings.Join(descLines, "\n"), contentWidth))
	if len(p.questions) > 1 {
		b.WriteString(renderQuestionTabStrip(p, contentWidth))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	body := p.renderBody(contentWidth)
	b.WriteString(body + strings.Repeat("\n", maxBody-lineCount(body)))
	b.WriteString("\n")
	foot := wrapQuestionText(p.footerHint(), contentWidth)
	b.WriteString(styleMeta.Render(foot + strings.Repeat("\n", maxFoot-lineCount(foot))))
	return b.String()
}

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

// wrapQuestionText wraps at words and hard-wraps unbroken input so generated
// URLs, paths, identifiers, and wide Unicode strings cannot widen the popup.
func wrapQuestionText(s string, width int) string {
	if width < 1 {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(wrapPlain(s, width), "\n") {
		for ansi.StringWidth(line) > width {
			runes := []rune(line)
			used, cut := 0, 0
			for cut < len(runes) {
				w := ansi.StringWidth(string(runes[cut]))
				if used+w > width {
					break
				}
				used += w
				cut++
			}
			if cut == 0 {
				cut = 1
			}
			lines = append(lines, string(runes[:cut]))
			line = string(runes[cut:])
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (p *questionPickerState) renderBody(width int) string {
	if p.onSubmitTab() {
		return renderSubmitTab(p, width)
	}
	return renderQuestionOptions(p, width)
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

// renderQuestionTabStrip uses progress-oriented labels rather than settings-style
// selected buttons, keeping the tabs as secondary navigation for the questions.
func renderQuestionTabStrip(p *questionPickerState, width int) string {
	activeStyle := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	disabledStyle := lipgloss.NewStyle().Foreground(colorMuted).Faint(true)
	type tab struct {
		text  string
		style lipgloss.Style
	}
	tabs := make([]tab, 0, len(p.questions)+1)
	for i, q := range p.questions {
		mark := "○"
		if p.answered(i) && (p.answers[i].visited || i == p.activeTab) {
			mark = "✓"
		}
		// Only the active tab is green; ✓/○ carries answered state.
		st := mutedStyle
		if i == p.activeTab {
			st = activeStyle
		}
		tabs = append(tabs, tab{mark + " " + q.Header, st})
	}
	review := tab{"→ Review", disabledStyle}
	switch {
	case p.onSubmitTab():
		review.style = activeStyle
	case p.allAnswered():
		review.style = mutedStyle
	}
	tabs = append(tabs, review)

	// Four 12-char headers don't fit a narrow popup: tighten the separator,
	// then shorten each label to its share of what is left.
	seps := []string{"  ·  ", " · ", " "}
	sep := seps[0]
	total := func(sep string) int {
		n := len(sep) * (len(tabs) - 1)
		for _, t := range tabs {
			n += ansi.StringWidth(t.text)
		}
		return n
	}
	for _, sp := range seps {
		sep = sp
		if total(sp) <= width {
			break
		}
	}
	if total(sep) > width {
		last := len(tabs) - 1 // Review keeps its full label
		avail := width - len(sep)*last - ansi.StringWidth(tabs[last].text)
		each := max(avail/last, 3)
		for i := 0; i < last; i++ {
			tabs[i].text = truncateDisplay(tabs[i].text, each)
		}
	}
	parts := make([]string, len(tabs))
	for i, t := range tabs {
		parts[i] = t.style.Render(t.text)
	}
	return strings.Join(parts, sep)
}

// questionOptionRows counts the rendered lines for one question's option list
// (label row + optional description row per option, plus the free-text row).
func questionOptionRows(q agent.Question, descWidth int) int {
	// Reserve the fixed answer/note display budget so editing cannot resize the frame.
	rows := 2 + (questionAnswerVisibleLines-1)*2 // free-text row + note row
	for _, opt := range q.Options {
		rows++
		if strings.TrimSpace(opt.Description) != "" {
			rows += strings.Count(wrapQuestionText(strings.TrimSpace(opt.Description), descWidth), "\n") + 1
		}
	}
	return rows
}

// questionAnswerLines wraps an answer or note while preserving its prefix.
// The visible portion is capped so the modal frame remains stable while editing.
func questionAnswerLines(prefix, value string, width int) []string {
	budget := max(width-ansi.StringWidth(prefix), 1)
	wrapped := strings.Split(wrapQuestionText(value, budget), "\n")
	if len(wrapped) > questionAnswerVisibleLines {
		wrapped = wrapped[:questionAnswerVisibleLines]
		wrapped[len(wrapped)-1] = truncateDisplay(wrapped[len(wrapped)-1], max(budget-1, 1)) + "…"
	}
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		indent := prefix
		if i > 0 {
			indent = strings.Repeat(" ", ansi.StringWidth(prefix))
		}
		lines = append(lines, indent+line)
	}
	return lines
}

const questionDescIndent = 7

func renderQuestionOptions(p *questionPickerState, width int) string {
	q := p.questions[p.activeTab]
	a := p.answers[p.activeTab]
	recIdx, hasRec := q.RecommendedIndex()
	descWidth := max(width-questionDescIndent, 20)
	on, off := "◉", "○"
	if q.MultiSelect {
		on, off = "☑", "☐"
	}
	cursorStyle := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	recStyle := lipgloss.NewStyle().Foreground(colorWarning)
	var lines []string
	for i, opt := range q.Options {
		cursor := "  "
		if a.cursor == i {
			cursor = "❯ "
		}
		mark := off
		if a.selected[i] {
			mark = on
		}
		labelBudget := width - 7 // cursor(2) + "N. "(3) + mark and gap(2)
		if hasRec && recIdx == i {
			labelBudget -= len("  ★ recommended")
		}
		row := fmt.Sprintf("%s%d. %s %s", cursor, i+1, mark, truncateDisplay(opt.Label, max(labelBudget, 8)))
		if a.cursor == i {
			row = cursorStyle.Render(row)
		} else {
			row = stylePaletteItem.Render(row)
		}
		if hasRec && recIdx == i {
			row += "  " + recStyle.Render("★ recommended")
		}
		lines = append(lines, row)
		if d := strings.TrimSpace(opt.Description); d != "" {
			for _, dl := range strings.Split(wrapQuestionText(d, descWidth), "\n") {
				lines = append(lines, styleMeta.Render(strings.Repeat(" ", questionDescIndent)+dl))
			}
		}
	}
	otherPrefix := "✎ "
	otherValue := "Type your own answer…"
	if a.editingOther {
		otherValue = "Type your own answer: " + a.freeTextBuf + "█"
	} else if a.usingFreeText {
		otherValue = "Your answer: " + a.freeText
	}
	for i, line := range questionAnswerLines(otherPrefix, otherValue, width) {
		if i == 0 && a.cursor == p.otherRowIndex() {
			lines = append(lines, cursorStyle.Render(line))
		} else {
			lines = append(lines, stylePaletteItem.Render(line))
		}
	}
	noteLines := []string{""}
	switch {
	case a.editingNote:
		noteLines = questionAnswerLines("  note: ", a.noteBuf+"█", width)
	case a.note != "":
		noteLines = questionAnswerLines("  note: ", a.note, width)
	case a.skipped:
		noteLines = []string{"  skipped"}
	}
	for _, line := range noteLines {
		lines = append(lines, styleMeta.Render(line))
	}
	// Hold the frame steady across tabs: pad to the tallest question.
	rows := 0
	for _, qq := range p.questions {
		rows = max(rows, questionOptionRows(qq, descWidth))
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func renderSubmitTab(p *questionPickerState, width int) string {
	if !p.allAnswered() {
		return styleEmpty.Render(truncateDisplay("  answer every question to enable submit", width))
	}
	var b strings.Builder
	for _, part := range strings.Split(strings.TrimPrefix(p.summaryLine(), "✓ "), " · ") {
		b.WriteString(styleMeta.Render(truncateDisplay("  "+part, width)) + "\n")
	}
	b.WriteString(renderMenuItem(menuItemOpts{Label: "Submit answers", Cursor: true}))
	return b.String()
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
		if sel.Skipped {
			vals = "skipped"
		}
		if sel.Note != "" {
			vals += " (note: " + sel.Note + ")"
		}
		// Scrollback summary only; the model receives the full answer.
		parts = append(parts, sel.Header+": "+truncateDisplay(vals, questionSummaryValueMax))
	}
	return "✓ " + strings.Join(parts, " · ")
}

func (m Model) updateQuestionPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := &m.questionPicker
	if p.isEditing() {
		switch {
		case msg.Code == tea.KeyEsc:
			p.cancelEditingOther()
		case msg.Code == tea.KeyEnter && p.answers[p.activeTab].editingNote:
			p.commitNote()
		case msg.Code == tea.KeyEnter:
			p.commitOther()
		case msg.Code == tea.KeyBackspace:
			p.backspaceOther()
		case msg.Text != "":
			p.appendToOther(msg.Text)
		}
		return m, nil
	}
	if msg.Code != tea.KeyEsc {
		p.escArmed = false
	}
	// On the free-text row, typing is the answer: any printable key starts
	// the edit (n/s/digits keep their meaning on the option rows).
	if p.onOtherRow() && msg.Text != "" && msg.Code != tea.KeySpace && msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		p.startEditingOther()
		p.appendToOther(msg.Text)
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		if !p.escArmed {
			p.escArmed = true
			return m, nil
		}
		if m.questionReq.Reply != nil {
			m.questionReq.Reply.Cancelled = true
		}
		if !m.sendDecision(agent.Deny) {
			return m, nil
		}
		m.appendLine(styleMeta.Render("question cancelled"))
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
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if p.quickPick(int(msg.Code - '1')) {
			if !p.questions[p.activeTab].MultiSelect {
				p.nextTab()
			}
		}
	case 'n':
		p.startEditingNote()
	case 's':
		if !p.onSubmitTab() {
			p.skipCurrent()
			p.nextTab()
		}
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
		case p.questions[p.activeTab].MultiSelect:
			// Space toggles; Enter just moves on once the user is done.
			p.nextTab()
		default:
			p.selectCurrent()
			p.nextTab()
		}
	}
	return m, nil
}
func updateQuestionPickerPaste(m Model, msg tea.PasteMsg) (Model, tea.Cmd) {
	if m.questionPicker.isEditing() {
		content := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(msg.Content)
		m.questionPicker.appendToOther(content)
	}
	return m, nil
}
func (p *questionPickerState) footerHint() string { return p.footerHintFor(p.activeTab) }

// footerHintFor builds the key hints as they would read on the given tab.
func (p *questionPickerState) footerHintFor(tab int) string {
	probe := *p
	probe.activeTab = tab
	return probe.footerHintAt()
}

func (p *questionPickerState) footerHintAt() string {
	if p.escArmed {
		return "  esc again to cancel all questions · any other key keeps answering"
	}
	if p.onSubmitTab() {
		nav := fmt.Sprintf("←/→ review a question (%d)", len(p.questions))
		if p.allAnswered() {
			return "  " + nav + " · enter submit · esc cancel all"
		}
		return "  " + nav + " · esc cancel all"
	}
	if p.isEditing() {
		return "  type your answer · enter confirm · esc cancel edit"
	}
	action := "enter choose · 1-9 quick pick"
	if p.questions[p.activeTab].MultiSelect {
		action = "space toggle · 1-9 toggle · enter next"
	}
	action += " · n note · s skip"
	nav := ""
	if len(p.questions) > 1 {
		nav = "tab next · "
	}
	return "  ↑/↓ move · " + action + " · " + nav + "esc cancel"
}
