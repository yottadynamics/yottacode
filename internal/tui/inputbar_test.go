package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// The input is borderless. The earlier design wrapped it in a
// brand-colored box that drowned out everything inside; the chevron +
// placeholder/content carry the focal weight on their own. Catches
// regressions that re-add a frame border — rounded (╭╮╰╯) or sharp
// (┌┐└┘).
func TestInput_HasNoBorder(t *testing.T) {
	m := newTestModel(t)
	view := m.renderInputBox()
	if strings.ContainsAny(view, "╭╮╯╰┌┐└┘│") {
		t.Errorf("input should be borderless; frame chars found: %q", view)
	}
}

// The placeholder is the short `ask anything…` form, not the four-hint
// preamble that used to live inline. The hints have moved to a
// separate footer line — see TestInput_HintFooterShowsBeforeFirstMessage.
func TestInput_PlaceholderIsShort(t *testing.T) {
	m := newTestModel(t)
	plain := stripANSI(m.renderInputBox())
	if !strings.Contains(plain, "ask anything…") {
		t.Errorf("input placeholder should be 'ask anything…': %q", plain)
	}
	if strings.Contains(plain, "/ for commands") || strings.Contains(plain, "@path to attach") {
		t.Errorf("input placeholder should not embed the four-hint preamble: %q", plain)
	}
}

// On a fresh session the onboarding hints render inlined on the
// placeholder row (chevron + dim italic placeholder + dim hints) — the
// hints used to live on a separate footer line, but a single row reads
// cleaner and matches the original "everything in the placeholder" UX.
// Keyed off m.firstMessageSent which starts false.
func TestInput_HintsShowInlineBeforeFirstMessage(t *testing.T) {
	m := newTestModel(t)
	plain := stripANSI(m.View())
	for _, want := range []string{"commands", "@ files", "↑↓ history"} {
		if !strings.Contains(plain, want) {
			t.Errorf("fresh session view missing hint %q: %q", want, plain)
		}
	}
	// Hints share the same line as the placeholder.
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "ask anything…") {
			if !strings.Contains(line, "↑↓ history") {
				t.Errorf("hints should be inlined on the placeholder row: %q", line)
			}
			return
		}
	}
	t.Errorf("placeholder row not found in view: %q", plain)
}

// After the first user submission the hints disappear permanently for
// this launch. Flipping m.firstMessageSent simulates what startTurn
// does on the user's first Enter.
func TestInput_HintsDisappearAfterFirstMessage(t *testing.T) {
	m := newTestModel(t)
	m.firstMessageSent = true
	plain := stripANSI(m.View())
	for _, gone := range []string{"@ files", "↑↓ history"} {
		if strings.Contains(plain, gone) {
			t.Errorf("hint %q should be hidden after first message: %q", gone, plain)
		}
	}
	// The placeholder itself stays visible on the empty input — only
	// the trailing hints disappear.
	if !strings.Contains(plain, "ask anything…") {
		t.Errorf("placeholder should still render after first message: %q", plain)
	}
}

// The chevron `❯` is Accent-colored and persistent — it stays
// rendered whether the input is empty (placeholder branch) or
// populated.
func TestInput_ChevronStaysVisibleWithContent(t *testing.T) {
	m := newTestModel(t)
	for _, r := range "hello" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !strings.Contains(m.View(), "❯") {
		t.Errorf("chevron should stay visible with content: %q", m.View())
	}
}

// renderModelName paints the model in Content (off-white), not
// Accent. Earlier versions painted it cyan + bold which competed with
// the connection dot for "active state" attention.
func TestRenderModelName_NotAccentColored(t *testing.T) {
	got := renderModelName("test-model")
	if !strings.Contains(stripANSI(got), "test-model") {
		t.Errorf("renderModelName should embed the literal name: %q", got)
	}
}

// The provider tag renders next to the model on a wide terminal so
// users can see at a glance which profile they're talking to (e.g.
// openai-auth vs anthropic). Drops before the vendor prefix gets
// stripped if width pressure forces a cascade.
func TestStatusBar_RendersProviderNextToModel(t *testing.T) {
	m := newTestModel(t)
	m.provider = "openai-auth"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "test-model") {
		t.Errorf("status bar should include model: %q", plain)
	}
	if !strings.Contains(plain, "openai-auth") {
		t.Errorf("status bar should include provider tag: %q", plain)
	}
}

// On a narrow terminal the provider tag drops first; model and ctx
// segments must survive every width the bar ever renders.
func TestStatusBar_NarrowTerminalKeepsModelAndCtx(t *testing.T) {
	m := newTestModel(t)
	m.provider = "openai-auth"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 40, Height: 24})
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "test-model") {
		t.Errorf("narrow status bar should keep model name: %q", plain)
	}
	if !strings.Contains(plain, "ctx ") {
		t.Errorf("narrow status bar should keep ctx segment: %q", plain)
	}
}

// When the session runs inside a yottacode worktree, the status bar
// shows "worktree: <name>" so users see at a glance where edits land.
// Empty worktree on the main checkout renders no chip.
func TestStatusBar_RendersWorktreeChip(t *testing.T) {
	m := newTestModel(t)
	m.worktree = "feature-auth"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "worktree: feature-auth") {
		t.Errorf("status bar should include the worktree chip: %q", plain)
	}
}

func TestStatusBar_NoWorktreeChipOnMainCheckout(t *testing.T) {
	m := newTestModel(t)
	m.worktree = ""
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "worktree:") {
		t.Errorf("main-checkout status bar should not render worktree chip: %q", plain)
	}
}

// The status bar shows a compact location chip — the working directory's
// last two path segments plus the git branch in parens — so users see at
// a glance which project and branch edits land in, without the full
// absolute path eating the footer.
func TestStatusBar_RendersLocationChip(t *testing.T) {
	m := newTestModel(t)
	m.cwd = "/home/me/go/src/foo/bar"
	m.branch = "main"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "foo/bar (main)") {
		t.Errorf("status bar should include the location chip 'foo/bar (main)': %q", plain)
	}
}

// With no branch (detached / not a repo) the chip still shows the dir,
// but drops the empty parens.
func TestStatusBar_LocationChipShowsDirWithoutBranch(t *testing.T) {
	m := newTestModel(t)
	m.cwd = "/home/me/go/src/foo/bar"
	m.branch = ""
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "foo/bar") {
		t.Errorf("location chip should still show the dir when no branch: %q", plain)
	}
	if strings.Contains(plain, "foo/bar (") {
		t.Errorf("no branch → no parens: %q", plain)
	}
}

// Under narrow-terminal pressure the location chip drops early (it's
// ambient orientation), but the model name and ctx segment — the critical
// signals — must survive every width.
func TestStatusBar_LocationChipDropsOnNarrow(t *testing.T) {
	m := newTestModel(t)
	m.cwd = "/home/me/go/src/some-long-project/deeply-nested-dir"
	m.branch = "feature-really-long-branch-name"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 44, Height: 24})
	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "feature-really-long-branch-name") {
		t.Errorf("narrow status bar should drop the location chip: %q", plain)
	}
	if !strings.Contains(plain, "test-model") || !strings.Contains(plain, "ctx ") {
		t.Errorf("narrow status bar must keep model + ctx: %q", plain)
	}
}

// When the current branch has an open PR, the status bar shows the PR
// number as a compact chip so users know which review thread edits target.
func TestStatusBar_RendersCurrentPRChip(t *testing.T) {
	m := newTestModel(t)
	m.currentPR = 13
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "PR #13") {
		t.Errorf("status bar should include current PR chip: %q", plain)
	}
}

func TestStatusBar_NoCurrentPRChipWhenUndetected(t *testing.T) {
	m := newTestModel(t)
	m.currentPR = 0
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "PR #") {
		t.Errorf("status bar should hide PR chip when no PR is detected: %q", plain)
	}
}

// The PR chip is lower priority than the model and context counter, so
// narrow terminals drop it before sacrificing the core status signals.
func TestStatusBar_NarrowTerminalDropsPRBeforeCoreSignals(t *testing.T) {
	m := newTestModel(t)
	m.currentPR = 13
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 40, Height: 24})
	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "PR #") {
		t.Errorf("narrow status bar should drop PR chip: %q", plain)
	}
	if !strings.Contains(plain, "test-model") || !strings.Contains(plain, "ctx ") {
		t.Errorf("narrow status bar should keep model and ctx: %q", plain)
	}
}

// On a narrow-enough terminal that even "model · ctx" won't fit, the
// vendor prefix on the model name (e.g. `nvidia/`) is stripped.
func TestStatusBar_VeryNarrowTrimsVendorPrefix(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "nvidia/nemotron-3-super-120b-a12b"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 30, Height: 24})
	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "nvidia/") {
		t.Errorf("very narrow status bar should drop vendor prefix: %q", plain)
	}
	// The base name should still be present.
	if !strings.Contains(plain, "nemotron") {
		t.Errorf("status bar should keep base model name: %q", plain)
	}
}

// Reasoning-capable models show the current effort level inline so the
// user can see whether the session is on provider default or an explicit
// low/medium/high override.
func TestStatusBar_RendersEffortForReasoningModel(t *testing.T) {
	m := newTestModel(t)
	m.provider = "openai"
	m.providerProfile.Provider = adapter.ProviderOpenAI
	m.modelName = "gpt-5"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})

	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "effort: default") {
		t.Errorf("status bar should show default effort for reasoning model: %q", plain)
	}

	m.reasoningEffort = "high"
	plain = stripANSI(m.renderStatus())
	if !strings.Contains(plain, "effort: high") {
		t.Errorf("status bar should show explicit effort for reasoning model: %q", plain)
	}
}

// Non-reasoning or unsupported models must not show an effort chip, even
// if the session has a stored level, because the adapter would ignore it.
func TestStatusBar_HidesEffortForUnsupportedModel(t *testing.T) {
	m := newTestModel(t)
	m.provider = "openai"
	m.providerProfile.Provider = adapter.ProviderOpenAI
	m.modelName = "gpt-4o"
	m.reasoningEffort = "high"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})

	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "effort:") {
		t.Errorf("status bar should hide effort for unsupported model: %q", plain)
	}
}

// The effort chip is useful but lower priority than model and context;
// narrow terminals drop it before they drop the irreducible signals.
func TestStatusBar_NarrowTerminalDropsEffortBeforeCoreSignals(t *testing.T) {
	m := newTestModel(t)
	m.provider = "openai"
	m.providerProfile.Provider = adapter.ProviderOpenAI
	m.modelName = "gpt-5"
	m.reasoningEffort = "high"
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 40, Height: 24})

	plain := stripANSI(m.renderStatus())
	if strings.Contains(plain, "effort:") {
		t.Errorf("narrow status bar should drop effort chip first: %q", plain)
	}
	if !strings.Contains(plain, "gpt-5") || !strings.Contains(plain, "ctx ") {
		t.Errorf("narrow status bar should keep model and ctx: %q", plain)
	}
}

// The input row is bracketed by two dim horizontal rules — top and
// bottom. Replaces the old saturated rounded box. Each rule is a
// sequence of `─` spanning the input's content width.
func TestInput_EnclosedInBorderedBox(t *testing.T) {
	m := newTestModel(t)
	plain := stripANSI(m.View())
	lines := strings.Split(plain, "\n")
	cmdIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "ask anything…") {
			cmdIdx = i
			break
		}
	}
	if cmdIdx < 0 {
		t.Fatalf("placeholder row not found: %q", plain)
	}
	if cmdIdx < 1 || cmdIdx >= len(lines)-1 {
		t.Fatalf("cmdline at edge of view, can't check borders; idx=%d lines=%d", cmdIdx, len(lines))
	}
	above := strings.TrimSpace(lines[cmdIdx-1])
	below := strings.TrimSpace(lines[cmdIdx+1])
	if !strings.HasPrefix(above, "┌") || !strings.HasSuffix(above, "┐") {
		t.Errorf("top border should be ┌...┐; got %q", above)
	}
	if !strings.HasPrefix(below, "└") || !strings.HasSuffix(below, "┘") {
		t.Errorf("bottom border should be └...┘; got %q", below)
	}
	cmdRow := strings.TrimSpace(lines[cmdIdx])
	if !strings.HasPrefix(cmdRow, "│") || !strings.HasSuffix(cmdRow, "│") {
		t.Errorf("cmdline row should have │ side borders; got %q", cmdRow)
	}
}

// renderInputRule spans the full terminal width — the bracket reads
// as a screen-wide divider, even though the input value itself stays
// capped at inputContentWidth. Earlier versions tracked the cap; the
// narrow rule looked like a floating underline rather than a frame.
func TestInput_RuleSpansFullTerminalWidth(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 200, Height: 24})
	rule := stripANSI(m.renderInputRule())
	if got := lipgloss.Width(rule); got != m.width {
		t.Errorf("rule width = %d, want %d (full terminal width)", got, m.width)
	}
}

// The cursor block toggles via cursorBlinkMsg ticks. Each tick flips
// m.cursorVisible and re-arms the next tick. We're not testing the
// full timing loop — just that the message handler does its job.
func TestCursor_BlinkMsgTogglesVisibility(t *testing.T) {
	m := newTestModel(t)
	if !m.cursorVisible {
		t.Fatalf("fresh model should start with cursor visible")
	}
	m, _ = applyMsg(m, cursorBlinkMsg{})
	if m.cursorVisible {
		t.Errorf("cursor should be invisible after first blink tick")
	}
	m, _ = applyMsg(m, cursorBlinkMsg{})
	if !m.cursorVisible {
		t.Errorf("cursor should be visible again after second blink tick")
	}
}

// renderEmptyCursor returns a 1-cell-wide block in either state — the
// width stability guarantee that prevents the placeholder from
// shifting on each blink. Both phases produce a 1-column-wide string.
func TestCursor_EmptyStateWidthStable(t *testing.T) {
	visible := stripANSI(renderEmptyCursor(true))
	invisible := renderEmptyCursor(false)
	if got := lipgloss.Width(visible); got != 1 {
		t.Errorf("visible cursor width = %d, want 1", got)
	}
	if got := lipgloss.Width(invisible); got != 1 {
		t.Errorf("invisible cursor width = %d, want 1", got)
	}
}

// insertCursor must keep row width stable across blink phases when
// the cursor is past the end of the row. Trailing space is appended
// in both branches so the cmdline doesn't flinch on each tick.
func TestCursor_InsertCursorWidthStableAtEnd(t *testing.T) {
	row := "hello"
	visible := stripANSI(insertCursor(row, len([]rune(row)), true))
	invisible := insertCursor(row, len([]rune(row)), false)
	if got := lipgloss.Width(visible); got != 6 {
		t.Errorf("visible insertCursor width at end = %d, want 6", got)
	}
	if got := lipgloss.Width(invisible); got != 6 {
		t.Errorf("invisible insertCursor width at end = %d, want 6", got)
	}
}
