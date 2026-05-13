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
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/version"
	"github.com/yottadynamics/yottacode/internal/wizard"
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

	sess, fresh, err := openSession(opts, cwd)
	if err != nil {
		return err
	}
	mem, err := memory.Load(cwd)
	if err != nil {
		return err
	}
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
		})
	}
	if fresh {
		sess.Messages = append(sess.Messages, adapter.Message{
			Role:    adapter.RoleSystem,
			Content: memory.SystemPrompt(composeSystemPrompt(baseSys, ad.Profile()), mem),
		})
	} else {
		recomposeSessionSystemPrompt(sess, memory.SystemPrompt(composeSystemPrompt(baseSys, ad.Profile()), mem))
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

	// Path-validation policy: every mutating filesystem tool gets the
	// same WriteOpts. Cwd-confined writes by default; --allow-paths
	// expands the allowed roots; the deny list is hardcoded to keep
	// yottacode's own state and git internals off-limits.
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
		Cwd:            cwd,
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

	cfg := agent.LoopConfig{
		Adapter:           ad,
		Registry:          reg,
		Permissions:       perms,
		BypassPermissions: opts.BypassPermissions,
		Cwd:               cwd,
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
		Provider:               opts.Provider,
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
		MemorySummary:          mem.Summary().String(),
		BaseSystemPrompt:       baseSys,
		FileCfg:                fileCfg,
		Subagents:              subagentTasks,
		AgentTool:              agentTool,
	})
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
	return base + "\nFor live or current information, use fetch_url for specific pages or feeds when needed."
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
	return s, true, nil
}
