package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// --- pure-helper unit tests ------------------------------------------------

func TestValidateWritePartition(t *testing.T) {
	writer := &subagents.AgentConfig{Name: "writer"} // nil Tools → write-capable
	reader := &subagents.AgentConfig{Name: "reader", Tools: []string{"read_file"}}

	mk := func(cfg *subagents.AgentConfig, files ...string) *dispatchChild {
		return &dispatchChild{cfg: cfg, isWrite: !agentIsReadOnly(cfg), spec: dispatchTaskSpec{Files: files, Description: cfg.Name}}
	}
	// vwp wraps validateWritePartition with an empty registry — these
	// subtests only exercise the within-call check. Cross-call collisions
	// against the registry are covered separately below.
	vwp := func(children []*dispatchChild) string {
		return validateWritePartition(children, subagents.NewRegistry())
	}

	t.Run("disjoint ok", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "a.go"), mk(writer, "b.go")})
		if got != "" {
			t.Errorf("expected ok, got %q", got)
		}
	})
	t.Run("overlap rejected", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "a.go", "shared.go"), mk(writer, "shared.go")})
		if !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected overlap rejection, got %q", got)
		}
	})
	t.Run("missing files rejected", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer), mk(writer, "b.go")})
		if !strings.Contains(got, "must declare their `files`") {
			t.Errorf("expected missing-files rejection, got %q", got)
		}
	})
	t.Run("read-only tasks need no files", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(reader), mk(writer, "b.go")})
		if got != "" {
			t.Errorf("read-only task should not need files, got %q", got)
		}
	})
	t.Run("path normalization catches ./ overlap", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "./a.go"), mk(writer, "a.go")})
		if !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected normalized overlap rejection, got %q", got)
		}
	})
	t.Run("directory claim overlaps descendant file", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "internal/api"), mk(writer, "internal/api/health.go")})
		if !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected directory/file overlap rejection, got %q", got)
		}
	})
	t.Run("descendant directory claim overlaps parent", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "internal/api/health"), mk(writer, "internal/api")})
		if !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected parent/child overlap rejection, got %q", got)
		}
	})
	t.Run("sibling directories allowed", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "internal/api"), mk(writer, "internal/app")})
		if got != "" {
			t.Errorf("sibling claims should be allowed, got %q", got)
		}
	})
	t.Run("root claim rejected", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "."), mk(writer, "foo.go")})
		if !strings.Contains(got, "invalid broad ownership") || !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected root ownership rejection, got %q", got)
		}
	})
	t.Run("blank claim rejected", func(t *testing.T) {
		got := vwp([]*dispatchChild{mk(writer, "  ")})
		if !strings.Contains(got, "invalid broad ownership") {
			t.Errorf("expected blank ownership rejection, got %q", got)
		}
	})
	t.Run("parent and absolute claims rejected", func(t *testing.T) {
		for _, claim := range []string{"..", "../", "../foo.go", "/tmp/foo.go"} {
			got := vwp([]*dispatchChild{mk(writer, claim)})
			if !strings.Contains(got, "invalid broad ownership") {
				t.Errorf("claim %q: expected broad ownership rejection, got %q", claim, got)
			}
		}
	})
	t.Run("collides with an already-running dispatch task", func(t *testing.T) {
		reg := subagents.NewRegistry()
		reg.Add(&subagents.Task{ID: subagents.NewTaskID(), Status: subagents.TaskRunning, Files: []string{"shared.go"}})
		got := validateWritePartition([]*dispatchChild{mk(writer, "shared.go")}, reg)
		if !strings.Contains(got, "non-overlapping") || !strings.Contains(got, "already-running dispatch task") {
			t.Errorf("expected cross-call collision rejection, got %q", got)
		}
	})
	t.Run("no collision against a finished task's stale claim", func(t *testing.T) {
		reg := subagents.NewRegistry()
		done := &subagents.Task{ID: subagents.NewTaskID(), Status: subagents.TaskRunning, Files: []string{"shared.go"}}
		reg.Add(done)
		reg.MarkDone(done.ID, subagents.TaskCompleted, "ok", false, 0)
		got := validateWritePartition([]*dispatchChild{mk(writer, "shared.go")}, reg)
		if got != "" {
			t.Errorf("a finished task's claim must not block a new one, got %q", got)
		}
	})
	t.Run("nil registry skips the cross-call check", func(t *testing.T) {
		got := validateWritePartition([]*dispatchChild{mk(writer, "a.go")}, nil)
		if got != "" {
			t.Errorf("expected ok with nil registry, got %q", got)
		}
	})
	t.Run("case-insensitive filesystem catches a case-only collision", func(t *testing.T) {
		old := dispatchCaseInsensitiveFS
		dispatchCaseInsensitiveFS = true
		defer func() { dispatchCaseInsensitiveFS = old }()
		got := vwp([]*dispatchChild{mk(writer, "Utils.go"), mk(writer, "utils.go")})
		if !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected case-insensitive collision rejection, got %q", got)
		}
	})
	t.Run("case-sensitive filesystem allows a case-only difference", func(t *testing.T) {
		old := dispatchCaseInsensitiveFS
		dispatchCaseInsensitiveFS = false
		defer func() { dispatchCaseInsensitiveFS = old }()
		got := vwp([]*dispatchChild{mk(writer, "Utils.go"), mk(writer, "utils.go")})
		if got != "" {
			t.Errorf("case-sensitive filesystem should allow Utils.go and utils.go as distinct files, got %q", got)
		}
	})
}

func TestBuildWorktreeChildRegistry_BackgroundDisablesLSP(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())
	d := &DispatchTool{
		EnableLSP:  true,
		LSPServers: map[string][]string{"go": {"gopls"}},
	}
	cfg := &subagents.AgentConfig{Name: "lsp-worker", Tools: []string{"lsp_status"}}

	bgReg := d.buildWorktreeChildRegistry(cfg, cwd, cwd.Get(), []string{"owned.go"}, true, nil)
	if _, ok := bgReg.Get("lsp_status"); ok {
		t.Fatal("background dispatch workers must not register LSP tools that can spawn language-server processes")
	}

	fgReg := d.buildWorktreeChildRegistry(cfg, cwd, cwd.Get(), []string{"owned.go"}, false, nil)
	raw, ok := fgReg.Get("lsp_status")
	if !ok {
		t.Fatal("foreground dispatch worker should keep LSP tools")
	}
	tool := raw.(*LSPStatusTool)
	if tool.Manager != nil {
		t.Fatal("foreground dispatch worker LSP tools must not share the parent LSP manager")
	}
	if got := strings.Join(tool.Servers["go"], " "); got != "gopls" {
		t.Fatalf("LSP server overrides were not propagated, got %q", got)
	}
}

// TestBuildWorktreeChildRegistry_DocumentIngestionPropagates is a
// regression for the parity gap: AllowPDFIngestion must reach
// dispatch/worktree-child workers the same way EnableLSP and
// EnableSyntaxRanges already do, so a subagent's read_document PDF gate
// matches the parent session's document_ingestion experimental feature.
// read_document itself is always registered now — only its PDF format is
// gated — so this checks the propagated field, not tool presence.
func TestBuildWorktreeChildRegistry_DocumentIngestionPropagates(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())
	cfg := &subagents.AgentConfig{Name: "doc-worker", Tools: []string{"read_document"}}

	off := &DispatchTool{}
	offReg := off.buildWorktreeChildRegistry(cfg, cwd, cwd.Get(), nil, false, nil)
	offRaw, ok := offReg.Get("read_document")
	if !ok {
		t.Fatal("read_document should always be registered on a worktree child, regardless of AllowPDFIngestion")
	}
	if offRaw.(*ReadDocumentTool).SubprocessFormatsEnabled {
		t.Fatal("read_document's PDF gate should be off when AllowPDFIngestion is false")
	}

	on := &DispatchTool{AllowPDFIngestion: true}
	onReg := on.buildWorktreeChildRegistry(cfg, cwd, cwd.Get(), nil, false, nil)
	onRaw, ok := onReg.Get("read_document")
	if !ok {
		t.Fatal("read_document should be registered on a worktree child")
	}
	if !onRaw.(*ReadDocumentTool).SubprocessFormatsEnabled {
		t.Fatal("read_document's PDF gate should be on when AllowPDFIngestion is true")
	}
}

func TestStripUnattendedProcessToolsRemovesLSPFromSharedRegistry(t *testing.T) {
	reg := NewRegistry()
	reg.Register(namedApprovalTool{name: "lsp_status"})
	reg.Register(namedApprovalTool{name: "media_probe"})
	reg.Register(namedApprovalTool{name: "read_file"})

	stripUnattendedProcessTools(reg)

	if _, ok := reg.Get("lsp_status"); ok {
		t.Fatal("background policy should remove LSP tools from shared registries")
	}
	if _, ok := reg.Get("media_probe"); ok {
		t.Fatal("background policy should remove media tools that launch external processes")
	}
	if _, ok := reg.Get("read_file"); !ok {
		t.Fatal("background policy should keep ordinary read-only tools")
	}
}

func TestCommitSubject(t *testing.T) {
	if got := commitSubject("writer", "add the parser"); got != "writer: add the parser" {
		t.Errorf("got %q", got)
	}
	if got := commitSubject("writer", ""); got != "writer: dispatched changes" {
		t.Errorf("empty desc: got %q", got)
	}
	long := commitSubject("writer", strings.Repeat("x", 200))
	if len(long) > 72 {
		t.Errorf("subject not capped: len=%d", len(long))
	}
	if strings.HasSuffix(long, ".") {
		t.Errorf("subject must not end with a period: %q", long)
	}
}

// TestCommitSubject_MultiByteTruncationStaysValidUTF8 is the byte-safe
// truncation regression: ApplyCommit's validator measures the subject in
// BYTES (CommitSubjectMaxLen=72), so a raw subj[:72] byte slice can split a
// multi-byte UTF-8 rune (CJK, emoji) straddling the cut point, producing an
// invalid-UTF-8 commit subject. The cut must land on a rune boundary while
// staying at or under 72 bytes.
func TestCommitSubject_MultiByteTruncationStaysValidUTF8(t *testing.T) {
	// A 3-byte-per-rune CJK description long enough to force truncation well
	// past byte offset 72, landing mid-rune under the old raw slice.
	desc := strings.Repeat("你", 40)
	got := commitSubject("writer", desc)
	if len(got) > 72 {
		t.Fatalf("subject not capped: len=%d bytes: %q", len(got), got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncated subject is not valid UTF-8: %q", got)
	}
}

// TestOutOfScopeWorkerChanges_RenameSourceChecked is the rename-source
// regression: statusPaths previously kept only the DESTINATION of a rename
// line, so `git mv other.txt owned.txt` (moving a file this worker never
// owned into its own filename) passed unnoticed — only "owned.txt" (in
// scope) was ever checked, never "other.txt" (someone else's file).
func TestOutOfScopeWorkerChanges_RenameSourceChecked(t *testing.T) {
	repo := dispatchTestRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "add other.txt"}} {
		if _, err := gitOutput(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if _, err := gitOutput(ctx, repo, "mv", "other.txt", "owned.txt"); err != nil {
		t.Fatalf("git mv: %v", err)
	}

	outside := outOfScopeWorkerChanges(ctx, repo, []string{"owned.txt"})
	if len(outside) == 0 {
		t.Fatal("a rename whose SOURCE is out of scope must be flagged, even though the destination is owned")
	}
	found := false
	for _, p := range outside {
		if p == "other.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the rename SOURCE %q among out-of-scope paths, got %v", "other.txt", outside)
	}
}

// TestOutOfScopeWorkerChanges_ArrowInFilenameNotMisparsedAsRename is the
// status-line-parsing regression: statusPaths previously split on the raw
// text " -> " with no check that the line's status code actually denotes a
// rename/copy (R/C), so a plain untracked file whose NAME contains that
// literal substring got misparsed and truncated to whatever followed it.
func TestOutOfScopeWorkerChanges_ArrowInFilenameNotMisparsedAsRename(t *testing.T) {
	repo := dispatchTestRepo(t)
	ctx := context.Background()
	weird := "weird -> file.txt"
	if err := os.WriteFile(filepath.Join(repo, weird), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outside := outOfScopeWorkerChanges(ctx, repo, []string{"owned.txt"})
	found := false
	for _, p := range outside {
		if p == weird {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the untracked file to be reported by its FULL literal name %q, got %v", weird, outside)
	}
}

// TestFormatResult_ErroredWorkerBranchExcludedFromIntegrate is the
// integrate-recommendation regression: a worker that ended TaskErrored (e.g.
// it left out-of-scope changes uncommitted) can still have a real commit on
// its branch from earlier in its run. formatResult's "call integrate with
// branches [...]" recommendation must exclude that branch even though
// c.commit is non-empty — recommending it would defeat dispatch's whole
// partition-by-files safety story for exactly the case it's meant to catch.
func TestFormatResult_ErroredWorkerBranchExcludedFromIntegrate(t *testing.T) {
	writer := &subagents.AgentConfig{Name: "writer"}
	d := &DispatchTool{}
	children := []*dispatchChild{
		{
			cfg: writer, isWrite: true,
			spec:      dispatchTaskSpec{Description: "bad", Files: []string{"a.go"}},
			status:    subagents.TaskErrored,
			errored:   true,
			branch:    "worktree-dispatch-x-1",
			commit:    "deadbeef",
			commitErr: "out-of-scope changes left uncommitted: rogue.go",
		},
		{
			cfg: writer, isWrite: true,
			spec:   dispatchTaskSpec{Description: "good", Files: []string{"b.go"}},
			status: subagents.TaskCompleted,
			branch: "worktree-dispatch-x-2",
			commit: "cafebabe",
		},
	}
	out := d.formatResult("goal", "batch-1", children, true)

	parts := strings.SplitN(out, "Next:", 2)
	if len(parts) != 2 {
		t.Fatalf("expected a 'Next: call integrate' recommendation, got:\n%s", out)
	}
	if strings.Contains(parts[1], "worktree-dispatch-x-1") {
		t.Errorf("errored worker's branch leaked into the integrate recommendation: %s", parts[1])
	}
	if !strings.Contains(parts[1], "worktree-dispatch-x-2") {
		t.Errorf("healthy worker's branch missing from the integrate recommendation: %s", parts[1])
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("expected the errored-but-committed worker's per-task line to say FAILED:\n%s", out)
	}
}

// TestDispatchPanic_DoesNotClobberAlreadyCompletedResult is the state-
// integrity regression: the panic-recovery defer used to unconditionally
// overwrite c.result/c.errored and re-call MarkDone, even for a panic that
// struck AFTER the real MarkDone already recorded a successful result (e.g.
// inside fireBackgroundDone's own callback). MarkDone's own contract
// overwrites Result/Errored unconditionally on a second call, so an
// unguarded second call would silently replace a genuine success with a
// fabricated panic message. The defer must skip entirely once the task is
// already terminal.
func TestDispatchPanic_DoesNotClobberAlreadyCompletedResult(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	// Panic on the FIRST fireBackgroundDone invocation only — by the time
	// this callback runs, runDispatchChild's normal-completion path has
	// ALREADY called the real MarkDone with the genuine result (the
	// callback is the very next thing it does), so this panic strikes in
	// normal (non-recover) code, exactly the scenario the file's own
	// comments call out ("fireBackgroundDone, or the onBackgroundDone
	// callback that runs TUI code"). It IS still caught by
	// runDispatchChild's top-level defer (which wraps the whole function
	// body), so the guard added there is what's under test.
	var panicOnce sync.Once
	var mu sync.Mutex
	fireCounts := map[string]int{}
	d.Agent.SetBackgroundDoneCallback(func(e SubagentBackgroundDone) {
		mu.Lock()
		fireCounts[e.TaskID]++
		mu.Unlock()
		fired := false
		panicOnce.Do(func() { fired = true })
		if fired {
			panic("boom inside fireBackgroundDone callback")
		}
	})

	if _, err := d.Execute(context.Background(), `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitForTasksDone(t, d.Agent.Tasks, 2, 5*time.Second)

	for _, tk := range d.Agent.Tasks.List() {
		mu.Lock()
		n := fireCounts[tk.ID]
		mu.Unlock()
		if n != 1 {
			t.Errorf("task %s: fireBackgroundDone callback invoked %d times, want exactly 1 — a panic inside it must not cause the recover to re-fire it", tk.ID[:8], n)
		}
		if tk.Status != subagents.TaskCompleted {
			t.Errorf("task %s: Status = %v, want TaskCompleted — a panic in code that runs AFTER the real MarkDone must not overwrite an already-terminal task", tk.ID[:8], tk.Status)
		}
		if tk.Errored {
			t.Errorf("task %s: Errored = true, want false — the real success must survive the later panic", tk.ID[:8])
		}
		if strings.Contains(tk.Result, "boom inside fireBackgroundDone callback") || strings.Contains(tk.Result, "dispatch subagent") {
			t.Errorf("task %s: Result was clobbered with a fabricated panic message: %q", tk.ID[:8], tk.Result)
		}
	}
}

// TestDispatchSandbox_ForegroundConstructionFailureEmitsSubagentDone is the
// scrollback-hang regression: a foreground write worker whose sandbox fails
// to construct must still emit SubagentDone to the parent (the same channel
// that painted its SubagentStart card), or that card is left permanently
// "running" in the scrollback even though the task itself is correctly
// marked errored.
func TestDispatchSandbox_ForegroundConstructionFailureEmitsSubagentDone(t *testing.T) {
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
	wantErr := fmt.Errorf("podman not found in PATH")
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

	// events is passed directly as runDispatchChild's parentEvents argument.
	events := make(chan Event, 8)
	d.runDispatchChild(context.Background(), c, "batch-1", false, events, nil)
	close(events)

	sawStart, sawDone := false, false
	for ev := range events {
		switch ev.(type) {
		case SubagentStart:
			sawStart = true
		case SubagentDone:
			sawDone = true
		}
	}
	if !sawStart {
		t.Fatal("test setup: expected a SubagentStart event")
	}
	if !sawDone {
		t.Error("a foreground worker whose sandbox failed to construct must still emit SubagentDone, or its scrollback card is stuck 'running' forever")
	}
}

// TestDispatchPanic_ForegroundEmitsSubagentDone mirrors
// TestBackgroundDispatchPanicStillFiresDoneCallback for the foreground path:
// a panic in runDispatchChild's own orchestration must still emit
// SubagentDone to the parent so a foreground child's scrollback card
// resolves, not just its registry record.
func TestDispatchPanic_ForegroundEmitsSubagentDone(t *testing.T) {
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
	d := &DispatchTool{Agent: at, Enabled: true}

	c := &dispatchChild{
		spec: dispatchTaskSpec{Prompt: "p", Description: "d"},
		cfg:  &at.Configs[0],
	}
	at.Tasks.Add(d.prepareDispatchChild(c, "batch-1", false))

	// events is passed directly as runDispatchChild's parentEvents argument
	// (forwardToParent uses it as-is, independent of ctx) — no need to also
	// thread it through a context value here.
	events := make(chan Event, 8)

	// nil ctx is deliberate — makes context.WithCancel(ctx) panic AFTER the
	// recover is armed, taking the same recover path as
	// TestBackgroundDispatchPanicStillFiresDoneCallback. forwardToParent's
	// SubagentStart emission (which runs before that point) sends on
	// parentEvents directly and never touches ctx for a non-blocking event,
	// so it's unaffected by ctx being nil.
	//nolint:staticcheck // SA1012: intentional nil ctx to force the panic
	d.runDispatchChild(nil, c, "batch-1", false, events, nil)
	close(events)

	sawDone := false
	for ev := range events {
		if _, ok := ev.(SubagentDone); ok {
			sawDone = true
		}
	}
	if !sawDone {
		t.Error("a foreground panic must still emit SubagentDone so the scrollback card resolves")
	}
}

// TestDispatch_ForegroundRespectsConcurrencyCap is the defense-in-depth
// regression for foreground admission: unlike every other admission path in
// this file, the foreground branch used to insert its tasks with a bare
// Add loop instead of an atomic TryReserveBatch, relying on convention
// (ParallelSafe=false, no reentrant foreground-spawn route) rather than code
// to stay under MaxForegroundSubagents.
func TestDispatch_ForegroundRespectsConcurrencyCap(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = false

	// Saturate the foreground cap with already-running tasks.
	for i := 0; i < MaxForegroundSubagents; i++ {
		d.Agent.Tasks.Add(&subagents.Task{
			ID:     subagents.NewTaskID(),
			Status: subagents.TaskRunning,
		})
	}

	out, err := d.Execute(context.Background(), `{"goal":"x","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "exceed") {
		t.Errorf("expected the foreground cap to reject the batch, got:\n%s", out)
	}
	if branches := gitListBranches(t, repoRoot, "worktree-dispatch-*"); len(branches) != 0 {
		t.Errorf("rejected batch leaked worktree branches: %v", branches)
	}
}

// --- Execute gating tests --------------------------------------------------

func newDispatchToolForGating() *DispatchTool {
	at := &AgentTool{
		Configs: []subagents.AgentConfig{
			{Name: "writer"},
			{Name: "reader", Tools: []string{"read_file"}},
		},
		Tasks:    subagents.NewRegistry(),
		PlanMode: &PlanModeState{},
	}
	return &DispatchTool{Agent: at, Enabled: true}
}

func TestDispatch_GatingErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		d := newDispatchToolForGating()
		d.Enabled = false
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"reader","prompt":"a"},{"subagent_type":"reader","prompt":"b"}]}`)
		if !strings.Contains(out, "experimental") || !strings.Contains(out, "--experimental dispatch") {
			t.Errorf("expected experimental gate message, got %q", out)
		}
	})
	t.Run("too few tasks", func(t *testing.T) {
		d := newDispatchToolForGating()
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"reader","prompt":"a"}]}`)
		if !strings.Contains(out, "at least 2 tasks") {
			t.Errorf("got %q", out)
		}
	})
	t.Run("unknown subagent", func(t *testing.T) {
		d := newDispatchToolForGating()
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"nope","prompt":"a"},{"subagent_type":"reader","prompt":"b"}]}`)
		if !strings.Contains(out, "unknown subagent_type") {
			t.Errorf("got %q", out)
		}
	})
	t.Run("write overlap rejected before any work", func(t *testing.T) {
		d := newDispatchToolForGating()
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"writer","prompt":"a","files":["x.go"]},{"subagent_type":"writer","prompt":"b","files":["x.go"]}]}`)
		if !strings.Contains(out, "non-overlapping") {
			t.Errorf("got %q", out)
		}
	})
	t.Run("write in plan mode rejected", func(t *testing.T) {
		d := newDispatchToolForGating()
		d.Agent.PlanMode.Active.Store(true)
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"writer","prompt":"a","files":["x.go"]},{"subagent_type":"writer","prompt":"b","files":["y.go"]}]}`)
		if !strings.Contains(out, "plan mode") {
			t.Errorf("got %q", out)
		}
	})
}

// --- end-to-end fan-out ----------------------------------------------------

// dispatchWriteStreamer is a stateless test adapter: on the first call for a
// child it emits a write_file tool call to the path named after "TESTWRITE:"
// in the user prompt; once a tool result is in the history it emits a final
// reply. Stateless so concurrent children don't race.
type dispatchWriteStreamer struct{}

func (dispatchWriteStreamer) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent, 4)
	hasToolResult := false
	userPrompt := ""
	for _, m := range msgs {
		if m.Role == adapter.RoleTool {
			hasToolResult = true
		}
		if m.Role == adapter.RoleUser {
			userPrompt = m.Content
		}
	}
	go func() {
		defer close(out)
		if hasToolResult {
			out <- sseDone("done — file written")
			return
		}
		path := extractTestWritePath(userPrompt)
		args := fmt.Sprintf(`{"path":%q,"content":%q}`, path, "hello from "+path)
		out <- sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: args})
	}()
	return out
}

func extractTestWritePath(prompt string) string {
	const marker = "TESTWRITE:"
	i := strings.Index(prompt, marker)
	if i < 0 {
		return "out.txt"
	}
	rest := prompt[i+len(marker):]
	return strings.Fields(rest)[0]
}

// dispatchTestRepo creates a git repo with committer identity configured (so
// ApplyCommit's `git commit` works) and one base commit.
func dispatchTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := gitOutput(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "base"}} {
		if _, err := gitOutput(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	// Disable background git maintenance in short-lived test repositories. On
	// CI, asynchronous maintenance can still be touching .git/objects when
	// testing.TempDir cleanup begins, which makes os.RemoveAll occasionally
	// fail with "directory not empty" even though the test assertions passed.
	for _, args := range [][]string{
		{"config", "gc.auto", "0"},
		{"config", "maintenance.auto", "false"},
	} {
		if _, err := gitOutput(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	// Worktrees live under ~/.yottacode/worktrees/<repo-slug>/ (outside
	// t.TempDir), so remove that slug dir on cleanup to avoid leaving
	// cruft behind across test runs.
	t.Cleanup(func() {
		os.RemoveAll(filepath.Dir(worktree.Dir(dir, "x")))
	})
	return dir
}

func newDispatchToolE2E(t *testing.T, repoRoot string) *DispatchTool {
	t.Helper()
	auto := &AutoModeState{}
	auto.Active.Store(true) // so child write_file auto-approves without a decisions channel
	at := &AgentTool{
		Configs:        []subagents.AgentConfig{{Name: "writer", Description: "implements a file"}},
		Tasks:          subagents.NewRegistry(),
		Adapter:        dispatchWriteStreamer{},
		ParentRegistry: NewRegistry(),
		Permissions:    nil,
		AutoMode:       auto,
		PlanMode:       &PlanModeState{},
		YoloMode:       &YoloModeState{},
		Cwd:            NewCwdRef(repoRoot),
		TranscriptDir:  t.TempDir(),
	}
	return &DispatchTool{Agent: at, Enabled: true}
}

func TestDispatch_EndToEnd_TwoWriteTasks(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	args := `{"goal":"build two files","tasks":[
		{"subagent_type":"writer","description":"file a","prompt":"create the file. TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"file b","prompt":"create the file. TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`
	out, err := d.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch Execute: %v", err)
	}

	// Two worktree branches were created and committed. Use a clean ref
	// format: git marks worktree-checked-out branches with "+", not "*".
	branchOut, gErr := gitOutput(context.Background(), repoRoot, "branch", "--list", "worktree-dispatch-*", "--format=%(refname:short)")
	if gErr != nil {
		t.Fatalf("git branch: %v", gErr)
	}
	branchNames := nonEmptyLines(branchOut)
	if len(branchNames) != 2 {
		t.Fatalf("expected 2 dispatch branches, got %v", branchNames)
	}

	// Each branch carries its owned file with the expected content.
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		found := false
		for _, br := range branchNames {
			if content, e := gitOutput(context.Background(), repoRoot, "show", br+":"+f); e == nil {
				if strings.TrimSpace(content) == "hello from "+f {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("no dispatch branch committed %s with expected content", f)
		}
	}

	// Result mentions integration next-step.
	if !strings.Contains(out, "call integrate") {
		t.Errorf("result missing integrate hint:\n%s", out)
	}
	// Base working tree is untouched (isolation): no alpha/beta in repoRoot.
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		if _, err := os.Stat(filepath.Join(repoRoot, f)); err == nil {
			t.Errorf("%s leaked into the base working tree — worktree isolation broken", f)
		}
	}
}

func TestDispatch_Foreground_OutOfScopeWriteIsNotCommitted(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	out, err := d.Execute(context.Background(), `{"goal":"stay scoped","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:beta.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:gamma.txt","files":["gamma.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if strings.Contains(out, "beta.txt") && strings.Contains(out, "committed") {
		t.Fatalf("out-of-scope beta.txt should not be committed, got:\n%s", out)
	}

	branchOut, gErr := gitOutput(context.Background(), repoRoot, "branch", "--list", "worktree-dispatch-*", "--format=%(refname:short)")
	if gErr != nil {
		t.Fatalf("git branch: %v", gErr)
	}
	for _, br := range nonEmptyLines(branchOut) {
		if _, e := gitOutput(context.Background(), repoRoot, "show", br+":beta.txt"); e == nil {
			t.Fatalf("out-of-scope beta.txt was committed on %s", br)
		}
	}
}

// TestDispatch_Background_DoneCallbackCarriesCommitStatus is the P1
// regression for the async path: the background-done callback must report
// whether the worker actually committed (Committed + CommitSHA), not fire a
// no-op. Before the fix the event carried no commit status at all, so a
// banner could imply integrate-ready work on an empty branch.
func TestDispatch_Background_DoneCallbackCarriesCommitStatus(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	done := make(chan SubagentBackgroundDone, 8)
	d.Agent.SetBackgroundDoneCallback(func(e SubagentBackgroundDone) { done <- e })

	_, err := d.Execute(context.Background(), `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	got := map[string]SubagentBackgroundDone{}
	timeout := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case e := <-done:
			got[e.TaskID] = e
		case <-timeout:
			t.Fatalf("only %d/2 background-done callbacks fired", len(got))
		}
	}
	// Receiving the async callback proves the commit status was reported, but
	// keep the usual detached-worker join before TempDir cleanup starts. This
	// avoids test teardown racing any final goroutine/file-handle cleanup.
	waitForTasksDone(t, d.Agent.Tasks, 2, 5*time.Second)
	for _, e := range got {
		if !e.Committed || e.CommitSHA == "" {
			t.Errorf("worker %s should report Committed with a SHA, got %+v", e.TaskID[:8], e)
		}
		if e.CommitErr != "" {
			t.Errorf("clean worker %s should have no CommitErr, got %q", e.TaskID[:8], e.CommitErr)
		}
	}
}

// TestDispatch_Foreground_HookRejectionSurfacesReason is the P1 regression
// for silent commit failures: a pre-commit hook rejecting the auto-commit
// must be reported as a reason (NOT a clean "no changes"), and such a branch
// must be excluded from the integrate hint.
func TestDispatch_Foreground_HookRejectionSurfacesReason(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	// A pre-commit hook that always fails. Linked worktrees share the main
	// repo's hooks dir, so this fires for every worker's commit.
	hookDir := filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "pre-commit"),
		[]byte("#!/bin/sh\necho 'lint failed: forbidden token'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDispatchToolE2E(t, repoRoot)

	out, err := d.Execute(context.Background(), `{"goal":"x","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "NOT committed") {
		t.Errorf("expected a NOT committed reason, got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "hook") {
		t.Errorf("expected the hook rejection surfaced as the reason, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing to integrate") {
		t.Errorf("expected 'nothing to integrate' when every commit was rejected, got:\n%s", out)
	}
}

// TestBranchTip is the P1 regression for commit-presence derivation: a
// branch with a commit beyond base reports its tip SHA (so a worker that
// committed its own work and left a clean tree is recognized as having
// produced commits, instead of being judged by end-of-run dirtiness); a
// branch even with base reports "".
func TestBranchTip(t *testing.T) {
	ctx := context.Background()
	repo := dispatchTestRepo(t)
	base, err := gitOutput(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)

	// A branch with no commits beyond base → "".
	if tip := branchTip(ctx, repo, base); tip != "" {
		t.Errorf("branch even with base should report no tip, got %q", tip)
	}

	// Add a commit (a "self-committed worker" leaving a clean tree), then the
	// tip is reported even though the worktree is clean.
	if err := os.WriteFile(filepath.Join(repo, "self.txt"), []byte("self\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "self-committed"}} {
		if _, err := gitOutput(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if gitWorktreeDirty(ctx, repo) {
		t.Fatal("tree should be clean after the worker's own commit")
	}
	tip := branchTip(ctx, repo, base)
	if tip == "" {
		t.Fatal("a committed-but-clean branch must still report its tip (P1 presence derivation)")
	}
	head, _ := gitOutput(ctx, repo, "rev-parse", "HEAD")
	if tip != strings.TrimSpace(head) {
		t.Errorf("branchTip = %q, want HEAD %q", tip, strings.TrimSpace(head))
	}

	// Empty base → "" (guards the no-base path).
	if tip := branchTip(ctx, repo, ""); tip != "" {
		t.Errorf("empty base should report no tip, got %q", tip)
	}
}

// TestDispatch_Background_EnforcesMaxConcurrent is the P3 regression: a
// background dispatch must respect MaxBackgroundSubagents (repeated calls
// would otherwise stack unbounded detached workers), and a rejected batch
// must leak no worktrees.
func TestDispatch_Background_EnforcesMaxConcurrent(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	// Saturate the background cap with already-running tasks.
	for i := 0; i < MaxBackgroundSubagents; i++ {
		d.Agent.Tasks.Add(&subagents.Task{
			ID:         subagents.NewTaskID(),
			Status:     subagents.TaskRunning,
			Background: true,
		})
	}

	out, err := d.Execute(context.Background(), `{"goal":"x","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "exceed") {
		t.Errorf("expected the background cap to reject the batch, got:\n%s", out)
	}
	// The rejection must happen before any worktree is created.
	if branches := gitListBranches(t, repoRoot, "worktree-dispatch-*"); len(branches) != 0 {
		t.Errorf("rejected batch leaked worktree branches: %v", branches)
	}
}

// TestDispatch_ConcurrencyCap_ConfigOverride is the configurable-cap
// regression for dispatch specifically: DispatchTool shares AgentTool's
// resolved cap (t.Agent.MaxConcurrentSubagents), so a session configured for
// more headroom lets a dispatch batch through where the fixed default of 8
// would have rejected it — while the per-call decomposition limit
// (MaxDispatchTasksPerCall) stays fixed at 8 regardless.
func TestDispatch_ConcurrencyCap_ConfigOverride(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true
	d.Agent.MaxConcurrentSubagents = 10

	// Pre-seed AT the fixed default of 8 (already at/over what the old code
	// would allow) — the coming 2-task batch brings the total to 10, still
	// within the configured override.
	for i := 0; i < 8; i++ {
		d.Agent.Tasks.Add(&subagents.Task{
			ID:         subagents.NewTaskID(),
			Status:     subagents.TaskRunning,
			Background: true,
		})
	}

	out, err := d.Execute(context.Background(), `{"goal":"x","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if strings.Contains(out, "exceed") {
		t.Errorf("a batch that stays under the configured override of 10 must not be rejected, got:\n%s", out)
	}

	// Release the synthetic saturation tasks after the admission check so the
	// helper below only waits on real workers that can actually finish.
	for _, tk := range d.Agent.Tasks.List() {
		if tk.Background && tk.Branch == "" {
			d.Agent.Tasks.MarkDone(tk.ID, subagents.TaskCompleted, "synthetic cap fixture", false, 0)
		}
	}
	// Join the accepted background batch before issuing the independent
	// decomposition-limit probe below. Otherwise those workers can still be
	// committing into their temp worktrees while testing.TempDir cleanup starts,
	// causing a flaky "directory not empty" cleanup failure unrelated to the
	// cap behavior under test.
	waitForTasksDone(t, d.Agent.Tasks, 10, 5*time.Second)

	// The per-call decomposition limit is independent of the override: 9
	// tasks in one call still exceeds MaxDispatchTasksPerCall (8) even
	// though the session's concurrency override (10) would allow them to run.
	tasks := make([]string, MaxDispatchTasksPerCall+1)
	for i := range tasks {
		tasks[i] = fmt.Sprintf(`{"subagent_type":"writer","description":"t%d","prompt":"TESTWRITE:f%d.txt","files":["f%d.txt"]}`, i, i, i)
	}
	out, err = d.Execute(context.Background(), fmt.Sprintf(`{"goal":"x","tasks":[%s]}`, strings.Join(tasks, ",")))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("at most %d concurrent subtasks", MaxDispatchTasksPerCall)) {
		t.Errorf("raising the session concurrency cap must not raise the fixed per-call task limit of %d, got:\n%s", MaxDispatchTasksPerCall, out)
	}
}

// TestDispatch_Background_OutOfWorktreeWriteDoesNotHang is the P3.8
// regression: a background worker that tries to write outside its worktree
// trips PathTrustElevationNeeded. With no human to prompt, the drain loop
// must auto-deny and FEED the decision so the worker finishes — before the
// fix it blocked forever on the unfed decisions channel (the iteration cap
// can't fire mid-tool), wedging the slot. The test would time out if the
// worker hung.
func TestDispatch_Background_OutOfWorktreeWriteDoesNotHang(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	// Absolute path OUTSIDE any worktree — the write must be refused.
	escape := filepath.Join(t.TempDir(), "escape.txt")
	args := fmt.Sprintf(`{"goal":"escape attempt","tasks":[
		{"subagent_type":"writer","description":"in","prompt":"TESTWRITE:inside.txt","files":["inside.txt"]},
		{"subagent_type":"writer","description":"out","prompt":"TESTWRITE:%s","files":["out.txt"]}
	]}`, escape)
	if _, err := d.Execute(context.Background(), args); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Joins the detached workers; fails the test if the escaping worker hung.
	waitForTasksDone(t, d.Agent.Tasks, 2, 5*time.Second)

	if _, err := os.Stat(escape); err == nil {
		t.Errorf("out-of-worktree write should have been denied, but %s exists", escape)
	}
}

// TestDispatch_Foreground_OutOfWorktreeWrite_NoDeadlock is the E2E regression
// for the approval-gate deadlock, through the real dispatch foreground path.
// Foreground dispatch installs its own approval gate; a write worker that
// targets a path outside its worktree trips PathTrustElevationNeeded, whose
// handler holds the gate across blocking on the decision. Before the fix the
// child's own loop and the drain-loop forwarder contended on that same gate and
// hung. With the fix it returns quickly (the escaping write denied).
func TestDispatch_Foreground_OutOfWorktreeWrite_NoDeadlock(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = false // force foreground

	escape := filepath.Join(t.TempDir(), "escape.txt")
	args := fmt.Sprintf(`{"goal":"fg escape","background":false,"tasks":[
		{"subagent_type":"writer","description":"in","prompt":"TESTWRITE:inside.txt","files":["inside.txt"]},
		{"subagent_type":"writer","description":"out","prompt":"TESTWRITE:%s","files":["out.txt"]}
	]}`, escape)

	// Wire parent events + decisions so the foreground forward path (which takes
	// the gate) is exercised; answer Deny to the path-trust modal like a real UI.
	events := make(chan Event, 64)
	decisions := make(chan Decision, 1)
	ctx := WithParentDecisions(WithParentEvents(context.Background(), events), decisions)
	go func() {
		for ev := range events {
			if _, ok := ev.(PathTrustElevationNeeded); ok {
				decisions <- Deny
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		_, _ = d.Execute(ctx, args)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("DEADLOCK: foreground dispatch hung on the approval gate for an out-of-worktree write")
	}
}

func TestDispatch_ResolveBackground(t *testing.T) {
	bp := func(b bool) *bool { return &b }
	cases := []struct {
		name     string
		req      *bool
		hasWrite bool
		supports bool
		wantBg   bool
		wantNote bool
	}{
		{"write batch defaults to background", nil, true, true, true, false},
		{"read batch defaults to foreground", nil, false, true, false, false},
		{"explicit false overrides write→fg", bp(false), true, true, false, false},
		{"explicit true overrides read→bg", bp(true), false, true, true, false},
		{"background unsupported falls back + notes", bp(true), false, false, false, true},
		{"write default unsupported falls back + notes", nil, true, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &DispatchTool{SupportsBackground: tc.supports}
			bg, note := d.resolveBackground(tc.req, tc.hasWrite)
			if bg != tc.wantBg {
				t.Errorf("bg = %v, want %v", bg, tc.wantBg)
			}
			if (note != "") != tc.wantNote {
				t.Errorf("note = %q, wantNote = %v", note, tc.wantNote)
			}
		})
	}
}

// waitForTasksDone polls the registry until n tasks exist and none is still
// running, or the timeout elapses. Used to join detached background workers
// before asserting on their committed branches.
func waitForTasksDone(t *testing.T, reg *subagents.Registry, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks := reg.List()
		if len(tasks) >= n {
			allDone := true
			for _, tk := range tasks {
				if tk.Status == subagents.TaskRunning {
					allDone = false
					break
				}
			}
			if allDone {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d subagent tasks to finish", n)
}

// TestDispatch_Background_EndToEnd runs a write batch in background mode:
// dispatch returns immediately (non-blocking) with a batch handle, the
// detached workers commit to their branches, and the base tree is untouched.
func TestDispatch_Background_EndToEnd(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true // enable real background execution

	// Write batch → defaults to background (no explicit flag).
	out, err := d.Execute(context.Background(), `{"goal":"two files in bg","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Returns immediately with the background handle (not per-task results).
	if !strings.Contains(out, "BACKGROUND") {
		t.Errorf("expected background result, got:\n%s", out)
	}
	if !strings.Contains(out, "call integrate") {
		t.Errorf("expected integrate hint, got:\n%s", out)
	}

	// Join the detached workers, then assert they committed their branches.
	waitForTasksDone(t, d.Agent.Tasks, 2, 5*time.Second)

	branches := gitListBranches(t, repoRoot, "worktree-dispatch-*")
	if len(branches) != 2 {
		t.Fatalf("expected 2 dispatch branches, got %v", branches)
	}
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		found := false
		for _, br := range branches {
			if content, e := gitOutput(context.Background(), repoRoot, "show", br+":"+f); e == nil &&
				strings.TrimSpace(content) == "hello from "+f {
				found = true
			}
		}
		if !found {
			t.Errorf("no background dispatch branch committed %s", f)
		}
	}
	// Workers were marked background in the registry.
	for _, tk := range d.Agent.Tasks.List() {
		if !tk.Background {
			t.Errorf("task %s should be marked background", tk.ID[:8])
		}
	}
}

// TestDispatch_RespectsSessionTokenBudget is the cost-ceiling regression:
// dispatch children record their estimated spend via MarkDone exactly like
// Agent-tool children, so they DEPLETE the session token budget — but
// DispatchTool.Execute never checked that budget, making a fan-out batch (N
// child loops at once) the one delegation path the backstop could not stop.
// The batch must be refused before any worktree is created.
func TestDispatch_RespectsSessionTokenBudget(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.Agent.MaxSessionTokens = 1000

	// A finished child that already consumed the whole budget.
	spent := &subagents.Task{ID: subagents.NewTaskID(), Status: subagents.TaskRunning}
	d.Agent.Tasks.Add(spent)
	d.Agent.Tasks.MarkDone(spent.ID, subagents.TaskCompleted, "done", false, 1200)

	out, err := d.Execute(context.Background(), `{"goal":"x","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "budget") {
		t.Errorf("expected the session token budget to reject the batch, got:\n%s", out)
	}
	// Refused before anything was spawned or created on disk.
	if branches := gitListBranches(t, repoRoot, "worktree-dispatch-*"); len(branches) != 0 {
		t.Errorf("budget-rejected batch leaked worktree branches: %v", branches)
	}
	if n := d.Agent.Tasks.ActiveCount(); n != 0 {
		t.Errorf("budget-rejected batch registered %d running tasks, want 0", n)
	}
}

// TestDispatch_Background_CompletionAsksToWake is the async-re-entry
// regression. Background is the DEFAULT for write batches, and its completion
// event omitted NotifyOnDone — so the TUI's wake arms (which gate on that flag)
// never fired and the fan-out → integrate workflow silently stalled after the
// workers finished, until the user happened to type something. Every background
// worker's completion must ask for a wake and carry its BatchID, which is what
// lets the TUI coalesce a batch into a single wake turn.
func TestDispatch_Background_CompletionAsksToWake(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	done := make(chan SubagentBackgroundDone, 4)
	d.Agent.SetBackgroundDoneCallback(func(e SubagentBackgroundDone) { done <- e })

	if _, err := d.Execute(context.Background(), `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitForTasksDone(t, d.Agent.Tasks, 2, 10*time.Second)

	batches := map[string]int{}
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			if !e.NotifyOnDone {
				t.Errorf("background completion %s did not ask to wake the model — the batch would stall before integrate", e.TaskID)
			}
			if e.BatchID == "" {
				t.Errorf("background completion %s carries no BatchID — the TUI cannot coalesce the batch into one wake", e.TaskID)
			}
			batches[e.BatchID]++
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for background completions")
		}
	}
	if len(batches) != 1 {
		t.Errorf("both workers should share one BatchID, got %v", batches)
	}

	// The registry record must agree — it is what reconcileSubagentCompletions
	// reads to heal a dropped completion event.
	for _, tk := range d.Agent.Tasks.List() {
		if !tk.NotifyOnDone {
			t.Errorf("task %s: NotifyOnDone not persisted on the registry record", tk.ID)
		}
	}
}

// TestDispatch_MarksCommittingWindow: the auto-commit runs on a
// cancellation-detached context, so session teardown cannot stop it and must
// instead wait it out. That wait is driven by Task.Committing, so dispatch has
// to actually raise the flag while the commit is in flight — and lower it after,
// or a finished worker would hold every future quit open. A slow pre-commit
// hook holds the window open long enough to observe.
func TestDispatch_MarksCommittingWindow(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	hook := filepath.Join(repoRoot, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 0.4\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	if _, err := d.Execute(context.Background(), `{"goal":"one file","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Catch the window while the hook is sleeping.
	sawCommitting := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d.Agent.Tasks.CommittingCount() > 0 {
			sawCommitting = true
			break
		}
		if d.Agent.Tasks.ActiveCount() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawCommitting {
		t.Error("no worker ever reported Committing — session teardown would abandon the commit on its flat grace deadline")
	}

	waitForTasksDone(t, d.Agent.Tasks, 2, 15*time.Second)
	if n := d.Agent.Tasks.CommittingCount(); n != 0 {
		t.Errorf("%d finished workers still flagged Committing — every later quit would pay the extended wait", n)
	}
}
