package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestBuildWorktreeChildRegistry_NilSandboxMeansHostDefault pins that a
// dispatch worker built without a Sandbox behaves exactly like the parent
// session's own nil-Sandbox default — no separate opt-in needed to keep
// today's host-exec behavior for workers.
func TestBuildWorktreeChildRegistry_NilSandboxMeansHostDefault(t *testing.T) {
	d := &DispatchTool{}
	cwd := NewCwdRef(t.TempDir())
	cfg := &subagents.AgentConfig{Name: "worker", Tools: []string{"run_bash"}}

	reg := d.buildWorktreeChildRegistry(cfg, cwd, cwd.Get(), nil, false, nil)
	tool, ok := reg.Get("run_bash")
	if !ok {
		t.Fatal("run_bash not registered")
	}
	rb := tool.(*RunBashTool)
	if rb.Sandbox != nil {
		t.Errorf("expected nil Sandbox on the worker's run_bash tool, got %T", rb.Sandbox)
	}
}

// TestBuildWorktreeChildRegistry_ThreadsSandbox pins that the sandbox
// buildWorktreeChildRegistry is called with reaches the worker's run_bash
// tool — the wiring DispatchTool.SandboxFactory depends on.
func TestBuildWorktreeChildRegistry_ThreadsSandbox(t *testing.T) {
	d := &DispatchTool{}
	cwd := NewCwdRef(t.TempDir())
	cfg := &subagents.AgentConfig{Name: "worker", Tools: []string{"run_bash"}}
	sb := &spySandbox{label: "[podman]"}

	reg := d.buildWorktreeChildRegistry(cfg, cwd, cwd.Get(), nil, false, sb)
	tool, ok := reg.Get("run_bash")
	if !ok {
		t.Fatal("run_bash not registered")
	}
	rb := tool.(*RunBashTool)
	if rb.Sandbox != Sandbox(sb) {
		t.Errorf("worker's run_bash Sandbox not threaded from buildWorktreeChildRegistry's sandbox param")
	}
}

// TestDispatchSandbox_ConstructionFailureErrorsWorkerImmediately: when
// SandboxFactory fails for a write worker, the worker must fail loud
// (task marked errored, result names the sandbox failure) rather than
// silently falling back to unsandboxed host execution — the same
// never-fall-back-on-error contract NewPodmanSandbox's callers follow at
// session startup.
func TestDispatchSandbox_ConstructionFailureErrorsWorkerImmediately(t *testing.T) {
	auto := &AutoModeState{}
	auto.Active.Store(true)
	at := &AgentTool{
		Configs:        []subagents.AgentConfig{{Name: "writer"}},
		Tasks:          subagents.NewRegistry(),
		Adapter:        dispatchWriteStreamer{},
		ParentRegistry: NewRegistry(),
		AutoMode:       auto,
		PlanMode:       &PlanModeState{},
		YoloMode:       &YoloModeState{},
		Cwd:            NewCwdRef(t.TempDir()),
		TranscriptDir:  t.TempDir(),
	}
	wantErr := errors.New("podman not found in PATH")
	d := &DispatchTool{
		Agent:   at,
		Enabled: true,
		SandboxFactory: func(ctx context.Context, wtDir, taskID string) (Sandbox, error) {
			return nil, wantErr
		},
	}

	c := &dispatchChild{
		spec:     dispatchTaskSpec{Prompt: "p", Description: "d", Files: []string{"x.go"}},
		cfg:      &at.Configs[0],
		isWrite:  true,
		worktree: t.TempDir(),
	}
	at.Tasks.Add(d.prepareDispatchChild(c, "batch-1", false))

	d.runDispatchChild(context.Background(), c, "batch-1", false, nil, nil)

	snap, ok := at.Tasks.Get(c.taskID)
	if !ok {
		t.Fatalf("task not registered")
	}
	if snap.Status != subagents.TaskErrored {
		t.Fatalf("task Status = %v, want TaskErrored", snap.Status)
	}
	if !strings.Contains(snap.Result, "sandbox:") || !strings.Contains(snap.Result, wantErr.Error()) {
		t.Errorf("result = %q, want it to name the sandbox construction failure", snap.Result)
	}
}

// TestDispatchSandbox_CloseCalledOnceOnNormalCompletion drives a full
// write-worker run through DispatchTool.Execute (real git worktree,
// real write_file call) with a counting spy Sandbox, and confirms Close
// is called exactly once when the worker finishes normally.
func TestDispatchSandbox_CloseCalledOnceOnNormalCompletion(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	var mu sync.Mutex
	var spies []*spySandbox
	d.SandboxFactory = func(ctx context.Context, wtDir, taskID string) (Sandbox, error) {
		sb := &spySandbox{label: "[podman]"}
		mu.Lock()
		spies = append(spies, sb)
		mu.Unlock()
		return sb, nil
	}

	out, err := d.Execute(context.Background(), `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"create the file. TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"create the file. TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch Execute: %v", err)
	}
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("dispatch Execute returned an error result: %s", out)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(spies) != 2 {
		t.Fatalf("expected exactly one sandbox constructed per write worker (2 workers), got %d", len(spies))
	}
	for i, sb := range spies {
		if sb.closeCount != 1 {
			t.Errorf("worker %d: Close called %d times, want exactly 1", i, sb.closeCount)
		}
	}
}

// TestDispatchSandbox_CloseCalledOnceOnPanicRecovery mirrors
// TestBackgroundDispatchPanicStillFiresDoneCallback's nil-ctx-forces-panic
// technique: a panic in runDispatchChild's own orchestration must still
// tear down the worker's sandbox via the panic-recovery defer, not leak
// the container.
func TestDispatchSandbox_CloseCalledOnceOnPanicRecovery(t *testing.T) {
	auto := &AutoModeState{}
	auto.Active.Store(true)
	at := &AgentTool{
		Configs:        []subagents.AgentConfig{{Name: "writer"}},
		Tasks:          subagents.NewRegistry(),
		Adapter:        dispatchWriteStreamer{},
		ParentRegistry: NewRegistry(),
		AutoMode:       auto,
		PlanMode:       &PlanModeState{},
		YoloMode:       &YoloModeState{},
		Cwd:            NewCwdRef(t.TempDir()),
		TranscriptDir:  t.TempDir(),
	}
	d := &DispatchTool{Agent: at, Enabled: true, SupportsBackground: true}

	c := &dispatchChild{
		spec:    dispatchTaskSpec{Prompt: "p", Description: "d"},
		cfg:     &at.Configs[0],
		isWrite: true,
	}
	at.Tasks.Add(d.prepareDispatchChild(c, "batch-1", true))
	spy := &spySandbox{label: "[podman]"}
	c.sandbox = spy // stand in for a SandboxFactory result — the panic fires before construction would run

	// nil ctx is deliberate — makes context.WithCancel(ctx) panic, taking
	// the recover path. See TestBackgroundDispatchPanicStillFiresDoneCallback.
	//nolint:staticcheck // SA1012: intentional nil ctx to force the panic
	d.runDispatchChild(nil, c, "batch-1", true, nil, nil)

	if spy.closeCount != 1 {
		t.Errorf("Close called %d times on panic recovery, want exactly 1", spy.closeCount)
	}
}

// TestDispatchSandbox_SkipsConstructionWhenWorkerCantUseRunBash: a write
// worker whose AgentConfig.Tools allowlist doesn't include run_bash has
// nothing for a Sandbox to cover — it must not pay container-creation
// cost or fail its task over a dependency it was never going to use.
func TestDispatchSandbox_SkipsConstructionWhenWorkerCantUseRunBash(t *testing.T) {
	auto := &AutoModeState{}
	auto.Active.Store(true)
	at := &AgentTool{
		Configs: []subagents.AgentConfig{{
			Name:  "writer-no-bash",
			Tools: []string{"write_file", "read_file"}, // explicit allowlist, no run_bash
		}},
		Tasks:          subagents.NewRegistry(),
		Adapter:        dispatchWriteStreamer{},
		ParentRegistry: NewRegistry(),
		AutoMode:       auto,
		PlanMode:       &PlanModeState{},
		YoloMode:       &YoloModeState{},
		Cwd:            NewCwdRef(t.TempDir()),
		TranscriptDir:  t.TempDir(),
	}
	factoryCalled := false
	d := &DispatchTool{
		Agent:   at,
		Enabled: true,
		SandboxFactory: func(ctx context.Context, wtDir, taskID string) (Sandbox, error) {
			factoryCalled = true
			return &spySandbox{}, nil
		},
	}

	c := &dispatchChild{
		spec:     dispatchTaskSpec{Prompt: "p", Description: "d", Files: []string{"x.go"}},
		cfg:      &at.Configs[0],
		isWrite:  true,
		worktree: t.TempDir(),
	}
	at.Tasks.Add(d.prepareDispatchChild(c, "batch-1", false))

	d.runDispatchChild(context.Background(), c, "batch-1", false, nil, nil)

	if factoryCalled {
		t.Error("SandboxFactory should not be called for a worker whose Tools allowlist excludes run_bash")
	}
}
