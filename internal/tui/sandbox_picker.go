package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/sandbox"
)

// sandboxMode is one of the three rows the /sandbox picker offers. The
// auto-allow row maps to this codebase's existing auto-mode behavior; it does
// not bypass run_bash approval because run_bash stays in auto mode's safety
// floor.
type sandboxMode int

const (
	sandboxModeAutoAllow sandboxMode = iota
	sandboxModeRegular
	sandboxModeOff
	sandboxModeCount
)

// sandboxModeLabel is the row text shown in the picker.
func sandboxModeLabel(mode sandboxMode) string { return sandboxModeLabelFor(mode, false) }

func sandboxModeLabelFor(mode sandboxMode, nextSessionOnly bool) string {
	prefix := ""
	if nextSessionOnly && mode != sandboxModeOff {
		prefix = "Next session: "
	}
	switch mode {
	case sandboxModeAutoAllow:
		return prefix + "Sandbox run_bash, with auto-allow"
	case sandboxModeRegular:
		return prefix + "Sandbox run_bash, regular permissions"
	default:
		return "No sandbox"
	}
}

// sandboxModeDescription is the help text shown under the highlighted row.
// Deliberately does NOT claim run_bash stops prompting under auto-allow —
// this codebase's auto mode keeps run_bash in its safety floor (see
// internal/agent/auto_mode.go); auto-allow here means "podman isolation is on,
// AND this session's auto mode is on" (edits auto-apply), not "run_bash
// bypasses approval."
func sandboxModeDescription(mode sandboxMode) string {
	switch mode {
	case sandboxModeAutoAllow:
		return "After restart, run_bash executes inside a podman container (network=none, project-dir-only mount) and this session's auto mode turns on: edits auto-apply; run_bash, git_commit, git_checkpoint, and rollback still prompt for approval, same as auto mode everywhere else."
	case sandboxModeRegular:
		return "After restart, run_bash executes inside a podman container (network=none, project-dir-only mount). Approval works exactly as it does today — every run_bash call still prompts."
	default:
		return "run_bash executes directly on the host, same as today. Approval and the hardline blocklist are the only guardrails."
	}
}

// sandboxPickerState owns the /sandbox overlay.
type sandboxPickerState struct {
	cursor         sandboxMode
	current        sandboxMode // the mode active when the picker opened
	configured     sandboxMode // the backend persisted in config.toml
	status         sandbox.Status
	experimentalOn bool
	sandboxActive  bool
	// detected is false until the async sandboxDetectMsg from
	// openSandboxPicker's sandboxDetectCmd arrives — the render shows a
	// "checking…" state until then instead of a stale zero-value Status.
	detected bool
	note     string
}

// currentSandboxMode derives the active mode from this session's feature gate,
// on-disk config, startup sandbox state, and live auto-mode state.
func currentSandboxMode(cfg config.Config, experimentalOn, sandboxActive bool, autoMode *agent.AutoModeState) sandboxMode {
	if !experimentalOn || !sandboxActive || cfg.Sandbox.Backend != "podman" {
		return sandboxModeOff
	}
	if autoMode != nil && autoMode.IsActive() {
		return sandboxModeAutoAllow
	}
	return sandboxModeRegular
}

func configuredSandboxMode(cfg config.Config, sandboxActive bool, autoMode *agent.AutoModeState) sandboxMode {
	if cfg.Sandbox.Backend != "podman" {
		return sandboxModeOff
	}
	if sandboxActive && autoMode != nil && autoMode.IsActive() {
		return sandboxModeAutoAllow
	}
	return sandboxModeRegular
}

// openSandboxPicker installs the overlay, seeding it from on-disk config
// and live session state. The podman/image detection pass runs via
// sandboxDetectCmd instead of inline here: DetectStatus makes up to two
// podman subprocess calls (bounded 3s each) and Bubbletea's Update() is
// single-threaded, so running it synchronously would freeze the whole
// TUI's render/input loop for as long as podman takes to answer — the
// same async tea.Cmd + Msg pattern runProviderProbe already uses for
// exactly this kind of external-process probe. The picker renders a
// "checking…" state (sandboxPickerState.detected starts false) until
// sandboxDetectMsg arrives and Update stores the result.
func (m Model) openSandboxPicker() (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	experimentalOn := sandboxExperimentActive(m.experimentalEnabled)
	current := currentSandboxMode(cfg, experimentalOn, m.sandboxActive, m.cfg.AutoMode)
	configured := configuredSandboxMode(cfg, m.sandboxActive, m.cfg.AutoMode)
	m.sandboxPicker = &sandboxPickerState{
		cursor:         current,
		current:        current,
		configured:     configured,
		experimentalOn: experimentalOn,
		sandboxActive:  m.sandboxActive,
	}
	m.sandboxPickerOpen = true
	return m, sandboxDetectCmd(m.parentCtx, cfg.Sandbox.Image)
}

// sandboxDetectMsg carries the result of an async sandbox.DetectStatus
// probe back to Update. See openSandboxPicker.
type sandboxDetectMsg struct {
	status sandbox.Status
}

// sandboxDetectStatus is a package seam for tests. The production path uses
// sandbox.DetectStatus, but unit tests can replace it so they never start real
// podman probes or create rootless container storage under t.TempDir.
var sandboxDetectStatus = sandbox.DetectStatus

func sandboxDetectCmd(ctx context.Context, image string) tea.Cmd {
	return func() tea.Msg {
		return sandboxDetectMsg{status: sandboxDetectStatus(ctx, image)}
	}
}

// updateSandboxPicker handles keystrokes while the overlay is open.
func (m Model) updateSandboxPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.sandboxPicker
	if p == nil {
		m.sandboxPickerOpen = false
		return m, nil
	}
	p.note = ""
	switch msg.Code {
	case tea.KeyEsc:
		return m.closeSandboxPicker()
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < sandboxModeCount-1 {
			p.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		return applySandboxMode(m, p.cursor)
	default:
		if r := []rune(msg.Text); len(r) == 1 {
			switch r[0] {
			case 'a', 'A':
				return applySandboxMode(m, p.cursor)
			case 'j':
				if p.cursor < sandboxModeCount-1 {
					p.cursor++
				}
			case 'k':
				if p.cursor > 0 {
					p.cursor--
				}
			}
		}
		return m, nil
	}
}

func sandboxExperimentActive(enabled []string) bool {
	for _, name := range enabled {
		if name == string(experimental.Sandbox) {
			return true
		}
	}
	return false
}

// closeSandboxPicker dismisses the picker. It does not reopen the slash palette:
// Esc should restore the normal footer immediately, not leave a second transient
// surface open that prevents the inline-overlay re-anchor repaint.
func (m Model) closeSandboxPicker() (Model, tea.Cmd) {
	m.sandboxPickerOpen = false
	m.sandboxPicker = nil
	m.paletteOpen = false
	m.paletteIndex = 0
	m.paletteOffset = 0
	m.textInput.SetValue("")
	return m, nil
}

// applySandboxMode enforces the experimental gate before persisting a picker
// selection. The TUI may observe and explain experimental state, but it must not
// enable experimental features on the user's behalf; that stays a startup/config
// concern handled by --experimental, YOTTACODE_EXPERIMENTAL, or [experimental].
func applySandboxMode(m Model, mode sandboxMode) (Model, tea.Cmd) {
	if mode != sandboxModeOff && m.sandboxPicker != nil && !m.sandboxPicker.experimentalOn {
		m.sandboxPicker.note = "Experimental sandbox is off. Enable with --experimental sandbox, then restart."
		return m, nil
	}
	if mode != sandboxModeOff && m.sandboxPicker != nil && m.sandboxPicker.configured != sandboxModeOff && !m.sandboxPicker.sandboxActive {
		m.sandboxPicker.note = "Restart yottacode to activate the configured sandbox before selecting sandbox options again."
		return m, nil
	}
	return commitSandboxMode(m, mode)
}

// commitSandboxMode persists the chosen mode to config.toml and applies
// what can apply live this session.
//
// Only the auto-mode half applies immediately (toggleAutoMode flips a live
// atomic.Bool). The podman backend itself does NOT apply live: RunBashTool
// is handed its Sandbox once, at session-registry construction
// (internal/tui/run.go), and there is no swap-mid-session path the way
// CwdRef has for cwd. The success message says so explicitly rather than
// implying immediate effect.
func commitSandboxMode(m Model, mode sandboxMode) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	switch mode {
	case sandboxModeOff:
		cfg.Sandbox.Backend = "none"
	default:
		cfg.Sandbox.Backend = "podman"
	}
	if err := config.Validate(cfg); err != nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "sandbox", "validation", err.Error())))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "sandbox", "write config", err.Error())))
		return m, nil
	}
	m.fileCfg = cfg

	// wasAutoAllow is the mode this SAME picker last saw as current — the
	// only case where auto mode being on is attributable to a prior pick
	// in this flow, not to something the user turned on independently
	// (Shift+Tab, --permission-mode auto) before ever opening /sandbox.
	// Only that case should be silently undone by picking a non-auto-
	// allow row; leaving auto mode alone otherwise is what
	// sandboxModeDescription's "regular permissions" text promises.
	wasAutoAllow := m.sandboxPicker != nil && m.sandboxPicker.current == sandboxModeAutoAllow

	switch {
	case mode == sandboxModeAutoAllow:
		if m.cfg.AutoMode != nil && !m.cfg.AutoMode.IsActive() {
			m, _ = toggleAutoMode(m)
		}
	case wasAutoAllow && m.cfg.AutoMode != nil && m.cfg.AutoMode.IsActive():
		exitAutoMode(&m)
	}

	detail := "persisted — restart yottacode (or start a new session) to activate"
	m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "sandbox", sandboxModeLabel(mode), detail)))
	if p := m.sandboxPicker; p != nil {
		p.current = mode
		p.configured = mode
	}
	return m.closeSandboxPicker()
}

// renderSandboxPicker draws the three-row mode menu plus podman/image
// detection status and the highlighted row's description.
func renderSandboxPicker(p *sandboxPickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	cursorArrow := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("❯ ")
	var b strings.Builder
	// "podman" is hardcoded, not derived from config — it's the only
	// real backend today (config.ValidSandboxBackends is "none"|"podman"),
	// and the picker's whole purpose is choosing whether THIS backend is
	// active. Generalize if a second backend ever lands.
	help := "↑↓ navigate · ↵/a apply · esc close"
	b.WriteString(renderMenuHeader("Sandbox (podman)", help, width))
	b.WriteString("\n\n")

	b.WriteString(renderSandboxStatusLine(p))
	b.WriteString("\n")
	if !p.experimentalOn {
		b.WriteString(styleEmpty.Render(wrapPlain("Enable: --experimental sandbox or [experimental].sandbox = true, then restart.", width)))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	for mode := sandboxMode(0); mode < sandboxModeCount; mode++ {
		row := strings.Count(b.String(), "\n")
		h.row(row, int(mode))
		marker := "  "
		labelText := sandboxModeLabelFor(mode, mode != sandboxModeOff && !p.sandboxActive && p.configured != sandboxModeOff)
		label := stylePaletteItem.Render(labelText)
		if mode == p.cursor {
			marker = cursorArrow
			label = stylePaletteSelected.Render(labelText)
		}
		check := "  "
		if mode == p.configured {
			check = styleEmpty.Render("✓ ")
		}
		b.WriteString(marker)
		b.WriteString(check)
		b.WriteString(label)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(styleEmpty.Render(wrapPlain(sandboxModeDescription(p.cursor), width)))
	b.WriteByte('\n')

	if p.cursor != sandboxModeOff {
		switch {
		case !p.detected:
			b.WriteString("\n")
			b.WriteString(styleEmpty.Render("checking podman…"))
		case !p.status.Installed:
			b.WriteString("\n")
			b.WriteString(styleError.Render(wrapPlain("podman not found on PATH — install podman before selecting this mode", width)))
		case !p.status.ImagePresent:
			b.WriteString("\n")
			b.WriteString(styleEmpty.Render(wrapPlain("base image not pulled locally yet — podman will pull it on first use", width)))
		}
	}
	b.WriteString("\n")
	actionsRow := strings.Count(b.String(), "\n")
	apply := "[A] Apply selection"
	cancel := "[Esc] Close"
	if h != nil {
		h.hotkeySpan(actionsRow, 0, ansi.StringWidth(apply), "a")
	}
	b.WriteString(styleHint.Render(apply + "   " + cancel))
	b.WriteByte('\n')
	if p.note != "" {
		b.WriteString("\n")
		b.WriteString(styleError.Render(p.note))
	}
	return strings.TrimRight(b.String(), "\n")
}

func sandboxStatusLine(p *sandboxPickerState) string {
	configured := "off"
	if p.configured != sandboxModeOff {
		configured = "on"
	}
	active := "off"
	if p.current != sandboxModeOff {
		active = "on"
	} else if p.configured != sandboxModeOff && !p.sandboxActive {
		active = "off — restart required"
	}
	experiment := "off"
	if p.experimentalOn {
		experiment = "on"
	}
	return "Configured: sandbox " + configured + "\nActive: sandbox " + active + " · experimental " + experiment
}

func renderSandboxStatusLine(p *sandboxPickerState) string {
	style := lipgloss.NewStyle().Foreground(colorError).Bold(true)
	if p.current != sandboxModeOff {
		style = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	}
	return style.Render(sandboxStatusLine(p))
}
