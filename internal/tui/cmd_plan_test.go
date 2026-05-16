package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/session"
)

// newPlanModeTestModel mirrors newTestModel but wires a real
// PlanModeState pointer + a registry with the three write tools so
// /plan can mutate their WriteOpts. Use this for plan-mode tests.
func newPlanModeTestModel(t *testing.T) (Model, *agent.PlanModeState) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", t.TempDir())
	sess, err := session.New("test-plan", "/cwd")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	planMode := &agent.PlanModeState{}
	autoMode := &agent.AutoModeState{}
	yoloMode := &agent.YoloModeState{}
	cwd := t.TempDir()
	cwdRef := agent.NewCwdRef(cwd)
	reg := agent.NewRegistry()
	reg.Register(&agent.WriteFileTool{Cwd: cwdRef})
	reg.Register(&agent.EditFileTool{Cwd: cwdRef})
	reg.Register(&agent.ApplyDiffTool{Cwd: cwdRef})
	reg.Register(&agent.ExitPlanModeTool{})
	cfg := agent.LoopConfig{Registry: reg, MaxIterations: 5, PlanMode: planMode, AutoMode: autoMode, YoloMode: yoloMode}
	perms := permissions.LoadEmpty(cwd)
	m := New(context.Background(), Config{
		Cfg:         cfg,
		Session:     sess,
		Permissions: perms,
		ModelName:   "test-model",
		BaseURL:     "http://test/v1",
		Cwd:         cwd,
	})
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, planMode
}

func TestCmdPlan_EntersAndPrintsBanner(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	if !planMode.IsActive() {
		t.Errorf("plan mode should be active after /plan")
	}
	if planMode.PlanFile != "" {
		t.Errorf("plan file should be deferred to the first user message; got %q", planMode.PlanFile)
	}
	out := stripANSI(m.transcript.String())
	// Headline now carries the full "what to do" prompt; second line
	// is the dim exit hint.
	if !strings.Contains(out, "plan mode active") {
		t.Errorf("expected plan-mode headline in transcript; got %q", out)
	}
	if !strings.Contains(out, "describe what you'd like planned in your next message") {
		t.Errorf("expected describe prompt on headline; got %q", out)
	}
	if !strings.Contains(out, "exit with /plan or Shift+Tab") {
		t.Errorf("expected exit hint on second line; got %q", out)
	}
	if !strings.Contains(out, PlanModeIcon) {
		t.Errorf("expected plan-mode icon %q in transcript; got %q", PlanModeIcon, out)
	}
}

func TestCmdPlan_PositionalArgsIgnored(t *testing.T) {
	// /plan used to take a topic; we removed it. Args are accepted (so
	// users typing /plan describe their task don't get an "unknown args"
	// error) but ignored — the slug always derives from the first user
	// message via maybeFillPlanFile.
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, []string{"add", "caching"})
	if !planMode.IsActive() {
		t.Errorf("plan mode should activate regardless of args")
	}
	if planMode.PlanFile != "" {
		t.Errorf("positional args should NOT seed the plan file; got %q", planMode.PlanFile)
	}
}

func TestCmdPlan_ToggleExits(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	if !planMode.IsActive() {
		t.Fatalf("first /plan should enter plan mode")
	}
	maybeFillPlanFile(&m, "investigate the cache")
	first := planMode.PlanFile
	m, _ = cmdPlan(m, nil)
	if planMode.IsActive() {
		t.Errorf("second /plan should exit plan mode")
	}
	if planMode.PlanFile != "" {
		t.Errorf("plan file should be cleared on exit; got %q", planMode.PlanFile)
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "plan mode exited") {
		t.Errorf("expected exit log; got %q", out)
	}
	// Exit log mentions the file with home abbreviation, so the test
	// needs to match against the abbreviated form (~/...).
	if !strings.Contains(out, filepath.Base(first)) {
		t.Errorf("exit log should mention the previous plan file basename %q; got %q", filepath.Base(first), out)
	}
}

func TestCmdPlan_WriteToolsCarryAllowedFile(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	// Drive the deferred fill via the first user message.
	maybeFillPlanFile(&m, "my plan task")
	want := planMode.PlanFile
	if want == "" {
		t.Fatalf("test setup: plan file should be filled after the first message")
	}
	for _, name := range []string{"write_file", "edit_file", "apply_diff"} {
		t.Run(name, func(t *testing.T) {
			tool, ok := m.cfg.Registry.Get(name)
			if !ok {
				t.Fatalf("%s not registered", name)
			}
			var got string
			switch tt := tool.(type) {
			case *agent.WriteFileTool:
				got = tt.WriteOpts.PlanModeAllowedFile
			case *agent.EditFileTool:
				got = tt.WriteOpts.PlanModeAllowedFile
			case *agent.ApplyDiffTool:
				got = tt.WriteOpts.PlanModeAllowedFile
			}
			if got != want {
				t.Errorf("%s.WriteOpts.PlanModeAllowedFile = %q, want %q", name, got, want)
			}
		})
	}
	// Exit and confirm the allowance is cleared.
	m, _ = cmdPlan(m, nil)
	for _, name := range []string{"write_file", "edit_file", "apply_diff"} {
		tool, _ := m.cfg.Registry.Get(name)
		var got string
		switch tt := tool.(type) {
		case *agent.WriteFileTool:
			got = tt.WriteOpts.PlanModeAllowedFile
		case *agent.EditFileTool:
			got = tt.WriteOpts.PlanModeAllowedFile
		case *agent.ApplyDiffTool:
			got = tt.WriteOpts.PlanModeAllowedFile
		}
		if got != "" {
			t.Errorf("%s.WriteOpts.PlanModeAllowedFile should be cleared after exit; got %q", name, got)
		}
	}
}

func TestPlanModeBanner_RendersInView(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "plan task")
	view := stripANSI(m.View())
	if !strings.Contains(view, "plan mode") {
		t.Errorf("plan-mode banner should appear above cmdline; got %q", view)
	}
}

func TestPlanModeBanner_HiddenWhenInactive(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	view := stripANSI(m.View())
	if strings.Contains(view, "plan mode") {
		t.Errorf("banner should not appear before /plan; got %q", view)
	}
}

func TestPlanModeBanner_NoSlugYet(t *testing.T) {
	// Pre-slug, the banner should NOT carry an "awaiting your
	// message" hint — that text was redundant with the entry log
	// printed right above. The banner just reads `◈ plan mode`
	// until a slug resolves and we have a basename to show.
	m, _ := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	view := stripANSI(m.View())
	if strings.Contains(view, "awaiting your message") {
		t.Errorf("banner should NOT carry the awaiting-message hint; got %q", view)
	}
	if !strings.Contains(view, "plan mode") {
		t.Errorf("banner should still carry the plan-mode label; got %q", view)
	}
}

// Shift+Tab now cycles through three states: normal → auto → plan →
// normal. Each press moves one stop along the cycle.
func TestShiftTab_CyclesAutoPlanNormal(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode

	// Step 1: normal → auto.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if !autoMode.IsActive() || planMode.IsActive() {
		t.Errorf("step 1 (normal → auto): autoMode=%v planMode=%v", autoMode.IsActive(), planMode.IsActive())
	}
	// Step 2: auto → plan.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if autoMode.IsActive() || !planMode.IsActive() {
		t.Errorf("step 2 (auto → plan): autoMode=%v planMode=%v", autoMode.IsActive(), planMode.IsActive())
	}
	// Step 3: plan → normal.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if autoMode.IsActive() || planMode.IsActive() {
		t.Errorf("step 3 (plan → normal): autoMode=%v planMode=%v", autoMode.IsActive(), planMode.IsActive())
	}
}

func TestShiftTab_BlockedWhilePalettesOpen(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.paletteOpen {
		t.Fatalf("typing '/' should open the slash palette")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if planMode.IsActive() {
		t.Errorf("Shift+Tab should not toggle plan mode while the slash palette is open")
	}
}

func TestMaybeFillPlanFile_DeferredSlug(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	if planMode.PlanFile != "" {
		t.Fatalf("test setup: plan file should be empty before the first message")
	}
	maybeFillPlanFile(&m, "Investigate the cache layer")
	if planMode.PlanFile == "" {
		t.Errorf("first user message should have filled the plan file")
	}
	if !strings.Contains(planMode.PlanFile, "investigate-the-cache-layer") {
		t.Errorf("plan file %q should embed kebab slug derived from the message", planMode.PlanFile)
	}
	tool, _ := m.cfg.Registry.Get("write_file")
	if tool.(*agent.WriteFileTool).WriteOpts.PlanModeAllowedFile != planMode.PlanFile {
		t.Errorf("write_file allowance not applied after deferred slug fill")
	}
}

func TestExitPlanModeApprovalCard_RendersFromPlanFile(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "investigate")
	if err := os.MkdirAll(filepath.Dir(planMode.PlanFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planMode.PlanFile, []byte("# Plan\n\nDo the thing."), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m, _ = m.handleAgentEventTea(agent.ApprovalNeeded{
		ToolName: "exit_plan_mode",
		Preview:  "exit_plan_mode (reading plan from file)",
		ArgsJSON: `{}`,
	})
	if !m.awaitingApproval {
		t.Fatalf("ApprovalNeeded for exit_plan_mode should set awaitingApproval")
	}
	// The decision box (in View) carries only the hotkeys and title.
	view := stripANSI(m.View())
	for _, want := range []string{"Approve plan?", "auto-approval", "manual approval", "later", "keep planning"} {
		if !strings.Contains(view, want) {
			t.Errorf("approval card missing %q; got %q", want, view)
		}
	}
	// The plan body lives in scrollback (transcript), NOT inside the
	// box. That's the refactor: pulling the body out so it persists
	// across modal dismissal and can hold a long plan without
	// truncation.
	transcript := stripANSI(m.transcript.String())
	if !strings.Contains(transcript, "plan ready for review") {
		t.Errorf("plan body header missing from scrollback; got %q", transcript)
	}
	if !strings.Contains(transcript, "Do the thing") {
		t.Errorf("plan body missing from scrollback; got %q", transcript)
	}
	if strings.Contains(view, "Do the thing") {
		t.Errorf("plan body should NOT be inside the decision box anymore; got %q", view)
	}

	// Press [A] — should send AllowOnce and flip plan mode off.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	select {
	case d := <-m.decisions:
		if d != agent.AllowOnce {
			t.Errorf("expected AllowOnce on [A]; got %v", d)
		}
	default:
		t.Errorf("no decision sent on [A]")
	}
	if planMode.IsActive() {
		t.Errorf("plan mode should be off after [A]")
	}
}

func TestExitPlanModeApprovalCard_SaveForLater(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "investigate")
	if err := os.MkdirAll(filepath.Dir(planMode.PlanFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planMode.PlanFile, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m, _ = m.handleAgentEventTea(agent.ApprovalNeeded{
		ToolName: "exit_plan_mode",
		ArgsJSON: `{}`,
	})
	if !m.awaitingApproval {
		t.Fatalf("test setup: ApprovalNeeded should open the modal")
	}
	// Press [L] for save-for-later.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	select {
	case d := <-m.decisions:
		if d != agent.SaveForLater {
			t.Errorf("expected SaveForLater on [L]; got %v", d)
		}
	default:
		t.Errorf("no decision sent on [L]")
	}
	if planMode.IsActive() {
		t.Errorf("plan mode should be off after [L]")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "plan saved for later") {
		t.Errorf("expected save-for-later notice in scrollback; got %q", out)
	}
}

func TestExitPlanModeApprovalCard_AutoDeniesWhenFileMissing(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "investigate") // resolves a slug but the file is NOT on disk
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m, _ = m.handleAgentEventTea(agent.ApprovalNeeded{
		ToolName: "exit_plan_mode",
		ArgsJSON: `{}`,
	})
	if m.awaitingApproval {
		t.Errorf("missing plan file should auto-deny rather than open the modal")
	}
	select {
	case d := <-m.decisions:
		if d != agent.Deny {
			t.Errorf("expected Deny on missing plan; got %v", d)
		}
	default:
		t.Errorf("no decision sent for missing plan")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "refusing exit_plan_mode") {
		t.Errorf("expected refusal notice in scrollback; got %q", out)
	}
}

// handleAgentEventTea is a tiny wrapper that lets the test drive
// handleAgentEvent without going through the full waitForEvent
// machinery — the tests only care about state transitions, not the
// next-event-pump Cmd.
func (m Model) handleAgentEventTea(ev agent.Event) (Model, tea.Cmd) {
	out, cmd := m.handleAgentEvent(ev)
	return out.(Model), cmd
}

// The banner shows the plan-file basename once a slug is resolved.
// The basename is the persistent middle text — it's what users want
// at-a-glance ("which plan am I in?"). Transient agent activity is
// NOT in the banner; the live thinking row above owns that signal.
// The exit hint and full path live only in the entry log.
func TestPlanModeBanner_ShowsBasenameOnceResolved(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "hello world")
	if planMode.PlanFile == "" {
		t.Fatalf("test setup: expected plan file to be set after first message")
	}
	rendered := stripANSI(renderPlanModeBanner(computePlanBannerInfo(m), false, 200))
	if !strings.Contains(rendered, "plan mode") {
		t.Errorf("banner missing the plan-mode label; got %q", rendered)
	}
	// Basename appears in some compacted form; tail (.md) should always be there.
	if !strings.Contains(rendered, ".md") {
		t.Errorf("banner should mention the plan file basename; got %q", rendered)
	}
	if strings.Contains(rendered, "exit") {
		t.Errorf("banner should NOT include the exit hint; got %q", rendered)
	}
	if strings.Contains(rendered, "PLAN MODE") {
		t.Errorf("banner label should be lowercase; got %q", rendered)
	}
}

// Two states the banner cycles through. No activity in the banner —
// that signal is the live thinking row's job. The "no slug yet"
// case renders as just `◈ plan mode` (no middle text); the
// "slug resolved" case shows the basename.
func TestPlanModeBanner_TwoStates(t *testing.T) {
	cases := []struct {
		name        string
		info        planBannerInfo
		wantMid     string // empty means: no middle text should appear
		forbidInMid string // text that must NOT appear anywhere in the banner
	}{
		{
			name:        "no slug yet",
			info:        planBannerInfo{},
			wantMid:     "",
			forbidInMid: "awaiting your message",
		},
		{
			name:    "slug resolved",
			info:    planBannerInfo{PlanFileBasename: "my-plan-abc.md"},
			wantMid: "my-plan-abc.md",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripANSI(renderPlanModeBanner(tc.info, false, 200))
			if tc.wantMid != "" && !strings.Contains(got, tc.wantMid) {
				t.Errorf("banner should contain %q; got %q", tc.wantMid, got)
			}
			if tc.forbidInMid != "" && strings.Contains(got, tc.forbidInMid) {
				t.Errorf("banner should NOT contain %q; got %q", tc.forbidInMid, got)
			}
			if !strings.Contains(got, "plan mode") {
				t.Errorf("banner missing plan-mode label; got %q", got)
			}
			if strings.Contains(got, "exit") {
				t.Errorf("banner should NOT carry the exit hint; got %q", got)
			}
		})
	}
}

func TestCompactPlanBasename(t *testing.T) {
	short := "short-plan-abc.md"
	if got := compactPlanBasename("/tmp/" + short); got != short {
		t.Errorf("short basename should pass through; got %q", got)
	}
	long := "i-want-to-implement-the-same-command-in-claude-context-here-f3fcc291bb20dae3.md"
	got := compactPlanBasename("/tmp/" + long)
	if len(got) >= len(long) {
		t.Errorf("long basename should be shortened; got %q (len %d) from src len %d", got, len(got), len(long))
	}
	if !strings.HasSuffix(got, ".md") {
		t.Errorf("compacted basename should keep .md suffix; got %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("compacted basename should contain an ellipsis; got %q", got)
	}
}

// Yolo entry is one-way per session: enterYoloMode flips the state on
// and prints the danger banner. There is no in-TUI exit (no toggle,
// no slash command, no Shift+Tab) — recovery requires restarting
// yottacode without --dangerously-skip-permissions.
func TestEnterYoloMode_OneWay(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	yoloMode := m.cfg.YoloMode
	if yoloMode.IsActive() {
		t.Fatalf("test setup: yolo should start off")
	}
	m = enterYoloMode(m)
	if !yoloMode.IsActive() {
		t.Errorf("enterYoloMode should activate yolo")
	}
	// Idempotent: a second call leaves state untouched and prints no
	// duplicate banner.
	transcriptBefore := m.transcript.String()
	m = enterYoloMode(m)
	if !yoloMode.IsActive() {
		t.Errorf("enterYoloMode should remain active on a second call")
	}
	if m.transcript.String() != transcriptBefore {
		t.Errorf("enterYoloMode should be idempotent — no second banner")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "permissions bypass active") {
		t.Errorf("expected 'permissions bypass active' banner in transcript; got %q", out)
	}
	if strings.Contains(out, "yolo") {
		t.Errorf("entry banner should not surface the internal 'yolo' label to the user; got %q", out)
	}
	if !strings.Contains(out, "restart without --dangerously-skip-permissions") {
		t.Errorf("expected recovery hint pointing at the flag; got %q", out)
	}
}

// Yolo is an orthogonal overlay, not a mode — entering plan does NOT
// exit yolo. The underlying mode keeps its state; yolo just adds
// "skip all prompts + no cap" on top.
func TestEnterYoloMode_LeavesOtherModesOn(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	yoloMode := m.cfg.YoloMode
	m = enterYoloMode(m)
	if !yoloMode.IsActive() {
		t.Fatalf("test setup: yolo should be active")
	}
	m, _ = cmdPlan(m, nil)
	if !planMode.IsActive() {
		t.Errorf("plan mode should activate even when yolo is already on")
	}
	if !yoloMode.IsActive() {
		t.Errorf("entering plan mode should NOT exit yolo — yolo is an orthogonal flag")
	}
}

// Symmetric: turning on auto via toggleAutoMode does NOT exit yolo.
func TestToggleAutoMode_DoesNotExitYolo(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode
	yoloMode := m.cfg.YoloMode
	m = enterYoloMode(m)
	if !yoloMode.IsActive() {
		t.Fatalf("test setup: yolo should be active")
	}
	m, _ = toggleAutoMode(m)
	if !autoMode.IsActive() {
		t.Errorf("toggleAutoMode should activate auto")
	}
	if !yoloMode.IsActive() {
		t.Errorf("entering auto mode should NOT exit yolo — yolo is an orthogonal flag")
	}
}

// Yolo suffix: when both auto + yolo are on, the banner is the auto
// banner with a "⚠ yolo" suffix appended. The auto label stays
// visible (yolo is an overlay, not a replacement), but the
// safety-floor activity text is dropped because yolo overrides it
// (bash auto-allows too, so "bash prompts" would be misleading).
func TestBanner_AutoPlusYoloShowsBothLabels(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m, _ = toggleAutoMode(m)
	m = enterYoloMode(m)
	view := stripANSI(m.View())
	if !strings.Contains(view, "auto mode") {
		t.Errorf("banner should still show 'auto mode' label; got %q", view)
	}
	if !strings.Contains(view, YoloModeIcon+" bypass") {
		t.Errorf("banner should show '⚠ bypass' suffix; got %q", view)
	}
	// Bypass overrides the safety floor, so the auto banner's
	// "bash & commits prompt" claim is misleading and should be
	// suppressed.
	if strings.Contains(view, "bash & commits prompt") {
		t.Errorf("auto banner's safety-floor text should be suppressed when bypass overrides it; got %q", view)
	}
}

// Plan + yolo: same idea — plan banner with a "⚠ yolo" suffix. The
// plan-file basename / awaiting-message stays.
func TestBanner_PlanPlusYoloShowsBothLabels(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	// Simulate the user having submitted their first plan-mode message,
	// which is when the persistent plan banner first appears (PlanFile
	// non-empty is the gate — see model.go's banner switch).
	m.cfg.PlanMode.PlanFile = "/tmp/plan-test.md"
	m = enterYoloMode(m)
	view := stripANSI(m.View())
	if !strings.Contains(view, "plan mode") {
		t.Errorf("banner should still show 'plan mode' label; got %q", view)
	}
	if !strings.Contains(view, YoloModeIcon+" bypass") {
		t.Errorf("banner should show '⚠ bypass' suffix; got %q", view)
	}
}

// Standalone bypass banner appears when the overlay is on AND no mode
// is active. Format: "⚠ permissions bypass · all tools auto-allow ·
// no iteration cap" (a mode-less overlay, not a mode itself — so no
// "mode" label).
func TestYoloStandaloneBanner_RendersWhenAlone(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m = enterYoloMode(m)
	view := stripANSI(m.View())
	if !strings.Contains(view, YoloModeIcon+" permissions bypass") {
		t.Errorf("standalone bypass banner missing icon + label; got %q", view)
	}
	if strings.Contains(view, "yolo") {
		t.Errorf("standalone bypass banner should not surface the internal 'yolo' label; got %q", view)
	}
	if strings.Contains(view, "permissions bypass mode") {
		t.Errorf("standalone bypass banner should not say 'mode' — it's an overlay; got %q", view)
	}
	if !strings.Contains(view, "no iteration cap") {
		t.Errorf("standalone bypass banner should call out 'no iteration cap'; got %q", view)
	}
}

// toggleAutoMode (called by Shift+Tab and the --permission-mode auto
// startup flag) flips auto on/off. Mutually exclusive with plan.
func TestToggleAutoMode_Toggles(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode
	if autoMode.IsActive() {
		t.Fatalf("test setup: auto should start off")
	}
	m, _ = toggleAutoMode(m)
	if !autoMode.IsActive() {
		t.Errorf("toggleAutoMode should activate auto mode")
	}
	m, _ = toggleAutoMode(m)
	if autoMode.IsActive() {
		t.Errorf("second toggleAutoMode should exit auto mode")
	}
}

// Entering auto mode from inside plan mode exits plan mode first —
// the two are mutually exclusive.
func TestToggleAutoMode_ExitsPlanModeOnEntry(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode
	m, _ = cmdPlan(m, nil)
	if !planMode.IsActive() {
		t.Fatalf("test setup: plan should be active")
	}
	m, _ = toggleAutoMode(m)
	if !autoMode.IsActive() {
		t.Errorf("toggleAutoMode should activate auto mode")
	}
	if planMode.IsActive() {
		t.Errorf("toggleAutoMode should exit plan mode (mutually exclusive)")
	}
}

// Auto-mode banner renders above the cmdline while active.
func TestAutoModeBanner_RendersWhenActive(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m, _ = toggleAutoMode(m)
	view := stripANSI(m.View())
	if !strings.Contains(view, "auto mode") {
		t.Errorf("auto-mode banner should appear when active; got %q", view)
	}
	if !strings.Contains(view, AutoModeIcon) {
		t.Errorf("auto-mode banner missing icon; got %q", view)
	}
}

// rebuildTranscript replays a session's prior tool calls into
// scrollback as full ╭/│/╰ tool cards — same shape live execution
// emits — so a resumed session reads like the live one. write_file
// gets the live two-card stack reproduced: a body card (header +
// highlighted body + approved/denied footer) plus the post-execution
// summary card.
func TestRebuildTranscript_RendersWriteFileTwoCardStack(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m.width = 100
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "do the thing"},
		{Role: adapter.RoleAssistant, Content: "on it", ToolCalls: []adapter.ToolCall{
			{ID: "1", Name: "write_file", ArgsJSON: `{"path":"hello.go","content":"package main\n\nfunc main() {}\n"}`},
		}},
		{Role: adapter.RoleTool, ToolCallID: "1", Content: "wrote 28 bytes to /tmp/hello.go"},
	}
	rebuildTranscript(&m)
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "do the thing") {
		t.Errorf("rebuild should replay the user message; got %q", out)
	}
	// Body card: header with size meta, highlighted body, approved footer.
	if !strings.Contains(out, "(29 bytes · 4 lines)") {
		t.Errorf("body card header should include byte/line meta; got %q", out)
	}
	if !strings.Contains(out, "package main") || !strings.Contains(out, "func main()") {
		t.Errorf("body card should include the full file content; got %q", out)
	}
	if !strings.Contains(out, "╰ approved") {
		t.Errorf("body card footer should read 'approved' on a successful write; got %q", out)
	}
	// Summary card: separate ╭ Write(...) / ╰ wrote N bytes block.
	if strings.Count(out, "╭ Write(hello.go)") < 2 {
		t.Errorf("rebuild should emit both a body card and a summary card; got %q", out)
	}
	if !strings.Contains(out, "╰ wrote 28 bytes") {
		t.Errorf("summary card footer should carry the persisted result; got %q", out)
	}
	if strings.Contains(out, "wrote 28 bytes to") {
		t.Errorf("summary footer should strip the redundant 'to <path>' tail; got %q", out)
	}
	if strings.Contains(out, "▸ write_file") {
		t.Errorf("rebuild must no longer emit the old one-line ▸ form; got %q", out)
	}
}

// A denied write_file call persists its tool result as "denied by
// user". The body card on replay should still show the proposed body
// (so the user remembers what they rejected) with a "denied" footer;
// the summary card carries the raw "denied by user" string.
func TestRebuildTranscript_WriteFileDenied(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m.width = 100
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{ID: "1", Name: "write_file", ArgsJSON: `{"path":"ttt.d","content":"sample text\n"}`},
		}},
		{Role: adapter.RoleTool, ToolCallID: "1", Content: "denied by user"},
	}
	rebuildTranscript(&m)
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "sample text") {
		t.Errorf("denied body card should still show the rejected content; got %q", out)
	}
	if !strings.Contains(out, "╰ denied") {
		t.Errorf("denied body card footer should read 'denied'; got %q", out)
	}
	if !strings.Contains(out, "╰ denied by user") {
		t.Errorf("summary card should carry the raw 'denied by user' string; got %q", out)
	}
}

// Defensive: when a session references a tool that isn't in the
// running binary's registry (renamed, removed), the rebuild must not
// crash — it should fall back to the bare "[tool] <name>(...)" line
// rather than swallowing the call.
func TestRebuildTranscript_UnknownToolFallback(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{ID: "1", Name: "vanished_tool", ArgsJSON: `{}`},
		}},
	}
	rebuildTranscript(&m)
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "[tool] vanished_tool(...)") {
		t.Errorf("rebuild should fall back to bare form for unknown tools; got %q", out)
	}
}

// Slash registry parity with Claude Code: /plan is invocable; /auto
// and /yolo are NOT. Locks the removal so a future palette edit
// doesn't accidentally re-expose them.
func TestSlashAuto_NotRegistered(t *testing.T) {
	if findSlash("auto") != nil {
		t.Errorf("/auto must not be a slash command — auto enters via Shift+Tab or --permission-mode auto")
	}
}

func TestSlashYolo_NotRegistered(t *testing.T) {
	if findSlash("yolo") != nil {
		t.Errorf("/yolo must not be a slash command — yolo enters only via --dangerously-skip-permissions at startup")
	}
}

// Plan card [A] both approves the plan AND enters auto mode for the
// implementation. The decision channel gets AllowOnce; plan mode is
// off; auto mode is on.
func TestExitPlanModeApprovalCard_AEntersAutoMode(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "investigate")
	if err := os.MkdirAll(filepath.Dir(planMode.PlanFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planMode.PlanFile, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m, _ = m.handleAgentEventTea(agent.ApprovalNeeded{
		ToolName: "exit_plan_mode",
		ArgsJSON: `{}`,
	})
	if !m.awaitingApproval {
		t.Fatalf("ApprovalNeeded for exit_plan_mode should open the modal")
	}
	// Press [A].
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	select {
	case d := <-m.decisions:
		if d != agent.AllowOnce {
			t.Errorf("expected AllowOnce on [A]; got %v", d)
		}
	default:
		t.Errorf("no decision sent on [A]")
	}
	if planMode.IsActive() {
		t.Errorf("plan mode should be off after [A]")
	}
	if !autoMode.IsActive() {
		t.Errorf("auto mode should be on after [A]")
	}
}

// Plan card [M] approves the plan but stays in manual mode — per-tool
// prompts continue. Decision is AllowOnce, plan mode is off, auto mode
// stays off.
func TestExitPlanModeApprovalCard_MIsManualApproval(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "investigate")
	if err := os.MkdirAll(filepath.Dir(planMode.PlanFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planMode.PlanFile, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m, _ = m.handleAgentEventTea(agent.ApprovalNeeded{
		ToolName: "exit_plan_mode",
		ArgsJSON: `{}`,
	})
	if !m.awaitingApproval {
		t.Fatalf("ApprovalNeeded for exit_plan_mode should open the modal")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	select {
	case d := <-m.decisions:
		if d != agent.AllowOnce {
			t.Errorf("expected AllowOnce on [M]; got %v", d)
		}
	default:
		t.Errorf("no decision sent on [M]")
	}
	if planMode.IsActive() {
		t.Errorf("plan mode should be off after [M]")
	}
	if autoMode.IsActive() {
		t.Errorf("auto mode must NOT be on after [M] — that's the whole point of the manual option")
	}
}

// The plan approval card advertises all four hotkeys: [A] auto-approval,
// [M] manual approval, [L] later, [K] keep planning.
func TestExitPlanModeApprovalCard_AdvertisesAllHotkeys(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, nil)
	maybeFillPlanFile(&m, "investigate")
	if err := os.MkdirAll(filepath.Dir(planMode.PlanFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planMode.PlanFile, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m, _ = m.handleAgentEventTea(agent.ApprovalNeeded{
		ToolName: "exit_plan_mode",
		ArgsJSON: `{}`,
	})
	view := stripANSI(m.View())
	for _, want := range []string{"auto-approval", "manual approval", "later", "keep planning"} {
		if !strings.Contains(view, want) {
			t.Errorf("approval card missing hotkey label %q; got %q", want, view)
		}
	}
}

// The /plan slash command should round-trip through the slash registry —
// users typing `/plan` at the prompt should hit cmdPlan.
func TestSlashPlan_RegisteredAndDispatches(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = m.runSlash("/plan add something")
	if !planMode.IsActive() {
		t.Errorf("/plan slash should activate plan mode")
	}
}

// /plan list opens the resume picker (or logs "no plans" when there
// are none on disk). Empty state should NOT enter plan mode.
func TestCmdPlan_ListEmptyLogsAndStaysOff(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	m, _ = cmdPlan(m, []string{"list"})
	if planMode.IsActive() {
		t.Errorf("/plan list on empty plans dir should NOT activate plan mode")
	}
	if m.plansPickerOpen {
		t.Errorf("/plan list on empty plans dir should NOT open picker")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "no plans saved yet") {
		t.Errorf("expected 'no plans saved yet' notice; got %q", out)
	}
}

// /plan list opens the picker when plans exist; Enter resumes the
// chosen plan, attaching its file to plan mode.
func TestCmdPlan_ListResumesPlan(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	// Seed two plans under the env-controlled plans dir.
	plansDir, err := agent.PlansDir()
	if err != nil {
		t.Fatalf("PlansDir: %v", err)
	}
	if err := os.MkdirAll(plansDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pathA := filepath.Join(plansDir, "older.md")
	pathB := filepath.Join(plansDir, "newer.md")
	for _, p := range []string{pathA, pathB} {
		if err := os.WriteFile(p, []byte("# plan"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Force pathB to be newer.
	now := time.Now()
	_ = os.Chtimes(pathA, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	_ = os.Chtimes(pathB, now, now)

	m, _ = cmdPlan(m, []string{"list"})
	if !m.plansPickerOpen {
		t.Fatalf("picker should be open after /plan list with plans on disk")
	}
	if got := len(m.plansPicker.plans); got != 2 {
		t.Errorf("expected 2 plans; got %d", got)
	}
	if m.plansPicker.plans[0].Slug != "newer" {
		t.Errorf("expected newest-first ordering; got %q first", m.plansPicker.plans[0].Slug)
	}

	// Press Enter to resume the highlighted (newest) plan.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !planMode.IsActive() {
		t.Errorf("plan mode should be active after resuming")
	}
	if planMode.PlanFile != pathB {
		t.Errorf("plan file should match resumed plan; got %q want %q", planMode.PlanFile, pathB)
	}
	if m.plansPickerOpen {
		t.Errorf("picker should close after Enter")
	}
	// Write tools should now carry the resumed allowance.
	wt, _ := m.cfg.Registry.Get("write_file")
	if wt.(*agent.WriteFileTool).WriteOpts.PlanModeAllowedFile != pathB {
		t.Errorf("write_file allowance not applied on resume")
	}
}

// Esc closes the picker without entering plan mode.
func TestPlansPicker_EscClosesWithoutResuming(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	plansDir, _ := agent.PlansDir()
	_ = os.MkdirAll(plansDir, 0o700)
	_ = os.WriteFile(filepath.Join(plansDir, "x.md"), []byte("x"), 0o644)

	m, _ = cmdPlan(m, []string{"list"})
	if !m.plansPickerOpen {
		t.Fatalf("test setup: picker should be open")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.plansPickerOpen {
		t.Errorf("Esc should close the picker")
	}
	if planMode.IsActive() {
		t.Errorf("Esc should not enter plan mode")
	}
}

// Avoid unused-import alarm if a future trim removes adapter usage.
var _ = adapter.RoleSystem
