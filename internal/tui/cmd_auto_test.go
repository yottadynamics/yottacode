package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// /auto jumps directly to the implementation stop in the Shift+Tab
// cycle so users can enter or exit auto mode without walking through plan/yolo.
func TestCmdAuto_TogglesAutoMode(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode

	m, _ = cmdAuto(m, nil)
	if !autoMode.IsActive() {
		t.Fatalf("/auto should enter auto mode")
	}

	m, _ = cmdAuto(m, nil)
	if autoMode.IsActive() {
		t.Fatalf("second /auto should exit auto mode")
	}
}

// /auto is part of the built-in slash registry alongside /plan and
// /yolo, so it appears in the palette and dispatches from typed input.
func TestSlashAuto_RegisteredAndDispatches(t *testing.T) {
	if findSlash("auto") == nil {
		t.Fatalf("/auto should be registered")
	}

	m, _ := newPlanModeTestModel(t)
	m, _ = m.runSlash("/auto")
	if !m.cfg.AutoMode.IsActive() {
		t.Fatalf("/auto slash should activate auto mode")
	}
}

// Shift+Tab cycles through normal, planning, bounded auto, and the
// explicit always-approve yolo stop.
func TestShiftTab_CyclesPlanAutoYoloNormal(t *testing.T) {
	m, planMode := newPlanModeTestModel(t)
	autoMode := m.cfg.AutoMode
	yoloMode := m.cfg.YoloMode

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if autoMode.IsActive() || !planMode.IsActive() || yoloMode.IsActive() {
		t.Fatalf("normal → plan got auto=%v plan=%v yolo=%v", autoMode.IsActive(), planMode.IsActive(), yoloMode.IsActive())
	}

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if !autoMode.IsActive() || planMode.IsActive() || yoloMode.IsActive() {
		t.Fatalf("plan → auto got auto=%v plan=%v yolo=%v", autoMode.IsActive(), planMode.IsActive(), yoloMode.IsActive())
	}

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if autoMode.IsActive() || planMode.IsActive() || !yoloMode.IsActive() {
		t.Fatalf("auto → yolo got auto=%v plan=%v yolo=%v", autoMode.IsActive(), planMode.IsActive(), yoloMode.IsActive())
	}

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if autoMode.IsActive() || planMode.IsActive() || yoloMode.IsActive() {
		t.Fatalf("yolo → normal got auto=%v plan=%v yolo=%v", autoMode.IsActive(), planMode.IsActive(), yoloMode.IsActive())
	}
}

func TestAutoModeEntryMentionsYoloCycleStop(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m, _ = toggleAutoMode(m)
	out := stripANSI(m.transcript.String())
	if !strings.Contains(out, "auto → yolo → normal → plan") {
		t.Fatalf("auto entry should describe the full mode cycle from auto; got %q", out)
	}
}
