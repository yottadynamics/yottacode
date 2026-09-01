package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/agentruntime"
	"github.com/yottadynamics/yottacode/internal/checkpoint"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/lsp"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/sensitive"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/skills"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/trust"
	"github.com/yottadynamics/yottacode/internal/update"
	"github.com/yottadynamics/yottacode/internal/usercmd"
	"github.com/yottadynamics/yottacode/internal/version"
	"github.com/yottadynamics/yottacode/internal/wizard"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

const (
	// subagentDrainGrace is how long session teardown waits for canceled
	// subagents to unwind normally. Short on purpose: a canceled worker
	// notices at its next context check, and nobody should pay a long hang on
	// quit for work that is already being thrown away.
	subagentDrainGrace = 3 * time.Second
	// subagentCommitDrainMax is the ceiling for the one case worth waiting on:
	// a dispatch worker mid-commit. That commit runs cancellation-detached so
	// a just-finished worker still saves its output, which means teardown
	// cannot stop it — only outrun it. Losing that race kills git mid-write and
	// can leave a stale index.lock in a worktree the user never knew existed,
	// so they get a hand-repair job instead of a clean quit. `git add -A` plus
	// a lint/format pre-commit hook routinely needs more than the grace window,
	// hence the separate, much larger ceiling. Still bounded: a wedged hook
	// must not hold the session hostage forever.
	subagentCommitDrainMax = 30 * time.Second
	// subagentDrainPoll is the registry poll interval for both phases.
	subagentDrainPoll = 20 * time.Millisecond
)

// drainSubagentsOnExit waits for canceled subagents to unwind at session
// teardown, in two phases.
//
// Phase 1 is the flat grace window: everything gets subagentDrainGrace to
// notice its canceled context and finish.
//
// Phase 2 only happens if a dispatch worker is still mid-commit when the grace
// expires (Registry.CommittingCount). Those workers are extended to
// subagentCommitDrainMax because abandoning them is destructive in a way that
// abandoning an ordinary canceled worker is not — see subagentCommitDrainMax.
// The wait is announced on stderr: Bubbletea has already released the terminal
// by the time this defer runs, so a silent multi-second pause would read as a
// hang. Printing only in phase 2 keeps the common quit silent.
func drainSubagentsOnExit(tasks *subagents.Registry) {
	drainSubagents(tasks, subagentDrainGrace, subagentCommitDrainMax, subagentDrainPoll, os.Stderr)
}

// drainSubagents is drainSubagentsOnExit with the timings and output sink
// injected, so tests can exercise both phases without waiting real seconds.
func drainSubagents(tasks *subagents.Registry, grace, commitMax, poll time.Duration, out io.Writer) {
	deadline := time.Now().Add(grace)
	for tasks.ActiveCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(poll)
	}
	committing := tasks.CommittingCount()
	if committing == 0 {
		return
	}
	fmt.Fprintf(out, "waiting for %s to finish committing (up to %s, Ctrl-C to abandon)…\n",
		pluralizeWorkers(committing), commitMax)
	extended := time.Now().Add(commitMax)
	for tasks.CommittingCount() > 0 && time.Now().Before(extended) {
		time.Sleep(poll)
	}
	if left := tasks.CommittingCount(); left > 0 {
		fmt.Fprintf(out, "gave up waiting on %s still committing; if a worktree reports a stale index.lock, remove that file and retry the git command\n",
			pluralizeWorkers(left))
	}
}

func pluralizeWorkers(n int) string {
	if n == 1 {
		return "1 dispatch worker"
	}
	return fmt.Sprintf("%d dispatch workers", n)
}

// Run wires up session, permissions, adapter, and tools, then drives
// the Bubbletea program. The non-interactive sibling is oneshot.Run, which
// shares the same ChatOptions but emits one turn to stdout and exits.
func Run(ctx context.Context, opts cli.ChatOptions, updateCheck ...<-chan update.Result) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Folder-trust gate: fires before Build so an untrusted workspace
	// never accumulates session state. Subfolders of any previously
	// trusted root inherit trust; --allow-paths roots satisfy the gate
	// session-only; YOTTACODE_TRUST_ALL=1 is the CI escape hatch. See
	// yottacode-roadmap/folder-trust.md.
	if err := ensureWorkspaceTrust(cwd, opts); err != nil {
		return err
	}

	// Load slash commands from two scopes merged by precedence:
	// project (<cwd>/.yottacode/commands/) > user (~/.yottacode/commands/).
	// Fail-soft: per-file load errors are surfaced as startup notices
	// below but never block launch. Shadow warnings fire when user
	// and project name-collide.
	customCmds, customErrs := usercmd.Load(cwd)

	// sessionID is set once Build returns (see below) and read by the
	// PreCompact closure — captured by reference here since Build needs
	// the closure before the session it snapshots exists yet, but the
	// closure itself is only ever invoked much later, mid-turn.
	var sessionID string
	spec := agentruntime.SessionSpec{
		ChatOptions: opts,
		Cwd:         cwd,
		// TUI is long-running and can host detached background workers,
		// surfacing their completion via the subagent inbox.
		SupportsBackgroundDispatch: true,
		// enter_worktree/exit_worktree's process-global os.Chdir is safe
		// here: the TUI hosts one session per process.
		DisableWorktreeTools: false,
		PreCompact: func(history []adapter.Message) (string, error) {
			return writePreSummarySnapshot(sessionID, history)
		},
		// TUI starts MCP servers asynchronously via its own Bubbletea
		// tea.Cmd (cmd_mcp.go) so slow/hanging npx-based servers don't
		// delay first paint.
		DeferMCPStart: true,
	}
	rt, err := agentruntime.NewBuilder().Build(ctx, spec)
	if err != nil {
		return err
	}
	sessionID = rt.Session.ID
	defer rt.LSPManager.CloseAll()
	if rt.CmdSandbox != nil {
		defer func() { _ = rt.CmdSandbox.Close() }()
	}

	// `--summarized` (only meaningful when resuming): replace the loaded
	// transcript with a structured summary injected into the system
	// prompt, the same way the in-TUI /sessions picker's `s`-toggle on
	// the Resume row does.
	if opts.Summarized && !rt.Fresh {
		newSess, _, note, err := loadSummarizedSession(summarizeDeps{
			ctx:     ctx,
			adapter: rt.Adapter,
			fileCfg: rt.FileCfg,
		}, rt.Session)
		if err != nil {
			return fmt.Errorf("resume --summarized: %w", err)
		}
		rt.Session = newSess
		if note != "" {
			fmt.Fprintln(os.Stdout, note)
		}
	}

	cwdRef := rt.CwdRef
	fileCfg := rt.FileCfg
	ad := rt.Adapter
	var updateCh <-chan update.Result
	if len(updateCheck) > 0 {
		updateCh = updateCheck[0]
	}

	// GitHub tool suite (PR/Issue composites + git_push) and the
	// WebSearchTool fallback are registered by Build now — shared with
	// oneshot/acp, not TUI-only. rt.GHClient is the same typed client
	// the tools use, kept here for the status bar's own direct
	// current-PR lookup (resolveCurrentPRCmd below), which has no ACP
	// equivalent.
	ghClient := rt.GHClient

	// Subagent teardown is DEFERRED (not inline after prog.Run) so it runs
	// even when prog.Run returns an error or panics: background workers run on
	// context.Background() and must never outlive the session (their
	// goroutines + provider SSE streams would leak), and empty dispatch
	// worktrees must be reclaimed regardless of how we exit. SIGKILL is still
	// uncatchable — nothing can defer through it — but error-returns and
	// panics, which previously skipped this entirely, are now covered.
	subagentTasks := rt.SubagentTasks
	defer func() {
		if n := subagentTasks.CancelAll(); n > 0 {
			drainSubagentsOnExit(subagentTasks)
		}
		sweepCtx, sweepCancel := context.WithTimeout(context.Background(), 5*time.Second)
		agent.ReclaimEmptyDispatchWorktrees(sweepCtx, subagentTasks)
		sweepCancel()
	}()
	// Sweep any empty dispatch worktrees a crashed session leaked — Build
	// already imported the resumed session's task index (the records it
	// carries have the worktree + base the reclaim check needs; the
	// at-exit defer above couldn't run if the previous process was
	// SIGKILLed or power-lost).
	{
		startupSweepCtx, startupSweepCancel := context.WithTimeout(context.Background(), 5*time.Second)
		agent.ReclaimEmptyDispatchWorktrees(startupSweepCtx, subagentTasks)
		// Then the same sweep driven off `git worktree list` rather than the
		// task index, which catches what the index cannot: worktrees from a
		// session that died before its records were ever saved, and from any
		// session other than the one just resumed. Both passes share the same
		// conservative keep-unless-provably-empty rule, so running them back to
		// back is safe — the second simply sees a wider set.
		if repoRoot, err := worktree.ResolveRepoRoot(startupSweepCtx, cwdRef.Get()); err == nil {
			agent.ReclaimOrphanDispatchWorktrees(startupSweepCtx, repoRoot)
		}
		startupSweepCancel()
	}

	// MCP client setup: the manager exists (Build constructed it) but
	// hasn't started — Start runs from the Bubble Tea Init cmd so the
	// TUI renders immediately. Servers initialize in the background;
	// tools are registered when the mcpStartupDoneMsg lands in Update
	// (cmd_mcp.go). Failed servers are non-fatal.
	mcpManager := rt.MCPManager

	cfg := rt.Cfg
	// Checkpoint store powers /checkpoints + Esc Esc. Failure here is
	// non-fatal — the feature simply stays disabled and the rest of
	// the TUI keeps working.
	cpStore, cpErr := checkpoint.New("")
	if cpErr == nil {
		cfg.Checkpoints = cpStore
		retention := fileCfg.Checkpoints.RetentionDays
		if retention <= 0 {
			retention = config.DefaultCheckpointRetentionDays
		}
		ttl := time.Duration(retention) * 24 * time.Hour
		go func() {
			_, _ = cpStore.Sweep(ttl)
		}()
	}

	// Build already opened the FTS5 index and registered session_recall
	// (shared with oneshot/acp — see agentruntime/runtime.go). TUI's own
	// addition on top: backfill the corpus in the background so a large
	// session history doesn't delay first paint, then embed any messages
	// lacking a current vector for auto-recall. Both skipped when the
	// index failed to open (idx nil — /recall just stays unavailable);
	// vector backfill is additionally skipped when embeddings are
	// unavailable or auto-recall is off, and resumes across restarts
	// since already-embedded messages are skipped. ctx cancellation (app
	// exit) stops it promptly.
	idx := rt.RecallIndex
	if idx != nil {
		ec := rt.EmbedClient
		autoRecall := fileCfg.Retrieval.SessionRecall.Auto
		go func() {
			_ = recall.Backfill(idx)
			if ec != nil && autoRecall {
				_ = idx.BackfillVectors(ctx, ec, ec.Model)
			}
		}()
	}

	// Resolve the project once, then decide the sensitivity posture from it.
	// Both feed auto-recall: projectRoots scopes what this project may pull
	// in, sensitiveRoots bounds what any quarantined project may ever emit.
	// Sensitivity keys off the repo root — the first entry — because that is
	// the path the user marks with `yottacode sensitive add`.
	projectRoots := sessionProjectRoots(ctx, cwd)
	sensitiveProject, sensitiveRoots, err := sensitivePosture(projectRoots[0])
	if err != nil {
		return err
	}

	branch := gitBranch(ctx, cwd)
	gitStatus := gitAheadBehind(ctx, cwd)

	model := New(ctx, Config{
		Cfg:                    cfg,
		Session:                rt.Session,
		UpdateCheck:            updateCh,
		ExperimentalEnabled:    rt.ExperimentalSet.EnabledNames(),
		SandboxActive:          rt.CmdSandbox != nil,
		Permissions:            rt.Permissions,
		Recall:                 idx,
		ModelName:              rt.Model,
		BaseURL:                opts.BaseURL,
		APIKey:                 opts.APIKey,
		Provider:               opts.ProviderKind,
		ProviderLabel:          wizard.CatalogIdentity(fileCfg.Active.Provider),
		ReasoningEffort:        opts.ReasoningEffort,
		EnableWebSearch:        opts.EnableWebSearch,
		DisableWebSearch:       opts.DisableWebSearch,
		EnableXSearch:          opts.EnableXSearch,
		EnableCodeInterpreter:  opts.EnableCodeInterpreter,
		SearchAllowedDomains:   opts.SearchAllowedDomains,
		SearchExcludedDomains:  opts.SearchExcludedDomains,
		XSearchAllowedHandles:  opts.XSearchAllowedHandles,
		XSearchExcludedHandles: opts.XSearchExcludedHandles,
		XSearchFromDate:        opts.XSearchFromDate,
		XSearchToDate:          opts.XSearchToDate,
		ProviderProfile:        ad.Profile(),
		Cwd:                    cwd,
		Version:                version.Current,
		Commit:                 version.Commit(),
		Dirty:                  version.Dirty(),
		Branch:                 branch,
		GitAhead:               gitStatus.ahead,
		GitBehind:              gitStatus.behind,
		ProjectRoots:           projectRoots,
		SensitiveProject:       sensitiveProject,
		SensitiveRoots:         sensitiveRoots,
		Worktree:               rt.Session.Worktree,
		MemorySummary:          rt.Mem.Summary().String(),
		BaseSystemPrompt:       rt.RawSystemPrompt,
		EmbedClient:            rt.EmbedClient,
		LSPManager:             rt.LSPManager,
		CodeMapProvider:        rt.CodeMapProvider,
		FileCfg:                fileCfg,
		RouterAdapters:         rt.RouterAdapters,
		RouterMode:             fileCfg.Router.Mode,
		Options:                opts,
		Subagents:              subagentTasks,
		AgentTool:              rt.AgentTool,
		CustomCommands:         customCmds,
		MCPManager:             mcpManager,
		Skills:                 rt.Skills,
		SkillTool:              rt.SkillTool,
		SummarizerAdapter:      routerFast(rt.RouterAdapters),
		SummarizerModel:        routerFastModel(rt.RouterAdapters),
	})
	model.githubClient = ghClient
	// Skills onboarding (skills installed but none enabled) is surfaced
	// inside the welcome card via startupTip() — see welcome.go's
	// memory > skills > rotating-pool priority. Emitting it as a
	// separate tea.Println here used to race with the welcome box's
	// per-row Println sequence, landing the notice above, below, or
	// even inside the box depending on Init batch timing.
	// Surface Build's non-fatal construction issues (router degrade,
	// embedding model unreachable, MCP server start failures — though
	// MCP itself starts later here, see DeferMCPStart above — skills/
	// subagents load warnings, unknown experimental flags) as startup
	// notices rather than raw stderr, so they render inside the TUI
	// instead of scrolling past before it even paints.
	for _, w := range rt.Warnings {
		model.pendingStartupNotices = append(model.pendingStartupNotices, styleAuto.Render(w))
	}
	// Surface custom-command load errors via the startup notice path
	// (historyLines is appendLine's queue; tea.Println replays it
	// once the program starts). Errors render in red, warnings in the
	// muted auto-mode style — same conventions other startup lines
	// already use.
	for _, e := range customErrs {
		var rendered string
		if e.Level == usercmd.LevelWarning {
			rendered = styleAuto.Render(fmt.Sprintf("[commands] %s", e.Error()))
		} else {
			rendered = styleError.Render(fmt.Sprintf("[commands] %s", e.Error()))
		}
		model.appendLine(rendered)
	}
	// Say so when this project is quarantined. A protection that engages
	// silently is one the user can't verify is on — and the visible absence of
	// "recalled N" is indistinguishable from simply having no relevant history.
	if sensitiveProject {
		model.pendingStartupNotices = append(model.pendingStartupNotices,
			styleAuto.Render("sensitive project: automatic session recall is off, and this project's "+
				"conversations are excluded from every other project's recall — `yottacode sensitive` to manage"))
	}
	// Confirm at startup which experimental features are on — without this
	// the gate left no in-session signal it was enabled. /experimental shows
	// the full catalog and detail.
	if names := rt.ExperimentalSet.EnabledNames(); len(names) > 0 {
		model.pendingStartupNotices = append(model.pendingStartupNotices,
			styleAuto.Render("experimental enabled: "+strings.Join(names, ", ")+" — /experimental for details"))
	}
	if langs, err := lsp.DetectWorkspace(ctx, cwd, 2000); err == nil {
		langs = lsp.ApplyOverridesToDetected(langs, fileCfg.LSP.Servers)
		if card := renderLSPAdvisory(langs); card != "" {
			model.pendingStartupNotices = append(model.pendingStartupNotices, card)
			model.pendingLSPSetupReminder = lspSetupReminder(langs)
		}
	}

	// Wire the AgentTool's background-completion callback into the
	// Model's long-lived inbox. The callback runs from a detached
	// goroutine when a background subagent finishes; non-blocking
	// send so a full inbox doesn't deadlock the runner.
	{
		inbox := model.subagentInbox
		rt.AgentTool.SetBackgroundDoneCallback(func(ev agent.SubagentBackgroundDone) {
			select {
			case inbox <- ev:
			default:
			}
		})
	}
	// Startup flags drop the freshly-built model into the requested
	// mode before the program starts. The entry log lines land in the
	// historyLines buffer; tea.Println replays them when the program
	// boots. --plan-resume wins over --permission-mode (resume implies
	// plan); --yolo is an orthogonal overlay
	// that stacks with whichever mode (if any) is requested.
	switch {
	case opts.PlanResume != "":
		plans, err := agent.ListPlans()
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: --plan-resume: %v\n", err)
			model, _ = togglePlanMode(model)
		case len(plans) == 0:
			fmt.Fprintf(os.Stderr, "warning: --plan-resume %q: no saved plans found; starting a fresh plan instead\n", opts.PlanResume)
			model, _ = togglePlanMode(model)
		default:
			match := agent.MatchPlan(plans, opts.PlanResume)
			if match == nil {
				fmt.Fprintf(os.Stderr, "warning: --plan-resume %q: no match; starting a fresh plan instead\n", opts.PlanResume)
				model, _ = togglePlanMode(model)
			} else {
				resumePlanFile(&model, *match)
			}
		}
	case opts.PermissionMode == "plan":
		model, _ = togglePlanMode(model)
	case opts.PermissionMode == "auto":
		model, _ = toggleAutoMode(model)
	}
	if opts.BypassPermissions {
		model = enterYoloMode(model)
	}
	// Alt-screen full-screen mode: the TUI owns the whole frame, including
	// an app-owned scrollable transcript viewport — see model.go's View.
	//
	// Restore the real terminal background (if a theme ever repainted it —
	// see tea.View.BackgroundColor in Model.View) before we hand the
	// terminal back. Deferred here, ahead of prog.Run, so it runs even on
	// a panic or early error-return from the run loop — same discipline
	// as the subagent-teardown defer above. capturedTerminalBackground
	// (terminal_background.go) is read at defer-EXECUTION time via this
	// closure, not registration time, since it isn't populated yet here
	// (it fills in asynchronously once Init's tea.RequestBackgroundColor
	// gets a reply).
	defer func() {
		restoreTerminalBackground(capturedTerminalBackground)
	}()
	prog := tea.NewProgram(model)
	finalModel, runErr := prog.Run()
	if fm, ok := finalModel.(Model); ok {
		model = fm
	}
	// Apply the user's Ctrl+C worktree-exit choice after Bubble Tea has
	// restored the terminal, keeping cleanup outside the renderer/update loop.
	if cleanup := model.worktreeExitCleanup; cleanup != "" {
		if repoRoot, err := cleanupCurrentWorktreeOnExitWithTimeout(model.cwd, model.worktreeExitConfirmName, cleanup); err != nil {
			if runErr == nil {
				runErr = err
			} else {
				fmt.Fprintf(os.Stderr, "worktree cleanup failed: %v\n", err)
			}
		} else if repoRoot != "" {
			applyWorktreeExitRepoRoot(&model, cwdRef, repoRoot)
		}
	}
	// Everything below is shutdown, and it must run on EVERY exit path.
	// This used to be `if err != nil { return }`, which was wrong: Ctrl+C
	// does not always reach us as a keystroke. Bubbletea only sees ^C as a
	// KeyCtrlC (-> tea.Quit -> nil) while the terminal is in raw mode; a
	// real SIGINT — stdin isn't a TTY, ^C lands during startup before raw
	// mode is set or after it's restored, or someone sends kill -INT —
	// hits bubbletea's own signal handler and comes back as
	// ErrInterrupted/ErrProgramKilled. The old early return then skipped
	// the sess.Save + resumeHint below, so the user got "error: program
	// was killed: program was interrupted" and no "sessions resume <id>"
	// line to get back in. Interrupts are an ordinary way to leave
	// yottacode, not a TUI failure — only a genuine failure propagates.
	normalExit := isNormalExit(runErr)

	// Subagent cancel + drain + worktree sweep now run in the deferred
	// teardown registered right after subagentTasks was created, so an
	// error-return or panic from prog.Run can't skip them.

	// Tear down MCP subprocesses before the index/session close so a
	// slow shutdown can't leak servers past yottacode's lifetime. The
	// context here is short-bounded; the SDK's CommandTransport.Close
	// runs its own SIGTERM/SIGKILL ladder.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	mcpManager.Stop(shutdownCtx)
	shutdownCancel()
	if idx != nil {
		_ = idx.Close()
	}
	// Persist the subagent task index alongside the session so its task-ids
	// resolve on a later resume. (The deferred teardown's CancelAll runs after
	// this save, so a task still running at exit persists as "running" and
	// rehydrates as orphaned next launch.)
	rt.Session.SubagentTasks = subagentTasks.Export()
	// Only sessions that actually held a conversation get written. session.New
	// doesn't touch the disk, so this at-exit Save is what creates the file —
	// and saving unconditionally meant every "open yottacode, change my mind,
	// quit" left a ~48KB system-prompt-only shell behind. Those shells then
	// showed up as resumable in /sessions and could be picked by --continue,
	// where they open with an empty transcript and read as lost history.
	// Skipping the write is what keeps them out of the store; List and
	// LatestInCwd filter the ones older builds already wrote.
	var saveErr error
	if rt.Session.HasExchange() {
		saveErr = rt.Session.Save()
	}
	// Only advertise a resume once the transcript is actually on disk —
	// pointing the user at an id that failed to persist is worse than
	// staying quiet.
	if saveErr == nil {
		if hint := resumeHint(rt.Session); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
	}
	switch {
	case !normalExit:
		return fmt.Errorf("tui: %w", runErr)
	case saveErr != nil:
		return saveErr
	}
	return nil
}

// isNormalExit reports whether a bubbletea Program.Run error represents an
// ordinary way of leaving yottacode rather than a TUI failure.
//
// Run returns ErrInterrupted when it takes a SIGINT (the ^C the terminal
// couldn't hand us as a keystroke because it wasn't in raw mode) and
// ErrProgramKilled when the program is killed or its context is cancelled;
// both wrap through to the caller, so errors.Is is required rather than ==.
// Treating these as failures is what used to cost an interrupted session its
// save and its resume hint.
func isNormalExit(err error) bool {
	return err == nil ||
		errors.Is(err, tea.ErrInterrupted) ||
		errors.Is(err, tea.ErrProgramKilled)
}

// resumeHint returns the one-line "how to come back to this session"
// nudge printed after the TUI exits. Returns "" for sessions that
// never reached an actual exchange (the user opened yottacode and
// quit immediately) — printing a resume command for an empty
// transcript is just noise and pollutes ~/.yottacode/sessions/ with
// drive-by ids the user doesn't want to see in /sessions.
//
// Prefers the saved name when one is set via the /sessions Rename
// action, since names are the friendlier reference; falls back to
// the timestamp id otherwise.
func resumeHint(sess *session.Session) string {
	if !sessionHasExchange(sess) {
		return ""
	}
	ref := sess.ID
	if sess.Name != "" {
		ref = sess.Name
	}
	return "To resume this session, run:\nyottacode sessions resume " + ref
}

// sessionHasExchange reports whether the session contains at least one
// user or assistant message. System-only sessions don't count — those
// are the empty shells produced by `yottacode` → quit, with no actual
// conversation to resume.
//
// Delegates to session.HasExchange rather than re-implementing it: the same
// predicate decides whether to persist the session at exit and whether
// LatestInCwd/List will offer it later, and those answers must not diverge.
func sessionHasExchange(sess *session.Session) bool {
	return sess.HasExchange()
}

// splitAllowPaths splits a comma-separated --allow-paths value into a
// slice, dropping empty entries and trimming whitespace. Returns nil
// when the input is empty so WritePathOptions doesn't carry a slice
// it never uses.
func splitAllowPaths(s string) []string {
	return splitCSV(s)
}

// ensureWorkspaceTrust fires the first-launch trust gate. Loads the
// user-scope store, checks IsTrusted against cwd + the resolved
// --allow-paths roots, and either no-ops (already trusted),
// drives the Bubbletea picker (interactive + untrusted), or skips
// (non-TTY).
//
// The store is loaded each launch — it's small and the user can
// edit `yottacode trust remove` between runs, so we don't cache.
//
// When cwd is a yottacode-managed worktree (under ~/.yottacode/worktrees/
// after the relocation), trust is checked against the *originating* repo
// — the worktree itself is never in the user's persistent trust set,
// but its parent repo is. Resolves the originating repo via
// `git rev-parse --git-common-dir`, which works inside a linked worktree.
func ensureWorkspaceTrust(cwd string, opts cli.ChatOptions) error {
	storePath, err := trust.DefaultStorePath()
	if err != nil {
		return fmt.Errorf("trust store path: %w", err)
	}
	store, err := trust.Load(storePath)
	if err != nil {
		return fmt.Errorf("load trust store: %w", err)
	}
	allowPaths := splitAllowPaths(opts.AllowPaths)

	// If cwd is inside a yottacode-managed worktree, the trust gate
	// belongs to the originating repo, not the worktree dir. Resolve
	// back via git's common-dir lookup; on success, run the gate
	// against the repo root. On failure (e.g. worktree no longer
	// linked), fall through to checking cwd directly so we still
	// produce a useful error message.
	gateTarget := cwd
	if _, _, ok := worktree.IsAnyWorktreePath(cwd); ok {
		if repoRoot, err := worktree.ResolveRepoRoot(context.Background(), cwd); err == nil {
			gateTarget = repoRoot
		}
	}

	if !trust.IsInteractiveStream(os.Stdin) || !trust.IsInteractiveStream(os.Stdout) {
		// Non-TTY (piped, CI): skip the trust gate entirely.
		// Matches Claude Code's `-p` behavior.
		return trust.Ensure(store, storePath, gateTarget, allowPaths, false, os.Stdin, os.Stderr)
	}
	return trust.EnsureInteractive(store, storePath, gateTarget, allowPaths)
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

// routerImplementer returns the implementer-model streamer for routing, or
// nil when routing is disabled (ra == nil). Returning a typed nil-free
// interface keeps AgentTool.ImplementerAdapter genuinely nil so routeChildModel
// falls back to the parent adapter.
func routerImplementer(ra *cli.RouterAdapters) agent.Streamer {
	if ra == nil || ra.Implementer == nil {
		return nil
	}
	return ra.Implementer
}

func routerImplementerModel(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.ImplementerModel
}

func routerImplementerRef(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.ImplementerRef
}

// routerAdvisor returns the advisor-model streamer for routing, or nil when
// routing is disabled.
func routerAdvisor(ra *cli.RouterAdapters) agent.Streamer {
	if ra == nil || ra.Advisor == nil {
		return nil
	}
	return ra.Advisor
}

func routerAdvisorModel(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.AdvisorModel
}

func routerAdvisorRef(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.AdvisorRef
}

// routerFast returns the legacy fast/implementer streamer for older call sites.
func routerFast(ra *cli.RouterAdapters) agent.Streamer { return routerImplementer(ra) }

func routerFastModel(ra *cli.RouterAdapters) string { return routerImplementerModel(ra) }

// routerResolve adapts RouterAdapters.Resolve (func → adapter.Streamer)
// to the agent package's func → agent.Streamer signature the AgentTool
// expects. Returns nil when routing is disabled.
func routerResolve(ra *cli.RouterAdapters) func(string) agent.Streamer {
	if ra == nil || ra.Resolve == nil {
		return nil
	}
	return func(model string) agent.Streamer {
		s := ra.Resolve(model)
		if s == nil {
			return nil
		}
		return s
	}
}

// routerModelResolver gates explicit model-frontmatter routing on the router
// mode. A configured pair may be built while routing is off so /router can
// toggle live, but off mode promises every subagent inherits the active model.
func routerModelResolver(ra *cli.RouterAdapters, enabled bool) func(string) agent.Streamer {
	if !enabled {
		return nil
	}
	return routerResolve(ra)
}

// routerSummarizer returns the fast-model summarizer only in auto mode. Manual
// routing resolves explicit subagent model pins but keeps compaction on the
// active model.
func routerSummarizer(ra *cli.RouterAdapters, auto bool) (agent.Streamer, string) {
	if !auto {
		return nil, ""
	}
	return routerFast(ra), routerFastModel(ra)
}

func composeSystemPrompt(base string, profile adapter.ProviderProfile) string {
	if profile.Provider == adapter.ProviderXAI && hasBuiltin(profile.EnabledBuiltinTools, adapter.BuiltinToolXSearch) {
		return base + "\nFor live or current information, use provider-native tools when needed. For X/Twitter posts, users, threads, trends, sentiment, or anything happening on X, use x_search, not web_search. Use web_search only for general web pages, news sites, docs, or pages outside X."
	}
	if hasBuiltin(profile.EnabledBuiltinTools, adapter.BuiltinToolWebSearch) {
		return base + "\nFor live or current information, use the provider-native web_search tool when needed."
	}
	return base + "\nFor live or current information, use the web_search tool to search the web via DuckDuckGo, or fetch_url for specific pages or feeds when needed."
}

// appendSkillsSection frames the Agent Skills surface in the system
// prompt: what skills are and how to invoke them. It deliberately does
// NOT enumerate the skills — that name+description list is the load-
// bearing content of the `Skill` tool's own schema description (see
// SkillTool.Description), which is always in the window. Listing it here
// too would duplicate the metadata tier in every turn (system prompt +
// tool schema), doubling its token cost for no gain. The framing points
// the model at the tool's list instead. Empty set → no section at all.
func appendSkillsSection(base string, loaded []skills.Skill) string {
	if len(loaded) == 0 {
		return base
	}
	return base + "\n\n# Available skills\n\n" +
		"You have access to reusable capability playbooks (Agent Skills). When a " +
		"user request matches a skill's described scope, invoke it via the `Skill` " +
		"tool (e.g. `Skill(skill=\"<name>\")`); the tool returns the skill's body so " +
		"you can apply it in the current turn. The `Skill` tool's description lists " +
		"every available skill by name and scope — consult that list and only invoke " +
		"a name that appears there.\n"
}

func hasBuiltin(tools []adapter.BuiltinToolKind, want adapter.BuiltinToolKind) bool {
	return slices.Contains(tools, want)
}

// gitBranch reads the current git branch via `git -C <cwd> branch --show-current`.
// Returns "" if cwd isn't a repo or git isn't installed — both are normal.
//
// The call is bounded by a short timeout (and honors ctx cancellation) so a
// wedged git — a locked repo, a slow NFS mount — can't hang TUI startup.
func gitBranch(ctx context.Context, cwd string) string {
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type gitAheadBehindStatus struct {
	ahead  int
	behind int
}

// gitAheadBehind reports the current branch's divergence from its upstream when
// configured, otherwise from the default branch. It is best-effort status chrome:
// git missing, detached HEAD, no base, or command timeouts all collapse to zero.
func gitAheadBehind(ctx context.Context, cwd string) gitAheadBehindStatus {
	if _, err := exec.LookPath("git"); err != nil {
		return gitAheadBehindStatus{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	base := strings.TrimSpace(gitCommandOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"))
	if base == "" {
		base = strings.TrimSpace(gitCommandOutput(ctx, cwd, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"))
	}
	if base == "" {
		for _, candidate := range []string{"origin/main", "origin/master", "origin/develop", "main", "master", "develop"} {
			if strings.TrimSpace(gitCommandOutput(ctx, cwd, "rev-parse", "--verify", candidate)) != "" {
				base = candidate
				break
			}
		}
	}
	if base == "" {
		return gitAheadBehindStatus{}
	}
	out := strings.TrimSpace(gitCommandOutput(ctx, cwd, "rev-list", "--left-right", "--count", base+"...HEAD"))
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return gitAheadBehindStatus{}
	}
	behind, errBehind := strconv.Atoi(fields[0])
	ahead, errAhead := strconv.Atoi(fields[1])
	if errBehind != nil || errAhead != nil || ahead < 0 || behind < 0 {
		return gitAheadBehindStatus{}
	}
	return gitAheadBehindStatus{ahead: ahead, behind: behind}
}

func gitCommandOutput(ctx context.Context, cwd string, args ...string) string {
	cmdArgs := append([]string{"-C", cwd}, args...)
	out, err := exec.CommandContext(ctx, "git", cmdArgs...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// sessionProjectRoots resolves every directory tree that counts as "this
// project", once, at startup. Auto-recall's project scope matches session cwds
// against them (see recall.projectScopeClause), and resolving here keeps a git
// subprocess off the per-turn path.
//
// The first entry is always the repo root and is what the sensitivity gate
// keys off. ResolveRepoRoot walks back to the *main* repo even from inside a
// yottacode worktree, so a worktree session recalls the whole repo's history
// rather than only its own.
//
// The second entry is the repo's worktree container,
// ~/.yottacode/worktrees/<repo-slug>/. Worktrees live outside the repo tree
// entirely, so without it two worktrees of one repo — or a worktree and the
// main checkout — could never recall each other despite being the same work.
// The slug embeds a hash of the repo root, so this can't pull in another
// repo's worktrees.
//
// Not a git repo, or git missing, yields just the plain cwd — reproducing the
// exact-match behaviour recall had before any of this.
func sessionProjectRoots(ctx context.Context, cwd string) []string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := worktree.ResolveRepoRoot(ctx, cwd)
	if err != nil || strings.TrimSpace(root) == "" {
		return []string{cwd}
	}
	return []string{root, worktree.SlugDir(root)}
}

// sensitivePosture reports whether projectRoot is a sensitive project, and
// returns every directory tree that should be excluded for sensitive projects.
//
// Both halves matter and they are not the same question. The bool gates
// auto-recall *into* this project; the slice bounds what any sensitive project
// — including ones unrelated to this session — may emit into this project's
// recall, which is why the whole list is needed rather than just this repo's
// status. Each marked root expands to its managed-worktree container too: a
// session launched from ~/.yottacode/worktrees/<repo-slug>/... carries the same
// sensitive content as the main checkout and must not bypass the outbound gate.
//
// A malformed store is a hard startup error rather than a degrade-to-empty.
// Treating an unreadable sensitivity list as "nothing is sensitive" would turn
// a typo into a silent loss of PHI protection — the one failure mode this
// feature exists to prevent. Same stance config.LoadDefault takes.
func sensitivePosture(projectRoot string) (bool, []string, error) {
	path, err := sensitive.DefaultStorePath()
	if err != nil {
		return false, nil, err
	}
	store, err := sensitive.Load(path)
	if err != nil {
		return false, nil, err
	}
	return store.Contains(projectRoot), sensitiveRecallRoots(store.Paths()), nil
}

// sensitiveRecallRoots expands user-marked sensitive roots into every path tree
// auto-recall must suppress. Marking the main repo root covers both sessions
// recorded under that checkout and sessions recorded under yottacode-managed
// worktrees for the same repo.
func sensitiveRecallRoots(roots []string) []string {
	out := make([]string, 0, len(roots)*2)
	seen := make(map[string]struct{}, len(roots)*2)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		for _, candidate := range []string{root, worktree.SlugDir(root)} {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}
