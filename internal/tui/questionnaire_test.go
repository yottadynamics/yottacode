package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// questionnaireTestQuestions mirrors a typical two-question call: a
// single-select with two options and a multi-select with three.
func questionnaireTestQuestions() []agent.UserQuestion {
	return []agent.UserQuestion{
		{
			Question: "Which auth approach should the CLI use?",
			Header:   "Auth",
			Options: []agent.UserQuestionOption{
				{Label: "OAuth device flow", Description: "browser hand-off, no stored secret"},
				{Label: "API key", Description: "simplest; stored in config.toml"},
			},
		},
		{
			Question:    "Where should tokens be stored?",
			Header:      "Storage",
			MultiSelect: true,
			Options: []agent.UserQuestionOption{
				{Label: "keychain", Description: "OS-native"},
				{Label: "encrypted file", Description: "portable"},
				{Label: "plaintext", Description: "do not"},
			},
		},
	}
}

// questionnaireTestModel arms a test model as if a turn were in
// flight and the UserQuestionsNeeded event had just landed.
func questionnaireTestModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.turnActive = true
	m.answersCh = make(chan agent.QuestionnaireAnswers, 1)
	m, cmd := applyMsg(m, agentEventMsg{ev: agent.UserQuestionsNeeded{Questions: questionnaireTestQuestions()}})
	if cmd != nil {
		t.Fatalf("UserQuestionsNeeded should return a nil cmd (no waitForEvent until the user answers)")
	}
	return m
}

// drainAnswers fails the test unless a reply was delivered on answersCh.
func drainAnswers(t *testing.T, m Model) agent.QuestionnaireAnswers {
	t.Helper()
	select {
	case ans := <-m.answersCh:
		return ans
	default:
		t.Fatalf("no QuestionnaireAnswers delivered on answersCh")
		return agent.QuestionnaireAnswers{}
	}
}

func TestQuestionnaire_EventParksState(t *testing.T) {
	m := questionnaireTestModel(t)
	if m.questionnaire == nil {
		t.Fatal("UserQuestionsNeeded should set m.questionnaire")
	}
	if got := len(m.questionnaire.questions); got != 2 {
		t.Fatalf("questionnaire questions = %d, want 2", got)
	}
}

func TestRenderQuestionnaire_TabsOptionsHint(t *testing.T) {
	m := questionnaireTestModel(t)
	out := ansi.Strip(renderQuestionnaire(m.questionnaire, m.width))
	for _, want := range []string{
		"Questions",
		"0/2 answered",
		"[ Auth ]", // active tab bracketed
		"Storage",  // inactive tab present
		"←/→ switch (1/2)",
		"Which auth approach should the CLI use?",
		"OAuth device flow",
		"API key",
		"Other",
		"type your own answer",
		"esc skip all",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n%s", want, out)
		}
	}
	// Multi-select chrome belongs to question 2 only.
	if strings.Contains(out, "[ ]") {
		t.Errorf("single-select question must not render checkbox rows:\n%s", out)
	}
	m.questionnaire.tab = 1
	out = ansi.Strip(renderQuestionnaire(m.questionnaire, m.width))
	for _, want := range []string{"(multi-select)", "[ ] keychain", "space toggle"} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-select render missing %q\n%s", want, out)
		}
	}
}

// Answering the first question advances to the next unanswered tab;
// answering the last submits the whole set in question order.
func TestQuestionnaire_SingleSelectAutoAdvanceAndSubmit(t *testing.T) {
	m := questionnaireTestModel(t)
	// Question 1: cursor starts on option 0; Enter commits it.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.questionnaire == nil {
		t.Fatal("questionnaire should still be open with question 2 unanswered")
	}
	if m.questionnaire.tab != 1 {
		t.Fatalf("tab after answering q1 = %d, want auto-advance to 1", m.questionnaire.tab)
	}
	// Question 2 (multi): toggle two options, then confirm.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeySpace}) // keychain
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeySpace}) // encrypted file
	m, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.questionnaire != nil {
		t.Fatal("answering the last question should submit and clear the questionnaire")
	}
	if cmd == nil {
		t.Fatal("submit should re-queue waitForEvent")
	}
	ans := drainAnswers(t, m)
	if ans.Declined {
		t.Fatal("submit should not be a decline")
	}
	if got, want := len(ans.Answers), 2; got != want {
		t.Fatalf("answers = %d, want %d", got, want)
	}
	if got := ans.Answers[0].Selected; len(got) != 1 || got[0] != "OAuth device flow" {
		t.Errorf("q1 answer = %v, want [OAuth device flow]", got)
	}
	if got := ans.Answers[1].Selected; len(got) != 2 || got[0] != "keychain" || got[1] != "encrypted file" {
		t.Errorf("q2 answer = %v, want [keychain encrypted file]", got)
	}
	if ans.Answers[0].IsOther || ans.Answers[1].IsOther {
		t.Errorf("option picks must not be flagged IsOther")
	}
}

// Enter on a multi-select with nothing toggled picks the cursor row
// (it must not commit an empty answer or no-op forever).
func TestQuestionnaire_MultiSelectEnterPicksCursorRow(t *testing.T) {
	m := questionnaireTestModel(t)
	m.questionnaire.tab = 1
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown}) // encrypted file
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.questionnaire == nil {
		t.Fatal("q1 is still unanswered — questionnaire must stay open")
	}
	if m.questionnaire.tab != 0 {
		t.Fatalf("tab = %d, want wrap-around auto-advance to 0", m.questionnaire.tab)
	}
	if !m.questionnaire.answered[1] {
		t.Fatal("q2 should be marked answered")
	}
	if got := activeToggles(m.questionnaire, 1); len(got) != 1 || got[0] != 1 {
		t.Fatalf("toggles = %v, want [1] (cursor row picked)", got)
	}
}

// Digit keys jump-select; the digit for the Other row opens free-text
// mode, and committed text comes back flagged IsOther.
func TestQuestionnaire_OtherFreeText(t *testing.T) {
	m := questionnaireTestModel(t)
	// "3" on question 1 = the synthetic Other row (2 options + 1).
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if !m.questionnaire.otherFocus {
		t.Fatal("digit on the Other row should focus the free-text input")
	}
	out := ansi.Strip(renderQuestionnaire(m.questionnaire, m.width))
	if !strings.Contains(out, "esc back") {
		t.Errorf("free-text mode hint missing:\n%s", out)
	}
	for _, r := range "keep legacy" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.questionnaire.otherFocus {
		t.Fatal("Enter should commit the free text and leave text mode")
	}
	if m.questionnaire.tab != 1 {
		t.Fatalf("tab = %d, want auto-advance to 1", m.questionnaire.tab)
	}
	// Answer q2 to submit, then check the Other flag round-trips.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	ans := drainAnswers(t, m)
	if got := ans.Answers[0]; !got.IsOther || len(got.Selected) != 1 || got.Selected[0] != "keep legacy" {
		t.Errorf("q1 answer = %+v, want IsOther with [keep legacy]", got)
	}
}

// Esc in free-text mode backs out to the option list; Esc on the list
// declines the entire set and the turn continues.
func TestQuestionnaire_EscDeclines(t *testing.T) {
	m := questionnaireTestModel(t)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnd}) // cursor to Other
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.questionnaire.otherFocus {
		t.Fatal("Enter on Other should focus the input")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.questionnaire == nil || m.questionnaire.otherFocus {
		t.Fatal("Esc in text mode should back out to the list, not decline")
	}
	m, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.questionnaire != nil {
		t.Fatal("Esc on the list should decline and clear the questionnaire")
	}
	if cmd == nil {
		t.Fatal("decline should re-queue waitForEvent")
	}
	ans := drainAnswers(t, m)
	if !ans.Declined {
		t.Fatal("Esc should deliver Declined=true")
	}
}

// The questionnaire branch must intercept keys ahead of the mid-turn
// handler: Esc declines instead of cancelling the turn.
func TestQuestionnaire_EscDoesNotCancelTurn(t *testing.T) {
	m := questionnaireTestModel(t)
	cancelled := false
	m.turnCancel = func() { cancelled = true }
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled {
		t.Fatal("Esc during the questionnaire must decline, not cancel the turn")
	}
	if m.questionnaire != nil {
		t.Fatal("questionnaire should be cleared by the decline")
	}
}

// Tab navigation: Left/Right cycle questions; revisiting an answered
// question and picking a different option overwrites the answer.
func TestQuestionnaire_TabSwitchAndReanswer(t *testing.T) {
	m := questionnaireTestModel(t)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // q1 = OAuth, advance to q2
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyLeft})  // back to q1
	if m.questionnaire.tab != 0 {
		t.Fatalf("tab after Left = %d, want 0", m.questionnaire.tab)
	}
	// Re-answer q1 with option 2; q2 is the only unanswered left.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m.questionnaire.tab != 1 {
		t.Fatalf("tab = %d, want advance to unanswered q2", m.questionnaire.tab)
	}
	if got := m.questionnaire.chosen[0]; got != 1 {
		t.Fatalf("q1 chosen = %d, want overwrite to 1", got)
	}
	// The answered q1 tab renders with the ✔ marker.
	out := ansi.Strip(renderQuestionnaire(m.questionnaire, m.width))
	if !strings.Contains(out, "Auth ✔") {
		t.Errorf("answered tab missing ✔ marker:\n%s", out)
	}
}

// A dead turn must clear a dangling questionnaire so the keyboard
// isn't owned by an overlay nothing is listening to.
func TestQuestionnaire_TurnEndClearsState(t *testing.T) {
	m := questionnaireTestModel(t)
	m, _ = applyMsg(m, turnEndedMsg{})
	if m.questionnaire != nil {
		t.Fatal("turnEndedMsg should clear the questionnaire")
	}
}

// View renders the questionnaire in place of the input frame while
// it's up (picker-style body in the live footer).
func TestQuestionnaire_ViewShowsBody(t *testing.T) {
	m := questionnaireTestModel(t)
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "Which auth approach should the CLI use?") {
		t.Errorf("View missing questionnaire body:\n%s", out)
	}
	if strings.Contains(out, "? for cheatsheet") {
		t.Errorf("View should not render the idle input footer under the questionnaire")
	}
}
