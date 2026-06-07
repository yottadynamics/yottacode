package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validQuestionsJSON() string {
	return `{"questions":[
		{"question":"Which auth approach should the CLI use?","header":"Auth","multiSelect":false,
		 "options":[{"label":"OAuth device flow","description":"browser hand-off"},{"label":"API key","description":"simplest"}]},
		{"question":"Where should tokens be stored?","header":"Storage","multiSelect":true,
		 "options":[{"label":"keychain","description":"OS-native"},{"label":"encrypted file","description":"portable"},{"label":"plaintext","description":"do not"}]}
	]}`
}

func TestParseUserQuestions_Bounds(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr string
	}{
		{"zero questions", `{"questions":[]}`, "needs 1-4 questions"},
		{"five questions", `{"questions":[` + strings.Repeat(`{"question":"q?","header":"h","options":[{"label":"a","description":""},{"label":"b","description":""}]},`, 4) +
			`{"question":"q?","header":"h","options":[{"label":"a","description":""},{"label":"b","description":""}]}]}`, "needs 1-4 questions"},
		{"one option", `{"questions":[{"question":"q?","header":"h","options":[{"label":"only","description":""}]}]}`, "needs 2-4 options"},
		{"five options", `{"questions":[{"question":"q?","header":"h","options":[` + strings.Repeat(`{"label":"x","description":""},`, 4) + `{"label":"y","description":""}]}]}`, "needs 2-4 options"},
		{"empty question text", `{"questions":[{"question":"  ","header":"h","options":[{"label":"a","description":""},{"label":"b","description":""}]}]}`, "empty question text"},
		{"empty option label", `{"questions":[{"question":"q?","header":"h","options":[{"label":"","description":""},{"label":"b","description":""}]}]}`, "empty label"},
		{"not json", `{"questions":`, "invalid args"},
		{"valid two questions", validQuestionsJSON(), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseUserQuestions(tc.json)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseUserQuestions: unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseUserQuestions error = %v; want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseUserQuestions_HeaderClampAndFallback(t *testing.T) {
	qs, err := ParseUserQuestions(`{"questions":[
		{"question":"q1?","header":"ThisHeaderIsWayTooLong","options":[{"label":"a","description":""},{"label":"b","description":""}]},
		{"question":"q2?","header":"","options":[{"label":"a","description":""},{"label":"b","description":""}]}
	]}`)
	if err != nil {
		t.Fatalf("ParseUserQuestions: %v", err)
	}
	if got := qs[0].Header; got != "ThisHeaderIs" {
		t.Errorf("long header clamped to %q; want %q (12 runes)", got, "ThisHeaderIs")
	}
	if got := qs[1].Header; got != "Q2" {
		t.Errorf("empty header fell back to %q; want Q2", got)
	}
}

// The questionnaire is the tool's function, not a permission gate:
// RequiresApproval=false is what lets the call pass PlanModeGate and
// keeps auto/yolo (which only skip approvals) from bypassing the UI.
// And it must NOT be ParallelSafe — the serial path guarantees no
// other modal competes for the single TUI surface.
func TestAskUserQuestion_RequiresApprovalFalseAndSerial(t *testing.T) {
	tool := &AskUserQuestionTool{}
	if tool.RequiresApproval("{}") {
		t.Errorf("RequiresApproval = true; want false")
	}
	if _, ok := any(tool).(ParallelSafeTool); ok {
		t.Errorf("AskUserQuestionTool implements ParallelSafeTool; it must stay serial")
	}
}

func TestAskUserQuestion_PreviewCall(t *testing.T) {
	tool := &AskUserQuestionTool{}
	got := tool.PreviewCall(validQuestionsJSON())
	want := "ask_user_question(2 questions: Auth, Storage)"
	if got != want {
		t.Errorf("PreviewCall = %q; want %q", got, want)
	}
	if got := tool.PreviewCall(`{"bad`); got != "ask_user_question(...)" {
		t.Errorf("PreviewCall on bad args = %q; want fallback", got)
	}
}

// Without the events + answers ctx seams (oneshot, subagent child
// loops, mis-wiring) the tool must return an instructive error, never
// hang.
func TestAskUserQuestion_NoUIChannel_ReturnsGuidance(t *testing.T) {
	tool := &AskUserQuestionTool{}
	_, err := tool.Execute(context.Background(), validQuestionsJSON())
	if err == nil || !strings.Contains(err.Error(), "best judgment") {
		t.Fatalf("Execute without ctx seams = %v; want instructive 'best judgment' error", err)
	}
}

func TestAskUserQuestion_RoundTrip(t *testing.T) {
	events := make(chan Event, 1)
	answers := make(chan QuestionnaireAnswers, 1)
	ctx := WithQuestionnaireAnswers(WithParentEvents(context.Background(), events), answers)

	done := make(chan struct{})
	var out string
	var execErr error
	go func() {
		defer close(done)
		out, execErr = (&AskUserQuestionTool{}).Execute(ctx, validQuestionsJSON())
	}()

	var req UserQuestionsNeeded
	select {
	case ev := <-events:
		var ok bool
		req, ok = ev.(UserQuestionsNeeded)
		if !ok {
			t.Fatalf("event = %T; want UserQuestionsNeeded", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no UserQuestionsNeeded event emitted")
	}
	if len(req.Questions) != 2 || req.Questions[0].Header != "Auth" || !req.Questions[1].MultiSelect {
		t.Fatalf("event payload = %+v; want 2 validated questions (Auth, Storage/multi)", req.Questions)
	}

	answers <- QuestionnaireAnswers{Answers: []QuestionAnswer{
		{Question: req.Questions[0].Question, Header: "Auth", Selected: []string{"OAuth device flow"}},
		{Question: req.Questions[1].Question, Header: "Storage", Selected: []string{"keychain", "encrypted file"}},
	}}
	<-done
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	for _, want := range []string{
		"User answered your questions:",
		"Q: Which auth approach should the CLI use?",
		"A: OAuth device flow",
		"Q: Where should tokens be stored?",
		"A: keychain; encrypted file",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %q\nresult:\n%s", want, out)
		}
	}
}

func TestAskUserQuestion_OtherAnswerFormatting(t *testing.T) {
	qs, err := ParseUserQuestions(validQuestionsJSON())
	if err != nil {
		t.Fatalf("ParseUserQuestions: %v", err)
	}
	out := FormatQuestionnaireResult(qs, QuestionnaireAnswers{Answers: []QuestionAnswer{
		{Question: qs[0].Question, Header: "Auth", Selected: []string{"keep the legacy prefix"}, IsOther: true},
	}})
	if !strings.Contains(out, "A (other): keep the legacy prefix") {
		t.Errorf("result missing free-text 'A (other):' line:\n%s", out)
	}
	// Second question got no answer entry — must degrade, not panic.
	if !strings.Contains(out, "A: (no answer)") {
		t.Errorf("result missing '(no answer)' for unanswered question:\n%s", out)
	}
}

func TestAskUserQuestion_Declined(t *testing.T) {
	events := make(chan Event, 1)
	answers := make(chan QuestionnaireAnswers, 1)
	answers <- QuestionnaireAnswers{Declined: true}
	ctx := WithQuestionnaireAnswers(WithParentEvents(context.Background(), events), answers)
	out, err := (&AskUserQuestionTool{}).Execute(ctx, validQuestionsJSON())
	if err != nil {
		t.Fatalf("Execute: %v — decline is a normal result, not an error", err)
	}
	for _, want := range []string{"declined", "best judgment", "do not re-ask"} {
		if !strings.Contains(out, want) {
			t.Errorf("declined result missing %q:\n%s", want, out)
		}
	}
}

// Cancelling the turn while the questionnaire is up must unwind the
// blocked Execute with ctx.Err so the loop's interrupt path engages.
func TestAskUserQuestion_CtxCancel(t *testing.T) {
	events := make(chan Event, 1)
	answers := make(chan QuestionnaireAnswers) // never fed
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithQuestionnaireAnswers(WithParentEvents(ctx, events), answers)

	done := make(chan error, 1)
	go func() {
		_, err := (&AskUserQuestionTool{}).Execute(ctx, validQuestionsJSON())
		done <- err
	}()
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatalf("no UserQuestionsNeeded event emitted")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || err != context.Canceled {
			t.Fatalf("Execute after cancel = %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Execute did not unwind on ctx cancel")
	}
}

// In plan mode the questionnaire is an END-of-planning surface:
// blocked until the plan file has real content (the "investigate →
// draft → ask → exit_plan_mode" order), reachable once it does.
func TestPlanModeGate_AskUserQuestionOnlyAfterPlanDrafted(t *testing.T) {
	tool := &AskUserQuestionTool{}

	// No plan file resolved yet (session opened with a bare /plan).
	msg, blocked := PlanModeGate(tool, "{}", "")
	if !blocked || !strings.Contains(msg, "END of a planning session") {
		t.Fatalf("empty planFile: blocked=%v msg=%q; want blocked with end-of-planning guidance", blocked, msg)
	}

	// Plan file named but never written.
	missing := filepath.Join(t.TempDir(), "plan.md")
	if msg, blocked = PlanModeGate(tool, "{}", missing); !blocked {
		t.Fatalf("missing plan file: gate passed; want blocked")
	}

	// Plan file exists but is blank — still "before the end".
	blank := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(blank, []byte("  \n\t\n"), 0o644); err != nil {
		t.Fatalf("write blank plan: %v", err)
	}
	if msg, blocked = PlanModeGate(tool, "{}", blank); !blocked {
		t.Fatalf("blank plan file: gate passed; want blocked")
	}
	if !strings.Contains(msg, "ask them in prose") {
		t.Errorf("guidance missing the prose-question escape hatch: %q", msg)
	}

	// Drafted plan — questions may fire now, right before exit_plan_mode.
	drafted := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(drafted, []byte("## Context\nreal plan body\n"), 0o644); err != nil {
		t.Fatalf("write drafted plan: %v", err)
	}
	if msg, blocked = PlanModeGate(tool, "{}", drafted); blocked {
		t.Fatalf("drafted plan file: gate blocked: %q; want pass", msg)
	}
}

// A plan that still carries an unresolved "Open questions" section
// must not reach the approval card — exit_plan_mode is blocked with
// the ask-first recipe until the section is resolved and removed.
// (Observed failure mode: the model parks material questions in the
// section and calls exit_plan_mode anyway; the hotkey-only card gives
// the user no way to answer them.)
func TestPlanModeGate_ExitBlockedOnOpenQuestions(t *testing.T) {
	exitTool := &ExitPlanModeTool{}
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "plan.md")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write plan: %v", err)
		}
		return p
	}

	cases := []struct {
		name    string
		body    string
		blocked bool
	}{
		{
			name:    "unresolved section blocks",
			body:    "## Context\nbody\n\n## Open questions\n1. Should cleanup also happen on oneshot exit?\n2. Add a prune fallback?\n",
			blocked: true,
		},
		{
			name:    "case-insensitive heading blocks",
			body:    "## Context\nbody\n\n### open Questions\n- anything\n",
			blocked: true,
		},
		{
			name:    "empty section passes",
			body:    "## Context\nbody\n\n## Open questions\n\n## Verification\ngo test ./...\n",
			blocked: false,
		},
		{
			name:    "no section passes",
			body:    "## Context\nbody\n\n## Verification\ngo test ./...\n",
			blocked: false,
		},
		{
			name:    "prose mention without heading passes",
			body:    "## Context\nWe resolved all open questions via ask_user_question.\n",
			blocked: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := write(t, tc.body)
			msg, blocked := PlanModeGate(exitTool, "{}", p)
			if blocked != tc.blocked {
				t.Fatalf("blocked = %v (msg %q); want %v", blocked, msg, tc.blocked)
			}
			if blocked && !strings.Contains(msg, "ask_user_question NOW") {
				t.Errorf("block message missing the ask-first recipe: %q", msg)
			}
		})
	}

	// Missing/unset plan file never blocks exit on THIS check — the
	// TUI's empty-plan auto-deny owns that path.
	if msg, blocked := PlanModeGate(exitTool, "{}", ""); blocked {
		t.Fatalf("empty planFile: exit blocked: %q; want pass-through to TUI guard", msg)
	}
}
