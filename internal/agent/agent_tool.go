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
// around. 8 is a round number that matches what most users informally
// do — enough for genuine parallelism, low enough to keep API spend
// bounded if a model gets enthusiastic.
const MaxBackgroundSubagents = 8

// MaxForegroundSubagents caps how many foreground subagents may be
// running concurrently per session. Foreground spawns are no longer
// serialized (AgentTool.ParallelSafe returns true), so a parent that
// emits N Agent calls in one assistant message fans them out via the
// loop's parallel-batch path. The cap matches the background ceiling
// because the cost profile is the same: every concurrent child holds
// a goroutine, an iteration budget, and a slice of the parent's
// provider rate limit. The (N+1)th call returns a recoverable error
// the model can react to by waiting on the in-flight children before
// dispatching more.
const MaxForegroundSubagents = 8

// childChildIterationCap is the iteration budget every child subagent
// runs under. We deliberately do NOT apply the auto-mode 4× multiplier
// or the yolo uncapped path: subagent runs should bound themselves,
// even when the parent session is in auto/yolo. The user opted into
// "let the parent run unattended" — they did not opt into "let the
// parent spawn unbounded child loops." The cap is still high enough
// for read-heavy workflows like /code-review, where an iter-cap wastes
// the child's tokens and drops review coverage.
const childIterationCap = 100

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
	// concurrency-safe by construction. This is the active session model
	// that a child inherits when nothing routes it elsewhere.
	Adapter Streamer

	// ImplementerAdapter is the fast coding model's streamer used for
	// cache-safe task routing. nil when routing is disabled. In auto mode,
	// delegated subagents default here; summarization also uses this role.
	ImplementerAdapter Streamer

	// ImplementerModel is recorded on task events so /subagents can show
	// which model handled a delegation. Empty when routing is off.
	ImplementerModel string

	// AdvisorAdapter is the reasoning/planning model's streamer. The main
	// session uses it at routed startup and in plan mode, while implementer
	// children can call consult_advisor for isolated help.
	AdvisorAdapter Streamer

	// AdvisorModel is the advisor model's name for task/tool display.
	AdvisorModel string

	// Fast*/Smart* are legacy aliases retained while TUI/oneshot call sites
	// migrate. Fast maps to implementer; smart maps to advisor.
	FastAdapter  Streamer
	FastModel    string
	SmartAdapter Streamer
	SmartModel   string

	// RouteAuto enables the heuristic that routes delegated subagents to
	// ImplementerAdapter. False in "manual" mode (only an explicit `model:`
	// frontmatter routes) and when routing is off.
	RouteAuto bool

	// ModelResolver resolves an agent's explicit `model:` frontmatter
	// to a streamer, or returns nil when the name matches no
	// configured model (the child then inherits Adapter). nil when
	// routing is disabled.
	ModelResolver func(model string) Streamer

	// ResolveWindow returns the context window (tokens) for a child
	// model, honoring the per-model override + default_window exactly
	// as the status bar does (contextwindow.EffectiveWindow). It is the
	// source of the child loop's compaction window: subagents run Turn
	// directly, so without this they have no context management and a
	// long run accumulates until the provider rejects it. The TUI and
	// oneshot both wire it; nil (tests) disables child compaction.
	ResolveWindow func(model string) int

	// ParentRegistry is the live tool set the parent session is using.
	// We clone it for the child, dropping the Agent tool itself plus
	// the plan-mode boundary tools (enter/exit_plan_mode), and
	// intersecting with the agent's `tools:` allowlist when one is
	// configured. The clone is a single-pass
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

	// TranscriptDir is the directory subagent transcripts get persisted
	// under, resolved at startup by the caller (TUI or oneshot wiring)
	// via subagents.TranscriptDirFor. It need NOT exist yet — openTranscript
	// MkdirAlls it lazily on the first dispatch, so a session that never
	// runs a subagent leaves no empty project-memory dir behind.
	TranscriptDir string

	// AllowBackground controls whether `run_in_background: true` is
	// honored. The TUI sets this true; oneshot leaves it false so the
	// non-interactive entry point returns a sensible error string the
	// model can recover from rather than silently detaching work that
	// nobody will see.
	AllowBackground bool

	// MaxSessionTokens caps the cumulative ESTIMATED tokens spent across
	// all subagent runs this session. A new spawn is rejected (with a
	// recoverable error the model relays) once the registry's
	// TotalTokensUsed reaches this ceiling — the session-wide backstop the
	// per-child iteration cap and the concurrency cap can't provide. 0
	// disables the budget (unbounded). Wired from
	// config.SubagentSessionTokenBudget by the TUI/oneshot setup.
	MaxSessionTokens int

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
			"notify_on_done": map[string]any{
				"type":        "boolean",
				"description": "Background only. If true, you are automatically re-prompted with the subagent's result when it finishes (even after this turn ends) so you can act on it — true fire-and-forget delegation. If false (default), the result is silent until you fetch it with get_subagent_result. Do NOT set this when you intend to collect the result yourself with get_subagent_result in the same turn (e.g. a fan-out you immediately wait on) — that would double up. Ignored for foreground.",
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

// ParallelSafe returns true so several Agent calls from the same
// assistant message run concurrently — matching the user-visible
// pattern in other Claude-Code-style frontends where a parent spawns
// three Explore agents in one turn and they fan out at once. The
// loop's executeToolCallsParallel handles the fan-out; each child
// runs in its own goroutine with its own loop + provider call.
//
// The cost the older sequential design avoided is real but
// manageable: N concurrent children share the parent's provider key,
// so a thundering herd can hit rate limits and burn iteration
// budget on dead-end investigations. Mitigations live elsewhere —
// MaxBackgroundSubagents caps background fan-out, and per-tool
// approval still gates write-y children. The model is generally
// conservative about how many it spawns at once (typically 2–4
// Explores), so an explicit numeric cap on foreground concurrency
// would mostly punish the rare legitimate burst.
func (t *AgentTool) ParallelSafe(string) bool { return true }

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
	NotifyOnDone    bool   `json:"notify_on_done"`
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
	// An agent definition can declare `background: true` so callers
	// don't have to remember the flag for safe read-only background agents.
	// Write-capable builtins keep this frontmatter for dispatch fan-out, but
	// standalone Agent calls do not auto-background them: ordinary GA background
	// delegation is read-only by default, while write-capable unattended work
	// belongs to dispatch's worktree/file-scoped worker path.
	if !a.RunInBackground && cfg.Background && t.AllowBackground && agentIsReadOnly(cfg) {
		a.RunInBackground = true
	}
	if a.RunInBackground && !t.AllowBackground {
		// Oneshot/noninteractive sessions have no long-lived UI, dock, or
		// /subagents picker to host detached work. The model can retry the same
		// task as a foreground subagent, which always works.
		return "error: background subagents are only available in interactive TUI sessions. " +
			"Re-run without run_in_background:true to dispatch a foreground subagent (which always works).", nil
	}
	// Session-wide token budget: a backstop against an enthusiastic or
	// adversarial prompt fanning out unbounded child loops on the user's
	// key. The per-child iteration cap and the concurrency cap bound one
	// wave; this bounds the session total. Estimated tokens are recorded at
	// MarkDone, so this counts COMPLETED spend (running children contribute
	// 0 until they finish) — which combined with the concurrency cap keeps
	// the overshoot to at most one in-flight wave. Recoverable error the
	// model relays so the user can raise the budget or wrap up.
	if t.MaxSessionTokens > 0 {
		if used := t.Tasks.TotalTokensUsed(); used >= t.MaxSessionTokens {
			return fmt.Sprintf("error: this session's subagent token budget (~%d estimated tokens) is exhausted (used ~%d); wait for nothing in particular — the budget is cumulative for the session. Raise it with `[subagents]` `session_token_budget = N` in ~/.yottacode/config.toml, or finish the remaining work without further delegation.",
				t.MaxSessionTokens, used), nil
		}
	}

	// The concurrency cap is enforced atomically at reservation time
	// (t.Tasks.TryReserve below), NOT with a check-then-Add here: AgentTool
	// is ParallelSafe, so a parent that emits N Agent calls in one assistant
	// message runs N Execute goroutines concurrently, and a separate
	// check-then-Add would let all N observe "under cap" before any inserted
	// and overshoot the ceiling. Build the task first, then reserve-or-reject
	// in one locked step.
	taskID := subagents.NewTaskID()
	transcriptPath := filepath.Join(t.TranscriptDir, fmt.Sprintf("%s-%s.md", cfg.Name, taskID))

	// Cache-safe routing: pick the child's model before it runs. The
	// child has its own isolated context, so swapping it onto the fast
	// model never touches the parent's prompt cache.
	childAdapter, childModel := t.routeChildModel(cfg)

	task := &subagents.Task{
		ID:             taskID,
		AgentType:      cfg.Name,
		Prompt:         a.Prompt,
		Started:        time.Now(),
		Status:         subagents.TaskRunning,
		Background:     a.RunInBackground,
		NotifyOnDone:   a.RunInBackground && a.NotifyOnDone,
		TranscriptPath: transcriptPath,
		Model:          childModel,
	}
	// Atomic reserve-or-reject: insert the task only if its class is under
	// cap, counting + inserting under one registry lock. Background bounds
	// TOTAL running concurrency (countForegroundOnly=false, matching the old
	// ActiveCount check); foreground bounds only foreground running tasks.
	if a.RunInBackground {
		if !t.Tasks.TryReserve(task, MaxBackgroundSubagents, false) {
			return fmt.Sprintf("error: at most %d background subagents may run concurrently (current: %d); wait for one to finish or stop it with /subagents stop <id>",
				MaxBackgroundSubagents, t.Tasks.ActiveCount()), nil
		}
	} else {
		if !t.Tasks.TryReserve(task, MaxForegroundSubagents, true) {
			return fmt.Sprintf("error: at most %d foreground subagents may run concurrently (current: %d); wait for one of the in-flight subagents to finish before dispatching more",
				MaxForegroundSubagents, t.Tasks.ActiveForegroundCount()), nil
		}
	}

	parentEvents := ParentEvents(ctx)
	parentDecisions := ParentDecisions(ctx)
	emitToParent := func(ev Event) { forwardToParent(ctx, parentEvents, ev) }

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
			// This is a bare detached goroutine: a panic anywhere in its
			// orchestration AROUND runChild — MarkDone, the registry read,
			// fireBackgroundDone, or the onBackgroundDone callback that runs
			// TUI code — has no parent frame to catch it and would crash the
			// whole interactive session. (runChild has its own internal
			// recovers for the child turn + drain loop; this guards the rest.)
			// Degrade to an errored terminal task so the slot frees and the
			// dock stops showing it running — but only if the run hadn't
			// already reached a terminal state, so a panic in the post-run
			// notification can't clobber a result the child actually produced.
			defer func() {
				if r := recover(); r != nil {
					err := panicToError("background subagent "+cfg.Name+" goroutine", r)
					if snap, ok := t.Tasks.Get(taskID); ok && snap.Status == subagents.TaskRunning {
						t.Tasks.MarkDone(taskID, subagents.TaskErrored, "error: "+err.Error(), true, 0)
					}
				}
			}()
			// Background: forward informational progress (SubagentStart
			// already fired above; the ticks below stream into the same
			// inline card + the dock) so a background subagent reads like
			// a foreground one WHILE its spawning turn is still active.
			// Once that turn ends the TUI CLOSES this per-turn event
			// channel, but the detached child (on context.Background())
			// keeps emitting — forwardToParent routes those ticks through
			// trySend, which drops on a closed/full channel instead of
			// panicking, so a post-turn tick is a no-op rather than a crash
			// that the runChild recover would turn into a failed run. The
			// dock keeps updating from the registry's activity ring either
			// way. decisions stays nil: with no one watching, approval-needed
			// events still auto-deny inside runChild (the forward path
			// requires BOTH emitToParent and decisions).
			result, errored, status, tokens := t.runChild(bgCtx, taskID, cfg, a.Prompt, transcript, emitToParent, nil, childAdapter, childModel, childRunOpts{standaloneBackgroundPolicy: true})
			t.Tasks.MarkDone(taskID, status, result, errored, tokens)
			// Read the just-recorded tool-call count off the registry
			// so the inline card can render accurate stats.
			tokensUsed, toolCalls := doneTokensAndCalls(t.Tasks, taskID, tokens)
			t.fireBackgroundDone(SubagentBackgroundDone{
				TaskID:       taskID,
				AgentType:    cfg.Name,
				Result:       result,
				Errored:      errored,
				Duration:     time.Since(task.Started),
				TokensUsed:   tokensUsed,
				ToolCalls:    toolCalls,
				Model:        childModel,
				NotifyOnDone: a.RunInBackground && a.NotifyOnDone,
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
	result, errored, status, tokens := t.runChild(childCtx, taskID, cfg, a.Prompt, transcript, emitToParent, parentDecisions, childAdapter, childModel, childRunOpts{})
	t.Tasks.MarkDone(taskID, status, result, errored, tokens)
	// Read the just-recorded tool-call count off the registry so the
	// done card can render accurate stats.
	tokensUsed, toolCalls := doneTokensAndCalls(t.Tasks, taskID, tokens)
	emitToParent(SubagentDone{
		TaskID:     taskID,
		AgentType:  cfg.Name,
		Result:     result,
		Errored:    errored,
		Duration:   time.Since(task.Started),
		TokensUsed: tokensUsed,
		ToolCalls:  toolCalls,
		Model:      childModel,
	})
	if errored {
		return result, nil // returned as a tool result so the model sees the failure
	}
	return result, nil
}

// readOnlyChildTools is the set of tools that cannot mutate the user's
// workspace or execute arbitrary commands. It is used only for execution
// posture (safe automatic backgrounding and dispatch worktree decisions),
// not for model routing: auto routing sends every delegated subagent to the
// implementer model. The advisor is reached explicitly through consult_advisor.
var readOnlyChildTools = map[string]bool{
	"read_file":                     true,
	"read_many_files":               true,
	"grep":                          true,
	"glob":                          true,
	"list_dir":                      true,
	"list_project_structure":        true,
	"git_log_file":                  true,
	"git_blame_lines":               true,
	"git_diff_files":                true,
	"git_show_file_at_rev":          true,
	"git_branch_status":             true,
	"list_git_changed_files":        true,
	"git_merge_base":                true,
	"fetch_url":                     true,
	"todo_write":                    true,
	"session_recall":                true,
	"memory_search":                 true,
	"lsp_status":                    true,
	"lsp_symbols":                   true,
	"lsp_document_symbols":          true,
	"lsp_document_highlights":       true,
	"lsp_selection_ranges":          true,
	"lsp_definition":                true,
	"lsp_type_definition":           true,
	"lsp_implementation":            true,
	"lsp_references":                true,
	"lsp_diagnostics":               true,
	"lsp_changed_files_diagnostics": true,
	"lsp_hover":                     true,
	"lsp_signature_help":            true,
	"lsp_code_actions":              true,
	"lsp_code_action_preview":       true,
	"lsp_rename_preview":            true,
	"lsp_format_preview":            true,
	"lsp_call_hierarchy":            true,
	"lsp_impact":                    true,
	ConsultAdvisorToolName:          true,
}

// IsReadOnlyTool reports whether a tool name is in the canonical read-only
// set — tools that cannot mutate the user's workspace or execute arbitrary
// commands. Used by the /usage efficiency section to distinguish verification-
// loop re-calls (same args after a mutation) from genuine idle duplicates.
func IsReadOnlyTool(name string) bool {
	return readOnlyChildTools[name]
}

// agentIsReadOnly reports whether an agent definition is restricted to the
// read-only tool set. A wildcard / inherit-all allowlist (empty Tools) is not
// read-only because it can reach the full registry.
func agentIsReadOnly(cfg *subagents.AgentConfig) bool {
	if cfg == nil || len(cfg.Tools) == 0 {
		return false
	}
	for _, name := range cfg.Tools {
		if !readOnlyChildTools[name] {
			return false
		}
	}
	return true
}

// routeChildModel picks the streamer + model name for a child subagent.
// Routing only ever touches isolated child contexts — never the main-thread
// model mid-conversation — so it never invalidates the parent's prompt cache.
// Precedence:
//  1. an explicit `model:` frontmatter resolves via ModelResolver (manual or
//     auto mode); an unresolvable name falls through so a typo degrades
//     gracefully rather than erroring;
//  2. auto mode: every delegated subagent → the implementer model;
//  3. otherwise (manual mode without an explicit model, or routing off)
//     inherit the parent's active adapter.
//
// The returned modelName is "" for the inherit case (the parent's model
// name lives in the TUI, not here); callers treat "" as "default".
func (t *AgentTool) routeChildModel(cfg *subagents.AgentConfig) (Streamer, string) {
	if m := strings.TrimSpace(cfg.Model); m != "" && t.ModelResolver != nil {
		if s := t.ModelResolver(m); s != nil {
			return s, m
		}
	}
	implementer := t.ImplementerAdapter
	implementerModel := t.ImplementerModel
	if implementer == nil {
		implementer = t.FastAdapter
		implementerModel = t.FastModel
	}
	if implementer == nil {
		implementer = t.SmartAdapter
		implementerModel = t.SmartModel
	}
	if t.RouteAuto && implementer != nil {
		return implementer, implementerModel
	}
	return t.Adapter, ""
}

// runChild assembles the child LoopConfig + history, runs agent.Turn,
// translates its events through the in-process translator, and
// captures the final assistant content for return to the parent.
// emitToParent forwards informational progress (SubagentProgress) to
// the parent's live event stream; it is non-nil for both foreground
// and background runs (background passes a non-blocking forwarder so a
// detached child can't stall — see the spawn site). The transcript
// captures everything regardless.
//
// Approval policy is foreground/background dependent and keys on the
// DECISIONS channel, not on emitToParent:
//   - Foreground (emitToParent != nil AND parentDecisions != nil): a
//     child's ApprovalNeeded forwards through to the parent's modal
//     so the user can answer it. The verdict routes back to the
//     child's own decisions channel. The Preview is prefixed with
//     "[subagent:<type>]" so the user knows which agent wants what.
//   - Background (parentDecisions == nil): auto-deny with a steering
//     message. Nobody's actively watching — surfacing a modal hours
//     after spawn-time is bad UX. Progress still streams (emitToParent
//     is set) so the inline card + dock stay live while the spawning
//     turn is active. The escape valve is permissions.json (allowlist
//     the tool the subagent needs).
//
// childRunOpts carries the optional overrides dispatch needs without
// turning runChild into a positional-argument soup. The zero value
// reproduces the standard Agent-tool behavior (shared cwd, registry built
// from the agent config, no extra prompt), so existing call sites pass
// childRunOpts{}.
type childRunOpts struct {
	// reg, when non-nil, is the child's tool registry. nil → built from
	// the agent config via buildChildRegistry (the shared-cwd path).
	// dispatch passes a worktree-isolated registry here.
	reg *Registry
	// cwd, when non-nil, is the child loop's working directory. nil →
	// the parent's shared CwdRef (t.Cwd). dispatch passes a fresh ref
	// pinned to the child's git worktree.
	cwd *CwdRef
	// extraSystemPrompt is appended to the child's system prompt (after
	// the agent body + SystemPromptSuffix, before the no-delegation
	// rule). dispatch uses it to declare the task's file ownership.
	extraSystemPrompt string
	// bgPolicy makes a dispatch background worker apply a deterministic
	// allow/deny policy: owned-file writes are allowed; tests, shell, and
	// approval-requiring git/network tools are denied. The child does NOT
	// bypass permissions — explicit deny rules still win, the run_bash
	// hardline floor still applies, and writes stay confined to the worktree
	// by the child registry's WriteOpts.
	bgPolicy bool
	// sandboxed reports whether this dispatch write worker's mutating
	// shell/test calls are routed through its own per-worker Sandbox (see
	// DispatchTool.SandboxFactory) rather than the host. Only set by
	// dispatch write children — read-only dispatch children share the
	// parent's own registry/Sandbox directly and leave this false. Threads
	// into dispatchBackgroundApprovalPolicy so an unattended (background)
	// worker's run_bash/run_tests calls are allowed exactly when a
	// container bounds their blast radius, denied on the host fallback.
	sandboxed bool
	// standaloneBackgroundPolicy makes ordinary Agent(run_in_background:true)
	// runs deny approval-requiring tools before parent auto/yolo modes can approve
	// them. Read-only/no-approval tools still execute normally. Dispatch
	// background workers use bgPolicy instead; foreground runs leave both false.
	standaloneBackgroundPolicy bool
}

func (t *AgentTool) runChild(
	ctx context.Context,
	taskID string,
	cfg *subagents.AgentConfig,
	userPrompt string,
	transcript *transcriptFile,
	emitToParent func(Event),
	parentDecisions <-chan Decision,
	childAdapter Streamer,
	childModel string,
	opts childRunOpts,
) (result string, errored bool, status subagents.TaskStatus, tokensUsed int) {
	// A panic anywhere in the child's orchestration (drain loop, approval
	// forwarding) must not crash the parent's interactive session — degrade
	// it to an errored subagent the model/dock can see. Tool panics inside
	// the child's own turn are already caught at executeToolCall; the child
	// Turn goroutine below has its own recover for panics in its loop logic.
	defer func() {
		if r := recover(); r != nil {
			result = "error: " + panicToError("subagent "+cfg.Name, r).Error()
			errored, status = true, subagents.TaskErrored
		}
	}()
	childReg := opts.reg
	if childReg == nil {
		childReg = t.buildChildRegistry(cfg)
	}
	if opts.bgPolicy || opts.standaloneBackgroundPolicy {
		stripUnattendedProcessTools(childReg)
	}

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
	// Child cwd: the parent's shared CwdRef by default (so an
	// enter_worktree mid-conversation propagates), OR a dispatch-supplied
	// ref pinned to this child's isolated git worktree.
	childCwd := opts.cwd
	if childCwd == nil {
		childCwd = t.Cwd
	}
	childCfg := LoopConfig{
		Adapter:           childAdapter,
		Registry:          childReg,
		Permissions:       t.Permissions,
		BypassPermissions: false, // children never blanket-bypass; background workers gate via BackgroundApprovalPolicy (see below), and the run_bash hardline floor + worktree write-confinement still apply
		Cwd:               childCwd,
		MaxIterations:     childIterationCap,
		// Pin the child's iteration budget to childIterationCap. The
		// child inherits the parent's AutoMode/YoloMode pointers for
		// approval behavior, but FixedIterationCap stops loop.go from
		// applying the 4×/yolo multiplier to the child's budget — see
		// the childIterationCap doc comment for why children must stay
		// bounded even under an auto/yolo parent.
		FixedIterationCap: true,
		PlanMode:          childPlanMode,
		AutoMode:          childAutoMode,
		YoloMode:          t.YoloMode, // shared (process-wide once entered)
	}
	if opts.bgPolicy {
		childCfg.BackgroundApprovalPolicy = dispatchBackgroundApprovalPolicyFor(opts.sandboxed)
	} else if opts.standaloneBackgroundPolicy {
		childCfg.BackgroundApprovalPolicy = standaloneBackgroundApprovalPolicy
	}

	// Give the child loop its own context management. A subagent runs
	// Turn directly — it never passes through the TUI's turn-boundary
	// summarizer — so without this a long run (e.g. a multi-round
	// codebase audit) accumulates its isolated history until the
	// provider rejects the request and the work is lost. When we can
	// resolve the child model's window, enable in-loop compaction at a
	// threshold below the window so the summary call still has input
	// headroom. The progress summary runs on FastAdapter when routing is
	// on — it's a mechanical compression, not reasoning, so the cheap
	// model is the right tool and avoids disturbing the child model's
	// cache prefix; nil falls back to the child's own adapter.
	//
	// Only enable below minCompactionWindow: under a too-small window the
	// input budget (window − output reservation − prompt) goes non-positive
	// and there's no room to summarize anything, so compaction can't help
	// — let such a (rare, barely-agentic) tiny-window model degrade to the
	// provider's own limit instead.
	if t.ResolveWindow != nil {
		if w := t.ResolveWindow(childModel); w > 0 {
			// Resolve the summarizer's own window so the summary call's input
			// is budgeted to whichever window is smaller (see CompactionConfig
			// .SummarizerWindow). 0 = "same as child window" (no FastAdapter,
			// or the fast model's window is unknown — leave it to the child).
			summarizerWindow := 0
			if t.FastAdapter != nil && t.FastModel != "" {
				summarizerWindow = t.ResolveWindow(t.FastModel)
			}
			childCfg.Compaction = newChildCompaction(w, t.FastAdapter, summarizerWindow)
		}
	}

	systemPrompt := strings.TrimSpace(cfg.Prompt)
	if t.SystemPromptSuffix != "" {
		systemPrompt += "\n\n" + t.SystemPromptSuffix
	}
	if opts.extraSystemPrompt != "" {
		systemPrompt += "\n\n" + opts.extraSystemPrompt
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

	// Run the child's own loop with the parent batch's approval gate DETACHED.
	// Only this drain loop (the forwarder) may hold that gate during a
	// forwarded approval; if the child loop also acquired it, the child would
	// block on its decision while we block on the gate. See withoutApprovalGate.
	turnCtx := withoutApprovalGate(ctx)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Without this, a panic in the child turn crashes the whole
				// process AND leaves childEvents unclosed, hanging the drain
				// loop below forever. Close the channel and report the panic
				// as the turn error so the parent unblocks cleanly.
				close(childEvents)
				errCh <- panicToError("subagent "+cfg.Name+" turn", r)
			}
		}()
		err := Turn(turnCtx, childCfg, &history, childEvents, childDecisions)
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
			// Capture this turn's exact provider usage onto the task, the
			// same way the main TUI loop accumulates it on the session. This
			// is the per-subagent token tally the dock and /usage read; it's
			// live (updated each turn), unlike the ~4-char/token estimate
			// computed once at the end. AddUsage is nil-safe.
			t.Tasks.AddUsage(taskID, e.Message.Usage)
			if len(e.Message.ToolCalls) == 0 && strings.TrimSpace(e.Message.Content) != "" {
				final = e.Message.Content
			}
		case ToolStart:
			// The Preview already starts with the tool name (e.g.
			// "grep(pattern in path)"), so prefixing with ToolName
			// would just duplicate the verb. Use the preview directly.
			toolCallCount++
			emitActivity(truncate(e.Preview, 96))
		case Fallback:
			// The subagent's model chain (smart_models) failed over.
			// writeEvent above already recorded it in the child transcript;
			// forward it to the parent so the fallover is visible live,
			// tagged with the agent type to distinguish it from a
			// main-thread fallback.
			if emitToParent != nil {
				flushRepeat()
				e.Agent = cfg.Name
				emitToParent(e)
			}
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
				// Block on the parent's decisions channel for the answer.
				// On the serial path the parent loop is blocked in
				// Agent.Execute waiting for us, so it isn't reading
				// decisions and we can read freely. But two foreground
				// subagents can run concurrently in one parallel batch
				// (AgentTool is ParallelSafe), and either could also be
				// forwarding an approval — so we take the shared approval
				// gate around this round-trip to keep the single channel +
				// single modal serving one request at a time. The gate is
				// a no-op when this subagent isn't inside a parallel batch.
				unlock := lockApprovalGate(ctx)
				emitToParent(ApprovalNeeded{
					ToolName: e.ToolName,
					Preview:  decorated,
					ArgsJSON: e.ArgsJSON,
				})
				emitActivity(fmt.Sprintf("waiting for user approval of %s …", e.ToolName))
				var verdict Decision
				select {
				case verdict = <-parentDecisions:
				case <-ctx.Done():
					unlock()
					flushRepeat()
					return "", true, subagents.TaskCanceled, 0
				}
				unlock()
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
			// Background path: nobody's watching. Dispatch's unattended
			// workers (bgPolicy) apply a deterministic allow/deny policy:
			// owned-file writes and an explicit read-only allowlist are allowed;
			// tests, shell, network, git process tools, and other approval-
			// requiring tools are denied. Other background subagents use the same
			// closed-form policy style, but only for read-only tools.
			verdict := Deny
			note := fmt.Sprintf("auto-denied %s (background subagent cannot prompt; allowlist the tool in permissions.json if needed)", e.ToolName)
			if opts.bgPolicy {
				verdict, note = backgroundWorkerDecision(e.ToolName, e.ArgsJSON, opts.sandboxed)
			}
			select {
			case childDecisions <- verdict:
			case <-ctx.Done():
				flushRepeat()
				return "", true, subagents.TaskCanceled, 0
			}
			emitActivity(note)
		case PathTrustElevationNeeded:
			// A mutating tool tried to write outside the child's workspace
			// (for a dispatch worker, outside its isolated worktree). The
			// child's Turn is now BLOCKED on the decisions channel — we MUST
			// feed it a verdict here, or the worker hangs forever (the
			// iteration cap can't fire mid-tool, and a background worker has
			// no other path out).
			canForward := emitToParent != nil && parentDecisions != nil
			if canForward {
				// Foreground: forward to the parent's path-trust modal so the
				// user can allow-once / trust-for-session, serialized across a
				// parallel batch by the shared approval gate.
				flushRepeat()
				unlock := lockApprovalGate(ctx)
				emitToParent(PathTrustElevationNeeded{
					ToolName:     e.ToolName,
					Path:         e.Path,
					Cwd:          e.Cwd,
					AllowedRoots: e.AllowedRoots,
					ArgsJSON:     e.ArgsJSON,
				})
				emitActivity(fmt.Sprintf("waiting for path-trust approval of %s …", e.Path))
				var verdict Decision
				select {
				case verdict = <-parentDecisions:
				case <-ctx.Done():
					unlock()
					flushRepeat()
					return "", true, subagents.TaskCanceled, 0
				}
				unlock()
				select {
				case childDecisions <- verdict:
				case <-ctx.Done():
					flushRepeat()
					return "", true, subagents.TaskCanceled, 0
				}
				if verdict == PathAllowOnce || verdict == PathTrustSession {
					emitActivity(fmt.Sprintf("path-trust granted for %s", e.Path))
				} else {
					emitActivity(fmt.Sprintf("path-trust denied for %s", e.Path))
				}
				continue
			}
			// Background: an unattended worker must stay in its sandbox.
			// Auto-deny — and feed the decision so the blocked child unblocks
			// (this is the fix for the forever-hang on the unfed channel).
			select {
			case childDecisions <- Deny:
			case <-ctx.Done():
				flushRepeat()
				return "", true, subagents.TaskCanceled, 0
			}
			emitActivity(fmt.Sprintf("auto-denied out-of-workspace write to %s (unattended worker is sandboxed; it cannot escape its worktree)", e.Path))
		case ContextCompacted:
			if e.Err != nil {
				emitActivity("context compaction skipped (summary failed) — continuing")
			} else {
				t.Tasks.IncrementCompactionCount(taskID)
				emitActivity(fmt.Sprintf("compacted context ~%dK → ~%dK tokens", e.Before/1000, e.After/1000))
			}
		case ContextUsage:
			// Live context fill for the dock. Recorded regardless of
			// foreground/background (the dock reads the registry).
			t.Tasks.SetContextUsage(taskID, e.Tokens, e.Window)
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

// doneTokensAndCalls resolves the completion-card stats for a finished
// subagent: its tool-call count and token total. Tokens are the child's
// EXACT provider-reported tally (Task.UsageTokens) when it reported usage,
// falling back to the passed rough ~4-char/token estimate only when it
// didn't. Centralized so every completion-event site — foreground and
// background, Agent tool and dispatch — reports identically; the per-site
// duplication this replaces once let the dispatch foreground path drift onto
// the estimate. Returns (estimate, 0) for an unknown id.
func doneTokensAndCalls(reg *subagents.Registry, taskID string, estimate int) (tokens, toolCalls int) {
	tokens = estimate
	if snap, ok := reg.Get(taskID); ok {
		toolCalls = snap.ToolCalls
		if exact := snap.UsageTokens(); exact > 0 {
			tokens = exact
		}
	}
	return tokens, toolCalls
}

// standaloneBackgroundApprovalPolicy is the GA safety policy for ordinary
// Agent(run_in_background:true) runs. Background delegation is available by
// default in the TUI, but unattended children still cannot answer approval
// modals, so only an explicit safe read-only allowlist can run. This is stricter
// than the normal no-approval path: no-approval mutators such as memory_save are
// still denied because standalone background subagents are documented read-only.
// Write-capable parallel implementation belongs to dispatch, which supplies
// dispatchBackgroundApprovalPolicy with worktree/file-scope guards.
func standaloneBackgroundApprovalPolicy(tool Tool, argsJSON string) (Decision, string, bool) {
	name := tool.Name()
	if !safeUnattendedReadOnlyTool(name) {
		return Deny, "denied " + name + " (standalone background subagents are read-only; use dispatch for worktree-isolated write fan-out, or run this subagent in the foreground)", true
	}
	if tool.RequiresApproval(argsJSON) {
		return Deny, "denied " + name + " (approval-requiring form is disabled for standalone background subagents)", true
	}
	return AllowOnce, "allowed " + name + " (standalone background read-only allowlist)", true
}

func safeUnattendedReadOnlyTool(name string) bool {
	if !readOnlyChildTools[name] {
		return false
	}
	if strings.HasPrefix(name, "lsp_") || strings.HasPrefix(name, "media_") || strings.HasPrefix(name, "git_") || name == "list_git_changed_files" || name == "fetch_url" {
		return false
	}
	return name != ConsultAdvisorToolName
}

// stripUnattendedProcessTools removes no-approval tools that can still launch
// non-git external binaries. Unattended workers have no prompt surface, so they
// must not reach language-server or ffmpeg/ffprobe startup through tools that
// otherwise look read-only.
func stripUnattendedProcessTools(reg *Registry) {
	for name := range reg.Names() {
		if strings.HasPrefix(name, "lsp_") || strings.HasPrefix(name, "media_") {
			reg.Deregister(name)
		}
	}
}

// dispatchBackgroundApprovalPolicyFor closes dispatchBackgroundApprovalPolicy
// over one dispatch worker's sandboxed-ness, producing the function value
// LoopConfig.BackgroundApprovalPolicy expects. sandboxed-ness is decided per
// worker (see childRunOpts.sandboxed), not per feature, so this is a closure
// rather than a package-level policy constant.
func dispatchBackgroundApprovalPolicyFor(sandboxed bool) func(Tool, string) (Decision, string, bool) {
	return func(tool Tool, argsJSON string) (Decision, string, bool) {
		return dispatchBackgroundApprovalPolicy(tool, argsJSON, sandboxed)
	}
}

// dispatchBackgroundApprovalPolicy is the deterministic approval policy for an
// unattended dispatch worker that can't prompt a human. It mirrors how hermes
// and Claude Code gate unattended subagents: allow the safe, contained
// operations; deny everything that needs human judgment — deterministically,
// never via an LLM. Returns the verdict + an activity note for the
// transcript/dock.
//
//   - File-mutation tools are allowed: the worktree child registry confines
//     their writes to the isolated worktree AND to the dispatch worker's owned
//     file set via WriteOpts, so the blast radius is the worker's branch.
//   - run_tests and run_bash execute arbitrary code from the worker's
//     worktree (tests can also be steered by GOFLAGS; the "read-only shell"
//     classifier, IsAutoModeSafeBash, is a first-token check that's
//     bypassable via env/command wrappers or process substitution, and
//     run_bash isn't path-confined once allowed). For an unsandboxed
//     (host-executing) worker that makes both an arbitrary-code-execution
//     surface for nobody-is-watching background workers, so they stay
//     DENIED. When sandboxed is true — this worker's commands run inside its
//     own per-worker container (see DispatchTool.SandboxFactory) — the blast
//     radius is the container, the same bet write_file already makes via
//     worktree confinement, so both are ALLOWED. "Confine, don't classify":
//     roadmap/dispatch-v3-collaboration.md § "0d revisited". A task that
//     needs shell/tests without a sandbox must run in the foreground (a
//     human approves).
//   - Everything else (git_commit, the unified git tool, fetch, …) is denied;
//     the worker's commit happens via dispatch's own auto-commit.
func dispatchBackgroundApprovalPolicy(tool Tool, argsJSON string, sandboxed bool) (Decision, string, bool) {
	name := tool.Name()
	switch name {
	case "write_file", "edit_file", "edit_anchored", "apply_diff", "mkdir", "copy_file", "move_file", "delete_file":
		return AllowOnce, "allowed " + name + " (dispatch worktree + owned-file scoped write)", true
	case "lsp_apply_workspace_edit":
		return Deny, "denied lsp_apply_workspace_edit (workspace edits can partially apply across files; run this task in the foreground to approve it)", true
	case "run_tests":
		if sandboxed {
			return AllowOnce, "allowed run_tests (confined to this worker's own sandbox container)", true
		}
		return Deny, "denied run_tests (tests execute code and are disabled for unattended dispatch workers without a sandbox; run the task in the foreground to approve tests, or enable [sandbox] backend = \"podman\")", true
	case "run_bash":
		if sandboxed {
			return AllowOnce, "allowed run_bash (confined to this worker's own sandbox container)", true
		}
		return Deny, "denied run_bash (shell is disabled for unattended dispatch workers without a sandbox; run this task in the foreground to approve shell/tests, or enable [sandbox] backend = \"podman\")", true
	case "create_document":
		return Deny, "denied create_document (document generation needs a human — run this task in the foreground to approve it)", true
	}
	if !safeUnattendedReadOnlyTool(name) {
		return Deny, "denied " + name + " (not auto-allowed for unattended dispatch workers; needs a human)", true
	}
	if tool.RequiresApproval(argsJSON) {
		return Deny, "denied " + name + " (approval-requiring form is disabled for unattended dispatch workers)", true
	}
	return AllowOnce, "allowed " + name + " (dispatch background read-only allowlist)", true
}
func backgroundWorkerDecision(toolName, argsJSON string, sandboxed bool) (Decision, string) {
	tool := namedApprovalTool{name: toolName}
	decision, note, _ := dispatchBackgroundApprovalPolicy(tool, argsJSON, sandboxed)
	return decision, note
}

type namedApprovalTool struct{ name string }

func (t namedApprovalTool) Name() string                 { return t.name }
func (t namedApprovalTool) Description() string          { return "" }
func (t namedApprovalTool) Schema() map[string]any       { return nil }
func (t namedApprovalTool) RequiresApproval(string) bool { return true }
func (t namedApprovalTool) PreviewCall(string) string    { return t.name }
func (t namedApprovalTool) Execute(context.Context, string) (string, error) {
	return "", nil
}

// buildChildRegistry clones the parent registry into a new one, stripping the
// delegation tools (recursion guard) and the plan-mode boundary tools
// (children inherit the parent's plan-mode state by pointer; only the
// top-level loop may transition it — a child flipping the shared mode
// mid-flight would yank the parent's gates out from under it), and
// applying the agent config's tools allowlist when one is set. The
// recursion guard is unconditional — even a config that names Agent/dispatch
// in its allowlist cannot reintroduce it.
func (t *AgentTool) buildChildRegistry(cfg *subagents.AgentConfig) *Registry {
	out := NewRegistry()
	for _, tool := range t.ParentRegistry.Tools() {
		name := tool.Name()
		if isDelegationTool(name) || isPlanBoundaryTool(name) {
			continue
		}
		if !cfg.ToolAllowed(name) {
			continue
		}
		out.Register(tool)
	}
	advisor := t.AdvisorAdapter
	advisorModel := t.AdvisorModel
	if advisor == nil {
		advisor = t.SmartAdapter
		advisorModel = t.SmartModel
	}
	if advisor != nil {
		out.Register(&ConsultAdvisorTool{Advisor: advisor, Model: advisorModel})
	}
	return out
}

func isDelegationTool(name string) bool {
	return name == agentToolName || name == DispatchToolName || name == IntegrateToolName
}

func (t *AgentTool) unknownSubagentError(name string) string {
	available := make([]string, 0, len(t.Configs))
	for _, c := range t.Configs {
		available = append(available, c.Name)
	}
	sort.Strings(available)
	return fmt.Sprintf("error: unknown subagent_type %q (available: %s)", name, strings.Join(available, ", "))
}

// forwardToParent delivers a child event to the parent's event channel.
// Most events are best-effort — dropped (via trySend) under buffer pressure
// OR after the per-turn channel is closed, rather than stalling or crashing
// the child loop (a chatty child must not be able to wedge the parent, and a
// detached background child outlives the turn that owns this channel). But
// events the child then BLOCKS on a decision for — ApprovalNeeded and
// PathTrustElevationNeeded — must never be dropped: a lost request leaves the
// child parked on the decisions channel with no feeder, and for a foreground
// batch the parent's wg.Wait never returns (frozen UI). For those we use a
// blocking, ctx-aware send so the request is delivered as soon as the consumer
// drains (e.g. after the user answers a parent-level modal), unblocking only
// if the turn itself is canceled. Those blocking sends are only ever reached
// on the FOREGROUND path (the background spawn passes decisions=nil, so an
// approval-needed event auto-denies inside runChild and never forwards), where
// the parent goroutine is blocked in Execute and the channel stays open — so
// the closed-channel race is confined to the best-effort default branch.
func forwardToParent(ctx context.Context, parentEvents chan<- Event, ev Event) {
	if parentEvents == nil {
		return
	}
	switch ev.(type) {
	case ApprovalNeeded, PathTrustElevationNeeded:
		select {
		case parentEvents <- ev:
		case <-ctx.Done():
		}
	default:
		// Best-effort: drop when the consumer is behind (full buffer) OR
		// when the per-turn channel has already been CLOSED because the
		// spawning turn ended. A detached background subagent runs on
		// context.Background() and keeps emitting progress after its
		// spawning turn closed this channel (the TUI closes the per-turn
		// events channel the moment agent.Turn returns). A plain
		// select-with-default still PANICS on a send to a closed channel —
		// the default guards only a *full* channel, never a *closed* one —
		// so trySend wraps the send to turn that end-of-turn race into a
		// clean drop. The registry's activity ring + the transcript remain
		// the durable record, so a dropped inline tick costs nothing.
		trySend(parentEvents, ev)
	}
}

// trySend performs a non-blocking, panic-safe send of ev on ch, returning
// false (dropping ev) when the channel is full OR closed. The closed case
// is the normal end-of-turn state for the per-turn event stream: a plain
// select-with-default is not enough on its own because a send to a closed
// channel panics even when a default case is present (the send case counts
// as "ready"). See forwardToParent's default branch for why a detached
// background child hits this.
func trySend(ch chan<- Event, ev Event) (sent bool) {
	defer func() {
		// A send on a closed channel panics from inside the select below;
		// recover degrades it to a dropped event rather than a crash that
		// the runChild recover would turn into a failed background run.
		if recover() != nil {
			sent = false
		}
	}()
	select {
	case ch <- ev:
		return true
	default:
		return false
	}
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
//
// The writer maintains two pieces of state so the rendered output is
// scannable markdown rather than a raw event dump: a content buffer
// that accumulates ContentToken streams into one block per assistant
// message, and a pending-tool record that pairs each ToolStart with
// its ToolResult into a single rendered tool section.
type transcriptFile struct {
	f *os.File

	// contentBuf accumulates ContentToken bodies between flushable
	// events (tool start, error, turn done, run outcome). Flushed
	// as one trimmed paragraph rather than one line per streamed
	// token.
	contentBuf strings.Builder

	// pendingTool tracks the most recent ToolStart so we can pair it
	// with its ToolResult into a single rendered block. nil when no
	// tool call is in flight.
	pendingTool *pendingToolCall
}

// pendingToolCall is the parked record between a ToolStart event and
// its matching ToolResult. We hold the preview + start time so the
// rendered block can show the same preview the user saw in the live
// TUI plus the wall-clock duration of the call.
type pendingToolCall struct {
	name    string
	preview string
	started time.Time
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
	fmt.Fprintf(f, "\n## Task\n\n%s\n\n## Events\n\n", a.Prompt)
	return &transcriptFile{f: f}
}

func (tr *transcriptFile) writeEvent(ev Event) {
	if tr.f == nil {
		return
	}
	switch e := ev.(type) {
	case ReasoningToken:
		tr.flushContent()
		if txt := strings.TrimSpace(e.Text); txt != "" {
			fmt.Fprintf(tr.f, "> %s\n\n", txt)
		}
	case ContentToken:
		tr.contentBuf.WriteString(e.Text)
	case ToolStart:
		tr.flushContent()
		tr.pendingTool = &pendingToolCall{
			name:    e.ToolName,
			preview: e.Preview,
			started: time.Now(),
		}
	case ToolResult:
		tr.flushContent()
		tr.renderToolBlock(e)
		tr.pendingTool = nil
	case ApprovalAuto:
		tr.flushContent()
		fmt.Fprintf(tr.f, "_auto-approved %s (%s)_\n\n", e.ToolName, e.Source)
	case ApprovalNeeded:
		tr.flushContent()
		fmt.Fprintf(tr.f, "_approval requested for %s_\n\n", e.ToolName)
	case AssistantMessage:
		tr.flushContent()
		if msg := strings.TrimSpace(e.Message.Content); msg != "" {
			fmt.Fprintf(tr.f, "%s\n\n", msg)
		}
	case ErrorEvent:
		tr.flushContent()
		fmt.Fprintf(tr.f, "**Error:** %v\n\n", e.Err)
	case IterCap:
		tr.flushContent()
		fmt.Fprintf(tr.f, "---\n\n_iteration cap hit (max %d)_\n\n", e.Max)
	case TurnDone:
		tr.flushContent()
		fmt.Fprint(tr.f, "---\n\n")
	}
}

// flushContent drains the streaming content buffer as one trimmed
// paragraph and resets it. No-op when the buffer is empty. Called
// from every non-content branch in writeEvent so streamed tokens
// always land before the next structural event.
func (tr *transcriptFile) flushContent() {
	if tr.contentBuf.Len() == 0 {
		return
	}
	text := strings.TrimSpace(tr.contentBuf.String())
	tr.contentBuf.Reset()
	if text != "" {
		fmt.Fprintf(tr.f, "%s\n\n", text)
	}
}

// renderToolBlock renders one ToolResult (paired with its matching
// ToolStart when available) as a markdown section: `### preview`
// heading, fenced code block carrying the full output, and a meta
// footer with errored / duration tags. The preview string from the
// original ToolStart is reused when available so the heading matches
// what the user saw in the live TUI.
//
// The output is written in full, untruncated. The transcript's job
// is to be the faithful record of what the subagent did — the live
// TUI already truncates tool cards at cardBodyLineCap lines, so the
// transcript is where the user goes when they need to see the
// complete output.
func (tr *transcriptFile) renderToolBlock(r ToolResult) {
	heading := r.ToolName
	var duration time.Duration
	if tr.pendingTool != nil && tr.pendingTool.name == r.ToolName {
		if tr.pendingTool.preview != "" {
			heading = tr.pendingTool.preview
		}
		duration = time.Since(tr.pendingTool.started)
	}
	fmt.Fprintf(tr.f, "### %s\n\n", heading)

	if output := strings.TrimRight(r.Output, "\n"); output != "" {
		lang := langForTool(r.ToolName)
		fmt.Fprintf(tr.f, "```%s\n%s\n```\n", lang, output)
	}

	var meta []string
	if r.Errored {
		meta = append(meta, "errored")
	}
	if duration > 0 {
		meta = append(meta, duration.Round(time.Millisecond).String())
	}
	if len(meta) > 0 {
		fmt.Fprintf(tr.f, "\n_%s_\n", strings.Join(meta, " · "))
	}
	fmt.Fprint(tr.f, "\n")
}

// langForTool returns the markdown fenced-code-block language hint
// for a tool's output. Returns "" for tools whose output isn't a
// natural single language (grep match lines, dir listings, etc.) so
// markdown renderers fall back to plain monospace.
func langForTool(name string) string {
	switch name {
	case "run_bash", "run_tests":
		return "bash"
	case "edit_file", "apply_diff", "git_diff_files":
		return "diff"
	}
	return ""
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
	tr.flushContent()
	if tr.pendingTool != nil {
		fmt.Fprintf(tr.f, "_%s call was in flight when the run ended_\n\n", tr.pendingTool.name)
		tr.pendingTool = nil
	}
	fmt.Fprintf(tr.f, "---\n\n**Outcome:** %s\n", outcome)
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
