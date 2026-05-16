package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// AgentToolName is the schema-visible name of the subagent dispatch
// tool. Capital-A mirrors Claude Code's surface ("Agent"/"Task"). The
// name is referenced in several places (recursion guard, plan-mode
// gate exemption checks, TUI tool-card suppression) so it lives as a
// const here. Exported so consumers in internal/tui can compare
// against it without hardcoding the string.
const AgentToolName = "Agent"

// agentToolName is the lowercase alias used in pre-export call sites.
// Kept as a separate identifier so the existing references inside
// this package don't all need to be updated at once.
const agentToolName = AgentToolName

// MaxBackgroundSubagents caps how many background subagents may be
// running concurrently per session. Hit the cap → the tool rejects
// the call with a recoverable error message the model can adapt
// around. Foreground subagents are unbounded (they serialize on the
// parent's call stack anyway). 8 is a round number that matches what
// most users informally do — enough for genuine parallelism, low
// enough to keep API spend bounded if a model gets enthusiastic.
const MaxBackgroundSubagents = 8

// childChildIterationCap is the iteration budget every child subagent
// runs under. We deliberately do NOT apply the auto-mode 4× multiplier
// or the yolo uncapped path: subagent runs should bound themselves,
// even when the parent session is in auto/yolo. The user opted into
// "let the parent run unattended" — they did not opt into "let the
// parent spawn unbounded child loops." A child that needs more than
// this many iterations is almost certainly stuck.
const childIterationCap = 40

// childActivityTranscriptHeader is the literal header prefixing every
// subagent transcript file. The visible separator makes it obvious
// where the prompt ends and the run begins when the user `cat`s the
// file. Trailing newline so the first event line starts cleanly.
const childActivityTranscriptHeader = "# Subagent transcript\n\n"

// AgentTool dispatches typed-subagent invocations. One instance is
// registered per session — Execute spawns a child agent.Turn against a
// filtered registry and either blocks until the child completes
// (foreground) or detaches the child to a goroutine that updates the
// task registry on completion (background).
type AgentTool struct {
	// Configs is the resolved set of agent definitions (builtin +
	// global + project). The Execute method looks up subagent_type
	// against this slice; it should remain stable across the session.
	Configs []subagents.AgentConfig

	// Tasks is the session-scoped task registry. Foreground runs add
	// + MarkDone within a single Execute; background runs add now and
	// MarkDone later from a detached goroutine.
	Tasks *subagents.Registry

	// Adapter is the streamer the child Turn calls into. Shared with
	// the parent — adapter calls are stateless per-request and
	// concurrency-safe by construction.
	Adapter Streamer

	// ParentRegistry is the live tool set the parent session is using.
	// We clone it for the child, dropping the Agent tool itself plus
	// exit_plan_mode, and intersecting with the agent's `tools:`
	// allowlist when one is configured. The clone is a single-pass
	// snapshot — runtime changes to the parent registry don't
	// propagate into in-flight children, which keeps semantics easy
	// to reason about.
	ParentRegistry *Registry

	// Permissions is the parent's permission ruleset; children
	// inherit it unchanged. Per-config narrowing is a v2 extension.
	Permissions *permissions.Permissions

	// YoloMode is the process-wide yolo overlay. The pointer is
	// shared so a yolo session also applies to its subagents — the
	// user explicitly opted into unattended mutation, child runs
	// included.
	YoloMode *YoloModeState

	// PlanMode is the parent's plan-mode state. Pointer-shared so a
	// child run under a plan-mode parent inherits the restriction
	// transitively (no writes outside the plan file). When the
	// parent flips out of plan mode mid-conversation the next
	// subagent run automatically sees the new state. nil is safe —
	// runChild allocates a fresh inactive state in that case.
	PlanMode *PlanModeState

	// AutoMode is the parent's auto-mode state. Pointer-shared so a
	// child inherits parent's auto-mode (mutating tools auto-allow
	// except the safety floor, iteration cap multiplied 4×). nil is
	// safe — runChild allocates a fresh inactive state in that case.
	AutoMode *AutoModeState

	// Cwd is the working directory child tools resolve relative
	// paths against. Shared with the parent so an in-session cwd
	// swap (enter_worktree) flows to the spawned subagent.
	Cwd *CwdRef

	// TranscriptDir is the directory background-task transcripts get
	// persisted under. Must exist at Execute time; the caller (TUI
	// or oneshot wiring) creates it via subagents.EnsureTranscriptDir
	// at startup.
	TranscriptDir string

	// AllowBackground controls whether `run_in_background: true` is
	// honored. The TUI sets this true; oneshot leaves it false so the
	// non-interactive entry point returns a sensible error string the
	// model can recover from rather than silently detaching work that
	// nobody will see.
	AllowBackground bool

	// SystemPromptSuffix is appended to the agent definition's body
	// when building the child's system prompt. Used to inject runtime
	// context the static config can't know (currently empty; reserved
	// for cwd / repo metadata if we decide to inject it).
	SystemPromptSuffix string

	// onBackgroundDone is the optional callback fired when a background
	// subagent finishes. The TUI sets this to a function that posts a
	// SubagentBackgroundDone event onto the model's long-lived inbox;
	// oneshot leaves it nil because background subagents are rejected
	// before they can start there.
	onBackgroundDone func(SubagentBackgroundDone)

	// mu protects onBackgroundDone since it can be set after
	// construction by the TUI wiring path.
	mu sync.RWMutex
}

// SetBackgroundDoneCallback installs the session-level handler that
// receives a SubagentBackgroundDone event when a detached child
// finishes. Safe to call after registration; safe to call with nil
// to clear.
func (t *AgentTool) SetBackgroundDoneCallback(fn func(SubagentBackgroundDone)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onBackgroundDone = fn
}

func (t *AgentTool) Name() string { return agentToolName }

func (t *AgentTool) Description() string {
	var b strings.Builder
	b.WriteString("Dispatch a typed subagent that runs in its own context window and returns a single final answer. ")
	b.WriteString("Use this to delegate research, code search, planning, or any investigation that would otherwise consume many tool calls in the parent context. ")
	b.WriteString("The parent only sees the subagent's final reply — its intermediate tool calls and reasoning stay isolated.\n\n")
	b.WriteString("Available subagent_type values:\n")
	// Sort by name so the model sees a stable ordering.
	configs := append([]subagents.AgentConfig(nil), t.Configs...)
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	for _, c := range configs {
		fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Description)
	}
	b.WriteString("\nSet run_in_background:true to fire-and-forget a long-running investigation; the call returns a task id immediately and completion lands in /subagents.")
	return b.String()
}

func (t *AgentTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subagent_type": map[string]any{
				"type":        "string",
				"description": "Which agent definition to dispatch (see tool description for the list).",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "A 3-5 word label shown to the user while the subagent runs.",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the subagent. Be specific and self-contained — the subagent has no access to the parent's conversation.",
			},
			"run_in_background": map[string]any{
				"type":        "boolean",
				"description": "If true, return immediately with a task id; the subagent runs to completion in the background and the result is available via /subagents. Default: false.",
			},
		},
		"required": []string{"subagent_type", "prompt"},
	}
}

// RequiresApproval is always false for the Agent tool itself —
// delegation is just compute. The child's own mutating tool calls
// still go through their normal approval flow (and v1 auto-denies
// any ApprovalNeeded inside the child since the child has no UI
// attached). The user retains control via the parent's permission
// rules.
func (t *AgentTool) RequiresApproval(string) bool { return false }

// ParallelSafe returns false so two Agent calls from the same
// assistant message serialize. Two simultaneous subagent spawns can
// rate-limit the same provider key and burn the iteration budget on
// both — better to do them in sequence. (Background subagents are
// the parallelism story; foreground spawns are a one-at-a-time
// affair.)
func (t *AgentTool) ParallelSafe(string) bool { return false }

func (t *AgentTool) PreviewCall(argsJSON string) string {
	a := parseAgentArgs(argsJSON)
	label := a.Description
	if label == "" {
		label = truncate(a.Prompt, 60)
	}
	if a.RunInBackground {
		return fmt.Sprintf("Agent[%s, background]: %s", a.SubagentType, label)
	}
	return fmt.Sprintf("Agent[%s]: %s", a.SubagentType, label)
}

// agentArgs is the parsed Execute payload. Fields match the schema.
type agentArgs struct {
	SubagentType    string `json:"subagent_type"`
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	RunInBackground bool   `json:"run_in_background"`
}

func parseAgentArgs(argsJSON string) agentArgs {
	var a agentArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a
}

// Configs returns the resolved set of agent definitions (for the
// TUI's /subagents help / status rendering and for tests). The
// returned slice references the same memory; callers must not mutate.
func (t *AgentTool) AgentConfigs() []subagents.AgentConfig { return t.Configs }

// Execute is the parent-loop entry point. Parses args, validates the
// subagent_type, builds the child config + history, and either:
//   - foreground: spawns the child Turn, drains its events through the
//     translator inline, and returns the captured final reply as the
//     tool result string the model sees;
//   - background: launches the whole flow in a goroutine, returns a
//     task-id handle immediately, and posts completion via the
//     session-level callback when the child finishes.
func (t *AgentTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	a := parseAgentArgs(argsJSON)
	if a.SubagentType == "" {
		return "", fmt.Errorf("Agent: subagent_type is required")
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", fmt.Errorf("Agent: prompt is required")
	}
	cfg := subagents.Find(t.Configs, a.SubagentType)
	if cfg == nil {
		return t.unknownSubagentError(a.SubagentType), nil
	}
	if a.RunInBackground && !t.AllowBackground {
		// The model relays this message to the user verbatim, so
		// it has to be both informative and actionable: name the
		// gate, name the env var / config key / flag, and clarify
		// that foreground delegation still works fine.
		return "error: background subagents are an experimental feature and are not enabled in this session. " +
			"Re-run without run_in_background:true to dispatch a foreground subagent (which always works), " +
			"OR enable the feature with `--experimental background_subagents` at startup, " +
			"`YOTTACODE_EXPERIMENTAL=background_subagents` in the environment, " +
			"or `[experimental]\\nbackground_subagents = true` in ~/.yottacode/config.toml. " +
			"See docs/experimental.md for details.", nil
	}
	if a.RunInBackground && t.Tasks.ActiveCount() >= MaxBackgroundSubagents {
		return fmt.Sprintf("error: at most %d background subagents may run concurrently (current: %d); wait for one to finish or stop it with /subagents stop <id>",
			MaxBackgroundSubagents, t.Tasks.ActiveCount()), nil
	}

	taskID := subagents.NewTaskID()
	transcriptPath := filepath.Join(t.TranscriptDir, fmt.Sprintf("%s-%s.md", cfg.Name, taskID))

	task := &subagents.Task{
		ID:             taskID,
		AgentType:      cfg.Name,
		Prompt:         a.Prompt,
		Started:        time.Now(),
		Status:         subagents.TaskRunning,
		Background:     a.RunInBackground,
		TranscriptPath: transcriptPath,
	}
	t.Tasks.Add(task)

	parentEvents := ParentEvents(ctx)
	parentDecisions := ParentDecisions(ctx)
	emitToParent := func(ev Event) {
		if parentEvents == nil {
			return
		}
		select {
		case parentEvents <- ev:
		default:
			// Non-blocking: the events channel is buffered at 64; if
			// we'd block, the consumer is far behind and we'd rather
			// drop a Subagent* event than stall the loop. The
			// transcript still captures the activity.
		}
	}

	emitToParent(SubagentStart{
		TaskID:         taskID,
		AgentType:      cfg.Name,
		Prompt:         truncate(a.Prompt, 200),
		Background:     a.RunInBackground,
		TranscriptPath: transcriptPath,
	})

	transcript := openTranscript(transcriptPath, cfg, a)

	if a.RunInBackground {
		// Background: detach from the parent's ctx so the child
		// survives parent-turn end. Cancellation goes through the
		// task registry's Cancel() instead.
		bgCtx, cancel := context.WithCancel(context.Background())
		t.Tasks.AttachCancel(taskID, cancel)
		go func() {
			defer cancel()
			// Background: no live UI to forward to (emitToParent nil)
			// AND no decisions channel to read from. Approval-needed
			// events auto-deny inside runChild.
			result, errored, status, tokens := t.runChild(bgCtx, taskID, cfg, a.Prompt, transcript, nil, nil)
			t.Tasks.MarkDone(taskID, status, result, errored, tokens)
			// Read the just-recorded tool-call count off the registry
			// so the inline card can render accurate stats.
			toolCalls := 0
			if snap, ok := t.Tasks.Get(taskID); ok {
				toolCalls = snap.ToolCalls
			}
			t.fireBackgroundDone(SubagentBackgroundDone{
				TaskID:     taskID,
				AgentType:  cfg.Name,
				Result:     result,
				Errored:    errored,
				Duration:   time.Since(task.Started),
				TokensUsed: tokens,
				ToolCalls:  toolCalls,
			})
		}()
		// Message the model relays back to the user. References the
		// /subagents picker (not the removed `view` subcommand) so
		// the model's reply doesn't suggest a command that no longer
		// exists. The user opens /subagents, navigates to the row
		// matching this id, presses Enter to read the transcript.
		return fmt.Sprintf("background subagent %q started as task %s — open /subagents and press Enter on this task's row to view its transcript", cfg.Name, taskID[:8]), nil
	}

	// Foreground: child Turn is bound to the parent's ctx so cancel
	// propagates. Run inline so the parent's tool-result string is
	// the child's final reply.
	childCtx, cancel := context.WithCancel(ctx)
	t.Tasks.AttachCancel(taskID, cancel)
	defer cancel()
	// Foreground: pass parent's events + decisions so child
	// ApprovalNeeded events surface on the parent's modal and the
	// user's verdict routes back to the child's loop.
	result, errored, status, tokens := t.runChild(childCtx, taskID, cfg, a.Prompt, transcript, emitToParent, parentDecisions)
	t.Tasks.MarkDone(taskID, status, result, errored, tokens)
	// Read the just-recorded tool-call count off the registry so the
	// done card can render accurate stats.
	toolCalls := 0
	if snap, ok := t.Tasks.Get(taskID); ok {
		toolCalls = snap.ToolCalls
	}
	emitToParent(SubagentDone{
		TaskID:     taskID,
		AgentType:  cfg.Name,
		Result:     result,
		Errored:    errored,
		Duration:   time.Since(task.Started),
		TokensUsed: tokens,
		ToolCalls:  toolCalls,
	})
	if errored {
		return result, nil // returned as a tool result so the model sees the failure
	}
	return result, nil
}

// runChild assembles the child LoopConfig + history, runs agent.Turn,
// translates its events through the in-process translator, and
// captures the final assistant content for return to the parent.
// emitToParent may be nil for background runs (no live UI to update);
// the transcript captures everything either way.
//
// Approval policy is foreground/background dependent:
//   - Foreground (emitToParent != nil AND parentDecisions != nil): a
//     child's ApprovalNeeded forwards through to the parent's modal
//     so the user can answer it. The verdict routes back to the
//     child's own decisions channel. The Preview is prefixed with
//     "[subagent:<type>]" so the user knows which agent wants what.
//   - Background (emitToParent == nil OR parentDecisions == nil):
//     auto-deny with a steering message. Nobody's actively watching
//     — surfacing a modal hours after spawn-time is bad UX. The
//     escape valve is permissions.json (allowlist the tool the
//     subagent needs).
func (t *AgentTool) runChild(
	ctx context.Context,
	taskID string,
	cfg *subagents.AgentConfig,
	userPrompt string,
	transcript *transcriptFile,
	emitToParent func(Event),
	parentDecisions <-chan Decision,
) (result string, errored bool, status subagents.TaskStatus, tokensUsed int) {
	childReg := t.buildChildRegistry(cfg)

	// Mode propagation: the child runs under the same mode as the
	// parent. If parent is in plan mode, the child also enters plan
	// mode with the parent's plan file — its writes are blocked
	// outside that file (PlanModeGate enforces this on every tool
	// dispatch). If parent is in auto mode, the child auto-allows
	// non-safety-floor tools too, and its iteration budget gets the
	// same 4× multiplier the parent's would. Yolo is process-wide
	// and was already shared.
	//
	// We pass parent's POINTERS so a mid-flight mode toggle on the
	// parent propagates to in-flight children — same semantics the
	// parent loop already relies on for its own state.
	childPlanMode := t.PlanMode
	if childPlanMode == nil {
		childPlanMode = &PlanModeState{}
	}
	childAutoMode := t.AutoMode
	if childAutoMode == nil {
		childAutoMode = &AutoModeState{}
	}
	childCfg := LoopConfig{
		Adapter:           t.Adapter,
		Registry:          childReg,
		Permissions:       t.Permissions,
		BypassPermissions: false, // never bypass for child; rely on yolo for true unattended
		Cwd:               t.Cwd, // shared CwdRef — enter_worktree mid-conversation propagates to child loops
		MaxIterations:     childIterationCap,
		PlanMode:          childPlanMode,
		AutoMode:          childAutoMode,
		YoloMode:          t.YoloMode, // shared (process-wide once entered)
	}

	systemPrompt := strings.TrimSpace(cfg.Prompt)
	if t.SystemPromptSuffix != "" {
		systemPrompt += "\n\n" + t.SystemPromptSuffix
	}
	// Hard-rule prohibition: children cannot delegate. Belt and
	// suspenders with the recursion guard in buildChildRegistry.
	systemPrompt += "\n\nYou cannot delegate further. The `Agent` tool is unavailable to you; produce your final answer directly."

	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: systemPrompt},
		{Role: adapter.RoleUser, Content: userPrompt},
	}

	childEvents := make(chan Event, 64)
	childDecisions := make(chan Decision, 1)
	errCh := make(chan error, 1)

	go func() {
		err := Turn(ctx, childCfg, &history, childEvents, childDecisions)
		close(childEvents)
		errCh <- err
	}()

	// Drain child events synchronously. The translator records every
	// event to the transcript and forwards a curated subset
	// (SubagentProgress) to the parent. ContentToken / ReasoningToken
	// are deliberately dropped from the parent stream — context
	// isolation is the entire point.
	//
	// Consecutive identical activities are deduplicated: the model
	// often retries the same grep with slightly different args, and
	// rendering nine near-identical lines just buries the signal.
	// The transcript captures every event for later inspection.
	final := ""
	hitIterCap := false
	toolCallCount := 0
	var lastEmittedActivity string
	var lastActivityRepeats int

	flushRepeat := func() {
		if lastActivityRepeats > 0 && emitToParent != nil {
			emitToParent(SubagentProgress{
				TaskID:    taskID,
				AgentType: cfg.Name,
				Activity:  fmt.Sprintf("  …repeated ×%d", lastActivityRepeats+1),
			})
		}
		lastActivityRepeats = 0
	}

	emitActivity := func(activity string) {
		t.Tasks.AppendActivity(taskID, activity)
		if activity == lastEmittedActivity {
			lastActivityRepeats++
			return
		}
		flushRepeat()
		lastEmittedActivity = activity
		if emitToParent != nil {
			emitToParent(SubagentProgress{TaskID: taskID, AgentType: cfg.Name, Activity: activity})
		}
	}

	for ev := range childEvents {
		transcript.writeEvent(ev)
		switch e := ev.(type) {
		case AssistantMessage:
			if len(e.Message.ToolCalls) == 0 && strings.TrimSpace(e.Message.Content) != "" {
				final = e.Message.Content
			}
		case ToolStart:
			// The Preview already starts with the tool name (e.g.
			// "grep(pattern in path)"), so prefixing with ToolName
			// would just duplicate the verb. Use the preview directly.
			toolCallCount++
			emitActivity(truncate(e.Preview, 96))
		case ApprovalNeeded:
			// Foreground subagents can forward the approval request
			// to the parent's modal — the user is actively watching
			// the parent block on us, so they have context for
			// what's being asked. Background subagents auto-deny:
			// the parent's turn may have ended, the user is
			// elsewhere, a surprise modal would be jarring.
			canForward := emitToParent != nil && parentDecisions != nil
			if canForward {
				// Decorate the preview so the modal makes it obvious
				// the request originated in a subagent rather than
				// directly from the parent assistant. The TUI's
				// approval renderer just shows Preview verbatim, so
				// no UI changes are needed.
				decorated := fmt.Sprintf("[subagent:%s] %s", cfg.Name, e.Preview)
				flushRepeat()
				emitToParent(ApprovalNeeded{
					ToolName: e.ToolName,
					Preview:  decorated,
					ArgsJSON: e.ArgsJSON,
				})
				emitActivity(fmt.Sprintf("waiting for user approval of %s …", e.ToolName))
				// Block on the parent's decisions channel for the
				// answer. The parent loop isn't reading it (it's
				// blocked in Agent.Execute waiting for us) so we
				// own the channel until we route the verdict.
				var verdict Decision
				select {
				case verdict = <-parentDecisions:
				case <-ctx.Done():
					flushRepeat()
					return "", true, subagents.TaskCanceled, 0
				}
				select {
				case childDecisions <- verdict:
				case <-ctx.Done():
					flushRepeat()
					return "", true, subagents.TaskCanceled, 0
				}
				switch verdict {
				case AllowOnce, AllowAlways:
					emitActivity(fmt.Sprintf("approved %s", e.ToolName))
				case Deny:
					emitActivity(fmt.Sprintf("denied %s", e.ToolName))
				case SaveForLater:
					// SaveForLater is plan-mode-specific to
					// exit_plan_mode; shouldn't fire here, but
					// treat defensively as a denial.
					emitActivity(fmt.Sprintf("save-for-later %s (treated as deny)", e.ToolName))
				}
				continue
			}
			// Background path: nobody's watching, auto-deny so the
			// child model can adapt or give up gracefully.
			select {
			case childDecisions <- Deny:
			case <-ctx.Done():
				flushRepeat()
				return "", true, subagents.TaskCanceled, 0
			}
			emitActivity(fmt.Sprintf("auto-denied %s (background subagent cannot prompt; allowlist the tool in permissions.json if needed)", e.ToolName))
		case IterCap:
			hitIterCap = true
		case ErrorEvent:
			// Captured in transcript already; the err returned by Turn
			// after the loop is the authoritative signal.
			_ = e
		}
	}
	flushRepeat()

	turnErr := <-errCh
	// Compute the runner's terminal decision. The Turn loop normally
	// emits one of {TurnDone, IterCap, ErrorEvent} before closing the
	// events channel, but a context cancellation or a send() that
	// fails on a canceled channel can short-circuit Turn before any
	// terminal event lands. That leaves a transcript with no
	// explicit ending — confusing for the user. Always write our
	// own outcome line so the transcript closes cleanly and the
	// task registry's Result field carries an actionable message.
	var outcome string
	switch {
	case ctx.Err() != nil:
		// Distinguish "user explicitly stopped me via /subagents stop"
		// from "the parent turn was canceled" / "context deadline."
		// The Tasks registry sets CanceledByUser=true when Cancel()
		// fires; absence implies the cancellation came from upstream
		// (parent ctx, deadline, Esc on the parent turn).
		who := "parent-turn-canceled-or-deadline"
		if snap, ok := t.Tasks.Get(taskID); ok && snap.CanceledByUser {
			who = "user-stop"
		}
		result, errored, status = "canceled", true, subagents.TaskCanceled
		outcome = fmt.Sprintf("runner_canceled (%s): %s", who, ctx.Err().Error())
	case turnErr != nil:
		msg := fmt.Sprintf("subagent error: %v", turnErr)
		if final != "" {
			msg = final + "\n\n" + msg
		}
		result, errored, status = msg, true, subagents.TaskErrored
		outcome = "runner_errored: " + turnErr.Error()
	case hitIterCap:
		msg := "subagent hit iteration cap before producing a final reply"
		if final != "" {
			msg = final + "\n\n[" + msg + "]"
		}
		result, errored, status = msg, true, subagents.TaskIterCapped
		outcome = "runner_iter_cap"
	case strings.TrimSpace(final) == "":
		// This is the silent failure mode the user just hit: Turn
		// returned nil (no error, no iter-cap), but no AssistantMessage
		// with empty ToolCalls + non-empty Content was ever captured.
		// Most common cause: the model's final assistant turn was
		// reasoning-only, or the adapter stream closed without a
		// concluding message. Pin the description so the next debugger
		// has a concrete starting point.
		msg := "subagent produced no final reply (the child loop ended without emitting an AssistantMessage that had no tool calls and non-empty content — likely an adapter mid-stream close or a reasoning-only final iteration)"
		result, errored, status = msg, true, subagents.TaskErrored
		outcome = "runner_no_final_reply"
	default:
		result, errored, status = final, false, subagents.TaskCompleted
		outcome = "runner_completed"
	}

	t.Tasks.SetToolCalls(taskID, toolCallCount)
	// Estimate token usage from the child's final message history.
	// `contextwindow.EstimateTokens` is the same rough 4-chars-per-
	// token heuristic the TUI status bar already uses, so the
	// number lines up with what users see elsewhere in the
	// product. It's approximate (10–15% error band) but useful for
	// "how big was that delegation?" at-a-glance comparisons.
	tokensUsed = contextwindow.EstimateTokens(history)

	transcript.writeOutcome(outcome, result)
	transcript.close()
	return result, errored, status, tokensUsed
}

// buildChildRegistry clones the parent registry into a new one,
// stripping Agent (recursion guard) and exit_plan_mode (never useful in
// a child), and applying the agent config's tools allowlist when one
// is set. The recursion guard is unconditional — even a config that
// names Agent in its allowlist cannot reintroduce it.
func (t *AgentTool) buildChildRegistry(cfg *subagents.AgentConfig) *Registry {
	out := NewRegistry()
	for _, tool := range t.ParentRegistry.Tools() {
		name := tool.Name()
		if name == agentToolName || name == "exit_plan_mode" {
			continue
		}
		if !cfg.ToolAllowed(name) {
			continue
		}
		out.Register(tool)
	}
	return out
}

func (t *AgentTool) unknownSubagentError(name string) string {
	available := make([]string, 0, len(t.Configs))
	for _, c := range t.Configs {
		available = append(available, c.Name)
	}
	sort.Strings(available)
	return fmt.Sprintf("error: unknown subagent_type %q (available: %s)", name, strings.Join(available, ", "))
}

func (t *AgentTool) fireBackgroundDone(ev SubagentBackgroundDone) {
	t.mu.RLock()
	cb := t.onBackgroundDone
	t.mu.RUnlock()
	if cb != nil {
		cb(ev)
	}
}

// transcriptFile is a thin wrapper around a per-task append-only file
// the runner writes every child event to. Failures are swallowed —
// transcript loss should never block the actual run.
type transcriptFile struct {
	f *os.File
}

func openTranscript(path string, cfg *subagents.AgentConfig, a agentArgs) *transcriptFile {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return &transcriptFile{}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return &transcriptFile{}
	}
	fmt.Fprint(f, childActivityTranscriptHeader)
	fmt.Fprintf(f, "**Agent type**: %s\n", cfg.Name)
	fmt.Fprintf(f, "**Source**: %s\n", cfg.Source)
	if cfg.SourcePath != "" {
		fmt.Fprintf(f, "**Source path**: %s\n", cfg.SourcePath)
	}
	fmt.Fprintf(f, "**Started**: %s\n", time.Now().UTC().Format(time.RFC3339))
	if a.RunInBackground {
		fmt.Fprint(f, "**Background**: yes\n")
	}
	fmt.Fprintf(f, "\n## Prompt\n\n%s\n\n## Events\n\n", a.Prompt)
	return &transcriptFile{f: f}
}

func (tr *transcriptFile) writeEvent(ev Event) {
	if tr.f == nil {
		return
	}
	ts := time.Now().UTC().Format("15:04:05.000")
	switch e := ev.(type) {
	case ReasoningToken:
		fmt.Fprintf(tr.f, "[%s] reasoning: %s\n", ts, e.Text)
	case ContentToken:
		fmt.Fprintf(tr.f, "[%s] content: %s\n", ts, e.Text)
	case ToolStart:
		fmt.Fprintf(tr.f, "[%s] tool_start %s: %s\n", ts, e.ToolName, e.Preview)
	case ToolResult:
		fmt.Fprintf(tr.f, "[%s] tool_result %s (errored=%t):\n%s\n---\n", ts, e.ToolName, e.Errored, e.Output)
	case ApprovalAuto:
		fmt.Fprintf(tr.f, "[%s] approval_auto %s [%s]: %s\n", ts, e.ToolName, e.Source, e.Preview)
	case ApprovalNeeded:
		fmt.Fprintf(tr.f, "[%s] approval_needed %s: %s\n", ts, e.ToolName, e.Preview)
	case AssistantMessage:
		fmt.Fprintf(tr.f, "[%s] assistant: %s\n", ts, e.Message.Content)
	case ErrorEvent:
		fmt.Fprintf(tr.f, "[%s] error: %v\n", ts, e.Err)
	case IterCap:
		fmt.Fprintf(tr.f, "[%s] iter_cap: %d\n", ts, e.Max)
	case TurnDone:
		fmt.Fprintf(tr.f, "[%s] turn_done\n", ts)
	}
}

// writeOutcome appends the runner's final decision to the transcript
// — a guaranteed end-of-file marker that lands even when Turn exited
// without emitting TurnDone / IterCap / ErrorEvent (e.g. context
// canceled mid-iteration, send() failed on a closed channel). The
// outcome label is one of `runner_completed`, `runner_canceled`,
// `runner_errored`, `runner_iter_cap`, or `runner_no_final_reply`;
// the result body is the same string the parent model receives.
func (tr *transcriptFile) writeOutcome(outcome, result string) {
	if tr.f == nil {
		return
	}
	ts := time.Now().UTC().Format("15:04:05.000")
	fmt.Fprintf(tr.f, "\n[%s] %s\n", ts, outcome)
	if result != "" {
		fmt.Fprintf(tr.f, "\n## Final result\n\n%s\n", result)
	}
}

func (tr *transcriptFile) close() {
	if tr.f != nil {
		_ = tr.f.Close()
	}
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
