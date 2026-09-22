package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

	if m.awaitingQuestion {
		t.Errorf("Esc should clear awaitingQuestion")
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

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown}) // option 0
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown}) // Other row
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
