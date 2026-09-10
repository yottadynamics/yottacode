// Package oneshot is the non-interactive entry point: read one prompt, run
// one agent turn, print the final answer to stdout, exit. The TUI is great
// for working sessions; oneshot is what scripts and CI pipelines call.
//
// Output convention:
//   - stdout: the model's *content* tokens (the answer)
//   - stderr: reasoning tokens, tool-call status, errors
//
// That way `yottacode run "summarize x" > out.md` produces a clean file with
// reasoning visible only on the terminal.
package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/agentruntime"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/session"
)

// Run drives a single non-interactive turn and returns when the assistant
// produces a tool-free reply (or hits an error / iter cap). The session is
// saved like any other for later /resume.
func Run(ctx context.Context, opts cli.ChatOptions, prompt string) error {
	// --permission-mode plan/auto are TUI-only (they need an approval
	// surface and a Shift+Tab cycle that don't exist in non-interactive
	// mode). Warn the user and proceed — matches how the old --plan
	// flag was a no-op for `yottacode run`.
	switch opts.PermissionMode {
	case "plan":
		fmt.Fprintln(os.Stderr, "[warning] --permission-mode plan is interactive-only; ignored for `yottacode run`")
	case "auto":
		fmt.Fprintln(os.Stderr, "[warning] --permission-mode auto is interactive-only; ignored for `yottacode run`")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	spec := agentruntime.SessionSpec{
		ChatOptions: opts,
		Cwd:         cwd,
		// No long-lived session exists to host async completions, so
		// background dispatch stays off — same rationale oneshot has
		// always used.
		SupportsBackgroundDispatch: false,
		// enter_worktree/exit_worktree are safe here: oneshot hosts one
		// session per process, so process-global os.Chdir() can't
		// collide with a sibling session the way it could in ACP.
		DisableWorktreeTools: false,
	}
	rt, err := agentruntime.NewBuilder().Build(ctx, spec)
	if err != nil {
		return err
	}
	defer rt.Close(context.Background())
	for _, w := range rt.Warnings {
		fmt.Fprintln(os.Stderr, w)
	}

	// One-shot has the full user prompt up front, so — unlike Build's
	// default prompt-unaware memory.SystemPrompt injection (shared with
	// TUI, which doesn't know the prompt at session-construction time) —
	// score memory bodies against it directly and overwrite what Build
	// already set. No two-phase rebuild needed: USER.md, YOTTACODE.md,
	// and both MEMORY.md indexes always inject in full regardless.
	semanticSys := memory.SystemPromptForSemantic(ctx, rt.BaseSystemPrompt, rt.Mem, prompt, rt.FileCfg.Retrieval, rt.EmbedClient)
	agentruntime.RecomposeSystemPrompt(rt.Session, semanticSys, len(rt.BaseSystemPrompt))

	// Auto-inject @<path> file references found in the prompt into the
	// system prompt before the turn fires. Mirrors the TUI startTurn
	// path so `yottacode run "explain @main.go"` and the interactive
	// equivalent behave identically. Load failures are reported on
	// stderr but never block the turn. We also strip the leading `@`
	// from the prompt for successfully-loaded refs so the model
	// doesn't see literal "@docs/foo.md" and try to read_file with
	// the `@` as part of the path — see filerefs.Rewrite for context.
	if refs := filerefs.Parse(prompt); len(refs) > 0 {
		refs = filerefs.Load(refs, cwd)
		injectRefsIntoSystem(rt.Session, refs)
		prompt = filerefs.Rewrite(prompt, refs)
		for _, r := range refs {
			if r.Loaded {
				fmt.Fprintf(os.Stderr, "[attachment] %s (%d bytes)\n", r.Token, r.Size)
			} else {
				fmt.Fprintf(os.Stderr, "[attachment:error] %s: %s\n", r.Token, r.Error)
			}
		}
	}
	submitted := time.Now()
	rt.Session.Messages = append(rt.Session.Messages, adapter.Message{
		Role:      adapter.RoleUser,
		Content:   prompt,
		Timestamp: &submitted,
	})

	turnErr := streamWithOptions(ctx, rt.Cfg, &rt.Session.Messages, os.Stdout, os.Stderr, StreamOptions{
		JSONStatus: opts.RunJSONStatus,
		Format:     opts.RunFormat,
		SessionID:  rt.Session.ID,
	})
	rt.Session.Todos = rt.PlanStore.Snapshot()
	if saveErr := rt.Session.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "[warning] session save failed: %v\n", saveErr)
	}
	return turnErr
}

// injectRefsIntoSystem rewrites the session's system message so it
// contains the latest auto-injected file-refs block. Mirrors the TUI
// helper of the same shape (internal/tui/cmd_filerefs.go) so both
// entry points produce identical system-prompt structure. Stays in
// oneshot (not agentruntime): only oneshot has a live prompt at this
// point in construction to parse @-refs out of.
func injectRefsIntoSystem(sess *session.Session, refs []filerefs.Ref) {
	for i := range sess.Messages {
		if sess.Messages[i].Role == adapter.RoleSystem {
			sess.Messages[i].Content = filerefs.Inject(sess.Messages[i].Content, refs)
			return
		}
	}
}

// StreamOptions carries script-facing output switches for the oneshot event
// drain. Keep this small: `run` stdout is deliberately stable, while stderr can
// grow metadata needed by integrations.
type StreamOptions struct {
	JSONStatus bool
	Format     string
	SessionID  string
}

const (
	// ExitReasonOK means the turn reached a final tool-free assistant answer.
	ExitReasonOK = "ok"
	// ExitReasonError means the turn returned a process-failing error.
	ExitReasonError = "error"
	// ExitReasonIterCap means the turn exhausted its iteration budget.
	ExitReasonIterCap = "iter_cap"
)

// ToolCallSummary is the stable, intentionally small record exposed to scripts.
// Summary reuses the same bounded, human-safe preview shown in text-mode status.
type ToolCallSummary struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// RunResult is the primary stdout contract for `yottacode run --format json`.
// Error is a pointer so successful runs encode an explicit JSON null value.
type RunResult struct {
	Content    string            `json:"content"`
	ToolCalls  []ToolCallSummary `json:"tool_calls"`
	Usage      adapter.Usage     `json:"usage"`
	ExitReason string            `json:"exit_reason"`
	Error      *string           `json:"error"`
	SessionID  string            `json:"session_id"`
}

// ToolRunStatus summarizes one tool's execution count for the JSON status
// envelope. Errored counts tool-level errors already surfaced to the model.
type ToolRunStatus struct {
	Count   int `json:"count"`
	Errored int `json:"errored,omitempty"`
}

// RunStatus is the machine-readable receipt emitted by `yottacode run --json`.
// Stdout still carries only the final assistant response; this envelope belongs
// on stderr so shell pipelines and CI can parse metadata independently.
type RunStatus struct {
	Status       string                   `json:"status"`
	Error        string                   `json:"error,omitempty"`
	Iterations   int                      `json:"iterations"`
	Tools        map[string]ToolRunStatus `json:"tools,omitempty"`
	ChangedFiles []string                 `json:"changed_files,omitempty"`
}

// stream is the testable core: spawns the agent goroutine, drains events,
// and writes them to the configured streams. ApprovalNeeded never fires
// when cfg.BypassPermissions is true (the loop emits ApprovalAuto instead);
// when it does fire, oneshot returns an error since there's no human to
// answer.
func stream(
	ctx context.Context,
	cfg agent.LoopConfig,
	history *[]adapter.Message,
	stdout, stderr io.Writer,
) error {
	return streamWithOptions(ctx, cfg, history, stdout, stderr, StreamOptions{})
}

func streamWithOptions(
	ctx context.Context,
	cfg agent.LoopConfig,
	history *[]adapter.Message,
	stdout, stderr io.Writer,
	opts StreamOptions,
) error {
	events := make(chan agent.Event, 64)
	decisions := make(chan agent.Decision, 1)
	errCh := make(chan error, 1)
	turnStart := time.Now()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				close(events)
				errCh <- fmt.Errorf("agent turn panicked: %v", r)
			}
		}()
		err := agent.Turn(ctx, cfg, history, events, decisions)
		close(events)
		errCh <- err
	}()

	var firstErr error
	jsonOutput := opts.Format == cli.RunFormatJSON
	result := RunResult{
		ToolCalls:  make([]ToolCallSummary, 0),
		ExitReason: ExitReasonOK,
		SessionID:  opts.SessionID,
	}
	status := RunStatus{
		Status: "running",
		Tools:  map[string]ToolRunStatus{},
	}
	changedSeen := map[string]bool{}
	iterCapHit := false
	interrupted := false
	policyDenied := false
	runTestsFailedLast := false
	finalContent := ""
	for ev := range events {
		switch e := ev.(type) {
		case agent.IterationStart:
			status.Iterations = e.Number
		case agent.ContentToken:
			if !jsonOutput {
				fmt.Fprint(stdout, e.Text)
			}
		case agent.ReasoningToken:
			fmt.Fprint(stderr, e.Text)
		case agent.ProviderToolCall:
			if e.Detail != "" {
				fmt.Fprintf(stderr, "[provider-tool] %s %s: %s\n", e.ToolName, e.Phase, e.Detail)
			} else {
				fmt.Fprintf(stderr, "[provider-tool] %s %s\n", e.ToolName, e.Phase)
			}
		case agent.Fallback:
			if e.Reason != "" {
				fmt.Fprintf(stderr, "[fallback] %s → %s [%s]: %s\n", e.From, e.To, e.Policy, e.Reason)
			} else {
				fmt.Fprintf(stderr, "[fallback] %s → %s [%s]\n", e.From, e.To, e.Policy)
			}
		case agent.ApprovalAuto:
			fmt.Fprintf(stderr, "[%s] %s\n", e.Source, e.Preview)
			if e.Source == "deny-rule" {
				policyDenied = true
			}
		case agent.ApprovalNeeded:
			err := fmt.Errorf("tool %q requires approval; add an allow rule to .yottacode/permissions.json, run interactively, or pass --yolo (DANGEROUS)", e.ToolName)
			fmt.Fprintf(stderr, "[error] %v\n", err)
			if firstErr == nil {
				firstErr = err
			}
			decisions <- agent.Deny

		case agent.ToolStart:
			fmt.Fprintf(stderr, "[tool] %s\n", e.Preview)
			result.ToolCalls = append(result.ToolCalls, ToolCallSummary{
				Name:    e.ToolName,
				Summary: truncateOneLine(e.Preview, 240),
			})
		case agent.ToolResult:
			recordToolStatus(&status, e)
			if e.ToolName == "run_tests" {
				runTestsFailedLast = runTestsFailed(e.Output)
			}
			if e.ToolName == "list_git_changed_files" && !e.Errored {
				collectChangedFiles(&status, changedSeen, e.Output)
			}
		case agent.SubagentStart:
			label := "foreground"
			if e.Background {
				label = "background"
			}
			fmt.Fprintf(stderr, "[subagent:%s] start (%s) — %s\n", e.AgentType, label, truncateOneLine(e.Prompt, 120))
		case agent.SubagentProgress:
			fmt.Fprintf(stderr, "[subagent:%s] %s\n", e.AgentType, e.Activity)
		case agent.SubagentDone:
			tag := "done"
			if e.Errored {
				tag = "errored"
			}
			fmt.Fprintf(stderr, "[subagent:%s] %s in %s\n", e.AgentType, tag, formatTurnDuration(e.Duration))
		case agent.SubagentBackgroundDone:
			// Should not occur in oneshot (AllowBackground=false) but
			// emit a defensive log if it ever does.
			fmt.Fprintf(stderr, "[subagent:%s] background %s (unexpected in oneshot)\n", e.AgentType, e.TaskID)
		case agent.TodoUpdate:
			done := 0
			for _, td := range e.Todos {
				if td.Status == agent.TodoCompleted {
					done++
				}
			}
			fmt.Fprintf(stderr, "[plan] %d items (%d done)\n", len(e.Todos), done)
		case agent.IterCap:
			iterCapHit = true
			fmt.Fprintf(stderr, "[agent] hit %d/%d iterations — re-run with --max-iterations %d if the work was unfinished\n",
				e.Max, e.Max, e.Max*2)
		case agent.ErrorEvent:
			// Prefix every line so multi-line provider errors remain visibly one
			// status class instead of leaving continuation lines orphaned.
			for _, line := range strings.Split(strings.TrimRight(e.Err.Error(), "\n"), "\n") {
				fmt.Fprintf(stderr, "[error] %s\n", line)
			}
			if firstErr == nil {
				firstErr = e.Err
			}
		case agent.AssistantMessage:
			printCitations(stderr, e.Message.Citations)
			finalContent = e.Message.Content
			result.Usage.Add(e.Message.Usage)
			if jsonOutput && len(e.Message.ToolCalls) == 0 && e.Message.Content != "" {
				result.Content += e.Message.Content
			}
		case agent.TurnDone:
			if !jsonOutput {
				fmt.Fprintln(stdout)
			}
			// Footnote on stderr (so `> out.md` redirects don't get
			// it) recording how long the turn took end-to-end —
			// matches the TUI's "› Thought for Ns" line.
			fmt.Fprintf(stderr, "[done] thought for %s\n", formatTurnDuration(time.Since(turnStart)))
		case agent.TurnInterrupted:
			// Cancel reached oneshot via SIGINT or a parent-ctx timeout. History
			// was preserved by the loop; JSON mode keeps stdout reserved for its
			// single result object while text mode terminates partial prose.
			interrupted = true
			if jsonOutput {
				result.Content = e.PartialContent
			} else {
				fmt.Fprintln(stdout)
			}
			if e.OrphanedCalls > 0 {
				fmt.Fprintf(stderr, "[interrupted] %d tool call(s) cancelled\n", e.OrphanedCalls)
			} else {
				fmt.Fprintln(stderr, "[interrupted]")
			}
		}
	}

	turnErr := <-errCh
	if firstErr == nil && turnErr != nil {
		firstErr = turnErr
	}
	// Text mode historically treats cancellation as a clean interruption. JSON
	// mode must retain the error so its `error` exit reason agrees with the shell.
	if firstErr != nil && errors.Is(firstErr, context.Canceled) && !jsonOutput {
		firstErr = nil
	}
	status.Status = classifyRunStatus(runOutcome{
		Err:          firstErr,
		IterCapHit:   iterCapHit,
		PolicyDenied: policyDenied,
		TestsFailed:  runTestsFailedLast,
		FinalContent: finalContent,
	})
	if firstErr != nil {
		status.Error = firstErr.Error()
	}
	if opts.JSONStatus {
		if err := emitJSONStatus(stderr, status); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if jsonOutput {
		switch {
		case firstErr != nil:
			result.ExitReason = ExitReasonError
			errText := firstErr.Error()
			result.Error = &errText
		case interrupted:
			result.ExitReason = ExitReasonError
			errText := "turn interrupted"
			result.Error = &errText
		case iterCapHit:
			result.ExitReason = ExitReasonIterCap
		default:
			result.ExitReason = ExitReasonOK
		}
		if err := emitRunResult(stdout, result); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// emitRunResult writes exactly one compact, newline-terminated JSON object.
// Compact output is friendlier to JSONL-style CI capture while remaining jq-readable.
func emitRunResult(w io.Writer, result RunResult) error {
	return json.NewEncoder(w).Encode(result)
}

func recordToolStatus(status *RunStatus, e agent.ToolResult) {
	if status.Tools == nil {
		status.Tools = map[string]ToolRunStatus{}
	}
	tool := status.Tools[e.ToolName]
	tool.Count++
	if e.Errored {
		tool.Errored++
	}
	status.Tools[e.ToolName] = tool
}

func collectChangedFiles(status *RunStatus, seen map[string]bool, output string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "(no changed files)" || seen[line] {
			continue
		}
		seen[line] = true
		status.ChangedFiles = append(status.ChangedFiles, line)
	}
}

type runOutcome struct {
	Err          error
	IterCapHit   bool
	PolicyDenied bool
	TestsFailed  bool
	FinalContent string
}

func classifyRunStatus(outcome runOutcome) string {
	if outcome.IterCapHit {
		return "iteration_cap"
	}
	if outcome.PolicyDenied {
		return "policy_denied"
	}
	if outcome.TestsFailed {
		return "tests_failed"
	}
	if looksBlockedForClarification(outcome.FinalContent) {
		return "blocked_needs_clarification"
	}
	if outcome.Err == nil {
		return "success"
	}
	msg := outcome.Err.Error()
	if strings.Contains(msg, "requires approval") {
		return "approval_required"
	}
	return "provider_error"
}

func runTestsFailed(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "exit=") {
			continue
		}
		code := strings.TrimSpace(strings.TrimPrefix(line, "exit="))
		if code == "" {
			return false
		}
		return code != "0"
	}
	return false
}

func looksBlockedForClarification(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))
	return strings.HasPrefix(content, "blocked:") && strings.Contains(content, "clarification")
}

func emitJSONStatus(w io.Writer, status RunStatus) error {
	if len(status.Tools) == 0 {
		status.Tools = nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(status)
}

// truncateOneLine returns at most max chars of s, collapsing any
// newlines to spaces so a multi-line subagent prompt renders as a
// single stderr log line.
func truncateOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// formatTurnDuration mirrors the TUI's formatDuration so the
// end-of-turn footnote reads identically across both entry points.
// Sub-minute resolutions in seconds, minute resolutions for longer
// turns, hour resolutions past an hour.
func formatTurnDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, (s%3600)/60)
}

func printCitations(w io.Writer, citations []adapter.Citation) {
	for _, c := range citations {
		if label := citationLabel(c); label != "" {
			fmt.Fprintf(w, "[citation] %s\n", label)
		}
	}
}

func citationLabel(c adapter.Citation) string {
	switch {
	case c.Title != "" && c.URL != "":
		return c.Title + " (" + c.URL + ")"
	case c.Title != "":
		return c.Title
	case c.Filename != "" && c.FileID != "":
		return c.Filename + " (" + c.FileID + ")"
	case c.Filename != "":
		return c.Filename
	case c.URL != "":
		return c.URL
	case c.FileID != "":
		return c.FileID
	default:
		return ""
	}
}
