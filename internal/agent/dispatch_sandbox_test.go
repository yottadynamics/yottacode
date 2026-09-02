package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// dispatchBashStreamer is a stateless test adapter: on the first call for a
// child it emits a run_bash tool call with the command named after
// "TESTBASH:" in the user prompt; once a tool result is in the history it
// echoes that result back as the final reply, so a test can assert on the
// sandboxed command's actual stdout having reached the worker's final
// result. Mirrors dispatchWriteStreamer in dispatch_tool_test.go.
type dispatchBashStreamer struct{}

func (dispatchBashStreamer) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent, 4)
	var toolResult string
	userPrompt := ""
	for _, m := range msgs {
		if m.Role == adapter.RoleTool {
			toolResult = m.Content
		}
		if m.Role == adapter.RoleUser {
			userPrompt = m.Content
		}
	}
	go func() {
		defer close(out)
		if toolResult != "" {
			out <- sseDone("done: " + toolResult)
			return
		}
		command := extractTestBashCommand(userPrompt)
		args := fmt.Sprintf(`{"command":%q}`, command)
		out <- sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: args})
	}()
	return out
}

func extractTestBashCommand(prompt string) string {
	_, rest, found := strings.Cut(prompt, "TESTBASH:")
	if !found {
		return "echo default"
	}
	return strings.TrimSpace(rest)
}

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

	withSuppressedPanicRecoveryStderr(t, func() {
		// nil ctx is deliberate — makes context.WithCancel(ctx) panic, taking
		// the recover path. See TestBackgroundDispatchPanicStillFiresDoneCallback.
		d.runDispatchChild(panicContextForDispatchPanicTest(), c, "batch-1", true, nil, nil)
	})

	if spy.closeCount != 1 {
		t.Errorf("Close called %d times on panic recovery, want exactly 1", spy.closeCount)
	}
}

// TestDispatchSandbox_SkipsConstructionWhenWorkerCantUseRunBash: a write
// worker whose AgentConfig.Tools allowlist includes none of run_bash,
// run_tests, or create_document — the tools that depend on Sandbox — has
// nothing for a Sandbox to cover. It must not pay container-creation cost or
// fail its task over a dependency it was never going to use.
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

// TestDispatchSandbox_ConstructsForCreateDocumentEvenWithoutRunBash pins
// that create_document still gets a Sandbox when granted to a worker: docx/pdf
// depend on Sandbox exactly like run_bash, so a worker granted create_document
// but not run_bash must not silently fall back to host pandoc execution.

func TestDispatchSandbox_ConstructsForCreateDocumentEvenWithoutRunBash(t *testing.T) {
	auto := &AutoModeState{}
	auto.Active.Store(true)
	at := &AgentTool{
		Configs: []subagents.AgentConfig{{
			Name:  "writer-docgen-no-bash",
			Tools: []string{"write_file", "read_file", "create_document"}, // no run_bash
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
		spec:     dispatchTaskSpec{Prompt: "p", Description: "d", Files: []string{"x.docx"}},
		cfg:      &at.Configs[0],
		isWrite:  true,
		worktree: t.TempDir(),
	}
	at.Tasks.Add(d.prepareDispatchChild(c, "batch-1", false))

	d.runDispatchChild(context.Background(), c, "batch-1", false, nil, nil)

	if !factoryCalled {
		t.Error("SandboxFactory should be called for a worker whose Tools allowlist includes create_document, even without run_bash")
	}
}

// TestDispatchSandbox_ConstructsForRunTestsEvenWithoutRunBash mirrors the
// create_document case above: a worker granted run_tests but not run_bash
// still needs its own Sandbox, since run_tests routes through it too (see
// RunTestsTool.sandbox) and dispatchBackgroundApprovalPolicy only allows an
// unattended run_tests call when this worker is actually sandboxed.
func TestDispatchSandbox_ConstructsForRunTestsEvenWithoutRunBash(t *testing.T) {
	auto := &AutoModeState{}
	auto.Active.Store(true)
	at := &AgentTool{
		Configs: []subagents.AgentConfig{{
			Name:  "writer-tests-no-bash",
			Tools: []string{"write_file", "read_file", "run_tests"}, // no run_bash
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

	if !factoryCalled {
		t.Error("SandboxFactory should be called for a worker whose Tools allowlist includes run_tests, even without run_bash")
	}
}

// TestDispatchSandbox_MarksOptsSandboxedWhenConstructed drives a full
// background write-worker run and confirms run_bash — denied without a
// sandbox — is ALLOWED once DispatchTool.SandboxFactory successfully builds
// one for this worker: the end-to-end wiring from SandboxFactory through
// childRunOpts.sandboxed into dispatchBackgroundApprovalPolicy.
func TestDispatchSandbox_MarksOptsSandboxedWhenConstructed(t *testing.T) {
	auto := &AutoModeState{}
	auto.Active.Store(true)
	at := &AgentTool{
		Configs: []subagents.AgentConfig{{
			Name:  "writer-bash",
			Tools: []string{"write_file", "read_file", "run_bash"},
		}},
		Tasks:          subagents.NewRegistry(),
		Adapter:        dispatchBashStreamer{},
		ParentRegistry: NewRegistry(),
		AutoMode:       auto,
		PlanMode:       &PlanModeState{},
		YoloMode:       &YoloModeState{},
		Cwd:            NewCwdRef(t.TempDir()),
		TranscriptDir:  t.TempDir(),
	}
	d := &DispatchTool{
		Agent:              at,
		Enabled:            true,
		SupportsBackground: true,
		SandboxFactory: func(ctx context.Context, wtDir, taskID string) (Sandbox, error) {
			return &spySandbox{label: "[podman]"}, nil
		},
	}

	c := &dispatchChild{
		spec:     dispatchTaskSpec{Prompt: "TESTBASH:echo confined", Description: "d", Files: []string{"x.go"}},
		cfg:      &at.Configs[0],
		isWrite:  true,
		worktree: t.TempDir(),
	}
	at.Tasks.Add(d.prepareDispatchChild(c, "batch-1", true))

	d.runDispatchChild(context.Background(), c, "batch-1", true, nil, nil)

	snap, ok := at.Tasks.Get(c.taskID)
	if !ok {
		t.Fatalf("task not registered")
	}
	if snap.Errored {
		t.Fatalf("worker errored, want run_bash allowed under sandbox: %s", snap.Result)
	}
	if !strings.Contains(snap.Result, "confined") {
		t.Errorf("result = %q, want it to contain the sandboxed run_bash's stdout", snap.Result)
	}
}
