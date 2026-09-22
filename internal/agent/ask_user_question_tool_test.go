package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestAskUserQuestionTool_SchemaShape(t *testing.T) {
	tool := &AskUserQuestionTool{}
	if tool.RequiresApproval(`{}`) {
		t.Errorf("ask_user_question must never require approval")
	}
	schema := tool.Schema()
	props, _ := schema["properties"].(map[string]any)
	questions, _ := props["questions"].(map[string]any)
	if questions == nil {
		t.Fatalf("schema missing questions property; got %+v", schema)
	}
	if questions["minItems"] != 1 || questions["maxItems"] != 4 {
		t.Errorf("questions should allow 1-4 items; got %+v", questions)
	}
}

func TestParseAskUserQuestionArgs_Validation(t *testing.T) {
	valid := `{"questions":[{"header":"Auth","question":"How should we authenticate?","options":[{"label":"OAuth"},{"label":"API key"}]}]}`
	if _, err := parseAskUserQuestionArgs(valid); err != nil {
		t.Fatalf("expected valid args to parse; got %v", err)
	}

	cases := []struct {
		name string
		args string
	}{
		{"invalid json", `{not json`},
		{"zero questions", `{"questions":[]}`},
		{"five questions", `{"questions":[
			{"header":"A","question":"a?","options":[{"label":"1"},{"label":"2"}]},
			{"header":"B","question":"b?","options":[{"label":"1"},{"label":"2"}]},
			{"header":"C","question":"c?","options":[{"label":"1"},{"label":"2"}]},
			{"header":"D","question":"d?","options":[{"label":"1"},{"label":"2"}]},
			{"header":"E","question":"e?","options":[{"label":"1"},{"label":"2"}]}
		]}`},
		{"missing header", `{"questions":[{"question":"a?","options":[{"label":"1"},{"label":"2"}]}]}`},
		{"missing question text", `{"questions":[{"header":"A","options":[{"label":"1"},{"label":"2"}]}]}`},
		{"one option", `{"questions":[{"header":"A","question":"a?","options":[{"label":"1"}]}]}`},
		{"five options", `{"questions":[{"header":"A","question":"a?","options":[{"label":"1"},{"label":"2"},{"label":"3"},{"label":"4"},{"label":"5"}]}]}`},
		{"empty option label", `{"questions":[{"header":"A","question":"a?","options":[{"label":""},{"label":"2"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseAskUserQuestionArgs(tc.args); err == nil {
				t.Errorf("expected %s to fail validation", tc.name)
			}
		})
	}
}

func TestParseAskUserQuestionArgs_RejectsOverlongText(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"header", `{"questions":[{"header":"` + strings.Repeat("x", headerMaxLen+1) + `","question":"q?","options":[{"label":"1"},{"label":"2"}]}]}`},
		{"question", `{"questions":[{"header":"A","question":"` + strings.Repeat("y", questionTextMaxLen+1) + `","options":[{"label":"1"},{"label":"2"}]}]}`},
		{"label", `{"questions":[{"header":"A","question":"q?","options":[{"label":"` + strings.Repeat("z", optionLabelMaxLen+1) + `"},{"label":"2"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseAskUserQuestionArgs(tc.args); err == nil {
				t.Fatalf("expected overlong %s to fail", tc.name)
			}
		})
	}
}

func TestParseAskUserQuestionArgs_RejectsDuplicateHeaders(t *testing.T) {
	args := `{"questions":[{"header":"Auth","question":"a?","options":[{"label":"1"},{"label":"2"}]},{"header":"Auth","question":"b?","options":[{"label":"1"},{"label":"2"}]}]}`
	if _, err := parseAskUserQuestionArgs(args); err == nil {
		t.Fatal("expected duplicate headers to fail")
	}
}

func TestFormatQuestionAnswer_PreservesMultiSelectOther(t *testing.T) {
	out := formatQuestionAnswer(&QuestionAnswer{Selections: []QuestionSelection{{Header: "Notify", Labels: []string{"Slack"}, FreeText: "webhook"}}})
	if !strings.Contains(out, `"options":["Slack"]`) || !strings.Contains(out, `"other":"webhook"`) {
		t.Fatalf("combined answer lost data: %s", out)
	}
}
func TestQuestion_RecommendedIndex(t *testing.T) {
	cases := []struct {
		name    string
		options []QuestionOption
		wantOK  bool
		wantIdx int
	}{
		{"none recommended", []QuestionOption{{Label: "a"}, {Label: "b"}}, false, -1},
		{"one recommended", []QuestionOption{{Label: "a"}, {Label: "b", Recommended: true}}, true, 1},
		{"two recommended is ambiguous", []QuestionOption{{Label: "a", Recommended: true}, {Label: "b", Recommended: true}}, false, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := Question{Options: tc.options}
			idx, ok := q.RecommendedIndex()
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && idx != tc.wantIdx {
				t.Errorf("idx = %d, want %d", idx, tc.wantIdx)
			}
		})
	}
}

func TestAskUserQuestionTool_Execute_NoInteractiveSession(t *testing.T) {
	tool := &AskUserQuestionTool{}
	args := `{"questions":[{"header":"A","question":"a?","options":[{"label":"1"},{"label":"2"}]}]}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "no interactive session") {
		t.Errorf("expected a no-interactive-session error; got %v", err)
	}
}

func TestAskUserQuestionTool_Execute_ContextCancelled(t *testing.T) {
	events := make(chan Event, 4)
	decisions := make(chan Decision) // unbuffered, deliberately never fed
	ctx, cancel := context.WithCancel(context.Background())
	toolCtx := WithParentDecisions(WithParentEvents(ctx, events), decisions)

	tool := &AskUserQuestionTool{}
	args := `{"questions":[{"header":"A","question":"a?","options":[{"label":"1"},{"label":"2"}]}]}`

	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(toolCtx, args)
		done <- err
	}()

	// Drain the QuestionNeeded event so Execute reaches its select, then
	// cancel instead of ever answering.
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for QuestionNeeded")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Errorf("expected context.Canceled; got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not return after ctx cancellation")
	}
}

func TestAskUserQuestionTool_Execute_RoundTrip(t *testing.T) {
	events := make(chan Event, 4)
	decisions := make(chan Decision, 1)
	toolCtx := WithParentDecisions(WithParentEvents(context.Background(), events), decisions)

	tool := &AskUserQuestionTool{}
	args := `{"questions":[
		{"header":"Auth","question":"How should we authenticate?","options":[{"label":"OAuth"},{"label":"API key"}]},
		{"header":"Notify","question":"Which channels?","multi_select":true,"options":[{"label":"Slack"},{"label":"Email"}]}
	]}`

	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		out, err := tool.Execute(toolCtx, args)
		resultCh <- out
		errCh <- err
	}()

	var qn QuestionNeeded
	select {
	case ev := <-events:
		var ok bool
		qn, ok = ev.(QuestionNeeded)
		if !ok {
			t.Fatalf("expected QuestionNeeded event; got %T", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for QuestionNeeded")
	}
	if len(qn.Questions) != 2 {
		t.Fatalf("expected 2 questions; got %d", len(qn.Questions))
	}
	qn.Reply.Selections = []QuestionSelection{
		{Header: "Auth", Labels: []string{"OAuth"}},
		{Header: "Notify", Labels: []string{"Slack", "Email"}},
	}
	decisions <- AllowOnce

	if err := <-errCh; err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := <-resultCh
	var parsed struct {
		Answers map[string]any `json:"answers"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("result not valid JSON: %v (%s)", err, out)
	}
	if parsed.Answers["Auth"] != "OAuth" {
		t.Errorf("expected single-select answer as a bare string; got %+v", parsed.Answers["Auth"])
	}
	multi, ok := parsed.Answers["Notify"].([]any)
	if !ok || len(multi) != 2 {
		t.Errorf("expected multi-select answer as a 2-element array; got %+v", parsed.Answers["Notify"])
	}
}

func TestAskUserQuestionTool_Execute_Cancelled(t *testing.T) {
	events := make(chan Event, 4)
	decisions := make(chan Decision, 1)
	toolCtx := WithParentDecisions(WithParentEvents(context.Background(), events), decisions)

	tool := &AskUserQuestionTool{}
	args := `{"questions":[{"header":"A","question":"a?","options":[{"label":"1"},{"label":"2"}]}]}`

	resultCh := make(chan string, 1)
	go func() {
		out, _ := tool.Execute(toolCtx, args)
		resultCh <- out
	}()

	ev := <-events
	qn := ev.(QuestionNeeded)
	qn.Reply.Cancelled = true
	decisions <- Deny

	out := <-resultCh
	if !strings.Contains(out, `"cancelled":true`) {
		t.Errorf("expected a cancelled result; got %q", out)
	}
}

// --- loop integration: yolo/auto-mode must not skip the real round trip ---

func TestLoop_AskUserQuestion_YoloDoesNotShortCircuit(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     AskUserQuestionToolName,
			ArgsJSON: `{"questions":[{"header":"Auth","question":"How should we authenticate?","options":[{"label":"OAuth"},{"label":"API key"}]}]}`,
		})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&AskUserQuestionTool{})
	yoloMode := &YoloModeState{}
	yoloMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, YoloMode: yoloMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events := make(chan Event, 64)
	decisions := make(chan Decision, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Turn(context.Background(), cfg, &hist, events, decisions)
		close(events)
	}()

	var sawQuestion bool
	var sawFalseAutoApproval bool
	for ev := range events {
		switch e := ev.(type) {
		case QuestionNeeded:
			sawQuestion = true
			e.Reply.Selections = []QuestionSelection{{Header: "Auth", Labels: []string{"API key"}}}
			decisions <- AllowOnce
		case ApprovalAuto:
			if e.ToolName == AskUserQuestionToolName {
				sawFalseAutoApproval = true
			}
		}
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !sawQuestion {
		t.Fatalf("expected QuestionNeeded to fire even under yolo mode")
	}
	if sawFalseAutoApproval {
		t.Errorf("ask_user_question should not log a yolo-mode auto-approval line")
	}
	var toolResult string
	for _, m := range hist {
		if m.Role == adapter.RoleTool {
			toolResult = m.Content
		}
	}
	if !strings.Contains(toolResult, "API key") {
		t.Errorf("expected the model's tool result to carry the real answer; got %q", toolResult)
	}
}
