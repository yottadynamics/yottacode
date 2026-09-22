package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AskUserQuestionToolName is the schema-visible name of the structured
// clarification tool.
const AskUserQuestionToolName = "ask_user_question"

// Length caps for model-supplied text. The schema advertises these via
// maxLength, but a schema is advice the adapter may not enforce — the
// TUI's tab strip and option rows have a real, finite render width, so
// parseAskUserQuestionArgs truncates defensively on the way in rather
// than trusting the model to have honored the hint. headerMaxLen
// matches the "<=12 chars" contract the tab strip is built around;
// the others are generous enough for real answers while still bounding
// a single row/line in the popup.
const (
	headerMaxLen       = 12
	questionTextMaxLen = 200
	optionLabelMaxLen  = 60
	optionDescMaxLen   = 160
)

// QuestionOption is one selectable choice for a Question. Recommended
// marks the option a non-interactive consumer (oneshot with no human
// attached) should auto-pick, and the option the TUI pre-selects when
// a question tab is first visited. At most one option per question
// should set it — parseAskUserQuestionArgs doesn't enforce that (a
// model naming two is a minor prompt-following slip, not a reason to
// refuse the whole call), but callers that consume Recommended for
// auto-answering must treat "more than one" as "no safe default."
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// Question is one multiple-choice question in an ask_user_question
// call. Header is the short (<=12 char) chip shown as the tab title
// when a call carries more than one question; Question is the full
// text shown as the picker's description line. MultiSelect allows
// choosing more than one option; every question additionally gets an
// automatic free-text "Other" option that the schema never lists
// explicitly — the TUI appends it, and a plain-text answer against
// that row is carried back as FreeText on QuestionSelection.
type Question struct {
	Header      string           `json:"header"`
	Question    string           `json:"question"`
	MultiSelect bool             `json:"multi_select,omitempty"`
	Options     []QuestionOption `json:"options"`
}

// QuestionSelection is one question's resolved answer. Labels holds
// the chosen option label(s) — normally exactly one unless the
// question was MultiSelect. FreeText is set when the user answered
// through the automatic "Other" row. Multi-select answers may carry
// both Labels and FreeText when the user chose listed options plus a
// custom value.
type QuestionSelection struct {
	Header   string
	Labels   []string
	FreeText string
}

// QuestionAnswer is the reply payload threaded back through
// QuestionNeeded.Reply. The consumer (TUI key handler, oneshot's
// recommended-default fallback, ACP's fail-closed path) populates
// this BEFORE sending the correlated Decision on the shared decisions
// channel — Execute only reads it after that receive, so the
// channel send is the synchronization point (no separate lock
// needed), the same pattern EnterPlanModeTool uses for PlanModeState.
type QuestionAnswer struct {
	Selections []QuestionSelection
	Cancelled  bool
}

// AskUserQuestionTool lets the model pause a turn to ask the user one
// to four structured multiple-choice questions instead of ending the
// turn with a prose question it can't branch on. Execute does the
// full interactive round trip itself: it emits QuestionNeeded on the
// parent loop's events channel (recovered from ctx via ParentEvents,
// attached by executeToolCallImpl for every tool call) and blocks on
// the parent's decisions channel (ParentDecisions) for the resolved
// answer. RequiresApproval is always false — the interactive exchange
// IS the tool's function, not a mutation needing a permission-style
// approval card, so yolo/auto-mode/permissions-Allow never block it
// (see the mode-priority switch in executeToolCallImpl and the small
// carve-out there that keeps auto/yolo from logging a misleading
// "auto-approved" line for this tool).
//
// Excluded from every subagent and dispatch worker registry
// (buildChildRegistry, buildWorktreeChildRegistry) — an unattended
// child has no human to answer. In plan mode it's gated by
// PlanModeGate to fire only once the plan file has real content
// (parseAskUserQuestionArgs still validates the call shape either
// way; the gate is a policy check in the loop, not in Execute).
type AskUserQuestionTool struct{}

func (t *AskUserQuestionTool) Name() string { return AskUserQuestionToolName }

func (t *AskUserQuestionTool) Description() string {
	return "Ask the user one to four structured multiple-choice questions and get back a machine-checkable answer, instead of ending the turn with a prose question. " +
		"Use this when you're genuinely blocked on a decision only the user can make — not for trivia you can reasonably assume. " +
		"Each question needs a short header (<=12 chars, shown as a tab when there's more than one question), the full question text, and 2-4 options (each with a label and an optional one-line description). " +
		"Mark at most one option per question recommended:true — it's what a non-interactive session auto-picks and what the interactive picker pre-selects. " +
		"An automatic free-text \"Other\" choice is always offered in addition to your listed options; you don't need to add it yourself. " +
		"Set multi_select:true on a question where more than one option can apply. " +
		"Unavailable in plan mode until you've investigated and written a real plan to the plan file — investigate and draft first, ask what's still ambiguous last. " +
		"Not available to subagents or dispatch workers, which run unattended."
}

func (t *AskUserQuestionTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": 4,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"header":       map[string]any{"type": "string", "maxLength": headerMaxLen, "description": "Short label, <=12 characters, shown as the tab/chip title when there is more than one question."},
						"question":     map[string]any{"type": "string", "maxLength": questionTextMaxLen, "description": "The full question text shown to the user."},
						"multi_select": map[string]any{"type": "boolean", "description": "Allow selecting more than one option for this question."},
						"options": map[string]any{
							"type":     "array",
							"minItems": 2,
							"maxItems": 4,
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label":       map[string]any{"type": "string", "maxLength": optionLabelMaxLen},
									"description": map[string]any{"type": "string", "maxLength": optionDescMaxLen},
									"recommended": map[string]any{"type": "boolean", "description": "Mark this the safe default — used when no human is available to answer (e.g. one-shot mode) and pre-selected in the interactive picker. At most one per question."},
								},
								"required":             []string{"label"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"header", "question", "options"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"questions"},
		"additionalProperties": false,
	}
}

// RequiresApproval is always false: this tool never mutates anything,
// so it needs no permission-style approval card. See the type doc for
// why that also means yolo/auto-mode can't skip the interactive
// exchange itself — there's no approval step to skip.
func (t *AskUserQuestionTool) RequiresApproval(string) bool { return false }

func (t *AskUserQuestionTool) PreviewCall(argsJSON string) string {
	questions, err := parseAskUserQuestionArgs(argsJSON)
	if err != nil || len(questions) == 0 {
		return "ask_user_question"
	}
	if len(questions) == 1 {
		return "ask_user_question: " + questions[0].Header
	}
	headers := make([]string, len(questions))
	for i, q := range questions {
		headers[i] = q.Header
	}
	return fmt.Sprintf("ask_user_question: %s", strings.Join(headers, ", "))
}

// Execute parses and validates the call, then performs the full
// interactive round trip using the parent loop's events/decisions
// channels recovered from ctx. It never calls the standard approval
// flow — ParentEvents/ParentDecisions and lockApprovalGate are the
// same seam PathTrustElevationNeeded's promptForPathElevation uses,
// just invoked from inside the tool instead of from loop.go, because
// (unlike an approval) the reply here carries structured data no
// Decision value can hold.
func (t *AskUserQuestionTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	questions, err := parseAskUserQuestionArgs(argsJSON)
	if err != nil {
		return "", err
	}
	events := ParentEvents(ctx)
	decisions := ParentDecisions(ctx)
	if events == nil || decisions == nil {
		return "", errors.New("ask_user_question: no interactive session attached to this call")
	}

	unlock := lockApprovalGate(ctx)
	defer unlock()

	reply := &QuestionAnswer{}
	if err := send(ctx, events, QuestionNeeded{
		Questions: questions,
		Reply:     reply,
	}); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case d, ok := <-decisions:
		if !ok {
			return "", errors.New("agent: decisions channel closed while waiting for ask_user_question response")
		}
		if d == Deny || reply.Cancelled {
			return `{"cancelled":true}`, nil
		}
		return formatQuestionAnswer(reply), nil
	}
}

// parseAskUserQuestionArgs decodes and validates the tool call args.
// Shared by Execute and (in internal/oneshot) the recommended-default
// scan, so both paths agree on what a well-formed call looks like.
func parseAskUserQuestionArgs(argsJSON string) ([]Question, error) {
	var args struct {
		Questions []Question `json:"questions"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil, fmt.Errorf("ask_user_question: invalid arguments: %w", err)
	}
	if len(args.Questions) == 0 {
		return nil, errors.New("ask_user_question: at least one question is required")
	}
	if len(args.Questions) > 4 {
		return nil, errors.New("ask_user_question: at most four questions are allowed")
	}
	for i, q := range args.Questions {
		if strings.TrimSpace(q.Header) == "" {
			return nil, fmt.Errorf("ask_user_question: question %d is missing a header", i+1)
		}
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("ask_user_question: question %d is missing question text", i+1)
		}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return nil, fmt.Errorf("ask_user_question: question %d (%s) needs 2-4 options", i+1, q.Header)
		}
		for j, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				return nil, fmt.Errorf("ask_user_question: question %d (%s) option %d is missing a label", i+1, q.Header, j+1)
			}
		}
	}
	// Enforce the advertised limits at the tool boundary. Silently
	// changing user-visible text would alter the model's question and
	// could make two distinct headers collide in the result.
	for i := range args.Questions {
		if n := len([]rune(args.Questions[i].Header)); n > headerMaxLen {
			return nil, fmt.Errorf("ask_user_question: question %d header exceeds %d characters", i+1, headerMaxLen)
		}
		if n := len([]rune(args.Questions[i].Question)); n > questionTextMaxLen {
			return nil, fmt.Errorf("ask_user_question: question %d text exceeds %d characters", i+1, questionTextMaxLen)
		}
		for j := range args.Questions[i].Options {
			if n := len([]rune(args.Questions[i].Options[j].Label)); n > optionLabelMaxLen {
				return nil, fmt.Errorf("ask_user_question: question %d option %d label exceeds %d characters", i+1, j+1, optionLabelMaxLen)
			}
			if n := len([]rune(args.Questions[i].Options[j].Description)); n > optionDescMaxLen {
				return nil, fmt.Errorf("ask_user_question: question %d option %d description exceeds %d characters", i+1, j+1, optionDescMaxLen)
			}
		}
	}
	seenHeaders := make(map[string]struct{}, len(args.Questions))
	for i, q := range args.Questions {
		header := strings.TrimSpace(q.Header)
		if _, exists := seenHeaders[header]; exists {
			return nil, fmt.Errorf("ask_user_question: question %d reuses header %q; headers must be unique", i+1, header)
		}
		seenHeaders[header] = struct{}{}
	}
	return args.Questions, nil
}

// RecommendedIndex returns the index of this question's sole
// recommended option, or (_, false) when zero or more than one option
// is marked recommended — the "no safe default" case every consumer
// (oneshot's fallback, the TUI's pre-selection) must treat identically
// to "not marked at all": an ambiguous recommendation is not a
// recommendation.
func (q Question) RecommendedIndex() (int, bool) {
	idx, count := -1, 0
	for i, o := range q.Options {
		if o.Recommended {
			idx = i
			count++
		}
	}
	if count != 1 {
		return -1, false
	}
	return idx, true
}

// RecommendedOnly returns this question's sole recommended option
// (see RecommendedIndex).
func (q Question) RecommendedOnly() (QuestionOption, bool) {
	idx, ok := q.RecommendedIndex()
	if !ok {
		return QuestionOption{}, false
	}
	return q.Options[idx], true
}

// formatQuestionAnswer renders the resolved answer as the tool result
// string the model sees: a JSON object keyed by each unique question
// header. Multi-select answers that include both listed options and
// free text use {"options": [...], "other": "..."} so neither part
// is lost.
func formatQuestionAnswer(reply *QuestionAnswer) string {
	answers := make(map[string]any, len(reply.Selections))
	order := make([]string, 0, len(reply.Selections))
	for _, sel := range reply.Selections {
		order = append(order, sel.Header)
			switch {
			case sel.FreeText != "" && len(sel.Labels) > 0:
				answers[sel.Header] = map[string]any{"options": sel.Labels, "other": sel.FreeText}
			case sel.FreeText != "":
				answers[sel.Header] = sel.FreeText
			case len(sel.Labels) == 1:
				answers[sel.Header] = sel.Labels[0]
			default:
				answers[sel.Header] = sel.Labels
			}
	}
	out, err := json.Marshal(map[string]any{"answers": answers, "order": order})
	if err != nil {
		// Unreachable for the value shapes built above, but never
		// let a marshal failure surface as a bare Go error string to
		// the model.
		return `{"cancelled":true}`
	}
	return string(out)
}
