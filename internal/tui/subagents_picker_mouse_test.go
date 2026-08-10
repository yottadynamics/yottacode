package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

func TestSubagentsPicker_ClickTaskRowOpensAndCloses(t *testing.T) {
	m := newTestModel(t)
	m.subagentsPicker = &subagentsPickerState{
		mode: subagentsPickerModeTasks,
		tasks: []subagents.Task{
			{ID: "aaaaaaaa1111", AgentType: "review", Status: subagents.TaskCompleted, Started: time.Now(), TranscriptPath: "/no/such/transcript.log"},
			{ID: "bbbbbbbb2222", AgentType: "review", Status: subagents.TaskCompleted, Started: time.Now(), TranscriptPath: "/no/such/transcript2.log"},
		},
	}
	m.subagentsPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderSubagentsPicker(m.subagentsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 1 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 1")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.subagentsPickerOpen {
		t.Errorf("clicking a task row should commit and close the picker, like Enter does")
	}
}
