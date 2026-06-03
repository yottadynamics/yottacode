package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestLoop_FixedIterationCap pins the subagent iteration-budget contract:
// when FixedIterationCap is set, the effective cap equals MaxIterations
// exactly, regardless of the AutoMode/YoloMode pointers the child shares
// with its parent. Without the flag, the auto 4× and yolo expansion still
// apply (top-level loops). The Max field on the first IterationStart event
// carries the effective cap the loop computed, so we read it directly.
func TestLoop_FixedIterationCap(t *testing.T) {
	activeAuto := func() *AutoModeState { s := &AutoModeState{}; s.Active.Store(true); return s }
	activeYolo := func() *YoloModeState { s := &YoloModeState{}; s.Active.Store(true); return s }

	cases := []struct {
		name    string
		fixed   bool
		auto    *AutoModeState
		yolo    *YoloModeState
		max     int
		wantCap int
	}{
		// FixedIterationCap holds the budget at MaxIterations under every mode.
		{"fixed under auto stays at max", true, activeAuto(), nil, 40, 40},
		{"fixed under yolo stays at max", true, nil, activeYolo(), 40, 40},
		{"fixed under both stays at max", true, activeAuto(), activeYolo(), 40, 40},
		// Without the flag the existing multipliers apply (regression anchors).
		{"unfixed auto multiplies x4", false, activeAuto(), nil, 40, 160},
		{"unfixed yolo expands", false, nil, activeYolo(), 50, 1000},
		{"unfixed plain inherits max", false, nil, nil, 40, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One turn with a tool-free final reply: the loop runs a
			// single iteration and emits IterationStart{Number:1, Max:cap}
			// before finishing, which is all we need to read the cap.
			streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
				{sseDone("done")},
			}}
			cfg := LoopConfig{
				Adapter:           streamer,
				Registry:          NewRegistry(),
				MaxIterations:     tc.max,
				FixedIterationCap: tc.fixed,
				AutoMode:          tc.auto,
				YoloMode:          tc.yolo,
			}
			hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
			events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
			if err != nil {
				t.Fatalf("Turn: %v", err)
			}
			gotCap := -1
			for _, ev := range events {
				if is, ok := ev.(IterationStart); ok {
					gotCap = is.Max
					break
				}
			}
			if gotCap != tc.wantCap {
				t.Errorf("effective cap = %d, want %d", gotCap, tc.wantCap)
			}
		})
	}
}
