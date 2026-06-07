package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// askUserQuestionToolName is referenced by buildChildRegistry's
// unconditional exclusion — subagents have no interactive user to ask.
const askUserQuestionToolName = "ask_user_question"

// userQuestionHeaderMax is the rune budget for a question's tab label.
// Headers longer than this are clamped, never rejected — a sloppy
// header shouldn't cost the model a failed tool call.
const userQuestionHeaderMax = 12

// UserQuestionOption is one selectable choice within a UserQuestion.
type UserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// UserQuestion is one question in an ask_user_question call. The TUI
// renders one questionnaire tab per question; the synthetic free-text
// "Other" choice is appended by the UI and is never part of Options.
type UserQuestion struct {
	Question    string               `json:"question"`
	Header      string               `json:"header"`
	Options     []UserQuestionOption `json:"options"`
	MultiSelect bool                 `json:"multiSelect"`
}

// QuestionAnswer is the user's answer to one UserQuestion, echoed with
// the question so FormatQuestionnaireResult needs no second lookup.
type QuestionAnswer struct {
	Question string   // full question text, echoed
	Header   string   // clamped header, echoed
	Selected []string // chosen option label(s); for Other, the typed free text
	IsOther  bool     // Selected carries free text, not option labels
}

// QuestionnaireAnswers is the TUI's reply for a whole ask_user_question
// call, sent on LoopConfig.QuestionAnswers. Declined means the user
// dismissed the questionnaire (Esc) without answering.
type QuestionnaireAnswers struct {
	Declined bool
	Answers  []QuestionAnswer // parallel to the validated questions slice
}

// askUserQuestionArgs mirrors the tool schema.
type askUserQuestionArgs struct {
	Questions []UserQuestion `json:"questions"`
}

// ParseUserQuestions validates and normalizes an ask_user_question
// call's arguments. It is the single validation authority: the
// UserQuestionsNeeded event carries the returned slice, so the TUI
// never re-parses or re-validates. Headers are clamped to
// userQuestionHeaderMax runes (empty → "Q1"/"Q2"/...); structural
// violations return an error the model can self-correct from.
func ParseUserQuestions(argsJSON string) ([]UserQuestion, error) {
	var a askUserQuestionArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, fmt.Errorf("ask_user_question: invalid args: %w", err)
	}
	if n := len(a.Questions); n < 1 || n > 4 {
		return nil, fmt.Errorf("ask_user_question: needs 1-4 questions, got %d", n)
	}
	for i := range a.Questions {
		q := &a.Questions[i]
		q.Question = strings.TrimSpace(q.Question)
		if q.Question == "" {
			return nil, fmt.Errorf("ask_user_question: question %d has empty question text", i+1)
		}
		if n := len(q.Options); n < 2 || n > 4 {
			return nil, fmt.Errorf("ask_user_question: question %d needs 2-4 options, got %d", i+1, n)
		}
		for j := range q.Options {
			o := &q.Options[j]
			o.Label = strings.TrimSpace(o.Label)
			if o.Label == "" {
				return nil, fmt.Errorf("ask_user_question: question %d option %d has empty label", i+1, j+1)
			}
		}
		q.Header = strings.TrimSpace(q.Header)
		if q.Header == "" {
			q.Header = fmt.Sprintf("Q%d", i+1)
		} else if r := []rune(q.Header); len(r) > userQuestionHeaderMax {
			q.Header = string(r[:userQuestionHeaderMax])
		}
	}
	return a.Questions, nil
}

// questionnaireDeclinedResult is returned (as a normal, non-errored
// tool result) when the user dismisses the questionnaire. Phrased so
// the model keeps going instead of stalling or re-asking.
const questionnaireDeclinedResult = "User declined to answer these questions. " +
	"Proceed using your best judgment, state any assumption you make, and do not re-ask the same questions this turn."

// FormatQuestionnaireResult renders the user's answers as the tool
// result string the model sees: plain Q:/A: lines, multi-select labels
// joined with "; ", free-text answers marked "A (other):".
func FormatQuestionnaireResult(qs []UserQuestion, ans QuestionnaireAnswers) string {
	if ans.Declined {
		return questionnaireDeclinedResult
	}
	var b strings.Builder
	b.WriteString("User answered your questions:\n")
	for i, q := range qs {
		b.WriteString("\nQ: ")
		b.WriteString(q.Question)
		b.WriteString("\n")
		if i >= len(ans.Answers) || len(ans.Answers[i].Selected) == 0 {
			b.WriteString("A: (no answer)\n")
			continue
		}
		a := ans.Answers[i]
		if a.IsOther {
			b.WriteString("A (other): ")
		} else {
			b.WriteString("A: ")
		}
		b.WriteString(strings.Join(a.Selected, "; "))
		b.WriteString("\n")
	}
	return b.String()
}

// AskUserQuestionTool pauses the turn and asks the user 1-4 structured
// multiple-choice questions. Mirrors Claude Code's AskUserQuestion
// surface: per-question header chips (questionnaire tabs), 2-4 options
// each, optional multi-select, and an automatic free-text "Other"
// choice appended by the UI.
//
// Unlike approval round-trips, the questionnaire IS the tool's
// function — RequiresApproval is false, so the call passes
// PlanModeGate and every auto-approval mode (auto/yolo) without being
// bypassed, and Execute itself performs the user round-trip through
// the ctx seams: it emits UserQuestionsNeeded on the parent events
// channel and blocks on the LoopConfig.QuestionAnswers channel
// (injected via WithQuestionnaireAnswers) until the TUI replies.
//
// Interactive sessions only: oneshot never registers it, subagent
// child registries exclude it, and a missing ctx seam returns an
// instructive error instead of hanging.
type AskUserQuestionTool struct{}

func (t *AskUserQuestionTool) Name() string { return askUserQuestionToolName }

func (t *AskUserQuestionTool) Description() string {
	return "Pause and ask the user 1-4 multiple-choice questions when a decision or genuine ambiguity blocks progress. " +
		"Each question renders as a questionnaire tab with 2-4 options plus an automatic free-text \"Other\" choice — never include an \"Other\"/\"Something else\" option yourself. " +
		"Use when requirements are ambiguous, multiple valid approaches exist, or only the user can decide (scope, naming, trade-offs). Put your recommended option first. " +
		"Do NOT use it to ask permission to run a tool (approvals handle that), for questions you can resolve yourself from the code, or to re-ask questions already answered this turn. " +
		"Interactive sessions only."
}

func (t *AskUserQuestionTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type":        "array",
				"minItems":    1,
				"maxItems":    4,
				"description": "1-4 questions to ask the user now.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "The full question text shown to the user. Clear, specific, ends with a question mark.",
						},
						"header": map[string]any{
							"type":        "string",
							"description": "Short tab label (max 12 chars), e.g. \"Auth\", \"Scope\", \"Approach\".",
						},
						"options": map[string]any{
							"type":        "array",
							"minItems":    2,
							"maxItems":    4,
							"description": "2-4 distinct choices. Do NOT add an \"Other\" option — the UI appends a free-text Other automatically.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label": map[string]any{
										"type":        "string",
										"description": "Concise option label the user picks (1-5 words).",
									},
									"description": map[string]any{
										"type":        "string",
										"description": "One-line explanation of what this choice means or its trade-off.",
									},
								},
								"required": []string{"label", "description"},
							},
						},
						"multiSelect": map[string]any{
							"type":        "boolean",
							"description": "true = the user may select multiple options.",
						},
					},
					"required": []string{"question", "header", "options", "multiSelect"},
				},
			},
		},
		"required": []string{"questions"},
	}
}

// RequiresApproval is false: the questionnaire is the tool's function,
// not a permission gate. This is what lets the call pass PlanModeGate's
// read-only allowlist and keeps auto/yolo from ever bypassing the UI —
// those modes skip approvals, and there is no approval here.
func (t *AskUserQuestionTool) RequiresApproval(string) bool { return false }

func (t *AskUserQuestionTool) PreviewCall(argsJSON string) string {
	qs, err := ParseUserQuestions(argsJSON)
	if err != nil {
		return "ask_user_question(...)"
	}
	headers := make([]string, len(qs))
	for i, q := range qs {
		headers[i] = q.Header
	}
	noun := "questions"
	if len(qs) == 1 {
		noun = "question"
	}
	return fmt.Sprintf("ask_user_question(%d %s: %s)", len(qs), noun, strings.Join(headers, ", "))
}

// Execute validates the questions, emits UserQuestionsNeeded on the
// parent events channel, and blocks until the TUI sends the user's
// answers on the LoopConfig.QuestionAnswers channel (or ctx is
// cancelled). Deliberately NOT ParallelSafe: the serial path
// guarantees no other modal competes for the single TUI surface while
// the questionnaire is up.
func (t *AskUserQuestionTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	qs, err := ParseUserQuestions(argsJSON)
	if err != nil {
		return "", err
	}
	events := ParentEvents(ctx)
	answers := QuestionnaireAnswersChan(ctx)
	if events == nil || answers == nil {
		return "", errors.New("ask_user_question is unavailable in this context (no interactive user attached) — proceed with your best judgment and state the assumption you made")
	}
	select {
	case events <- UserQuestionsNeeded{Questions: qs}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case ans := <-answers:
		return FormatQuestionnaireResult(qs, ans), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

var _ Tool = (*AskUserQuestionTool)(nil)
