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
		fmt.Fprintln(os.Stderr, "warning: --permission-mode plan is interactive-only; ignored for `yottacode run`")
	case "auto":
		fmt.Fprintln(os.Stderr, "warning: --permission-mode auto is interactive-only; ignored for `yottacode run`")
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
				fmt.Fprintf(os.Stderr, "· attached %s (%d bytes)\n", r.Token, r.Size)
			} else {
				fmt.Fprintf(os.Stderr, "· could not attach %s: %s\n", r.Token, r.Error)
			}
		}
	}
	rt.Session.Messages = append(rt.Session.Messages, adapter.Message{
		Role:    adapter.RoleUser,
		Content: prompt,
	})

	turnErr := stream(ctx, rt.Cfg, &rt.Session.Messages, os.Stdout, os.Stderr)
	rt.Session.Todos = rt.PlanStore.Snapshot()
	if saveErr := rt.Session.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "⚠ session save failed: %v\n", saveErr)
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
	for ev := range events {
		switch e := ev.(type) {
		case agent.ContentToken:
			fmt.Fprint(stdout, e.Text)
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
		case agent.AssistantMessage:
			printCitations(stderr, e.Message.Citations)
		case agent.ApprovalAuto:
			fmt.Fprintf(stderr, "[%s] %s\n", e.Source, e.Preview)
		case agent.ApprovalNeeded:
			err := fmt.Errorf("tool %q requires approval; add an allow rule to .yottacode/permissions.json, run interactively, or pass --yolo (DANGEROUS)", e.ToolName)
			fmt.Fprintf(stderr, "✗ %v\n", err)
			if firstErr == nil {
				firstErr = err
			}
			decisions <- agent.Deny
		case agent.ToolStart:
			fmt.Fprintf(stderr, "[tool] %s\n", e.Preview)
		case agent.ToolResult:
			_ = e // result feeds the model; nothing to print
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
			fmt.Fprintf(stderr, "[agent] hit %d/%d iterations — re-run with --max-iterations %d if the work was unfinished\n",
				e.Max, e.Max, e.Max*2)
		case agent.ErrorEvent:
			// Multi-line errors (e.g. 429 with retry-after hint) print
			// each line with its own ✗ prefix so the second line
			// doesn't look orphaned on stderr.
			for _, line := range strings.Split(strings.TrimRight(e.Err.Error(), "\n"), "\n") {
				fmt.Fprintf(stderr, "✗ %s\n", line)
			}
			if firstErr == nil {
				firstErr = e.Err
			}
		case agent.TurnDone:
			fmt.Fprintln(stdout)
			// Footnote on stderr (so `> out.md` redirects don't get
			// it) recording how long the turn took end-to-end —
			// matches the TUI's "› Thought for Ns" line.
			fmt.Fprintf(stderr, "› Thought for %s\n", formatTurnDuration(time.Since(turnStart)))
		case agent.TurnInterrupted:
			// Cancel reached oneshot via SIGINT or a parent-ctx
			// timeout. History was preserved by the agent loop —
			// note it on stderr so the exit code's "non-zero =
			// something happened" reads cleanly against a clean
			// stdout. The orphaned-call count helps debug whether
			// the cancel landed mid-tool or mid-stream.
			fmt.Fprintln(stdout)
			if e.OrphanedCalls > 0 {
				fmt.Fprintf(stderr, "↩ interrupted (%d tool call(s) cancelled)\n", e.OrphanedCalls)
			} else {
				fmt.Fprintln(stderr, "↩ interrupted")
			}
		}
	}

	if turnErr := <-errCh; firstErr == nil && turnErr != nil {
		firstErr = turnErr
	}
	if firstErr != nil && errors.Is(firstErr, context.Canceled) {
		return nil
	}
	return firstErr
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
			fmt.Fprintf(w, "[source] %s\n", label)
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
