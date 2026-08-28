package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/sandbox"
)

func TestCurrentSandboxMode(t *testing.T) {
	off := config.Config{Sandbox: config.SandboxConfig{Backend: "none"}}
	if got := currentSandboxMode(off, false, nil); got != sandboxModeOff {
		t.Errorf("backend=none should be sandboxModeOff, got %v", got)
	}

	on := config.Config{Sandbox: config.SandboxConfig{Backend: "podman"}}
	if got := currentSandboxMode(on, false, nil); got != sandboxModeOff {
		t.Errorf("backend=podman without a live sandbox should be sandboxModeOff, got %v", got)
	}
	if got := currentSandboxMode(on, true, nil); got != sandboxModeRegular {
		t.Errorf("backend=podman with a live sandbox and nil autoMode should be sandboxModeRegular, got %v", got)
	}

	auto := &agent.AutoModeState{}
	if got := currentSandboxMode(on, true, auto); got != sandboxModeRegular {
		t.Errorf("backend=podman with inactive autoMode should be sandboxModeRegular, got %v", got)
	}
	auto.Active.Store(true)
	if got := currentSandboxMode(on, true, auto); got != sandboxModeAutoAllow {
		t.Errorf("backend=podman with active autoMode should be sandboxModeAutoAllow, got %v", got)
	}
}

func TestCurrentSandboxMode_ActiveSessionStaysActiveAfterConfigOff(t *testing.T) {
	off := config.Config{Sandbox: config.SandboxConfig{Backend: "none"}}
	if got := currentSandboxMode(off, true, nil); got != sandboxModeRegular {
		t.Fatalf("active session after persisted-off config = %v, want sandboxModeRegular", got)
	}
}

func TestSandboxPicker_ShowsConfiguredButInactiveRestartState(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.fileCfg = config.Default()
	m.fileCfg.Sandbox.Backend = "podman"
	if err := writeConfig(m.fileCfg); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	m.sandboxActive = false

	m, _ = m.openSandboxPicker()

	if m.sandboxPicker.current != sandboxModeOff {
		t.Fatalf("live sandbox mode should be off until restart, got %v", m.sandboxPicker.current)
	}
	if m.sandboxPicker.configured != sandboxModeRegular {
		t.Fatalf("configured sandbox mode should show podman persisted, got %v", m.sandboxPicker.configured)
	}
	got := renderSandboxPicker(m.sandboxPicker, 100)
	for _, want := range []string{"Configured: sandbox on", "Active: sandbox off — restart required"} {
		if !strings.Contains(got, want) {
			t.Fatalf("configured-but-inactive picker missing %q:\n%s", want, got)
		}
	}
}

func TestUpdateSandboxPicker_BlocksPodmanWhenRestartRequired(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.fileCfg = config.Default()
	m.fileCfg.Sandbox.Backend = "podman"
	if err := writeConfig(m.fileCfg); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	m.sandboxActive = false
	m, _ = m.openSandboxPicker()
	m.sandboxPicker.cursor = sandboxModeRegular

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.sandboxPickerOpen {
		t.Fatal("restart-required Podman choice should stay in the picker")
	}
	if !strings.Contains(m.sandboxPicker.note, "Restart yottacode") {
		t.Fatalf("expected restart-required note, got %q", m.sandboxPicker.note)
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

func TestUpdateSandboxPicker_EnterAppliesSelection(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m, _ = m.openSandboxPicker()
	m.sandboxPicker.cursor = sandboxModeRegular
	m.sandboxPicker.detected = true
	m.sandboxPicker.status = sandbox.Status{Installed: true, ImagePresent: true, DocumentsImagePresent: true}

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sandboxPickerOpen {
		t.Fatal("Enter should apply the selected sandbox mode and close the picker")
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload after apply Enter: %v", err)
	}
	if reloaded.Sandbox.Backend != "podman" {
		t.Fatalf("sandbox backend = %q, want podman", reloaded.Sandbox.Backend)
	}
}

func TestUpdateSandboxPicker_BlocksPodmanWhenMissing(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m, _ = m.openSandboxPicker()
	m.sandboxPicker.cursor = sandboxModeRegular
	m.sandboxPicker.detected = true
	m.sandboxPicker.status = sandbox.Status{Installed: false}

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.sandboxPickerOpen {
		t.Fatal("podman-missing choice should stay in the picker")
	}
	if !strings.Contains(m.sandboxPicker.note, "Podman is not installed") {
		t.Fatalf("expected podman-missing note, got %q", m.sandboxPicker.note)
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload after blocked apply: %v", err)
	}
	if reloaded.Sandbox.Backend == "podman" {
		t.Fatal("blocked Podman choice should not persist sandbox backend")
	}
}

func TestUpdateSandboxPicker_WaitsForPodmanDetectionBeforePersisting(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m, _ = m.openSandboxPicker()
	m.sandboxPicker.cursor = sandboxModeRegular
	m.sandboxPicker.detected = false

	m, _ = m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.sandboxPickerOpen {
		t.Fatal("pending detection choice should stay in the picker")
	}
	if !strings.Contains(m.sandboxPicker.note, "Still checking Podman availability") {
		t.Fatalf("expected pending-detection note, got %q", m.sandboxPicker.note)
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload after blocked apply: %v", err)
	}
	if reloaded.Sandbox.Backend == "podman" {
		t.Fatal("pending detection should not persist sandbox backend")
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
// low-level persistence path: choosing auto-allow persists [sandbox].backend =
// "podman" and turns on this session's live auto mode. commitSandboxMode must
// not mutate the legacy [experimental] compatibility flag on its own.
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
	if reloaded.Experimental["sandbox"] {
		t.Error("commitSandboxMode must not enable experimental.sandbox")
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
		t.Fatalf("inactive sandbox commit should surface restart requirement, got:\n%s", got)
	}
}

func TestCommitSandboxMode_ActiveSessionMentionsLazyProfiles(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.sandboxActive = true
	m.sandboxPicker = &sandboxPickerState{}

	m, _ = commitSandboxMode(m, sandboxModeRegular)

	got := strings.Join(m.historyLines, "\n")
	if !strings.Contains(got, "lazily start unused sandbox profiles") {
		t.Fatalf("active sandbox commit should mention lazy profile recovery, got:\n%s", got)
	}
}

func TestCommitSandboxMode_OffKeepsActiveSessionVisible(t *testing.T) {
	m := newTestModel(t)
	m.cfg.AutoMode = &agent.AutoModeState{}
	m.sandboxActive = true
	m.sandboxPicker = &sandboxPickerState{current: sandboxModeRegular}

	m, _ = commitSandboxMode(m, sandboxModeOff)

	if m.sandboxPicker != nil {
		t.Fatal("commitSandboxMode should close the picker")
	}
	reloaded, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Sandbox.Backend != "none" {
		t.Fatalf("Sandbox.Backend = %q, want none", reloaded.Sandbox.Backend)
	}
	got := strings.Join(m.historyLines, "\n")
	for _, want := range []string{"No sandbox", "this session keeps its existing sandbox"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in sandbox commit output, got:\n%s", want, got)
		}
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
	p := &sandboxPickerState{cursor: sandboxModeAutoAllow, current: sandboxModeOff, configured: sandboxModeOff, detected: true, status: sandbox.Status{Installed: true, ImagePresent: true}}
	got := renderSandboxPicker(p, 100)
	for _, want := range []string{"Sandbox run_bash, with auto-allow", "Sandbox run_bash, regular permissions", "No sandbox"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing row %q:\n%s", want, got)
		}
	}

	disabling := &sandboxPickerState{cursor: sandboxModeOff, current: sandboxModeRegular, configured: sandboxModeOff, sandboxActive: true, detected: true, status: sandbox.Status{Installed: true, ImagePresent: true}}
	if got := sandboxStatusLine(disabling); got != "Configured: sandbox off\nActive: sandbox on — restart required to disable" {
		t.Fatalf("disabled-but-active status = %q", got)
	}
}

func TestRenderSandboxPicker_StatusLineHighlightsCurrentMode(t *testing.T) {
	on := &sandboxPickerState{cursor: sandboxModeRegular, current: sandboxModeRegular, configured: sandboxModeRegular, sandboxActive: true, detected: true, status: sandbox.Status{Installed: true, ImagePresent: true}}
	if got := sandboxStatusLine(on); got != "Configured: sandbox on\nActive: sandbox on" {
		t.Fatalf("sandbox on status line = %q", got)
	}

	off := &sandboxPickerState{cursor: sandboxModeRegular, current: sandboxModeOff, configured: sandboxModeOff, detected: true, status: sandbox.Status{Installed: true, ImagePresent: true}}
	got := renderSandboxPicker(off, 100)
	for _, want := range []string{"Configured: sandbox off", "Active: sandbox off", "[A] Apply selection"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing concise top status %q:\n%s", want, got)
		}
	}
}

// TestRenderSandboxPicker_ChecksBeforeDetectionArrives pins the async-
// detection fix: before sandboxDetectMsg arrives (detected stays false),
// the picker must show a "checking" state, not silently claim podman is
// missing just because the zero-value Status says Installed=false.
func TestRenderSandboxPicker_ChecksBeforeDetectionArrives(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeAutoAllow} // detected: false (zero value)
	got := renderSandboxPicker(p, 100)
	if !strings.Contains(got, "checking podman") {
		t.Errorf("expected a checking-podman state before detection arrives:\n%s", got)
	}
	if strings.Contains(got, "podman not found on PATH") {
		t.Errorf("must not claim podman is missing before detection has actually run:\n%s", got)
	}
}

func TestRenderSandboxPicker_WarnsWhenPodmanMissing(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeAutoAllow, detected: true}
	got := renderSandboxPicker(p, 100)
	if !strings.Contains(got, "podman not found on PATH") {
		t.Errorf("expected a podman-missing warning:\n%s", got)
	}
}

func TestRenderSandboxPicker_NoWarningWhenModeIsOff(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeOff, detected: true}
	got := renderSandboxPicker(p, 100)
	if strings.Contains(got, "podman not found") {
		t.Errorf("no-sandbox row should not show a podman warning:\n%s", got)
	}
}

// TestRenderSandboxPicker_LongDescriptionWrapsWithinWidth is a
// regression guard: the auto-allow mode's description is a long
// sentence that used to render as one unwrapped line, bleeding past the
// popup's right border (and often past the terminal's own right edge)
// instead of wrapping like every other prose block in the picker
// family.
func TestRenderSandboxPicker_LongDescriptionWrapsWithinWidth(t *testing.T) {
	p := &sandboxPickerState{cursor: sandboxModeAutoAllow, detected: true, status: sandbox.Status{Installed: true, ImagePresent: true}}
	// Wide enough for the longest mode row label, narrow enough that the
	// long description sentence must wrap onto multiple lines to prove
	// the regression is actually fixed.
	const width = 60
	got := renderSandboxPicker(p, width)
	for line := range strings.SplitSeq(got, "\n") {
		if w := runeLen(stripANSI(line)); w > width {
			t.Errorf("line exceeds width %d (got %d): %q", width, w, line)
		}
	}
	if !strings.Contains(got, "run_bash executes inside a podman container") {
		t.Errorf("wrapped description should still contain its full text (just split across lines):\n%s", got)
	}
}

// TestOpenSandboxPicker_ReturnsAsyncDetectionCmd pins that opening the
// picker no longer blocks on sandbox.DetectStatus inline — it must
// return a non-nil tea.Cmd that performs the check off the Update
// goroutine, per the codebase's runProviderProbe/providerProbeMsg
// pattern for exactly this kind of external-process probe.
func TestOpenSandboxPicker_ReturnsAsyncDetectionCmd(t *testing.T) {
	detectCalls := 0
	oldDetect := sandboxDetectStatus
	sandboxDetectStatus = func(_ context.Context, image, documentsImage string) sandbox.Status {
		detectCalls++
		if image == "" {
			t.Error("detection should receive the configured sandbox image")
		}
		if documentsImage == "" {
			t.Error("detection should receive the configured documents image")
		}
		return sandbox.Status{Installed: true, ImagePresent: true, DocumentsImagePresent: true}
	}
	t.Cleanup(func() { sandboxDetectStatus = oldDetect })

	m := newTestModel(t)
	m, cmd := m.openSandboxPicker()
	if cmd == nil {
		t.Fatal("openSandboxPicker should return a tea.Cmd to run detection asynchronously")
	}
	if detectCalls != 0 {
		t.Fatal("openSandboxPicker must not run detection inline")
	}
	if m.sandboxPicker.detected {
		t.Error("detected should start false — the picker hasn't heard back from the async probe yet")
	}
	msg := cmd()
	if detectCalls != 1 {
		t.Fatalf("cmd() should run exactly one detection pass, got %d", detectCalls)
	}
	det, ok := msg.(sandboxDetectMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want sandboxDetectMsg", msg)
	}
	if !det.status.Installed || !det.status.ImagePresent {
		t.Fatalf("cmd() should return the stubbed detection status, got %+v", det.status)
	}
	m, _ = applyMsg(m, det)
	if !m.sandboxPicker.detected {
		t.Error("Update should mark detected true after receiving sandboxDetectMsg")
	}
}

func TestRenderSandboxPicker_WarnsWhenDocumentsImageMissing(t *testing.T) {
	p := &sandboxPickerState{
		cursor:   sandboxModeRegular,
		detected: true,
		status:   sandbox.Status{Installed: true, ImagePresent: true},
	}
	got := renderSandboxPicker(p, 100)
	if !strings.Contains(got, "documents image not pulled locally yet") {
		t.Fatalf("expected documents-image warning, got:\n%s", got)
	}
}
