package agent

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestBackgroundDispatchPanicStillFiresDoneCallback is a regression for the
// release review's lost-notification gap: when a BACKGROUND dispatch worker
// panics in runDispatchChild's own post-runChild orchestration (the
// commit/merge logic the recover at dispatch_tool.go was written to cover),
// the recover used to call only MarkDone — never fireBackgroundDone — so the
// inbox/dock never learned the worker finished and integrate was never
// prompted. The recover must fire the completion callback for background
// workers too.
//
// We stand in for the commit-logic panic with a nil parent ctx, which makes
// context.WithCancel(ctx) panic AFTER the recover is armed and BEFORE the
// normal-return fireBackgroundDone. Any panic in that window takes the same
// recover path.
func TestBackgroundDispatchPanicStillFiresDoneCallback(t *testing.T) {
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

	fired := make(chan SubagentBackgroundDone, 1)
	at.SetBackgroundDoneCallback(func(e SubagentBackgroundDone) { fired <- e })

	c := &dispatchChild{
		spec: dispatchTaskSpec{Prompt: "p", Description: "d"},
		cfg:  &at.Configs[0],
	}
	// Execute builds and admits every child's registry record before spawning
	// (one atomic reservation for the batch), so a direct runDispatchChild call
	// has to do the same two steps itself.
	at.Tasks.Add(d.prepareDispatchChild(c, "batch-1", true))

	// background=true; the nil ctx is deliberate — it makes
	// context.WithCancel panic after the recover is armed, standing in for a
	// commit/merge-logic panic.
	//nolint:staticcheck // SA1012: intentional nil ctx to force the panic
	d.runDispatchChild(nil, c, "batch-1", true, nil, nil)

	// The recover ran (panic did not escape) and marked the task done+errored.
	snap, ok := at.Tasks.Get(c.taskID)
	if !ok {
		t.Fatalf("task not registered after panic recovery")
	}
	if snap.Status != subagents.TaskErrored {
		t.Fatalf("task Status = %v, want TaskErrored (recover should MarkDone)", snap.Status)
	}

	// The gap: a background worker was marked done but never fired the
	// completion callback.
	select {
	case <-fired:
		// correct — only happens once fireBackgroundDone is added to the recover
	default:
		t.Error("background dispatch worker panicked: task marked errored+done but fireBackgroundDone was NOT called — the inbox/dock never sees completion and integrate is never prompted")
	}
}
