// Package agentruntime is the shared session-construction layer behind
// `yottacode run` (internal/oneshot), the TUI (internal/tui), and the ACP
// server (internal/acp). All three build the same "world" — registry,
// permissions, adapter, router, LSP manager, skills, subagents, MCP,
// compaction — and previously did so via two independently-drifting
// ~370/~850 line inline blocks. Builder.Build is the single place that
// construction happens now; callers layer their own presentation-specific
// state (Bubbletea Model, stdout streaming, checkpoints, recall) on top of
// the returned Runtime.
package agentruntime

import (
	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
)

// SessionSpec is the input to Builder.Build. It wraps cli.ChatOptions
// (the struct both current callers already resolve from flags/env/config
// via cli.Resolve) rather than re-deriving a hand-picked subset, plus the
// handful of fields ChatOptions has no room for.
type SessionSpec struct {
	cli.ChatOptions

	// Cwd is the session's working directory. ChatOptions carries no cwd
	// field — oneshot/tui both derive it from os.Getwd() today. ACP must
	// derive it from session/new's cwd parameter instead, and Build must
	// never call os.Getwd()/os.Chdir() itself: it hosts N concurrent
	// sessions with potentially different cwds in one process.
	Cwd string

	// MCPServers is session-scoped and additive on top of the global
	// fileCfg.MCPServers Build loads from config.toml. Empty for
	// oneshot/tui (they only ever use the global set); ACP's session/new
	// carries this from the client's mcpServers param. On name collision
	// a session-scoped entry overrides the global one of the same name.
	MCPServers []config.MCPServer

	// DisableWorktreeTools excludes EnterWorktreeTool/ExitWorktreeTool
	// from the registry. Both tools call process-global os.Chdir() in
	// addition to updating the session's CwdRef (see
	// EnterWorktreeTool.swapCwd) — safe when one process hosts one
	// session at a time (oneshot, TUI), unsafe when one process hosts N
	// concurrent sessions with different cwds (ACP). oneshot/tui pass
	// false; acp passes true.
	DisableWorktreeTools bool

	// SupportsBackgroundDispatch controls AgentTool.AllowBackground and
	// DispatchTool.SupportsBackground. oneshot passes false (no
	// long-lived session to host async completions — its own rationale,
	// documented at the oneshot call site historically). tui and acp
	// both pass true: both are long-lived processes that can host
	// detached background work and surface its completion later.
	SupportsBackgroundDispatch bool

	// PreCompact is the optional pre-compaction snapshot hook threaded
	// into LoopConfig.Compaction.PreCompact. tui supplies its snapshot
	// writer (writePreSummarySnapshot); oneshot and acp leave this nil,
	// which agent.CompactionConfig treats as "no snapshot taken."
	PreCompact func(history []adapter.Message) (string, error)

	// DeferMCPStart skips starting the MCP manager and registering its
	// tools inside Build. Build still constructs Runtime.MCPManager (via
	// mcp.NewManager) so the caller has something to Start() later — it
	// just doesn't block Build on it. TUI sets this: it deliberately
	// starts MCP servers asynchronously via its own Bubbletea tea.Cmd
	// (internal/tui/cmd_mcp.go) so slow/hanging npx-based servers don't
	// delay first paint, and that existing machinery already knows how
	// to Start() the manager and register tools once it has a
	// *mcp.Manager and *agent.Registry to work with — nothing new is
	// needed there. oneshot and acp both leave this false: neither has a
	// progressive-UI concept, so Build's synchronous start-and-register
	// is exactly what they want (acp's session/new must return only
	// once the session is actually ready).
	DeferMCPStart bool

	// SupportsLiveRouterToggle controls whether Build attempts
	// cli.BuildRouterAdapters even when [router].mode is "off". TUI and
	// ACP are long-lived and let a user flip routing on mid-session
	// (/advisor on, or ACP's "Advisor routing" session config option) —
	// both need the advisor/implementer pair pre-resolved at startup so
	// that toggle can work instantly and so the pair's mere presence can
	// be checked (see internal/tui/cmd_router.go's routerOn and
	// internal/acp/config_options.go's sessionConfigOptions). oneshot has
	// no such toggle — a single `yottacode run` never flips [router].mode
	// mid-session — so it leaves this false: with routing off, Build
	// skips resolving the pair entirely rather than surfacing a stale or
	// misconfigured advisor pair's resolution error as a confusing
	// warning for a session that would never use it anyway.
	SupportsLiveRouterToggle bool
}
