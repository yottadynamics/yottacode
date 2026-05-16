package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// emitPlanBodyToScrollback writes the plan body to scrollback as a
// labeled header + markdown-rendered body (plain rows, no card
// wrap). Glamour wraps the body to the live terminal width — putting
// it inside a narrower card causes a double-wrap which renders
// erratically. Plain emit is cleaner and reads like any markdown
// output in the conversation, while the modal underneath still
// gathers the decision.
//
// Called once when ApprovalNeeded for exit_plan_mode fires; the
// content persists naturally across modal dismissal.
func emitPlanBodyToScrollback(m *Model, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	// Leading blank line separates the header from whatever came
	// before (typically the model's write_file card finishing with
	// "╰ wrote N bytes") so the two don't visually butt up against
	// each other.
	m.appendLine("")
	m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon+" plan ready for review:") +
		" " + stylePlanBannerHint.Render("read it, then pick a decision below"))
	m.appendLine("")
	rendered := strings.TrimRight(m.md.render(body), "\n")
	for _, line := range strings.Split(rendered, "\n") {
		m.appendLine(line)
	}
	m.appendLine("")
}

// loadPlanForApproval reads the plan file's contents for the approval
// card. Returns (body, "") when the file exists and has non-empty
// trimmed content; returns ("", reason) for any guard failure so the
// caller can auto-deny with a console notice. Reasons are phrased for
// direct display to the user.
func loadPlanForApproval(state *agent.PlanModeState) (body string, denyReason string) {
	if state == nil || state.PlanFile == "" {
		return "", "no plan file resolved yet (model called exit_plan_mode before writing a plan)"
	}
	raw, err := os.ReadFile(state.PlanFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "plan file does not exist yet — write the plan to " + state.PlanFile + " first"
		}
		return "", fmt.Sprintf("could not read plan file: %v", err)
	}
	body = strings.TrimSpace(string(raw))
	if body == "" {
		return "", "plan file is empty — the model called exit_plan_mode without writing content"
	}
	return body, ""
}

// cmdPlan is the slash-command handler for /plan. Two shapes:
//
//	/plan          — toggle plan mode (mirror of Shift+Tab).
//	/plan list     — open a picker over ~/.yottacode/plans/ for resume.
//
// Other positional arguments are ignored — the slug is always derived
// from the user's first message of the plan-mode session by
// maybeFillPlanFile (called from startTurn). This keeps the entry
// path identical for /plan, Shift+Tab, and the --plan launch flag.
func cmdPlan(m Model, args []string) (Model, tea.Cmd) {
	if m.cfg.PlanMode == nil {
		// Defensive: a non-TUI build of LoopConfig might not wire the
		// state. Don't pretend plan mode is on.
		m.appendLine(styleError.Render("[plan] plan mode is not available in this build"))
		return m, nil
	}
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "list") {
		m.openPlansPicker()
		return m, nil
	}
	return togglePlanMode(m)
}

// togglePlanMode is the entry/exit helper shared by /plan, the
// Shift+Tab key chord, and the --plan launch flag. Idempotent at the
// boundaries: invoking again while active exits; invoking when off
// enters. The slug is always deferred — entry leaves PlanFile empty,
// then maybeFillPlanFile (called from startTurn) resolves it on the
// first user message.
func togglePlanMode(m Model) (Model, tea.Cmd) {
	state := m.cfg.PlanMode
	if state == nil {
		return m, nil
	}
	if state.IsActive() {
		exitPlanMode(&m)
		return m, nil
	}
	// Entering: clear auto mode if it's on (plan and auto are
	// mutually exclusive). Yolo is NOT exited — it's an orthogonal
	// overlay flag. Then clear any stale plan file and the
	// write-tool allowance, and flip the active flag.
	if m.cfg.AutoMode.IsActive() {
		exitAutoMode(&m)
	}
	state.PlanFile = ""
	applyPlanFileToWriteTools(m.cfg.Registry, "")
	state.Active.Store(true)
	// Two-line entry: bold headline carries the mode + the prompt;
	// Card-shaped entry: header + body + footer in the gutter style
	// shared with tool-output cards. The exit hint lives in the footer
	// (not in the persistent banner) so it surfaces once on entry
	// without crowding the cmdline frame on every redraw. Trailing
	// blank line separates the card from the live banner that renders
	// immediately below it.
	m.appendLine(renderPlanModeEntryCard())
	m.appendLine("")
	return m, nil
}

// exitPlanMode is the cleanup half of togglePlanMode, also reused on
// the post-approval path (handleAgentEvent → ApprovalNeeded → AllowOnce
// for exit_plan_mode). Mutates the shared state pointer + the write
// tools' WriteOpts in place; the agent goroutine sees the new state on
// its next iteration.
func exitPlanMode(m *Model) {
	state := m.cfg.PlanMode
	if state == nil {
		return
	}
	wasActive := state.Active.Swap(false)
	planFile := state.PlanFile
	state.PlanFile = ""
	applyPlanFileToWriteTools(m.cfg.Registry, "")
	if !wasActive {
		return
	}
	if planFile != "" {
		m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon+" plan mode exited") +
			" " + stylePlanBannerHint.Render("— plan file kept at "+abbrevHome(planFile)))
	} else {
		m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon + " plan mode exited"))
	}
}

// maybeFillPlanFile derives the plan-file path from the user's first
// message in a plan-mode session when /plan was invoked without a
// topic. Called from startTurn just before dispatch. No-op when plan
// mode is off, when a plan file is already set, or when the message is
// empty/whitespace.
func maybeFillPlanFile(m *Model, userMessage string) {
	state := m.cfg.PlanMode
	if !state.IsActive() || state.PlanFile != "" {
		return
	}
	prompt := strings.TrimSpace(userMessage)
	if prompt == "" {
		return
	}
	salt := ""
	if m.sess != nil {
		salt = m.sess.ID
	}
	slug := agent.SlugFromPrompt(prompt, salt)
	path, err := agent.PlanFilePath(slug)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[plan] could not resolve plan file: %v", err)))
		return
	}
	state.PlanFile = path
	applyPlanFileToWriteTools(m.cfg.Registry, path)
	m.appendLine(renderPlanFileCard(path))
}

// applyPlanFileToWriteTools mutates the WriteOpts.PlanModeAllowedFile
// field on every registered write/edit tool. The registry stores tools
// as pointer-typed values, so the in-flight agent goroutine sees the
// new field value on its next ValidateWritePath call. Pass "" to
// clear the allowance on plan-mode exit.
//
// Tools that don't carry a WriteOpts (read-only, run_bash, git_*) are
// silently skipped — the registry returns them via Get but the type
// assertion fails harmlessly.
func applyPlanFileToWriteTools(reg *agent.Registry, planFile string) {
	if reg == nil {
		return
	}
	for _, name := range []string{"write_file", "edit_file", "apply_diff"} {
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		switch tt := t.(type) {
		case *agent.WriteFileTool:
			tt.WriteOpts.PlanModeAllowedFile = planFile
		case *agent.EditFileTool:
			tt.WriteOpts.PlanModeAllowedFile = planFile
		case *agent.ApplyDiffTool:
			tt.WriteOpts.PlanModeAllowedFile = planFile
		}
	}
}

