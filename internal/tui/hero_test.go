package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestHero_ShownBeforeFirstMessage(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("fresh model should not have entered conversation yet")
	}
	v := stripANSI(m.View().Content)
	if !strings.Contains(v, "YottaCode") {
		t.Errorf("hero should show the startup identity card; got %q", v)
	}
	if !strings.Contains(v, "ask anything") {
		t.Errorf("hero should still show the cmdline below the card; got %q", v)
	}
}

func TestHero_ExitsOnFirstUserMessage(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "hello there")
	if !m.enteredConversation {
		t.Error("submitting a chat message should leave the launch hero")
	}
}

// TestHero_PickerSlashCommandDoesNotExitHero guards the fix for a real
// UX bug: opening a picker-only command like /help used to exit the
// launch hero permanently, so the cmdline jumped to the bottom of the
// (still-empty) conversation layout and left a gap once the picker
// closed. Just looking at a picker shouldn't count as "the conversation
// has begun" — only a real chat message or a command that actually
// starts agent activity should (see TestHero_ExitsOnFirstUserMessage
// and enteredConversation's field doc).
func TestHero_PickerSlashCommandDoesNotExitHero(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/help")
	if !m.helpOpen {
		t.Fatalf("test setup: /help should open the help popup")
	}
	if m.enteredConversation {
		t.Error("opening a picker-only slash command should not exit the launch hero")
	}
}

func TestHero_StaysExitedAfterClear(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m, _ = cmdClear(m, nil)
	if !m.enteredConversation {
		t.Error("enteredConversation is one-way — /clear must not revert to the launch hero mid-session")
	}
}

func TestHero_PopupsRenderOverHeroBackground(t *testing.T) {
	m := newTestModel(t)
	// Wide enough that the popup (content capped at popupMaxWidth) leaves
	// visible background margin on the sides — the cheatsheet's rows
	// aren't wrapped to width (pre-existing, unrelated to popups), so at
	// the default 80-col test width the popup alone is already wider
	// than the frame and would occlude it entirely either way.
	m.width = 200
	m.cheatsheetOpen = true
	v := stripANSI(m.View().Content)
	if !strings.Contains(v, "Keyboard shortcuts") {
		t.Errorf("popups should render over the hero background too, not just the conversation layout; got %q", v)
	}
	if !strings.Contains(v, "YottaCode") {
		t.Errorf("the hero background should still be visible around the popup; got %q", v)
	}
}

// TestRenderHero_AnchorsNearTopOfScreen pins the identity card landing
// within a couple of rows of the very top of the screen (lipgloss.Top,
// plus the one deliberate leading blank row — see renderHero), with the
// rest of a tall screen staying blank below it, rather than floating
// mid-screen. renderHero explicitly avoids a fractional lipgloss.Place
// vPos for this: charm.land/lipgloss/v2@v2.0.5's Place has a verified
// bug where any vPos strictly between 0 and 1 renders inverted (content
// lands near the BOTTOM instead of proportionally down from the top);
// only the exact 0.0/1.0 endpoints land where documented, which is
// exactly why this test checks near-the-top-edge, not "somewhere in the
// upper half" — a regression back to a fractional vPos would still pass
// a looser "upper half" check while visibly reintroducing the original
// bug report (card rendering near the bottom of a tall terminal).
func TestRenderHero_AnchorsNearTopOfScreen(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 60
	lines := strings.Split(stripANSI(m.renderHero()), "\n")
	cardRow := -1
	for i, line := range lines {
		if strings.Contains(line, "YottaCode") {
			cardRow = i
			break
		}
	}
	if cardRow < 0 {
		t.Fatalf("could not locate the identity card in the hero render:\n%s", strings.Join(lines, "\n"))
	}
	if cardRow > 2 {
		t.Errorf("hero card should anchor within a couple rows of the top edge, got row %d of %d", cardRow, m.height)
	}
}

// TestHero_ShowsPaletteWhenOpen guards the bug where renderHero built
// its block by hand instead of going through aboveInputRows (shared
// with renderFooter) — the slash-command palette state flipped open
// correctly on "/", but nothing rendered it during the launch hero, so
// typing "/" looked like slash commands didn't work at all pre-first-
// message.
func TestHero_ShowsPaletteWhenOpen(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "/"})
	if !m.paletteOpen {
		t.Fatalf("test setup: typing / should open the palette")
	}
	if m.enteredConversation {
		t.Fatalf("test setup: typing / alone should not leave the hero yet")
	}
	v := stripANSI(m.View().Content)
	if !strings.Contains(v, "/model") {
		t.Errorf("hero should render the open slash palette (expected to see command names like '/model'); got %q", v)
	}
}

// TestHero_ShowsModeBannerWhenActive guards the same class of bug for
// mode banners: yolo/auto mode can be armed via a startup flag before
// the user ever submits a first message, so the banner needs to be
// visible on the launch hero too, not just once conversation begins.
func TestHero_ShowsModeBannerWhenActive(t *testing.T) {
	m := newTestModel(t)
	// newTestModel doesn't allocate YoloMode state (only tests that need
	// it do) — enterYoloMode is a safe no-op against a nil *YoloModeState,
	// so it has to be allocated here first, same as
	// TestStartupBanner_DeferredUntilWidthKnown's construction.
	m.cfg.YoloMode = &agent.YoloModeState{}
	m = enterYoloMode(m)
	if m.enteredConversation {
		t.Fatalf("test setup: entering yolo mode alone should not leave the hero")
	}
	v := stripANSI(m.View().Content)
	if !strings.Contains(v, "yolo mode") {
		t.Errorf("hero should show the yolo banner when active pre-conversation; got %q", v)
	}
}
