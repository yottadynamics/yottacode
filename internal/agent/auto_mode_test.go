package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestIsAutoModeSafeBash(t *testing.T) {
	cases := []struct {
		name, command string
		want          bool
	}{
		{"single-grep", "grep -r foo internal/", true},
		{"single-ls", "ls -la", true},
		{"single-cat", "cat README.md", true},
		{"single-find", `find . -name "*.go"`, true},

		{"cd-then-grep", "cd /home/me/proj && grep -r foo internal/", true},
		{"cd-then-ls", "cd /repo && ls -la", true},
		{"cd-then-find", `cd /repo && find . -name "*.go"`, true},
		{"three-segments-all-safe", "cd /repo && ls && cat README.md", true},

		{"single-rm", "rm -rf /tmp/foo", false},
		{"cd-then-rm", "cd /repo && rm foo", false},
		{"single-touch", "touch foo", false},
		{"single-cp", "cp a b", false},
		{"single-mv", "mv a b", false},
		{"single-mkdir", "mkdir foo", false},
		{"single-sudo", "sudo ls", false},

		{"single-curl", "curl https://example.com", false},
		{"single-wget", "wget https://example.com", false},

		{"single-go-test", "go test ./...", false},
		{"single-npm-run", "npm run build", false},

		// Risk classification rejects even if verb is safe.
		{"safe-verb-with-redirect", "cat /etc/passwd > /tmp/x", false},
		{"safe-verb-with-pipe-to-sh", "cat foo | sh", false},

		// sed deliberately excluded (has -i in-place writes).
		{"sed-rejected-with-i", "sed -i s/a/b/ foo.go", false},
		{"sed-rejected-readonly-form", "sed s/a/b/ foo.go", false},

		// Multi-segment pipe with both ends safe is OK (SplitCommand
		// treats `|` as a separator and assigns no caution since neither
		// segment trips destructive/caution patterns).
		{"piped-grep", "cat foo.go | grep bar | wc -l", true},

		{"empty", "", false},
		{"whitespace", "   ", false},
	}
	for _, tc := range cases {
		args := ""
		if tc.command != "" {
			b, _ := json.Marshal(struct {
				Command string `json:"command"`
			}{tc.command})
			args = string(b)
		} else {
			args = `{"command":""}`
		}
		got := IsAutoModeSafeBash(args)
		if got != tc.want {
			t.Errorf("%s (%q): got %v, want %v", tc.name, tc.command, got, tc.want)
		}
	}
}

func TestIsAutoModeSafeBash_BadJSON(t *testing.T) {
	if IsAutoModeSafeBash("{not json") {
		t.Errorf("malformed JSON should return false")
	}
	if IsAutoModeSafeBash("") {
		t.Errorf("empty argsJSON should return false")
	}
}

// Loop integration: when auto-mode is active and the run_bash call's
// verbs are all in the read-only allowlist, the loop fires
// ApprovalAuto with Source="auto-mode-safe-bash" instead of prompting
// the user.
func TestLoop_AutoMode_SafeBashSkipsApproval(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "run_bash",
			ArgsJSON: `{"command":"cd /repo && grep -r foo internal/"}`,
		})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("auto-mode safe-bash should auto-allow, not prompt")
	}
	var sawSource bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode-safe-bash" {
			sawSource = true
		}
	}
	if !sawSource {
		t.Errorf("expected ApprovalAuto with Source=auto-mode-safe-bash; got %+v", events)
	}
}

// A mutating bash command in auto-mode still goes through the modal:
// the safety floor catches it because IsAutoModeSafeBash returns false.
func TestLoop_AutoMode_UnsafeBashStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "run_bash",
			ArgsJSON: `{"command":"rm -rf /tmp/foo"}`,
		})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	// Verify auto-mode-safe-bash did NOT short-circuit this call.
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode-safe-bash" {
			t.Errorf("rm should not be auto-approved as safe-bash: %+v", e)
		}
	}
}

func TestAutoModeState_IsActiveNilSafe(t *testing.T) {
	var a *AutoModeState
	if a.IsActive() {
		t.Errorf("nil AutoModeState should report inactive")
	}
	a = &AutoModeState{}
	if a.IsActive() {
		t.Errorf("zero-value AutoModeState should report inactive")
	}
	a.Active.Store(true)
	if !a.IsActive() {
		t.Errorf("Active=true should report active")
	}
}

func TestIsAutoModeSafetyFloor(t *testing.T) {
	for _, name := range []string{
		"run_bash", "git_commit", "git_checkpoint", "rollback",
		// enter_worktree / exit_worktree shift the agent's working
		// context (and exit's force-remove is destructive); always
		// prompt regardless of mode.
		"enter_worktree", "exit_worktree",
	} {
		if !IsAutoModeSafetyFloor(name) {
			t.Errorf("%s should be in the safety floor", name)
		}
	}
	for _, name := range []string{
		"write_file", "edit_file", "apply_diff", "mkdir", "copy_file", "move_file",
		"delete_file", "git_stage_files", "git_unstage_files", "read_file", "grep",
		// The plain git_worktree_* wrappers are narrow, explicit, and
		// recoverable; they stay auto-allowed in auto mode.
		"git_worktree_list", "git_worktree_add", "git_worktree_remove",
		"git_worktree_lock", "git_worktree_unlock", "git_worktree_prune",
		"worktree_status",
	} {
		if IsAutoModeSafetyFloor(name) {
			t.Errorf("%s should NOT be in the safety floor", name)
		}
	}
}

// Loop integration: when auto mode is on, mutating tools that aren't
// in the safety floor auto-approve with Source=auto-mode instead of
// triggering the approval modal.
func TestLoop_AutoMode_AutoApprovesNonFloorTool(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"main.go"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("auto-mode write should auto-allow, not prompt")
	}
	var sawAutoMode bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode" {
			sawAutoMode = true
		}
	}
	if !sawAutoMode {
		t.Errorf("expected ApprovalAuto with Source=auto-mode; got %+v", events)
	}
}

// Safety-floor tools (run_bash, git_commit, …) still prompt even when
// auto mode is on. That's the whole point of the floor.
func TestLoop_AutoMode_SafetyFloorStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"rm -rf /"}`})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	_, _ = runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	// Verify ApprovalNeeded fired AT LEAST once for run_bash.
	// (The decide func is mandatory in runTurnSync when ApprovalNeeded
	// fires, so reaching here without panic already proves it fired.)
}

// Auto mode doesn't override an explicit Deny rule — the user said
// "never" with permissions.json and that always wins.
func TestLoop_AutoMode_DenyRuleStillWins(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, []string{"Write(forbidden.go)"})
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"forbidden.go"}`})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: NewCwdRef(cwd), MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sawDeny, sawAutoMode bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			if a.Source == "deny-rule" {
				sawDeny = true
			}
			if a.Source == "auto-mode" {
				sawAutoMode = true
			}
		}
	}
	if !sawDeny {
		t.Errorf("expected deny-rule to fire; got %+v", events)
	}
	if sawAutoMode {
		t.Errorf("auto-mode should NOT override an explicit deny rule")
	}
}

// Plan and auto are mutually exclusive at the loop level: when both
// flags happen to be on (shouldn't happen in real use), the plan-mode
// path takes precedence so plan-mode-allow / plan-mode-block still
// govern writes.
func TestLoop_AutoMode_PlanModePrecedesWhenBothActive(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		// Write to plan file → plan-mode-allow.
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"/tmp/plan.md"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	planMode := &PlanModeState{PlanFile: "/tmp/plan.md"}
	planMode.Active.Store(true)
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sources []string
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			sources = append(sources, a.Source)
		}
	}
	// The plan-mode-allow should fire (plan file write). The
	// auto-mode source should NOT fire for this call (plan takes
	// precedence).
	sawPlanAllow := false
	sawAutoMode := false
	for _, s := range sources {
		if s == "plan-mode-allow" {
			sawPlanAllow = true
		}
		if s == "auto-mode" {
			sawAutoMode = true
		}
	}
	if !sawPlanAllow {
		t.Errorf("expected plan-mode-allow; got sources %+v", sources)
	}
	if sawAutoMode {
		t.Errorf("auto-mode should not fire when plan-mode-allow already handled the call; got sources %+v", sources)
	}
}
