package agent

import "testing"

// TestSubagentBackgroundDone_IntegrateReadyAndFailedWithCommit pins the
// shared "safe to integrate" rule every dispatch render site (formatResult,
// the wake-turn message, the live-dock badge) goes through: a worker can be
// BOTH Committed (an earlier, legitimate commit on its branch) AND Errored
// (e.g. it left out-of-scope changes uncommitted), and that combination
// must read as failed, never as integrate-ready.
func TestSubagentBackgroundDone_IntegrateReadyAndFailedWithCommit(t *testing.T) {
	cases := []struct {
		name          string
		committed     bool
		errored       bool
		wantReady     bool
		wantFailedCmt bool
	}{
		{"clean success", true, false, true, false},
		{"committed but errored", true, true, false, true},
		{"errored with no commit", false, true, false, false},
		{"no commit, not errored", false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := SubagentBackgroundDone{Committed: tc.committed, Errored: tc.errored}
			if got := e.IntegrateReady(); got != tc.wantReady {
				t.Errorf("IntegrateReady() = %v, want %v", got, tc.wantReady)
			}
			if got := e.FailedWithCommit(); got != tc.wantFailedCmt {
				t.Errorf("FailedWithCommit() = %v, want %v", got, tc.wantFailedCmt)
			}
			// The free-function forms (used by dispatch's own formatResult,
			// which works from *dispatchChild fields, not this type) must
			// agree with the methods for the same inputs.
			if got := integrateReady(tc.committed, tc.errored); got != tc.wantReady {
				t.Errorf("integrateReady(%v, %v) = %v, want %v", tc.committed, tc.errored, got, tc.wantReady)
			}
			if got := failedWithCommit(tc.committed, tc.errored); got != tc.wantFailedCmt {
				t.Errorf("failedWithCommit(%v, %v) = %v, want %v", tc.committed, tc.errored, got, tc.wantFailedCmt)
			}
		})
	}
}
