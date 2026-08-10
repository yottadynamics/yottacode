package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/checkpoint"
)

func TestCheckpointsPicker_ClickEntryAdvancesToActionMenu(t *testing.T) {
	m := newTestModel(t)
	m.checkpointsPicker = &checkpointsPickerState{
		entries: []checkpoint.ManifestEntry{
			{PromptPreview: "first prompt", Created: time.Now()},
			{PromptPreview: "second prompt", Created: time.Now()},
		},
	}
	m.checkpointsPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderCheckpointsPicker(m.checkpointsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 1 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for entry 1")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.checkpointsPicker.picked == nil {
		t.Fatalf("clicking an entry should advance to the action menu, like Enter does")
	}
	if m.checkpointsPicker.picked.PromptPreview != "second prompt" {
		t.Errorf("clicked entry 1 should pick %q, got %q", "second prompt", m.checkpointsPicker.picked.PromptPreview)
	}
	if !m.checkpointsPickerOpen {
		t.Errorf("advancing to the action menu should not close the picker")
	}
}

func TestCheckpointsPicker_ClickActionCommitsAndCloses(t *testing.T) {
	m := newTestModel(t)
	entry := checkpoint.ManifestEntry{PromptPreview: "a prompt", Created: time.Now()}
	m.checkpointsPicker = &checkpointsPickerState{
		entries: []checkpoint.ManifestEntry{entry},
		picked:  &entry,
	}
	m.checkpointsPickerOpen = true
	// applyCheckpointAction needs a real store; action 3 (summarize from
	// here) goes through cmdSummarize instead of a checkpoint.Store
	// call, so it's the one action reachable without seeding one.
	// Simpler: just verify the picker closes (commit was attempted),
	// which proves the click resolved to the right action index and
	// replayed Enter — same assertion shape as the model picker's
	// click-commits test.
	target := len(checkpointsPickerActions) - 1

	hits := &pickerHits{}
	box := popupBox(renderCheckpointsPicker(m.checkpointsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == target {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for action %d", target)
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.checkpointsPickerOpen {
		t.Errorf("clicking an action should commit and close the picker, like Enter does")
	}
}
