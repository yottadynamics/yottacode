package agent

import (
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// Event is the union of things the agent loop emits while a turn runs.
// Consumers (REPL today, TUI next, `yottacode run` after that) type-switch
// on the concrete value.
//
// Channel ownership: the *caller* owns the events channel and is responsible
// for closing it; Turn never closes it. Use a buffered channel (~64) so the
// loop doesn't block when the consumer is briefly busy.
//
// Approval flow: when a tool requires approval and policy doesn't
// pre-approve (a matching allow rule in permissions.json, or
// --yolo), the loop emits ApprovalNeeded and
// blocks on a receive from the decisions channel. The consumer must
// reply with a Decision or cancel ctx.
type Event interface{ event() }

// IterationStart fires when a new model->tools->model loop iteration begins.
// Number is 1-based.
type IterationStart struct {
	Number int
	Max    int
}

// IterationContinue explains why the loop is going around again after an
// iteration completes.
type IterationContinue struct {
	Number    int
	Reason    string
	ToolCalls int
}

// ReasoningToken carries one chunk of "thinking" output from a reasoning
// model (Qwen 3, DeepSeek R1). Render dimmed.
type ReasoningToken struct{ Text string }

// ContentToken carries one chunk of the assistant's actual reply. Render
// in normal style.
type ContentToken struct{ Text string }

// StreamProgress is a heartbeat for adapter-side stream activity that
// has no visible text — currently emitted by the OpenAI Responses
// adapter on each `function_call_arguments.delta`. The TUI uses this
// to keep the live "tok/s" indicator moving on turns that produce
// only a tool call (no reasoning summary, no body text) so the row
// doesn't sit at "0.0 tok/s" the whole time.
type StreamProgress struct{}

// ProviderToolCall carries a provider-native tool lifecycle update emitted by
// the adapter stream itself, e.g. OpenAI/xAI web search or code interpreter.
type ProviderToolCall struct {
	ToolName string
	Phase    string
	Detail   string
}

// ApprovalAuto is logged when the loop auto-approves (or auto-denies)
// a tool call without asking the user. Source identifies which gate
// fired: "permissions" (matched an allow rule), "deny-rule" (matched
// a deny rule, no execution), or "bypass-permissions"
// (--yolo flag is set and no rule matched).
type ApprovalAuto struct {
	ToolName string
	Preview  string
	Source   string
}

// ApprovalNeeded is the request half of an approval round-trip. The loop
// blocks on the decisions channel until the consumer replies. ArgsJSON is
// the raw tool args so the consumer can render richer previews — e.g. the
// TUI shows a colored diff for edit_file by parsing old_string/new_string.
type ApprovalNeeded struct {
	ToolName string
	Preview  string
	ArgsJSON string
}

// PathTrustElevationNeeded fires when a mutating tool's
// ValidateWritePath rejects a target as outside the workspace
// (Cwd + AllowedPaths). The TUI catches this via its existing
// decisions channel and renders the inline path-trust elevation
// modal — see yottacode-roadmap/folder-trust.md "Prompt 2."
//
// The loop blocks on the decisions channel exactly like
// ApprovalNeeded. Expected replies:
//
//   - PathAllowOnce:    consumer added Path to the session allow
//     list; loop re-runs Execute once.
//   - PathTrustSession: consumer added filepath.Dir(Path) to the
//     session allow list; loop re-runs Execute
//     once.
//   - Deny:             loop surfaces the structured error to the
//     model as the tool result (Claude-style
//     per-tool deny: descriptive + with a
//     recovery hint).
//
// Cwd and AllowedRoots are echoed so the modal can render the
// existing trust state next to the new request without re-walking
// LoopConfig.
type PathTrustElevationNeeded struct {
	ToolName     string
	Path         string
	Cwd          string
	AllowedRoots []string
	ArgsJSON     string
}

// ToolStart fires immediately before a tool's Execute is called (after any
// approval flow has resolved). The consumer can render this as a status line.
// ArgsJSON carries the raw tool-call arguments so consumers can do structured
// rendering (e.g., the TUI's edit_file diff card) without parsing the
// human-friendly Preview string.
type ToolStart struct {
	ToolName string
	Preview  string
	ArgsJSON string
}

// ToolResult fires after the tool finishes. Output is the string the model
// will see; Errored signals whether it was a tool-level error vs. success.
type ToolResult struct {
	ToolName string
	Output   string
	Errored  bool
}

// CwdChanged fires when a tool (today: enter_worktree / exit_worktree)
// swapped the session's working directory mid-conversation. The TUI
// refreshes its status-line worktree chip and any cwd-derived display
// state; oneshot ignores it. Emitted by the loop right after the
// tool's ToolResult when LoopConfig.Cwd.Get() differs from the
// pre-call value.
type CwdChanged struct {
	NewCwd string
}

// TodoUpdate fires after a tool implementing the planAware interface
// (TodoWriteTool today) finishes, carrying the new full snapshot of
// the working plan. The TUI renders this as a scrollback card showing
// the current list with status markers; oneshot prints a one-liner on
// stderr. The slice is a copy — consumers may retain it.
type TodoUpdate struct{ Todos []Todo }

// AssistantMessage fires once a streamed assistant response is finalized,
// just before its tool_calls (if any) are dispatched. Includes the same
// Message that gets appended to history; useful for consumers that want to
// react to a complete reply (e.g., TUI message-list rendering).
type AssistantMessage struct{ Message adapter.Message }

// IterCap fires when the loop hits MaxIterations without a final assistant
// reply. The turn ends after this event.
type IterCap struct{ Max int }

// ErrorEvent fires when the turn terminates because of an error (adapter
// failure, ctx cancel, etc.). The error is also returned by Turn.
type ErrorEvent struct{ Err error }

// CheckpointInfo carries a non-fatal status from the checkpoint
// subsystem — typically a snapshot failure for one file (permission
// denied, race with deletion) that should NOT abort the user's tool
// call. The TUI renders these dimly in the scrollback so the user
// knows the file won't be restorable from this checkpoint without
// confusing them about whether the tool itself failed.
type CheckpointInfo struct{ Message string }

// TurnDone fires when the turn completes cleanly (no more tool calls, no
// error, not at iter cap). The consumer can re-enable input.
type TurnDone struct{}

// TurnInterrupted fires when the turn ended via user-initiated context
// cancellation (Enter or Esc/Ctrl+C mid-turn) rather than an error or a
// clean finish. By the time this event lands, the loop has already
// preserved history correctness: any tokens that streamed before the
// cancel are appended as a content-only assistant message, and any
// in-flight or pending tool_calls in the current batch get synthetic
// "interrupted by user" tool_result entries so no tool_use is left
// orphaned for the next request. Consumers should render this as a
// calm marker, not an error — the turn was cut on purpose.
type TurnInterrupted struct {
	// PartialContent is the assistant text that streamed before the
	// cancel, already appended to history. Carried in the event so the
	// TUI can render a one-line snippet without re-walking history.
	PartialContent string
	// OrphanedCalls counts tool_use entries in the just-cancelled batch
	// that received synthetic results (i.e. were never actually run or
	// did not produce real output). Zero when the cancel landed mid-
	// stream before any tool call started.
	OrphanedCalls int
}

// Fallback fires when the multi-provider router falls through from one
// candidate to another after an early failure (an error before any
// tokens streamed). Carries enough metadata for the TUI to render a
// loud "↻ fallback: A → B (reason)" line — silent fallback is the
// failure mode the router design is built to avoid.
type Fallback struct {
	From   string
	To     string
	Reason string
	Policy string
	// Agent names the context the fallback happened in — a subagent type
	// (e.g. "Explore") or "summarize" — so the TUI can distinguish a
	// delegated/summarization fallover from a main-thread one. Empty for
	// the main conversation.
	Agent string
}

// SubagentStart fires when the Agent tool begins a child Turn. The
// parent's TUI/oneshot renders this as a short header so the user can
// see that delegation is happening; the child's transcript file at
// TranscriptPath captures everything that happens inside the child.
type SubagentStart struct {
	TaskID         string
	AgentType      string
	Prompt         string
	Background     bool
	TranscriptPath string
}

// SubagentProgress is the parent-visible activity stream for a running
// subagent — typically "Explore: read_file internal/foo.go" or "Plan:
// grep TODO". The child's raw ContentToken/ReasoningToken events are
// deliberately NOT forwarded; only high-level tool-level activity
// reaches the parent, which keeps the parent's UI uncluttered AND keeps
// the child's reasoning out of the parent's adapter context.
type SubagentProgress struct {
	TaskID    string
	AgentType string
	Activity  string
}

// SubagentDone fires when a foreground subagent completes. The Result
// is the child's final assistant message — that same string is also
// returned synchronously from the Agent tool's Execute, so the parent's
// model receives it as a normal tool result. SubagentDone is for the
// UI side of the picture: it lets the TUI close the "subagent running"
// card and oneshot print a final status line.
type SubagentDone struct {
	TaskID     string
	AgentType  string
	Result     string
	Errored    bool
	Duration   time.Duration
	TokensUsed int
	ToolCalls  int    // child's tool-call count, for inline stats rendering
	Model      string // model the child ran on when task-routed; "" = inherited the parent's model
}

// SubagentBackgroundDone fires asynchronously when a background subagent
// completes after the parent turn has already ended. The TUI surfaces
// this as a card on the next idle redraw / via /subagents list. Oneshot
// rejects background invocations entirely so this event is unreachable
// from non-interactive contexts.
type SubagentBackgroundDone struct {
	TaskID     string
	AgentType  string
	Result     string
	Errored    bool
	Duration   time.Duration
	TokensUsed int
	ToolCalls  int    // child's tool-call count, for inline stats rendering
	Model      string // model the child ran on when task-routed; "" = inherited the parent's model
}

func (ReasoningToken) event()           {}
func (ContentToken) event()             {}
func (StreamProgress) event()           {}
func (ProviderToolCall) event()         {}
func (IterationStart) event()           {}
func (IterationContinue) event()        {}
func (ApprovalAuto) event()             {}
func (ApprovalNeeded) event()           {}
func (PathTrustElevationNeeded) event() {}
func (ToolStart) event()                {}
func (ToolResult) event()               {}
func (CwdChanged) event()               {}
func (TodoUpdate) event()               {}
func (AssistantMessage) event()         {}
func (IterCap) event()                  {}
func (ErrorEvent) event()               {}
func (CheckpointInfo) event()           {}
func (TurnDone) event()                 {}
func (TurnInterrupted) event()          {}
func (Fallback) event()                 {}

func (SubagentStart) event()          {}
func (SubagentProgress) event()       {}
func (SubagentDone) event()           {}
func (SubagentBackgroundDone) event() {}
func (UserMessageAppended) event()    {}

// UserMessageAppended fires when a user message queued via UserMessages
// is injected into history between tool rounds. The TUI renders a
// "[delivered]" receipt so the user knows the model will see it on the
// next iteration.
type UserMessageAppended struct{ Content string }
