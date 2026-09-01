package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestLoop_ApprovalAllowSession_GrantsFutureCallsWithoutTouchingDisk is the
// core contract of the "[S] allow for this session" answer: it behaves
// like AllowAlways for the rest of the turn (a later matching call
// auto-approves, no second prompt) but — unlike AllowAlways — never
// writes permissions.local.json. See
// TestLoop_ApprovalAllowAlwaysAppendsToPermissionsLocal for the disk-write
// case this deliberately does NOT reproduce.
func TestLoop_ApprovalAllowSession_GrantsFutureCallsWithoutTouchingDisk(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"go test ./..."}`})},
		{sseDone("", adapter.ToolCall{ID: "c2", Name: "run_bash", ArgsJSON: `{"command":"go build ./..."}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "x"})
	cfg := LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		Cwd: NewCwdRef(cwd), MaxIterations: 5,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var prompts int
	events, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		prompts++
		if prompts > 1 {
			t.Fatalf("second call should auto-approve from the session grant, not prompt again")
		}
		return AllowSession
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if prompts != 1 {
		t.Errorf("expected exactly 1 prompt (the first call); got %d", prompts)
	}

	// The second call's approval must be sourced from the session grant.
	var sawSessionAuto bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "permissions" && a.RuleSource == "session" {
			sawSessionAuto = true
		}
	}
	if !sawSessionAuto {
		t.Errorf("expected an ApprovalAuto sourced from the session grant; got %+v", events)
	}

	// Nothing must be written to disk — that's the whole point of [S].
	if _, err := os.Stat(filepath.Join(cwd, ".yottacode", "permissions.local.json")); !os.IsNotExist(err) {
		t.Errorf("permissions.local.json should not exist after AllowSession, stat err = %v", err)
	}
}
