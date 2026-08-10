package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestPlansPicker_ClickRowResumesAndCloses(t *testing.T) {
	m := newTestModel(t)
	m.cfg.PlanMode = &agent.PlanModeState{}
	m.plansPicker = &plansPickerState{plans: []agent.PlanEntry{
		{Slug: "first-plan", Path: "/tmp/first-plan.md", Modified: time.Now()},
		{Slug: "second-plan", Path: "/tmp/second-plan.md", Modified: time.Now()},
	}}
	m.plansPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderPlansPicker(m.plansPicker, m.popupWidth(), hits))
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
	if m.plansPickerOpen {
		t.Errorf("clicking a plan row should commit and close the picker, like Enter does")
	}
	if m.cfg.PlanMode.PlanFile != "/tmp/second-plan.md" {
		t.Errorf("PlanFile = %q, want /tmp/second-plan.md", m.cfg.PlanMode.PlanFile)
	}
	if !m.cfg.PlanMode.IsActive() {
		t.Errorf("resuming a plan should activate plan mode")
	}
}
