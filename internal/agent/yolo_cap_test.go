package agent

import (
	"math"
	"testing"
)

// yoloIterationCap must always return a finite, positive budget — the whole
// point of A4 is that yolo mode can no longer loop forever. This pins the
// floor, the scaling off the configured cap, and the overflow guard.
func TestYoloIterationCap(t *testing.T) {
	tests := []struct {
		name          string
		maxIterations int
		want          int
	}{
		{"default cap hits floor", 50, 1000},     // 50*20 == 1000
		{"small cap is floored", 1, 1000},        // 20 < floor
		{"zero falls back to floor", 0, 1000},    // misconfig guard
		{"negative falls back to floor", -5, 1000},
		{"large cap scales above floor", 500, 10000}, // 500*20
		{"overflow stays finite at floor", math.MaxInt, 1000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := yoloIterationCap(tc.maxIterations)
			if got != tc.want {
				t.Errorf("yoloIterationCap(%d) = %d, want %d", tc.maxIterations, got, tc.want)
			}
			if got <= 0 {
				t.Errorf("yoloIterationCap(%d) = %d, must be positive and finite", tc.maxIterations, got)
			}
		})
	}
}
