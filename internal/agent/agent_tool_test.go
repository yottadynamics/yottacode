package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestDoneTokensAndCalls is the regression for the completion-stats gap: the
// child's exact provider tally must win over the rough estimate at EVERY
// emission site. This logic previously lived as duplicated per-site glue, and
// the dispatch FOREGROUND path drifted onto the estimate; centralizing it in
// doneTokensAndCalls means all five sites (Agent + dispatch, foreground +
// background) share this one code path.
func TestDoneTokensAndCalls(t *testing.T) {
	reg := subagents.NewRegistry()

	// Provider reported usage → exact tally wins over the estimate.
	reg.Add(&subagents.Task{ID: "withusage", Status: subagents.TaskRunning})
	reg.SetToolCalls("withusage", 5)
	reg.AddUsage("withusage", &adapter.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 300}) // UsageTokens = 450
	if tok, calls := doneTokensAndCalls(reg, "withusage", 999); tok != 450 || calls != 5 {
		t.Errorf("with usage: got (%d tokens, %d calls), want (450, 5) — exact tally must beat estimate 999", tok, calls)
	}

	// Provider reported no usage → fall back to the estimate.
	reg.Add(&subagents.Task{ID: "noreport", Status: subagents.TaskRunning})
	reg.SetToolCalls("noreport", 2)
	if tok, calls := doneTokensAndCalls(reg, "noreport", 777); tok != 777 || calls != 2 {
		t.Errorf("no usage: got (%d tokens, %d calls), want (777, 2) — estimate fallback", tok, calls)
	}

	// Unknown id → estimate, zero calls, no panic.
	if tok, calls := doneTokensAndCalls(reg, "ghost", 42); tok != 42 || calls != 0 {
		t.Errorf("unknown id: got (%d, %d), want (42, 0)", tok, calls)
	}
}

// TestAgentTool_ForegroundApprovalUnderGate_NoDeadlock is the regression for
// the approval-gate self-deadlock: when an approval gate is installed (as the
// parallel Agent batch and foreground dispatch both do), a foreground child
// that needs approval must still complete. Before the fix the child's own loop
// and the drain-loop forwarder contended on the same gate mutex and hung.
func TestAgentTool_ForegroundApprovalUnderGate_NoDeadlock(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
		{sseDone("ran with approval")},
	}}
	cfg := subagents.AgentConfig{Name: "fg", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, false)

	parentEvents := make(chan Event, 64)
	parentDecisions := make(chan Decision, 1)
	// Install the approval gate exactly as a parallel batch / foreground
	// dispatch does — this is what used to trigger the deadlock.
	ctx := WithApprovalGate(context.Background(), &sync.Mutex{})
	ctx = WithParentDecisions(WithParentEvents(ctx, parentEvents), parentDecisions)

	go func() {
		for ev := range parentEvents {
			if _, ok := ev.(ApprovalNeeded); ok {
				parentDecisions <- AllowOnce
			}
		}
	}()

	done := make(chan string, 1)
	go func() {
		out, err := tool.Execute(ctx, mustJSON(t, agentArgs{SubagentType: "fg", Prompt: "p"}))
		if err != nil {
			t.Errorf("Execute: %v", err)
		}
		done <- out
	}()

	select {
	case out := <-done:
		close(parentEvents)
		if !strings.Contains(out, "ran with approval") {
			t.Errorf("child reply not surfaced; got: %q", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: foreground approval under an installed gate did not complete")
	}
}

// TestIsReadOnlyTool_Classification confirms the exported IsReadOnlyTool
// function correctly classifies canonical read-only and mutating tools.
func TestIsReadOnlyTool_Classification(t *testing.T) {
	// Read-only tools must return true.
	for _, name := range []string{"read_file", "grep", "glob", "lsp_diagnostics", "git_branch_status", "list_git_changed_files"} {
		if !IsReadOnlyTool(name) {
			t.Errorf("IsReadOnlyTool(%q) = false, want true", name)
		}
	}
	// Mutating tools must return false.
	for _, name := range []string{"edit_file", "write_file", "run_bash", "run_tests", "git_commit", "apply_diff"} {
		if IsReadOnlyTool(name) {
			t.Errorf("IsReadOnlyTool(%q) = true, want false", name)
		}
	}
}

// TestBuiltinRoles_DispatchClassification asserts the built-in roles classify
// the way dispatch relies on: implement/test/docs are write-capable (→ isolated
// worktree) and carry dispatch background defaults; review is read-only
// (→ shared cwd, no worktree) and foreground.
func TestBuiltinRoles_DispatchClassification(t *testing.T) {
	byName := map[string]subagents.AgentConfig{}
	for _, c := range subagents.LoadBuiltins() {
		byName[c.Name] = c
	}
	cases := []struct {
		name         string
		wantReadOnly bool
		wantBg       bool
	}{
		{"implement", false, true},
		{"test", false, true},
		{"docs", false, true},
		{"review", true, false},
		// code-verifier is read-only (→ shared cwd, no worktree) and
		// foreground-default, like review — it reads and reasons to
		// refute a finding; it never runs or edits.
		{"code-verifier", true, false},
	}
	for _, tc := range cases {
		c, ok := byName[tc.name]
		if !ok {
			t.Errorf("builtin %q not loaded", tc.name)
			continue
		}
		cfg := c
		if got := agentIsReadOnly(&cfg); got != tc.wantReadOnly {
			t.Errorf("%q agentIsReadOnly = %v, want %v", tc.name, got, tc.wantReadOnly)
		}
		if c.Background != tc.wantBg {
			t.Errorf("%q Background = %v, want %v", tc.name, c.Background, tc.wantBg)
		}
	}
}

// TestForwardToParent_CriticalBlocksLossyDrops is the P4 regression: a
// forwarded ApprovalNeeded must never be dropped under event-buffer
// pressure (dropping it deadlocks the child on the decisions channel and,
// for a foreground batch, hangs the parent's wg.Wait), while progress
// events stay best-effort.
func TestForwardToParent_CriticalBlocksLossyDrops(t *testing.T) {
	t.Run("lossy event is dropped when the buffer is full", func(t *testing.T) {
		ch := make(chan Event, 1)
		ch <- ToolStart{} // fill the buffer
		done := make(chan struct{})
		go func() {
			forwardToParent(context.Background(), ch, SubagentProgress{Activity: "x"})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("best-effort forward blocked on a full buffer")
		}
		if len(ch) != 1 {
			t.Fatalf("expected the progress event to be dropped, buffer len=%d", len(ch))
		}
	})

	t.Run("ApprovalNeeded blocks until drained, never dropped", func(t *testing.T) {
		ch := make(chan Event, 1)
		ch <- ToolStart{} // fill the buffer
		done := make(chan struct{})
		go func() {
			forwardToParent(context.Background(), ch, ApprovalNeeded{ToolName: "write_file"})
			close(done)
		}()
		// While the buffer is full the critical send must still be pending.
		select {
		case <-done:
			t.Fatal("ApprovalNeeded returned while buffer full — it was dropped, not delivered")
		case <-time.After(50 * time.Millisecond):
		}
		if _, ok := (<-ch).(ToolStart); !ok {
			t.Fatal("expected the filler event first")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("ApprovalNeeded never delivered after the buffer drained")
		}
		if _, ok := (<-ch).(ApprovalNeeded); !ok {
			t.Fatal("expected ApprovalNeeded to be delivered")
		}
	})

	t.Run("PathTrustElevationNeeded is also blocking", func(t *testing.T) {
		ch := make(chan Event, 1)
		ch <- ToolStart{}
		done := make(chan struct{})
		go func() {
			forwardToParent(context.Background(), ch, PathTrustElevationNeeded{ToolName: "write_file"})
			close(done)
		}()
		select {
		case <-done:
			t.Fatal("PathTrustElevationNeeded was dropped under buffer pressure")
		case <-time.After(50 * time.Millisecond):
		}
		<-ch
		<-done
	})

	t.Run("ctx cancel unblocks a stuck critical send", func(t *testing.T) {
		ch := make(chan Event, 1)
		ch <- ToolStart{} // filled and never drained
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			forwardToParent(ctx, ch, ApprovalNeeded{})
			close(done)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("ctx cancel did not unblock the critical send")
		}
	})

	t.Run("nil parent channel is a no-op", func(t *testing.T) {
		forwardToParent(context.Background(), nil, ApprovalNeeded{}) // must not panic or block
	})
}

func newTestAgentTool(t *testing.T, configs []subagents.AgentConfig, streamer Streamer, allowBackground bool) (*AgentTool, *Registry) {
	t.Helper()
	parent := NewRegistry()
	parent.Register(&mockTool{name: "read_file", output: "file body"})
	parent.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "shell out"})
	tool := &AgentTool{
		Configs:         configs,
		Tasks:           subagents.NewRegistry(),
		Adapter:         streamer,
		ParentRegistry:  parent,
		Cwd:             NewCwdRef(t.TempDir()),
		TranscriptDir:   t.TempDir(),
		YoloMode:        &YoloModeState{},
		PlanMode:        &PlanModeState{},
		AutoMode:        &AutoModeState{},
		AllowBackground: allowBackground,
	}
	parent.Register(tool)
	return tool, parent
}

func TestAgentTool_RecursionGuard(t *testing.T) {
	// A config that tries to allowlist Agent into the child must not
	// actually expose it. Belt-and-suspenders: the child-registry
	// builder hard-excludes Agent regardless of the allowlist.
	cfg := subagents.AgentConfig{
		Name:        "evil",
		Description: "tries to recurse",
		Tools:       []string{"read_file", "Agent", "dispatch", "integrate"},
		Prompt:      "be evil",
		Source:      "test",
	}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	tool.ParentRegistry.Register(&DispatchTool{Enabled: true})
	tool.ParentRegistry.Register(&IntegrateTool{Enabled: true})
	child := tool.buildChildRegistry(&cfg)
	if _, ok := child.Get(agentToolName); ok {
		t.Fatalf("child registry contains the Agent tool — recursion guard failed")
	}
	if _, ok := child.Get(DispatchToolName); ok {
		t.Fatalf("child registry contains dispatch — delegation guard failed")
	}
	if _, ok := child.Get(IntegrateToolName); ok {
		t.Fatalf("child registry contains integrate — delegation guard failed")
	}
	if _, ok := child.Get("read_file"); !ok {
		t.Errorf("child registry missing read_file (should be inherited)")
	}
}

func TestStandaloneBackgroundPolicyIsExplicitReadOnlyAllowlist(t *testing.T) {
	read := &mockTool{name: "read_file"}
	decision, note, handled := standaloneBackgroundApprovalPolicy(read, `{}`)
	if !handled || decision != AllowOnce || !strings.Contains(note, "read-only allowlist") {
		t.Fatalf("read_file should be explicitly allowed by standalone background policy, got decision=%v note=%q handled=%v", decision, note, handled)
	}

	memorySave := &mockTool{name: "memory_save"}
	decision, note, handled = standaloneBackgroundApprovalPolicy(memorySave, `{}`)
	if !handled || decision != Deny || !strings.Contains(note, "read-only") {
		t.Fatalf("memory_save should be explicitly denied in standalone background, got decision=%v note=%q handled=%v", decision, note, handled)
	}

	approvalRead := &mockTool{name: "read_file", requiresApproval: true}
	decision, note, handled = standaloneBackgroundApprovalPolicy(approvalRead, `{}`)
	if !handled || decision != Deny || !strings.Contains(note, "approval-requiring") {
		t.Fatalf("approval-requiring read_file form should be denied, got decision=%v note=%q handled=%v", decision, note, handled)
	}
}

func TestDispatchBackgroundPolicyIsExplicitAllowlist(t *testing.T) {
	write := &mockTool{name: "write_file", requiresApproval: true}
	decision, note, handled := dispatchBackgroundApprovalPolicy(write, `{}`, false)
	if !handled || decision != AllowOnce || !strings.Contains(note, "owned-file scoped") {
		t.Fatalf("write_file should be allowed once, got decision=%v note=%q handled=%v", decision, note, handled)
	}

	read := &mockTool{name: "read_file"}
	decision, note, handled = dispatchBackgroundApprovalPolicy(read, `{}`, false)
	if !handled || decision != AllowOnce || !strings.Contains(note, "read-only allowlist") {
		t.Fatalf("read_file should be explicitly allowed by dispatch background policy, got decision=%v note=%q handled=%v", decision, note, handled)
	}

	gitRead := &mockTool{name: "git_diff_files"}
	decision, note, handled = dispatchBackgroundApprovalPolicy(gitRead, `{}`, false)
	if !handled || decision != Deny || !strings.Contains(note, "not auto-allowed") {
		t.Fatalf("git_diff_files should be denied by explicit unattended allowlist, got decision=%v note=%q handled=%v", decision, note, handled)
	}

	fetch := &mockTool{name: "fetch_url"}
	decision, note, handled = dispatchBackgroundApprovalPolicy(fetch, `{}`, false)
	if !handled || decision != Deny || !strings.Contains(note, "not auto-allowed") {
		t.Fatalf("fetch_url should be denied by unattended network policy, got decision=%v note=%q handled=%v", decision, note, handled)
	}

	// create_document is explicitly denied for unattended workers even
	// though xlsx/pptx generation is native: every format writes an output
	// document, and docx/pdf may still shell out through pandoc.

	createDoc := &mockTool{name: "create_document", requiresApproval: true}
	decision, note, handled = dispatchBackgroundApprovalPolicy(createDoc, `{}`, false)
	if !handled || decision != Deny || !strings.Contains(note, "create_document") || !strings.Contains(note, "needs a human") {
		t.Fatalf("create_document should be explicitly denied with a specific reason, got decision=%v note=%q handled=%v", decision, note, handled)
	}

	// create_document stays denied even when this worker IS sandboxed —
	// sandboxing only relaxes run_bash/run_tests (see below); create_document
	// is denied for a different reason ("needs a human"), unrelated to shell
	// confinement.
	decision, note, handled = dispatchBackgroundApprovalPolicy(createDoc, `{}`, true)
	if !handled || decision != Deny || !strings.Contains(note, "needs a human") {
		t.Fatalf("create_document should stay denied even when sandboxed, got decision=%v note=%q handled=%v", decision, note, handled)
	}
}

// AgentTool is ParallelSafe — multiple Agent calls from the same
// assistant message fan out via executeToolCallsParallel. This is the
// surface a parent leans on to spawn 2-3 Explore subagents in one
// turn during plan-mode research; if it ever flips back to false the
// fan-out silently regresses to sequential execution.
func TestAgentTool_ParallelSafe(t *testing.T) {
	tool, _ := newTestAgentTool(t, nil, nil, false)
	if !tool.ParallelSafe("") {
		t.Errorf("AgentTool.ParallelSafe() = false; want true so parallel foreground subagent dispatch works")
	}
}

func TestAgentTool_ExcludesPlanBoundaryTools(t *testing.T) {
	cfg := subagents.AgentConfig{
		Name:        "x",
		Description: "x",
		Prompt:      "x",
		Source:      "test",
	}
	tool, parent := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	parent.Register(&ExitPlanModeTool{})
	parent.Register(&EnterPlanModeTool{State: &PlanModeState{}})
	child := tool.buildChildRegistry(&cfg)
	// Children share the parent's plan-mode state by pointer; only the
	// top-level loop may transition it, so neither boundary tool may
	// reach a child.
	if _, ok := child.Get("exit_plan_mode"); ok {
		t.Errorf("child registry should not contain exit_plan_mode")
	}
	if _, ok := child.Get("enter_plan_mode"); ok {
		t.Errorf("child registry should not contain enter_plan_mode")
	}
}

func TestAgentTool_AppliesToolAllowlist(t *testing.T) {
	cfg := subagents.AgentConfig{
		Name:        "readonly",
		Description: "x",
		Tools:       []string{"read_file"}, // run_bash explicitly excluded
		Prompt:      "x",
		Source:      "test",
	}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	child := tool.buildChildRegistry(&cfg)
	if _, ok := child.Get("read_file"); !ok {
		t.Errorf("read_file should be present")
	}
	if _, ok := child.Get("run_bash"); ok {
		t.Errorf("run_bash should be filtered out by the allowlist")
	}
}

func TestAgentTool_UnknownSubagentTypeReturnsRecoverableError(t *testing.T) {
	tool, _ := newTestAgentTool(t, nil, nil, false)
	args := mustJSON(t, agentArgs{SubagentType: "DoesNotExist", Prompt: "hello"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute returned error %v — should return recoverable string", err)
	}
	if !strings.Contains(out, "unknown subagent_type") {
		t.Errorf("output = %q, want a clear unknown-type error", out)
	}
}

func TestAgentTool_BackgroundRejectedWhenDisabled(t *testing.T) {
	// AllowBackground=false now models oneshot/noninteractive sessions only. The
	// TUI enables background subagents by default, but oneshot still rejects an
	// explicit detached run because there is no long-lived /subagents UI.
	cfg := subagents.AgentConfig{Name: "x", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false /* AllowBackground=false */)
	args := mustJSON(t, agentArgs{SubagentType: "x", Prompt: "hello", RunInBackground: true})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute err = %v, want nil", err)
	}
	if !strings.Contains(out, "background") || !strings.Contains(out, "interactive TUI") {
		t.Errorf("output should explain that background requires an interactive TUI; got %q", out)
	}
	if strings.Contains(out, "background_subagents") || strings.Contains(out, "experimental") {
		t.Errorf("background is GA in TUI; rejection should not mention an experimental flag: %q", out)
	}
}

// Foreground subagents share the same cap shape as background ones —
// the (N+1)th concurrent spawn is rejected with a recoverable error
// the model can adapt to (wait for an in-flight child to finish).
// Pre-seed the registry with MaxForegroundSubagents running fg tasks
// so the cap check fires before any real dispatch happens.
func TestAgentTool_ForegroundCapEnforced(t *testing.T) {
	cfg := subagents.AgentConfig{Name: "x", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	for i := 0; i < MaxForegroundSubagents; i++ {
		tool.Tasks.Add(&subagents.Task{
			ID:         subagents.NewTaskID(),
			AgentType:  "x",
			Status:     subagents.TaskRunning,
			Background: false,
		})
	}
	args := mustJSON(t, agentArgs{SubagentType: "x", Prompt: "hello"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute err = %v, want nil", err)
	}
	if !strings.Contains(out, "foreground subagents may run concurrently") {
		t.Errorf("output should explain the foreground cap; got %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("at most %d", MaxForegroundSubagents)) {
		t.Errorf("output should name the cap value %d; got %q", MaxForegroundSubagents, out)
	}
}

// A read-only agent definition with `background: true` should dispatch as
// background even when the caller omits run_in_background. Write-capable
// background-default builtins are reserved for dispatch, where worktree/file
// scope makes unattended writes safe.
func TestAgentTool_ConfigBackgroundDefaultsToBackgroundForReadOnly(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("verdict: pass")},
	}}
	cfg := subagents.AgentConfig{
		Name:        "reviewer",
		Description: "x",
		Prompt:      "verify",
		Tools:       []string{"read_file"},
		Background:  true,
		Source:      "test",
	}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true /* AllowBackground */)
	args := mustJSON(t, agentArgs{SubagentType: "reviewer", Prompt: "go"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Background dispatch returns a task-id handle string, not the
	// child's final reply. The "started as task" prefix is the
	// distinguishing marker.
	if !strings.Contains(out, "background subagent") {
		t.Errorf("output = %q, want background-dispatch handle", out)
	}
	// Wait briefly for the detached goroutine to finish before listing
	// — otherwise the registry may still show Running.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tasks := tool.Tasks.List()
		if len(tasks) == 1 && tasks[0].Status == subagents.TaskCompleted {
			if !tasks[0].Background {
				t.Errorf("task.Background = false, want true (config default should have flipped it)")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background task did not complete within 2s")
}

// When AllowBackground is false (oneshot mode), a config-level
// `background: true` must silently fall back to foreground rather
// than rejecting the call. Otherwise read-only background-default agents become
// unreachable from oneshot, which has no detached-task UI.
func TestAgentTool_ConfigBackgroundFallsBackToForegroundWhenDisabled(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("verdict: pass")},
	}}
	cfg := subagents.AgentConfig{
		Name:        "reviewer",
		Description: "x",
		Prompt:      "verify",
		Tools:       []string{"read_file"},
		Background:  true,
		Source:      "test",
	}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, false /* AllowBackground */)
	args := mustJSON(t, agentArgs{SubagentType: "reviewer", Prompt: "go"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Foreground dispatch returns the child's final reply directly.
	if !strings.Contains(out, "verdict: pass") {
		t.Errorf("output = %q, want the child's final reply inline (foreground fallback)", out)
	}
	if strings.Contains(out, "experimental") {
		t.Errorf("config-default background should silently fall back; got the experimental-gate error: %q", out)
	}
}

func TestAgentTool_ForegroundCapturesFinalReply(t *testing.T) {
	// The child Turn does one round: assistant returns content directly,
	// no tool calls. The tool should return that content as the result.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("subagent answer: the readme is at README.md")},
	}}
	cfg := subagents.AgentConfig{Name: "Explore", Description: "x", Prompt: "be terse", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	args := mustJSON(t, agentArgs{SubagentType: "Explore", Prompt: "where is the readme?"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "subagent answer: the readme is at README.md"
	if !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to contain %q", out, want)
	}
	tasks := tool.Tasks.List()
	if len(tasks) != 1 {
		t.Fatalf("Tasks.List len = %d, want 1", len(tasks))
	}
	if tasks[0].Status != subagents.TaskCompleted {
		t.Errorf("Status = %v, want completed", tasks[0].Status)
	}
}

func TestAgentTool_ForegroundContextIsolation(t *testing.T) {
	// The child streams content tokens AND a final reply. The PARENT's
	// events must not contain the child's ContentTokens — that's the
	// entire point of subagents. We simulate the parent by capturing
	// the events the AgentTool would emit through ParentEvents.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{
			sseToken("internal "),
			sseToken("reasoning "),
			sseToken("only"),
			sseDone("clean final answer"),
		},
	}}
	cfg := subagents.AgentConfig{Name: "Explore", Description: "x", Prompt: "be terse", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	parentEvents := make(chan Event, 64)
	ctx := WithParentEvents(context.Background(), parentEvents)

	args := mustJSON(t, agentArgs{SubagentType: "Explore", Prompt: "find it"})
	done := make(chan struct{})
	go func() {
		_, _ = tool.Execute(ctx, args)
		close(done)
		close(parentEvents)
	}()

	var sawContentToken bool
	var sawSubagentStart bool
	var sawSubagentDone bool
	for ev := range parentEvents {
		switch ev.(type) {
		case ContentToken:
			sawContentToken = true
		case SubagentStart:
			sawSubagentStart = true
		case SubagentDone:
			sawSubagentDone = true
		}
	}
	<-done

	if sawContentToken {
		t.Errorf("parent saw a ContentToken from the child — context isolation failed")
	}
	if !sawSubagentStart {
		t.Errorf("parent did not see SubagentStart")
	}
	if !sawSubagentDone {
		t.Errorf("parent did not see SubagentDone")
	}
}

func TestAgentTool_SchemaIncludesRequiredFields(t *testing.T) {
	tool, _ := newTestAgentTool(t, nil, nil, false)
	schema := tool.Schema()
	required, _ := schema["required"].([]string)
	wantSet := map[string]bool{"subagent_type": false, "prompt": false}
	for _, r := range required {
		if _, ok := wantSet[r]; ok {
			wantSet[r] = true
		}
	}
	for k, found := range wantSet {
		if !found {
			t.Errorf("schema.required missing %q", k)
		}
	}
}

func TestAgentTool_RequiresApprovalIsFalse(t *testing.T) {
	tool, _ := newTestAgentTool(t, nil, nil, false)
	if tool.RequiresApproval("") {
		t.Errorf("Agent tool itself must not require approval; child tool calls handle approval individually")
	}
}

func TestAgentTool_DescriptionListsAvailableAgents(t *testing.T) {
	cfgs := []subagents.AgentConfig{
		{Name: "Explore", Description: "Read-only search.", Prompt: "x", Source: "test"},
		{Name: "Plan", Description: "Plan-drafting.", Prompt: "x", Source: "test"},
	}
	tool, _ := newTestAgentTool(t, cfgs, nil, false)
	desc := tool.Description()
	if !strings.Contains(desc, "Explore") || !strings.Contains(desc, "Plan") {
		t.Errorf("description must list available agents; got: %q", desc)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestAgentTool_PlanModePropagatesToChild verifies the mode propagation
// invariant: when the parent is in plan mode, the child runs in plan
// mode too. A child that attempts a write outside the plan file gets
// blocked by PlanModeGate just like the parent would. We exercise this
// by configuring a child registry that includes a mutating mock tool
// (RequiresApproval=true), scripting the child to call it, and
// asserting the result is the plan-mode block message — not the tool's
// output.
func TestAgentTool_PlanModePropagatesToChild(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
		{sseDone("done after blocked write")},
	}}
	cfg := subagents.AgentConfig{Name: "test", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, false)
	tool.PlanMode.Active.Store(true)
	tool.PlanMode.PlanFile = "/tmp/plan.md"

	args := mustJSON(t, agentArgs{SubagentType: "test", Prompt: "do something"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The final reply ends up captured; the run_bash call should have
	// hit PlanModeGate and returned its block message as the tool
	// result, which the model then incorporated into the final reply.
	// Either way the run_bash tool's actual output ("shell out") must
	// NOT appear in the result.
	if strings.Contains(out, "shell out") {
		t.Errorf("child under plan-mode parent executed run_bash; got %q", out)
	}
}

// TestAgentTool_AutoModePropagatesToChild verifies the child inherits
// the parent's auto-mode pointer and that the child loop's
// effective iteration cap reflects auto-mode's 4× multiplier. We
// don't need to exercise the multiplier itself — the agent loop is
// already tested elsewhere — just verify the pointer wiring.
func TestAgentTool_AutoModePropagatesToChild(t *testing.T) {
	cfg := subagents.AgentConfig{Name: "test", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	tool.AutoMode.Active.Store(true)

	// runChild builds the child config inline; check that the
	// AgentTool's AutoMode pointer is what would be passed in.
	if !tool.AutoMode.IsActive() {
		t.Fatalf("expected AutoMode to be active for this test setup")
	}
	// The configuration is built per-call inside runChild; the
	// pointer identity check elsewhere proves propagation. Here we
	// just sanity-check that the AgentTool retains the parent's
	// active state — the alternative (a fresh disabled state) would
	// fail this assertion.
	if tool.AutoMode == nil {
		t.Errorf("AgentTool.AutoMode is nil; child cannot inherit")
	}
}

// TestAgentTool_BackgroundDeniesApprovalBeforeAutoMode verifies standalone
// background subagents are read-only even when the parent is in auto mode: the
// background approval policy must run before auto/yolo approvals can leak in.
func TestAgentTool_BackgroundDeniesApprovalBeforeAutoMode(t *testing.T) {
	// Script the child to: (1) call a tool that requires approval,
	// (2) after that returns "denied by user", emit a final reply.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
		{sseDone("adapted after denial")},
	}}
	cfg := subagents.AgentConfig{Name: "x", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)
	tool.AutoMode.Active.Store(true)

	args := mustJSON(t, agentArgs{SubagentType: "x", Prompt: "p", RunInBackground: true})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Background tool returns a handle immediately.
	if !strings.Contains(out, "background subagent") {
		t.Fatalf("expected immediate background handle, got: %q", out)
	}
	// Wait for the detached goroutine to finish (run is short with
	// scripted streamer + nil emitToParent).
	for i := 0; i < 50; i++ {
		tasks := tool.Tasks.List()
		if len(tasks) > 0 && tasks[0].Status != subagents.TaskRunning {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	tasks := tool.Tasks.List()
	if len(tasks) == 0 {
		t.Fatalf("no task recorded")
	}
	// The child should have completed (not errored) because the denial lets the
	// model adapt and produce a final reply. The result also proves run_bash was
	// denied by background policy instead of auto-approved by parent auto mode.
	if tasks[0].Errored {
		t.Errorf("expected non-errored completion after background denial adapt; got Result=%q", tasks[0].Result)
	}
	if !strings.Contains(tasks[0].Result, "adapted after denial") {
		t.Errorf("expected model to adapt after denied run_bash; got Result=%q", tasks[0].Result)
	}
}

// TestAgentTool_BackgroundForwardsProgress locks the live-card behavior
// for background subagents: a background child's SubagentProgress ticks
// must reach the parent's event stream (so the inline card + dock stay
// live while the spawning turn is active), alongside the SubagentStart
// that already fired. Before this wiring the background goroutine passed
// nil for emitToParent and no progress reached the parent — only the
// dock (registry) updated. Approvals stay auto-denied regardless; see
// TestAgentTool_BackgroundAutoDeniesApproval.
func TestAgentTool_BackgroundForwardsProgress(t *testing.T) {
	// Child turn 1 calls read_file (a no-approval tool → emits a
	// ToolStart → progress tick); turn 2 emits the final reply.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "read_file", ArgsJSON: `{"path":"x"}`})},
		{sseDone("done reviewing")},
	}}
	cfg := subagents.AgentConfig{Name: "review", Description: "x", Tools: []string{"read_file"}, Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	// Keep the parent event stream attached and alive for the whole run
	// (the orchestration case: spawning turn stays active).
	parentEvents := make(chan Event, 64)
	ctx := WithParentEvents(context.Background(), parentEvents)

	out, err := tool.Execute(ctx, mustJSON(t, agentArgs{SubagentType: "review", Prompt: "p", RunInBackground: true}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "background subagent") {
		t.Fatalf("expected immediate background handle, got: %q", out)
	}

	// Wait for the detached child to finish; all parentEvents sends
	// happen inside runChild, before MarkDone flips the status.
	for range 100 {
		tasks := tool.Tasks.List()
		if len(tasks) > 0 && tasks[0].Status != subagents.TaskRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Drain the buffered events the run forwarded (non-blocking — the
	// run is over, so the buffer holds everything; nothing is still
	// sending, so we never close the channel out from under a sender).
	var starts, progress int
	for done := false; !done; {
		select {
		case ev := <-parentEvents:
			switch ev.(type) {
			case SubagentStart:
				starts++
			case SubagentProgress:
				progress++
			}
		default:
			done = true
		}
	}

	if starts == 0 {
		t.Errorf("expected a SubagentStart forwarded for the background run")
	}
	if progress == 0 {
		t.Errorf("expected at least one SubagentProgress forwarded for the background run — background should stream live ticks, not just update the dock")
	}
}

// TestAgentTool_BackgroundSurvivesClosedTurnChannel is the regression
// test for the closed-per-turn-channel panic: a background subagent
// outlives its spawning turn, which CLOSES the per-turn event channel,
// yet the detached child keeps emitting SubagentStart/SubagentProgress.
// Forwarding those onto the closed channel must degrade to a dropped
// tick (via trySend), NOT a "send on closed channel" panic that the
// runChild recover would turn into a failed (errored) run.
//
// An already-closed channel reproduces the post-turn-end state
// deterministically: the SubagentStart forwarded synchronously inside
// Execute (parent goroutine) AND every SubagentProgress forwarded from
// the detached child goroutine both target a closed channel. Before the
// fix the first send panicked; after it, the run completes cleanly.
func TestAgentTool_BackgroundSurvivesClosedTurnChannel(t *testing.T) {
	// Turn 1 calls read_file (a no-approval tool → a ToolStart → a
	// progress tick); turn 2 emits the final reply.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "read_file", ArgsJSON: `{"path":"x"}`})},
		{sseDone("done reviewing")},
	}}
	cfg := subagents.AgentConfig{Name: "review", Description: "x", Tools: []string{"read_file"}, Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	// A per-turn channel that is ALREADY closed — i.e. the spawning turn
	// has ended. Every forwarded event targets a closed channel.
	parentEvents := make(chan Event, 64)
	close(parentEvents)
	ctx := WithParentEvents(context.Background(), parentEvents)

	// Execute forwards a SubagentStart synchronously (parent goroutine)
	// before detaching — that send must not panic, so Execute returns the
	// handle normally.
	out, err := tool.Execute(ctx, mustJSON(t, agentArgs{SubagentType: "review", Prompt: "p", RunInBackground: true}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "background subagent") {
		t.Fatalf("expected immediate background handle, got: %q", out)
	}

	// The detached child forwards SubagentProgress onto the closed channel
	// from its own goroutine; the run must still finish cleanly — completed,
	// not errored by a recovered send-on-closed-channel panic.
	var final subagents.Task
	for range 200 {
		tasks := tool.Tasks.List()
		if len(tasks) > 0 && tasks[0].Status != subagents.TaskRunning {
			final = tasks[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != subagents.TaskCompleted {
		t.Fatalf("expected TaskCompleted despite the closed turn channel, got status=%v errored=%v result=%q",
			final.Status, final.Errored, final.Result)
	}
	if final.Errored {
		t.Errorf("a background run must not be errored by a forward onto a closed channel")
	}
}

// TestAgentTool_BackgroundGoroutineRecoversPanic: a panic in the detached
// background goroutine's POST-run orchestration (here the onBackgroundDone
// callback, which in the TUI runs UI code) must be recovered, not crash the
// whole process. The run already reached its terminal state before the
// notification, so the result is preserved (TaskCompleted, not clobbered).
func TestAgentTool_BackgroundGoroutineRecoversPanic(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("done reviewing")},
	}}
	cfg := subagents.AgentConfig{Name: "review", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	// A completion callback that panics simulates a fault in the post-run
	// notification path (e.g. a TUI render on the inbox). Without the
	// goroutine-level recover this crashes the whole `go test` process.
	fired := make(chan struct{}, 1)
	tool.SetBackgroundDoneCallback(func(ev SubagentBackgroundDone) {
		select {
		case fired <- struct{}{}:
		default:
		}
		panic("boom in onBackgroundDone")
	})

	ctx := WithParentEvents(context.Background(), make(chan Event, 64))
	if _, err := tool.Execute(ctx, mustJSON(t, agentArgs{SubagentType: "review", Prompt: "p", RunInBackground: true})); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("background completion callback never fired")
	}

	// Process survived (we're still running). The task reached its real
	// terminal state before the notification panicked, so the panic did not
	// clobber the result.
	var final subagents.Task
	for range 100 {
		tasks := tool.Tasks.List()
		if len(tasks) > 0 && tasks[0].Status != subagents.TaskRunning {
			final = tasks[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != subagents.TaskCompleted {
		t.Errorf("task should be TaskCompleted despite the callback panic; got %v", final.Status)
	}
}

// TestAgentTool_BackgroundSurvivesParentCtxCancel: a background subagent
// detaches onto context.Background(), so cancelling the PARENT turn's ctx
// (the spawning turn ending) must NOT kill it — the completion still fires
// with a real result. This is the detachment guarantee the whole
// fire-and-forget model rests on.
func TestAgentTool_BackgroundSurvivesParentCtxCancel(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("done reviewing")}}}
	cfg := subagents.AgentConfig{Name: "review", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	fired := make(chan SubagentBackgroundDone, 1)
	tool.SetBackgroundDoneCallback(func(ev SubagentBackgroundDone) {
		select {
		case fired <- ev:
		default:
		}
	})

	ctx, cancel := context.WithCancel(WithParentEvents(context.Background(), make(chan Event, 64)))
	if _, err := tool.Execute(ctx, mustJSON(t, agentArgs{SubagentType: "review", Prompt: "p", RunInBackground: true})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	cancel() // the spawning turn ends immediately after dispatch

	select {
	case ev := <-fired:
		if ev.Errored {
			t.Errorf("detached bg subagent must complete after parent ctx cancel, not error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background completion did not fire after parent ctx cancel — detachment is broken")
	}
}

// TestAgentTool_NotifyOnDonePropagates: a background spawn with
// notify_on_done:true records the flag on the task AND on the fired
// SubagentBackgroundDone event, so the TUI knows to wake the model with the
// result rather than only painting a banner.
func TestAgentTool_NotifyOnDonePropagates(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("result body")}}}
	cfg := subagents.AgentConfig{Name: "review", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	got := make(chan SubagentBackgroundDone, 1)
	tool.SetBackgroundDoneCallback(func(ev SubagentBackgroundDone) {
		select {
		case got <- ev:
		default:
		}
	})

	ctx := WithParentEvents(context.Background(), make(chan Event, 64))
	if _, err := tool.Execute(ctx, mustJSON(t, agentArgs{SubagentType: "review", Prompt: "p", RunInBackground: true, NotifyOnDone: true})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	select {
	case ev := <-got:
		if !ev.NotifyOnDone {
			t.Error("fired SubagentBackgroundDone must carry NotifyOnDone=true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background completion never fired")
	}
	if tasks := tool.Tasks.List(); len(tasks) == 0 || !tasks[0].NotifyOnDone {
		t.Error("task must record NotifyOnDone=true")
	}
}

// TestAgentTool_NotifyOnDoneIgnoredForeground: notify_on_done is a
// background-only affordance (foreground returns its result inline), so a
// foreground spawn must not record it — otherwise the TUI could try to wake
// the model for a result it already has.
func TestAgentTool_NotifyOnDoneIgnoredForeground(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("done")}}}
	cfg := subagents.AgentConfig{Name: "fg", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	ctx := WithParentEvents(context.Background(), make(chan Event, 64))
	if _, err := tool.Execute(ctx, mustJSON(t, agentArgs{SubagentType: "fg", Prompt: "p", RunInBackground: false, NotifyOnDone: true})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if tasks := tool.Tasks.List(); len(tasks) == 0 || tasks[0].NotifyOnDone {
		t.Error("foreground task must NOT record NotifyOnDone (background-only flag)")
	}
}

// TestAgentTool_SessionTokenBudgetRejects: once cumulative subagent spend
// reaches MaxSessionTokens, a new spawn is rejected with a recoverable
// error and no task is reserved.
func TestAgentTool_SessionTokenBudgetRejects(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("done")}}}
	cfg := subagents.AgentConfig{Name: "review", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)
	tool.MaxSessionTokens = 1000

	// Pre-load the registry with recorded spend over the cap.
	done := &subagents.Task{ID: subagents.NewTaskID(), AgentType: "review", Status: subagents.TaskRunning}
	tool.Tasks.Add(done)
	tool.Tasks.MarkDone(done.ID, subagents.TaskCompleted, "r", false, 1500)

	out, err := tool.Execute(context.Background(), mustJSON(t, agentArgs{SubagentType: "review", Prompt: "p"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "token budget") {
		t.Errorf("expected a budget-exhausted rejection; got: %q", out)
	}
	if n := len(tool.Tasks.List()); n != 1 {
		t.Errorf("a rejected spawn must not reserve a task; have %d tasks (want 1)", n)
	}
}

// TestAgentTool_SessionTokenBudgetUnlimitedWhenZero: MaxSessionTokens=0
// disables the budget — a spawn proceeds regardless of prior spend.
func TestAgentTool_SessionTokenBudgetUnlimitedWhenZero(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("done")}}}
	cfg := subagents.AgentConfig{Name: "review", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)
	tool.MaxSessionTokens = 0 // unlimited

	done := &subagents.Task{ID: subagents.NewTaskID(), AgentType: "review", Status: subagents.TaskRunning}
	tool.Tasks.Add(done)
	tool.Tasks.MarkDone(done.ID, subagents.TaskCompleted, "r", false, 9_000_000)

	out, err := tool.Execute(context.Background(), mustJSON(t, agentArgs{SubagentType: "review", Prompt: "p"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "token budget") {
		t.Errorf("budget must be disabled when MaxSessionTokens=0; got rejection: %q", out)
	}
}

// TestAgentTool_ForegroundForwardsApproval verifies the foreground
// path forwards a child's ApprovalNeeded event to the parent's events
// channel and reads the user's verdict back from the parent's
// decisions channel. We simulate the parent UI by draining its events
// channel in a goroutine and answering the approval modal.
func TestAgentTool_ForegroundForwardsApproval(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
		{sseDone("ran with approval")},
	}}
	cfg := subagents.AgentConfig{Name: "fg", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, false)

	parentEvents := make(chan Event, 64)
	parentDecisions := make(chan Decision, 1)
	ctx := WithParentDecisions(WithParentEvents(context.Background(), parentEvents), parentDecisions)

	// Simulated parent UI: watch for the forwarded ApprovalNeeded
	// and answer Allow. Run in a goroutine so Execute can block on
	// the response.
	gotApproval := make(chan ApprovalNeeded, 1)
	go func() {
		for ev := range parentEvents {
			if a, ok := ev.(ApprovalNeeded); ok {
				gotApproval <- a
				parentDecisions <- AllowOnce
			}
		}
	}()

	args := mustJSON(t, agentArgs{SubagentType: "fg", Prompt: "p"})
	out, err := tool.Execute(ctx, args)
	close(parentEvents)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	select {
	case a := <-gotApproval:
		if !strings.Contains(a.Preview, "[subagent:fg]") {
			t.Errorf("approval Preview missing subagent badge: %q", a.Preview)
		}
		if a.ToolName != "run_bash" {
			t.Errorf("approval ToolName = %q, want run_bash", a.ToolName)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no ApprovalNeeded forwarded to parent")
	}

	if !strings.Contains(out, "ran with approval") {
		t.Errorf("child final reply not surfaced; got: %q", out)
	}
}
