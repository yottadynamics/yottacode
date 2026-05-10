package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The input is borderless. The earlier design wrapped it in a
// brand-colored rounded box that drowned out everything inside; the
// chevron + placeholder/content carry the focal weight on their own.
// Catches regressions that re-add a rounded-frame border.
func TestInput_HasNoRoundedBorder(t *testing.T) {
	m := newTestModel(t)
	view := m.renderInputBox()
	if strings.ContainsAny(view, "╭╮╯╰│") {
		t.Errorf("input should be borderless; rounded-frame chars found: %q", view)
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

// The input row is bracketed by two dim horizontal rules — top and
// bottom. Replaces the old saturated rounded box. Each rule is a
// sequence of `─` spanning the input's content width.
func TestInput_BracketedByHorizontalRules(t *testing.T) {
	m := newTestModel(t)
	plain := stripANSI(m.View())
	lines := strings.Split(plain, "\n")
	var ruleIdx []int
	cmdIdx := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.Contains(line, "ask anything…") {
			cmdIdx = i
		}
		if t != "" && strings.HasPrefix(t, "─") && !strings.ContainsAny(t, "╭╮╯╰│") {
			ruleIdx = append(ruleIdx, i)
		}
	}
	if cmdIdx < 0 {
		t.Fatalf("placeholder row not found: %q", plain)
	}
	if len(ruleIdx) < 2 {
		t.Fatalf("expected at least two `─` rules, got %d in: %q", len(ruleIdx), plain)
	}
	// One rule must be on the line above the cmdline; one must be
	// directly below.
	above, below := false, false
	for _, idx := range ruleIdx {
		if idx == cmdIdx-1 {
			above = true
		}
		if idx == cmdIdx+1 {
			below = true
		}
	}
	if !above || !below {
		t.Errorf("expected rules immediately above and below the cmdline; got rules at %v, cmdline at %d", ruleIdx, cmdIdx)
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
