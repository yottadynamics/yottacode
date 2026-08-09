package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/sandbox"
)

func TestCurrentSandboxMode(t *testing.T) {
	off := config.Config{Sandbox: config.SandboxConfig{Backend: "none"}}
	if got := currentSandboxMode(off, nil); got != sandboxModeOff {
		t.Errorf("backend=none should be sandboxModeOff, got %v", got)
	}

	on := config.Config{Sandbox: config.SandboxConfig{Backend: "podman"}}
	if got := currentSandboxMode(on, nil); got != sandboxModeRegular {
		t.Errorf("backend=podman with nil autoMode should be sandboxModeRegular, got %v", got)
	}

	auto := &agent.AutoModeState{}
	if got := currentSandboxMode(on, auto); got != sandboxModeRegular {
		t.Errorf("backend=podman with inactive autoMode should be sandboxModeRegular, got %v", got)
	}
	auto.Active.Store(true)
	if got := currentSandboxMode(on, auto); got != sandboxModeAutoAllow {
		t.Errorf("backend=podman with active autoMode should be sandboxModeAutoAllow, got %v", got)
	}
}

func TestUpdateSandboxPicker_UpDownNavigatesAndEscCloses(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.openSandboxPicker()
	if !m.sandboxPickerOpen {
		t.Fatal("openSandboxPicker should set sandboxPickerOpen")
	}
	// Default config has no [sandbox] block, so the current (and starting
	// cursor) mode is Off — the BOTTOM row in display order (AutoAllow=0,
	// Regular=1, Off=2).
	if m.sandboxPicker.cursor != sandboxModeOff {
		t.Fatalf("cursor should start on the current mode (off, by default), got %v", m.sandboxPicker.cursor)
	}

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.sandboxPicker.cursor != sandboxModeRegular {
		t.Errorf("Up from the bottom row should move to Regular, got %v", m.sandboxPicker.cursor)
	}
	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.sandboxPicker.cursor != sandboxModeAutoAllow {
		t.Errorf("Up should reach the top row (AutoAllow), got %v", m.sandboxPicker.cursor)
	}
	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.sandboxPicker.cursor != sandboxModeAutoAllow {
		t.Errorf("Up at the top row should stay put, got %v", m.sandboxPicker.cursor)
	}

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.sandboxPicker.cursor != sandboxModeRegular {
		t.Errorf("Down should move to the next row, got %v", m.sandboxPicker.cursor)
	}
	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.sandboxPicker.cursor != sandboxModeOff {
		t.Errorf("Down should move to the last row, got %v", m.sandboxPicker.cursor)
	}
	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.sandboxPicker.cursor != sandboxModeOff {
		t.Errorf("Down at the bottom row should stay put, got %v", m.sandboxPicker.cursor)
	}

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.sandboxPickerOpen {
		t.Error("Esc should close the picker")
	}
}

func TestUpdateSandboxPicker_EnterRequiresExplicitConfirmation(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m, _ = m.openSandboxPicker()
	m.sandboxPicker.cursor = sandboxModeRegular

	// First Enter only arms confirmation. It must not write config or close the
	// picker because changing command isolation is too safety-sensitive for a
	// single list-selection keypress.
	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.sandboxPickerOpen || m.sandboxPicker == nil || !m.sandboxPicker.confirming {
		t.Fatalf("first Enter should keep the picker open in confirming state: open=%v picker=%#v", m.sandboxPickerOpen, m.sandboxPicker)
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload after preview Enter: %v", err)
	}
	if reloaded.Sandbox.Backend == "podman" {
		t.Fatal("first Enter should not persist the sandbox backend")
	}

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sandboxPickerOpen {
		t.Fatal("second Enter should confirm and close the picker")
	}
	reloaded, err = config.LoadDefault()
	if err != nil {
		t.Fatalf("reload after confirm Enter: %v", err)
	}
	if reloaded.Sandbox.Backend != "podman" {
		t.Fatalf("confirmed sandbox backend = %q, want podman", reloaded.Sandbox.Backend)
	}
}

func TestUpdateSandboxPicker_EscFromConfirmationReturnsToPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.openSandboxPicker()
	m.sandboxPicker.cursor = sandboxModeRegular
	m.sandboxPicker.confirming = true

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.sandboxPickerOpen {
		t.Fatal("Esc from confirmation should return to the picker, not close it")
	}
	if m.sandboxPicker.confirming {
		t.Fatal("Esc from confirmation should clear confirming state")
	}
}

func TestUpdateSandboxPicker_EscCloseDoesNotReopenSlashPalette(t *testing.T) {
	m := newTestModel(t)
	m.openSlashPalette()
	m, _ = m.openSandboxPicker()

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.sandboxPickerOpen {
		t.Fatal("Esc should close the sandbox picker")
	}
	if m.paletteOpen {
		t.Fatal("Esc should not reopen the slash palette after closing /sandbox")
	}
}

// TestCommitSandboxMode_AutoAllowPersistsAndActivatesAutoMode is the
// end-to-end path: choosing auto-allow must persist [sandbox].backend =
// "podman" + [experimental].sandbox = true to config.toml, AND turn on
// this session's live auto mode (the "auto-allow is auto mode" mapping).
func TestCommitSandboxMode_AutoAllowPersistsAndActivatesAutoMode(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.sandboxPicker = &sandboxPickerState{}

	m, _ = commitSandboxMode(m, sandboxModeAutoAllow)

	if !m.cfg.AutoMode.IsActive() {
		t.Error("auto-allow should activate this session's live auto mode")
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Sandbox.Backend != "podman" {
		t.Errorf("Sandbox.Backend = %q, want podman", reloaded.Sandbox.Backend)
	}
	if !reloaded.Experimental["sandbox"] {
		t.Error("experimental.sandbox should be persisted true")
	}
	if err := config.Validate(reloaded); err != nil {
		t.Errorf("persisted config should validate: %v", err)
	}
}

// TestCommitSandboxMode_RegularDoesNotActivateAutoMode confirms the
// "regular permissions" row leaves live auto mode untouched (does not
// implicitly turn it on) while still persisting the podman backend.
func TestCommitSandboxMode_RegularDoesNotActivateAutoMode(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.sandboxPicker = &sandboxPickerState{}

	m, _ = commitSandboxMode(m, sandboxModeRegular)

	if m.cfg.AutoMode.IsActive() {
		t.Error("regular-permissions mode must not turn auto mode on")
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Sandbox.Backend != "podman" {
		t.Errorf("Sandbox.Backend = %q, want podman", reloaded.Sandbox.Backend)
	}
}

// TestCommitSandboxMode_BackendChoiceAlwaysSaysRestartRequired pins the
// safety-critical UX: /sandbox writes config only; it never hot-swaps the
// current RunBashTool's Sandbox. Every podman/no-sandbox selection must say a
// restart/new session is required, even when the selected backend matches the
// persisted config, because the live session may still be using the old backend.
func TestCommitSandboxMode_BackendChoiceAlwaysSaysRestartRequired(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.sandboxPicker = &sandboxPickerState{}
	m.fileCfg = config.Default()
	m.fileCfg.Sandbox.Backend = "podman"

	m, _ = commitSandboxMode(m, sandboxModeRegular)

	got := strings.Join(m.historyLines, "\n")
	if !strings.Contains(got, "restart yottacode") {
		t.Fatalf("sandbox commit should always surface restart requirement, got:\n%s", got)
	}
}

// TestCommitSandboxMode_OffTurnsAutoModeBackOff: switching to "no sandbox"
// from an auto-allow state (this picker's own `current` says the live
// auto mode is attributable to a prior auto-allow pick) must not leave a
// stale live auto mode on — the mode just chosen contradicts it.
func TestCommitSandboxMode_OffTurnsAutoModeBackOff(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.cfg.AutoMode.Active.Store(true)
	m.sandboxPicker = &sandboxPickerState{current: sandboxModeAutoAllow}

	m, _ = commitSandboxMode(m, sandboxModeOff)

	if m.cfg.AutoMode.IsActive() {
		t.Error("choosing No sandbox should turn off a live auto mode left on from a previous auto-allow pick")
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Sandbox.Backend != "none" {
		t.Errorf("Sandbox.Backend = %q, want none", reloaded.Sandbox.Backend)
	}
}

// TestCommitSandboxMode_RegularDoesNotTurnOffUnrelatedAutoMode is the
// regression this picker's `current`-tracking fixes: auto mode turned on
// for reasons that have nothing to do with sandboxing (Shift+Tab, before
// /sandbox was ever opened — current starts at sandboxModeOff, not
// sandboxModeAutoAllow) must survive picking "regular permissions."
// Silently exiting it here would be a surprising side effect of a choice
// that, per sandboxModeDescription, only concerns run_bash isolation.
func TestCommitSandboxMode_RegularDoesNotTurnOffUnrelatedAutoMode(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.cfg.AutoMode.Active.Store(true) // on for unrelated reasons, e.g. Shift+Tab
	m.sandboxPicker = &sandboxPickerState{current: sandboxModeOff}

	m, _ = commitSandboxMode(m, sandboxModeRegular)

	if !m.cfg.AutoMode.IsActive() {
		t.Error("picking regular permissions must not turn off auto mode that was on for unrelated reasons")
	}
}

func TestAnyOverlayOpen_SandboxPicker(t *testing.T) {
	m := Model{}
	if m.anyOverlayOpen() {
		t.Fatal("no overlay should be open on a zero Model")
	}
	m.sandboxPickerOpen = true
	if !m.anyOverlayOpen() {
		t.Error("sandboxPickerOpen should count as an open overlay")
	}
}

func TestRenderSandboxPicker_ShowsThreeRowsAndCurrentCheckmark(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeAutoAllow, current: sandboxModeOff, detected: true, status: sandbox.Status{Installed: true, ImagePresent: true}}
	got := renderSandboxPicker(p)
	for _, want := range []string{"Sandbox run_bash, with auto-allow", "Sandbox run_bash, with regular permissions", "No sandbox"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing row %q:\n%s", want, got)
		}
	}
}

func TestRenderSandboxPicker_ConfirmationCopy(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeRegular, confirming: true, detected: true, status: sandbox.Status{Installed: true, ImagePresent: true}}
	got := renderSandboxPicker(p)
	for _, want := range []string{"↵ confirm · esc back", "Press Enter again to persist Sandbox run_bash, with regular permissions"} {
		if !strings.Contains(got, want) {
			t.Errorf("confirmation render missing %q:\n%s", want, got)
		}
	}
}

// TestRenderSandboxPicker_ChecksBeforeDetectionArrives pins the async-
// detection fix: before sandboxDetectMsg arrives (detected stays false),
// the picker must show a "checking" state, not silently claim podman is
// missing just because the zero-value Status says Installed=false.
func TestRenderSandboxPicker_ChecksBeforeDetectionArrives(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeAutoAllow} // detected: false (zero value)
	got := renderSandboxPicker(p)
	if !strings.Contains(got, "checking podman") {
		t.Errorf("expected a checking-podman state before detection arrives:\n%s", got)
	}
	if strings.Contains(got, "podman not found on PATH") {
		t.Errorf("must not claim podman is missing before detection has actually run:\n%s", got)
	}
}

func TestRenderSandboxPicker_WarnsWhenPodmanMissing(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeAutoAllow, detected: true}
	got := renderSandboxPicker(p)
	if !strings.Contains(got, "podman not found on PATH") {
		t.Errorf("expected a podman-missing warning:\n%s", got)
	}
}

func TestRenderSandboxPicker_NoWarningWhenModeIsOff(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeOff, detected: true}
	got := renderSandboxPicker(p)
	if strings.Contains(got, "podman not found") {
		t.Errorf("no-sandbox row should not show a podman warning:\n%s", got)
	}
}

// TestOpenSandboxPicker_ReturnsAsyncDetectionCmd pins that opening the
// picker no longer blocks on sandbox.DetectStatus inline — it must
// return a non-nil tea.Cmd that performs the check off the Update
// goroutine, per the codebase's runProviderProbe/providerProbeMsg
// pattern for exactly this kind of external-process probe.
func TestOpenSandboxPicker_ReturnsAsyncDetectionCmd(t *testing.T) {
	m := newTestModel(t)
	m, cmd := m.openSandboxPicker()
	if cmd == nil {
		t.Fatal("openSandboxPicker should return a tea.Cmd to run detection asynchronously")
	}
	if m.sandboxPicker.detected {
		t.Error("detected should start false — the picker hasn't heard back from the async probe yet")
	}
	msg := cmd()
	det, ok := msg.(sandboxDetectMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want sandboxDetectMsg", msg)
	}
	m, _ = applyMsg(m, det)
	if !m.sandboxPicker.detected {
		t.Error("Update should mark detected true after receiving sandboxDetectMsg")
	}
}
