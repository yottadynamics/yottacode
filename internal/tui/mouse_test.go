package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func openScrollableLoopList(m *Model) {
	m.loopListOpen = true
	m.loops = map[string]loopState{}
	m.loopOrder = nil
	now := time.Now()
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("loop-%d", i)
		m.loopOrder = append(m.loopOrder, id)
		m.loops[id] = loopState{
			id:        id,
			active:    true,
			interval:  time.Minute,
			remaining: -1,
			payload:   fmt.Sprintf("payload %d", i),
			expiresAt: now.Add(time.Hour),
		}
	}
}

func TestView_EnablesAllMotionForWelcomeBeforeConversationStarts(t *testing.T) {
	m := newTestModel(t)
	if got := m.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Errorf("View().MouseMode = %v, want MouseModeAllMotion for welcome hover", got)
	}
}

func TestWelcomeActionAtUsesStableRowsWithoutRenderedHitTest(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.welcomeCursor = int(welcomeHelp)

	tests := []struct {
		name string
		x    int
		y    int
		want welcomeAction
		ok   bool
	}{
		{"negative x misses", -1, 6, 0, false},
		{"past width misses", 80, 6, 0, false},
		{"before actions misses", 6, 5, 0, false},
		{"new worktree row", 6, 7, welcomeNewWorktree, true},
		{"resume session row", 6, 8, welcomeResumeSession, true},
		{"plan row", 6, 9, welcomeEnterPlanMode, true},
		{"help row", 6, 10, welcomeHelp, true},
		{"after actions misses", 6, 11, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := m.welcomeActionAt(tc.x, tc.y)
			if ok != tc.ok {
				t.Fatalf("welcomeActionAt(%d, %d) ok = %v, want %v", tc.x, tc.y, ok, tc.ok)
			}
			if ok && welcomeAction(got) != tc.want {
				t.Fatalf("welcomeActionAt(%d, %d) = %v, want %v", tc.x, tc.y, welcomeAction(got), tc.want)
			}
		})
	}
}

func TestView_EnablesConversationSelectionMouse(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("View().MouseMode = %v, want MouseModeCellMotion so drag selection can auto-copy", got)
	}
}

func TestView_EnablesAllMotionForInteractiveSurfaces(t *testing.T) {
	m := newTestModel(t)
	m.cheatsheetOpen = true
	if got := m.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Errorf("View().MouseMode = %v, want MouseModeAllMotion", got)
	}
}

// newSelectableTranscriptModel builds a model with real, multi-line
// transcript content and a known viewport size, ready for mouse
// selection/scroll tests.
func newSelectableTranscriptModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.enteredConversation = true
	for i := range 50 {
		m.appendLine(fmt.Sprintf("history line %d", i))
	}
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func TestMouseWheel_ScrollsTranscript(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	if !m.transcriptViewport.AtBottom() {
		t.Fatalf("test setup: expected the viewport to start at the bottom")
	}
	m.paletteOpen = true
	m.paletteFiltered = m.filterPaletteAll("/")

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelUp})
	if !m.transcriptViewport.AtBottom() {
		t.Error("scrolling the wheel should stay inert while the slash palette owns mouse input")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelDown})
	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelDown})
	if !m.transcriptViewport.AtBottom() {
		t.Error("scrolling the wheel back down over the transcript should return the transcript viewport to the bottom")
	}
}

func TestMouseWheel_OverCmdlineBrowsesInputHistory(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.inputHistory = []string{"first command", "second command"}
	if !m.transcriptViewport.AtBottom() {
		t.Fatalf("test setup: expected the viewport to start at the bottom")
	}

	_, y := m.inputFrameOrigin()
	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 4, Y: y + 1, Button: tea.MouseWheelUp})
	if got := m.textInput.Value(); got != "second command" {
		t.Fatalf("wheel-up over cmdline should browse input history, got %q", got)
	}
	if !m.transcriptViewport.AtBottom() {
		t.Error("wheel-up over cmdline should not scroll the transcript")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 4, Y: y + 1, Button: tea.MouseWheelDown})
	if got := m.textInput.Value(); got != "" {
		t.Errorf("wheel-down over cmdline should return to the empty draft, got %q", got)
	}
}

func TestMouseWheel_ScrollsUsagePopup(t *testing.T) {
	m := newTestModel(t)
	m.height = 8
	m.usageOpen = true
	m.usagePanel = strings.Join([]string{
		"usage header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := m.usageScrollOffset; got != popupMouseWheelLines {
		t.Fatalf("wheel-down should scroll /usage popup content, offset = %d, want %d", got, popupMouseWheelLines)
	}
	if !m.usageOpen {
		t.Fatal("wheel-scrolling /usage should keep the popup open")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if got := m.usageScrollOffset; got != 0 {
		t.Fatalf("wheel-up should scroll /usage popup content back, offset = %d, want 0", got)
	}
}

func TestMouseWheel_ScrollsInspectPopup(t *testing.T) {
	m := newTestModel(t)
	m.height = 8
	m.inspectOpen = true
	m.inspectPanel = strings.Join([]string{
		"inspect header",
		"turn 1",
		"turn 2",
		"turn 3",
		"turn 4",
		"turn 5",
		"turn 6",
		"turn 7",
	}, "\n")

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := m.inspectScrollOffset; got != popupMouseWheelLines {
		t.Fatalf("wheel-down should scroll /inspect popup content, offset = %d, want %d", got, popupMouseWheelLines)
	}
	if !m.inspectOpen {
		t.Fatal("wheel-scrolling /inspect should keep the popup open")
	}
}

func TestMouseWheel_ScrollsContextReportPopup(t *testing.T) {
	m := newTestModel(t)
	m.height = 8
	m.contextReportOpen = true
	m.contextReportBody = strings.Join([]string{
		"context header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := m.contextReportScrollOffset; got != popupMouseWheelLines {
		t.Fatalf("wheel-down should scroll /context popup content, offset = %d, want %d", got, popupMouseWheelLines)
	}
	if !m.contextReportOpen {
		t.Fatal("wheel-scrolling /context should keep the popup open")
	}
}

func TestMouseWheel_ScrollsExperimentalPopup(t *testing.T) {
	m := newTestModel(t)
	m.height = 8
	m.experimentalOpen = true
	m.experimentalPanel = strings.Join([]string{
		"experimental header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := m.experimentalScrollOffset; got != popupMouseWheelLines {
		t.Fatalf("wheel-down should scroll /experimental popup content, offset = %d, want %d", got, popupMouseWheelLines)
	}
	if !m.experimentalOpen {
		t.Fatal("wheel-scrolling /experimental should keep the popup open")
	}
}

func TestMouseWheel_ScrollsLoopListPopup(t *testing.T) {
	m := newTestModel(t)
	m.height = 8
	openScrollableLoopList(&m)

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := m.loopListScrollOffset; got != popupMouseWheelLines {
		t.Fatalf("wheel-down should scroll /loop popup content, offset = %d, want %d", got, popupMouseWheelLines)
	}
	if !m.loopListOpen {
		t.Fatal("wheel-scrolling /loop should keep the popup open")
	}
}

func TestMouseClick_UsageHintArrowsScrollWithoutClosing(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m.usageOpen = true
	m.usageScrollOffset = 1
	m.usagePanel = strings.Join([]string{
		"usage header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}
	originX, originY := m.popupOrigin(box)
	lastContentY := originY + lipgloss.Height(box) - 2

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + 4, Y: lastContentY})
	if got := m.usageScrollOffset; got != 0 {
		t.Fatalf("clicking the hint-row up side should scroll up, offset = %d, want 0", got)
	}
	if !m.usageOpen {
		t.Fatal("clicking usage scroll controls should keep the popup open")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + lipgloss.Width(box) - 5, Y: lastContentY})
	if got := m.usageScrollOffset; got != 1 {
		t.Fatalf("clicking the hint-row down side should scroll down, offset = %d, want 1", got)
	}
}

func TestMouseClick_InspectHintArrowsScrollWithoutClosing(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m.inspectOpen = true
	m.inspectScrollOffset = 1
	m.inspectPanel = strings.Join([]string{
		"inspect header",
		"turn 1",
		"turn 2",
		"turn 3",
		"turn 4",
		"turn 5",
		"turn 6",
		"turn 7",
	}, "\n")
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}
	originX, originY := m.popupOrigin(box)
	lastContentY := originY + lipgloss.Height(box) - 2

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + 4, Y: lastContentY})
	if got := m.inspectScrollOffset; got != 0 {
		t.Fatalf("clicking the hint-row up side should scroll up, offset = %d, want 0", got)
	}
	if !m.inspectOpen {
		t.Fatal("clicking inspect scroll controls should keep the popup open")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + lipgloss.Width(box) - 5, Y: lastContentY})
	if got := m.inspectScrollOffset; got != 1 {
		t.Fatalf("clicking the hint-row down side should scroll down, offset = %d, want 1", got)
	}
}

func TestMouseClick_ContextReportHintArrowsScrollWithoutClosing(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m.contextReportOpen = true
	m.contextReportScrollOffset = 1
	m.contextReportBody = strings.Join([]string{
		"context header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}
	originX, originY := m.popupOrigin(box)
	lastContentY := originY + lipgloss.Height(box) - 2

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + 4, Y: lastContentY})
	if got := m.contextReportScrollOffset; got != 0 {
		t.Fatalf("clicking the hint-row up side should scroll up, offset = %d, want 0", got)
	}
	if !m.contextReportOpen {
		t.Fatal("clicking context scroll controls should keep the popup open")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + lipgloss.Width(box) - 5, Y: lastContentY})
	if got := m.contextReportScrollOffset; got != 1 {
		t.Fatalf("clicking the hint-row down side should scroll down, offset = %d, want 1", got)
	}
}

func TestMouseClick_ExperimentalHintArrowsScrollWithoutClosing(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m.experimentalOpen = true
	m.experimentalScrollOffset = 1
	m.experimentalPanel = strings.Join([]string{
		"experimental header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}
	originX, originY := m.popupOrigin(box)
	lastContentY := originY + lipgloss.Height(box) - 2

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + 4, Y: lastContentY})
	if got := m.experimentalScrollOffset; got != 0 {
		t.Fatalf("clicking the hint-row up side should scroll up, offset = %d, want 0", got)
	}
	if !m.experimentalOpen {
		t.Fatal("clicking experimental scroll controls should keep the popup open")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + lipgloss.Width(box) - 5, Y: lastContentY})
	if got := m.experimentalScrollOffset; got != 1 {
		t.Fatalf("clicking the hint-row down side should scroll down, offset = %d, want 1", got)
	}
}

func TestMouseClick_LoopListHintArrowsScrollWithoutClosing(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	openScrollableLoopList(&m)
	m.loopListScrollOffset = 1
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}
	originX, originY := m.popupOrigin(box)
	lastContentY := originY + lipgloss.Height(box) - 2

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + 4, Y: lastContentY})
	if got := m.loopListScrollOffset; got != 0 {
		t.Fatalf("clicking the hint-row up side should scroll up, offset = %d, want 0", got)
	}
	if !m.loopListOpen {
		t.Fatal("clicking loop-list scroll controls should keep the popup open")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + lipgloss.Width(box) - 5, Y: lastContentY})
	if got := m.loopListScrollOffset; got != 1 {
		t.Fatalf("clicking the hint-row down side should scroll down, offset = %d, want 1", got)
	}
}

func TestMouseClick_ScrollablePopupBodyDoesNotClose(t *testing.T) {
	for _, tc := range []struct {
		name   string
		open   func(*Model)
		closed func(Model) bool
	}{
		{
			name: "usage",
			open: func(m *Model) {
				m.usageOpen = true
				m.usagePanel = strings.Join([]string{
					"usage header",
					"line 1",
					"line 2",
					"line 3",
					"line 4",
					"line 5",
					"line 6",
					"line 7",
				}, "\n")
			},
			closed: func(m Model) bool { return !m.usageOpen },
		},
		{
			name: "inspect",
			open: func(m *Model) {
				m.inspectOpen = true
				m.inspectPanel = strings.Join([]string{
					"inspect header",
					"turn 1",
					"turn 2",
					"turn 3",
					"turn 4",
					"turn 5",
					"turn 6",
					"turn 7",
				}, "\n")
			},
			closed: func(m Model) bool { return !m.inspectOpen },
		},
		{
			name: "help",
			open: func(m *Model) {
				m.helpOpen = true
				m.helpPanel = strings.Join([]string{
					"help header",
					"command 1",
					"command 2",
					"command 3",
					"command 4",
					"command 5",
					"command 6",
					"command 7",
				}, "\n")
			},
			closed: func(m Model) bool { return !m.helpOpen },
		},
		{
			name: "contextReport",
			open: func(m *Model) {
				m.contextReportOpen = true
				m.contextReportBody = strings.Join([]string{
					"context header",
					"line 1",
					"line 2",
					"line 3",
					"line 4",
					"line 5",
					"line 6",
					"line 7",
				}, "\n")
			},
			closed: func(m Model) bool { return !m.contextReportOpen },
		},
		{
			name: "experimental",
			open: func(m *Model) {
				m.experimentalOpen = true
				m.experimentalPanel = strings.Join([]string{
					"experimental header",
					"line 1",
					"line 2",
					"line 3",
					"line 4",
					"line 5",
					"line 6",
					"line 7",
				}, "\n")
			},
			closed: func(m Model) bool { return !m.experimentalOpen },
		},
		{
			name:   "loopList",
			open:   openScrollableLoopList,
			closed: func(m Model) bool { return !m.loopListOpen },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.width = 80
			m.height = 8
			tc.open(&m)
			box, ok := m.activePopupBody()
			if !ok {
				t.Fatal("test setup: expected active popup body")
			}
			originX, originY := m.popupOrigin(box)

			m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + 4, Y: originY + 2})
			if tc.closed(m) {
				t.Fatalf("clicking %s popup body should keep the scrollable popup open", tc.name)
			}
		})
	}
}

func TestMouseWheel_ScrollsHelpPopupWhenRenderedLinesOverflow(t *testing.T) {
	m := newTestModel(t)
	m.width = 60
	m.height = 8
	m.helpOpen = true
	m.helpPanel = strings.Join([]string{
		"Help",
		"/first this command has a deliberately long description that wraps across several rendered terminal rows",
		"/second this command also wraps across several rendered terminal rows",
		"/third this command also wraps across several rendered terminal rows",
	}, "\n")
	if maxOffset := m.helpMaxScrollOffset(); maxOffset <= 0 {
		t.Fatalf("wrapped help content should require scrolling, maxOffset=%d", maxOffset)
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := m.helpScrollOffset; got == 0 {
		t.Fatal("wheel-down should scroll /help when rendered rows overflow even if logical line count is small")
	}
	if !m.helpOpen {
		t.Fatal("wheel-scrolling /help should keep the popup open")
	}
}

func TestMouseClick_HelpPopupClickDoesNotDismissWhenNotScrollable(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24
	m.helpOpen = true
	m.helpPanel = "Help\n/help show this list"
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}
	originX, originY := m.popupOrigin(box)

	m, _ = applyMsg(m, tea.MouseClickMsg{X: originX + 4, Y: originY + 2})
	if !m.helpOpen {
		t.Fatal("clicking inside /help should keep the popup open; Esc or × closes it")
	}
}

func TestHelpPopup_RenderedHeightNeverExceedsTerminal(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m, _ = cmdHelp(m, nil)
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active help popup")
	}
	if got := lipgloss.Height(box); got > m.height {
		t.Fatalf("help popup height = %d, want <= terminal height %d", got, m.height)
	}
}

func TestWindowedUsagePopup_KeepsRenderedHeightStableWhileScrolling(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m.usageOpen = true
	m.usagePanel = strings.Join([]string{
		"usage header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")
	before, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}

	m.usageScrollOffset = 2
	after, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body after scrolling")
	}
	if lipgloss.Height(before) != lipgloss.Height(after) {
		t.Fatalf("popup height changed while scrolling: before=%d after=%d", lipgloss.Height(before), lipgloss.Height(after))
	}
}

func TestWindowedContextReportPopup_KeepsRenderedHeightStableWhileScrolling(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m.contextReportOpen = true
	m.contextReportBody = strings.Join([]string{
		"context header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")
	before, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}

	m.contextReportScrollOffset = 2
	after, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body after scrolling")
	}
	if lipgloss.Height(before) != lipgloss.Height(after) {
		t.Fatalf("popup height changed while scrolling: before=%d after=%d", lipgloss.Height(before), lipgloss.Height(after))
	}
}

func TestWindowedExperimentalPopup_KeepsRenderedHeightStableWhileScrolling(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	m.experimentalOpen = true
	m.experimentalPanel = strings.Join([]string{
		"experimental header",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")
	before, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}

	m.experimentalScrollOffset = 2
	after, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body after scrolling")
	}
	if lipgloss.Height(before) != lipgloss.Height(after) {
		t.Fatalf("popup height changed while scrolling: before=%d after=%d", lipgloss.Height(before), lipgloss.Height(after))
	}
}

func TestWindowedLoopListPopup_KeepsRenderedHeightStableWhileScrolling(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 8
	openScrollableLoopList(&m)
	before, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body")
	}

	m.loopListScrollOffset = 2
	after, ok := m.activePopupBody()
	if !ok {
		t.Fatal("test setup: expected active popup body after scrolling")
	}
	if lipgloss.Height(before) != lipgloss.Height(after) {
		t.Fatalf("popup height changed while scrolling: before=%d after=%d", lipgloss.Height(before), lipgloss.Height(after))
	}
}

func TestMouseWheel_NoopWhilePopupOpen(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.cheatsheetOpen = true

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if !m.transcriptViewport.AtBottom() {
		t.Error("wheel scroll should be a no-op while a popup is open")
	}
}

func TestMouseWheel_ScrollsTranscriptWhileApprovalPending(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.awaitingApproval = true
	m.approvalTool = "run_bash"
	if !m.transcriptViewport.AtBottom() {
		t.Fatalf("test setup: expected the viewport to start at the bottom")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelUp})
	if m.transcriptViewport.AtBottom() {
		t.Error("wheel-up should scroll transcript content even while a regular approval modal is open")
	}
}

func TestMouseWheel_ScrollsTranscriptWhilePlanApprovalPending(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.awaitingApproval = true
	m.approvalTool = "exit_plan_mode"
	if !m.transcriptViewport.AtBottom() {
		t.Fatalf("test setup: expected the viewport to start at the bottom")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelUp})
	if m.transcriptViewport.AtBottom() {
		t.Error("wheel-up should scroll transcript content while the plan approval card is open")
	}
}

func TestMouseWheel_NoopBeforeFirstMessage(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("test setup: fresh model should still be on the launch hero")
	}
	// Should not panic or otherwise misbehave against a transcriptViewport
	// with no content — the hero has nothing to scroll.
	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.transcriptSelecting {
		t.Error("wheel scroll on the hero should not start a selection")
	}
}

// clipboardText extracts the string payload from a tea.SetClipboard Cmd
// via reflection — the underlying setClipboardMsg type is unexported,
// but its Kind is String, so reflect.Value.String() returns the real
// content directly.
func clipboardText(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a clipboard Cmd, got nil")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.String {
		t.Fatalf("expected a string-kind clipboard message, got %T", msg)
	}
	return v.String()
}

func TestTranscriptSelection_ClickDragReleaseCopiesToClipboard(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	// AtBottom, so the last visible row is "history line 49".
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if !m.transcriptSelecting {
		t.Fatalf("mouse-down over the transcript should begin a selection")
	}
	m, _ = applyMsg(m, tea.MouseMotionMsg{X: 12, Y: 0})
	if !m.transcriptSelecting {
		t.Fatalf("motion mid-drag should keep the selection active")
	}

	var cmd tea.Cmd
	m, cmd = applyMsg(m, tea.MouseReleaseMsg{X: 12, Y: 0})
	if m.transcriptSelecting {
		t.Error("release should end the drag")
	}
	got := clipboardText(t, cmd)
	if got == "" {
		t.Error("release should copy non-empty selected text to the clipboard")
	}
}

func viewportLinesPtr(t *testing.T, m Model) uintptr {
	t.Helper()
	lines := reflect.ValueOf(m.transcriptViewport).FieldByName("lines")
	if !lines.IsValid() || lines.Len() == 0 {
		t.Fatal("expected viewport lines to be populated")
	}
	return lines.Pointer()
}

func TestTranscriptSelection_ExtendingToSameCellKeepsViewportContent(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	m, _ = applyMsg(m, tea.MouseMotionMsg{X: 12, Y: 0})
	beforeLinesPtr := viewportLinesPtr(t, m)

	m, cmd := applyMsg(m, tea.MouseMotionMsg{X: 12, Y: 0})
	if cmd != nil {
		t.Fatalf("drag motion should not trigger a command, got %T", cmd)
	}
	if got := viewportLinesPtr(t, m); got != beforeLinesPtr {
		t.Fatal("dragging within the same transcript cell should not rebuild viewport lines")
	}
}

func TestTranscriptSelection_ClickWithNoDragCopiesNothing(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 5, Y: 0})
	m, cmd := applyMsg(m, tea.MouseReleaseMsg{X: 5, Y: 0})
	if cmd != nil {
		t.Errorf("a click with no drag should not produce a clipboard write, got a non-nil Cmd")
	}
	if m.transcriptSelecting {
		t.Error("release should always end the drag, even a no-op one")
	}
}

func TestTranscriptSelection_ClearedWhenPopupOpensMidDrag(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if !m.transcriptSelecting {
		t.Fatalf("test setup: expected an active selection")
	}
	m.cheatsheetOpen = true

	m, _ = applyMsg(m, tea.MouseMotionMsg{X: 10, Y: 0})
	if m.transcriptSelecting {
		t.Error("a popup opening mid-drag should clear the selection, not keep extending it")
	}
}

func TestTranscriptSelection_NoSelectionOnHero(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("test setup: fresh model should still be on the launch hero")
	}
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if m.transcriptSelecting {
		t.Error("clicking on the launch hero should not begin a transcript selection")
	}
}

func TestTranscriptSelection_NoopWhilePopupOpen(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.cheatsheetOpen = true
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if m.transcriptSelecting {
		t.Error("clicking while a popup is open should not begin a transcript selection")
	}
}

func TestMouseClick_DismissesStaticPopups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		open  func(*Model)
		check func(Model) bool
	}{
		{"cheatsheet", func(m *Model) { m.cheatsheetOpen = true }, func(m Model) bool { return m.cheatsheetOpen }},
		{"usage", func(m *Model) { m.usageOpen = true; m.usagePanel = "x\ny" }, func(m Model) bool { return m.usageOpen }},
		{"experimental", func(m *Model) { m.experimentalOpen = true }, func(m Model) bool { return m.experimentalOpen }},
		{"contextReport", func(m *Model) { m.contextReportOpen = true }, func(m Model) bool { return m.contextReportOpen }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			tc.open(&m)
			m, _ = applyMsg(m, tea.MouseClickMsg{X: 3, Y: 3})
			if tc.check(m) {
				t.Errorf("clicking anywhere should dismiss the %s popup", tc.name)
			}
		})
	}
}
