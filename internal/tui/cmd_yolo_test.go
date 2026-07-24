package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// newYoloTestModel mirrors newPlanModeTestModel but does not pre-arm
// plan mode — the entry tests want a clean overlay state. Reuses the
// same wiring (registry with the three write tools, real PlanMode /
// AutoMode / YoloMode state pointers) so /yolo can mutate
// WriteOpts and exit auto mode exactly as it does in production.
func newYoloTestModel(t *testing.T) (Model, *agent.YoloModeState) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", t.TempDir())
	m, _ := newPlanModeTestModel(t) // wires registry + all three state pointers
	return m, m.cfg.YoloMode
}

// cmdYolo toggles the overlay on. The shared YoloModeState pointer
// flips true, and the entry banner surfaces the "yolo mode active"
// wording (not the old "permissions bypass" phrasing) so the user
// sees the same label inside the TUI that they passed on the CLI.
func TestCmdYolo_EntersAndPrintsBanner(t *testing.T) {
	m, yoloMode := newYoloTestModel(t)
	if yoloMode.IsActive() {
		t.Fatalf("fixture: yolo mode should start off")
	}
	m, _ = cmdYolo(m, nil)
	if !yoloMode.IsActive() {
		t.Errorf("cmdYolo should activate yolo mode")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "yolo mode active") {
		t.Errorf("expected 'yolo mode active' banner; got %q", out)
	}
	if strings.Contains(out, "permissions bypass") {
		t.Errorf("banner should use 'yolo mode', not the old 'permissions bypass' wording; got %q", out)
	}
}

// cmdYolo toggles the overlay OFF when already on — the escape hatch
// the --yolo startup flag never had. The state pointer flips false and
// the exit banner surfaces an "exited" line so the user sees the
// overlay come down.
func TestCmdYolo_TogglesOffWhenActive(t *testing.T) {
	m, yoloMode := newYoloTestModel(t)
	// Enter via the producer path (mirrors /yolo's on-half) so the
	// entry banner is already in the transcript; then toggle off with
	// the slash handler.
	m = enterYoloMode(m)
	if !yoloMode.IsActive() {
		t.Fatalf("enterYoloMode should have armed the overlay")
	}
	m, _ = cmdYolo(m, nil)
	if yoloMode.IsActive() {
		t.Errorf("cmdYolo should deactivate yolo mode when already active")
	}
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "yolo mode exited") {
		t.Errorf("expected 'yolo mode exited' line on toggle-off; got %q", out)
	}
}

// Idempotent at the off boundary: calling cmdYolo when the overlay
// is already off just re-enters — it does not print an exit line.
// Mirrors how /plan's toggle behaves at its own off boundary.
func TestCmdYolo_IdempotentAtOffBoundary(t *testing.T) {
	m, yoloMode := newYoloTestModel(t)
	if yoloMode.IsActive() {
		t.Fatalf("fixture: yolo mode should start off")
	}
	m, _ = cmdYolo(m, nil)
	if !yoloMode.IsActive() {
		t.Fatalf("first /yolo should have entered the overlay")
	}
	out := stripANSI(m.transcript.String())
	if strings.Contains(out, "yolo mode exited") {
		t.Errorf("enter from off should not print an exit line; got %q", out)
	}
}

// enterYoloMode preserves orthogonality — yolo is an overlay, not a
// mode, so entering it does NOT exit auto mode. The two coexist: the
// auto banner picks up a "⚠ yolo mode" suffix and the precedence
// switch in the agent loop picks the yolo branch first. This is the
// same contract the --yolo startup path has always had; /yolo is
// just an additional entry point into it.
func TestEnterYoloMode_DoesNotExitAutoMode(t *testing.T) {
	m, _ := newYoloTestModel(t)
	autoMode := m.cfg.AutoMode
	if autoMode == nil {
		t.Fatalf("fixture: auto mode state pointer must be wired")
	}
	// Arm auto mode the way Shift+Tab does.
	autoMode.Active.Store(true)
	if !autoMode.IsActive() {
		t.Fatalf("auto mode should be active after arming")
	}
	m = enterYoloMode(m)
	if !autoMode.IsActive() {
		t.Errorf("enterYoloMode should NOT exit auto mode — yolo is an orthogonal overlay")
	}
	if !m.cfg.YoloMode.IsActive() {
		t.Errorf("enterYoloMode should arm yolo mode on top of auto")
	}
	// exiting yolo mode still leaves auto mode on
	exitYoloMode(&m)
	if !autoMode.IsActive() {
		t.Errorf("exitYoloMode should NOT turn auto mode off — orthogonality runs both ways")
	}
	if m.cfg.YoloMode.IsActive() {
		t.Errorf("exitYoloMode should have turned the overlay off")
	}
}

// Nil-state guard: in a build where the YoloModeState pointer was never
// wired, /yolo must degrade to a printed notice rather than panic on the
// nil dereference. Exercises the cmd_yolo.go `state == nil` branch that the
// happy-path fixtures never reach.
func TestCmdYolo_NilStatePrintsUnavailable(t *testing.T) {
	m, _ := newYoloTestModel(t)
	m.cfg.YoloMode = nil
	m, _ = cmdYolo(m, nil)
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "not available in this build") {
		t.Errorf("nil YoloMode should print an unavailable notice; got %q", out)
	}
}

// Orthogonality with PLAN mode (the auto-mode direction is covered by
// TestEnterYoloMode_DoesNotExitAutoMode). Yolo is an overlay, so toggling
// it on then off must leave plan mode exactly as it found it — the loop's
// approval switch checks the yolo case ahead of the plan case while active,
// and hands the gate back to plan on exit.
func TestYoloMode_DoesNotTouchPlanMode(t *testing.T) {
	m, _ := newYoloTestModel(t)
	planMode := m.cfg.PlanMode
	if planMode == nil {
		t.Fatalf("fixture: plan mode state pointer must be wired")
	}
	planMode.Active.Store(true)

	m = enterYoloMode(m)
	if !planMode.IsActive() {
		t.Errorf("enterYoloMode should NOT exit plan mode — yolo is an orthogonal overlay")
	}
	exitYoloMode(&m)
	if !planMode.IsActive() {
		t.Errorf("exitYoloMode should NOT turn plan mode off — orthogonality runs both ways")
	}
}

// F1 regression (TUI side): toggling /yolo OFF must leave the overlay
// inactive so the loop's approval gate returns. This is the TUI half of the
// bypass-restore contract; the behavioral half (an inactive YoloMode with no
// BypassPermissions actually prompts) is pinned in the agent package by
// TestApproval_YoloOffRestoresPrompt. Regression target: a --yolo-launched
// session used to keep auto-approving after /yolo off because a second flag
// (cfg.BypassPermissions) stayed set; that flag no longer exists in the TUI.
func TestCmdYolo_ToggleOffLeavesOverlayInactive(t *testing.T) {
	m, yoloMode := newYoloTestModel(t)
	m = enterYoloMode(m) // the --yolo / first-/yolo shape
	if !yoloMode.IsActive() {
		t.Fatalf("enterYoloMode should have armed the overlay")
	}
	m, _ = cmdYolo(m, nil) // /yolo again → off
	if yoloMode.IsActive() {
		t.Errorf("after /yolo off the overlay must be inactive so approvals return")
	}
}

// /yolo is registered in allSlash (the slash registry parity test
// lives in cmd_plan_test.go as TestSlashYolo_Registered). Here we
// additionally pin the help blurb so a future edit doesn't silently
// drop the "deny rules still win" safety wording that the danger
// setting needs in the palette.
func TestCmdYolo_HelpBlurbMentionsDenyRules(t *testing.T) {
	c := findSlash("yolo")
	if c == nil {
		t.Fatal("/yolo should be registered in allSlash")
	}
	if !strings.Contains(c.Help, "deny rules still win") {
		t.Errorf("/yolo help blurb should mention that deny rules still win; got %q", c.Help)
	}
	if !strings.Contains(c.Help, "NO safety floor") {
		t.Errorf("/yolo help blurb should call out the lack of a safety floor; got %q", c.Help)
	}
}
