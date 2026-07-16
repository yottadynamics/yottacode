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
	"github.com/yottadynamics/yottacode/internal/catalog"
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
	// Derive the Responses-API prompt_cache_key from the session id so
	// every turn this run makes shares one server-side cache shard. Set
	// before the adapters (single + router candidates) are built below.
	opts.CacheKey = sess.ID
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
		adhocMaxOutput, adhocThinking := catalog.ReasoningInfo(opts.Model)
		ad = adapter.NewWithConfig(adapter.Config{
			BaseURL:                opts.BaseURL,
			APIKey:                 opts.APIKey,
			Model:                  opts.Model,
			ProviderOverride:       adapter.Provider(strings.TrimSpace(opts.ProviderKind)),
			ReasoningEffort:        opts.ReasoningEffort,
			CacheKey:               opts.CacheKey,
			ModelMaxOutput:         adhocMaxOutput,
			ModelSupportsThinking:  adhocThinking,
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
	// Cache-safe task routing: when [router].mode is "manual"/"auto",
	// resolve the fast/smart model adapters. These drive isolated
	// contexts only (subagents + summarization), so they never disturb
	// the main-thread prompt cache. nil when routing is off.
	routerAdapters, err := cli.BuildRouterAdapters(fileCfg, opts)
	if err != nil {
		return err
	}

	// Load skills early so the resolved set can flow into both the
	// system prompt (description-matched metadata tier) and the Skill
	// tool registration below. Loading twice would be wasteful and
	// risks the prompt and the tool drifting; this single load wins.
	skillsRes, _ := skills.LoadAll(cwd, usercmd.Reserved)
	for _, w := range skillsRes.Warnings {
		fmt.Fprintln(os.Stderr, "skills: "+w)
	}
	// Resolve experimental features up front (CLI > env > config) so the
	// dispatch steering can be baked into the system prompt below — without
	// it the model has no nudge toward the dispatch/integrate tools and
	// falls back to implementing serially itself. Reused for tool gating
	// further down so we resolve the Set exactly once.
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
	if expSet.IsEnabled(experimental.Dispatch) {
		baseSys += "\n\n" + agent.DispatchPromptAddendum
	}
	composedBase := composeSystemPrompt(baseSys, ad.Profile())
	composedBase = appendSkillsSection(composedBase, skillsRes.Skills)
	if fresh {
		sess.Messages = append(sess.Messages, adapter.Message{
			Role:           adapter.RoleSystem,
			Content:        memory.SystemPrompt(composedBase, mem),
			CacheHeadBytes: len(composedBase),
		})
	} else {
		recomposeSessionSystemPrompt(sess, memory.SystemPrompt(composedBase, mem), len(composedBase))
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
	//     startup flag + model-requested enter_plan_mode)
	//   - auto (Shift+Tab cycle + --permission-mode auto startup flag
	//     + plan-card [A]; no slash command, mirroring Claude Code)
	//   - yolo (--yolo startup flag only; no
	//     slash command, no keybinding — opt-in once per process)
	// Plan and auto are mutually exclusive; yolo is an orthogonal
	// overlay that stacks with either. Per-session lifetime;
	// always-off at startup unless the corresponding flag was passed.
	planMode := &agent.PlanModeState{}
	autoMode := &agent.AutoModeState{}
	yoloMode := &agent.YoloModeState{}
	// Shared /loop self-stop signal: the loop_control tool (registered below)
	// writes a stop request through it; the TUI Model consumes it at turn end.
	loopControl := &agent.LoopControlState{}

	reg := agent.NewRegistry()
	// Core cwd-bound tools (file read/write/edit, search, git-read/stage/
	// commit, checkpoints, run_bash/run_tests). Extracted into a shared
	// builder so the dispatch subagent path can register the identical
	// toolset against an isolated worktree CwdRef. The session-scoped
	// extras (worktree-admin, commit-workflow composites, GH/PR, memory,
	// web, todo, plan) stay registered inline below.
	agent.RegisterCoreCwdTools(reg, cwdRef, agent.CoreToolDeps{
		WriteOpts:      writeOpts,
		DenyReads:      denyReads,
		SupportsImages: ad.Profile().SupportsImages,
	})
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
	// code_review_context is the local-diff counterpart to
	// gh_pr_review_context, paired with the /code-review slash
	// command. Read-only and Cwd-only (no github.Interface): it
	// reviews the branch-vs-base diff, or the uncommitted working
	// tree when there are no commits ahead.
	reg.Register(&agent.CodeReviewContextTool{Cwd: cwdRef})
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
	// gh_issue_context + gh_issue_create pair for /git-create-issue.
	// Context needs no client (git remote + token chain + local
	// template lookup); create shares the github.Interface instance
	// with the other issue tools.
	reg.Register(&agent.GHIssueContextTool{Cwd: cwdRef})
	reg.Register(&agent.GHIssueCreateTool{Cwd: cwdRef, GH: ghClient})
	// git_push is paired with /git-push. The GH dependency is for
	// the best-effort PR-URL lookup after a successful push — a
	// nil client would skip the lookup silently, but registering
	// with the shared ghClient gives us the "PR updated: <url>"
	// footer for free.
	reg.Register(&agent.GitPushTool{Cwd: cwdRef, GH: ghClient})
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
	reg.Register(&agent.FetchURLTool{})
	if !hasBuiltin(ad.Profile().EnabledBuiltinTools, adapter.BuiltinToolWebSearch) {
		reg.Register(&agent.WebSearchTool{})
	}
	var embedClient *memory.EmbedClient
	var embedMissingLine1, embedMissingLine2 string
	if s := fileCfg.Retrieval.Strategy; s == "semantic" || s == "auto" {
		ec := memory.NewEmbedClient("", fileCfg.Retrieval.EmbeddingModel)
		reachable, installed := ec.Status(ctx)
		switch {
		case installed:
			// Short per-call bound on the interactive path so a
			// mid-session Ollama hang can't freeze the TUI; reindex uses
			// its own default-timeout client.
			ec.Timeout = memory.InteractiveEmbedTimeout
			embedClient = ec
		case reachable:
			// Ollama is up but the configured embedding model isn't
			// there — most likely the user removed it via `ollama rm`
			// after enabling semantic search. Surface this on startup
			// so the silent fallback to BM25 doesn't look like a
			// retrieval-quality regression with no cause.
			embedMissingLine1 = fmt.Sprintf(
				"[memory] embedding model %q not installed — falling back to BM25 (keyword search).",
				ec.Model)
			embedMissingLine2 = fmt.Sprintf("Run: ollama pull %s", ec.Model)
		}
	}
	reg.Register(&agent.MemorySaveTool{Cwd: cwdRef, Embedder: embedClient})
	reg.Register(&agent.MemoryForgetTool{Cwd: cwdRef})
	reg.Register(&agent.MemorySearchTool{Cwd: cwdRef, Embedder: embedClient, Strategy: fileCfg.Retrieval.Strategy})
	reg.Register(&agent.MemoryGetTool{Cwd: cwdRef})
	reg.Register(&agent.GitTool{Cwd: cwdRef})
	reg.Register(&agent.TodoWriteTool{Store: planStore})
	// The plan-mode boundary pair. The loop's schema filter advertises
	// exit_plan_mode only while plan mode is active and enter_plan_mode
	// only while it is NOT, and both always route through the approval
	// modal — the TUI's [Y]/[N] / [A][M][L][K] handlers are what
	// actually flip the shared PlanModeState before forwarding the
	// decision. EnterPlanModeTool carries the state pointer so its
	// Execute can report the resolved plan-file path to the model.
	reg.Register(&agent.ExitPlanModeTool{})
	reg.Register(&agent.EnterPlanModeTool{State: planMode})

	// loop_control lets a /loop prose iteration end its own loop once the
	// model judges the loop's goal met (e.g. "…and stop when CI is green").
	// Gated to loop turns in iterationToolFilter via the shared state below,
	// so it's hidden from ordinary turns.
	reg.Register(&agent.LoopControlTool{State: loopControl})

	// Subagents: load definitions (built-in + ~/.yottacode/agents +
	// .yottacode/agents) and register the Agent dispatch tool. The
	// background-done callback is wired below after the Model exists
	// so completions route to the long-lived inbox the Model owns.
	subRes, _ := subagents.LoadAll(cwd, reg.Names())
	for _, w := range subRes.Warnings {
		fmt.Fprintln(os.Stderr, "subagents: "+w)
	}
	// Resolve the transcript dir but don't create it: openTranscript
	// MkdirAlls on the first dispatch, so a session with no subagent run
	// leaves no empty ~/.yottacode/memory/projects/<slug>/ behind.
	transcriptDir, _ := subagents.TranscriptDirFor(cwd)
	subagentTasks := subagents.NewRegistry()
	// Subagent teardown is DEFERRED (not inline after prog.Run) so it runs
	// even when prog.Run returns an error or panics: background workers run on
	// context.Background() and must never outlive the session (their
	// goroutines + provider SSE streams would leak), and empty dispatch
	// worktrees must be reclaimed regardless of how we exit. SIGKILL is still
	// uncatchable — nothing can defer through it — but error-returns and
	// panics, which previously skipped this entirely, are now covered.
	defer func() {
		if n := subagentTasks.CancelAll(); n > 0 {
			drainDeadline := time.Now().Add(3 * time.Second)
			for subagentTasks.ActiveCount() > 0 && time.Now().Before(drainDeadline) {
				time.Sleep(20 * time.Millisecond)
			}
		}
		sweepCtx, sweepCancel := context.WithTimeout(context.Background(), 5*time.Second)
		agent.ReclaimEmptyDispatchWorktrees(sweepCtx, subagentTasks)
		sweepCancel()
	}()
	// Rehydrate the prior session's subagent task index so task-ids the model
	// wrote into the conversation still resolve (get_subagent_result /
	// subagents) after a restart; tasks that were running when that session
	// ended come back marked orphaned. Then sweep any empty dispatch worktrees
	// a crashed session leaked — the rehydrated records carry the worktree +
	// base the reclaim check needs (the at-exit defer above couldn't run if
	// the previous process was SIGKILLed or power-lost).
	subagentTasks.Import(sess.SubagentTasks)
	{
		startupSweepCtx, startupSweepCancel := context.WithTimeout(context.Background(), 5*time.Second)
		agent.ReclaimEmptyDispatchWorktrees(startupSweepCtx, subagentTasks)
		startupSweepCancel()
	}
	// experimental.Set (expSet) was resolved earlier, before prompt
	// composition, so the dispatch steering could be baked into the
	// system prompt. Reuse it here for tool gating.

	agentTool := &agent.AgentTool{
		Configs:        subRes.Configs,
		Tasks:          subagentTasks,
		Adapter:        ad,
		ParentRegistry: reg,
		// Cache-safe routing wiring; zero values (nil/false) when
		// routing is disabled, so the AgentTool inherits the parent
		// adapter for every child exactly as before.
		FastAdapter:   routerFast(routerAdapters),
		FastModel:     routerFastModel(routerAdapters),
		SmartAdapter:  routerSmart(routerAdapters),
		SmartModel:    routerSmartModel(routerAdapters),
		RouteAuto:     fileCfg.Router.RoutingAuto(),
		ModelResolver: routerResolve(routerAdapters),
		// Source the child loop's compaction window the same way the
		// status bar does, so subagents size context against the real
		// (override- and default_window-aware) window.
		ResolveWindow: func(model string) int {
			return catalog.ResolveWindowForProvider(fileCfg.ProviderKindForModel(model), model, fileCfg.ContextWindowOverride(model), fileCfg.Context.DefaultWindow)
		},
		Permissions:   perms,
		YoloMode:      yoloMode,
		PlanMode:      planMode,
		AutoMode:      autoMode,
		Cwd:           cwdRef,
		TranscriptDir: transcriptDir,
		// Session-wide subagent token backstop (config-driven, generous
		// default). Bounds cumulative fan-out spend the per-wave caps can't.
		MaxSessionTokens: fileCfg.SubagentSessionTokenBudget(),
		// Background subagents are GA in the interactive TUI. Oneshot still
		// leaves AllowBackground=false because there is no long-lived UI or
		// /subagents picker to host detached work.
		AllowBackground: true,
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

	// Dispatch + integrate: fan a batch of subtasks out to concurrent
	// subagents (write-capable ones in isolated git worktrees, partitioned
	// by file ownership) and merge their branches into one integration
	// branch. Gated behind the `dispatch` experimental feature; when off,
	// each tool returns a recoverable error the model relays to the user.
	// dispatch reuses the AgentTool for routing/transcripts/runChild.
	dispatchEnabled := expSet.IsEnabled(experimental.Dispatch)
	reg.Register(&agent.DispatchTool{
		Agent:          agentTool,
		SupportsImages: ad.Profile().SupportsImages,
		// The TUI is a long-running session that can host detached
		// background workers and surface their completion via the
		// subagent inbox, so background dispatch is available here.
		SupportsBackground: true,
		Enabled:            dispatchEnabled,
	})
	reg.Register(&agent.IntegrateTool{Cwd: cwdRef, Enabled: dispatchEnabled})

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
	//
	// Persistent override: `[skills] default_on = [...]` in config
	// seeds the enablement map with the named skills so a user who's
	// found a stable workflow doesn't have to redo the picker dance
	// each session. Names that don't match any loaded skill emit a
	// warning so a typo surfaces instead of silently no-op'ing.
	skillTool := &agent.SkillTool{All: skillsRes.Skills}
	defaultOn := map[string]bool{}
	loadedNames := map[string]bool{}
	for _, sk := range skillsRes.Skills {
		loadedNames[sk.Name] = true
	}
	for _, name := range fileCfg.Skills.DefaultOn {
		if !loadedNames[name] {
			fmt.Fprintln(os.Stderr, "skills: [skills] default_on references unknown skill "+name+" — ignoring")
			continue
		}
		defaultOn[name] = true
	}
	skillTool.SetEnabled(defaultOn)
	reg.Register(skillTool)

	// Compaction is deliberately left unset for the interactive session.
	// In-loop compaction (which subagents use) rewrites history in place
	// and discards the summarized middle — fine for a subagent's
	// ephemeral, isolated history, but for the main session it would
	// silently drop the user's own conversation mid-turn, bypassing the
	// turn-boundary summarizer that first snapshots the full history to
	// disk and rebuilds the /recall index. The interactive session manages
	// context at the turn boundary instead (see updateContextUsage /
	// startAutoSummarize). The known gap — a single turn that balloons past
	// the window before it ends — degrades to the provider's own limit and
	// /clear, which is preferable to losing history without a snapshot.
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
		LoopControl:       loopControl,
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
		// Backfill the FTS index first (so message rows exist), then embed any
		// messages lacking a current vector for auto-recall. Both run in one
		// background goroutine so a large corpus never delays the UI; vector
		// backfill is skipped when embeddings are unavailable or auto-recall is
		// off, and resumes across restarts since already-embedded messages are
		// skipped. ctx cancellation (app exit) stops it promptly.
		ec := embedClient
		autoRecall := fileCfg.Retrieval.SessionRecall.Auto
		go func() {
			_ = recall.Backfill(idx)
			if ec != nil && autoRecall {
				_ = idx.BackfillVectors(ctx, ec, ec.Model)
			}
		}()
		reg.Register(&agent.SessionRecallTool{Searcher: &recallAdapter{idx: idx}})
	}

	model := New(ctx, Config{
		Cfg:                    cfg,
		Session:                sess,
		ExperimentalEnabled:    expSet.EnabledNames(),
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
		Branch:                 gitBranch(ctx, cwd),
		Worktree:               sess.Worktree,
		MemorySummary:          mem.Summary().String(),
		BaseSystemPrompt:       baseSys,
		EmbedClient:            embedClient,
		FileCfg:                fileCfg,
		Subagents:              subagentTasks,
		AgentTool:              agentTool,
		CustomCommands:         customCmds,
		MCPManager:             mcpManager,
		Skills:                 skillsRes.Skills,
		SkillTool:              skillTool,
		SummarizerAdapter:      routerFast(routerAdapters),
		SummarizerModel:        routerFastModel(routerAdapters),
	})
	model.githubClient = ghClient
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
	if embedMissingLine1 != "" {
		line1 := styleWarnIcon.Render("⚠") + " " + styleAuto.Render(embedMissingLine1)
		line2 := "  " + styleAuto.Render(embedMissingLine2)
		model.pendingStartupNotices = append(model.pendingStartupNotices, line1, line2)
	}
	for _, e := range customErrs {
		var rendered string
		if e.Level == usercmd.LevelWarning {
			rendered = styleAuto.Render(fmt.Sprintf("[commands] %s", e.Error()))
		} else {
			rendered = styleError.Render(fmt.Sprintf("[commands] %s", e.Error()))
		}
		model.appendLine(rendered)
	}
	// Confirm at startup which experimental features are on — without this
	// the gate left no in-session signal it was enabled. /experimental shows
	// the full catalog and detail.
	if names := expSet.EnabledNames(); len(names) > 0 {
		model.pendingStartupNotices = append(model.pendingStartupNotices,
			styleAuto.Render("experimental enabled: "+strings.Join(names, ", ")+" — /experimental for details"))
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
	// Inline mode (no alt-screen): conversation lines flow into the
	// terminal's native scrollback via tea.Println from inside the model.
	// Only the live footer (input + status + transient overlays) redraws in
	// place. This makes selection, scroll-wheel, and copy "just work" via
	// the terminal — see model.go for the appendLine emit path.
	prog := tea.NewProgram(model)
	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
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
	sess.SubagentTasks = subagentTasks.Export()
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

// routerFast returns the fast-model streamer for subagent routing, or
// nil when routing is disabled (ra == nil). Returning a typed nil-free
// interface keeps AgentTool.FastAdapter genuinely nil so routeChildModel
// falls back to the parent adapter.
func routerFast(ra *cli.RouterAdapters) agent.Streamer {
	if ra == nil || ra.Fast == nil {
		return nil
	}
	return ra.Fast
}

func routerFastModel(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.FastModel
}

// routerSmart returns the smart-model streamer for subagent routing, or
// nil when routing is disabled (then non-read-only subagents inherit the
// active model, the pre-routing behavior).
func routerSmart(ra *cli.RouterAdapters) agent.Streamer {
	if ra == nil || ra.Smart == nil {
		return nil
	}
	return ra.Smart
}

func routerSmartModel(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.SmartModel
}

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

// recomposeSessionSystemPrompt rewrites the session's system message.
// headBytes is the length of the stable cache prefix (the static base
// prompt ahead of the memory tail) — see Message.CacheHeadBytes.
func recomposeSessionSystemPrompt(sess *session.Session, content string, headBytes int) {
	for i := range sess.Messages {
		if sess.Messages[i].Role == adapter.RoleSystem {
			sess.Messages[i].Content = content
			sess.Messages[i].CacheHeadBytes = headBytes
			return
		}
	}
	sess.Messages = append([]adapter.Message{{
		Role:           adapter.RoleSystem,
		Content:        content,
		CacheHeadBytes: headBytes,
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

// recallAdapter bridges *recall.Index (which returns recall.Hit) to
// agent.RecallSearcher (which expects agent.RecallHit) so the agent
// package stays cycle-free (agent → session → agent would cycle if
// agent imported recall directly).
type recallAdapter struct{ idx *recall.Index }

func (a *recallAdapter) Search(query string, limit int) ([]agent.RecallHit, error) {
	hits, err := a.idx.Search(query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.RecallHit, len(hits))
	for i, h := range hits {
		out[i] = agent.RecallHit{
			SessionID:   h.SessionID,
			SessionName: h.SessionName,
			Model:       h.Model,
			Created:     h.Created,
			Role:        string(h.Role),
			Snippet:     h.Snippet,
		}
	}
	return out, nil
}
