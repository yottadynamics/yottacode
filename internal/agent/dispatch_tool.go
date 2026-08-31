package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// DispatchToolName is the schema-visible name of the batch fan-out tool.
const DispatchToolName = "dispatch"

// maxDispatchReplyChars caps how much of each child's final reply is folded
// into the combined dispatch result, so a batch of chatty children can't
// blow the parent's context. The full reply is always in the transcript.
const maxDispatchReplyChars = 4000

// DispatchTool fans a batch of subtasks out to subagents that run
// concurrently. Write batches usually return immediately and continue in
// background worktrees; all-read batches wait and return their findings
// labeled together so the parent can assemble them. Write-capable subtasks
// each run in an isolated git worktree+branch (no shared-cwd clobbering);
// read-only subtasks share the parent cwd. The parent partitions work by
// declaring each write subtask's file scope; an overlap guard rejects
// colliding scopes so the branches merge cleanly via the integrate tool.
//
// It reuses AgentTool for routing, transcripts, the task registry, and the
// child loop runner (runChild) — DispatchTool is the batch + worktree
// orchestration layer on top.
type DispatchTool struct {
	// Agent is the session's AgentTool. DispatchTool reuses its Configs,
	// Tasks registry, model routing, transcript dir, mode pointers, and
	// runChild. Required.
	Agent *AgentTool

	// SupportsImages mirrors the adapter profile so worktree children's
	// read_file can return image blocks.
	SupportsImages bool

	// SupportsBackground reports whether this session can host detached
	// background workers (true in the TUI, false in oneshot where there's
	// no long-running session to surface async completions). When false,
	// a background dispatch silently falls back to foreground/waiting mode.
	SupportsBackground bool

	// Enabled gates the tool behind the `dispatch` experimental feature.
	// When false, Execute returns a recoverable error string.
	Enabled bool

	// EnableSyntaxRanges lets dispatch workers use the same offline range-selection surface.
	EnableSyntaxRanges bool

	// AllowPDFIngestion lets dispatch workers use the same read_document
	// PDF gate as the parent session — see CoreToolDeps.AllowPDFIngestion.
	// read_document itself is always available to workers regardless.
	AllowPDFIngestion bool

	// AllowDocxPdfGeneration lets dispatch workers use the same
	// create_document docx/pdf gate as the parent session — see
	// CoreToolDeps.AllowDocxPdfGeneration. create_document itself is
	// always available to workers regardless.
	AllowDocxPdfGeneration bool

	// EnableLSP lets dispatch workers expose the same LSP tool surface as the
	// parent session, while writes still flow through the worker's owned-file
	// WriteOpts.
	EnableLSP bool

	// LSPServers carries optional per-language server command overrides.
	LSPServers map[string][]string

	// LSPDisabled lists language IDs whose server launch is disabled by config.
	LSPDisabled []string

	// SandboxFactory, when non-nil, constructs a per-write-worker Sandbox
	// (a fresh podman container mounted at the worker's worktree) — the
	// same posture the parent session's own Sandbox uses, inherited by
	// default rather than gated by a separate dispatch-level flag. Nil
	// means dispatch write-workers run run_bash on the host, same as
	// today. Read-only workers never need this: they reuse the parent's
	// registry (and its Sandbox) via buildChildRegistry.
	SandboxFactory SandboxFactory
}

func (t *DispatchTool) Name() string { return DispatchToolName }

func (t *DispatchTool) Description() string {
	var b strings.Builder
	b.WriteString("Fan a batch of independent subtasks out to subagents that run concurrently. ")
	b.WriteString("Use this to decompose a large piece of work (e.g. a PR) into smaller independent tasks. ")
	b.WriteString("WRITE subtasks (agents that can edit files) each run in their OWN git worktree+branch, so they never clobber each other or your working tree; you then call `integrate` to merge the branches into one branch for a PR. READ/research subtasks share the working dir and just return findings.\n\n")
	b.WriteString("CRITICAL — partition by files: give each WRITE subtask a `files` list naming the files it owns. The file sets MUST NOT overlap across write subtasks (the tool rejects the call if they do) — non-overlapping ownership is what makes the branches merge cleanly. A subtask may READ any file; it must only CREATE/EDIT files in its own set.\n\n")
	b.WriteString("Modes: write/implementation batches run in the BACKGROUND by default (returns a batch id + branches immediately; owned-file writes are auto-approved, while tests, shell, and other approval-requiring tools are denied; you call `integrate` once they finish). All-read/research batches run in the FOREGROUND (the call waits for the subtasks and returns every finding together for you to assemble right away). Set `background` explicitly to override. ")
	b.WriteString("Available subagent_type values are the same as the Agent tool's.")
	return b.String()
}

func (t *DispatchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "The overall objective the subtasks add up to (one or two sentences). Used for context and for the integration commit.",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "Run the batch in the background: returns a batch id + branches immediately in TUI sessions, workers run in their worktrees, you call integrate later. Non-interactive sessions cannot host detached workers and run foreground/waiting instead. Omit to use the default: background for TUI batches with write-capable tasks (parallel implementation), foreground/waiting for all-read batches.",
			},
			"tasks": map[string]any{
				"type":        "array",
				"description": "2 or more independent subtasks to run concurrently.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"subagent_type": map[string]any{
							"type":        "string",
							"description": "Which agent definition to dispatch (see the Agent tool for the list).",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "A 3-5 word label shown while the subtask runs.",
						},
						"prompt": map[string]any{
							"type":        "string",
							"description": "The self-contained task for this subagent. It has no access to the parent conversation or sibling subtasks.",
						},
						"files": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Repo-relative files this subtask OWNS (will create/edit). Required for write-capable subtasks; must not overlap any other write subtask's files. Ignored for read-only subtasks.",
						},
					},
					"required": []string{"subagent_type", "prompt"},
				},
			},
		},
		"required": []string{"goal", "tasks"},
	}
}

// RequiresApproval is false for the dispatch call itself — it's
// orchestration. Each child's own mutating tool calls still flow through
// the normal approval path (forwarded to the parent modal for foreground
// children, serialized across the batch by the approval gate).
func (t *DispatchTool) RequiresApproval(string) bool { return false }

// ParallelSafe is false: dispatch is a heavyweight orchestration call that
// itself fans out concurrent children. Running two dispatch calls at once
// would multiply worktree/branch churn and contend on the single approval
// modal with no benefit. The loop runs it on its own.
func (t *DispatchTool) ParallelSafe(string) bool { return false }

func (t *DispatchTool) PreviewCall(argsJSON string) string {
	a := parseDispatchArgs(argsJSON)
	return fmt.Sprintf("dispatch[%d tasks]: %s", len(a.Tasks), truncate(a.Goal, 60))
}

type dispatchTaskSpec struct {
	SubagentType string   `json:"subagent_type"`
	Description  string   `json:"description"`
	Prompt       string   `json:"prompt"`
	Files        []string `json:"files"`
}

type dispatchArgs struct {
	Goal string `json:"goal"`
	// Background is a pointer so we can tell "unset" (nil → smart
	// default) from an explicit true/false.
	Background *bool              `json:"background"`
	Tasks      []dispatchTaskSpec `json:"tasks"`
}

func parseDispatchArgs(argsJSON string) dispatchArgs {
	var a dispatchArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a
}

// dispatchChild is the resolved plan for one subtask, built before fan-out.
type dispatchChild struct {
	spec     dispatchTaskSpec
	cfg      *subagents.AgentConfig
	taskID   string
	isWrite  bool
	branch   string // write only
	worktree string // write only
	repoRoot string // write only: the main repo the worktree belongs to
	base     string // write only: the base commit the worktree branched from
	// task, adapter, model, and transcriptPath are resolved in Execute — BEFORE
	// any worker goroutine starts — so the whole batch can be admitted to the
	// registry in one atomic reservation (Registry.TryReserveBatch) instead of
	// each worker counting-then-inserting itself. Written pre-spawn only; the
	// goroutines mutate the post-run fields below and never these.
	task           *subagents.Task
	adapter        Streamer
	model          string
	transcriptPath string
	// filled after the run
	status  subagents.TaskStatus
	result  string
	errored bool
	tokens  int
	commit  string // branch tip SHA when the worker produced commits, else ""
	// commitErr explains why a write worker's branch has no committable
	// work when that's worth surfacing — a hook/lint rejection, a staging
	// failure, or an errored worker that left uncommitted work in its
	// worktree. Empty when commit presence is unambiguous (committed, or a
	// clean no-op). Cleared once commit presence is confirmed via rev-list.
	commitErr string
	// reclaimed is true when the worker's empty worktree+branch were
	// removed at the end of its run (no commits, clean tree — nothing to
	// keep), so the result can explain why the branch no longer exists.
	reclaimed bool
	// sandbox is this write worker's own Sandbox (a podman container
	// mounted at c.worktree), built from DispatchTool.SandboxFactory when
	// non-nil. Nil for read-only children (they reuse the parent's
	// registry/Sandbox) and whenever sandboxing isn't configured. Closed
	// exactly once, at whichever of runDispatchChild's two outcomes
	// (panic-recovery or normal completion) actually happens.
	sandbox Sandbox
}

func (t *DispatchTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return "error: `dispatch` is an experimental feature and is not enabled in this session. Enable it with `--experimental dispatch`, `YOTTACODE_EXPERIMENTAL=dispatch`, or `[experimental] dispatch = true` in config.toml. You can still dispatch one subagent at a time with the Agent tool.", nil
	}
	a := parseDispatchArgs(argsJSON)

	if strings.TrimSpace(a.Goal) == "" {
		return "error: dispatch requires a `goal` describing the overall objective", nil
	}
	if len(a.Tasks) < 2 {
		return "error: dispatch needs at least 2 tasks (use the Agent tool for a single subagent)", nil
	}

	if len(a.Tasks) > MaxForegroundSubagents {
		return fmt.Sprintf("error: dispatch supports at most %d concurrent subtasks (got %d); split into smaller batches", MaxForegroundSubagents, len(a.Tasks)), nil
	}

	// Session-wide token budget, same backstop the Agent tool applies at spawn
	// (see AgentTool.Execute). Dispatch children record their estimated spend
	// via MarkDone exactly like Agent children, so they DEPLETE this budget —
	// without this check they were the one fan-out path that could never be
	// stopped by it, which is the worst place to omit it: a dispatch batch is
	// N child loops at once, not one. Checked before any worktree exists so a
	// rejected batch leaves nothing behind.
	if t.Agent.MaxSessionTokens > 0 {
		if used := t.Agent.Tasks.TotalTokensUsed(); used >= t.Agent.MaxSessionTokens {
			return fmt.Sprintf("error: this session's subagent token budget (~%d estimated tokens) is exhausted (used ~%d); the budget is cumulative for the session and covers dispatch batches as well as single subagents. Raise it with `[subagents]` `session_token_budget = N` in ~/.yottacode/config.toml, or finish the remaining work without further delegation.",
				t.Agent.MaxSessionTokens, used), nil
		}
	}

	// Resolve each task's agent config + classify read vs write.
	children := make([]*dispatchChild, len(a.Tasks))
	for i, spec := range a.Tasks {
		if spec.SubagentType == "" {
			return fmt.Sprintf("error: task %d is missing subagent_type", i+1), nil
		}
		if strings.TrimSpace(spec.Prompt) == "" {
			return fmt.Sprintf("error: task %d is missing prompt", i+1), nil
		}
		cfg := subagents.Find(t.Agent.Configs, spec.SubagentType)
		if cfg == nil {
			return t.Agent.unknownSubagentError(spec.SubagentType), nil
		}
		children[i] = &dispatchChild{
			spec:    spec,
			cfg:     cfg,
			isWrite: !agentIsReadOnly(cfg),
		}
	}

	// Overlap guard: write subtasks must declare a non-overlapping file
	// scope. This is what makes the branches merge cleanly by construction.
	if msg := validateWritePartition(children); msg != "" {
		return msg, nil
	}

	hasWrite := false
	for _, c := range children {
		if c.isWrite {
			hasWrite = true
			break
		}
	}

	// Write subtasks need git worktrees. Guard the preconditions up front
	// so we fail before spawning anything.
	var repoRoot, baseSHA string
	if hasWrite {
		if t.Agent.PlanMode.IsActive() {
			return "error: dispatch with write-capable subtasks can't run in plan mode (worktree writes would be blocked). " +
				"Exit plan mode first, or dispatch only read-only subtasks.", nil
		}
		rr, err := worktree.ResolveRepoRoot(ctx, t.Agent.Cwd.Get())
		if err != nil {
			return "error: dispatch with write-capable subtasks requires a git repository (worktree isolation needs git): " + err.Error() +
				". Dispatch only read-only/research subtasks here, or run inside a git repo.", nil
		}
		repoRoot = rr
		sha, err := gitOutput(ctx, repoRoot, "rev-parse", "HEAD")
		if err != nil {
			return "error: dispatch could not resolve the base commit (is there at least one commit?): " + err.Error(), nil
		}
		baseSHA = strings.TrimSpace(sha)
	}

	batchID := subagents.NewTaskID()[:8]

	// Mode: background for write-capable batches by default —
	// they're long-running parallel implementation; foreground (waiting,
	// assemble-now) for all-read research batches. An explicit `background`
	// overrides. Falls back to foreground where the session can't host
	// detached workers (oneshot). Decided here, before any worktree is
	// created, so the background cap can fail fast and leak nothing.
	runBackground, bgNote := t.resolveBackground(a.Background, hasWrite)

	// Background cap, fast pre-check: each detached worker holds a slot until
	// it finishes, and repeated background dispatch calls would otherwise stack
	// unbounded workers (provider streams + goroutines). Rejecting here means
	// the common over-cap case fails before a single worktree is created. The
	// AUTHORITATIVE check is the atomic TryReserveBatch below — this one can go
	// stale between the count and the inserts, which is exactly the race that
	// made a bare check-then-Add wrong.
	if runBackground {
		if active := t.Agent.Tasks.ActiveCount(); active+len(children) > MaxBackgroundSubagents {
			return fmt.Sprintf("error: dispatching %d background workers would exceed the cap of %d concurrent background subagents (currently %d running); wait for some to finish or stop them with /subagents stop, then retry",
				len(children), MaxBackgroundSubagents, active), nil
		}
	}

	// Create worktrees for write subtasks. On any failure, clean up what
	// we created so a half-built batch doesn't leak worktrees.
	var created []string // worktree dirs, for cleanup on error
	cleanup := func() {
		for _, dir := range created {
			_ = worktree.Remove(ctx, repoRoot, dir, true)
		}
	}
	for i, c := range children {
		if !c.isWrite {
			continue
		}
		name := fmt.Sprintf("dispatch-%s-%d", batchID, i+1)
		if err := worktree.ValidateName(name); err != nil {
			cleanup()
			return "error: internal worktree name invalid: " + err.Error(), nil
		}
		wtDir := worktree.Dir(repoRoot, name)
		branch := worktree.Branch(name)
		if _, err := gitOutput(ctx, repoRoot, "worktree", "add", "-b", branch, wtDir, baseSHA); err != nil {
			cleanup()
			return fmt.Sprintf("error: failed to create worktree for task %d (%s): %v", i+1, c.spec.Description, err), nil
		}
		created = append(created, wtDir)
		c.branch = branch
		c.worktree = wtDir
		c.repoRoot = repoRoot
		c.base = baseSHA
	}

	// Build every worker's registry record now — branch/worktree already
	// resolved — then admit the whole batch in ONE locked step. Adding from
	// inside each spawned goroutine (what this used to do) left a window
	// between the cap count above and the inserts, the same check-then-Add race
	// Registry.TryReserve exists to close for the Agent tool. Reserving here
	// also gives a rejected batch exactly one cleanup path: the worktrees
	// created above.
	tasks := make([]*subagents.Task, len(children))
	for i, c := range children {
		tasks[i] = t.prepareDispatchChild(c, batchID, runBackground)
	}
	if runBackground {
		if !t.Agent.Tasks.TryReserveBatch(tasks, MaxBackgroundSubagents, false) {
			cleanup()
			return fmt.Sprintf("error: dispatching %d background workers would exceed the cap of %d concurrent background subagents (currently %d running); wait for some to finish or stop them with /subagents stop, then retry",
				len(children), MaxBackgroundSubagents, t.Agent.Tasks.ActiveCount()), nil
		}
	} else {
		// Foreground batches are bounded by the MaxForegroundSubagents check at
		// the top of Execute, and ParallelSafe==false keeps two dispatch calls
		// from overlapping — so there is no cap to reserve against here.
		for _, task := range tasks {
			t.Agent.Tasks.Add(task)
		}
	}

	if runBackground {
		// Detach each worker from the parent turn (context.Background) so
		// it survives the turn ending. Workers auto-approve within their
		// worktree; completion lands via the background-done callback (the
		// dock + /subagents reflect live state from the registry). Return
		// immediately with the batch handle.
		for _, c := range children {
			go t.runDispatchChild(context.Background(), c, batchID, true, nil, nil)
		}
		return t.formatBackgroundResult(a.Goal, batchID, children, bgNote), nil
	}

	// Foreground: dispatch fans children out itself (not via the loop's
	// parallel batch), so it installs the approval gate that serializes
	// their approval round-trips on the single decisions channel + modal.
	gate := &sync.Mutex{}
	gatedCtx := WithApprovalGate(ctx, gate)
	parentEvents := ParentEvents(ctx)
	parentDecisions := ParentDecisions(ctx)

	var wg sync.WaitGroup
	for _, c := range children {
		wg.Add(1)
		go func(c *dispatchChild) {
			defer wg.Done()
			t.runDispatchChild(gatedCtx, c, batchID, false, parentEvents, parentDecisions)
		}(c)
	}
	wg.Wait()

	return t.formatResult(a.Goal, batchID, children, hasWrite), nil
}

// resolveBackground decides whether the batch runs detached. nil request →
// background when the batch has write tasks (parallel implementation),
// foreground when it's all read-only (research). An explicit request wins,
// but background degrades to foreground when the session can't host
// detached workers; the returned note explains that fallback.
func (t *DispatchTool) resolveBackground(request *bool, hasWrite bool) (bool, string) {
	want := hasWrite
	if request != nil {
		want = *request
	}
	if want && !t.SupportsBackground {
		return false, " (background isn't available in this session — ran foreground/waiting instead)"
	}
	return want, ""
}

// prepareDispatchChild resolves one subtask's identity, model routing, and
// registry record, returning the record for the caller to admit. Execute calls
// this for every child BEFORE any worker goroutine starts, so the whole batch
// can be admitted in a single atomic reservation; runDispatchChild therefore
// assumes c.task is already built and registered.
//
// Must be called AFTER worktree creation — the record carries the child's
// branch/worktree/base, and the registry copy is what the session-exit sweep
// and the crash-recovery import read to reclaim worktrees.
func (t *DispatchTool) prepareDispatchChild(c *dispatchChild, batchID string, background bool) *subagents.Task {
	c.taskID = subagents.NewTaskID()
	c.transcriptPath = filepath.Join(t.Agent.TranscriptDir, fmt.Sprintf("%s-%s.md", c.cfg.Name, c.taskID))
	c.adapter, c.model = t.Agent.routeChildModel(c.cfg)
	c.task = &subagents.Task{
		ID:             c.taskID,
		AgentType:      c.cfg.Name,
		Prompt:         c.spec.Prompt,
		Started:        time.Now(),
		Status:         subagents.TaskRunning,
		Background:     background,
		TranscriptPath: c.transcriptPath,
		Model:          c.model,
		Branch:         c.branch,
		Worktree:       c.worktree,
		Base:           c.base,
		BatchID:        batchID,
		// A background batch re-prompts the model once its LAST worker finishes
		// (the TUI coalesces pending wakes per BatchID) so the fan-out →
		// integrate workflow completes on its own instead of stalling until the
		// user happens to type. Foreground batches return their results inline
		// — there is nothing to wake.
		NotifyOnDone: background,
	}
	return c.task
}

// runDispatchChild runs one subtask to completion: routes its model, builds
// its (worktree-isolated, for write tasks) registry, runs the child loop,
// and auto-commits a write task's worktree to its branch.
//
// background changes the posture: a foreground child forwards progress +
// approvals to the parent (emit/decisions), keeping the dispatch call open; a
// background child runs detached and silent — no inline cards (the dock +
// /subagents read the live registry), auto-approves within its worktree,
// and reports completion via the background-done callback so it lands after
// the parent turn ends.
func (t *DispatchTool) runDispatchChild(ctx context.Context, c *dispatchChild, batchID string, background bool, parentEvents chan<- Event, parentDecisions <-chan Decision) {
	// Identity, model routing, and registry admission all happened in Execute
	// (one atomic reservation for the batch), so this function starts from an
	// already-registered task.
	task := c.task
	transcriptPath := c.transcriptPath
	childAdapter, childModel := c.adapter, c.model

	// A panic in this child's orchestration must not crash the user's
	// session (background workers are detached; a foreground panic also
	// skips wg.Done and would hang the batch). Convert it to an errored,
	// done task. Panics inside the child's own runChild/turn are already
	// caught there; this covers this function's own commit/merge logic.
	defer func() {
		if r := recover(); r != nil {
			c.errored, c.status = true, subagents.TaskErrored
			c.result = "error: " + panicToError("dispatch subagent "+c.cfg.Name, r).Error()
			// Even a panicked worker must not leak an empty worktree. The
			// helper re-derives emptiness itself (the panic may have struck
			// before commit classification ran) and keeps anything it can't
			// affirmatively prove is empty+clean. context.Background(), not
			// WithoutCancel(ctx): the recover must never re-panic, and ctx
			// itself may be the poison that got us here (e.g. nil).
			if !c.reclaimed {
				c.reclaimed = reclaimEmptyWorktree(context.Background(), c.repoRoot, c.worktree, c.base)
			}
			// Same non-fatal, best-effort posture as reclaimEmptyWorktree
			// above: a panic mid-run must not leak this worker's container.
			if c.sandbox != nil {
				_ = c.sandbox.Close()
			}
			t.Agent.Tasks.MarkDone(c.taskID, c.status, c.result, c.errored, c.tokens)
			// A background worker's completion is surfaced ONLY via the
			// async callback (the normal-return branch below). Without
			// firing it here too, a panic in this function's own
			// orchestration leaves the inbox/dock waiting forever and
			// integrate is never prompted. Foreground workers don't need
			// it — their result is read from c after the batch's wg.Wait.
			if background {
				tokensUsed, toolCalls := doneTokensAndCalls(t.Agent.Tasks, c.taskID, c.tokens)
				t.Agent.fireBackgroundDone(SubagentBackgroundDone{
					TaskID:       c.taskID,
					AgentType:    c.cfg.Name,
					Result:       c.result,
					Errored:      true,
					Duration:     time.Since(task.Started),
					TokensUsed:   tokensUsed,
					ToolCalls:    toolCalls,
					Model:        childModel,
					Branch:       c.branch,
					BatchID:      batchID,
					Committed:    c.commit != "",
					CommitSHA:    c.commit,
					CommitErr:    c.commitErr,
					Reclaimed:    c.reclaimed,
					NotifyOnDone: true,
				})
			}
		}
	}()

	// Foreground forwards events/approvals to the parent; background runs
	// silent (nil emit + nil decisions): no inline card spam, and the
	// child auto-approves within its worktree so it never needs to prompt.
	var emitToParent func(Event)
	var decisions <-chan Decision
	if !background {
		// Critical events the child blocks on (ApprovalNeeded /
		// PathTrustElevationNeeded) are delivered with a blocking send;
		// progress stays lossy. See forwardToParent.
		emitToParent = func(ev Event) { forwardToParent(ctx, parentEvents, ev) }
		decisions = parentDecisions
		emitToParent(SubagentStart{
			TaskID:         c.taskID,
			AgentType:      c.cfg.Name,
			Prompt:         truncate(c.spec.Prompt, 200),
			TranscriptPath: transcriptPath,
			Branch:         c.branch,
			BatchID:        batchID,
		})
	}

	transcript := openTranscript(transcriptPath, c.cfg, agentArgs{
		SubagentType:    c.cfg.Name,
		Description:     c.spec.Description,
		Prompt:          c.spec.Prompt,
		RunInBackground: background,
	})

	childCtx, cancel := context.WithCancel(ctx)
	t.Agent.Tasks.AttachCancel(c.taskID, cancel)
	defer cancel()

	opts := childRunOpts{bgPolicy: background}
	if c.isWrite {
		// A write worker's worktree is a different absolute path than
		// whatever the parent session's own container has mounted, so it
		// needs its own Sandbox — inherits the parent's sandboxing posture
		// by default (see DispatchTool.SandboxFactory), no separate
		// dispatch-level opt-in. Construction failure fails this worker
		// loud rather than silently falling back to unsandboxed host
		// execution, the same "never fall back on error" contract
		// NewPodmanSandbox's caller follows at session startup.
		if t.SandboxFactory != nil && dispatchChildNeedsSandbox(c.cfg) {
			sb, err := t.SandboxFactory(childCtx, c.worktree, c.taskID)
			if err != nil {
				c.errored, c.status = true, subagents.TaskErrored
				c.result = "error: sandbox: " + err.Error()
				c.reclaimed = reclaimEmptyWorktree(context.Background(), c.repoRoot, c.worktree, c.base)
				t.Agent.Tasks.MarkDone(c.taskID, c.status, c.result, c.errored, c.tokens)
				if background {
					tokensUsed, toolCalls := doneTokensAndCalls(t.Agent.Tasks, c.taskID, c.tokens)
					t.Agent.fireBackgroundDone(SubagentBackgroundDone{
						TaskID:       c.taskID,
						AgentType:    c.cfg.Name,
						Result:       c.result,
						Errored:      true,
						Duration:     time.Since(task.Started),
						TokensUsed:   tokensUsed,
						ToolCalls:    toolCalls,
						Model:        childModel,
						Branch:       c.branch,
						BatchID:      batchID,
						Reclaimed:    c.reclaimed,
						NotifyOnDone: true,
					})
				}
				return
			}
			c.sandbox = sb
		}
		childCwd := NewCwdRef(c.worktree)
		opts.reg = t.buildWorktreeChildRegistry(c.cfg, childCwd, c.worktree, c.spec.Files, background, c.sandbox)
		opts.cwd = childCwd
		opts.extraSystemPrompt = writeScopePrompt(c.branch, c.spec.Files)
		// Threads into dispatchBackgroundApprovalPolicy: a background write
		// worker's run_bash/run_tests calls are allowed exactly when this
		// worker's own container bounds their blast radius, denied on the
		// host fallback. Only meaningful for write children — read-only
		// dispatch children share the parent's own registry/Sandbox
		// directly (see buildChildRegistry) and never set this.
		opts.sandboxed = c.sandbox != nil
	}

	result, errored, status, tokens := t.Agent.runChild(
		childCtx, c.taskID, c.cfg, c.spec.Prompt, transcript,
		emitToParent, decisions, childAdapter, childModel, opts,
	)

	// Commit + classify the worker's output. Runs BEFORE MarkDone so anyone
	// observing the task as finished (a parent blocked in get_subagent_result,
	// the integrate step, the dock) already sees an accurate commit state —
	// no race where "done" precedes the commit. Uses context.WithoutCancel so
	// a just-finished foreground run whose parent turn is being canceled still
	// commits its work.
	//
	// Three things this gets right that the naive "dirty → commit, else clean"
	// path did not (the P1 silent-failure cluster):
	//   1. A SUCCESSFUL worker's dirty tree is auto-committed; if that commit
	//      is rejected (pre-commit hook, lint, validation) or staging fails,
	//      the *reason* is captured in c.commitErr instead of the branch being
	//      reported as a clean "no changes".
	//   2. An errored/iter-capped worker's tree is NOT auto-committed (its
	//      partial work may be broken — folding it into the integrate set
	//      silently would be worse than surfacing it); its worktree path is
	//      surfaced so the user can recover it.
	//   3. Commit PRESENCE is derived from the branch itself (rev-list
	//      base..HEAD), not end-of-run dirtiness — so a worker that committed
	//      its own work and left a clean tree is still recognized.
	if c.isWrite {
		// Use commitCtx (cancellation-detached) for the dirtiness checks too,
		// not just the add/commit — otherwise a just-finished foreground worker
		// whose parent turn is being canceled would see gitWorktreeDirty error
		// out (canceled ctx) → false → skip its own commit, defeating the
		// commit-on-cancel intent this block exists for.
		commitCtx := context.WithoutCancel(ctx)
		// Detaching from cancellation is what saves the work — but it also means
		// session teardown can't stop this, only outrun it. Flag the window so
		// the shutdown drain waits for a commit that's genuinely in flight
		// rather than abandoning it on a flat deadline and leaving a stale
		// index.lock behind.
		//
		// The defer clears it at FUNCTION exit, deliberately: that covers the
		// worktree reclaim below (also worth waiting for) and the panic path.
		// MarkDone runs first either way, and CommittingCount only counts
		// TaskRunning tasks, so the flag stops mattering the moment the task
		// goes terminal — the defer is the belt-and-braces.
		t.Agent.Tasks.SetCommitting(c.taskID, true)
		defer t.Agent.Tasks.SetCommitting(c.taskID, false)
		if !errored && gitWorktreeDirty(commitCtx, c.worktree) {
			if outside := outOfScopeWorkerChanges(commitCtx, c.worktree, c.spec.Files); len(outside) > 0 {
				c.commitErr = "out-of-scope changes left uncommitted: " + strings.Join(outside, ", ")
				errored = true
				status = subagents.TaskErrored
				if strings.TrimSpace(result) != "" {
					result += "\n\n"
				}
				result += c.commitErr
			} else if _, err := gitOutput(commitCtx, c.worktree, "add", "-A"); err != nil {
				c.commitErr = "staging changes failed: " + err.Error()
			} else {
				msg := commitSubject(c.cfg.Name, c.spec.Description)
				res, cErr := ApplyCommit(commitCtx, c.worktree, msg)
				switch {
				case cErr != nil:
					c.commitErr = "commit failed: " + cErr.Error()
				case res.HookError != "":
					c.commitErr = "pre-commit hook rejected the commit — " + firstLine(res.HookError)
				case res.ValidationErr != "":
					c.commitErr = "commit validation failed — " + res.ValidationErr
				}
			}
		}
		// Authoritative presence check, however the commit got there
		// (auto-committed above, or the worker committed its own work).
		if c.branch != "" {
			if sha := branchTip(commitCtx, c.worktree, c.base); sha != "" {
				c.commit = sha
				c.commitErr = "" // the branch has commits after all
			} else if gitWorktreeDirty(commitCtx, c.worktree) {
				if c.commitErr == "" {
					c.commitErr = "ended without committing; uncommitted work left in " + c.worktree
				}
			}
		}
		// Reclaim an empty worktree — EVERY outcome (completed, errored,
		// canceled, iter-capped) and both postures (foreground and
		// background): a worker that produced no commits and left a clean
		// tree has nothing worth keeping, so its worktree+branch go now
		// instead of accumulating. Committed worktrees stay for integrate;
		// dirty ones stay for recovery (the helper re-verifies both).
		// Positioned BEFORE MarkDone so session teardown's bounded drain
		// (which waits on ActiveCount) covers the cleanup as well.
		if c.commit == "" {
			c.reclaimed = reclaimEmptyWorktree(commitCtx, c.repoRoot, c.worktree, c.base)
		}
		// Tear down this worker's own container, if it had one — every
		// outcome, same as the reclaim above. The panic-recovery defer
		// covers the other exit path; the two are mutually exclusive (this
		// line only runs on normal return), so Close is called exactly once.
		if c.sandbox != nil {
			_ = c.sandbox.Close()
		}
	}

	t.Agent.Tasks.MarkDone(c.taskID, status, result, errored, tokens)

	c.status = status
	c.result = result
	c.errored = errored
	c.tokens = tokens

	tokensUsed, toolCalls := doneTokensAndCalls(t.Agent.Tasks, c.taskID, tokens)
	if background {
		// Async completion: route through the session-level callback (the
		// long-lived inbox), the same path AgentTool background runs use,
		// so it surfaces after the parent turn has ended. The empty-worktree
		// reclaim already happened above (before MarkDone), uniformly with
		// the foreground path.
		t.Agent.fireBackgroundDone(SubagentBackgroundDone{
			TaskID:     c.taskID,
			AgentType:  c.cfg.Name,
			Result:     result,
			Errored:    errored,
			Duration:   time.Since(task.Started),
			TokensUsed: tokensUsed,
			ToolCalls:  toolCalls,
			Model:      childModel,
			Branch:     c.branch,
			BatchID:    batchID,
			Committed:  c.commit != "",
			CommitSHA:  c.commit,
			CommitErr:  c.commitErr,
			Reclaimed:  c.reclaimed,
			// Wake the model with this batch's results. The TUI holds the wake
			// until every worker sharing this BatchID has finished, so an
			// 8-worker batch produces one wake turn, not eight.
			NotifyOnDone: true,
		})
		return
	}
	emitToParent(SubagentDone{
		TaskID:     c.taskID,
		AgentType:  c.cfg.Name,
		Result:     result,
		Errored:    errored,
		Duration:   time.Since(task.Started),
		TokensUsed: tokensUsed,
		ToolCalls:  toolCalls,
		Model:      childModel,
		Branch:     c.branch,
		BatchID:    batchID,
	})
}

// dispatchChildNeedsSandbox reports whether cfg's granted toolset can reach
// Sandbox-routed execution: run_bash and run_tests execute inside it when
// sandboxed, and create_document's docx/pdf/pptx paths route through it too.
// A worker granted none of these has nothing for a Sandbox to cover and
// shouldn't pay container-creation cost or fail its task over a dependency
// it was never going to use.
func dispatchChildNeedsSandbox(cfg *subagents.AgentConfig) bool {
	return cfg.ToolAllowed("run_bash") || cfg.ToolAllowed("run_tests") || cfg.ToolAllowed("create_document")
}

// buildWorktreeChildRegistry builds the core cwd-bound toolset pinned to the
// child's worktree, then narrows it to the agent config's allowlist. The
// core set already excludes the delegation tools (Agent/dispatch/integrate)
// and exit_plan_mode, so a worker can't recurse.
//
// sandbox is this worker's own Sandbox (nil means host execution, same as
// the parent session's own nil-Sandbox default) — see DispatchTool.SandboxFactory.
func (t *DispatchTool) buildWorktreeChildRegistry(cfg *subagents.AgentConfig, cwd *CwdRef, wtDir string, ownedFiles []string, background bool, sandbox Sandbox) *Registry {
	core := NewRegistry()
	enableLSP := t.EnableLSP && !background
	RegisterCoreCwdTools(core, cwd, CoreToolDeps{
		WriteOpts:      WritePathOptions{Cwd: cwd, DenyExact: DefaultDenyPaths(wtDir), OwnedPaths: append([]string(nil), ownedFiles...)},
		DenyReads:      DefaultDenyReadPaths(wtDir),
		SupportsImages: t.SupportsImages,
		EnableLSP:      enableLSP,
		LSPServers:     t.LSPServers,
		LSPDisabled:    t.LSPDisabled,
		// Background workers are unattended, so they must not spawn language-server
		// binaries. Foreground workers may use LSP tools, but still do not share the
		// parent manager because eviction is process-level and not lease-aware.
		LSPManager:             nil,
		EnableSyntaxRanges:     t.EnableSyntaxRanges,
		AllowPDFIngestion:      t.AllowPDFIngestion,
		AllowDocxPdfGeneration: t.AllowDocxPdfGeneration,
		Sandbox:                sandbox,
	})
	out := NewRegistry()
	for _, tool := range core.Tools() {
		if cfg.ToolAllowed(tool.Name()) {
			out.Register(tool)
		}
	}
	return out
}

// validateWritePartition enforces that write subtasks declare a file scope
// and that no file is claimed by two write subtasks. Returns "" when valid,
// or a recoverable error message naming the collisions / missing scopes.
func validateWritePartition(children []*dispatchChild) string {
	var claims []dispatchFileClaim
	var missing []string
	var collisions []string
	for i, c := range children {
		if !c.isWrite {
			continue
		}
		if len(c.spec.Files) == 0 {
			missing = append(missing, fmt.Sprintf("task %d (%s)", i+1, c.spec.Description))
			continue
		}
		for _, f := range c.spec.Files {
			key := filepath.Clean(strings.TrimSpace(f))
			if key == "" || key == "." || key == ".." || filepath.IsAbs(key) || strings.HasPrefix(key, ".."+string(filepath.Separator)) {
				collisions = append(collisions, fmt.Sprintf("task %d has invalid broad ownership claim %q", i+1, f))
				continue
			}
			if prev, ok := overlappingDispatchClaim(claims, key); ok {
				collisions = append(collisions, fmt.Sprintf("%q overlaps %q claimed by task %d and task %d", key, prev.path, prev.task, i+1))
				continue
			}
			claims = append(claims, dispatchFileClaim{path: key, task: i + 1})
		}
	}
	if len(missing) > 0 {
		return "error: write-capable subtasks must declare their `files` (the files they will create/edit) so the tool can guarantee non-overlapping ownership. Missing files for: " +
			strings.Join(missing, ", ") + ". Add a files list to each, or use a read-only subagent_type for research-only tasks."
	}
	if len(collisions) > 0 {
		return "error: write subtasks must own non-overlapping files (overlap causes merge conflicts). Conflicts: " +
			strings.Join(collisions, "; ") + ". Re-partition so each file is owned by exactly one task."
	}
	return ""
}

type dispatchFileClaim struct {
	path string
	task int
}

func overlappingDispatchClaim(claims []dispatchFileClaim, path string) (dispatchFileClaim, bool) {
	for _, c := range claims {
		if pathsOverlap(c.path, path) {
			return c, true
		}
	}
	return dispatchFileClaim{}, false
}

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

// writeScopePrompt is the system-prompt addendum that tells a write subtask
// which files it owns and that it must stay within them.
func writeScopePrompt(branch string, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are working in an ISOLATED git worktree on branch %q as part of a larger effort split across several agents.\n", branch)
	if len(files) > 0 {
		fmt.Fprintf(&b, "You OWN ONLY these files (create/edit ONLY these): %s.\n", strings.Join(files, ", "))
	}
	b.WriteString("You may READ any file in the repo for context, but DO NOT create or edit any file outside your owned set — another agent owns the rest, and editing shared files will cause merge conflicts when the branches are integrated. ")
	b.WriteString("Your changes are committed automatically when you finish; you don't need to commit. Produce a short final summary of what you changed.")
	return b.String()
}

// commitSubject builds a one-line, <=72-char commit subject ApplyCommit will
// accept (no trailing period, single line).
func commitSubject(agentType, description string) string {
	desc := strings.TrimSpace(description)
	if desc == "" {
		desc = "dispatched changes"
	}
	subj := fmt.Sprintf("%s: %s", agentType, desc)
	subj = strings.ReplaceAll(subj, "\n", " ")
	if len(subj) > 72 {
		subj = subj[:72]
	}
	subj = strings.TrimRight(subj, ". ")
	return subj
}

// formatBackgroundResult is the immediate return for a background dispatch:
// the workers are still running, so it reports the batch handle, each
// worker's branch, and how to follow + integrate. Status/commit aren't
// known yet (the children are detached).
func (t *DispatchTool) formatBackgroundResult(goal, batchID string, children []*dispatchChild, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dispatched %d subtasks in the BACKGROUND for: %s%s\n", len(children), strings.TrimSpace(goal), note)
	fmt.Fprintf(&b, "Batch %s — workers are running in parallel; this call returned immediately.\n\n", batchID)

	var branches []string
	for i, c := range children {
		kind := "read"
		if c.isWrite {
			kind = "write"
		}
		fmt.Fprintf(&b, "  task %d · %s [%s] · %s", i+1, firstNonEmpty(c.spec.Description, c.cfg.Name), c.cfg.Name, kind)
		if c.branch != "" {
			fmt.Fprintf(&b, " · branch %s", c.branch)
			branches = append(branches, c.branch)
		}
		fmt.Fprintln(&b)
	}
	b.WriteString("\nFollow progress in the live dock (pinned above the status bar) or with /subagents. ")
	if len(branches) > 0 {
		b.WriteString("Each write worker auto-approves within its own isolated worktree and is committed to its branch when it finishes.\n")
	} else {
		b.WriteString("Read-only workers run without worktrees or integration branches; collect their results from /subagents when they finish.\n")
	}
	if len(branches) > 0 {
		// Don't over-promise: at dispatch time we don't yet know which
		// workers will actually produce commits (a hook rejection, an empty
		// change, or an iter-cap can leave a branch with nothing). integrate
		// now fails fast on missing branches, so tell the model to pass only the
		// branches the dock reports as committed.
		fmt.Fprintf(&b, "When the workers finish (watch the dock for each one's commit status), call integrate with the committed branches from this list [%s]. Do not include workers reported as empty/reclaimed or NOT committed.\n", strings.Join(branches, ", "))
	}
	return b.String()
}

func (t *DispatchTool) formatResult(goal, batchID string, children []*dispatchChild, hasWrite bool) string {
	var b strings.Builder
	done, failed := 0, 0
	for _, c := range children {
		if c.errored {
			failed++
		} else {
			done++
		}
	}
	fmt.Fprintf(&b, "Dispatched %d subtasks for: %s\n", len(children), strings.TrimSpace(goal))
	fmt.Fprintf(&b, "Batch %s — %d completed, %d failed.\n\n", batchID, done, failed)

	for i, c := range children {
		kind := "read"
		if c.isWrite {
			kind = "write"
		}
		fmt.Fprintf(&b, "── task %d · %s [%s] · %s ──\n", i+1, firstNonEmpty(c.spec.Description, c.cfg.Name), c.cfg.Name, kind)
		fmt.Fprintf(&b, "status: %s", c.status)
		if c.branch != "" {
			fmt.Fprintf(&b, " · branch: %s", c.branch)
			switch {
			case c.commit != "":
				fmt.Fprintf(&b, " · committed %s", shortSHA(c.commit))
			case c.commitErr != "":
				fmt.Fprintf(&b, " · NOT committed (%s)", c.commitErr)
			case c.reclaimed:
				b.WriteString(" · (no changes — empty worktree and branch reclaimed)")
			default:
				b.WriteString(" · (no changes to commit)")
			}
		}
		fmt.Fprintln(&b)
		if reply := strings.TrimSpace(c.result); reply != "" {
			b.WriteString(truncate(reply, maxDispatchReplyChars))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if hasWrite {
		var branches []string
		for _, c := range children {
			if c.branch != "" && c.commit != "" {
				branches = append(branches, c.branch)
			}
		}
		if len(branches) > 0 {
			fmt.Fprintf(&b, "Next: call integrate with branches [%s] to merge them into one branch for your PR. ", strings.Join(branches, ", "))
			b.WriteString("Resolve any conflicts it reports, then open a PR from the integration branch.\n")
		} else {
			b.WriteString("No write subtasks produced commits, so there is nothing to integrate.\n")
		}
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// outOfScopeWorkerChanges returns changed paths that are not covered by the
// dispatch worker's declared ownership. The write tools enforce this at call
// time, but this commit-time guard catches indirect mutations (for example a
// test command or a tool bug writing generated files) before dispatch commits
// another worker's files.
func outOfScopeWorkerChanges(ctx context.Context, worktreeDir string, owned []string) []string {
	out, err := gitOutput(ctx, worktreeDir, "status", "--porcelain")
	if err != nil {
		return []string{"<could not inspect worktree status: " + firstLine(err.Error()) + ">"}
	}
	var outside []string
	for _, line := range splitNonEmptyLines(out) {
		path := statusPath(line)
		if path == "" {
			continue
		}
		if !pathOwned(path, owned, worktreeDir) {
			outside = append(outside, path)
		}
	}
	return outside
}

func statusPath(line string) string {
	if len(line) < 4 {
		return ""
	}
	p := strings.TrimSpace(line[3:])
	if before, after, ok := strings.Cut(p, " -> "); ok {
		_ = before
		p = after
	}
	return strings.Trim(p, `"`)
}

func pathOwned(path string, owned []string, worktreeDir string) bool {
	for _, raw := range owned {
		original := strings.TrimSpace(raw)
		raw = filepath.Clean(original)
		if raw == "" || raw == "." {
			continue
		}
		if path == raw {
			return true
		}
		candidate := filepath.Join(worktreeDir, raw)
		if strings.HasSuffix(original, "/") || strings.HasSuffix(original, string(filepath.Separator)) || isDir(candidate) {
			if pathUnder(filepath.Join(worktreeDir, path), candidate) {
				return true
			}
		}
	}
	return false
}

// branchTip returns the worktree branch's HEAD sha when it has at least one
// commit beyond base, or "" when the branch added nothing (or the query
// fails). This is the authoritative "did this worker produce committable
// work?" signal — independent of end-of-run worktree dirtiness, so it
// recognizes a worker that committed its own changes and left a clean tree.
func branchTip(ctx context.Context, worktreeDir, base string) string {
	if base == "" {
		return ""
	}
	out, err := gitOutput(ctx, worktreeDir, "rev-list", "-1", base+"..HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// firstLine returns the first non-empty line of s, trimmed — used to fold a
// multi-line hook/validation error into a one-line reason for the dispatch
// result without dumping the whole stderr into the parent's context.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if before, _, found := strings.Cut(s, "\n"); found {
		return strings.TrimSpace(before)
	}
	return s
}
