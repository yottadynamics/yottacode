package agentruntime

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/codemap"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/experimental"
	githubapi "github.com/yottadynamics/yottacode/internal/github"
	"github.com/yottadynamics/yottacode/internal/lsp"
	mcppkg "github.com/yottadynamics/yottacode/internal/mcp"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/sandbox"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/skills"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/usercmd"
)

// defaultSystemPrompt is sourced from internal/agent so every caller
// (oneshot, TUI, ACP) shares one base prompt and cannot drift.
const defaultSystemPrompt = agent.DefaultSystemPrompt

// Runtime is everything Builder.Build constructs for one session. Callers
// layer their own presentation-specific state on top: the Bubbletea
// Model/Config (TUI), stdout streaming (oneshot), or ACP protocol
// plumbing (internal/acp). Deliberately excluded (stay caller-specific):
// checkpoints, recall/FTS index, workspace trust, sensitivity posture,
// and the GitHub PR/Issue tool suite — all TUI-interactive or
// ghClient-coupled concerns with no ACP v1 equivalent yet.
type Runtime struct {
	Session *session.Session
	Fresh   bool

	Cfg      agent.LoopConfig
	Registry *agent.Registry

	Permissions   *permissions.Permissions
	AgentTool     *agent.AgentTool
	SubagentTasks *subagents.Registry
	MCPManager    *mcppkg.Manager

	Skills    []skills.Skill
	SkillTool *agent.SkillTool

	LSPManager      *lsp.Manager
	CodeMapProvider codemap.Provider

	Adapter        adapter.Client
	RouterAdapters *cli.RouterAdapters

	// ChatOptions is the live, as-constructed options snapshot Adapter
	// (and RouterAdapters) were built from — mirrors internal/tui's own
	// m.opts. Build seeds it (including any advisor-model override
	// applied to spec.ChatOptions.Model below); RebuildAdapterForEffort
	// mutates ChatOptions.ReasoningEffort and reconstructs every adapter
	// that bakes reasoning effort in at construction time. Session-only —
	// never written back to config.toml.
	ChatOptions cli.ChatOptions

	// RoutingAuto is the session-live mirror of fileCfg.Router.RoutingAuto()
	// at construction — whether subagent dispatch/summarization currently
	// route through RouterAdapters' implementer/advisor pair. Distinct from
	// FileCfg.Router.Mode (the on-disk setting Build read once): this field
	// is what SetAdvisorRouting toggles, session-only, matching
	// internal/tui/cmd_router.go's own m.routerMode/RouterModeAuto split
	// between "what's persisted" and "what's live."
	RoutingAuto bool

	// GHClient is the typed GitHub client backing the pr_*/issue_*/
	// git_push composite tools (see Build). Lazy and side-effect-free
	// to construct — the real auth/network cost is paid on first tool
	// call, not here — so every caller gets it unconditionally, same as
	// MCPManager. Exposed on Runtime because TUI's status bar also
	// reads it directly (resolveCurrentPRCmd) for a display-only
	// current-PR lookup that has no ACP equivalent.
	GHClient githubapi.Interface

	// RecallIndex backs the model-callable session_recall tool (see
	// Build) — a full-text/semantic index over every saved session at
	// ~/.yottacode/index.sqlite, shared across every caller (it's one
	// file on disk, not per-session state; SQLite's WAL mode is what
	// makes concurrent handles from oneshot/tui/acp safe at once). Nil
	// when recall.Open fails (non-fatal — see rt.Warnings); callers
	// must Close it themselves when the session ends (TUI's own
	// teardown does this already; CloseSession/Shutdown do it for ACP).
	// Backfilling the corpus (session.List + re-index) stays a
	// TUI-only background job — see internal/tui/run.go — since it's a
	// startup-time catch-up most valuable for the interactive,
	// long-running case; oneshot/ACP sessions still search whatever a
	// prior TUI/backfill run already indexed, they just don't trigger
	// a fresh corpus-wide catch-up themselves.
	RecallIndex *recall.Index

	// Model is the effective model name after Build's own resolution —
	// specifically, when advisor routing is enabled and overrides the
	// requested model, this is the *overridden* name. spec.Model (i.e.
	// spec.ChatOptions.Model) stays whatever the caller originally
	// passed in; callers that display or persist the active model (the
	// TUI's status bar, session bookkeeping) must read this field, not
	// spec.Model, or they'll show a stale name under routing.
	Model string

	PlanMode    *agent.PlanModeState
	AutoMode    *agent.AutoModeState
	YoloMode    *agent.YoloModeState
	LoopControl *agent.LoopControlState

	PlanStore *agent.PlanStore
	CwdRef    *agent.CwdRef

	// CmdSandbox is the optional session-scoped command sandbox backing
	// run_bash and worktree/dispatch sandbox inheritance. Nil preserves
	// HostSandbox behavior.
	CmdSandbox agent.Sandbox

	// SandboxManager owns lazy per-profile containers when sandboxing is enabled.
	SandboxManager *SandboxManager

	FileCfg         config.Config
	ExperimentalSet *experimental.Set
	Mem             memory.Loaded
	EmbedClient     *memory.EmbedClient

	// BaseSystemPrompt is the pre-memory composed prompt (profile framing
	// + skills section, before memory injection). Callers that know the
	// live user prompt at construction time (oneshot) can re-score memory
	// against it and overwrite the session's system message via
	// RecomposeSystemPrompt; callers that don't (TUI, ACP — the prompt
	// arrives later, via session/prompt) just keep what Build already
	// injected with the prompt-unaware memory.SystemPrompt.
	BaseSystemPrompt string

	// RawSystemPrompt is the base system prompt *before*
	// composeSystemPrompt/appendSkillsSection are applied — opts.
	// SystemPrompt (or defaultSystemPrompt) plus the dispatch addendum,
	// nothing else. TUI needs this raw form, not BaseSystemPrompt: its
	// own per-turn/reload paths (rebuildSystemPromptForTurn,
	// reloadMemoryNow, recomposeSystemPromptWithSkills) call
	// composeSystemPrompt/appendSkillsSection themselves, so handing them
	// the already-composed BaseSystemPrompt would double the profile
	// framing and skills section on every turn.
	RawSystemPrompt string

	// Warnings collects non-fatal construction issues (router degrade,
	// embedding model unreachable, MCP server start failures, unknown
	// experimental flags, skills/subagents load warnings) as pre-formatted
	// lines. The caller decides how to surface them — TUI startup
	// notices, oneshot stderr, an ACP diagnostic message.
	Warnings []string
}

const runtimeSubagentDrainGrace = 5 * time.Second

// Close releases every process/goroutine resource owned by this Runtime.
// It is intentionally safe to call more than once: callers use it from both
// normal session-close paths and error/defer cleanup paths. Persistence stays
// caller-owned because TUI, oneshot, and ACP each have different save gates.
func (rt *Runtime) Close(ctx context.Context) {
	if rt == nil {
		return
	}
	_ = agent.CleanupRegistryTools(ctx, rt.Registry)
	if rt.SubagentTasks != nil {
		if rt.SubagentTasks.CancelAll() > 0 {
			deadline := time.NewTimer(runtimeSubagentDrainGrace)
			defer deadline.Stop()
			tick := time.NewTicker(50 * time.Millisecond)
			defer tick.Stop()
			draining := true
			for draining && rt.SubagentTasks.ActiveCount() > 0 {
				select {
				case <-ctx.Done():
					draining = false
				case <-deadline.C:
					draining = false
				case <-tick.C:
				}
			}
		}
		reclaimCtx, cancel := context.WithTimeout(context.Background(), runtimeSubagentDrainGrace)
		agent.ReclaimEmptyDispatchWorktrees(reclaimCtx, rt.SubagentTasks)
		cancel()
	}
	if rt.CmdSandbox != nil {
		_ = rt.CmdSandbox.Close()
	}
	if rt.MCPManager != nil {
		rt.MCPManager.Stop(ctx)
	}
	if rt.LSPManager != nil {
		rt.LSPManager.CloseAll()
	}
	if rt.RecallIndex != nil {
		_ = rt.RecallIndex.Close()
	}
}

// Builder constructs a Runtime from a SessionSpec. Stateless — safe to
// share across concurrent Build calls (each call only touches its own
// local state and the spec/session it's given).
type Builder struct{}

// NewBuilder returns a Builder. A constructor exists (rather than a bare
// struct literal at call sites) so adding builder-level config later
// (e.g. injected clocks for tests) doesn't churn every caller.
func NewBuilder() *Builder { return &Builder{} }

// Build constructs the full session world: session, memory, adapter,
// router, permissions, registry + tools, MCP, subagents, and the
// LoopConfig ready to hand to agent.Turn. It never calls
// os.Getwd()/os.Chdir() — every constructor here takes spec.Cwd
// explicitly, which is what makes this safe to call concurrently for N
// different sessions with different working directories (see
// SessionSpec.Cwd's doc comment).
func (b *Builder) Build(ctx context.Context, spec SessionSpec) (*Runtime, error) {
	opts := spec.ChatOptions
	cwd := spec.Cwd
	rt := &Runtime{}

	sess, fresh, err := openSession(opts, cwd)
	if err != nil {
		return nil, err
	}
	rt.Session = sess
	rt.Fresh = fresh
	// Derive the Responses-API prompt_cache_key from the session id so
	// every turn of this session shares one server-side cache shard.
	opts.CacheKey = sess.ID

	mem, err := memory.Load(cwd)
	if err != nil {
		return nil, err
	}
	rt.Mem = mem

	fileCfg, err := config.LoadDefault()
	if err != nil {
		return nil, err
	}
	rt.FileCfg = fileCfg

	var embedClient *memory.EmbedClient
	if s := fileCfg.Retrieval.Strategy; s == "semantic" || s == "auto" {
		ec := memory.NewEmbedClient("", fileCfg.Retrieval.EmbeddingModel)
		if reachable, installed := ec.Status(ctx); installed {
			ec.Timeout = memory.InteractiveEmbedTimeout
			embedClient = ec
		} else if reachable {
			rt.Warnings = append(rt.Warnings, fmt.Sprintf(
				"memory: embedding model %q not installed — using BM25 (run: ollama pull %s)", ec.Model, ec.Model))
		}
	}
	rt.EmbedClient = embedClient

	skillsRes, _ := skills.LoadAll(cwd, usercmd.Reserved)
	for _, w := range skillsRes.Warnings {
		rt.Warnings = append(rt.Warnings, "skills: "+w)
	}
	rt.Skills = skillsRes.Skills

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
		rt.Warnings = append(rt.Warnings, fmt.Sprintf(
			"warning: --experimental %q is not a recognized feature (typo? graduated? see docs/experimental.md)", unknown))
	}
	rt.ExperimentalSet = expSet

	baseSys := opts.SystemPrompt
	if baseSys == "" {
		baseSys = defaultSystemPrompt
	}
	if expSet.IsEnabled(experimental.Dispatch) {
		baseSys += "\n\n" + agent.DispatchPromptAddendum
	}

	// Adapter + router. BuildRouter failures are always fatal (both
	// current callers agree on this). BuildRouterAdapters failures are
	// only fatal when routing is actually enabled — otherwise a stale
	// advisor/implementer pair must not block an unrouted session (TUI's
	// more lenient behavior; picked as canonical per the divergence
	// policy below).
	var ad adapter.Client
	router, err := cli.BuildRouter(fileCfg, opts)
	if err != nil {
		return nil, err
	}
	routerAdapters, err := cli.BuildRouterAdapters(fileCfg, opts)
	if err != nil {
		if fileCfg.Router.RoutingEnabled() {
			return nil, err
		}
		rt.Warnings = append(rt.Warnings, "warning: [advisor] pair unresolved (routing is off, continuing without it): "+err.Error())
		routerAdapters = nil
	}
	if fileCfg.Router.RoutingEnabled() && routerAdapters != nil && routerAdapters.Advisor != nil {
		ad = routerAdapters.Advisor
		if routerAdapters.AdvisorModel != "" {
			opts.Model = routerAdapters.AdvisorModel
		}
	} else if router != nil {
		ad = router
	} else {
		if err := preflight(ctx, adapterConfig(opts, fileCfg)); err != nil {
			return nil, err
		}
		ad = adapter.NewWithConfig(adapterConfig(opts, fileCfg))
	}
	rt.Adapter = ad
	rt.RouterAdapters = routerAdapters
	rt.Model = opts.Model
	rt.ChatOptions = opts
	rt.RoutingAuto = fileCfg.Router.RoutingAuto()

	rt.RawSystemPrompt = baseSys
	composedBase := appendSkillsSection(composeSystemPrompt(baseSys, ad.Profile()), skillsRes.Skills)
	rt.BaseSystemPrompt = composedBase
	if fresh {
		sess.Messages = append(sess.Messages, adapter.Message{
			Role:           adapter.RoleSystem,
			Content:        memory.SystemPrompt(composedBase, mem),
			CacheHeadBytes: len(composedBase),
		})
	} else {
		RecomposeSystemPrompt(sess, memory.SystemPrompt(composedBase, mem), len(composedBase))
	}

	perms, err := permissions.Load(cwd)
	if err != nil {
		return nil, err
	}
	rt.Permissions = perms

	cwdRef := agent.NewCwdRef(cwd)
	rt.CwdRef = cwdRef

	writeOpts := agent.WritePathOptions{
		Cwd:          cwdRef,
		AllowedPaths: splitCSV(opts.AllowPaths),
		DenyExact:    agent.DefaultDenyPaths(cwd),
	}
	denyReads := agent.DefaultDenyReadPaths(cwd)

	planStore := agent.NewPlanStore()
	if len(sess.Todos) > 0 {
		planStore.Replace(sess.Todos)
	}
	rt.PlanStore = planStore

	planMode := &agent.PlanModeState{}
	autoMode := &agent.AutoModeState{}
	yoloMode := &agent.YoloModeState{}
	loopControl := &agent.LoopControlState{}
	rt.PlanMode, rt.AutoMode, rt.YoloMode, rt.LoopControl = planMode, autoMode, yoloMode, loopControl

	lspManager := lsp.NewManager(0, 0)
	rt.LSPManager = lspManager
	var codeMapProvider codemap.Provider
	if expSet.IsEnabled(experimental.CodeMap) {
		codeMapProvider = &codemap.CachedProvider{Options: codemap.BuildOptions{Root: cwd, Source: codemap.LSPSource{Manager: lspManager, Servers: fileCfg.LSP.Servers, Root: cwd}}}
	}
	rt.CodeMapProvider = codeMapProvider

	// Podman command sandbox: enabled by [sandbox].backend = "podman".
	// Manager construction is intentionally cheap
	// and does not require Podman or any image to be present yet. Profile startup
	// failures later fail closed at the tool call that needed the profile; they
	// never fall back to HostSandbox when the user requested isolation.
	//
	// The manager itself is created at startup, but profile containers are lazy:
	// run_bash gets the default image on first use, while document subprocess
	// tools get the documents image only when they need it.
	var cmdSandbox agent.Sandbox
	var sandboxFactory agent.SandboxFactory
	if fileCfg.Sandbox.Backend == "podman" {
		// Best-effort: reclaims yc-* containers left stuck non-running by a
		// crashed or interrupted-teardown prior session (see PruneOrphaned's
		// doc comment for why State, not age, is the safety filter). Errors
		// are swallowed the same way podman.removeContainer's own callers
		// already do for best-effort cleanup elsewhere in this package.
		_ = sandbox.PruneOrphaned(ctx)
		mgr := NewSandboxManager(fileCfg.Sandbox, sess.ID, cwd, podmanSandboxConstructor)
		mgr.SetConfigReloader(config.LoadDefault)
		rt.SandboxManager = mgr
		cmdSandbox = mgr.Handler()
		rt.CmdSandbox = cmdSandbox
		sandboxFactory = func(ctx context.Context, wtDir, taskID string) (agent.Sandbox, error) {
			workerMgr := NewSandboxManager(fileCfg.Sandbox, sess.ID+"-"+taskID, wtDir, podmanSandboxConstructor)
			workerMgr.SetConfigReloader(config.LoadDefault)
			return workerMgr.Handler(), nil
		}
	}

	reg := agent.NewRegistry()
	rt.Registry = reg
	agent.RegisterCoreCwdTools(reg, cwdRef, agent.CoreToolDeps{
		WriteOpts:              writeOpts,
		DenyReads:              denyReads,
		SupportsImages:         ad.Profile().SupportsImages,
		EnableLSP:              true,
		LSPManager:             lspManager,
		LSPServers:             fileCfg.LSP.Servers,
		LSPDisabled:            fileCfg.LSP.Disabled,
		EnableCodeMap:          expSet.IsEnabled(experimental.CodeMap),
		CodeMapProvider:        codeMapProvider,
		EnableSyntaxRanges:     true,
		AllowPDFIngestion:      true,
		AllowDocxPdfGeneration: true,
		Sandbox:                cmdSandbox,
	})

	// Git worktree tools. enter_worktree/exit_worktree call process-global
	// os.Chdir() (see SessionSpec.DisableWorktreeTools) — unsafe for a
	// multi-session-per-process host, so ACP excludes all nine of these.
	if !spec.DisableWorktreeTools {
		reg.Register(&agent.EnterWorktreeTool{Cwd: cwdRef, Sandbox: cmdSandbox})
		reg.Register(&agent.ExitWorktreeTool{Cwd: cwdRef})
		reg.Register(&agent.WorktreeStatusTool{Cwd: cwdRef})
		reg.Register(&agent.GitWorktreeListTool{Cwd: cwdRef})
		reg.Register(&agent.GitWorktreeAddTool{Cwd: cwdRef})
		reg.Register(&agent.GitWorktreeRemoveTool{Cwd: cwdRef})
		reg.Register(&agent.GitWorktreeLockTool{Cwd: cwdRef})
		reg.Register(&agent.GitWorktreeUnlockTool{Cwd: cwdRef})
		reg.Register(&agent.GitWorktreePruneTool{Cwd: cwdRef})
	}

	// Local git-commit-workflow composites — no ghClient/network needed.
	reg.Register(&agent.GitCommitContextTool{Cwd: cwdRef})
	reg.Register(&agent.GitCommitApplyTool{Cwd: cwdRef})

	// GitHub tool suite (PR/Issue composites + git_push). Originally
	// registered TUI-only ("ghClient-coupled, no ACP v1 equivalent
	// yet") — that stopped being true once the bucket-A ACP slash
	// commands (git-push, git-create-pr, git-update-pr,
	// git-create-issue, git-review-pr, git-implement-issue, code-review)
	// shipped assuming these tools exist. NewTypedClient is lazy and
	// side-effect-free (auth/network cost is paid on first real call via
	// sync.Once, not here), so constructing it unconditionally for every
	// caller is safe — same shape as MCPManager below. In-session cache
	// wraps the typed client so duplicate reads within one turn make one
	// API call; writes (CreatePR, UpdatePR, AddPRComment) pass through,
	// and UpdatePR invalidates matching ReadPR entries so the next read
	// sees fresh data.
	ghClient := githubapi.NewCachingClient(githubapi.NewTypedClient(cwd))
	rt.GHClient = ghClient
	reg.Register(&agent.GHPRContextTool{Cwd: cwdRef})
	reg.Register(&agent.GHPRCreateTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GHPRReviewContextTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.PRWatchChecksTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.PRCheckLogsTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.PRRerunChecksTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.CodeReviewContextTool{Cwd: cwdRef})
	reg.Register(&agent.GHPRReadTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GHIssueReadTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GHIssueListTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GHIssueContextTool{Cwd: cwdRef})
	reg.Register(&agent.GHIssueCreateTool{Cwd: cwdRef, GH: ghClient})
	// git_push's GH dependency is only for the best-effort PR-URL
	// lookup after a successful push (GitPushTool tolerates a nil GH —
	// see git_push_workflow.go's PushBranch — but every caller gets the
	// real client now anyway).
	reg.Register(&agent.GitPushTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GHPRUpdateTool{Cwd: cwdRef, GH: ghClient})
	reg.Register(&agent.GHPRAddCommentTool{Cwd: cwdRef, GH: ghClient})
	if !hasBuiltin(ad.Profile().EnabledBuiltinTools, adapter.BuiltinToolWebSearch) {
		reg.Register(&agent.WebSearchTool{})
	}

	// session_recall — searches every saved session's transcript. A
	// failure to open the index is non-fatal, same posture as MCP
	// server start failures: /session_recall just becomes unavailable
	// and the rest of the session runs fine.
	if idx, err := recall.Open(); err == nil {
		rt.RecallIndex = idx
		reg.Register(&agent.SessionRecallTool{Searcher: &recallAdapter{idx: idx}})
	} else {
		rt.Warnings = append(rt.Warnings, "recall: "+err.Error())
	}

	reg.Register(&agent.FetchURLTool{})
	registerMemoryTools(reg, cwdRef, embedClient, fileCfg.Retrieval.Strategy, fileCfg.Retrieval.SemanticWeight, memory.Source{Session: sess.ID})
	reg.Register(&agent.GitTool{Cwd: cwdRef, LSPManager: lspManager})
	reg.Register(&agent.TodoWriteTool{Store: planStore})
	reg.Register(&agent.ExitPlanModeTool{})
	reg.Register(&agent.EnterPlanModeTool{State: planMode})
	reg.Register(&agent.LoopControlTool{State: loopControl})

	validSubagentTools := reg.Names()
	validSubagentTools[agent.ConsultAdvisorToolName] = true
	subRes, _ := subagents.LoadAll(cwd, validSubagentTools)
	for _, w := range subRes.Warnings {
		rt.Warnings = append(rt.Warnings, "subagents: "+w)
	}
	transcriptDir, _ := subagents.TranscriptDirFor(cwd)
	subagentTasks := subagents.NewRegistry()
	if len(sess.SubagentTasks) > 0 {
		subagentTasks.Import(sess.SubagentTasks)
	}
	rt.SubagentTasks = subagentTasks

	agentTool := &agent.AgentTool{
		Configs:            subRes.Configs,
		Tasks:              subagentTasks,
		Adapter:            ad,
		ParentRegistry:     reg,
		ImplementerAdapter: routerImplementer(routerAdapters),
		ImplementerModel:   routerImplementerModel(routerAdapters),
		AdvisorAdapter:     routerAdvisor(routerAdapters),
		AdvisorModel:       routerAdvisorModel(routerAdapters),
		FastAdapter:        routerImplementer(routerAdapters),
		FastModel:          routerImplementerModel(routerAdapters),
		SmartAdapter:       routerAdvisor(routerAdapters),
		SmartModel:         routerAdvisorModel(routerAdapters),
		RouteAuto:          fileCfg.Router.RoutingAuto(),
		ModelResolver:      routerModelResolver(routerAdapters, fileCfg.Router.RoutingEnabled()),
		ResolveWindow: func(model string) int {
			return catalog.ResolveWindowForProvider(fileCfg.ProviderKindForModel(model), model, fileCfg.ContextWindowOverride(model), fileCfg.Context.DefaultWindow)
		},
		Permissions:            perms,
		YoloMode:               yoloMode,
		PlanMode:               planMode,
		AutoMode:               autoMode,
		Cwd:                    cwdRef,
		TranscriptDir:          transcriptDir,
		MaxSessionTokens:       fileCfg.SubagentSessionTokenBudget(),
		MaxConcurrentSubagents: fileCfg.SubagentMaxConcurrent(),
		AllowBackground:        spec.SupportsBackgroundDispatch,
	}
	reg.Register(agentTool)
	reg.Register(&agent.GetSubagentResultTool{Tasks: subagentTasks})
	rt.AgentTool = agentTool

	dispatchEnabled := expSet.IsEnabled(experimental.Dispatch)
	reg.Register(&agent.DispatchTool{
		Agent:                  agentTool,
		SupportsImages:         ad.Profile().SupportsImages,
		EnableLSP:              true,
		LSPServers:             fileCfg.LSP.Servers,
		LSPDisabled:            fileCfg.LSP.Disabled,
		EnableSyntaxRanges:     true,
		AllowPDFIngestion:      true,
		AllowDocxPdfGeneration: true,
		SupportsBackground:     spec.SupportsBackgroundDispatch,
		Enabled:                dispatchEnabled,
		SandboxFactory:         sandboxFactory,
	})
	reg.Register(&agent.IntegrateTool{Cwd: cwdRef, Enabled: dispatchEnabled})

	skillTool := &agent.SkillTool{All: skillsRes.Skills}
	defaultOn := map[string]bool{}
	loadedNames := map[string]bool{}
	for _, sk := range skillsRes.Skills {
		loadedNames[sk.Name] = true
	}
	for _, name := range fileCfg.Skills.DefaultOn {
		if !loadedNames[name] {
			rt.Warnings = append(rt.Warnings, "skills: [skills] default_on references unknown skill "+name+" — ignoring")
			continue
		}
		defaultOn[name] = true
	}
	skillTool.SetEnabled(defaultOn)
	reg.Register(skillTool)
	rt.SkillTool = skillTool

	// MCP: new shared surface (oneshot had none before this extraction).
	// Global servers from config.toml plus session-scoped servers from
	// SessionSpec.MCPServers (ACP's session/new only); a session-scoped
	// entry overrides a global one of the same name. The manager is
	// always constructed; starting it is skipped when the caller wants
	// to do that itself asynchronously (see SessionSpec.DeferMCPStart).
	mcpServers := mergeMCPServers(fileCfg.MCPServers, spec.MCPServers)
	mcpManager := mcppkg.NewManager(mcpServers)
	if !spec.DeferMCPStart {
		startMCPAndRegisterTools(ctx, rt, mcpManager, reg)
	}
	rt.MCPManager = mcpManager

	compactionWindow := catalog.ResolveWindowForProvider(fileCfg.ProviderKindForModel(opts.Model), opts.Model, fileCfg.ContextWindowOverride(opts.Model), fileCfg.Context.DefaultWindow)
	var summarizerWindow int
	if fastModel := routerImplementerModel(routerAdapters); fastModel != "" {
		summarizerWindow = catalog.ResolveWindowForProvider(fileCfg.ProviderKindForModel(fastModel), fastModel, fileCfg.ContextWindowOverride(fastModel), fileCfg.Context.DefaultWindow)
	}
	cfg := agent.LoopConfig{
		Adapter:       ad,
		Registry:      reg,
		Permissions:   perms,
		Cwd:           cwdRef,
		MaxIterations: opts.MaxIterations,
		PlanMode:      planMode,
		AutoMode:      autoMode,
		YoloMode:      yoloMode,
		LoopControl:   loopControl,
		// Restores wiring the pre-extraction oneshot.go set directly
		// (`BypassPermissions: opts.BypassPermissions`) — Build's own
		// construction never carried it over, silently breaking
		// `yottacode run --yolo` (oneshot has no equivalent of
		// internal/tui/cmd_yolo.go's enterYoloMode compensating call,
		// which only sets YoloMode.Active — a separate, higher-priority
		// bypass path agent/loop.go checks before ever consulting this
		// field; see the mode-priority switch's comment there). Setting
		// both here is harmless for TUI/ACP callers: YoloMode's check
		// runs first and short-circuits before BypassPermissions is
		// ever consulted.
		BypassPermissions: opts.BypassPermissions,
	}
	if compactionWindow > 0 {
		cfg.Compaction = &agent.CompactionConfig{
			Window:           compactionWindow,
			Threshold:        fileCfg.Context.CompactionThreshold,
			TargetRatio:      compactionTargetRatio(fileCfg.Context.CompactionTargetRatio),
			Summarizer:       routerImplementer(routerAdapters),
			SummarizerWindow: summarizerWindow,
			PreCompact:       spec.PreCompact,
		}
	}
	rt.Cfg = cfg

	return rt, nil
}

// startMCPAndRegisterTools starts every configured MCP server and
// registers an agent.MCPTool per descriptor. Non-fatal: a server that
// fails to start or list tools is recorded on rt.Warnings and skipped,
// the way handleMCPStartupDone (internal/tui/cmd_mcp.go) already treats
// TUI's async completion path.
func startMCPAndRegisterTools(ctx context.Context, rt *Runtime, mcpManager *mcppkg.Manager, reg *agent.Registry) {
	for _, res := range mcpManager.Start(ctx) {
		if res.Err != nil {
			rt.Warnings = append(rt.Warnings, fmt.Sprintf("mcp(%s): %v", res.Name, res.Err))
			continue
		}
		client := mcpManager.Client(res.Name)
		if client == nil {
			continue
		}
		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		toolDescs, err := client.ListTools(listCtx)
		cancel()
		if err != nil {
			rt.Warnings = append(rt.Warnings, fmt.Sprintf("mcp(%s): list tools: %v", res.Name, err))
			continue
		}
		for _, td := range toolDescs {
			reg.Register(&agent.MCPTool{
				Server:      res.Name,
				ToolName:    td.Name,
				Desc:        td.Description,
				InputSchema: td.InputSchema,
				ReadOnly:    td.ReadOnlyHint,
				Client:      client,
			})
		}
	}
}

func adapterConfig(opts cli.ChatOptions, fileCfg config.Config) adapter.Config {
	maxOutput, supportsThinking := catalog.ReasoningInfo(opts.Model)
	return adapter.Config{
		BaseURL:                opts.BaseURL,
		APIKey:                 opts.APIKey,
		Model:                  opts.Model,
		ProviderOverride:       adapter.Provider(strings.TrimSpace(opts.ProviderKind)),
		ReasoningEffort:        opts.ReasoningEffort,
		CacheKey:               opts.CacheKey,
		CacheTTL:               fileCfg.Cache.AnthropicTTL,
		ModelMaxOutput:         maxOutput,
		ModelSupportsThinking:  supportsThinking,
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
}

func preflight(ctx context.Context, cfg adapter.Config) error {
	result := adapter.Probe(ctx, cfg)
	if len(result.Issues) == 0 {
		return nil
	}
	return fmt.Errorf("provider preflight failed: %s", strings.Join(result.Issues, "; "))
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
	s.Worktree = opts.Worktree
	return s, true, nil
}

// RecomposeSystemPrompt rewrites the session's system message. headBytes
// marks the stable cache prefix (the static base prompt ahead of the
// memory tail) — see adapter.Message.CacheHeadBytes. Exported because
// callers with a live prompt at construction time (oneshot) re-score
// memory against it after Build returns and need to overwrite what Build
// injected with the prompt-unaware memory.SystemPrompt.
func RecomposeSystemPrompt(sess *session.Session, content string, headBytes int) {
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

func registerMemoryTools(reg *agent.Registry, cwdRef *agent.CwdRef, embedClient *memory.EmbedClient, strategy string, semanticWeight float64, source memory.Source) {
	reg.Register(&agent.MemorySaveTool{Cwd: cwdRef, Embedder: embedClient, Source: source})
	reg.Register(&agent.MemoryForgetTool{Cwd: cwdRef})
	reg.Register(&agent.MemorySearchTool{Cwd: cwdRef, Embedder: embedClient, Strategy: strategy, SemanticWeight: semanticWeight, SemanticWeightConfigured: true})
	reg.Register(&agent.MemoryAuditTool{Cwd: cwdRef})
	reg.Register(&agent.MemoryCurateApplyTool{Cwd: cwdRef})
	reg.Register(&agent.MemoryArchivePruneTool{Cwd: cwdRef})
	reg.Register(&agent.MemoryGetTool{Cwd: cwdRef})
}

func composeSystemPrompt(base string, profile adapter.ProviderProfile) string {
	if profile.Provider == adapter.ProviderXAI && hasBuiltin(profile.EnabledBuiltinTools, adapter.BuiltinToolXSearch) {
		return base + "\nFor live or current information, use provider-native tools when needed. For X/Twitter posts, users, threads, trends, sentiment, or anything happening on X, use x_search, not web_search. Use web_search only for general web pages, news sites, docs, or pages outside X."
	}
	if hasBuiltin(profile.EnabledBuiltinTools, adapter.BuiltinToolWebSearch) {
		return base + "\nFor live or current information, use the provider-native web_search tool when needed."
	}
	return base + "\nFor live or current information, use fetch_url for specific pages or feeds when needed."
}

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

func compactionTargetRatio(configured float64) float64 {
	if configured <= 0 {
		return 0.35
	}
	return configured
}

// mergeMCPServers combines the global (config.toml) server list with the
// session-scoped list, session entries overriding global ones of the same
// name.
func mergeMCPServers(global, session []config.MCPServer) []config.MCPServer {
	if len(session) == 0 {
		return global
	}
	byName := make(map[string]config.MCPServer, len(global)+len(session))
	order := make([]string, 0, len(global)+len(session))
	for _, s := range global {
		if _, ok := byName[s.Name]; !ok {
			order = append(order, s.Name)
		}
		byName[s.Name] = s
	}
	for _, s := range session {
		if _, ok := byName[s.Name]; !ok {
			order = append(order, s.Name)
		}
		byName[s.Name] = s
	}
	out := make([]config.MCPServer, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
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

// routerImplementer / routerImplementerModel / routerAdvisor /
// routerAdvisorModel / routerModelResolver adapt cli.RouterAdapters to
// the agent.AgentTool/DispatchTool fields, nil-safe when routing is
// disabled. Mirror the TUI's helpers of the same name (internal/tui/run.go).
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

func routerModelResolver(ra *cli.RouterAdapters, enabled bool) func(string) agent.Streamer {
	if !enabled {
		return nil
	}
	return routerResolve(ra)
}

// recallAdapter bridges *recall.Index (which returns recall.Hit) to
// agent.RecallSearcher (which expects agent.RecallHit) so the agent
// package stays cycle-free (agent → session → agent would cycle if
// agent imported recall directly). Duplicated from internal/tui/run.go's
// identical private type — this repo's convention is small private
// duplicates over a shared testutil-style package.
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
