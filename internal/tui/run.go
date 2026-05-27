package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/checkpoint"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/experimental"
	githubapi "github.com/yottadynamics/yottacode/internal/github"
	"github.com/yottadynamics/yottacode/internal/mcp"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/skills"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/trust"
	"github.com/yottadynamics/yottacode/internal/usercmd"
	"github.com/yottadynamics/yottacode/internal/version"
	"github.com/yottadynamics/yottacode/internal/wizard"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// defaultSystemPrompt is sourced from internal/agent so the TUI and
// the oneshot runner cannot drift. See agent.DefaultSystemPrompt.
const defaultSystemPrompt = agent.DefaultSystemPrompt

// Run wires up session, permissions, adapter, and tools, then drives
// the Bubbletea program. The non-interactive sibling is oneshot.Run, which
// shares the same ChatOptions but emits one turn to stdout and exits.
func Run(ctx context.Context, opts cli.ChatOptions) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Folder-trust gate: fires before openSession so an untrusted
	// workspace never accumulates session state. Subfolders of any
	// previously trusted root inherit trust; --allow-paths roots
	// satisfy the gate session-only; YOTTACODE_TRUST_ALL=1 is the
	// CI escape hatch. See yottacode-roadmap/folder-trust.md.
	if err := ensureWorkspaceTrust(cwd, opts); err != nil {
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
	// Load slash commands from two scopes merged by precedence:
	// project (<cwd>/.yottacode/commands/) > user (~/.yottacode/commands/).
	// Fail-soft: per-file load errors are surfaced as startup notices
	// below but never block launch. Shadow warnings fire when user
	// and project name-collide.
	customCmds, customErrs := usercmd.Load(cwd)
	// Load tunables (~/.yottacode/config.toml). Missing file → defaults
	// (no error). Invalid file → return the error so the user fixes it
	// rather than silently running with stale defaults.
	fileCfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	baseSys := opts.SystemPrompt
	if baseSys == "" {
		baseSys = defaultSystemPrompt
	}
	// Routing: when [router].enabled is true in config.toml, dispatch
	// across the configured candidates per the chosen policy. The
	// returned Client implements both Streamer (for the agent loop)
	// and Profile() (for system-prompt composition / connection
	// probe). When routing is disabled, fall back to the original
	// single-adapter dispatch path.
	var ad adapter.Client
	if router, err := cli.BuildRouter(fileCfg, opts); err != nil {
		return err
	} else if router != nil {
		ad = router
	} else {
		ad = adapter.NewWithConfig(adapter.Config{
			BaseURL:                opts.BaseURL,
			APIKey:                 opts.APIKey,
			Model:                  opts.Model,
			ProviderOverride:       adapter.Provider(strings.TrimSpace(opts.ProviderKind)),
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
		})
	}
	// Load skills early so the resolved set can flow into both the
	// system prompt (description-matched metadata tier) and the Skill
	// tool registration below. Loading twice would be wasteful and
	// risks the prompt and the tool drifting; this single load wins.
	skillsRes, _ := skills.LoadAll(cwd, usercmd.Reserved)
	for _, w := range skillsRes.Warnings {
		fmt.Fprintln(os.Stderr, "skills: "+w)
	}
	composedBase := composeSystemPrompt(baseSys, ad.Profile())
	composedBase = appendSkillsSection(composedBase, skillsRes.Skills)
	if fresh {
		sess.Messages = append(sess.Messages, adapter.Message{
			Role:    adapter.RoleSystem,
			Content: memory.SystemPrompt(composedBase, mem),
		})
	} else {
		recomposeSessionSystemPrompt(sess, memory.SystemPrompt(composedBase, mem))
	}

	// `--summarized` (only meaningful when resuming): replace the loaded
	// transcript with a structured summary injected into the system
	// prompt, the same way the in-TUI /sessions picker's `s`-toggle on
	// the Resume row does.
	if opts.Summarized && !fresh {
		newSess, _, note, err := loadSummarizedSession(summarizeDeps{
			ctx:     ctx,
			adapter: ad,
			fileCfg: fileCfg,
		}, sess)
		if err != nil {
			return fmt.Errorf("resume --summarized: %w", err)
		}
		sess = newSess
		if note != "" {
			fmt.Fprintln(os.Stdout, note)
		}
	}

	// Permissions are project-local: .yottacode/permissions.json
	// (committable team rules) + .yottacode/permissions.local.json
	// (gitignored personal additions). Missing files → empty rule
	// set; malformed file → returned error so the user can fix it
	// instead of silently running with stale rules.
	perms, err := permissions.Load(cwd)
	if err != nil {
		return err
	}

	// cwdRef is the shared working-directory holder every tool reads
	// from. enter_worktree / exit_worktree call cwdRef.Set(...) (plus
	// os.Chdir) so a mid-session worktree swap propagates to all
	// tools without rebuilding the registry. WriteOpts holds the
	// same pointer so write validation tracks the swap too — important
	// now that yottacode worktrees live in ~/.yottacode/worktrees/,
	// outside the original cwd's pathUnder perimeter.
	cwdRef := agent.NewCwdRef(cwd)

	// Path-validation policy: every mutating filesystem tool gets the
	// same WriteOpts. Cwd-confined writes by default; --allow-paths
	// expands the allowed roots; the deny list is hardcoded to keep
	// yottacode's own state and git internals off-limits.
	writeOpts := agent.WritePathOptions{
		Cwd:          cwdRef,
		AllowedPaths: splitAllowPaths(opts.AllowPaths),
		DenyExact:    agent.DefaultDenyPaths(cwd),
	}
	denyReads := agent.DefaultDenyReadPaths(cwd)

	planStore := agent.NewPlanStore()
	if len(sess.Todos) > 0 {
		planStore.Replace(sess.Todos)
	}

	// Mode flags shared between LoopConfig and the TUI Model:
	//   - plan (Shift+Tab cycle + /plan slash + --permission-mode plan
	//     startup flag + plan-card [Y])
	//   - auto (Shift+Tab cycle + --permission-mode auto startup flag
	//     + plan-card [Y]; no slash command, mirroring Claude Code)
	//   - yolo (--dangerously-skip-permissions startup flag only; no
	//     slash command, no keybinding — opt-in once per process)
	// Plan and auto are mutually exclusive; yolo is an orthogonal
	// overlay that stacks with either. Per-session lifetime;
	// always-off at startup unless the corresponding flag was passed.
	planMode := &agent.PlanModeState{}
	autoMode := &agent.AutoModeState{}
	yoloMode := &agent.YoloModeState{}

	reg := agent.NewRegistry()
	reg.Register(&agent.ReadFileTool{Cwd: cwdRef, DenyReadPaths: denyReads})
	reg.Register(&agent.ReadManyFilesTool{Cwd: cwdRef, DenyReadPaths: denyReads})
	reg.Register(&agent.WriteFileTool{Cwd: cwdRef, WriteOpts: writeOpts})
	reg.Register(&agent.EditFileTool{Cwd: cwdRef, WriteOpts: writeOpts})
	reg.Register(&agent.ApplyDiffTool{Cwd: cwdRef, WriteOpts: writeOpts})
	reg.Register(&agent.MkdirTool{Cwd: cwdRef, WriteOpts: writeOpts})
	reg.Register(&agent.CopyFileTool{Cwd: cwdRef, WriteOpts: writeOpts})
	reg.Register(&agent.MoveFileTool{Cwd: cwdRef, WriteOpts: writeOpts})
	reg.Register(&agent.DeleteFileTool{Cwd: cwdRef, WriteOpts: writeOpts})
	reg.Register(&agent.ListGitChangedFilesTool{Cwd: cwdRef})
	reg.Register(&agent.GitBranchStatusTool{Cwd: cwdRef})
	reg.Register(&agent.GitShowFileAtRevTool{Cwd: cwdRef})
	reg.Register(&agent.GitDiffFilesTool{Cwd: cwdRef})
	reg.Register(&agent.GitStageFilesTool{Cwd: cwdRef})
	reg.Register(&agent.GitUnstageFilesTool{Cwd: cwdRef})
	reg.Register(&agent.GitCreateBranchTool{Cwd: cwdRef})
	reg.Register(&agent.GitCommitTool{Cwd: cwdRef})
	// Git worktree tools. Layer 1 (enter/exit/status) are the agent-
	// friendly entry points; Layer 2 (the git_worktree_* wrappers) sit
	// underneath for finer-grained admin. enter_worktree and
	// exit_worktree are in the auto-mode safety floor — they always
	// prompt, even when auto mode is on, because they shift the agent's
	// working context (and exit's force-remove is destructive).
	reg.Register(&agent.EnterWorktreeTool{Cwd: cwdRef})
	reg.Register(&agent.ExitWorktreeTool{Cwd: cwdRef})
	reg.Register(&agent.WorktreeStatusTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeListTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeAddTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeRemoveTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeLockTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeUnlockTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreePruneTool{Cwd: cwdRef})
	// Composite commit-workflow tools paired with the /commit slash
	// command (cmd_commit.go). Context is read-only and parallel-safe;
	// apply is approval-gated and validates the subject in Go before
	// invoking git, so empty-staging / oversize / trailing-period /
	// multi-line messages can't reach a `git commit` invocation no
	// matter what the model emits.
	reg.Register(&agent.GitCommitContextTool{Cwd: cwdRef})
	reg.Register(&agent.GitCommitApplyTool{Cwd: cwdRef})
	// Composite PR-workflow tools paired with the /create-pr slash
	// command (cmd_create_pr.go). Context is read-only (no network
	// beyond a cheap `git ls-remote` push-state check); create is
	// approval-gated and validates the title in Go before dialing the
	// github.Interface. The Interface is the foundation hook for
	// v0.5.0's typed go-github client — swapping the ShellOut for the
	// typed client is a one-line registration change.
	// In-session cache wraps the typed client so duplicate reads
	// within one turn make one API call. The cache lives for the
	// session and is cleared at process exit — no explicit
	// teardown needed. Writes (CreatePR, UpdatePR, AddPRComment)
	// pass through; UpdatePR invalidates matching ReadPR entries
	// so the next read sees fresh data.
	ghClient := githubapi.NewCachingClient(githubapi.NewTypedClient(cwd))
	reg.Register(&agent.GHPRContextTool{Cwd: cwdRef})
	reg.Register(&agent.GHPRCreateTool{Cwd: cwdRef, GH: ghClient})
	// gh_pr_review_context is the read-side composite paired with
	// /git-review-pr. Shares the same github.Interface instance as
	// gh_pr_create so the v0.5.0 swap to a typed go-github client
	// changes one variable above instead of two registration sites.
	reg.Register(&agent.GHPRReviewContextTool{Cwd: cwdRef, GH: ghClient})
	// gh_pr_read is the lightweight metadata-only sibling — one
	// API call vs. review_context's three. The model picks between
	// them based on whether it needs the diff + checks (review) or
	// just metadata (read). The Description on each tool spells out
	// the selection rule so the model doesn't reach for run_bash
	// `gh pr view --json body`.
	reg.Register(&agent.GHPRReadTool{Cwd: cwdRef, GH: ghClient})
	// Issue-side counterparts: gh_issue_read for single-issue
	// metadata + comments (the /git-implement-issue command's
	// first step), gh_issue_list for filtered open-issue
	// summaries. Same nudge-the-model-away-from-run_bash framing
	// as the PR tools.
	reg.Register(&agent.GHIssueReadTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GHIssueListTool{Cwd: cwdRef, GH: ghClient})
	// git_push is paired with /git-push. The GH dependency is for
	// the best-effort PR-URL lookup after a successful push — a
	// nil client would skip the lookup silently, but registering
	// with the shared ghClient gives us the "PR updated: <url>"
	// footer for free.
	reg.Register(&agent.GitPushTool{Cwd: cwdRef,GH: ghClient})
	// gh_pr_update is paired with /git-update-pr. Same Interface
	// instance as the other PR tools — the v0.5.0 typed client
	// swap will switch one variable above and pick up all four
	// (create/read-review/push-lookup/update) at once.
	reg.Register(&agent.GHPRUpdateTool{Cwd: cwdRef, GH: ghClient})
	// gh_pr_add_comment posts a conversation-level comment on a PR.
	// Approval-gated like the other write tools. Used for
	// cross-linking related issues, post-review follow-ups, and
	// structured summaries the model wants public on the PR.
	reg.Register(&agent.GHPRAddCommentTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GitLogFileTool{Cwd: cwdRef})
	reg.Register(&agent.GitBlameLinesTool{Cwd: cwdRef})
	reg.Register(&agent.GitMergeBaseTool{Cwd: cwdRef})
	reg.Register(&agent.GitCheckpointTool{Cwd: cwdRef})
	reg.Register(&agent.RollbackTool{Cwd: cwdRef})
	reg.Register(&agent.RunTestsTool{Cwd: cwdRef})
	reg.Register(&agent.RunBashTool{Cwd: cwdRef})
	reg.Register(&agent.ListDirTool{Cwd: cwdRef})
	reg.Register(&agent.ListProjectStructureTool{Cwd: cwdRef})
	reg.Register(&agent.GlobTool{Cwd: cwdRef})
	reg.Register(&agent.GrepTool{Cwd: cwdRef, DenyReadPaths: denyReads})
	reg.Register(&agent.FetchURLTool{})
	if !hasBuiltin(ad.Profile().EnabledBuiltinTools, adapter.BuiltinToolWebSearch) {
		reg.Register(&agent.WebSearchTool{})
	}
	reg.Register(&agent.MemorySaveTool{Cwd: cwdRef})
	reg.Register(&agent.MemoryForgetTool{Cwd: cwdRef})
	reg.Register(&agent.GitTool{Cwd: cwdRef})
	reg.Register(&agent.TodoWriteTool{Store: planStore})
	// ExitPlanModeTool is registered with a nil Approve callback at
	// startup; cmdPlan wires the callback (and the plan-file slug)
	// on /plan entry. The adapter-tools filter in the loop hides
	// this tool from the model's schema until plan mode is active,
	// so the nil-callback path is unreachable from the model.
	reg.Register(&agent.ExitPlanModeTool{})

	// Subagents: load definitions (built-in + ~/.yottacode/agents +
	// .yottacode/agents) and register the Agent dispatch tool. The
	// background-done callback is wired below after the Model exists
	// so completions route to the long-lived inbox the Model owns.
	subRes, _ := subagents.LoadAll(cwd, reg.Names())
	for _, w := range subRes.Warnings {
		fmt.Fprintln(os.Stderr, "subagents: "+w)
	}
	transcriptDir, _ := subagents.EnsureTranscriptDir(cwd)
	subagentTasks := subagents.NewRegistry()
	// Resolve experimental features. CLI > env > config; the cli
	// package already merged CLI flags + env into opts.Experimental.
	// Here we layer the [experimental] config section underneath
	// (later additions wouldn't override earlier ones — we use the
	// Set's Enable as the union operator).
	expSet := experimental.NewSet()
	for name, on := range fileCfg.Experimental {
		if on {
			expSet.Enable(name)
		}
	}
	for _, name := range opts.Experimental {
		expSet.Enable(name)
	}
	for _, unknown := range expSet.UnknownNames() {
		fmt.Fprintf(os.Stderr, "warning: --experimental %q is not a recognized feature (typo? graduated? see docs/experimental.md)\n", unknown)
	}

	agentTool := &agent.AgentTool{
		Configs:        subRes.Configs,
		Tasks:          subagentTasks,
		Adapter:        ad,
		ParentRegistry: reg,
		Permissions:    perms,
		YoloMode:       yoloMode,
		PlanMode:       planMode,
		AutoMode:       autoMode,
		Cwd:            cwdRef,
		TranscriptDir:  transcriptDir,
		// Background subagents are an opt-in experimental feature.
		// When the gate is off, `run_in_background:true` returns a
		// recoverable error the model relays to the user (see
		// AgentTool.Execute). Foreground subagents are always on.
		AllowBackground: expSet.IsEnabled(experimental.BackgroundSubagents),
	}
	reg.Register(agentTool)
	// Pair with the Agent tool: lets the parent fetch a previously-
	// dispatched (typically background) subagent's final result by
	// task id. Without this tool, background runs are fire-and-forget
	// — their results live in the transcript on disk and the in-memory
	// registry, but the parent's model has no way to pull a result
	// back into the conversation. With it, "what did the background
	// subagent find?" becomes one tool call.
	reg.Register(&agent.GetSubagentResultTool{Tasks: subagentTasks})

	// MCP client setup: construct the manager now but defer Start to
	// the Bubble Tea Init cmd so the TUI renders immediately. Servers
	// initialize in the background; tools are registered when the
	// mcpStartupDoneMsg lands in Update. Failed servers are non-fatal.
	mcpManager := mcp.NewManager(fileCfg.MCPServers)

	// Agent Skills tool. Skills are loaded up-front (alongside the
	// system-prompt composition) so the same resolved set drives both
	// the description-matched metadata tier in the prompt and the
	// model-facing Skill tool. User-typed /<skill-name> dispatches
	// through the TUI Model's skillSlash (built from c.Skills below)
	// and works regardless of enablement — typing the slash IS the
	// selection.
	//
	// Default policy: NO skills enabled at session start. The model
	// sees an empty skills section in the prompt until the user opens
	// /skills and picks some. This keeps the prompt small and avoids
	// the model reaching for a skill the user didn't ask about. Slash
	// invocations stay available so a user who knows the name can
	// always pull a skill in one keystroke.
	skillTool := &agent.SkillTool{All: skillsRes.Skills}
	skillTool.SetEnabled(map[string]bool{}) // empty map = none enabled
	reg.Register(skillTool)

	cfg := agent.LoopConfig{
		Adapter:           ad,
		Registry:          reg,
		Permissions:       perms,
		BypassPermissions: opts.BypassPermissions,
		Cwd:               cwdRef,
		MaxIterations:     opts.MaxIterations,
		PlanMode:          planMode,
		AutoMode:          autoMode,
		YoloMode:          yoloMode,
	}

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

	// Open the FTS5 index. A failure here is non-fatal — /recall just
	// becomes unavailable and the rest of yottacode runs fine. Backfill
	// runs in a goroutine so a large session corpus doesn't delay the UI.
	idx, recallErr := recall.Open()
	if recallErr == nil {
		go func() {
			_ = recall.Backfill(idx)
		}()
	}

	model := New(ctx, Config{
		Cfg:                    cfg,
		Session:                sess,
		Permissions:            perms,
		Recall:                 idx,
		ModelName:              opts.Model,
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
		BypassPermissions:      opts.BypassPermissions,
		Version:                version.Current,
		Commit:                 version.Commit(),
		Dirty:                  version.Dirty(),
		Branch:                 gitBranch(cwd),
		Worktree:               sess.Worktree,
		MemorySummary:          mem.Summary().String(),
		BaseSystemPrompt:       baseSys,
		FileCfg:                fileCfg,
		Subagents:              subagentTasks,
		AgentTool:              agentTool,
		CustomCommands:         customCmds,
		MCPManager:             mcpManager,
		Skills:                 skillsRes.Skills,
		SkillTool:              skillTool,
	})
	// Skills onboarding (skills installed but none enabled) is surfaced
	// inside the welcome card via startupTip() — see welcome.go's
	// memory > skills > rotating-pool priority. Emitting it as a
	// separate tea.Println here used to race with the welcome box's
	// per-row Println sequence, landing the notice above, below, or
	// even inside the box depending on Init batch timing.
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
	// Wire the AgentTool's background-completion callback into the
	// Model's long-lived inbox. The callback runs from a detached
	// goroutine when a background subagent finishes; non-blocking
	// send so a full inbox doesn't deadlock the runner.
	{
		inbox := model.subagentInbox
		agentTool.SetBackgroundDoneCallback(func(ev agent.SubagentBackgroundDone) {
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
	// plan); --dangerously-skip-permissions is an orthogonal overlay
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
	// Inline mode (no alt-screen): conversation lines flow into the
	// terminal's native scrollback via tea.Println from inside the model.
	// Only the live footer (input + status + transient overlays) redraws in
	// place. This makes selection, scroll-wheel, and copy "just work" via
	// the terminal — see model.go for the appendLine emit path.
	prog := tea.NewProgram(model)
	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
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
	if err := sess.Save(); err != nil {
		return err
	}
	if hint := resumeHint(sess); hint != "" {
		fmt.Fprintln(os.Stderr, hint)
	}
	return nil
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
func sessionHasExchange(sess *session.Session) bool {
	for _, msg := range sess.Messages {
		if msg.Role == adapter.RoleUser || msg.Role == adapter.RoleAssistant {
			return true
		}
	}
	return false
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

func composeSystemPrompt(base string, profile adapter.ProviderProfile) string {
	if hasBuiltin(profile.EnabledBuiltinTools, adapter.BuiltinToolWebSearch) {
		return base + "\nFor live or current information, use the provider-native web_search tool when needed."
	}
	return base + "\nFor live or current information, use the web_search tool to search the web via DuckDuckGo, or fetch_url for specific pages or feeds when needed."
}

// appendSkillsSection adds the description-matched metadata tier of
// Agent Skills to the system prompt. This is the spec's "always-on,
// small" tier — names + descriptions only; bodies stay out of the
// prompt until the model invokes Skill(name=...). The framing mirrors
// Claude Code's skills system reminder so models that have seen that
// surface recognize the contract.
func appendSkillsSection(base string, loaded []skills.Skill) string {
	if len(loaded) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n# Available skills\n\n")
	b.WriteString("You have access to a set of reusable capability playbooks (Agent Skills). When a user request matches a skill's described scope, invoke it via the `Skill` tool (e.g. `Skill(skill=\"<name>\")`); the tool returns the skill's body so you can apply it in the current turn. Only invoke a skill that appears in the list below — do NOT guess names.\n\n")
	for _, sk := range loaded {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String()
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

func hasBuiltin(tools []adapter.BuiltinToolKind, want adapter.BuiltinToolKind) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

// gitBranch reads the current git branch via `git -C <cwd> branch --show-current`.
// Returns "" if cwd isn't a repo or git isn't installed — both are normal.
func gitBranch(cwd string) string {
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
	// Stamp the worktree name when the session is launched inside one
	// (via --worktree at startup). Sessions resume into the right
	// worktree because Session.Cwd is the worktree dir; the field is
	// kept for `sessions list` display and future tooling.
	s.Worktree = opts.Worktree
	return s, true, nil
}
