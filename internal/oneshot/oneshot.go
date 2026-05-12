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
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// defaultSystemPrompt is sourced from internal/agent so the TUI and
// the oneshot runner cannot drift. See agent.DefaultSystemPrompt.
const defaultSystemPrompt = agent.DefaultSystemPrompt

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

	sess, fresh, err := openSession(opts, cwd)
	if err != nil {
		return err
	}
	mem, err := memory.Load(cwd)
	if err != nil {
		return err
	}
	// Load tunables — only Retrieval is consumed in oneshot today, but
	// loading the full Config keeps behavior consistent with the TUI
	// path. Missing file → defaults; invalid file → returned error.
	fileCfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	// AutoMemory: flag > env > config file. cli.Resolve handled the
	// first two; honor the persistent file toggle here so the wizard's
	// step isn't a no-op for one-shot runs.
	adCfg := adapter.Config{
		BaseURL:                opts.BaseURL,
		APIKey:                 opts.APIKey,
		Model:                  opts.Model,
		ProviderOverride:       adapter.Provider(strings.TrimSpace(opts.Provider)),
		ReasoningEffort:        opts.ReasoningEffort,
		EnableWebSearch:        opts.EnableWebSearch,
		DisableWebSearch:       opts.DisableWebSearch,
		EnableXSearch:          opts.EnableXSearch,
		EnableCodeInterpreter:  opts.EnableCodeInterpreter,
		SearchAllowedDomains:   splitCSV(opts.SearchAllowedDomains),
		SearchExcludedDomains:  splitCSV(opts.SearchExcludedDomains),
		XSearchAllowedHandles:  splitCSV(opts.XSearchAllowedHandles),
		XSearchExcludedHandles: splitCSV(opts.XSearchExcludedHandles),
		XSearchFromDate:        strings.TrimSpace(opts.XSearchFromDate),
		XSearchToDate:          strings.TrimSpace(opts.XSearchToDate),
	}
	// Routing: when [router].enabled is true in config.toml, dispatch
	// across the configured candidates per the chosen policy. The
	// returned Client implements both Streamer (for the agent loop)
	// and Profile() (for system-prompt composition). Preflight runs
	// only on the single-adapter path — per-candidate preflight is a
	// follow-up; in routed mode the router's first attempt surfaces
	// auth/connection errors via Fallback or ErrorEvent.
	router, err := cli.BuildRouter(fileCfg, opts)
	if err != nil {
		return err
	}
	var profile adapter.ProviderProfile
	if router != nil {
		profile = router.Profile()
	} else {
		profile = adapter.NewWithConfig(adCfg).Profile()
	}
	// One-shot has the full user prompt up front, so we score memory
	// bodies against it directly — no two-phase rebuild needed (the
	// TUI does the rebuild per-turn because it doesn't know the
	// prompt at session start). USER.md, YOTTACODE.md, and both
	// MEMORY.md indexes always inject in full.
	if fresh {
		sys := opts.SystemPrompt
		if sys == "" {
			sys = defaultSystemPrompt
		}
		sys = memory.SystemPromptFor(composeSystemPrompt(sys, profile), mem, prompt, fileCfg.Retrieval)
		sess.Messages = append(sess.Messages, adapter.Message{
			Role:    adapter.RoleSystem,
			Content: sys,
		})
	} else {
		sys := opts.SystemPrompt
		if sys == "" {
			sys = defaultSystemPrompt
		}
		recomposeSessionSystemPrompt(sess, memory.SystemPromptFor(composeSystemPrompt(sys, profile), mem, prompt, fileCfg.Retrieval))
	}
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
		injectRefsIntoSystem(sess, refs)
		prompt = filerefs.Rewrite(prompt, refs)
		for _, r := range refs {
			if r.Loaded {
				fmt.Fprintf(os.Stderr, "· attached %s (%d bytes)\n", r.Token, r.Size)
			} else {
				fmt.Fprintf(os.Stderr, "· could not attach %s: %s\n", r.Token, r.Error)
			}
		}
	}
	sess.Messages = append(sess.Messages, adapter.Message{
		Role:    adapter.RoleUser,
		Content: prompt,
	})
	var ad adapter.Client
	if router != nil {
		ad = router
	} else {
		if err := preflight(ctx, adCfg); err != nil {
			return err
		}
		ad = adapter.NewWithConfig(adCfg)
	}

	perms, err := permissions.Load(cwd)
	if err != nil {
		return err
	}
	writeOpts := agent.WritePathOptions{
		Cwd:          cwd,
		AllowedPaths: splitAllowPaths(opts.AllowPaths),
		DenyExact:    agent.DefaultDenyPaths(cwd),
	}
	denyReads := agent.DefaultDenyReadPaths(cwd)

	planStore := agent.NewPlanStore()
	if len(sess.Todos) > 0 {
		planStore.Replace(sess.Todos)
	}

	reg := agent.NewRegistry()
	reg.Register(&agent.ReadFileTool{Cwd: cwd, DenyReadPaths: denyReads})
	reg.Register(&agent.ReadManyFilesTool{Cwd: cwd, DenyReadPaths: denyReads})
	reg.Register(&agent.WriteFileTool{Cwd: cwd, WriteOpts: writeOpts})
	reg.Register(&agent.EditFileTool{Cwd: cwd, WriteOpts: writeOpts})
	reg.Register(&agent.ApplyDiffTool{Cwd: cwd, WriteOpts: writeOpts})
	reg.Register(&agent.MkdirTool{Cwd: cwd, WriteOpts: writeOpts})
	reg.Register(&agent.CopyFileTool{Cwd: cwd, WriteOpts: writeOpts})
	reg.Register(&agent.MoveFileTool{Cwd: cwd, WriteOpts: writeOpts})
	reg.Register(&agent.DeleteFileTool{Cwd: cwd, WriteOpts: writeOpts})
	reg.Register(&agent.ListGitChangedFilesTool{Cwd: cwd})
	reg.Register(&agent.GitBranchStatusTool{Cwd: cwd})
	reg.Register(&agent.GitShowFileAtRevTool{Cwd: cwd})
	reg.Register(&agent.GitDiffFilesTool{Cwd: cwd})
	reg.Register(&agent.GitStageFilesTool{Cwd: cwd})
	reg.Register(&agent.GitUnstageFilesTool{Cwd: cwd})
	reg.Register(&agent.GitCommitTool{Cwd: cwd})
	reg.Register(&agent.GitLogFileTool{Cwd: cwd})
	reg.Register(&agent.GitBlameLinesTool{Cwd: cwd})
	reg.Register(&agent.GitMergeBaseTool{Cwd: cwd})
	reg.Register(&agent.GitCheckpointTool{Cwd: cwd})
	reg.Register(&agent.RollbackTool{Cwd: cwd})
	reg.Register(&agent.RunTestsTool{Cwd: cwd})
	reg.Register(&agent.RunBashTool{Cwd: cwd})
	reg.Register(&agent.ListDirTool{Cwd: cwd})
	reg.Register(&agent.ListProjectStructureTool{Cwd: cwd})
	reg.Register(&agent.GlobTool{Cwd: cwd})
	reg.Register(&agent.GrepTool{Cwd: cwd, DenyReadPaths: denyReads})
	reg.Register(&agent.FetchURLTool{})
	reg.Register(&agent.MemorySaveTool{Cwd: cwd})
	reg.Register(&agent.MemoryForgetTool{Cwd: cwd})
	reg.Register(&agent.GitTool{Cwd: cwd})
	reg.Register(&agent.TodoWriteTool{Store: planStore})
	// ExitPlanModeTool is registered for schema parity with the TUI
	// build. The adapter-tools filter in the loop hides it whenever
	// PlanMode is inactive (which is always, in oneshot v1) so the
	// model never sees it. Plan mode itself is not yet exposed via
	// any oneshot flag.
	reg.Register(&agent.ExitPlanModeTool{})

	// Subagents: load definitions (built-in + ~/.yottacode/agents +
	// .yottacode/agents) and register the dispatch tool. Background
	// runs are rejected in oneshot — the model gets a recoverable
	// error string and can retry without the flag.
	subRes, _ := subagents.LoadAll(cwd, reg.Names())
	for _, w := range subRes.Warnings {
		fmt.Fprintln(os.Stderr, "subagents: "+w)
	}
	transcriptDir, _ := subagents.EnsureTranscriptDir(cwd)
	tasks := subagents.NewRegistry()
	// Oneshot doesn't expose plan/auto modes (no UI surface), so
	// the parent's mode states are inactive instances. Subagents
	// inherit them by pointer for consistency with the TUI path
	// even though they'll never flip on in this context.
	parentPlanMode := &agent.PlanModeState{}
	parentAutoMode := &agent.AutoModeState{}
	parentYoloMode := &agent.YoloModeState{}

	// Background subagents stay disabled in oneshot regardless of
	// experimental flag — there's no long-running session to host
	// async completion notifications, so honoring `run_in_background`
	// would silently lose results. Foreground subagents work in
	// oneshot whether or not the experimental gate is on.
	_ = experimental.BackgroundSubagents // referenced to document the link

	agentTool := &agent.AgentTool{
		Configs:         subRes.Configs,
		Tasks:           tasks,
		Adapter:         ad,
		ParentRegistry:  reg,
		Permissions:     perms,
		YoloMode:        parentYoloMode,
		PlanMode:        parentPlanMode,
		AutoMode:        parentAutoMode,
		Cwd:             cwd,
		TranscriptDir:   transcriptDir,
		AllowBackground: false,
	}
	reg.Register(agentTool)
	// Even though oneshot rejects background spawns (AllowBackground=
	// false), foreground subagent runs still write to the registry,
	// so the parent can call get_subagent_result to re-read a
	// completed foreground task's transcript-equivalent in the same
	// turn. Rare workflow in oneshot, but keeping the surface
	// symmetric with the TUI is worth the one extra line.
	reg.Register(&agent.GetSubagentResultTool{Tasks: tasks})

	cfg := agent.LoopConfig{
		Adapter:           ad,
		Registry:          reg,
		Permissions:       perms,
		BypassPermissions: opts.BypassPermissions,
		Cwd:               cwd,
		MaxIterations:     opts.MaxIterations,
		PlanMode:          parentPlanMode,
		AutoMode:          parentAutoMode,
		YoloMode:          parentYoloMode,
	}

	turnErr := stream(ctx, cfg, &sess.Messages, os.Stdout, os.Stderr)
	sess.Todos = planStore.Snapshot()
	if saveErr := sess.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "⚠ session save failed: %v\n", saveErr)
	}
	return turnErr
}

// splitAllowPaths is a duplicate of the same helper in tui/run.go,
// kept here so the oneshot package doesn't have to import the TUI.
// Trivial enough that the duplication is cheaper than a new shared
// package for one function.
func splitAllowPaths(s string) []string {
	return splitCSV(s)
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func openSession(opts cli.ChatOptions, cwd string) (*session.Session, bool, error) {
	if opts.Resume != "" {
		s, err := session.Load(opts.Resume)
		if err != nil {
			return nil, false, err
		}
		return s, false, nil
	}
	s, err := session.New(opts.Model, cwd)
	if err != nil {
		return nil, false, err
	}
	return s, true, nil
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
			err := fmt.Errorf("tool %q requires approval; add an allow rule to .yottacode/permissions.json, run interactively, or pass --dangerously-skip-permissions (DANGEROUS)", e.ToolName)
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

func preflight(ctx context.Context, cfg adapter.Config) error {
	result := adapter.Probe(ctx, cfg)
	if len(result.Issues) == 0 {
		return nil
	}
	return fmt.Errorf("provider preflight failed: %s", strings.Join(result.Issues, "; "))
}

func composeSystemPrompt(base string, profile adapter.ProviderProfile) string {
	if hasBuiltin(profile.EnabledBuiltinTools, adapter.BuiltinToolWebSearch) {
		return base + "\nFor live or current information, use the provider-native web_search tool when needed."
	}
	return base + "\nFor live or current information, use fetch_url for specific pages or feeds when needed."
}

func hasBuiltin(tools []adapter.BuiltinToolKind, want adapter.BuiltinToolKind) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func recomposeSessionSystemPrompt(sess *session.Session, content string) {
	for i := range sess.Messages {
		if sess.Messages[i].Role == adapter.RoleSystem {
			sess.Messages[i].Content = content
			return
		}
	}
	sess.Messages = append([]adapter.Message{{
		Role:    adapter.RoleSystem,
		Content: content,
	}}, sess.Messages...)
}

// injectRefsIntoSystem rewrites the session's system message so it
// contains the latest auto-injected file-refs block. Mirrors the TUI
// helper of the same shape (internal/tui/cmd_filerefs.go) so both
// entry points produce identical system-prompt structure.
func injectRefsIntoSystem(sess *session.Session, refs []filerefs.Ref) {
	for i := range sess.Messages {
		if sess.Messages[i].Role == adapter.RoleSystem {
			sess.Messages[i].Content = filerefs.Inject(sess.Messages[i].Content, refs)
			return
		}
	}
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
