package tui

import (
	"testing"

	"charm.land/bubbles/v2/spinner"
)

func TestNewModel_UsesUpdatedMiniDotSpinner(t *testing.T) {
	m := newTestModel(t)
	if len(m.spinner.Spinner.Frames) == 0 {
		t.Fatal("spinner should have frames")
	}
	if m.spinner.Spinner.Frames[0] != spinner.MiniDot.Frames[0] {
		t.Fatalf("spinner frame[0] = %q, want MiniDot frame %q", m.spinner.Spinner.Frames[0], spinner.MiniDot.Frames[0])
	}
}
