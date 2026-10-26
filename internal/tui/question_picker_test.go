package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func twoQuestions() []agent.Question {
	return []agent.Question{
		{
			Header:   "Auth",
			Question: "How should we authenticate?",
			Options: []agent.QuestionOption{
				{Label: "OAuth"},
				{Label: "API key", Recommended: true},
			},
		},
		{
			Header:      "Notify",
			Question:    "Which channels?",
			MultiSelect: true,
			Options: []agent.QuestionOption{
				{Label: "Slack"},
				{Label: "Email"},
			},
		},
	}
}

// --- questionPickerState unit tests ----------------------------------------

func TestRenderQuestionPicker_StableHeightAcrossTabs(t *testing.T) {
	questions := twoQuestions()
	questions[0].Question = "Short?"
	questions[1].Question = strings.Repeat("This is a deliberately long question. ", 8)
	p := newQuestionPickerState(questions)
	p.activeTab = 0
	first := stripANSI(renderQuestionPicker(&p, 80))
	p.activeTab = 1
	second := stripANSI(renderQuestionPicker(&p, 80))
	if strings.Count(first, "\n") != strings.Count(second, "\n") {
		t.Fatalf("question tabs should preserve frame height: first=%d second=%d", strings.Count(first, "\n"), strings.Count(second, "\n"))
	}
	if !strings.Contains(first, "Questions · 1/2") || !strings.Contains(second, "Questions · 2/2") {
		t.Fatalf("expected questionnaire progress headers: first=%q second=%q", first, second)
	}
}
func TestNewQuestionPickerState_PreSelectsRecommendedOption(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	if !p.answered(0) {
		t.Fatalf("question 0 has a recommended option and should start answered")
	}
	if p.answered(1) {
		t.Errorf("question 1 has no recommended option and should start unanswered")
	}
	if p.allAnswered() {
		t.Errorf("allAnswered should be false until every question is answered")
	}
}

func TestQuestionPickerState_SingleSelectReplacesPriorChoice(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	p.moveCursor(-1) // land on index 0 ("OAuth")
	p.selectCurrent()
	if !p.answers[0].selected[0] || p.answers[0].selected[1] {
		t.Errorf("single-select should replace the prior selection; got %+v", p.answers[0].selected)
	}
}

func TestQuestionPickerState_MultiSelectToggles(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	p.activeTab = 1
	p.moveCursor(-1) // land on option 0 ("Slack")
	p.selectCurrent()
	p.moveCursor(1) // option 1 ("Email")
	p.selectCurrent()
	if !p.answers[1].selected[0] || !p.answers[1].selected[1] {
		t.Fatalf("both options should be selected; got %+v", p.answers[1].selected)
	}
	p.moveCursor(-1) // back to option 0
	p.selectCurrent()
	if p.answers[1].selected[0] {
		t.Errorf("re-selecting a chosen multi-select option should toggle it off")
	}
}

func TestQuestionPickerState_OtherIsExclusiveWithOptions(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	p.moveCursor(1) // "API key" was pre-selected; move onto the Other row (index 2)
	p.moveCursor(1)
	p.startEditingOther()
	p.appendToOther("delegated auth")
	p.commitOther()
	if p.answers[0].usingFreeText != true || p.answers[0].freeText != "delegated auth" {
		t.Fatalf("expected free-text answer to be committed; got %+v", p.answers[0])
	}
	if len(p.answers[0].selected) != 0 {
		t.Errorf("committing free text should clear any prior option selection; got %+v", p.answers[0].selected)
	}
}

func TestQuestionPickerState_CommitOtherIgnoresEmptyBuffer(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	p.moveCursor(1)
	p.moveCursor(1)
	p.startEditingOther()
	p.commitOther() // nothing typed
	if p.answers[0].usingFreeText {
		t.Errorf("an empty Other submission should not count as an answer")
	}
}

func TestQuestionPickerState_MoveCursorClampsAtBounds(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	for range 10 {
		p.moveCursor(-1)
	}
	if p.answers[0].cursor != 0 {
		t.Errorf("cursor should clamp at 0; got %d", p.answers[0].cursor)
	}
	for range 10 {
		p.moveCursor(1)
	}
	if p.answers[0].cursor != p.otherRowIndex() {
		t.Errorf("cursor should clamp at the Other row; got %d, want %d", p.answers[0].cursor, p.otherRowIndex())
	}
}

func TestQuestionPickerState_BuildAnswerOrderAndShape(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	p.answers[1].selected = map[int]bool{0: true, 1: true}
	reply := p.buildAnswer()
	if len(reply.Selections) != 2 {
		t.Fatalf("expected 2 selections; got %d", len(reply.Selections))
	}
	if reply.Selections[0].Header != "Auth" || reply.Selections[0].Labels[0] != "API key" {
		t.Errorf("question 0 selection wrong: %+v", reply.Selections[0])
	}
	if reply.Selections[1].Header != "Notify" || len(reply.Selections[1].Labels) != 2 {
		t.Errorf("question 1 selection wrong: %+v", reply.Selections[1])
	}
}

// --- Update() integration: full interactive round trip ---------------------

func setupQuestionModel(t *testing.T, questions []agent.Question) Model {
	t.Helper()
	m := newTestModel(t)
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m, _ = m.handleAgentEventTea(agent.QuestionNeeded{
		Questions: questions,
		Reply:     &agent.QuestionAnswer{},
	})
	if !m.awaitingQuestion {
		t.Fatalf("QuestionNeeded should set awaitingQuestion")
	}
	return m
}

func TestQuestionNeeded_SetsAwaitingQuestionAndInitState(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	if len(m.questionPicker.questions) != 2 {
		t.Fatalf("expected picker to hold 2 questions; got %d", len(m.questionPicker.questions))
	}
	if _, ok := m.activePopupBody(); !ok {
		t.Errorf("expected the question picker to render as the active popup body")
	}
}

func TestQuestionNeeded_EscCancelsWholeSet(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	reply := m.questionReq.Reply

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.awaitingQuestion {
		t.Fatalf("first Esc must only arm cancellation")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.awaitingQuestion {
		t.Errorf("second Esc should clear awaitingQuestion")
	}
	select {
	case d := <-m.decisions:
		if d != agent.Deny {
			t.Errorf("expected Deny on Esc; got %v", d)
		}
	default:
		t.Fatalf("no decision sent on Esc")
	}
	if !reply.Cancelled {
		t.Errorf("expected Reply.Cancelled = true after Esc")
	}
}

func TestQuestionNeeded_SubmitOnlyReachableOnceAllAnswered(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	// Jump straight to the Submit tab (question 0 already has its
	// recommended default; question 1 does not).
	m.questionPicker.activeTab = len(m.questionPicker.questions)
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.awaitingQuestion {
		t.Fatalf("submitting with an unanswered question should be a no-op, not close the modal")
	}
	select {
	case d := <-m.decisions:
		t.Fatalf("no decision should be sent while a question is unanswered; got %v", d)
	default:
	}
}

func TestQuestionNeeded_FullRoundTripSubmitsAllAnswers(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	reply := m.questionReq.Reply

	// Question 0 ("Auth") already has its recommended default selected.
	// Answer question 1 ("Notify", multi_select): select both options.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyRight}) // -> question 1
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeySpace}) // select "Slack"
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeySpace}) // select "Email"

	if !m.questionPicker.allAnswered() {
		t.Fatalf("expected both questions answered before submitting; state=%+v", m.questionPicker.answers)
	}

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyRight}) // -> Submit tab
	if !m.questionPicker.onSubmitTab() {
		t.Fatalf("expected to be on the Submit tab")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm submit

	if m.awaitingQuestion {
		t.Errorf("submitting should clear awaitingQuestion")
	}
	select {
	case d := <-m.decisions:
		if d != agent.AllowOnce {
			t.Errorf("expected AllowOnce on submit; got %v", d)
		}
	default:
		t.Fatalf("no decision sent on submit")
	}
	if len(reply.Selections) != 2 {
		t.Fatalf("expected 2 resolved selections; got %+v", reply.Selections)
	}
	if reply.Selections[0].Labels[0] != "API key" {
		t.Errorf("expected question 0's recommended default in the answer; got %+v", reply.Selections[0])
	}
	if len(reply.Selections[1].Labels) != 2 {
		t.Errorf("expected both Notify options in the answer; got %+v", reply.Selections[1])
	}
}

func TestQuestionNeeded_OtherRowFreeTextEndToEnd(t *testing.T) {
	m := setupQuestionModel(t, []agent.Question{{
		Header:   "Auth",
		Question: "How should we authenticate?",
		Options:  []agent.QuestionOption{{Label: "OAuth"}, {Label: "API key"}},
	}})
	reply := m.questionReq.Reply

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})  // option 0
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})  // Other row
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // start editing
	if !m.questionPicker.isEditingOther() {
		t.Fatalf("Enter on the Other row should start editing")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "d"})
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "e"})
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "v"})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // commit
	if m.questionPicker.isEditingOther() {
		t.Fatalf("Enter should commit and leave edit mode")
	}
	if !m.questionPicker.allAnswered() {
		t.Fatalf("free-text answer should count as answered")
	}

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyRight}) // -> Submit tab
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm

	<-m.decisions
	if len(reply.Selections) != 1 || reply.Selections[0].FreeText != "dev" {
		t.Fatalf("expected free-text answer %q; got %+v", "dev", reply.Selections)
	}
}

func TestQuestionNeeded_PasteGoesIntoOtherFieldNotBackgroundInput(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})  // option 0
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})  // Other row
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // start editing
	if !m.questionPicker.isEditingOther() {
		t.Fatalf("expected to be editing the Other row")
	}

	m, _ = applyMsg(m, tea.PasteMsg{Content: "pasted answer\nwith a newline"})

	if m.textInput.Value() != "" {
		t.Errorf("paste while editing Other must not reach the background input box; got %q", m.textInput.Value())
	}
	if got := m.questionPicker.answers[0].freeTextBuf; !strings.Contains(got, "pasted answer") {
		t.Errorf("expected pasted content in the Other buffer; got %q", got)
	}
}

func TestQuestionNeeded_PasteIgnoredWhenNotEditingOther(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	// Not editing Other — a stray paste (e.g. terminal noise) must not
	// silently land in the answer buffer either.
	m, _ = applyMsg(m, tea.PasteMsg{Content: "unexpected"})
	if m.questionPicker.answers[0].freeTextBuf != "" {
		t.Errorf("paste should be a no-op outside of Other-editing; got %q", m.questionPicker.answers[0].freeTextBuf)
	}
}

func TestQuestionNeeded_OwnsAllKeysWhileOpen(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	if !m.popupOpen() {
		t.Errorf("popupOpen() should report true while a question is open")
	}
	// A plain letter isn't bound to any picker action; the modal must
	// still swallow it rather than let it fall through to the
	// background chat textarea (the guard extensions at every
	// !m.awaitingApproval && !m.awaitingPathTrust site exist precisely
	// so this can't happen).
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "x"})
	if m.textInput.Value() != "" {
		t.Errorf("typing while a question is open should not reach the background input box; got %q", m.textInput.Value())
	}
	if !m.awaitingQuestion {
		t.Errorf("an unbound key must not close the question modal")
	}
}

func TestQuestionNeeded_EnterSelectsAndAdvancesSingleChoice(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyUp}) // cursor on "OAuth"
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.questionPicker.activeTab != 1 {
		t.Fatalf("Enter on a single-choice question should move to the next question; activeTab=%d", m.questionPicker.activeTab)
	}
	if !m.questionPicker.answers[0].selected[0] || m.questionPicker.answers[0].selected[1] {
		t.Errorf("Enter should have selected OAuth and replaced the recommended default; got %+v", m.questionPicker.answers[0].selected)
	}
}

func TestQuestionNeeded_EnterOnMultiSelectMovesOnWithoutToggling(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeySpace}) // select "Slack"
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.questionPicker.onSubmitTab() {
		t.Fatalf("Enter on the last multi-select question should move to Review")
	}
	if got := m.questionPicker.answers[1].selected; len(got) != 1 || !got[0] {
		t.Errorf("Enter must not toggle options; got %+v", got)
	}
}

func TestQuestionNeeded_KeyHintsMatchBehavior(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	if got := strings.Join(m.contextualKeyHints(), " "); !strings.Contains(got, "Enter: select") {
		t.Errorf("single-choice hints should mention Enter: select; got %q", got)
	}
	m.questionPicker.activeTab = 1
	if got := strings.Join(m.contextualKeyHints(), " "); !strings.Contains(got, "Space: toggle") || !strings.Contains(got, "Enter: next") {
		t.Errorf("multi-select hints should mention Space: toggle and Enter: next; got %q", got)
	}
	m.questionPicker.activeTab = 0
	m.questionPicker.answers[0].editingOther = true
	if got := strings.Join(m.contextualKeyHints(), " "); !strings.Contains(got, "Esc: cancel edit") {
		t.Errorf("Other-editing hints should say Esc cancels the edit; got %q", got)
	}
}

func TestQuestionPickerState_SummaryLineTruncatesLongFreeText(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	p.answers[0].usingFreeText = true
	p.answers[0].freeText = strings.Repeat("x", 300)
	line := p.summaryLine()
	if strings.Contains(line, strings.Repeat("x", 100)) {
		t.Errorf("summary should truncate long answers; got %d chars", len(line))
	}
	if !strings.Contains(line, "…") {
		t.Errorf("expected an ellipsis on the truncated answer; got %q", line)
	}
}

func TestRenderQuestionPicker_WrapsLongFreeTextAndNotes(t *testing.T) {
	p := newQuestionPickerState([]agent.Question{{
		Header:   "Auth",
		Question: "How should we authenticate?",
		Options:  []agent.QuestionOption{{Label: "OAuth"}},
	}})
	p.answers[0].cursor = p.otherRowIndex()
	p.answers[0].usingFreeText = true
	p.answers[0].freeText = strings.Repeat("long answer ", 12)
	p.answers[0].note = strings.Repeat("note ", 12)
	before := stripANSI(renderQuestionPicker(&p, 50))
	p.answers[0].editingOther = true
	p.answers[0].freeTextBuf = strings.Repeat("long answer ", 12)
	editing := stripANSI(renderQuestionPicker(&p, 50))
	if strings.Count(before, "\n") != strings.Count(editing, "\n") {
		t.Fatalf("editing a long answer must not resize the popup: before=%d after=%d", strings.Count(before, "\n"), strings.Count(editing, "\n"))
	}
	for _, line := range strings.Split(editing, "\n") {
		if ansi.StringWidth(line) > 42 {
			t.Fatalf("content line exceeds popup content width: %d: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestWrapQuestionText_HardWrapsLongTokensAndUnicode(t *testing.T) {
	got := wrapQuestionText(strings.Repeat("界", 40), 10)
	for _, line := range strings.Split(got, "\n") {
		if ansi.StringWidth(line) > 10 {
			t.Fatalf("hard-wrapped line exceeds width: %d: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestRenderQuestionPicker_ShowsRecommendedMarkerAndNumbers(t *testing.T) {
	qs := twoQuestions()
	qs[0].Options[1].Description = "Simplest to ship"
	p := newQuestionPickerState(qs)
	out := stripANSI(renderQuestionPicker(&p, 80))
	for _, want := range []string{"1. ○ OAuth", "2. ◉ API key", "★ recommended", "Simplest to ship", "✎ Type your own answer…", "1-9 quick pick", "○ Notify", "✓ Auth"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "★ recommended") != 1 {
		t.Errorf("recommended marker should appear once:\n%s", out)
	}
	p.activeTab = 1
	if out := stripANSI(renderQuestionPicker(&p, 80)); strings.Contains(out, "★") || !strings.Contains(out, "1. ☐ Slack") {
		t.Errorf("multi-select tab without recommendation should use checkboxes and no star:\n%s", out)
	}
}

func TestQuestionPickerState_QuickPickSelectsByNumber(t *testing.T) {
	p := newQuestionPickerState(twoQuestions())
	if !p.quickPick(0) || !p.answers[0].selected[0] || p.answers[0].selected[1] {
		t.Fatalf("quickPick(0) should select only option 1: %+v", p.answers[0].selected)
	}
	if p.quickPick(5) {
		t.Errorf("out-of-range digit must be ignored")
	}
	p.activeTab = 1
	p.quickPick(0)
	p.quickPick(1)
	if len(p.answers[1].selected) != 2 {
		t.Errorf("multi-select quick pick should toggle on: %+v", p.answers[1].selected)
	}
	p.quickPick(0)
	if p.answers[1].selected[0] {
		t.Errorf("second quick pick should toggle off")
	}
}

func TestQuestionNeeded_DigitKeyPicksAndAdvances(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	next, _ := m.updateQuestionPicker(tea.KeyPressMsg{Code: '1', Text: "1"})
	m = next
	if !m.questionPicker.answers[0].selected[0] || m.questionPicker.activeTab != 1 {
		t.Fatalf("digit should select option and advance: tab=%d sel=%+v", m.questionPicker.activeTab, m.questionPicker.answers[0].selected)
	}
}

func TestRenderQuestionPicker_HeightStableAcrossEveryTabIncludingReview(t *testing.T) {
	qs := twoQuestions()
	qs[0].Options[0].Description = strings.Repeat("a long trade-off description ", 6)
	qs[1].Options = append(qs[1].Options, agent.QuestionOption{Label: "Pager"}, agent.QuestionOption{Label: "SMS"})
	p := newQuestionPickerState(qs)
	p.quickPick(0)
	p.activeTab = 1
	p.quickPick(0)
	for _, width := range []int{60, 80, 120} {
		want := -1
		for tab := 0; tab <= len(qs); tab++ {
			p.activeTab = tab
			got := strings.Count(stripANSI(renderQuestionPicker(&p, width)), "\n")
			if want < 0 {
				want = got
			}
			if got != want {
				t.Fatalf("width %d tab %d: height %d, want %d", width, tab, got, want)
			}
		}
	}
}

func TestQuestionNeeded_NoteKeyAttachesNote(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	for _, k := range []tea.KeyPressMsg{{Code: 'n', Text: "n"}, {Code: 'n', Text: "n"}, {Code: 'o', Text: "o"}, {Code: tea.KeyEnter}} {
		m, _ = m.updateQuestionPicker(k)
	}
	if got := m.questionPicker.answers[0].note; got != "no" {
		t.Fatalf("note = %q; the first n opens the editor, later keys are text", got)
	}
	if got := m.questionPicker.buildAnswer().Selections[0].Note; got != "no" {
		t.Errorf("answer note = %q", got)
	}
	m, _ = m.updateQuestionPicker(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m, _ = m.updateQuestionPicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.questionPicker.answers[0].note != "no" || m.questionPicker.isEditing() {
		t.Errorf("Esc should abandon the edit and keep the stored note")
	}
}

func TestQuestionNeeded_SkipKeyAnswersWithoutSelection(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	m, _ = m.updateQuestionPicker(tea.KeyPressMsg{Code: 's', Text: "s"})
	p := &m.questionPicker
	if !p.answered(0) || p.activeTab != 1 || len(p.answers[0].selected) != 0 {
		t.Fatalf("skip should clear the preselection, count as answered, and advance: %+v tab=%d", p.answers[0], p.activeTab)
	}
	if sel := p.buildAnswer().Selections[0]; !sel.Skipped || len(sel.Labels) != 0 {
		t.Errorf("selection = %+v", sel)
	}
	p.activeTab = 0
	p.quickPick(1)
	if p.answers[0].skipped {
		t.Errorf("choosing an option should clear skipped")
	}
}

func TestQuestionPopup_HasNoMouseCloseGlyph(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	body, ok := m.activePopupBody()
	if !ok {
		t.Fatalf("question popup should be active")
	}
	if strings.Contains(stripANSI(body), "×") {
		t.Errorf("keyboard-only question popup must not draw the mouse close glyph")
	}
}

func TestQuestionNeeded_EscArmingResetsOnOtherKey(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !strings.Contains(stripANSI(renderQuestionPicker(&m.questionPicker, 80)), "esc again to cancel") {
		t.Errorf("armed footer should explain the second Esc")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.awaitingQuestion {
		t.Fatalf("an intervening key must disarm Esc")
	}
}

func TestRenderQuestionPicker_UnvisitedDefaultShowsPendingMark(t *testing.T) {
	qs := twoQuestions()
	qs[1].Options[0].Recommended = true
	p := newQuestionPickerState(qs)
	out := stripANSI(renderQuestionPicker(&p, 80))
	if !strings.Contains(out, "✓ Auth") || !strings.Contains(out, "○ Notify") {
		t.Errorf("defaulted but unvisited question must show ○:\n%s", out)
	}
	if !p.allAnswered() {
		t.Errorf("defaults still count as answered for submission")
	}
	p.nextTab()
	if out := stripANSI(renderQuestionPicker(&p, 80)); !strings.Contains(out, "✓ Notify") {
		t.Errorf("visiting the tab confirms it:\n%s", out)
	}
}

func TestQuestionNeeded_TypingOnOtherRowStartsEditing(t *testing.T) {
	m := setupQuestionModel(t, twoQuestions())
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	for _, r := range "no s" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := m.questionPicker.answers[0].freeTextBuf; got != "no s" {
		t.Fatalf("typed text should land in Other (n/s included), got %q", got)
	}
	if m.questionPicker.answers[0].skipped || m.questionPicker.answers[0].editingNote {
		t.Errorf("n/s must not trigger note/skip while typing on the Other row")
	}
}

func TestRenderQuestionPicker_NarrowWidthStaysStableAndBounded(t *testing.T) {
	qs := twoQuestions()
	qs[0].Options[1].Description = strings.Repeat("trade-off ", 20)
	p := newQuestionPickerState(qs)
	want := -1
	for tab := 0; tab <= len(qs); tab++ {
		p.activeTab = tab
		out := stripANSI(renderQuestionPicker(&p, 50))
		if want < 0 {
			want = strings.Count(out, "\n")
		}
		if got := strings.Count(out, "\n"); got != want {
			t.Fatalf("tab %d height %d want %d", tab, got, want)
		}
		for _, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > 50 {
				t.Fatalf("tab %d line exceeds width 50 (%d): %q", tab, w, line)
			}
		}
	}
}

func TestRenderQuestionPicker_NeverExceedsContentWidth(t *testing.T) {
	long := strings.Repeat("L", 60)
	var qs []agent.Question
	for i, h := range []string{"HeaderNumber1", "HeaderNumber2", "HeaderNumber3", "HeaderNumber4"} {
		qs = append(qs, agent.Question{
			Header: h[:12], Question: "Pick one of these please? " + strings.Repeat("q ", 30),
			Options: []agent.QuestionOption{
				{Label: long, Description: strings.Repeat("trade-off ", 20), Recommended: i%2 == 0},
				{Label: "short"},
			},
		})
	}
	for _, width := range []int{48, 60, 80, 100} {
		limit := width - 8
		p := newQuestionPickerState(qs)
		p.answers[0].note = strings.Repeat("n", 200)
		p.answers[1].freeText, p.answers[1].usingFreeText = strings.Repeat("f", 200), true
		for tab := 0; tab <= len(qs); tab++ {
			p.activeTab = tab
			for _, editing := range []bool{false, true} {
				if tab < len(qs) {
					p.answers[tab].editingNote = editing
					p.answers[tab].noteBuf = strings.Repeat("e", 200)
				}
				for _, line := range strings.Split(stripANSI(renderQuestionPicker(&p, width)), "\n") {
					if w := ansi.StringWidth(line); w > limit {
						t.Fatalf("width %d tab %d editing=%v: line is %d > %d: %q", width, tab, editing, w, limit, line)
					}
				}
			}
		}
	}
}
