package agentruntime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// newTestSpec sets up an isolated HOME (so config/permissions/session
// reads never touch the real user's ~/.yottacode) and a temp project cwd,
// plus a stub OpenAI-compatible /models endpoint so Build's preflight
// probe (the fallback, no-router path every test here takes) succeeds
// without real network access — mirrors oneshot_test.go's
// TestPreflight_ModelInvisible stub pattern.
func newTestSpec(t *testing.T) SessionSpec {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
	}))
	t.Cleanup(srv.Close)

	return SessionSpec{
		ChatOptions: cli.ChatOptions{
			Model:         "test-model",
			BaseURL:       srv.URL,
			APIKey:        "sk-test",
			ProviderKind:  "openai",
			MaxIterations: 10,
		},
		Cwd: cwd,
	}
}

func mustBuild(t *testing.T, spec SessionSpec) *Runtime {
	t.Helper()
	rt, err := NewBuilder().Build(context.Background(), spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return rt
}

type blockingCleanupTool struct {
	agent.Tool
	started chan struct{}
}

func (t blockingCleanupTool) Cleanup(ctx context.Context) error {
	close(t.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestRuntimeCloseBoundsBackgroundContext(t *testing.T) {
	oldTimeout := runtimeCloseTimeout
	runtimeCloseTimeout = 20 * time.Millisecond
	t.Cleanup(func() { runtimeCloseTimeout = oldTimeout })

	reg := agent.NewRegistry()
	started := make(chan struct{})
	base := &agent.MemorySearchTool{}
	reg.Register(blockingCleanupTool{Tool: base, started: started})
	rt := &Runtime{Registry: reg}
	begin := time.Now()
	rt.Close(context.Background())
	select {
	case <-started:
	default:
		t.Fatal("cleanup hook was not called")
	}
	if elapsed := time.Since(begin); elapsed > 200*time.Millisecond {
		t.Fatalf("Close exceeded internal deadline: %v", elapsed)
	}
}

// TestBuild_CoreToolsRegistered locks in that Build wires the shared core
// toolset regardless of caller shape — the whole point of the extraction.
func TestBuild_CoreToolsRegistered(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)

	for _, name := range []string{
		"read_file", "write_file", "edit_file", "run_bash", "run_tests",
		"git_stage_files", "git_commit",
		"memory_save", "memory_search", "todo_write", "exit_plan_mode",
		"enter_plan_mode", "loop_control", "fetch_url",
		agent.AgentToolName, "get_subagent_result", "dispatch", "integrate", agent.SkillToolName,
	} {
		if _, ok := rt.Registry.Get(name); !ok {
			t.Errorf("expected tool %q to be registered by Build", name)
		}
	}
}

func TestBuild_CodeMapPromptMatchesToolGate(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		spec := newTestSpec(t)
		rt := mustBuild(t, spec)

		if strings.Contains(rt.RawSystemPrompt, agent.CodeMapPromptAddendum) {
			t.Error("RawSystemPrompt includes CodeMapPromptAddendum while Code Map is disabled")
		}
		if _, ok := rt.Registry.Get("code_map"); ok {
			t.Error("code_map tool is registered while Code Map is disabled")
		}
	})

	t.Run("enabled with default base", func(t *testing.T) {
		spec := newTestSpec(t)
		spec.ChatOptions.Experimental = []string{string(experimental.CodeMap)}
		rt := mustBuild(t, spec)

		want := defaultSystemPrompt + "\n\n" + agent.CodeMapPromptAddendum
		if rt.RawSystemPrompt != want {
			t.Fatalf("RawSystemPrompt did not append CodeMapPromptAddendum before composition\ngot:  %q\nwant: %q", rt.RawSystemPrompt, want)
		}
		if _, ok := rt.Registry.Get("code_map"); !ok {
			t.Error("code_map tool is not registered while Code Map is enabled")
		}
	})

	t.Run("enabled with custom base", func(t *testing.T) {
		spec := newTestSpec(t)
		spec.ChatOptions.SystemPrompt = "custom base prompt"
		spec.ChatOptions.Experimental = []string{string(experimental.CodeMap)}
		rt := mustBuild(t, spec)

		want := "custom base prompt\n\n" + agent.CodeMapPromptAddendum
		if rt.RawSystemPrompt != want {
			t.Fatalf("RawSystemPrompt did not preserve the custom base before the Code Map addendum\ngot:  %q\nwant: %q", rt.RawSystemPrompt, want)
		}
		if !strings.Contains(rt.BaseSystemPrompt, agent.CodeMapPromptAddendum) {
			t.Error("composed BaseSystemPrompt lost CodeMapPromptAddendum")
		}
	})
}

// TestBuild_GitHubToolSuiteRegistered is the regression test for a real
// gap: the pr_*/issue_*/git_push composite tools were registered only
// in internal/tui/run.go, never in Build, even after the ACP bucket-A
// slash commands (git-push, git-create-pr, git-update-pr,
// git-create-issue, git-review-pr, git-implement-issue, code-review)
// shipped assuming these tools exist in every session's registry. An
// ACP session driving any of those macros would fail on its first
// tool call. Checked for both worktree-tool-gating shapes (ACP and
// TUI/oneshot) since the bug applied to both equally — it was never
// about DisableWorktreeTools, just a registration that only ever ran
// from internal/tui/run.go's own code path.
func TestBuild_GitHubToolSuiteRegistered(t *testing.T) {
	names := []string{
		"pr_context", "pr_create", "pr_review_context", "pr_watch_checks",
		"pr_check_logs", "pr_rerun_checks", "code_review_context", "pr_read",
		"issue_read", "issue_list", "issue_context", "issue_create",
		"git_push", "pr_update", "pr_add_comment",
	}
	for _, disableWorktree := range []bool{true, false} {
		spec := newTestSpec(t)
		spec.DisableWorktreeTools = disableWorktree
		rt := mustBuild(t, spec)
		for _, name := range names {
			if _, ok := rt.Registry.Get(name); !ok {
				t.Errorf("DisableWorktreeTools=%v: expected tool %q to be registered by Build", disableWorktree, name)
			}
		}
		if rt.GHClient == nil {
			t.Errorf("DisableWorktreeTools=%v: expected rt.GHClient to be set", disableWorktree)
		}
	}
}

func TestBuild_AttributionConfigWiredConsistently(t *testing.T) {
	tests := []struct {
		name        string
		configBody  string
		wantTrailer string
		wantFooter  string
	}{
		{name: "defaults", wantTrailer: config.DefaultCommitAttributionTrailer, wantFooter: config.DefaultPRAttributionFooter},
		{name: "disabled", configBody: "[attribution]\ndisabled = true\n"},
		{name: "custom commit trailer", configBody: "[attribution]\ntrailer = \"custom trailer\"\n", wantTrailer: "custom trailer", wantFooter: config.DefaultPRAttributionFooter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := newTestSpec(t)
			if tt.configBody != "" {
				path := filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("mkdir config: %v", err)
				}
				if err := os.WriteFile(path, []byte(tt.configBody), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			rt := mustBuild(t, spec)

			commitAny, _ := rt.Registry.Get("git_commit_apply")
			commitTool := commitAny.(*agent.GitCommitApplyTool)
			legacyCommitAny, _ := rt.Registry.Get("git_commit")
			legacyCommitTool := legacyCommitAny.(*agent.GitCommitTool)
			createAny, _ := rt.Registry.Get("pr_create")
			createTool := createAny.(*agent.GHPRCreateTool)
			updateAny, _ := rt.Registry.Get("pr_update")
			updateTool := updateAny.(*agent.GHPRUpdateTool)
			dispatchAny, _ := rt.Registry.Get("dispatch")
			dispatchTool := dispatchAny.(*agent.DispatchTool)

			if commitTool.Trailer != tt.wantTrailer || legacyCommitTool.Trailer != tt.wantTrailer || dispatchTool.CommitTrailer != tt.wantTrailer {
				t.Fatalf("commit attribution: apply=%q legacy=%q dispatch=%q want=%q", commitTool.Trailer, legacyCommitTool.Trailer, dispatchTool.CommitTrailer, tt.wantTrailer)
			}
			if createTool.Footer != tt.wantFooter || updateTool.Footer != tt.wantFooter {
				t.Fatalf("PR attribution: create=%q update=%q want=%q", createTool.Footer, updateTool.Footer, tt.wantFooter)
			}
		})
	}
}

// TestBuild_SessionRecallRegistered is the same-shaped regression test
// for session_recall, the other model-callable tool that was TUI-only
// (internal/tui/run.go's own recall.Open + registration) even though
// nothing about it depends on TUI's UI. Unlike checkpoints/workspace
// trust/sensitivity posture — the other items in Runtime's "deliberately
// excluded" list — session_recall and the GitHub suite are the two that
// are agent-callable tools, not TUI interaction surfaces, so both belong
// in Build.
func TestBuild_SessionRecallRegistered(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)

	if _, ok := rt.Registry.Get("session_recall"); !ok {
		t.Error("expected tool \"session_recall\" to be registered by Build")
	}
	if rt.RecallIndex == nil {
		t.Error("expected rt.RecallIndex to be set")
	}
}

// TestBuild_WorktreeTools_GatedBySpec is the concurrency-safety decision
// from the plan: EnterWorktreeTool/ExitWorktreeTool (and the git_worktree_*
// admin wrappers) call process-global os.Chdir(), unsafe when one process
// hosts N concurrent sessions with different cwds. ACP-shaped specs must
// exclude them; oneshot/tui-shaped specs must keep them.
func TestBuild_WorktreeTools_GatedBySpec(t *testing.T) {
	worktreeTools := []string{
		"enter_worktree", "exit_worktree", "worktree_status",
		"git_worktree_list", "git_worktree_add", "git_worktree_remove",
		"git_worktree_lock", "git_worktree_unlock", "git_worktree_prune",
	}

	t.Run("enabled for oneshot/tui shape", func(t *testing.T) {
		spec := newTestSpec(t)
		spec.DisableWorktreeTools = false
		rt := mustBuild(t, spec)
		for _, name := range worktreeTools {
			if _, ok := rt.Registry.Get(name); !ok {
				t.Errorf("expected worktree tool %q when DisableWorktreeTools=false", name)
			}
		}
	})

	t.Run("disabled for acp shape", func(t *testing.T) {
		spec := newTestSpec(t)
		spec.DisableWorktreeTools = true
		rt := mustBuild(t, spec)
		for _, name := range worktreeTools {
			if _, ok := rt.Registry.Get(name); ok {
				t.Errorf("worktree tool %q must be absent when DisableWorktreeTools=true (os.Chdir is unsafe for concurrent ACP sessions)", name)
			}
		}
	})
}

// TestBuild_SupportsBackgroundDispatch_ControlsAgentAndDispatchTools pins
// the plan's canonical-behavior split: oneshot has no long-lived session
// to host async completions (AllowBackground/SupportsBackground=false),
// while tui and acp are both long-lived and get background dispatch.
func TestBuild_SupportsBackgroundDispatch_ControlsAgentAndDispatchTools(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(fmt.Sprintf("supportsBackground=%v", want), func(t *testing.T) {
			spec := newTestSpec(t)
			spec.SupportsBackgroundDispatch = want
			rt := mustBuild(t, spec)

			agentTool, ok := rt.AgentTool, rt.AgentTool != nil
			if !ok {
				t.Fatalf("Runtime.AgentTool is nil")
			}
			if agentTool.AllowBackground != want {
				t.Errorf("AgentTool.AllowBackground = %v, want %v", agentTool.AllowBackground, want)
			}

			dispatchTool, ok := rt.Registry.Get("dispatch")
			if !ok {
				t.Fatalf("dispatch tool not registered")
			}
			dt, ok := dispatchTool.(*agent.DispatchTool)
			if !ok {
				t.Fatalf("dispatch tool is %T, want *agent.DispatchTool", dispatchTool)
			}
			if dt.SupportsBackground != want {
				t.Errorf("DispatchTool.SupportsBackground = %v, want %v", dt.SupportsBackground, want)
			}
		})
	}
}

// TestBuild_MCPManagerAlwaysConstructed is the "new shared surface" from
// the plan: oneshot had zero MCP integration before this extraction.
// Build must always construct an *mcp.Manager, even with no configured
// servers, so a future session/new call carrying per-session MCP servers
// has something to Add() against.
// TestBuild_CodeMapWatchIntegration exercises the real construction and
// teardown path for CachedProvider.StartWatch wired into Build/Close (see
// runtime.go's codeMapProvider construction and the CachedProvider.Close
// call in Runtime.Close) — the one part of the Code Map watcher work that
// unit tests on CachedProvider alone can't cover, since they never go
// through Builder.Build.
func TestBuild_CodeMapWatchIntegration(t *testing.T) {
	spec := newTestSpec(t)
	spec.ChatOptions.Experimental = []string{"code_map"}
	rt := mustBuild(t, spec)

	if rt.CodeMapProvider == nil {
		t.Fatal("Runtime.CodeMapProvider is nil with code_map enabled")
	}
	if _, ok := rt.Registry.Get("code_map"); !ok {
		t.Error("expected code_map tool to be registered")
	}
	idx, err := rt.CodeMapProvider.Index(context.Background())
	if err != nil {
		t.Fatalf("CodeMapProvider.Index: %v", err)
	}
	if idx == nil {
		t.Fatal("CodeMapProvider.Index returned a nil index")
	}
	rt.Close(context.Background()) // must not panic; stops the watch goroutine
}

func TestBuild_MCPManagerAlwaysConstructed(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)
	if rt.MCPManager == nil {
		t.Fatal("Runtime.MCPManager is nil — MCP absorption must be unconditional")
	}
	if got := len(rt.MCPManager.Names()); got != 0 {
		t.Errorf("expected zero configured MCP servers, got %d", got)
	}
}

// TestBuild_DeferMCPStart_ConstructsButDoesNotStart guards TUI's async
// startup UX: with no servers configured, Start() has nothing to do
// either way, but the manager must still exist and be untouched by Build
// so the caller (TUI's own cmd_mcp.go) can Start() it later itself
// without Build having raced ahead and called Start() first.
func TestBuild_DeferMCPStart_ConstructsButDoesNotStart(t *testing.T) {
	spec := newTestSpec(t)
	spec.DeferMCPStart = true
	rt := mustBuild(t, spec)
	if rt.MCPManager == nil {
		t.Fatal("Runtime.MCPManager is nil even with DeferMCPStart — the caller needs a manager to Start() later")
	}
}

// TestBuild_MCPServers_SessionOverridesGlobalByName exercises
// mergeMCPServers directly: a session-scoped server must win over a
// global one of the same name (ACP's session/new should be able to point
// an already-configured server name at a different endpoint).
func TestBuild_MCPServers_SessionOverridesGlobalByName(t *testing.T) {
	global := []config.MCPServer{{Name: "docs", Command: "global-cmd"}, {Name: "other", Command: "other-cmd"}}
	session := []config.MCPServer{{Name: "docs", Command: "session-cmd"}}

	merged := mergeMCPServers(global, session)
	if len(merged) != 2 {
		t.Fatalf("merged = %d servers, want 2: %+v", len(merged), merged)
	}
	byName := map[string]config.MCPServer{}
	for _, s := range merged {
		byName[s.Name] = s
	}
	if byName["docs"].Command != "session-cmd" {
		t.Errorf("session-scoped %q did not override global: got command %q", "docs", byName["docs"].Command)
	}
	if byName["other"].Command != "other-cmd" {
		t.Errorf("non-overridden global server %q lost its command: got %q", "other", byName["other"].Command)
	}
}

// TestBuild_CompactionAlwaysRich pins the canonical-behavior decision:
// every caller now gets the rich compaction config (TargetRatio set),
// not oneshot's old bare Window+Threshold:0 — a long-lived ACP session
// needs it at least as much as the TUI does.
func TestBuild_CompactionAlwaysRich(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)
	if rt.Cfg.Compaction == nil {
		t.Fatal("Cfg.Compaction is nil — expected a compaction config when the model has a resolvable context window")
	}
	if rt.Cfg.Compaction.TargetRatio <= 0 {
		t.Errorf("Cfg.Compaction.TargetRatio = %v, want > 0 (rich compaction, not oneshot's old bare Window+Threshold:0)", rt.Cfg.Compaction.TargetRatio)
	}
}

// TestBuild_BypassPermissionsWired is the regression test for a real
// gap the Builder extraction introduced silently: ChatOptions.
// BypassPermissions (the internal name for --yolo) was never carried
// into the constructed agent.LoopConfig, so `yottacode run --yolo`
// stopped bypassing approval entirely — oneshot has no equivalent of
// internal/tui/cmd_yolo.go's enterYoloMode, which sets a separate,
// higher-priority YoloMode flag instead. TUI/ACP sessions were
// unaffected (YoloMode short-circuits first), which is why this went
// unnoticed until a direct oneshot code-review pass caught it.
func TestBuild_BypassPermissionsWired(t *testing.T) {
	for _, v := range []bool{true, false} {
		spec := newTestSpec(t)
		spec.ChatOptions.BypassPermissions = v
		rt := mustBuild(t, spec)
		if rt.Cfg.BypassPermissions != v {
			t.Errorf("BypassPermissions=%v: Cfg.BypassPermissions = %v, want %v", v, rt.Cfg.BypassPermissions, v)
		}
	}
}

// TestBuild_ResumeImportsSubagentTasks confirms a resumed session's
// persisted SubagentTasks get imported into the fresh registry so
// task-ids the model wrote into the conversation still resolve — the
// oneshot bug the plan flags (oneshot never exported them, so they were
// silently lost) is specifically about export, handled by callers after
// Build; this test guards the import half, which Build owns.
func TestBuild_ResumeImportsSubagentTasks(t *testing.T) {
	spec := newTestSpec(t)
	first := mustBuild(t, spec)
	if !first.Fresh {
		t.Fatalf("first Build should report a fresh session")
	}
	first.Session.Messages = append(first.Session.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello"},
	)
	first.Session.SubagentTasks = []subagents.TaskRecord{
		{ID: "task-1", AgentType: "reviewer", Status: subagents.TaskCompleted, Result: "looks good"},
	}
	if err := first.Session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	resumeSpec := spec
	resumeSpec.Resume = first.Session.ID
	second := mustBuild(t, resumeSpec)
	if second.Fresh {
		t.Fatalf("resumed Build should report Fresh=false")
	}
	if _, ok := second.SubagentTasks.Get("task-1"); !ok {
		t.Error("resumed session's SubagentTasks were not imported into the new Registry")
	}
}

// TestBuild_RawSystemPromptIsPreComposition is the regression test for
// a real bug introduced by the Builder extraction: internal/tui/run.go
// used to hand its own per-turn/reload recomposition paths
// (rebuildSystemPromptForTurn, reloadMemoryNow,
// recomposeSystemPromptWithSkills — all of which call
// composeSystemPrompt/appendSkillsSection themselves) the *raw* base
// prompt. After the extraction it was wired to rt.BaseSystemPrompt,
// which Build itself already runs through composeSystemPrompt +
// appendSkillsSection — so every TUI turn, /memory reload, and /skills
// change re-applied both, duplicating the profile framing line and (for
// skills) appending a second skills section on top of the one already
// baked in. RawSystemPrompt exists specifically so TUI has a
// pre-composition value to hand those paths instead. This test locks in
// that RawSystemPrompt really is the input composeSystemPrompt +
// appendSkillsSection were called with, i.e. re-running them against it
// reproduces BaseSystemPrompt exactly.
func TestBuild_RawSystemPromptIsPreComposition(t *testing.T) {
	spec := newTestSpec(t)
	rt := mustBuild(t, spec)

	if rt.RawSystemPrompt == "" {
		t.Fatal("RawSystemPrompt is empty")
	}
	if rt.RawSystemPrompt == rt.BaseSystemPrompt {
		// Only a meaningful signal if composition actually changes the
		// text — true for the default profile/skills used here.
		t.Fatal("RawSystemPrompt equals BaseSystemPrompt — composition never ran, or the fields alias each other")
	}
	recomposed := appendSkillsSection(composeSystemPrompt(rt.RawSystemPrompt, rt.Adapter.Profile()), rt.Skills)
	if recomposed != rt.BaseSystemPrompt {
		t.Fatalf("appendSkillsSection(composeSystemPrompt(RawSystemPrompt, ...), ...) != BaseSystemPrompt\ngot:  %q\nwant: %q", recomposed, rt.BaseSystemPrompt)
	}
}

// The tests below were ported verbatim (function names aside) from
// internal/oneshot/oneshot_test.go when composeSystemPrompt, preflight,
// registerMemoryTools, and the router-resolver gating moved into this
// package as part of the Builder extraction — preserving coverage rather
// than dropping it at the package boundary.

// TestRegisterMemoryTools_ParityWithTUI guards the oneshot↔TUI memory
// parity that predates this extraction: memory_search must be
// registered, and both memory_save and memory_search must carry the
// embedder + configured strategy so headless runs produce .vec sidecars
// and rank the same way the interactive surface does.
func TestRegisterMemoryTools_ParityWithTUI(t *testing.T) {
	reg := agent.NewRegistry()
	cwdRef := agent.NewCwdRef("/tmp")
	client := memory.NewEmbedClient("http://localhost:11434", "test-model")

	registerMemoryTools(reg, cwdRef, client, "bm25", 0.4, memory.Source{Session: "test-session"})

	for _, name := range []string{"memory_save", "memory_forget", "memory_search", "memory_audit", "memory_curate_apply", "memory_archive_prune", "memory_get"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("memory tool %q not registered", name)
		}
	}

	saveTool, _ := reg.Get("memory_save")
	save, ok := saveTool.(*agent.MemorySaveTool)
	if !ok {
		t.Fatalf("memory_save is %T, want *agent.MemorySaveTool", saveTool)
	}
	if save.Embedder != client {
		t.Error("memory_save did not receive the wired embedder — headless saves would skip .vec sidecars")
	}

	searchTool, _ := reg.Get("memory_search")
	search, ok := searchTool.(*agent.MemorySearchTool)
	if !ok {
		t.Fatalf("memory_search is %T, want *agent.MemorySearchTool", searchTool)
	}
	if search.Embedder != client {
		t.Error("memory_search did not receive the wired embedder")
	}
	if search.Strategy != "bm25" {
		t.Errorf("memory_search strategy = %q, want the configured %q", search.Strategy, "bm25")
	}
}

// TestRegisterMemoryTools_NilEmbedderOK confirms the registration is
// safe when no embedder is available (Ollama absent) — all seven tools
// still register and degrade to BM25.
func TestRegisterMemoryTools_NilEmbedderOK(t *testing.T) {
	reg := agent.NewRegistry()
	registerMemoryTools(reg, agent.NewCwdRef("/tmp"), nil, "auto", 0.4, memory.Source{})
	for _, name := range []string{"memory_save", "memory_forget", "memory_search", "memory_audit", "memory_curate_apply", "memory_archive_prune", "memory_get"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("memory tool %q not registered with nil embedder", name)
		}
	}
}

func TestComposeSystemPrompt_XAIPrefersXSearch(t *testing.T) {
	got := composeSystemPrompt("base", adapter.ProviderProfile{
		Provider: adapter.ProviderXAI,
		EnabledBuiltinTools: []adapter.BuiltinToolKind{
			adapter.BuiltinToolWebSearch,
			adapter.BuiltinToolXSearch,
		},
	})
	for _, want := range []string{"use x_search, not web_search", "X/Twitter posts", "Use web_search only for general web pages"} {
		if !strings.Contains(got, want) {
			t.Fatalf("xAI prompt guidance missing %q:\n%s", want, got)
		}
	}
}

func TestPreflight_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	err := preflight(context.Background(), adapter.Config{
		BaseURL:          srv.URL,
		APIKey:           "bad",
		Model:            "gpt-5",
		ProviderOverride: adapter.ProviderOpenAI,
	})
	if err == nil {
		t.Fatalf("expected preflight error")
	}
	if !strings.Contains(err.Error(), "authentication failed (HTTP 401)") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflight_ModelInvisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-4.1"}]}`)
	}))
	t.Cleanup(srv.Close)

	err := preflight(context.Background(), adapter.Config{
		BaseURL:          srv.URL,
		APIKey:           "sk-test",
		Model:            "gpt-5",
		ProviderOverride: adapter.ProviderOpenAI,
	})
	if err == nil {
		t.Fatalf("expected preflight error")
	}
	if !strings.Contains(err.Error(), `model "gpt-5" not listed by /models`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// routerModelResolver must be nil in mode off even when a pair is
// configured: adapters build regardless of mode (so /router can toggle
// live), but "off" promises every agent — including ones with explicit
// model: frontmatter — runs on the active model.
func TestRouterModelResolver_GatedOnMode(t *testing.T) {
	ra := &cli.RouterAdapters{Resolve: func(string) adapter.Streamer { return nil }}
	if got := routerModelResolver(ra, false); got != nil {
		t.Error("resolver must be nil when routing is off")
	}
	if got := routerModelResolver(ra, true); got == nil {
		t.Error("resolver must be wired when routing is enabled")
	}
	if got := routerModelResolver(nil, true); got != nil {
		t.Error("nil adapters must yield a nil resolver")
	}
}
