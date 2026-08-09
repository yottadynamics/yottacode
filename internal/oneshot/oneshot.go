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
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/codemap"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/lsp"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/sandbox"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/skills"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/usercmd"
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
	// Derive the Responses-API prompt_cache_key from the session id so
	// every turn of this run shares one server-side cache shard.
	opts.CacheKey = sess.ID
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
	// Semantic memory retrieval: when the configured strategy wants it
	// ("semantic" or "auto"), probe the local Ollama embedding endpoint
	// once at startup and wire the resulting client into memory_save
	// (so headless saves still get .vec sidecars), memory_search, and
	// per-turn injection. Mirrors the TUI runner so a memory created or
	// searched via `yottacode run` ranks identically to the interactive
	// surface. The probe is bounded (Status uses an 800ms timeout) and
	// gated on strategy, and it doubles as protection against a slow or
	// unreachable OLLAMA_HOST hanging the single turn on the query
	// embed. When the model isn't installed we degrade to BM25 and note
	// it on stderr (oneshot's diagnostics stream).
	var embedClient *memory.EmbedClient
	if s := fileCfg.Retrieval.Strategy; s == "semantic" || s == "auto" {
		ec := memory.NewEmbedClient("", fileCfg.Retrieval.EmbeddingModel)
		if reachable, installed := ec.Status(ctx); installed {
			// Short per-call bound on the interactive path (the single
			// turn's prompt build + any memory_save); reindex keeps the
			// default 30s.
			ec.Timeout = memory.InteractiveEmbedTimeout
			embedClient = ec
		} else if reachable {
			fmt.Fprintf(os.Stderr, "memory: embedding model %q not installed — using BM25 (run: ollama pull %s)\n", ec.Model, ec.Model)
		}
	}
	// AutoMemory: flag > env > config file. cli.Resolve handled the
	// first two; honor the persistent file toggle here so the wizard's
	// step isn't a no-op for one-shot runs.
	maxOutput, supportsThinking := catalog.ReasoningInfo(opts.Model)
	adCfg := adapter.Config{
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
	// Cache-safe task routing: resolve fast/smart adapters for subagent
	// routing (oneshot has no /summarize loop, so only subagents apply).
	// nil when [router].mode is off.
	routerAdapters, err := cli.BuildRouterAdapters(fileCfg, opts)
	if err != nil {
		return err
	}
	var profile adapter.ProviderProfile
	if fileCfg.Router.RoutingEnabled() && routerAdapters != nil && routerAdapters.Advisor != nil {
		profile = routerAdapters.Advisor.Profile()
		if routerAdapters.AdvisorModel != "" {
			opts.Model = routerAdapters.AdvisorModel
		}
	} else if router != nil {
		profile = router.Profile()
	} else {
		profile = adapter.NewWithConfig(adCfg).Profile()
	}
	// One-shot has the full user prompt up front, so we score memory
	// bodies against it directly — no two-phase rebuild needed (the
	// TUI does the rebuild per-turn because it doesn't know the
	// prompt at session start). USER.md, YOTTACODE.md, and both
	// MEMORY.md indexes always inject in full.
	// Load Agent Skills up-front so the resolved set drives both the
	// system-prompt's description-matched metadata tier and the Skill
	// tool registration below. Same one-load-two-uses pattern as the
	// TUI runner.
	skillsRes, _ := skills.LoadAll(cwd, usercmd.Reserved)
	for _, w := range skillsRes.Warnings {
		fmt.Fprintln(os.Stderr, "skills: "+w)
	}
	// Resolve experimental features before composing the prompt so the
	// optional feature prompts can be baked in. Dispatch stays experimental for
	// this PR, so its steering is appended only when the flag enables the tools.
	expSet := experimental.NewSet()
	for name, on := range fileCfg.Experimental {
		if on {
			expSet.Enable(name)
		}
	}
	for _, name := range opts.Experimental {
		expSet.Enable(name)
	}
	// Warn (don't fail) on unrecognized --experimental names so a typo or a
	// graduated/removed feature surfaces instead of silently no-op'ing —
	// mirrors the TUI launch path.
	for _, unknown := range expSet.UnknownNames() {
		fmt.Fprintf(os.Stderr, "warning: --experimental %q is not a recognized feature (typo? graduated? see docs/experimental.md)\n", unknown)
	}
	baseSys := opts.SystemPrompt
	if baseSys == "" {
		baseSys = defaultSystemPrompt
	}
	if expSet.IsEnabled(experimental.Dispatch) {
		baseSys += "\n\n" + agent.DispatchPromptAddendum
	}
	if fresh {
		composed := appendSkillsSection(composeSystemPrompt(baseSys, profile), skillsRes.Skills)
		sys := memory.SystemPromptForSemantic(ctx, composed, mem, prompt, fileCfg.Retrieval, embedClient)
		sess.Messages = append(sess.Messages, adapter.Message{
			Role:           adapter.RoleSystem,
			Content:        sys,
			CacheHeadBytes: len(composed),
		})
	} else {
		composed := appendSkillsSection(composeSystemPrompt(baseSys, profile), skillsRes.Skills)
		recomposeSessionSystemPrompt(sess, memory.SystemPromptForSemantic(ctx, composed, mem, prompt, fileCfg.Retrieval, embedClient), len(composed))
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
	if fileCfg.Router.RoutingEnabled() && routerAdapters != nil && routerAdapters.Advisor != nil {
		ad = routerAdapters.Advisor
	} else if router != nil {
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
	// cwdRef is the shared cwd holder every tool reads from. In
	// oneshot the value never changes mid-run (enter_worktree is in
	// the safety floor and would prompt — there's no approval surface
	// here), but we still wire it consistently so the agent codepath
	// matches the TUI's. WriteOpts shares the same pointer so the
	// write validator and the tools agree on the current cwd.
	cwdRef := agent.NewCwdRef(cwd)

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

	lspManager := lsp.NewManager(0, 0)
	defer lspManager.CloseAll()
	var codeMapProvider codemap.Provider
	if expSet.IsEnabled(experimental.CodeMap) {
		codeMapProvider = &codemap.CachedProvider{Options: codemap.BuildOptions{Root: cwd, Source: codemap.LSPSource{Manager: lspManager, Servers: fileCfg.LSP.Servers, Root: cwd}}}
	}

	// Podman command sandbox (roadmap/sandbox-podman.md): opt-in via the
	// "sandbox" experimental feature + [sandbox].backend = "podman". A
	// construction failure is fatal to startup — never falls back to
	// HostSandbox silently. sandboxFactory gives dispatch write-workers
	// their own container inheriting the same posture.
	var cmdSandbox agent.Sandbox
	var sandboxFactory agent.SandboxFactory
	if expSet.IsEnabled(experimental.Sandbox) && fileCfg.Sandbox.Backend == "podman" {
		ps, err := sandbox.NewPodmanSandbox(ctx, fileCfg.Sandbox, sess.ID, cwd)
		if err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		cmdSandbox = ps
		defer func() { _ = cmdSandbox.Close() }()
		sandboxFactory = func(ctx context.Context, wtDir, taskID string) (agent.Sandbox, error) {
			return sandbox.NewPodmanSandbox(ctx, fileCfg.Sandbox, sess.ID+"-"+taskID, wtDir)
		}
	}

	reg := agent.NewRegistry()
	// Core cwd-bound tools — shared with the TUI build and the dispatch
	// worktree-child registry via RegisterCoreCwdTools. Oneshot's extras
	// (worktree-admin, memory, git escape-hatch, todo, plan) stay inline.
	agent.RegisterCoreCwdTools(reg, cwdRef, agent.CoreToolDeps{
		WriteOpts:               writeOpts,
		DenyReads:               denyReads,
		EnableLSP:               true,
		LSPManager:              lspManager,
		LSPServers:              fileCfg.LSP.Servers,
		LSPDisabled:             fileCfg.LSP.Disabled,
		EnableCodeMap:           expSet.IsEnabled(experimental.CodeMap),
		CodeMapProvider:         codeMapProvider,
		EnableSyntaxRanges:      true,
		EnableDocumentIngestion: expSet.IsEnabled(experimental.DocumentIngestion),
		Sandbox:                 cmdSandbox,
	})
	// Git worktree tools. enter_worktree / exit_worktree always prompt
	// (auto-mode safety floor); see IsAutoModeSafetyFloor.
	reg.Register(&agent.EnterWorktreeTool{Cwd: cwdRef, Sandbox: cmdSandbox})
	reg.Register(&agent.ExitWorktreeTool{Cwd: cwdRef})
	reg.Register(&agent.WorktreeStatusTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeListTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeAddTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeRemoveTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeLockTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreeUnlockTool{Cwd: cwdRef})
	reg.Register(&agent.GitWorktreePruneTool{Cwd: cwdRef})
	reg.Register(&agent.FetchURLTool{})
	registerMemoryTools(reg, cwdRef, embedClient, fileCfg.Retrieval.Strategy, fileCfg.Retrieval.SemanticWeight, memory.Source{Session: sess.ID})
	reg.Register(&agent.GitTool{Cwd: cwdRef, LSPManager: lspManager})
	reg.Register(&agent.TodoWriteTool{Store: planStore})
	// ExitPlanModeTool is registered for schema parity with the TUI
	// build. The adapter-tools filter in the loop hides it whenever
	// PlanMode is inactive (which is always, in oneshot v1) so the
	// model never sees it. Plan mode itself is not yet exposed via
	// any oneshot flag. EnterPlanModeTool is deliberately NOT
	// registered here: entering plan mode requires the TUI's
	// confirmation card + state handshake, and oneshot has no
	// interactive approval surface to host it.
	reg.Register(&agent.ExitPlanModeTool{})

	// Subagents: load definitions (built-in + ~/.yottacode/agents +
	// .yottacode/agents) and register the dispatch tool. Background
	// runs are rejected in oneshot — the model gets a recoverable
	// error string and can retry without the flag.
	validSubagentTools := reg.Names()
	validSubagentTools[agent.ConsultAdvisorToolName] = true
	subRes, _ := subagents.LoadAll(cwd, validSubagentTools)
	for _, w := range subRes.Warnings {
		fmt.Fprintln(os.Stderr, "subagents: "+w)
	}
	// Resolve the transcript dir but don't create it: openTranscript
	// MkdirAlls on the first dispatch, so a run with no subagent leaves
	// no empty ~/.yottacode/memory/projects/<slug>/ behind.
	transcriptDir, _ := subagents.TranscriptDirFor(cwd)
	tasks := subagents.NewRegistry()
	// Oneshot doesn't expose plan/auto modes (no UI surface), so
	// the parent's mode states are inactive instances. Subagents
	// inherit them by pointer for consistency with the TUI path
	// even though they'll never flip on in this context.
	parentPlanMode := &agent.PlanModeState{}
	parentAutoMode := &agent.AutoModeState{}
	parentYoloMode := &agent.YoloModeState{}

	// expSet was resolved earlier (before prompt composition) so the
	// dispatch steering could be baked into the system prompt; reuse it.
	// Note: background subagents stay disabled in oneshot regardless of
	// the gate (no long-running session to host async completions);
	// dispatch is foreground-blocking here and honors the gate.

	agentTool := &agent.AgentTool{
		Configs:         subRes.Configs,
		Tasks:           tasks,
		Adapter:         ad,
		ParentRegistry:  reg,
		Permissions:     perms,
		YoloMode:        parentYoloMode,
		PlanMode:        parentPlanMode,
		AutoMode:        parentAutoMode,
		Cwd:             cwdRef,
		TranscriptDir:   transcriptDir,
		AllowBackground: false,
		// Foreground subagent spend is still bounded session-wide (oneshot
		// can fan out foreground children), same backstop as the TUI.
		MaxSessionTokens:   fileCfg.SubagentSessionTokenBudget(),
		ImplementerAdapter: oneshotRouterFast(routerAdapters),
		ImplementerModel:   oneshotRouterFastModel(routerAdapters),
		AdvisorAdapter:     oneshotRouterSmart(routerAdapters),
		AdvisorModel:       oneshotRouterSmartModel(routerAdapters),
		FastAdapter:        oneshotRouterFast(routerAdapters),
		FastModel:          oneshotRouterFastModel(routerAdapters),
		SmartAdapter:       oneshotRouterSmart(routerAdapters),
		SmartModel:         oneshotRouterSmartModel(routerAdapters),
		RouteAuto:          fileCfg.Router.RoutingAuto(),
		ModelResolver:      oneshotRouterResolve(routerAdapters, fileCfg.Router.RoutingEnabled()),
		ResolveWindow: func(model string) int {
			return catalog.ResolveWindowForProvider(fileCfg.ProviderKindForModel(model), model, fileCfg.ContextWindowOverride(model), fileCfg.Context.DefaultWindow)
		},
	}
	reg.Register(agentTool)
	// Even though oneshot rejects background spawns (AllowBackground=
	// false), foreground subagent runs still write to the registry,
	// so the parent can call get_subagent_result to re-read a
	// completed foreground task's transcript-equivalent in the same
	// turn. Rare workflow in oneshot, but keeping the surface
	// symmetric with the TUI is worth the one extra line.
	reg.Register(&agent.GetSubagentResultTool{Tasks: tasks})

	// Dispatch + integrate (foreground in oneshot when enabled).
	dispatchEnabled := expSet.IsEnabled(experimental.Dispatch)
	reg.Register(&agent.DispatchTool{Agent: agentTool, Enabled: dispatchEnabled, EnableLSP: true, LSPServers: fileCfg.LSP.Servers, LSPDisabled: fileCfg.LSP.Disabled, EnableSyntaxRanges: true, EnableDocumentIngestion: expSet.IsEnabled(experimental.DocumentIngestion), SandboxFactory: sandboxFactory})
	reg.Register(&agent.IntegrateTool{Cwd: cwdRef, Enabled: dispatchEnabled})

	// Skill tool: reuses the set loaded above for system-prompt
	// composition so the model's view stays consistent across both
	// surfaces.
	reg.Register(&agent.SkillTool{All: skillsRes.Skills})

	cfg := agent.LoopConfig{
		Adapter:           ad,
		Registry:          reg,
		Permissions:       perms,
		BypassPermissions: opts.BypassPermissions,
		Cwd:               cwdRef,
		MaxIterations:     opts.MaxIterations,
		PlanMode:          parentPlanMode,
		AutoMode:          parentAutoMode,
		YoloMode:          parentYoloMode,
	}
	if w := catalog.ResolveWindowForProvider(fileCfg.ProviderKindForModel(opts.Model), opts.Model, fileCfg.ContextWindowOverride(opts.Model), fileCfg.Context.DefaultWindow); w > 0 {
		// Oneshot has no turn boundary, so only provider-overflow recovery
		// is enabled here (Threshold 0 disables preemptive compaction). It
		// intentionally omits PreCompact for now: the saved oneshot session
		// may contain the compacted transcript, but the command has no
		// interactive /recall workflow to preserve mid-turn.
		cfg.Compaction = &agent.CompactionConfig{Window: w, Threshold: 0}
	}

	turnErr := stream(ctx, cfg, &sess.Messages, os.Stdout, os.Stderr)
	sess.Todos = planStore.Snapshot()
	if saveErr := sess.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "⚠ session save failed: %v\n", saveErr)
	}
	return turnErr
}

// registerMemoryTools wires the three agent-managed memory tools onto
// the registry with the same shape the TUI runner uses: memory_save
// and memory_search both carry the (possibly nil) embedder so headless
// saves get .vec sidecars and search ranks semantically when Ollama is
// available, and the search tool inherits the configured retrieval
// strategy instead of forcing "auto". Extracted so the registration —
// and the parity with the TUI surface — is unit-testable without
// driving a full one-shot run. A nil embedder degrades gracefully:
// memory_save skips the sidecar, search and injection fall back to
// BM25.
func registerMemoryTools(reg *agent.Registry, cwdRef *agent.CwdRef, embedClient *memory.EmbedClient, strategy string, semanticWeight float64, source memory.Source) {
	reg.Register(&agent.MemorySaveTool{Cwd: cwdRef, Embedder: embedClient, Source: source})
	reg.Register(&agent.MemoryForgetTool{Cwd: cwdRef})
	reg.Register(&agent.MemorySearchTool{Cwd: cwdRef, Embedder: embedClient, Strategy: strategy, SemanticWeight: semanticWeight, SemanticWeightConfigured: true})
	reg.Register(&agent.MemoryAuditTool{Cwd: cwdRef})
	reg.Register(&agent.MemoryCurateApplyTool{Cwd: cwdRef})
	reg.Register(&agent.MemoryArchivePruneTool{Cwd: cwdRef})
	reg.Register(&agent.MemoryGetTool{Cwd: cwdRef})
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

func preflight(ctx context.Context, cfg adapter.Config) error {
	result := adapter.Probe(ctx, cfg)
	if len(result.Issues) == 0 {
		return nil
	}
	return fmt.Errorf("provider preflight failed: %s", strings.Join(result.Issues, "; "))
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

// appendSkillsSection frames the Agent Skills surface in the system
// prompt without enumerating the skills — the name+description list lives
// in the `Skill` tool's schema description (always in the window), so
// listing it here too would duplicate the metadata tier and double its
// per-turn cost. Mirrors tui/run.go's helper of the same name.
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

// oneshotRouterFast / oneshotRouterFastModel / oneshotRouterResolve
// adapt cli.RouterAdapters to the agent.AgentTool fields, nil-safe when
// routing is disabled. Mirror the TUI's routerFast helpers.
func oneshotRouterFast(ra *cli.RouterAdapters) agent.Streamer {
	if ra == nil || ra.Fast == nil {
		return nil
	}
	return ra.Fast
}

func oneshotRouterFastModel(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.FastModel
}

func oneshotRouterSmart(ra *cli.RouterAdapters) agent.Streamer {
	if ra == nil || ra.Smart == nil {
		return nil
	}
	return ra.Smart
}

func oneshotRouterSmartModel(ra *cli.RouterAdapters) string {
	if ra == nil {
		return ""
	}
	return ra.SmartModel
}

func oneshotRouterResolve(ra *cli.RouterAdapters, enabled bool) func(string) agent.Streamer {
	if !enabled || ra == nil || ra.Resolve == nil {
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

func hasBuiltin(tools []adapter.BuiltinToolKind, want adapter.BuiltinToolKind) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

// recomposeSessionSystemPrompt rewrites the session's system message.
// headBytes marks the stable cache prefix (the static base prompt ahead
// of the memory tail) — see adapter.Message.CacheHeadBytes.
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
