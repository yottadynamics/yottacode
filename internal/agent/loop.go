package agent

import (
	"context"
	"errors"
	"fmt"
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
	// BypassPermissions is the renamed --yolo: skip every approval
	// prompt, run silently. DANGEROUS — model-emitted commands
	// execute without a human in the loop. Explicit `deny` rules in
	// permissions.json still refuse the call (bypass is "skip
	// prompts," not "ignore my policy"). Use only in trusted CI /
	// scripted contexts.
	BypassPermissions bool
	Cwd               string
	MaxIterations     int
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
	for state.iteration = 1; state.iteration <= cfg.MaxIterations; state.iteration++ {
		if err := send(ctx, events, IterationStart{
			Number: state.iteration,
			Max:    cfg.MaxIterations,
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
			if state.iteration == cfg.MaxIterations {
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
	if err := send(ctx, events, IterCap{Max: cfg.MaxIterations}); err != nil {
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
	tools := cfg.Registry.AsAdapterTools()
	stream := cfg.Adapter.ChatStream(ctx, history, tools)

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

	verdict := permissions.Default
	if cfg.Permissions != nil {
		verdict = cfg.Permissions.Evaluate(tool.Name(), tc.ArgsJSON)
	}

	switch verdict {
	case permissions.Deny:
		_ = send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "deny-rule",
		})
		return "denied by permissions.json deny rule", true, nil
	case permissions.Allow:
		if err := send(ctx, events, ApprovalAuto{
			ToolName: tool.Name(), Preview: preview, Source: "permissions",
		}); err != nil {
			return "", false, err
		}
	case permissions.Ask:
		if denied, err := promptForApproval(ctx, cfg, tool, tc, preview, events, decisions); err != nil || denied {
			return deniedResult(denied, err)
		}
	default:
		if tool.RequiresApproval(tc.ArgsJSON) {
			if cfg.BypassPermissions {
				if err := send(ctx, events, ApprovalAuto{
					ToolName: tool.Name(), Preview: preview, Source: "bypass-permissions",
				}); err != nil {
					return "", false, err
				}
			} else {
				if denied, err := promptForApproval(ctx, cfg, tool, tc, preview, events, decisions); err != nil || denied {
					return deniedResult(denied, err)
				}
			}
		}
	}

	if err := send(ctx, events, ToolStart{ToolName: tool.Name(), Preview: preview, ArgsJSON: tc.ArgsJSON}); err != nil {
		return "", false, err
	}
	out, err := tool.Execute(ctx, tc.ArgsJSON)
	if err != nil {
		msg := fmt.Sprintf("error: %v", err)
		_ = send(ctx, events, ToolResult{ToolName: tool.Name(), Output: msg, Errored: true})
		return msg, false, nil
	}
	_ = send(ctx, events, ToolResult{ToolName: tool.Name(), Output: out, Errored: false})
	return out, false, nil
}

// promptForApproval emits ApprovalNeeded and blocks on the user's
// decision. Returns (denied, err) — err is non-nil only on context
// cancellation; denied=true when the user picked No.
func promptForApproval(
	ctx context.Context,
	cfg LoopConfig,
	tool Tool,
	tc adapter.ToolCall,
	preview string,
	events chan<- Event,
	decisions <-chan Decision,
) (bool, error) {
	if err := send(ctx, events, ApprovalNeeded{
		ToolName: tool.Name(), Preview: preview, ArgsJSON: tc.ArgsJSON,
	}); err != nil {
		return false, err
	}
	var d Decision
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case d = <-decisions:
	}
	if d == Deny {
		return true, nil
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
	return false, nil
}

func deniedResult(denied bool, err error) (string, bool, error) {
	if err != nil {
		return "", false, err
	}
	if denied {
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
