package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/permissions"
)

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
	// --dangerously-skip-permissions flag (mirroring Claude Code).
	// Skip every approval prompt, run silently. DANGEROUS —
	// model-emitted commands execute without a human in the loop.
	// Explicit `deny` rules in permissions.json still refuse the call
	// (bypass is "skip prompts," not "ignore my policy"). Use only
	// in trusted CI / scripted contexts.
	BypassPermissions bool
	Cwd               string
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
	// calls including the safety floor, and removes the iteration
	// cap entirely. Explicit Deny rules in permissions.json still
	// win. Intended for unattended long-running implementations
	// where the user has decided no further oversight is needed.
	// Mutually exclusive with AutoMode and PlanMode at the TUI layer.
	YoloMode *YoloModeState
}

type loopState struct {
	iteration int
	history   *[]adapter.Message
}

type toolExecResult struct {
	content string
	denied  bool
	err     error
}

const (
	continueReasonToolCalls       = "tool_calls"
	continueReasonTruncatedOutput = "truncated_output"
)

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
	// further still (no cap; MaxInt). Read once at turn start so a
	// mode toggle mid-turn doesn't change the budget mid-flight.
	effectiveCap := cfg.MaxIterations
	switch {
	case cfg.YoloMode.IsActive():
		effectiveCap = math.MaxInt
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

		final, err := streamIteration(ctx, cfg, *state.history, events)
		if err != nil {
			_ = send(ctx, events, ErrorEvent{Err: err})
			return err
		}

		*state.history = append(*state.history, *final)
		if err := send(ctx, events, AssistantMessage{Message: *final}); err != nil {
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
				_ = send(ctx, events, ErrorEvent{Err: err})
				return err
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
	stream := cfg.Adapter.ChatStream(ctx, msgs, tools)

	var final *adapter.Message
	for ev := range stream {
		switch ev.Kind {
		case adapter.EventReasoning:
			if err := send(ctx, events, ReasoningToken{Text: ev.Token}); err != nil {
				return nil, err
			}
		case adapter.EventTokenDelta:
			if err := send(ctx, events, ContentToken{Text: ev.Token}); err != nil {
				return nil, err
			}
		case adapter.EventStreamProgress:
			if err := send(ctx, events, StreamProgress{}); err != nil {
				return nil, err
			}
		case adapter.EventProviderTool:
			if err := send(ctx, events, ProviderToolCall{
				ToolName: ev.ProviderToolName,
				Phase:    ev.ProviderToolPhase,
				Detail:   ev.ProviderToolDetail,
			}); err != nil {
				return nil, err
			}
		case adapter.EventFallback:
			if err := send(ctx, events, Fallback{
				From:   ev.FallbackFrom,
				To:     ev.FallbackTo,
				Reason: ev.FallbackReason,
				Policy: ev.FallbackPolicy,
			}); err != nil {
				return nil, err
			}
		case adapter.EventDone:
			final = ev.Final
		case adapter.EventErr:
			return nil, ev.Err
		}
	}
	if final == nil {
		return nil, errors.New("agent: stream ended without a final message")
	}
	return final, nil
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
				return err
			}
			appendToolResults(history, calls[:batch], results)
			calls = calls[batch:]
			continue
		}
		tc := calls[0]
		result, denied, err := executeToolCall(ctx, cfg, tc, events, decisions)
		if err != nil {
			return err
		}
		*history = append(*history, adapter.Message{
			Role:       adapter.RoleTool,
			Content:    result,
			ToolCallID: tc.ID,
		})
		if denied {
			// Inform the model; it can choose to ask differently or give up.
		}
		calls = calls[1:]
	}
	return nil
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
	for i, tc := range calls {
		wg.Add(1)
		go func(i int, tc adapter.ToolCall) {
			defer wg.Done()
			result, denied, err := executeToolCall(ctx, cfg, tc, events, decisions)
			results[i] = toolExecResult{content: result, denied: denied, err: err}
			if err != nil {
				errCh <- err
			}
		}(i, tc)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return nil, err
	default:
		return results, nil
	}
}

func appendToolResults(history *[]adapter.Message, calls []adapter.ToolCall, results []toolExecResult) {
	for i, tc := range calls {
		*history = append(*history, adapter.Message{
			Role:       adapter.RoleTool,
			Content:    results[i].content,
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
) (string, bool, error) {
	tool, ok := cfg.Registry.Get(tc.Name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", tc.Name), false, nil
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
			return msg, true, nil
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
		return "denied by permissions.json deny rule", true, nil
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
			return "", false, err
		}
	case cfg.PlanMode.IsActive() && IsPlanFileWrite(tool.Name(), tc.ArgsJSON, cfg.PlanMode.PlanFile):
		if err := send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "plan-mode-allow",
		}); err != nil {
			return "", false, err
		}
	case cfg.AutoMode.IsActive() && !IsAutoModeSafetyFloor(tool.Name()):
		// Auto-mode auto-allow. User opted into batch implementation
		// after approving a plan (or via /auto); skip the per-tool
		// modal for everything except the safety floor (run_bash,
		// git_commit, git_checkpoint, rollback) which always prompt.
		if err := send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "auto-mode",
		}); err != nil {
			return "", false, err
		}
	default:
		switch verdict {
		case permissions.Allow:
			if err := send(ctx, events, ApprovalAuto{
				ToolName: tool.Name(), Preview: preview, Source: "permissions",
			}); err != nil {
				return "", false, err
			}
		case permissions.Ask:
			if denied, savedForLater, err := promptForApproval(ctx, cfg, tool, tc, preview, events, decisions); err != nil || denied || savedForLater {
				return deniedResultFor(tool.Name(), denied, savedForLater, err)
			}
		default:
			if tool.RequiresApproval(tc.ArgsJSON) {
				if cfg.BypassPermissions && tool.Name() != "exit_plan_mode" {
					// exit_plan_mode is the one tool whose "approval"
					// is the actual user signal, not a safety gate —
					// even --dangerously-skip-permissions must NOT skip
					// it. Without this carve-out, a yolo session would
					// silently exit plan mode without the user ever
					// seeing the plan.
					if err := send(ctx, events, ApprovalAuto{
						ToolName: tool.Name(), Preview: preview, Source: "bypass-permissions",
					}); err != nil {
						return "", false, err
					}
				} else {
					if denied, savedForLater, err := promptForApproval(ctx, cfg, tool, tc, preview, events, decisions); err != nil || denied || savedForLater {
						return deniedResultFor(tool.Name(), denied, savedForLater, err)
					}
				}
			}
		}
	}

	if err := send(ctx, events, ToolStart{ToolName: tool.Name(), Preview: preview, ArgsJSON: tc.ArgsJSON}); err != nil {
		return "", false, err
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
	out, err := tool.Execute(toolCtx, tc.ArgsJSON)
	if err != nil {
		msg := fmt.Sprintf("error: %v", err)
		_ = send(ctx, events, ToolResult{ToolName: tool.Name(), Output: msg, Errored: true})
		return msg, false, nil
	}
	_ = send(ctx, events, ToolResult{ToolName: tool.Name(), Output: out, Errored: false})
	if pa, ok := tool.(planAware); ok {
		if store := pa.PlanStore(); store != nil {
			_ = send(ctx, events, TodoUpdate{Todos: store.Snapshot()})
		}
	}
	return out, false, nil
}

// planAware is the optional capability marker for tools that maintain
// a PlanStore. After such a tool runs successfully the loop snapshots
// the store and emits a TodoUpdate event so consumers (TUI, oneshot)
// can render the new state. Keeping the integration behind an
// interface avoids string-matching on tool names.
type planAware interface {
	PlanStore() *PlanStore
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
		if rule, ok := permissions.DeriveAllowRule(tool.Name(), tc.ArgsJSON, cfg.Cwd); ok {
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
		// Other tools should never see SaveForLater — defensive default.
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
