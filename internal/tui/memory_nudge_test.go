package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
)

// --- pre-compaction reminder (warn-watermark arming + consumption) ---

// watermarkTestModel returns a model whose context usage computes to
// roughly chars/4 tokens against a 1000-token window, with the warn
// threshold at 0.65 and auto-summarize disabled (threshold 1.0) so the
// warn branch is reachable in isolation.
func watermarkTestModel(t *testing.T, sessionChars int) Model {
	t.Helper()
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow: 1000,
		WarnThreshold: 0.65,
		AutoThreshold: 1.0, // 1.0 disables the auto branch entirely
	}}
	if sessionChars > 0 {
		m.sess.Messages = append(m.sess.Messages, adapter.Message{
			Role: adapter.RoleUser, Content: strings.Repeat("x", sessionChars),
		})
	}
	return m
}

func TestUpdateContextUsage_WarnCrossingArmsMemoryNudge(t *testing.T) {
	m := watermarkTestModel(t, 4000) // ~1000 tokens ≥ 65% of 1000
	if m.memoryNudgePending {
		t.Fatalf("nudge must start disarmed")
	}
	_ = m.updateContextUsage(true)
	if !m.memoryNudgePending {
		t.Errorf("crossing the warn threshold must arm the pre-compaction memory nudge")
	}
	if got := m.transcript.String(); !strings.Contains(got, "save durable memories") {
		t.Errorf("arming must emit a visible notice; transcript: %q", got)
	}
}

func TestUpdateContextUsage_BelowWarnDisarmsMemoryNudge(t *testing.T) {
	m := watermarkTestModel(t, 4000)
	_ = m.updateContextUsage(true)
	if !m.memoryNudgePending {
		t.Fatalf("precondition: nudge armed after warn crossing")
	}
	// Context shrinks back under the threshold (post-/summarize, /clear).
	m.sess.Messages = nil
	_ = m.updateContextUsage(true)
	if m.memoryNudgePending {
		t.Errorf("dropping below the warn threshold must disarm the nudge")
	}
	if m.lastWatermarkPct != 0 {
		t.Errorf("watermark must reset alongside the nudge")
	}
}

func TestUpdateContextUsage_BelowWarnNeverArms(t *testing.T) {
	m := watermarkTestModel(t, 100) // ~25 tokens, far below warn
	_ = m.updateContextUsage(true)
	if m.memoryNudgePending {
		t.Errorf("usage below the warn threshold must not arm the nudge")
	}
}

func TestStartTurn_ConsumesPendingMemoryNudge(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.memoryNudgePending = true

	out, _ := m.startTurn("hello there")
	m2 := out.(Model)
	defer m2.turnCancel()

	last := m2.sess.Messages[len(m2.sess.Messages)-1]
	if last.Role != adapter.RoleUser {
		t.Fatalf("expected user message appended, got role %q", last.Role)
	}
	want := "hello there\n\n" + preCompactionMemoryReminder
	if last.Content != want {
		t.Errorf("reminder must ride the history copy of the message;\ngot:  %q\nwant: %q", last.Content, want)
	}
	if m2.memoryNudgePending {
		t.Errorf("nudge must be consumed by the turn that carries it")
	}
	// The transcript shows the user's own words only — the reminder is
	// model-facing, not a visible chat line.
	if got := m2.transcript.String(); strings.Contains(got, "system reminder") {
		t.Errorf("reminder leaked into the rendered transcript: %q", got)
	}
}

func TestStartTurn_NoPendingNudgeLeavesMessageUntouched(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}

	out, _ := m.startTurn("plain message")
	m2 := out.(Model)
	defer m2.turnCancel()

	last := m2.sess.Messages[len(m2.sess.Messages)-1]
	if last.Content != "plain message" {
		t.Errorf("message must pass through verbatim when no nudge is pending; got %q", last.Content)
	}
}

func TestMemoryNudgeTextPinsRecallBiasAndEscapeHatch(t *testing.T) {
	for name, text := range map[string]string{
		"pre-compaction": preCompactionMemoryReminder,
		"periodic":       captureReminderPrompt,
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{
				"decisions and their rationale",
				"gotchas",
				"how things work",
				"project facts",
				"Capture anything durable",
				"nothing durable is unsaved",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("memory nudge lost recall-bias wording: missing %q in %q", want, text)
				}
			}
		})
	}
}

// --- periodic capture reminder (P2.2) ---

// captureModel returns a model with the periodic reminder set to cadence n
// and userTurnsThisLaunch positioned so the NEXT turn is number `turn`.
func captureModel(t *testing.T, n, turn int) Model {
	t.Helper()
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.fileCfg.Memory.CaptureReminderEveryTurns = n
	m.userTurnsThisLaunch = turn - 1
	return m
}

// startAndLastContent runs one turn and returns the history copy of the
// message it appended.
func startAndLastContent(t *testing.T, m Model, input string) (Model, string) {
	t.Helper()
	out, _ := m.startTurn(input)
	m2 := out.(Model)
	t.Cleanup(func() {
		if m2.turnCancel != nil {
			m2.turnCancel()
		}
	})
	return m2, m2.sess.Messages[len(m2.sess.Messages)-1].Content
}

func TestCaptureReminder_FiresOnCadence(t *testing.T) {
	// With n=6 the reminder rides turns 6, 12, 18 — and no others.
	for turn, want := range map[int]bool{
		1: false, 5: false, 6: true, 7: false, 11: false, 12: true, 18: true,
	} {
		t.Run(fmt.Sprintf("turn%d", turn), func(t *testing.T) {
			m := captureModel(t, 6, turn)
			if got := m.captureReminderDue(); got != want {
				t.Errorf("turn %d: captureReminderDue = %v, want %v", turn, got, want)
			}
		})
	}
}

func TestCaptureReminder_RidesHistoryCopyOnly(t *testing.T) {
	m, content := startAndLastContent(t, captureModel(t, 6, 6), "hello there")

	want := "hello there\n\n" + captureReminderPrompt
	if content != want {
		t.Errorf("reminder must ride the history copy;\ngot:  %q\nwant: %q", content, want)
	}
	// Model-facing only: the transcript keeps the user's own words.
	if got := m.transcript.String(); strings.Contains(got, "system reminder") {
		t.Errorf("reminder leaked into the rendered transcript: %q", got)
	}
}

func TestCaptureReminder_DisabledWhenZero(t *testing.T) {
	m := captureModel(t, 0, 6)
	if m.captureReminderDue() {
		t.Error("cadence 0 must disable the reminder entirely")
	}
	_, content := startAndLastContent(t, m, "plain message")
	if content != "plain message" {
		t.Errorf("disabled reminder must leave the message verbatim; got %q", content)
	}
}

// The pre-compaction reminder is the more urgent trigger and asks for the
// same thing, so exactly one reminder rides a message — never both.
func TestCaptureReminder_YieldsToPreCompaction(t *testing.T) {
	m := captureModel(t, 6, 6)
	m.memoryNudgePending = true

	if m.captureReminderDue() {
		t.Error("capture reminder must stand down while a pre-compaction nudge is pending")
	}

	m2, content := startAndLastContent(t, m, "hello there")
	if !strings.Contains(content, preCompactionMemoryReminder) {
		t.Errorf("pre-compaction reminder should win; got %q", content)
	}
	if strings.Contains(content, captureReminderPrompt) {
		t.Errorf("both reminders rode the same message; got %q", content)
	}
	if m2.memoryNudgePending {
		t.Error("pre-compaction nudge must still be consumed")
	}
}

func TestStartTurn_ConsumesPendingLSPSetupReminder(t *testing.T) {
	m := captureModel(t, 0, 1)
	m.pendingLSPSetupReminder = "[system reminder — not from the user] offer run_bash approval for install_command=npm install -g pyright"

	m2, content := startAndLastContent(t, m, "review app.py")

	if !strings.Contains(content, "install_command=npm install -g pyright") {
		t.Fatalf("LSP setup reminder should ride the history copy; got %q", content)
	}
	if m2.pendingLSPSetupReminder != "" {
		t.Fatalf("LSP setup reminder should be consumed, got %q", m2.pendingLSPSetupReminder)
	}
	if got := m2.transcript.String(); strings.Contains(got, "install_command=npm install") {
		t.Fatalf("LSP setup reminder leaked into transcript: %q", got)
	}
}

func TestStartTurn_AppendsLSPReminderAfterMemoryReminder(t *testing.T) {
	m := captureModel(t, 6, 6)
	m.pendingLSPSetupReminder = "[system reminder — not from the user] install_command=npm install -g pyright"
	_, content := startAndLastContent(t, m, "review app.py")
	if !strings.Contains(content, captureReminderPrompt+"\n\n"+m.pendingLSPSetupReminder) {
		t.Fatalf("LSP setup reminder should coexist after memory reminder; got %q", content)
	}
}

func TestCaptureReminder_SuppressedDuringSummarize(t *testing.T) {
	m := captureModel(t, 6, 6)
	m.summarizing = true
	if m.captureReminderDue() {
		t.Error("capture reminder must be suppressed while summarizing")
	}
}


// --- quitting never starts an AI turn ---

// assertQuits fails unless cmd resolves to tea.QuitMsg.
func assertQuits(t *testing.T, cmd tea.Cmd, context string) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("%s: expected a quit Cmd, got nil", context)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("%s: expected tea.QuitMsg", context)
	}
}

func TestQuitAndIdleCtrlDDoNotStartTurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(Model) (Model, tea.Cmd)
	}{
		{"slash quit", func(m Model) (Model, tea.Cmd) { return cmdQuit(m, nil) }},
		{"idle ctrl+d", func(m Model) (Model, tea.Cmd) {
			return applyMsg(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.cfg.Adapter = stubAdapterNoStream{}
			before := len(m.sess.Messages)
			out, cmd := tc.run(m)
			assertQuits(t, cmd, tc.name)
			if out.turnActive || len(out.sess.Messages) != before {
				t.Fatalf("quit started model work: active=%v messages=%d→%d", out.turnActive, before, len(out.sess.Messages))
			}
		})
	}
}
