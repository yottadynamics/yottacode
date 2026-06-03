package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// interruptedToolResult is the synthetic tool_result content injected for
// every tool_use that was orphaned by a user-initiated cancel — either
// because the call was queued in the same batch but never executed, or
// because the call started but was killed mid-flight. Every provider
// rejects an assistant message whose tool_use blocks lack matching
// tool_result entries on the next request, so this content goes in the
// history before Turn returns. The string is also what the model sees on
// the next turn, hence the plain English wording.
const interruptedToolResult = "interrupted by user"

// Streamer is the slice of the adapter the loop actually depends on. Defining
// it here (instead of pulling in the concrete *adapter.Adapter) lets tests
// substitute a scripted implementation without standing up an HTTP server.
type Streamer interface {
	ChatStream(ctx context.Context, messages []adapter.Message, tools []adapter.Tool) <-chan adapter.StreamEvent
}

// LoopConfig is the value-typed configuration for one or more agent turns.
// Channels are passed separately to Turn so the same config can drive a
// streaming session across many turns without rewiring.
type LoopConfig struct {
	Adapter  Streamer
	Registry *Registry
	// Permissions gates every tool call against project-local rules
	// loaded from .yottacode/permissions.json (committable) and
	// .yottacode/permissions.local.json (gitignored). Optional: nil
	// disables rule matching and falls through to the tool's own
	// RequiresApproval policy.
	Permissions *permissions.Permissions
	// BypassPermissions is the internal name for the user-facing
	// --yolo flag.
	// Skip every approval prompt, run silently. DANGEROUS —
	// model-emitted commands execute without a human in the loop.
	// Explicit `deny` rules in permissions.json still refuse the call
	// (bypass is "skip prompts," not "ignore my policy"). Use only
	// in trusted CI / scripted contexts.
	BypassPermissions bool
	Cwd               *CwdRef
	MaxIterations     int
	// PlanMode is the shared plan-mode flag the TUI flips via /plan or
	// Shift+Tab. nil disables plan mode entirely (oneshot; tests). When
	// set and Active, the loop prepends a plan-mode addendum to the
	// system prompt on every request and gates mutating tools through
	// PlanModeGate before approval evaluation. Pointer-shared so a TUI
	// flip takes effect on the next iteration with no reconfiguration.
	PlanMode *PlanModeState

	// AutoMode is the shared auto-mode flag the TUI flips via /auto,
	// Shift+Tab, or the plan-card [Y] hotkey. When active, the loop
	// auto-approves non-safety-floor tool calls (no modal) so the
	// model can implement a multi-step plan without per-edit friction.
	// run_bash and git mutations remain in the safety floor — see
	// IsAutoModeSafetyFloor.
	AutoMode *AutoModeState

	// YoloMode is the unrestricted toggle — auto-approves ALL tool
	// calls including the safety floor, and raises the iteration cap to
	// a generous but finite budget (see yoloIterationCap) so a runaway
	// model still terminates instead of looping forever. Explicit Deny
	// rules in permissions.json still win. Intended for unattended
	// long-running implementations where the user has decided no further
	// oversight is needed. Mutually exclusive with AutoMode and PlanMode
	// at the TUI layer.
	YoloMode *YoloModeState

	// FixedIterationCap pins the effective iteration budget to
	// MaxIterations exactly, bypassing the auto-mode 4× multiplier and
	// the yolo expansion. Subagents set this: a child inherits the
	// parent's AutoMode/YoloMode pointers so its *approval* behavior
	// matches the parent (writes don't block under auto, etc.), but its
	// iteration budget must stay bounded — the user opted into "let the
	// parent run unattended," not "let the parent spawn child loops with
	// 4×/unbounded budgets." Without this the shared mode pointers leak
	// the multiplier into children (see childIterationCap). Default
	// false: top-level loops keep the mode-scaled budget.
	FixedIterationCap bool

	// Checkpoints, when non-nil, receives pre-image snapshot
	// requests for every Mutator tool call. nil disables checkpoint
	// capture entirely — oneshot and tests pass nil. The TUI builds
	// a *checkpoint.Store and attaches it; see internal/tui/run.go.
	// Implementations must be safe under concurrent SnapshotPath
	// calls within a turn.
	Checkpoints CheckpointWriter

	// UserMessages, when non-nil, is checked (non-blocking) after
	// each tool round completes. If a message is pending, it's
	// appended to history as a RoleUser message and the loop
	// continues — the next streamIteration sees the user's input
	// alongside the tool results. This lets the TUI inject
	// additive instructions ("also check the tests") without
	// cancelling the active turn.
	UserMessages <-chan string

	// Compaction, when non-nil, enables in-loop history compaction:
	// once the running history approaches Window, the loop summarizes
	// its older middle messages in place and continues with a compacted
	// history. This is the ONLY context management a subagent has — it
	// runs Turn directly rather than through the TUI's turn-boundary
	// summarizer, so without this a long subagent accumulates until the
	// provider rejects the request. nil disables it; the TUI and oneshot
	// leave it unset and manage context at their own boundaries.
	Compaction *CompactionConfig
}

// CompactionConfig parameterizes in-loop compaction (LoopConfig.Compaction).
type CompactionConfig struct {
	// Window is the model's context window in tokens. <=0 disables.
	Window int
	// Threshold is the fraction of Window at which compaction fires,
	// checked at the top of each iteration. <=0 disables.
	Threshold float64
	// Summarizer streams the one-shot summary call. nil falls back to
	// the loop's own Adapter (cache-safe routing can inject a cheaper
	// model here).
	Summarizer Streamer
	// SummarizerWindow is the context window (tokens) of the Summarizer
	// model. Under cache-safe routing the Summarizer is a cheaper model
	// that may have a SMALLER window than the loop's own model, so the
	// summary call's INPUT must be budgeted against whichever window is
	// smaller — otherwise a middle sized to the (larger) loop window
	// overflows the summarizer and the summary call fails, defeating
	// compaction. 0 means "same as Window" (the summarizer is the loop's
	// own adapter, so the windows match).
	SummarizerWindow int
}

// CheckpointWriter is the slice of the checkpoint store the agent loop
// depends on. Defining it here keeps internal/checkpoint out of the
// agent's import surface and lets tests substitute a recording fake.
type CheckpointWriter interface {
	SnapshotPath(sessionID, checkpointID, absPath string) error
}

// checkpointIDKey is the unexported context key carrying the active
// checkpoint id through one Turn. Set by the TUI's startTurn before it
// launches Turn; read by executeToolCall right before each Mutator
// tool runs. Out-of-band so adding the field doesn't change Turn's
// signature.
type checkpointIDKey struct{}

// checkpointSessionKey carries the session id alongside the checkpoint
// id so the snapshot writer doesn't need a separate lookup.
type checkpointSessionKey struct{}

// WithCheckpoint binds a checkpoint id + session id to ctx. The TUI calls
// this immediately after checkpoints.Begin returns, before passing
// ctx into Turn. Returns ctx unchanged when either id is empty so
// callers don't need to special-case nil-checkpoint paths.
func WithCheckpoint(ctx context.Context, sessionID, cpID string) context.Context {
	if cpID == "" || sessionID == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, checkpointIDKey{}, cpID)
	ctx = context.WithValue(ctx, checkpointSessionKey{}, sessionID)
	return ctx
}

// CheckpointFromContext recovers the (sessionID, checkpointID) pair
// stored by WithCheckpoint. Returns empty strings when no checkpoint
// is bound — callers should treat that as "checkpointing disabled."
func CheckpointFromContext(ctx context.Context) (sessionID, cpID string) {
	if v, ok := ctx.Value(checkpointIDKey{}).(string); ok {
		cpID = v
	}
	if v, ok := ctx.Value(checkpointSessionKey{}).(string); ok {
		sessionID = v
	}
	return sessionID, cpID
}

type loopState struct {
	iteration int
	history   *[]adapter.Message
}

type toolExecResult struct {
	content string
	images  []adapter.ImageBlock
	denied  bool
	err     error
}

const (
	continueReasonToolCalls       = "tool_calls"
	continueReasonTruncatedOutput = "truncated_output"
)

const (
	// yoloIterationMultiplier scales the configured iteration cap for
	// yolo mode (vs auto mode's 4x). Yolo is meant for long unattended
	// runs, so the budget is generous — but finite.
	yoloIterationMultiplier = 20
	// yoloIterationFloor is the minimum yolo budget, so a small
	// --max-iterations doesn't starve an unattended run. With the
	// default cap of 50 this is also the effective ceiling (50*20 ==
	// 1000), matching a sane "runaway backstop" default.
	yoloIterationFloor = 1000
)

// yoloIterationCap returns the finite iteration budget for yolo mode.
// It scales off the user-configurable maxIterations (so /max-iterations
// and --max-iterations still tune it) with a high floor, and guards
// against int overflow on absurd inputs so the loop never wraps to a
// non-positive cap that would silently run zero iterations.
func yoloIterationCap(maxIterations int) int {
	c := yoloIterationFloor
	if maxIterations > 0 {
		scaled := maxIterations * yoloIterationMultiplier
		// scaled/mult == maxIterations only when the multiply didn't
		// overflow; on overflow keep the floor (a safe finite bound).
		if scaled/yoloIterationMultiplier == maxIterations && scaled > c {
			c = scaled
		}
	}
	return c
}

// Turn drives one user-initiated round: it streams an assistant response
// (emitting Reasoning/Content tokens), dispatches any tool calls (with
// approval flow if required), feeds the results back, and loops until the
// assistant produces a tool-free reply or hits MaxIterations.
//
// events is producer-only; Turn never closes it (caller owns lifecycle).
// decisions is consumer-only; Turn reads from it only after emitting an
// ApprovalNeeded event. Cancel ctx to abort cleanly.
//
// history is mutated in place: user message is assumed already appended by
// the caller; Turn appends each assistant reply and tool result.
func Turn(
	ctx context.Context,
	cfg LoopConfig,
	history *[]adapter.Message,
	events chan<- Event,
	decisions <-chan Decision,
) error {
	state := loopState{history: history}
	// Effective cap: auto mode quadruples the configured limit
	// because the user explicitly opted into "let this run" — most
	// plan implementations need 100–200 iterations. Yolo mode goes
	// further still but stays finite — a runaway backstop so a
	// confused or adversarial model can't loop forever burning the
	// user's API budget with no escape short of Ctrl+C. Read once at
	// turn start so a mode toggle mid-turn doesn't change the budget
	// mid-flight.
	effectiveCap := cfg.MaxIterations
	switch {
	case cfg.FixedIterationCap:
		// Subagents: keep MaxIterations exactly, even when the parent's
		// shared AutoMode/YoloMode pointers are active. The mode still
		// governs approvals for the child; only the budget is pinned.
		effectiveCap = cfg.MaxIterations
	case cfg.YoloMode.IsActive():
		effectiveCap = yoloIterationCap(cfg.MaxIterations)
	case cfg.AutoMode.IsActive():
		effectiveCap *= 4
	}
	for state.iteration = 1; state.iteration <= effectiveCap; state.iteration++ {
		if err := send(ctx, events, IterationStart{
			Number: state.iteration,
			Max:    effectiveCap,
		}); err != nil {
			return err
		}

		// Report live context usage for loops that know their window
		// (subagents set Compaction). The runner forwards this to the task
		// registry so the live dock can show each subagent's context fill.
		// The main TUI/oneshot loops leave Compaction nil and skip this —
		// their status bar tracks context on its own.
		//
		// Include the tool-schema overhead (the cloned toolset rides on
		// every request but isn't in the message slice) so the dock's
		// reported fill matches what the compaction trigger actually
		// measures — otherwise compaction could fire while the dock still
		// looked well under the window.
		if cfg.Compaction != nil && cfg.Compaction.Window > 0 {
			_ = send(ctx, events, ContextUsage{
				Tokens: contextwindow.EstimateTokens(*history) + toolSchemaTokens(cfg.Registry),
				Window: cfg.Compaction.Window,
			})
		}

		// Compact older history in place before the next model call when
		// it approaches the window. The top of the iteration is a clean
		// point — every prior tool_use already has its matching
		// tool_result appended — so the rewrite preserves pairing. No-op
		// unless cfg.Compaction is set (subagents opt in; TUI/oneshot
		// manage context themselves).
		if err := maybeCompact(ctx, cfg, state.history, events); err != nil {
			return err
		}

		final, err := streamIteration(ctx, cfg, *state.history, events)
		if err != nil {
			// Distinguish a user-initiated cancel (Enter / Esc / Ctrl+C
			// fired the parent's turnCancel) from a real adapter error.
			// On cancel we preserve whatever streamed so history stays
			// valid for the follow-up turn the TUI is about to start;
			// on error we keep the existing ErrorEvent path so the
			// failure stays loud. context.Canceled is the user path;
			// context.DeadlineExceeded would also land here if a future
			// per-turn timeout is added.
			if isCancelErr(err) {
				if final != nil {
					*state.history = append(*state.history, *final)
				}
				partialContent := ""
				if final != nil {
					partialContent = final.Content
				}
				_ = send(context.Background(), events, TurnInterrupted{
					PartialContent: partialContent,
				})
				return err
			}
			_ = send(ctx, events, ErrorEvent{Err: err})
			return err
		}

		*state.history = append(*state.history, *final)
		if err := send(ctx, events, AssistantMessage{Message: *final}); err != nil {
			if isCancelErr(err) {
				_ = send(context.Background(), events, TurnInterrupted{
					PartialContent: final.Content,
				})
			}
			return err
		}

		if len(final.ToolCalls) > 0 {
			if err := send(ctx, events, IterationContinue{
				Number:    state.iteration,
				Reason:    continueReasonToolCalls,
				ToolCalls: len(final.ToolCalls),
			}); err != nil {
				return err
			}
			if err := executeToolCalls(ctx, cfg, final.ToolCalls, history, events, decisions); err != nil {
				if isCancelErr(err) {
					// executeToolCalls has already appended synthetic
					// tool_result entries for any orphaned calls before
					// returning, so history is valid. Count how many of
					// the call slice landed as synthetic so the UI can
					// render "N tool calls cancelled."
					orphans := countOrphanedToolResults(*history, final.ToolCalls)
					_ = send(context.Background(), events, TurnInterrupted{
						PartialContent: final.Content,
						OrphanedCalls:  orphans,
					})
					return err
				}
				_ = send(ctx, events, ErrorEvent{Err: err})
				return err
			}
			// Between tool rounds: check for a user message queued
			// by the TUI. History is clean (all tool_use entries
			// have matching tool_result), so appending a user
			// message here pairs naturally with the next
			// streamIteration call.
			if cfg.UserMessages != nil {
				select {
				case msg := <-cfg.UserMessages:
					if msg != "" {
						*state.history = append(*state.history, adapter.Message{
							Role:    adapter.RoleUser,
							Content: msg,
						})
						_ = send(ctx, events, UserMessageAppended{Content: msg})
					}
				default:
				}
			}
			continue
		}

		if shouldContinueIncomplete(final) {
			if state.iteration == effectiveCap {
				break
			}
			if err := send(ctx, events, IterationContinue{
				Number: state.iteration,
				Reason: continueReasonTruncatedOutput,
			}); err != nil {
				return err
			}
			*state.history = append(*state.history, adapter.Message{
				Role:    adapter.RoleUser,
				Content: "Continue exactly where you left off. No recap. No apology.",
			})
			continue
		}

		return send(ctx, events, TurnDone{})
	}
	if err := send(ctx, events, IterCap{Max: effectiveCap}); err != nil {
		return err
	}
	return nil
}

func streamIteration(
	ctx context.Context,
	cfg LoopConfig,
	history []adapter.Message,
	events chan<- Event,
) (*adapter.Message, error) {
	// Hide exit_plan_mode from the schema when plan mode is off — the
	// model should never see (or invent) the call outside of /plan.
	planActive := cfg.PlanMode.IsActive()
	tools := cfg.Registry.AsAdapterToolsFiltered(func(name string) bool {
		if name == "exit_plan_mode" {
			return planActive
		}
		return true
	})
	// When plan mode is active, prepend a fresh system message carrying
	// the plan-mode addendum (path + current contents of the plan
	// file). Re-read on every iteration so the model always sees the
	// live plan body — critical for resume (model has no other way to
	// learn what was previously planned) and for catching out-of-band
	// edits (user manually tweaks the plan in $EDITOR mid-session).
	// Done per-iteration on a local slice so the persisted history
	// stays untouched.
	msgs := history
	if planActive {
		body := readPlanFileForAddendum(cfg.PlanMode.PlanFile)
		msgs = append([]adapter.Message{{
			Role:    adapter.RoleSystem,
			Content: fmt.Sprintf(PlanModeAddendum, cfg.PlanMode.PlanFile, body),
		}}, history...)
	}
	// Defensive normalization: never send two assistant turns in a row
	// (Gemini rejects it; Claude 4.6+ treats it as prefill). No-op for an
	// already-alternating history — see mergeAdjacentAssistant.
	msgs = mergeAdjacentAssistant(msgs)
	stream := cfg.Adapter.ChatStream(ctx, msgs, tools)

	// Accumulate streamed content tokens into a buffer alongside the
	// normal send. If ctx is cancelled mid-stream (user hit Enter / Esc
	// to interrupt), Turn needs to append a partial assistant message to
	// history so the next turn's request body doesn't reference an
	// orphan tool_use (or, in the simpler case, so the user's
	// interruption preserves what they already saw on screen). Reasoning
	// tokens are NOT accumulated: providers don't echo them back in
	// history, and partial reasoning would just bloat the user message
	// trail without changing what the model sees next turn. Any
	// in-flight tool_use the adapter was mid-building is intentionally
	// dropped — an assistant message with content and no tool_calls is
	// valid for every provider, and the alternative (smuggling out
	// half-parsed JSON args) is fragile.
	var final *adapter.Message
	var contentBuf strings.Builder
	for ev := range stream {
		switch ev.Kind {
		case adapter.EventReasoning:
			if err := send(ctx, events, ReasoningToken{Text: ev.Token}); err != nil {
				return partialAssistantMessage(&contentBuf), err
			}
		case adapter.EventTokenDelta:
			contentBuf.WriteString(ev.Token)
			if err := send(ctx, events, ContentToken{Text: ev.Token}); err != nil {
				return partialAssistantMessage(&contentBuf), err
			}
		case adapter.EventStreamProgress:
			if err := send(ctx, events, StreamProgress{}); err != nil {
				return partialAssistantMessage(&contentBuf), err
			}
		case adapter.EventProviderTool:
			if err := send(ctx, events, ProviderToolCall{
				ToolName: ev.ProviderToolName,
				Phase:    ev.ProviderToolPhase,
				Detail:   ev.ProviderToolDetail,
			}); err != nil {
				return partialAssistantMessage(&contentBuf), err
			}
		case adapter.EventFallback:
			if err := send(ctx, events, Fallback{
				From:   ev.FallbackFrom,
				To:     ev.FallbackTo,
				Reason: ev.FallbackReason,
				Policy: ev.FallbackPolicy,
			}); err != nil {
				return partialAssistantMessage(&contentBuf), err
			}
		case adapter.EventDone:
			final = ev.Final
		case adapter.EventErr:
			return partialAssistantMessage(&contentBuf), ev.Err
		}
	}
	if final == nil {
		// Stream channel closed without EventDone. If ctx was cancelled
		// the adapter is allowed to bail without emitting Done — treat
		// it as an interrupt and surface whatever we buffered. Otherwise
		// it's an adapter bug; return the existing error so it's loud.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return partialAssistantMessage(&contentBuf), ctxErr
		}
		return nil, errors.New("agent: stream ended without a final message")
	}
	return final, nil
}

// partialAssistantMessage returns nil when no content has been buffered
// (the cancel landed before the first token), so Turn can distinguish
// "nothing to preserve" from "preserve this partial reply."
func partialAssistantMessage(buf *strings.Builder) *adapter.Message {
	if buf == nil || buf.Len() == 0 {
		return nil
	}
	return &adapter.Message{
		Role:    adapter.RoleAssistant,
		Content: buf.String(),
	}
}

func executeToolCalls(
	ctx context.Context,
	cfg LoopConfig,
	calls []adapter.ToolCall,
	history *[]adapter.Message,
	events chan<- Event,
	decisions <-chan Decision,
) error {
	for len(calls) > 0 {
		if batch := parallelBatchSize(cfg, calls); batch > 1 {
			results, err := executeToolCallsParallel(ctx, cfg, calls[:batch], events, decisions)
			if err != nil {
				if isCancelErr(err) {
					// Mixed-state batch: completed workers keep their
					// real result; cancelled / errored workers get the
					// synthetic interrupt marker. Then every queued
					// call after this batch (none started) gets the
					// marker too. History must end with a tool_result
					// for every tool_use in the assistant message.
					appendToolResultsWithInterrupts(history, calls[:batch], results)
					appendSyntheticInterrupts(history, calls[batch:])
				}
				return err
			}
			appendToolResults(history, calls[:batch], results)
			calls = calls[batch:]
			continue
		}
		tc := calls[0]
		result, images, denied, err := executeToolCall(ctx, cfg, tc, events, decisions)
		if err != nil {
			if isCancelErr(err) {
				appendSyntheticInterrupts(history, calls)
			}
			return err
		}
		*history = append(*history, adapter.Message{
			Role:       adapter.RoleTool,
			Content:    result,
			Images:     images,
			ToolCallID: tc.ID,
		})
		if denied {
			// Inform the model; it can choose to ask differently or give up.
		}
		calls = calls[1:]
	}
	return nil
}

// appendSyntheticInterrupts pairs every tool_call in `calls` with a
// "interrupted by user" tool_result. Idempotent against already-
// resolved calls is NOT a goal — callers must only pass calls that have
// not yet had a real result appended (the serial path's
// `calls = calls[1:]` shrinks the slice after each real append, and the
// parallel path passes `calls[batch:]` for batches that never started).
func appendSyntheticInterrupts(history *[]adapter.Message, calls []adapter.ToolCall) {
	for _, tc := range calls {
		*history = append(*history, adapter.Message{
			Role:       adapter.RoleTool,
			Content:    interruptedToolResult,
			ToolCallID: tc.ID,
		})
	}
}

// appendToolResultsWithInterrupts is the cancel-aware companion to
// appendToolResults. Workers that completed cleanly (nil err) keep
// their real result; workers that errored or returned empty content
// get the synthetic interrupt marker. The "empty content" check is the
// defensive arm — a worker may have been cancelled inside Execute
// before it produced any output but after recording err. Pairs every
// tool_call with exactly one tool_result so the assistant message
// invariant holds.
func appendToolResultsWithInterrupts(history *[]adapter.Message, calls []adapter.ToolCall, results []toolExecResult) {
	for i, tc := range calls {
		content := results[i].content
		if results[i].err != nil || content == "" {
			content = interruptedToolResult
		}
		*history = append(*history, adapter.Message{
			Role:       adapter.RoleTool,
			Content:    content,
			ToolCallID: tc.ID,
		})
	}
}

func parallelBatchSize(cfg LoopConfig, calls []adapter.ToolCall) int {
	n := 0
	for _, tc := range calls {
		tool, ok := cfg.Registry.Get(tc.Name)
		if !ok || tool.RequiresApproval(tc.ArgsJSON) || !toolParallelSafe(tool, tc.ArgsJSON) {
			break
		}
		// In plan mode, blocked calls must hit the serial path so the
		// gate can short-circuit before tool.Execute. Bundling them in
		// a parallel batch would skip the gate entirely (the parallel
		// branch goes straight to Execute via the read-only fast
		// path).
		if cfg.PlanMode.IsActive() {
			if _, blocked := PlanModeGate(tool, tc.ArgsJSON, cfg.PlanMode.PlanFile); blocked {
				break
			}
		}
		n++
	}
	return n
}

func executeToolCallsParallel(
	ctx context.Context,
	cfg LoopConfig,
	calls []adapter.ToolCall,
	events chan<- Event,
	decisions <-chan Decision,
) ([]toolExecResult, error) {
	results := make([]toolExecResult, len(calls))
	var wg sync.WaitGroup
	errCh := make(chan error, len(calls))
	// One shared lock per batch guards the single decisions channel and
	// the single TUI modal: any worker that needs to prompt the user (a
	// permission Ask, a path-trust elevation, a forwarded subagent
	// approval) acquires it for the duration of that round-trip so two
	// workers can't interleave on the channel. Workers that never prompt
	// run fully in parallel — they never touch the gate.
	gateCtx := WithApprovalGate(ctx, new(sync.Mutex))
	for i, tc := range calls {
		wg.Add(1)
		go func(i int, tc adapter.ToolCall) {
			defer wg.Done()
			result, images, denied, err := executeToolCall(gateCtx, cfg, tc, events, decisions)
			results[i] = toolExecResult{content: result, images: images, denied: denied, err: err}
			if err != nil {
				errCh <- err
			}
		}(i, tc)
	}
	wg.Wait()
	// Always return the populated results slice — even on error — so a
	// cancel caller can pair partial successes with their tool_use IDs
	// and synthesize a marker only for the workers that actually
	// errored. Returning (nil, err) would force a synthetic result for
	// every parallel call, including ones that finished cleanly before
	// the cancel landed.
	//
	// Drain every error rather than just the first: a batch can fail in
	// more than one way (e.g. a real tool error alongside a context
	// cancellation), and dropping the rest hides diagnostics.
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return results, errors.Join(errs...)
}

func appendToolResults(history *[]adapter.Message, calls []adapter.ToolCall, results []toolExecResult) {
	for i, tc := range calls {
		*history = append(*history, adapter.Message{
			Role:       adapter.RoleTool,
			Content:    results[i].content,
			Images:     results[i].images,
			ToolCallID: tc.ID,
		})
	}
}

func shouldContinueIncomplete(final *adapter.Message) bool {
	if final == nil || len(final.ToolCalls) > 0 || final.Content == "" {
		return false
	}
	switch final.StopReason {
	case "length", "max_output_tokens", "incomplete":
		return true
	default:
		return false
	}
}

// executeToolCall handles one tool invocation. Layered approval flow:
//
//  1. Permissions evaluation (Deny > Allow > Ask) — explicit user
//     rules from .yottacode/permissions{,.local}.json. Deny wins
//     even under BypassPermissions; Allow skips the prompt; Ask
//     forces a prompt even if the tool would normally auto-execute.
//  2. BypassPermissions — auto-approve everything else (announced
//     in scrollback so audits stay honest).
//  3. Tool's own RequiresApproval — the pre-existing policy
//     (read-only auto-execute, mutators prompt).
//
// Returns (result, denied, fatalErr). A user-denied call is not a
// fatal error — "denied by user" is reported back to the model so it
// can recover.
func executeToolCall(
	ctx context.Context,
	cfg LoopConfig,
	tc adapter.ToolCall,
	events chan<- Event,
	decisions <-chan Decision,
) (string, []adapter.ImageBlock, bool, error) {
	tool, ok := cfg.Registry.Get(tc.Name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", tc.Name), nil, false, nil
	}
	preview := tool.PreviewCall(tc.ArgsJSON)

	// Plan-mode gate runs BEFORE permissions evaluation: explicit deny
	// rules still beat the gate (the model never gets to call a denied
	// tool, plan mode or not), but the gate beats the tool's own
	// RequiresApproval policy. Returning the gate's error string as a
	// tool result lets the model recover by switching to a read-only
	// or plan-file alternative on the next iteration.
	if cfg.PlanMode.IsActive() {
		if msg, blocked := PlanModeGate(tool, tc.ArgsJSON, cfg.PlanMode.PlanFile); blocked {
			_ = send(ctx, events, ApprovalAuto{
				ToolName: tool.Name(), Preview: preview, Source: "plan-mode-block",
			})
			return msg, nil, true, nil
		}
	}

	verdict := permissions.Default
	if cfg.Permissions != nil {
		verdict = cfg.Permissions.Evaluate(tool.Name(), tc.ArgsJSON)
	}

	// Permission Deny always wins, even over plan-mode auto-allow. A
	// user who explicitly denies write_file via permissions.json wants
	// no writes at all, plan file included.
	if verdict == permissions.Deny {
		_ = send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "deny-rule",
		})
		return "denied by permissions.json deny rule", nil, true, nil
	}

	// Mode-priority approval chain. Order matters:
	//   1. Yolo: auto-allow every tool (no safety floor).
	//   2. Plan-mode auto-allow for the plan file.
	//   3. Auto-mode auto-allow for non-floor tools.
	//   4. Default: permissions verdict → tool's own RequiresApproval.
	switch {
	case cfg.YoloMode.IsActive():
		if err := send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "yolo-mode",
		}); err != nil {
			return "", nil, false, err
		}
	case cfg.PlanMode.IsActive() && IsPlanFileWrite(tool.Name(), tc.ArgsJSON, cfg.PlanMode.PlanFile):
		if err := send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "plan-mode-allow",
		}); err != nil {
			return "", nil, false, err
		}
	case cfg.AutoMode.IsActive() && tool.Name() == "run_bash" && IsAutoModeSafeBash(tc.ArgsJSON):
		if err := send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "auto-mode-safe-bash",
		}); err != nil {
			return "", nil, false, err
		}
	case cfg.AutoMode.IsActive() && !IsAutoModeSafetyFloor(tool.Name()):
		if err := send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "auto-mode",
		}); err != nil {
			return "", nil, false, err
		}
	default:
		switch verdict {
		case permissions.Allow:
			if err := send(ctx, events, ApprovalAuto{
				ToolName: tool.Name(), Preview: preview, Source: "permissions",
			}); err != nil {
				return "", nil, false, err
			}
		case permissions.Ask:
			if denied, savedForLater, err := promptForApproval(ctx, cfg, tool, tc, preview, events, decisions); err != nil || denied || savedForLater {
				return deniedResultForMultimodal(tool.Name(), denied, savedForLater, err)
			}
		default:
			if tool.RequiresApproval(tc.ArgsJSON) {
				if cfg.BypassPermissions && tool.Name() != "exit_plan_mode" {
					if err := send(ctx, events, ApprovalAuto{
						ToolName: tool.Name(), Preview: preview, Source: "bypass-permissions",
					}); err != nil {
						return "", nil, false, err
					}
				} else {
					if denied, savedForLater, err := promptForApproval(ctx, cfg, tool, tc, preview, events, decisions); err != nil || denied || savedForLater {
						return deniedResultForMultimodal(tool.Name(), denied, savedForLater, err)
					}
				}
			}
		}
	}

	if err := send(ctx, events, ToolStart{ToolName: tool.Name(), Preview: preview, ArgsJSON: tc.ArgsJSON}); err != nil {
		return "", nil, false, err
	}
	// Attach the parent's events + decisions channels so tools that
	// need to participate in the parent's approval flow (today:
	// AgentTool, which forwards a foreground subagent's child
	// ApprovalNeeded events to the parent's modal and routes the
	// user's answer back) can do so without changing the Tool
	// interface signature. While this tool's Execute is running the
	// parent loop is blocked on its return, so neither channel has a
	// competing reader/writer.
	toolCtx := WithParentDecisions(WithParentEvents(ctx, events), decisions)
	// Snapshot pre-images for Mutator tools before they run. Soft-fail:
	// a snapshot error must not block the user's tool call — surface it
	// as a scrollback event but let the tool proceed.
	if cfg.Checkpoints != nil {
		if sessionID, cpID := CheckpointFromContext(ctx); cpID != "" {
			for _, p := range ToolPathsToSnapshot(tool, cfg.Cwd.Get(), tc.ArgsJSON) {
				if err := cfg.Checkpoints.SnapshotPath(sessionID, cpID, p); err != nil {
					_ = send(ctx, events, CheckpointInfo{Message: fmt.Sprintf("snapshot %s: %v", p, err)})
				}
			}
		}
	}
	// Snapshot the pre-tool cwd so we can emit CwdChanged after the
	// tool returns if it swapped (today: enter_worktree / exit_worktree
	// call cfg.Cwd.Set). The shared *CwdRef is the only authority on
	// in-session cwd state — tools that swap go through it, never
	// directly through os.Chdir without the ref.
	cwdBefore := ""
	if cfg.Cwd != nil {
		cwdBefore = cfg.Cwd.Get()
	}

	// Normalize string-encoded scalar args (e.g. {"max_results":"5"} from
	// Llama-via-NIM/Ollama) to the types the tool's schema declares. No-op
	// for compliant providers; fail-open for anything it can't safely fix.
	// See yottacode-roadmap/tool-arg-coercion.md.
	argsJSON := coerceArgsToSchema(tc.ArgsJSON, tool.Schema())

	var out string
	var images []adapter.ImageBlock
	var err error
	if mm, ok := tool.(MultimodalTool); ok {
		var res MultimodalResult
		res, err = mm.ExecuteMultimodal(toolCtx, argsJSON)
		out, images = res.Content, res.Images
	} else {
		out, err = tool.Execute(toolCtx, argsJSON)
	}
	// Inline path-trust elevation: if the write validator rejected
	// the target as outside the workspace, give the user a chance to
	// elevate (allow once / trust for session) before the model sees
	// the error. On non-Deny, the consumer has mutated the tool's
	// WriteOpts.AllowedPaths, so a retry of Execute succeeds via the
	// normal path. On Deny, fall through to the regular error path
	// — the model sees the descriptive *ErrPathOutsideWorkspace
	// message with its recovery hint. See
	// yottacode-roadmap/folder-trust.md "Prompt 2."
	if err != nil {
		if elev, ok := pathElevation(err); ok {
			d, derr := promptForPathElevation(ctx, tool.Name(), elev, tc.ArgsJSON, events, decisions)
			if derr != nil {
				return "", nil, false, derr
			}
			if d != Deny {
				if mm, ok := tool.(MultimodalTool); ok {
					var res MultimodalResult
					res, err = mm.ExecuteMultimodal(toolCtx, argsJSON)
					out, images = res.Content, res.Images
				} else {
					out, err = tool.Execute(toolCtx, argsJSON)
				}
			}
		}
	}
	if err != nil {
		if isCancelErr(err) {
			return "", nil, false, err
		}
		if ctx.Err() != nil {
			return "", nil, false, ctx.Err()
		}
		msg := fmt.Sprintf("error: %v", err)
		_ = send(ctx, events, ToolResult{ToolName: tool.Name(), Output: msg, Errored: true})
		return msg, nil, false, nil
	}
	if err := send(ctx, events, ToolResult{ToolName: tool.Name(), Output: out, Errored: false}); err != nil {
		// Tool ran to completion but ctx fired before we could
		// announce its result — propagate cancel so the caller treats
		// this slot as orphaned (synthetic result). The real `out` is
		// dropped intentionally: a model that interrupted mid-tool
		// will see uniform "interrupted by user" markers across the
		// batch instead of one stale real result amid synthetic ones.
		return "", nil, false, err
	}
	if cfg.Cwd != nil {
		if after := cfg.Cwd.Get(); after != cwdBefore {
			_ = send(ctx, events, CwdChanged{NewCwd: after})
		}
	}
	if pa, ok := tool.(planAware); ok {
		if store := pa.PlanStore(); store != nil {
			_ = send(ctx, events, TodoUpdate{Todos: store.Snapshot()})
		}
	}
	return out, images, false, nil
}

// planAware is the optional capability marker for tools that maintain
// a PlanStore. After such a tool runs successfully the loop snapshots
// the store and emits a TodoUpdate event so consumers (TUI, oneshot)
// can render the new state. Keeping the integration behind an
// interface avoids string-matching on tool names.
type planAware interface {
	PlanStore() *PlanStore
}

// pathElevation extracts the inline path-trust elevation signal
// from a tool's error return. The write-path validator returns a
// *ErrPathOutsideWorkspace exactly in the case Prompt 2 wants to
// catch; this helper isolates the errors.As call so loop.go's
// runTool stays narrow.
func pathElevation(err error) (*ErrPathOutsideWorkspace, bool) {
	var sentinel *ErrPathOutsideWorkspace
	if errors.As(err, &sentinel) {
		return sentinel, true
	}
	return nil, false
}

// promptForPathElevation emits PathTrustElevationNeeded and blocks
// on the consumer's decision (the same decisions channel used by
// promptForApproval). Returns the Decision verbatim — the caller
// in runTool decides whether to retry Execute (non-Deny) or fall
// through to the structured error path (Deny).
//
// Cancellation propagates as an error. Any non-cancel send/recv
// failure also propagates so a half-completed elevation never
// leaves the loop in an inconsistent state.
func promptForPathElevation(
	ctx context.Context,
	toolName string,
	sentinel *ErrPathOutsideWorkspace,
	argsJSON string,
	events chan<- Event,
	decisions <-chan Decision,
) (Decision, error) {
	// Serialize against other concurrent workers in the same parallel
	// batch (no-op on the serial path); released when this elevation's
	// decision is in hand.
	unlock := lockApprovalGate(ctx)
	defer unlock()
	if err := send(ctx, events, PathTrustElevationNeeded{
		ToolName:     toolName,
		Path:         sentinel.Path,
		Cwd:          sentinel.Cwd,
		AllowedRoots: append([]string(nil), sentinel.AllowedRoots...),
		ArgsJSON:     argsJSON,
	}); err != nil {
		return Deny, err
	}
	select {
	case <-ctx.Done():
		return Deny, ctx.Err()
	case d := <-decisions:
		return d, nil
	}
}

// promptForApproval emits ApprovalNeeded and blocks on the user's
// decision. Returns (denied, savedForLater, err) — err is non-nil
// only on context cancellation; denied=true when the user picked
// No/Keep-planning; savedForLater=true when the user picked the
// plan-mode "[L] approve and implement later" option (only fires for
// exit_plan_mode; the standard modal never sends it).
func promptForApproval(
	ctx context.Context,
	cfg LoopConfig,
	tool Tool,
	tc adapter.ToolCall,
	preview string,
	events chan<- Event,
	decisions <-chan Decision,
) (bool, bool, error) {
	// Serialize this round-trip against other concurrent workers in the
	// same parallel batch (no-op on the serial path). Released as soon as
	// the user's decision is in hand — never held across tool.Execute.
	unlock := lockApprovalGate(ctx)
	defer unlock()
	if err := send(ctx, events, ApprovalNeeded{
		ToolName: tool.Name(), Preview: preview, ArgsJSON: tc.ArgsJSON,
	}); err != nil {
		return false, false, err
	}
	var d Decision
	select {
	case <-ctx.Done():
		return false, false, ctx.Err()
	case d = <-decisions:
	}
	if d == Deny {
		return true, false, nil
	}
	if d == SaveForLater {
		return false, true, nil
	}
	if d == AllowAlways && cfg.Permissions != nil {
		// Inject worktree-aware path normalization so [A]-always inside
		// a yottacode worktree doesn't bake the auto-generated worktree
		// name (e.g. `noble-hopping-salmon`) into the saved rule.
		// NormalizeForRule rewrites the name segment to `*` for paths
		// under ~/.yottacode/worktrees/<slug>/<name>/; pass-through
		// otherwise. The deriver applies it to both cwd and any
		// absolute descriptor, so relative-path AND absolute-path tool
		// args produce the same name-agnostic rule.
		if rule, ok := permissions.DeriveAllowRule(tool.Name(), tc.ArgsJSON, cfg.Cwd.Get(), worktree.NormalizeForRule); ok {
			if err := cfg.Permissions.AddAllow(rule); err != nil {
				_ = send(ctx, events, ApprovalAuto{
					ToolName: tool.Name(),
					Preview:  fmt.Sprintf("[warn] could not save rule %q: %v", rule, err),
					Source:   "permissions",
				})
			} else {
				_ = send(ctx, events, ApprovalAuto{
					ToolName: tool.Name(),
					Preview:  "saved rule: " + rule,
					Source:   "permissions",
				})
			}
		}
	}
	return false, false, nil
}

// deniedResultFor packages the "user denied this tool" outcome. The
// tool name parameter exists so exit_plan_mode can carry useful
// steering — for that tool, "denied" means the user wants the model to
// keep refining the plan, not that they've vetoed a code action.
// Returning the generic "denied by user" there would teach the model
// to give up. The savedForLater bool is plan-mode-specific: when the
// user picks "[L] approve and implement later" the model gets a firm
// "end this turn now" signal instead of refinement guidance.
func deniedResultFor(toolName string, denied, savedForLater bool, err error) (string, bool, error) {
	if err != nil {
		return "", false, err
	}
	if savedForLater {
		if toolName == "exit_plan_mode" {
			return ExitPlanModeSavedForLaterMessage, true, nil
		}
		return "user deferred this action", true, nil
	}
	if denied {
		if toolName == "exit_plan_mode" {
			return ExitPlanModeRefusalMessage, true, nil
		}
		return "denied by user", true, nil
	}
	return "", false, nil
}

func deniedResultForMultimodal(toolName string, denied, savedForLater bool, err error) (string, []adapter.ImageBlock, bool, error) {
	s, d, e := deniedResultFor(toolName, denied, savedForLater, err)
	return s, nil, d, e
}

// send is a context-aware channel send; if ctx is canceled while we're
// blocked on a slow consumer, we abort the turn instead of hanging forever.
func send(ctx context.Context, ch chan<- Event, ev Event) error {
	select {
	case ch <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isCancelErr reports whether err is a user-initiated context
// cancellation (or a deadline expiration, treated identically — both
// mean "the turn was cut, preserve history"). Distinguishing this from
// a real adapter / tool error lets the loop route to the interrupt
// path (synthetic tool_results + TurnInterrupted) instead of
// ErrorEvent.
func isCancelErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// countOrphanedToolResults counts how many of `calls` received a
// synthetic tool_result on the cancel path. Walks the tail of history
// matching by ToolCallID — the synthetic entries carry the same IDs as
// their parent tool_use blocks, with Content == interruptedToolResult.
// The UI uses the count to render "N tool calls cancelled" in the
// TurnInterrupted line.
func countOrphanedToolResults(history []adapter.Message, calls []adapter.ToolCall) int {
	if len(history) == 0 || len(calls) == 0 {
		return 0
	}
	// Build a set of IDs from the just-cancelled batch so we can scan
	// the history tail without an O(n*m) inner loop.
	wanted := make(map[string]struct{}, len(calls))
	for _, c := range calls {
		wanted[c.ID] = struct{}{}
	}
	orphans := 0
	// Walk back from the end — synthetic entries are the most recent
	// appends, real tool_result entries from earlier in the batch may
	// have a different content but the same ID space.
	for i := len(history) - 1; i >= 0 && len(wanted) > 0; i-- {
		m := history[i]
		if m.Role != adapter.RoleTool {
			continue
		}
		if _, ok := wanted[m.ToolCallID]; !ok {
			continue
		}
		delete(wanted, m.ToolCallID)
		if m.Content == interruptedToolResult {
			orphans++
		}
	}
	return orphans
}
