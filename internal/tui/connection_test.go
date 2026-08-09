package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// probeConnectionState maps an active probe to the dot color. The key
// rule for production: a reachable, authenticated endpoint whose only
// problem is model visibility is degraded (amber), NOT down (red) — that
// was the false-red the Anthropic/Gemini/Ollama probes used to trip.
func TestProbeConnectionState(t *testing.T) {
	cases := []struct {
		name string
		in   adapter.ProbeResult
		want connState
	}{
		{
			name: "reachable+authed+clean",
			in:   adapter.ProbeResult{EndpointReachable: true, AuthOK: true},
			want: connOK,
		},
		{
			name: "reachable+authed+model-not-visible",
			in:   adapter.ProbeResult{EndpointReachable: true, AuthOK: true, Issues: []string{`model "x" not listed by /models`}},
			want: connDegraded,
		},
		{
			name: "reachable+auth-failed",
			in:   adapter.ProbeResult{EndpointReachable: true, AuthOK: false, Issues: []string{"authentication failed (HTTP 401)"}},
			want: connDown,
		},
		{
			name: "unreachable",
			in:   adapter.ProbeResult{EndpointReachable: false, Issues: []string{"endpoint unreachable"}},
			want: connDown,
		},
	}
	for _, tc := range cases {
		if got := probeConnectionState(tc.in); got != tc.want {
			t.Errorf("%s: probeConnectionState = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Regression: every state used to render the identical "●" glyph, colored
// differently — invisible under NO_COLOR or to a colorblind user. Each
// state must now have its own distinct, plain-text glyph so shape alone
// (not just color) tells them apart.
func TestRenderConnDot_DistinctGlyphPerState(t *testing.T) {
	seen := map[string]connState{}
	for label, state := range map[string]connState{
		"ok": connOK, "degraded": connDegraded, "down": connDown, "unknown": connUnknown,
	} {
		got := stripANSI(renderConnDot(state))
		if got == "" {
			t.Errorf("dot for %s is empty", label)
			continue
		}
		if prior, dup := seen[got]; dup {
			t.Errorf("dot for %s (%q) is identical to %v — states must be shape-distinguishable without color", label, got, prior)
		}
		seen[got] = state
	}
}

// The healthy state keeps its historical "●" glyph — only the other three
// states change shape, so the common-case status bar look is unchanged.
func TestRenderConnDot_OKKeepsFilledBullet(t *testing.T) {
	if got := stripANSI(renderConnDot(connOK)); !strings.Contains(got, "●") {
		t.Errorf("connOK dot = %q, want the filled bullet ●", got)
	}
}

// renderConnectionSummary covers all four states with distinct,
// human-readable labels. Used in /provider details output.
func TestRenderConnectionSummary_AllStates(t *testing.T) {
	cases := map[connState]string{
		connOK:       "reachable",
		connDegraded: "degraded",
		connDown:     "unreachable",
		connUnknown:  "unknown",
	}
	for state, want := range cases {
		if got := renderConnectionSummary(state); got != want {
			t.Errorf("renderConnectionSummary(%v) = %q, want %q", state, got, want)
		}
	}
}
