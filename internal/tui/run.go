package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
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

	cfg := agent.LoopConfig{
		Adapter:           ad,
		Registry:          reg,
		Permissions:       perms,
		BypassPermissions: opts.BypassPermissions,
		Cwd:               cwd,
		MaxIterations:     opts.MaxIterations,
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
	})
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
