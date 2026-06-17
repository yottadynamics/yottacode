package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

// --- final memory turn on quit ---

// exitReadyModel returns a model that satisfies every exit-save gate:
// feature on, adapter present, idle, and above the activity bar.
func exitReadyModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.fileCfg.Memory.FinalTurnOnQuit = true
	m.userTurnsThisLaunch = exitSaveMinUserTurns
	return m
}

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

func TestExitSave_GatesQuitImmediately(t *testing.T) {
	cases := []struct {
		name string
		mut  func(m Model) Model
	}{
		{"feature off", func(m Model) Model { m.fileCfg.Memory.FinalTurnOnQuit = false; return m }},
		{"nil adapter", func(m Model) Model { m.cfg.Adapter = nil; return m }},
		{"below activity bar", func(m Model) Model { m.userTurnsThisLaunch = exitSaveMinUserTurns - 1; return m }},
		{"summarizing", func(m Model) Model { m.summarizing = true; return m }},
		{"already running", func(m Model) Model { m.exitSavePending = true; return m }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.mut(exitReadyModel(t))
			alreadyRunning := m.exitSavePending
			out, cmd := maybeStartExitSaveTurn(m)
			assertQuits(t, cmd, c.name)
			m2 := out.(Model)
			if m2.exitSavePending != alreadyRunning {
				t.Errorf("gated path must not flip exitSavePending")
			}
			if m2.turnActive {
				t.Errorf("gated path must not start a turn")
			}
		})
	}
}

func TestExitSave_StartsFinalTurnWhenWarranted(t *testing.T) {
	m := exitReadyModel(t)
	m.memoryNudgePending = true // pending reminder must be superseded, not duplicated

	out, cmd := maybeStartExitSaveTurn(m)
	if cmd == nil {
		t.Fatalf("expected the final turn's Cmd batch, got nil")
	}
	m2 := out.(Model)
	defer m2.turnCancel()

	if !m2.exitSavePending {
		t.Errorf("exitSavePending must mark the in-flight final turn")
	}
	if !m2.turnActive {
		t.Errorf("the final memory turn must be a real turn (turnActive)")
	}
	last := m2.sess.Messages[len(m2.sess.Messages)-1]
	if last.Content != exitSavePrompt {
		t.Errorf("final turn must carry exitSavePrompt verbatim; got %q", last.Content)
	}
	transcript := m2.transcript.String()
	if !strings.Contains(transcript, exitSaveDisplayLabel) {
		t.Errorf("transcript must show the compact display label; got %q", transcript)
	}
	if strings.Contains(transcript, "memory_forget any now known") {
		t.Errorf("exitSavePrompt body must not render in the transcript")
	}
}

func TestExitSave_TurnEndCompletesQuit(t *testing.T) {
	m := newTestModel(t)
	m.exitSavePending = true
	_, cmd := applyMsg(m, turnEndedMsg{})
	assertQuits(t, cmd, "turn end with exitSavePending")
}

func TestExitSave_CtrlDIdleRoutesThroughExitSave(t *testing.T) {
	m := exitReadyModel(t)
	out, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatalf("Ctrl+D should produce a Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); ok {
		t.Fatalf("Ctrl+D on an exit-ready session must start the final memory turn, not quit instantly")
	}
	if !out.exitSavePending {
		t.Errorf("Ctrl+D must mark the final turn pending")
	}
	if out.turnCancel != nil {
		defer out.turnCancel()
	}
}

func TestExitSave_CtrlCAlwaysQuitsImmediately(t *testing.T) {
	m := exitReadyModel(t)
	out, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	assertQuits(t, cmd, "idle Ctrl+C")
	if out.exitSavePending {
		t.Errorf("Ctrl+C must never start the final memory turn")
	}
}

func TestExitSave_CtrlDMidTurnHardQuits(t *testing.T) {
	m := exitReadyModel(t)
	m.turnActive = true // e.g. the final memory turn itself is running
	_, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlD})
	assertQuits(t, cmd, "mid-turn Ctrl+D")
}
