package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPingEndpoint_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	cmd := pingEndpoint(srv.URL)
	msg := cmd().(connectionStatusMsg)
	if msg.state != connOK {
		t.Errorf("state = %v, want connOK", msg.state)
	}
}

func TestPingEndpoint_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	msg := pingEndpoint(srv.URL)().(connectionStatusMsg)
	if msg.state != connDown {
		t.Errorf("state = %v, want connDown", msg.state)
	}
}

func TestPingEndpoint_Unreachable(t *testing.T) {
	// Use a bogus address that should fail to connect quickly.
	msg := pingEndpoint("http://127.0.0.1:1")().(connectionStatusMsg)
	if msg.state != connDown {
		t.Errorf("state = %v, want connDown for unreachable host", msg.state)
	}
}

func TestRenderConnDot_PresentForEveryState(t *testing.T) {
	// In a non-TTY test environment lipgloss strips ANSI codes, so we can't
	// assert the dots LOOK different here. The real differentiation is the
	// per-state Foreground call, which we trust. What we can verify: every
	// state still renders the bullet glyph.
	for label, state := range map[string]connState{
		"ok": connOK, "degraded": connDegraded, "down": connDown, "unknown": connUnknown,
	} {
		if got := renderConnDot(state); !strings.Contains(got, "●") {
			t.Errorf("dot for %s missing bullet glyph: %q", label, got)
		}
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
