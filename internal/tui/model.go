package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/checkpoint"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	githubapi "github.com/yottadynamics/yottacode/internal/github"
	mcppkg "github.com/yottadynamics/yottacode/internal/mcp"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/providerops"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/skills"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/usercmd"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// worktreeNameFromPath extracts the yottacode worktree name from any
// path that lives under ~/.yottacode/worktrees/<slug>/<name>/..., or
// returns "" when the path is in the main checkout (or outside the
// yottacode worktree tree entirely). Used by the CwdChanged handler
// so the status-bar chip stays in sync with mid-session
// enter/exit_worktree swaps.
func worktreeNameFromPath(path string) string {
	_, name, ok := worktree.IsAnyWorktreePath(path)
	if !ok {
		return ""
	}
	return name
}

// Config carries everything Run needs to build a Model. Bundling these into a
// struct is just ergonomics — there are too many fields for a positional
// argument list to stay readable.
type Config struct {
	Cfg         agent.LoopConfig
	Session     *session.Session
	Permissions *permissions.Permissions
	Recall      *recall.Index // optional; nil disables /recall
	ModelName   string
	BaseURL     string
	APIKey      string
	Provider    string
	// ProviderLabel is the catalog identity ("nvidia-nim", "xai") of
	// the active profile, surfaced in the status bar instead of the
	// generic dispatch tag (Provider). Populated by Run() from the
	// loaded config; leave empty when no profile is matched and the
	// renderer falls back to Provider.
	ProviderLabel          string
	ReasoningEffort        string
	EnableWebSearch        bool
	DisableWebSearch       bool
	EnableXSearch          bool
	EnableCodeInterpreter  bool
	SearchAllowedDomains   string
	SearchExcludedDomains  string
	XSearchAllowedHandles  string
	XSearchExcludedHandles string
	XSearchFromDate        string
	XSearchToDate          string
	ProviderProfile        adapter.ProviderProfile
	Cwd                    string
	Version                string   // e.g. "0.3.0" — shown in the header
	Commit                 string   // short SHA the binary was built from; "" when unknown (go run, tarball)
	Dirty                  bool     // true when the build had uncommitted changes; renders a "*" beside the commit
	Branch                 string   // current git branch (empty if not in a repo)
	ProjectRoots           []string // roots counting as this project (repo root + its worktree container); auto-recall's project scope matches them and everything below
	SensitiveProject       bool     // this project is marked sensitive: no automatic recall injection at all
	SensitiveRoots         []string // every sensitive root; their sessions never surface in any project's recall
	Worktree               string   // yottacode worktree name when running inside one (empty for main checkout); rendered as a status-line chip
	MemorySummary          string   // "USER", "YOTTA", "USER+YOTTA", "UMEM", "USER+UMEM", or "" if none
	BaseSystemPrompt       string   // pre-memory prompt — needed by /memory reload to recompose
	EmbedClient            *memory.EmbedClient

	// FileCfg holds tunables loaded from ~/.yottacode/config.toml
	// (context watermarks, retrieval). The TUI reads these at session
	// start and re-reads them on /memory reload.
	FileCfg config.Config

	// Subagents is the session task registry the /subagents slash
	// command inspects. Populated by run.go alongside Agent tool
	// registration; nil disables /subagents (tests that don't wire
	// subagents in are still valid).
	Subagents *subagents.Registry

	// ExperimentalEnabled is the sorted list of experimental feature
	// names switched on this session (experimental.Set.EnabledNames).
	// The /experimental overlay renders the full catalog and marks these
	// on; empty means none are enabled.
	ExperimentalEnabled []string

	// AgentTool is the dispatch tool registered on Cfg.Registry. The
	// TUI keeps a typed reference so the slash command can introspect
	// the resolved agent list, and so the background-done callback
	// can be wired to push events onto the model's session inbox.
	AgentTool *agent.AgentTool

	// CustomCommands is the set of user-authored slash commands
	// loaded from ~/.yottacode/commands/ and <cwd>/.yottacode/commands/
	// at startup. New() builds slashCommand entries from these and
	// stores them on the Model so the dispatcher and /help can see
	// them alongside built-ins.
	CustomCommands []usercmd.Command

	// MCPManager is the live MCP client lifecycle manager, built from
	// config.MCPServers at session start. The /mcp slash command
	// inspects + restarts servers through it. Nil when no servers are
	// configured (the slash command renders "no servers configured").
	MCPManager *mcppkg.Manager

	// Skills is the resolved set of Agent Skills (built-in + user +
	// project) loaded at startup. New() builds slashCommand entries
	// from skills with metadata.slash != "false" so `/<skill-name>`
	// works alongside model-side invocation via the Skill tool.
	Skills []skills.Skill

	// SkillTool is the live Skill dispatch tool registered on
	// Cfg.Registry. The TUI keeps a typed reference so the /skills
	// picker can read SkillTool.All (the universe) and call
	// SkillTool.SetEnabled (the per-session filter). nil disables the
	// /skills picker; the slash command still falls through to the
	// "unknown command" error in that case.
	SkillTool *agent.SkillTool

	// RouterAdapters holds the resolved fast/smart task-routing adapters.
	// RouterMode is the live routing mode. They drive the /router picker and
	// live rewiring without switching the main-thread adapter mid-turn.
	RouterAdapters *cli.RouterAdapters
	RouterMode     string
	Options        cli.ChatOptions

	// SummarizerAdapter routes the /summarize + auto-compaction call to
	// the fast model under cache-safe routing. nil → New() falls back to
	// Cfg.Adapter (the legacy single-adapter behavior).
	SummarizerAdapter agentStreamer
	// SummarizerModel is the fast model's name for routing telemetry;
	// empty when summarization isn't routed.
	SummarizerModel string
}

// Model is the Bubbletea state for the chat TUI. The TUI runs in inline mode
// (no alt-screen): conversation lines flow into the terminal's native
// scrollback via tea.Println, while only the live footer (input box + status
// bar + transient overlays) redraws in place at the bottom. This means the
// terminal — not the app — owns history, so native selection, scroll-wheel,
// and copy work end-to-end.
type Model struct {
	parentCtx context.Context
	cfg       agent.LoopConfig
	modelName string
	baseURL   string
	apiKey    string
	provider  string
	// providerLabel is the human-readable identity shown in the
	// status bar — typically the catalog entry name ("nvidia-nim",
	// "xai") rather than the dispatch kind ("openai-compatible").
	// Empty when the active profile doesn't trace back to a catalog
	// entry; renderers fall back to `provider` in that case. Kept
	// separate from `provider` because that field is the
	// adapter-dispatch tag (ProviderOverride) and changing it
	// breaks dispatch routing.
	providerLabel          string
	reasoningEffort        string
	enableWebSearch        bool
	disableWebSearch       bool
	enableXSearch          bool
	enableCodeInterpreter  bool
	searchAllowedDomains   string
	searchExcludedDomains  string
	xSearchAllowedHandles  string
	xSearchExcludedHandles string
	xSearchFromDate        string
	xSearchToDate          string
	providerProfile        adapter.ProviderProfile
	cwd                    string
	projectRoots           []string // roots counting as this project, resolved once at startup; scopes auto-recall
	sensitiveProject       bool     // auto-recall suppressed entirely for this project
	sensitiveRoots         []string // sensitive roots excluded from every recall search
	perms                  *permissions.Permissions
	recall                 *recall.Index
	version                string
	commit                 string
	dirty                  bool
	branch                 string
	worktree               string // yottacode worktree name when session runs inside one
	// currentPR is the open pull request number for the current branch.
	// Zero means no PR has been detected (or GitHub/auth was unavailable).
	currentPR int
	// githubClient is the typed GitHub client shared with PR tools. The
	// startup/status probe uses it when run.go wires one in; tests may leave
	// it nil and still exercise the pure rendering path.
	githubClient     githubapi.Interface
	memorySummary    string
	baseSystemPrompt string // pre-memory prompt; used by /memory reload
	embedClient      *memory.EmbedClient

	// routerAdapters/routerMode hold live task-routing state for /router.
	// Routing only changes isolated contexts (summarization/subagents), not the
	// main-thread adapter, so prompt-cache locality is preserved.
	router     *cli.RouterAdapters
	routerMode string
	opts       cli.ChatOptions

	// summarizerAdapter is the streamer the /summarize + auto-compaction
	// path calls into. When cache-safe routing is on it points at the
	// fast model (compaction is a single isolated call on a near-full
	// context — a large, safe saving); otherwise it equals cfg.Adapter.
	// Never nil after New().
	summarizerAdapter agentStreamer
	// summarizerModel is the fast model's name (for routing telemetry /
	// future per-model usage attribution); empty when not routed.
	summarizerModel string

	// fileCfg mirrors ~/.yottacode/config.toml. Fields read by the
	// extractor (confidence threshold, max input) and the watermark
	// tracker (warn / auto thresholds, default window). Live-reloaded
	// by /memory reload.
	fileCfg config.Config

	// contextTokens is the estimated token count of the current
	// session's message history — drives the status bar `ctx=` segment
	// and the watermark thresholds. Distinct from statsTokens, which
	// is the cumulative count of streamed tokens this session (used
	// for live throughput display).
	contextTokens int

	// lastWatermarkPct is the highest fill ratio at which we've already
	// emitted a notice. Reset to 0 when usage drops back below the
	// warn threshold (after /summarize, /clear, /resume). The 5% step
	// constant in watermark.go gates re-notification.
	lastWatermarkPct float64

	// nonConvergentAt records the fill ratio at which the last
	// auto-summarize failed to converge (compacted history still ≥
	// auto_threshold — the irreducible floor of system prompt + tool
	// schemas + retained tail exceeds this window). While set, the auto
	// gate suppresses re-runs at ~this fill against ~this window so it
	// doesn't burn a multi-minute compression every turn to land in the
	// same place. It is NOT a permanent latch: the gate re-arms once fill
	// grows a step past this point or the window changes (a /model switch
	// or a drift pin makes the verdict stale). 0 means "no stuck
	// summarize on record." nonConvergentWindow is the resolved window at
	// the time, so a window change invalidates the record even when the
	// fill ratio lands back in the suppressed band.
	nonConvergentAt     float64
	nonConvergentWindow int

	// compactionSeq increments after any successful mid-turn compaction.
	// Manual/auto summarize captures it when cloning history; if it moves
	// before summaryDoneMsg lands, the summary result is stale and must not
	// overwrite the compactor's newer history.
	compactionSeq int

	// lastContextSummary and lastContextCompaction are short diagnostic
	// breadcrumbs shown by /context. They stay in memory only because
	// scrollback already contains the user-visible status lines.
	lastContextSummary    string
	lastContextCompaction string

	// summarizing flips true while /summarize (manual or auto) is
	// running so we don't fire a second one on top of the first if
	// the user crosses another threshold while we're working.
	summarizing bool

	// summarizeFailures counts consecutive summarize failures while auto routing
	// is using the fast model. At the threshold we degrade to smart for the next
	// attempt so a fast-provider outage cannot block compaction.
	summarizeFailures int

	// summarizeStart timestamps when the active summarization began, so
	// the live summarizing row can show elapsed time. Only meaningful
	// while summarizing is true.
	summarizeStart time.Time

	// probedModels records models whose context-window probe has already
	// been attempted this session (success OR failure), so a model whose
	// probe keeps failing isn't re-probed on every model switch. A
	// successful probe persists to config and is skipped by the override
	// check anyway; this guards the failure case. nil-safe to read; the
	// write site lazily initializes it.
	probedModels map[string]bool

	// Cumulative session stats — incremented as events stream in.
	statsTokens    int
	statsToolCalls int

	// Per-turn throughput tracking
	turnStart     time.Time
	turnTokens    int
	turnToolCalls int
	// turnUsage accumulates this turn's exact provider-reported usage
	// (summed across the turn's AssistantMessages) so the end-of-turn
	// "Thought for …" footer can show the turn's real token total. Reset
	// at turn start alongside turnTokens.
	turnUsage adapter.Usage

	// Per-turn meta surfaced in the thinking-row footer instead of as
	// scrollback lines. iterRound/iterMax come from agent.IterationStart;
	// toolsRequested/toolsStarted track how many tools the model just
	// asked for and how many have fired so far this iteration. Reset
	// at the start of every new turn / iteration. The field is
	// `iterRound` (not `iterStep`) so the in-code vocabulary matches
	// the on-screen "round N/M" segment.
	iterRound      int
	iterMax        int
	toolsRequested int
	toolsStarted   int

	// In-flight tool-call buffer. The unified tool card is rendered on
	// ToolResult — until then we hold the start metadata so the card
	// header can carry the original preview + duration. Cleared on
	// every ToolResult. pendingToolStart is stamped on ToolStart so the
	// card header can show how long a slow call took (see slowDurationTag).
	pendingToolName    string
	pendingToolPreview string
	pendingToolArgs    string
	pendingToolStart   time.Time

	// Input history (Up/Down when palette is closed)
	inputHistory    []string
	inputHistoryIdx int
	inputDraft      string

	// killRing holds the text most recently cut with Ctrl+U so Ctrl+Y can
	// yank it back at the cursor. Single-slot (no stack) — matches the
	// readline default users expect from a terminal cmdline. Bounded to
	// the current logical row, mirroring bubbles' Ctrl+U semantics; an
	// empty kill (cursor already at column 0) leaves the ring untouched.
	killRing string

	// subagentTasks is the session-scoped Registry for tracking
	// foreground + background subagent runs. /subagents reads from it;
	// the AgentTool writes into it. Nil when subagents aren't wired
	// (tests).
	subagentTasks *subagents.Registry

	// dockFocused + dockCursor drive keyboard navigation of the live
	// subagent dock: Tab (when no palette is open and subagents are
	// running) focuses it, up/down move dockCursor among the running
	// subagents, Enter opens the selected one's transcript, Esc exits.
	// No mouse capture — keyboard-only keeps terminal text selection
	// intact.
	dockFocused bool
	dockCursor  int

	// subagentTool is the live AgentTool registered on the loop's
	// registry. The TUI uses it to wire the background-done callback
	// and to introspect the available agent configs from /subagents.
	subagentTool *agent.AgentTool

	// experimentalEnabled lists the experimental feature names switched on
	// this session (sorted). The /experimental overlay renders the full
	// catalog and marks these on.
	experimentalEnabled []string

	// mcpManager owns the lifecycle of every configured MCP client.
	// Nil when the session has no MCP servers configured. The /mcp
	// slash command reads from it; run.go invokes Stop on shutdown.
	mcpManager *mcppkg.Manager

	// customSlash carries the user-authored slash commands loaded
	// from ~/.yottacode/commands/ and <cwd>/.yottacode/commands/. The
	// dispatcher (m.findSlash) walks built-ins first, then this list.
	// /help groups them under a "Custom commands:" header so it's
	// obvious which entries come from user files.
	customSlash []slashCommand

	// skillSlash carries one slashCommand per loaded skill that opted
	// in (metadata.slash != "false"). Walked after built-ins and
	// customSlash in m.findSlash so user-authored commands always
	// shadow skills on name collision. /help groups them under their
	// own "Skills:" header.
	skillSlash []slashCommand

	// skills is the raw list of loaded skills (built-in + user +
	// project) shown to the user via /help. The Skill tool registered
	// on cfg.Registry holds the authoritative copy plus the per-
	// session enabled set; skillTool below is the typed handle.
	skills []skills.Skill

	// skillTool is the live SkillTool registered on cfg.Registry.
	// /skills mutates its enablement map; reloadMemoryNow reads
	// .Active() to recompose the system prompt's skills section.
	// Nil when no skills are configured.
	skillTool *agent.SkillTool

	// skillsPickerOpen / skillsPicker drive the /skills inline overlay.
	// Mirrors subagentsPicker: multi-select via spacebar toggles a
	// skill's per-session enablement; Enter views the body in $PAGER;
	// `c` (or Enter on the bottom row) commits and recomposes the
	// system prompt; Esc cancels without changes.
	skillsPickerOpen bool
	skillsPicker     *skillsPickerState

	// skillsMenuOpen / skillsMenu drive the top-level /skills overlay.
	// /skills now opens a menu (Catalog / Install / Check / Update);
	// the picker is one of the menu's options (Catalog). The two
	// overlays are mutually exclusive — picking Catalog closes the
	// menu before opening the picker, so only one inline overlay is
	// ever active at a time.
	skillsMenuOpen bool
	skillsMenu     *skillsMenuState

	// subagentInbox is a long-lived channel the AgentTool pushes
	// SubagentBackgroundDone events onto from detached goroutines
	// when a background subagent finishes after the parent turn has
	// ended. The Model drains this in parallel with the per-turn
	// eventsCh via waitForSubagentInbox so notifications surface
	// without waiting for the next user input. Buffer is generous
	// (32) so a flurry of completions can't block the goroutines.
	subagentInbox chan agent.SubagentBackgroundDone

	// pendingSubagentWakes queues background completions whose spawner set
	// notify_on_done while a turn is in flight (a wake can't interrupt a
	// live turn). The turnEndedMsg drain starts a single wake turn from the
	// whole queue once the in-flight turn finishes; an idle completion
	// wakes the model immediately instead of queueing.
	pendingSubagentWakes []agent.SubagentBackgroundDone

	// banneredSubagentDone records every background task id we've already
	// painted a completion banner for, so the self-healing reconciliation
	// (reconcileSubagentCompletions) can detect terminal registry tasks
	// whose SubagentBackgroundDone event was dropped on a full inbox and
	// surface them rather than losing the completion (and any wake) silently.
	banneredSubagentDone map[string]bool

	// Persistent session
	sess *session.Session

	// histMu serializes access to sess.Messages between the agent Turn
	// goroutine (which appends during a turn) and this bubbletea Update
	// goroutine (live token estimate, /context, system-prompt edits).
	// Handed to the loop as cfg.HistoryLock and held around this model's
	// own Messages reads/writes. A pointer so value-copies of Model share
	// one lock (a sync.Mutex value field would trip copylocks). Allocated
	// in New.
	histMu *sync.Mutex

	// recalledCount is how many prior conversations auto-recall injected into
	// the current turn's system prompt. Written by the turn goroutine during
	// the per-turn prompt rebuild, read by this Update goroutine's thinking-row
	// render — so it's an atomic behind a pointer (shared across value-copies
	// of Model, like histMu; a bare atomic value field would trip copylocks).
	// Reset each rebuild; 0 means no "recalled N" segment is shown. Allocated
	// in New.
	recalledCount *atomic.Int32

	// UI components
	textInput textarea.Model
	spinner   spinner.Model
	md        *markdownRenderer
	width     int
	height    int
	ready     bool

	// transcript is an append-only record of every line we've emitted to the
	// terminal. View() does NOT render it (lines live in terminal scrollback);
	// kept solely so tests can assert "this line was emitted." It also backs
	// /export as a quick fallback if needed.
	transcript    *strings.Builder
	streaming     *strings.Builder
	streamingMode streamMode

	// reasoning buffers chain-of-thought tokens from EventReasoning events
	// while a turn is in flight. Rendered live above the thinking row in
	// faint italic so the user can watch the model think; cleared when
	// content starts streaming (model has finished reasoning), when a tool
	// call fires, or when the turn ends. Never goes to scrollback — it's
	// strictly an in-flight surface, like the streaming preview but for
	// reasoning summaries (Responses API) and reasoning_content (xAI Grok
	// + Ollama thinking models).
	reasoning *strings.Builder

	// Code-block streaming state. Prose streams line-by-line into
	// scrollback (preserves scrolling); code blocks buffer until the
	// closing ``` fence, then land in scrollback fully syntax-highlighted
	// in one shot. Live footer shows a "…writing code" notice while
	// inCodeBlock is true.
	codeBlockBuf  *strings.Builder
	codeBlockLang string
	inCodeBlock   bool

	// Table streaming state. Markdown pipe tables buffer until the
	// first non-table line, then render through glamour in one shot.
	tableBuf *strings.Builder
	inTable  bool

	// livePlan is the in-flight todo snapshot rendered as a live card
	// in View() (next to/below the spinner). Updated in place on every
	// agent.TodoUpdate; nil/empty means "no plan to show" (never set,
	// or cleared via an empty todo_write call). Persists across turns
	// so the user can glance at the active plan between turns —
	// matches Claude Code's TodoWrite surface. The per-call ToolResult
	// scrollback card for todo_write is suppressed (see handleAgentEvent)
	// because the live card already shows the same content; without
	// the suppression every todo_write would stack a duplicate card in
	// scrollback alongside the in-place live render.
	livePlan []agent.Todo

	// livePlanTouched flips true the first time todo_write fires in
	// the current turn. Drives the end-of-turn snapshot: at TurnDone
	// we commit a single full-card snapshot to scrollback IFF the plan
	// was touched, then reset the flag. A turn that never touches the
	// plan emits no snapshot, so the live card stays unchanged in the
	// View and scrollback stays clean. Reset in TurnInterrupted too so
	// a cancelled turn doesn't leave the flag armed for the next one.
	livePlanTouched bool

	// pendingCmds holds tea.Println commands queued during one Update tick.
	// Flushed at the tail of Update via the wrapper below. Kept as a slice on
	// a pointer-backed field (strings.Builder above is *strings.Builder for
	// the same reason) so value-receiver Update copies see consistent state.
	pendingCmds []tea.Cmd

	// historyLines records every conversation line emitted via appendLine —
	// user blocks, assistant replies, tool calls, errors, footers — in the
	// order they were emitted. On terminal resize the live frame's
	// tea.ClearScreen wipes the visible viewport; replaying historyLines
	// restores the conversation under a freshly-rendered startup box.
	// Startup chrome (box, hint, welcome) is NOT in this slice — those get
	// regenerated at the new width on every resize.
	historyLines []string

	// pendingStartupNotices holds notice strings set by run.go before the
	// program starts (e.g. "embedding model missing"). Emitted via
	// appendLine inside the first WindowSizeMsg handler, AFTER the startup
	// box has been queued — calling appendLine from run.go would queue
	// them before the box and the message would land above it.
	pendingStartupNotices []string

	// pastes maps short placeholder markers to the full content of large
	// pastes. When the user pastes more than pasteThreshold bytes, we
	// insert a `[Pasted text #N: lines, bytes]` token into the input box
	// instead of the full body — keeps the cmdline from stretching to
	// fill the screen. On submit, expandPastes() swaps the markers back
	// for their original content before the message hits the agent.
	pastes   map[string]string
	pasteSeq int

	// pastedImages maps image markers to their loaded ImageBlock data.
	// When the user pastes a path to an image file, handleLargePaste
	// detects the extension and reads the file. On submit,
	// collectPastedImages() returns the blocks to attach to the user
	// message.
	pastedImages map[string]adapter.ImageBlock

	turnActive bool
	turnCancel context.CancelFunc
	eventsCh   chan agent.Event
	turnErrCh  chan error
	decisions  chan agent.Decision
	// memoryNudgePending arms the pre-compaction memory reminder: set
	// when the context watermark first crosses the warn threshold
	// (updateContextUsage), consumed by the next startTurnWithDisplay,
	// which appends preCompactionMemoryReminder to the outgoing user
	// message (history only — the transcript shows the user's own
	// text). Cleared when usage drops back below the warn threshold,
	// alongside the lastWatermarkPct reset.
	memoryNudgePending bool
	// exitSavePending marks the in-flight final memory turn started by
	// maybeStartExitSaveTurn. When the turn ends (or is cancelled), the
	// turnEndedMsg handler completes the quit instead of idling.
	exitSavePending bool
	// userTurnsThisLaunch counts turns started since THIS process
	// launched — unlike firstMessageSent it ignores user turns carried
	// in by --resume, so the exit-save activity bar
	// (exitSaveMinUserTurns) measures fresh interaction only.
	userTurnsThisLaunch int
	// pendingInputAfterTurn captures a user message that couldn't be
	// delivered mid-turn (model finished without a tool round, or the
	// userMsgCh buffer was full and we fell back to cancel+resubmit).
	// The turnEndedMsg handler picks it up and starts a fresh turn.
	pendingInputAfterTurn string

	// userMsgCh feeds mid-turn user messages into the agent loop's
	// UserMessages channel. Created per-turn in startTurn; the loop
	// checks it (non-blocking) between tool rounds and appends the
	// message to history without cancelling. Buffer of 1; overflow
	// falls back to the old cancel+resubmit path.
	userMsgCh chan string

	// loops holds every active /loop command in this TUI process. Loops are
	// local/in-memory, keyed by a user-visible ID, and mutated only from the
	// Update goroutine so they need no mutex. loopOrder preserves stable status
	// and banner ordering.
	loops     map[string]loopState
	loopOrder []string

	// currentLoopTurnID names the loop whose prose iteration owns the active
	// turn (empty for user turns and slash loops). Set when fireLoopIteration
	// starts the turn and read at turnEndedMsg so a loop_control{stop} from the
	// agent disarms the right loop. See consumeLoopControl.
	currentLoopTurnID string

	// loopExitConfirmOpen shows a graceful-exit warning when local loops would
	// stop on quit. The cursor picks Exit anyway vs Stay.
	loopExitConfirmOpen   bool
	loopExitConfirmCursor int

	// loopListOpen shows the active-loop panel (bare `/loop`) as a dismissable
	// inline overlay below the cmdline, instead of writing loop cards into the
	// session transcript. Any key closes it.
	loopListOpen bool

	// paragraphStart tracks blank-line boundaries so the first line
	// of each new prose paragraph gets 2 extra spaces of indent.
	paragraphStart bool

	// Approval modal state
	awaitingApproval bool
	approvalTool     string
	approvalPreview  string
	approvalArgs     string
	// approvalAllowAlwaysOK gates the [a]lways-allow keypress. Set
	// true when DeriveAllowRule can produce a sensible pattern from
	// this call; false for compound shell commands and other shapes
	// where a one-click blanket grant would be a footgun. Recomputed
	// each time a new ApprovalNeeded event lands.
	approvalAllowAlwaysOK bool
	// approvalDerivedRule is the pattern the modal will save when the
	// user picks [a]. Shown in the modal so the user knows what
	// they're committing to.
	approvalDerivedRule string
	// approvalDenyAlwaysOK gates the [d] never/block keypress; set true
	// whenever DeriveDenyRule yields a rule for the pending call (broader
	// than approvalAllowAlwaysOK — offered for dangerous/compound commands
	// too). approvalDerivedDenyRule is the rule shown and persisted.
	approvalDenyAlwaysOK    bool
	approvalDerivedDenyRule string

	// Inline path-trust elevation modal state (Prompt 2 in
	// yottacode-roadmap/folder-trust.md). When awaitingPathTrust is
	// true, the regular approvalTool flow is bypassed: the user is
	// picking [1] Allow once / [2] Trust for session / [3] Reject for
	// a write target outside cwd. pathTrustReq holds the elevation
	// event so the renderer and keypress handler can stay narrow.
	awaitingPathTrust bool
	pathTrustReq      agent.PathTrustElevationNeeded

	// Cheatsheet overlay
	cheatsheetOpen bool

	// Usage overlay (/usage). Read-only panel rendered below the
	// cmdline via renderInlineOverlay rather than appended to chat
	// scrollback — token tallies are transient inspection, not part
	// of the conversation the model should re-read. usagePanel holds
	// the body rendered once at open time (so the per-frame redraw
	// doesn't re-fire the openai-auth backend probe); any key closes.
	usageOpen  bool
	usagePanel string
	// Context report overlay (/context). Renders the context-window
	// breakdown on the inline-overlay surface (below the cmdline +
	// status bar) instead of in chat history, so the report — which is
	// transient inspection, not conversation — stays out of scrollback,
	// the transcript, and resume replay. Body is snapshotted at open
	// time (the report reads memory files from disk and walks the skill
	// set, too heavy to recompute every frame); any key dismisses it,
	// mirroring the cheatsheet.
	contextReportOpen bool
	contextReportBody string

	// Permissions picker (/permissions). Two-row picker (shared /
	// local) modelled on /memory's three-row picker — Up/Down
	// navigates, Enter suspends to vim on the chosen rule file, Esc
	// closes. Rendered below the cmdline via renderInlineOverlay so
	// the paths sit right next to the input the user is about to type
	// the next command into. The store re-reads both files on the
	// next tool call, so there's no explicit reload step.
	permissionsOpen   bool
	permissionsCursor int

	// Model-picker overlay (/model). pickerList is the model-list
	// source; nil in production (defaults to catalog.List), tests
	// inject a fake to exercise the picker without a network round-
	// trip. Curated providers (anthropic/openai/gemini/xai) read from
	// the embedded catalog instantly; openai-compatible / ollama
	// providers fetch live each open.
	pickerList      pickerListFn
	modelPickerOpen bool
	modelPicker     *modelPickerState

	// Theme-picker overlay (/themes). Two-pane layout: left holds
	// the theme list, right shows a live showcase rendered with
	// the highlighted palette. Cursor moves trigger ApplyTheme on
	// the highlighted entry so the entire TUI repaints with the
	// candidate look. Esc reverts to the theme that was active at
	// open time; Enter commits the live-applied theme to
	// config.toml.
	themePickerOpen bool
	themePicker     *themePickerState

	// Reasoning-effort overlay (/effort). Single-column picker over
	// default/low/medium/high; Enter commits by rebuilding the active
	// adapter with the new effort, Esc closes without change.
	effortPickerOpen bool
	effortPicker     *effortPickerState

	// Provider sub-menu overlay (/provider). Layered state machine:
	// menu → action sub-pickers (Use, Remove, Add). M6 wires List+Use;
	// Remove and Add land in M7+M8.
	providerPickerOpen bool
	providerPicker     *providerPickerState

	// Memory picker overlay (/memory). Three-row picker (Project /
	// User / auto-folder); subcommands have been retired. Lifetime is
	// one /memory invocation; closed on Esc or after Enter dispatches.
	memoryPickerOpen bool
	memoryPicker     *memoryPickerState

	// Recall picker overlay (/recall <query>). Search results are transient
	// navigation context, rendered below the cmdline instead of being appended
	// to the session transcript. Enter resumes the selected session.
	recallPickerOpen bool
	recallPicker     *recallPickerState

	// Embed setup overlay — opened from /memory "Enable semantic search".
	embedSetupOpen    bool
	embedSetupCursor  int
	embedSetupPulling bool

	// Sessions picker overlay (/sessions). Layered state machine:
	// menu (Resume/Rename/Export) → list-of-recent → optional
	// textinput sub-mode for rename + export path. Lifetime is one
	// /sessions invocation; closed on Esc from the menu or after a
	// successful action commits.
	sessionsPickerOpen bool
	sessionsPicker     *sessionsPickerState

	// Plans picker overlay (/plan list). Single-row picker over
	// ~/.yottacode/plans/ ordered newest-first; Enter resumes the
	// chosen plan (attaches its file and enters plan mode if
	// inactive). Esc closes without changes.
	plansPickerOpen bool
	plansPicker     *plansPickerState

	// Checkpoints picker overlay (/checkpoints + Esc Esc). Two-screen
	// state machine: first the prompt list (one row per past user
	// message), then a four-action menu (restore code+conv / conv only
	// / code only / summarize from here). Lifetime is one invocation;
	// closed on Esc from the prompt list or after an action commits.
	checkpointsPickerOpen bool
	checkpointsPicker     *checkpointsPickerState

	// lastEscAt timestamps the last bare Esc keypress so a second Esc
	// within escChordWindow opens the checkpoints picker. Reset when
	// the chord fires, or when any non-Esc key handler runs.
	lastEscAt time.Time

	// Subagents picker overlay (/subagents with no args). Lists the
	// session's subagent tasks newest-first; Enter opens the chosen
	// task's transcript in $PAGER; `s` stops a running task; `r`
	// refreshes the snapshot; Esc closes. Cleans up scrollback by
	// keeping the table out of session history (vs. the inline-list
	// rendering used by /subagents list).
	subagentsPickerOpen bool
	subagentsPicker     *subagentsPickerState

	// routerPickerOpen/routerPicker drive the /router overlay: a menu for
	// toggling cache-safe task routing and selecting fast/smart models.
	routerPickerOpen bool
	routerPicker     *routerPickerState

	mcpPickerOpen bool
	mcpPicker     *mcpPickerState

	// Connection probe state for the status footer dot
	connection connState

	// Slash command palette state
	paletteOpen     bool
	paletteFiltered []slashCommand
	paletteIndex    int
	paletteOffset   int // first visible row when filtered list overflows the window

	// File palette state — opens when the user types `@` (at start-of-
	// word) so they can pick a file from cwd by tab-completion instead
	// of typing the path by hand. The palette is mutually exclusive
	// with the slash palette: `/` opens the slash palette, `@` opens
	// the file palette, and the textarea-update path closes the other
	// when it detects the active prefix.
	//
	// filePaletteEntries caches the cwd walk; it's populated lazily
	// the first time @ opens the palette and re-walked on /clear (or
	// any explicit invalidation). filePaletteFiltered holds the
	// currently-visible subset for the in-flight query.
	filePaletteOpen     bool
	filePaletteEntries  []fileEntry
	filePaletteFiltered []fileEntry
	filePaletteIndex    int
	filePaletteOffset   int // first visible row when filtered list overflows the window
	filePaletteQuery    string
	filePaletteAt       int // byte index of the active `@` in textarea value

	// startupPrinted gates the one-shot scrollback emission of the startup
	// box and welcome panel. Init() can't directly tea.Println because Init
	// runs before the first WindowSizeMsg, so we defer the print to the
	// first Update tick — gated on this flag.
	startupPrinted bool

	// firstMessageSent flips true the first time the user submits a turn
	// in this launch. Drives whether the dim onboarding hint
	// (`/ commands · @ files · ↑↓ history`) renders inlined on the
	// placeholder row. The hint is per-launch chrome — once the user
	// has actually used the app, it disappears for the rest of the
	// process and doesn't come back on subsequent prompts. Initialized
	// true when the loaded session already contains a user turn so
	// resumed sessions skip the onboarding chrome.
	firstMessageSent bool

	// cursorVisible drives the blink phase of the manually-rendered
	// cmdline cursor (renderInputBody / insertCursor). Toggled by a
	// cursorBlinkMsg tick at ~530ms (standard cursor cadence). Layout
	// stays stable across phases — the cursor cell is always 1 column
	// wide; visibility just swaps reverse-video on for the block.
	cursorVisible bool

	// openAIAuthPending tracks an in-flight inline OAuth login from
	// /provider add openai-auth. Non-nil between the URL-ready and
	// done messages — the program holds it so a Ctrl-C path can
	// release the loopback listener cleanly.
	openAIAuthPending *openaiauth.PendingLogin

	// openAIAuthPendingAdd holds the not-yet-persisted profile for an
	// in-flight OAuth login. The provider is intentionally NOT written
	// to config.toml until the OAuth callback returns success — without
	// this deferral, a cancelled or failed sign-in leaves a broken
	// profile on disk that the next chat turn 401s against. Dropped on
	// either success-after-persist or any failure branch.
	openAIAuthPendingAdd *pendingOpenAIAuthAdd
	copilotPendingAdd    *pendingCopilotAdd
}

// pendingOpenAIAuthAdd carries everything persistProviderAdd would
// have written, deferred to the OAuth-success callback. The persist
// path is reconstructed from these fields exactly the way the picker
// and the slash command path would have run it themselves.
type pendingOpenAIAuthAdd struct {
	add           providerops.AddProvider
	becomesActive bool
	// fromPicker tracks whether the close-the-overlay step needs to
	// run on success. The slash-command path doesn't open a picker so
	// it has nothing to close.
	fromPicker bool
}

type streamMode int

const (
	streamIdle streamMode = iota
	streamReasoning
	streamContent
)

// textareaSyncMsg is a no-op message used to force bubbles/textarea to
// recalculate its internal viewport after we resize or append wrapped input.
type textareaSyncMsg struct{}

type providerProbeMsg struct {
	result   adapter.ProbeResult
	announce bool
}

type prStatusMsg struct {
	number int
}

// cursorBlinkMsg ticks the manually-rendered cmdline cursor on/off at
// the standard 530ms cadence. We can't piggyback on bubbles' textarea
// blink because the input is rendered by hand (renderInputBody) — the
// textarea owns value + cursor position but not the painted glyphs.
// Re-armed by Update on each receipt; a single perpetual loop drives
// it for the lifetime of the process.
type cursorBlinkMsg struct{}

// cursorBlinkInterval is the half-cycle between visible→invisible
// transitions. 530ms matches the long-standing default in xterm and
// most terminal emulators; faster reads as agitated, slower reads as
// stuck.
const cursorBlinkInterval = 530 * time.Millisecond

// cursorBlinkCmd schedules the next blink toggle. Kept as a free
// function so Init and update() can both reach for it.
func cursorBlinkCmd() tea.Cmd {
	return tea.Tick(cursorBlinkInterval, func(time.Time) tea.Msg {
		return cursorBlinkMsg{}
	})
}

// loopState drives the /loop recurring command. Each iteration
// re-dispatches payload on a tea.Tick heartbeat. All fields are touched only
// from the Update goroutine.
type loopState struct {
	id        string        // user-visible loop ID
	active    bool          // a repeat is armed
	payload   string        // prose prompt OR a "/cmd args" line
	isSlash   bool          // route payload through runSlash, not startTurnWithDisplay
	interval  time.Duration // required repeat cadence
	remaining int           // >0 bounded (Nx), decremented each fire; -1 unbounded
	total     int           // original bounded count (0 for unbounded); drives K/N progress
	armedAt   time.Time     // when this loop was created
	expiresAt time.Time     // default five-day expiry for local loops
}

// loopTickMsg is the /loop heartbeat for one interval loop. A pending tick is
// invalidated by removing the loop from m.loops: the handler drops any tick
// whose id is no longer an active loop (stopped, expired, or reset).
type loopTickMsg struct {
	id string
}

// loopMinInterval is the sanity floor for /loop intervals: faster than
// this hammers the provider and reads as a runaway.
const loopMinInterval = 5 * time.Second

// loopTickCmd schedules the next /loop heartbeat for a loop id.
func loopTickCmd(d time.Duration, id string) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return loopTickMsg{id: id} })
}

// New builds a Model wired with the given config.
func New(parent context.Context, c Config) Model {
	// Defensive: tests build Config without FileCfg, leaving the
	// thresholds at 0.0 — which would mean every status redraw colors
	// the token counter as if we were over budget. Detect the zero
	// value and substitute documented defaults so all callers see a
	// usable config without each having to remember to populate it.
	// Hoisted above the textarea/spinner construction so ApplyTheme
	// can read FileCfg.Theme.Name before those components capture
	// their style values.
	if c.FileCfg.Context.DefaultWindow == 0 {
		c.FileCfg = config.Default()
	}

	// Apply the configured theme BEFORE building any sub-component
	// — textarea, spinner, and any palette-derived defaults capture
	// styles by value. An unknown name (stale config from a removed
	// theme) silently falls back to the default; config.Validate
	// already rejects unknown names at load time, so reaching this
	// branch means the user hand-edited config.toml after parse.
	ApplyTheme(c.FileCfg.Theme.Name)

	ti := textarea.New()
	// Placeholder is dim italic and short — the four-hint preamble used
	// to live here, but a crowded empty state buries the actual signal
	// (an empty input ready to type into). The onboarding hints have
	// migrated to a separate footer line that disappears after the
	// first message — see renderInputHintFooter.
	ti.Placeholder = "ask anything…"
	ti.CharLimit = 0
	// Render the chevron only on the first display row; soft-wrapped and
	// hard-newline continuations get a 2-col indent so the input reads as
	// one block instead of "❯" repeating on every wrapped line.
	ti.SetPromptFunc(2, func(displayLine int) string {
		if displayLine == 0 {
			return "❯ "
		}
		return "  "
	})
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	// Brand-colored bold prompt — the focal point of the input area.
	ti.FocusedStyle.Prompt = styleInputPrompt
	ti.BlurredStyle.Prompt = styleInputPrompt
	// Bubbles' textarea defaults to a dark background on the line under
	// the cursor (Background "0" on dark terminals = solid black). That
	// reads as "your typing got highlighted" which is jarring on a
	// transparent terminal. Override to no background.
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ti.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	ti.Focus()

	sp := spinner.New()
	// spinner.Dot is the stock single-cell Braille rotation
	// (`⣾⣽⣻⢿⡿⣟⣯⣷`) — calmer, narrower than the previous custom
	// 4×4 grid. docs/TUI.md recommends Dot / Pulse / Points as the
	// professional defaults Charm projects reach for.
	sp.Spinner = spinner.Dot
	sp.Style = styleSpinner

	// A loaded session may already contain user turns from a prior
	// launch (--resume). Skip the onboarding hint footer in that case
	// since the user is past the moment it would help.
	firstMessageSent := false
	if c.Session != nil {
		for _, msg := range c.Session.Messages {
			if msg.Role == adapter.RoleUser {
				firstMessageSent = true
				break
			}
		}
	}

	// Seed the context-usage counter from the loaded session so a
	// resumed session shows its true %, not 0%, on the very first
	// frame. Without this the status bar reads `ctx 0%` until the
	// next IterationStart fires — and a resumed session might not
	// run another turn for minutes (or ever, if the user is just
	// reading scrollback).
	contextTokensInit := 0
	if c.Session != nil {
		contextTokensInit = contextwindow.EstimateTokens(c.Session.Messages) + registrySchemaTokens(c.Cfg.Registry)
	}

	// Seed livePlan from the resumed session so the live plan card
	// in View() reappears immediately on resume — the agent's
	// PlanStore is already restored from sess.Todos in run.go, but
	// the TUI keeps its own copy for the live-area render so resize
	// and rendering don't need to reach across into agent state.
	// livePlanTouched stays false: a resumed-but-untouched plan
	// should not commit a redundant snapshot on the first TurnDone.
	var livePlanInit []agent.Todo
	if c.Session != nil && len(c.Session.Todos) > 0 {
		livePlanInit = append([]agent.Todo(nil), c.Session.Todos...)
	}

	return Model{
		parentCtx:              parent,
		cfg:                    c.Cfg,
		modelName:              c.ModelName,
		baseURL:                c.BaseURL,
		apiKey:                 c.APIKey,
		provider:               c.Provider,
		providerLabel:          c.ProviderLabel,
		reasoningEffort:        c.ReasoningEffort,
		enableWebSearch:        c.EnableWebSearch,
		disableWebSearch:       c.DisableWebSearch,
		enableXSearch:          c.EnableXSearch,
		enableCodeInterpreter:  c.EnableCodeInterpreter,
		searchAllowedDomains:   c.SearchAllowedDomains,
		searchExcludedDomains:  c.SearchExcludedDomains,
		xSearchAllowedHandles:  c.XSearchAllowedHandles,
		xSearchExcludedHandles: c.XSearchExcludedHandles,
		xSearchFromDate:        c.XSearchFromDate,
		xSearchToDate:          c.XSearchToDate,
		providerProfile:        c.ProviderProfile,
		cwd:                    c.Cwd,
		perms:                  c.Permissions,
		recall:                 c.Recall,
		version:                c.Version,
		commit:                 c.Commit,
		dirty:                  c.Dirty,
		branch:                 c.Branch,
		projectRoots:           c.ProjectRoots,
		sensitiveProject:       c.SensitiveProject,
		sensitiveRoots:         c.SensitiveRoots,
		worktree:               c.Worktree,
		memorySummary:          c.MemorySummary,
		baseSystemPrompt:       c.BaseSystemPrompt,
		embedClient:            c.EmbedClient,
		summarizerAdapter:      summarizerOrDefault(c.SummarizerAdapter, c.Cfg.Adapter),
		summarizerModel:        c.SummarizerModel,
		router:                 c.RouterAdapters,
		routerMode:             routerModeOrOff(c.RouterMode),
		opts:                   c.Options,
		fileCfg:                c.FileCfg,
		subagentTasks:          c.Subagents,
		subagentTool:           c.AgentTool,
		experimentalEnabled:    c.ExperimentalEnabled,
		mcpManager:             c.MCPManager,
		subagentInbox:          make(chan agent.SubagentBackgroundDone, 32),
		banneredSubagentDone:   map[string]bool{},
		customSlash:            buildCustomSlash(c.CustomCommands),
		skillSlash:             buildSkillSlash(c.Skills),
		skills:                 c.Skills,
		skillTool:              c.SkillTool,
		sess:                   c.Session,
		histMu:                 &sync.Mutex{},
		recalledCount:          &atomic.Int32{},
		textInput:              ti,
		spinner:                sp,
		md:                     newMarkdownRenderer(80),
		inputHistoryIdx:        -1,
		transcript:             &strings.Builder{},
		streaming:              &strings.Builder{},
		reasoning:              &strings.Builder{},
		codeBlockBuf:           &strings.Builder{},
		tableBuf:               &strings.Builder{},
		livePlan:               livePlanInit,
		firstMessageSent:       firstMessageSent,
		contextTokens:          contextTokensInit,
		// Cursor starts visible — the first blink tick (530ms after
		// Init) flips it off, giving an unambiguous "yes the input is
		// focused" cue on the very first frame.
		cursorVisible: true,
	}
}

func (m *Model) clearPendingDecisionUI() {
	m.awaitingApproval = false
	m.approvalTool = ""
	m.approvalPreview = ""
	m.approvalArgs = ""
	m.approvalAllowAlwaysOK = false
	m.approvalDerivedRule = ""
	m.approvalDenyAlwaysOK = false
	m.approvalDerivedDenyRule = ""
	m.awaitingPathTrust = false
	m.pathTrustReq = agent.PathTrustElevationNeeded{}
}

func (m *Model) sendDecision(d agent.Decision) bool {
	if m.decisions == nil {
		m.clearPendingDecisionUI()
		m.appendLine(styleError.Render("✗ turn already ended; decision ignored"))
		return false
	}
	select {
	case m.decisions <- d:
		return true
	default:
		m.clearPendingDecisionUI()
		m.appendLine(styleError.Render("✗ turn already ended; decision ignored"))
		return false
	}
}

func (m *Model) finishDecisionUI() tea.Cmd {
	m.clearPendingDecisionUI()
	if m.turnActive {
		return waitForEvent(m.eventsCh, m.turnErrCh)
	}
	return nil
}

func (m Model) Init() tea.Cmd {
	// Deliberately do NOT start m.spinner.Tick here. The spinner's
	// thinking indicator is only visible during a turn (renderThinkingRow
	// returns "" when !m.turnActive), so a perpetual 10Hz tick when idle
	// just churns redraws of the live footer. Each redraw emits cursor
	// and line-clear ANSI which can invalidate active mouse selections
	// in scrollback on terminals/tmux. startTurn re-arms the tick.
	cmds := []tea.Cmd{
		textarea.Blink,
		cursorBlinkCmd(),
		runProviderProbe(m.parentCtx, m.adapterConfig(m.modelName, m.baseURL), false),
		// Drain the long-lived subagent inbox for the life of the
		// program. A nil channel here is a programming error (New
		// always allocates it), so we don't guard.
		waitForSubagentInbox(m.subagentInbox),
		startMCPServers(m.parentCtx, m.mcpManager),
		resolveCurrentPRCmd(m.parentCtx, m.githubClient, m.cwd),
		// Warm the local models.dev copy in the background (TTL-gated: a
		// no-op when the on-disk cache is fresh, a daily refresh otherwise),
		// so window lookups for hosted providers (NVIDIA NIM, …) resolve
		// instantly without waiting on the ~2MB fetch.
		func() tea.Msg { catalog.WarmModelsDev(); return nil },
	}
	// Auto-discover the active model's context window when it's unknown
	// (windowless OpenAI-compatible endpoint, no override). Background +
	// at-most-once — so the user never has to run `model fetch` for the
	// bar/threshold to size correctly.
	if c := m.maybeProbeActiveModelWindowCmd(); c != nil {
		cmds = append(cmds, c)
	}
	// A session resumed at startup can already exceed the auto
	// threshold; check once now (handled as resumeWatermarkCheckMsg,
	// since Init's receiver is a copy and can't mutate watermark
	// state) so it heals before the first send. Fresh sessions carry
	// at most the system prompt and skip the no-op.
	if len(m.sess.Messages) > 1 {
		cmds = append(cmds, func() tea.Msg { return resumeWatermarkCheckMsg{} })
	}
	return tea.Batch(cmds...)
}

// Update is the public Bubbletea entry point. It delegates to update() then
// flushes any tea.Println commands appendLine queued during the tick. Single
// chokepoint keeps the rest of the model from threading Cmds by hand.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	out, cmd := m.update(msg)
	mm := out.(Model)
	// Inline-mode re-anchor guard. A full-screen overlay (e.g. /context,
	// the /skills menu) renders taller than the bare footer; in inline mode
	// Bubbletea doesn't re-anchor a shrinking live frame to the terminal
	// bottom, so closing one leaves the footer (input box + status bar)
	// stranded mid-screen until something redraws scrollback. Overlays that
	// emit a line on close — model/provider selection's `appendLine` — get
	// re-anchored for free by that tea.Println; the quiet closes (Esc / any
	// key) don't. So when a key closes an overlay and nothing else queued a
	// scrollback redraw this tick, force the clean redraw ourselves. Only
	// key input closes overlays, so other message types skip the check.
	if _, isKey := msg.(tea.KeyMsg); isKey && m.overlayClosed(mm) && len(mm.pendingCmds) == 0 {
		mm.repaintViewport()
	}
	if flush := mm.flushPending(); flush != nil {
		if cmd == nil {
			return mm, flush
		}
		// Sequence: scrollback prints first, then any other Cmd (event pump,
		// quit, ping, etc.) — keeps line ordering deterministic.
		return mm, tea.Sequence(flush, cmd)
	}
	return mm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(spinner.TickMsg); ok {
		if !m.turnActive && !m.summarizing && !m.hasRunningSubagents() && !m.skillsBusy() {
			// Idle: drop the tick so we stop re-scheduling ourselves.
			// startTurn re-arms m.spinner.Tick when work begins; the
			// summarize commands re-arm it for the summarizing row.
			// We also keep ticking while background subagents run so the
			// live dock's elapsed times + latest activity refresh after
			// the parent turn has ended (a background dispatch detaches
			// its workers; nothing else would drive a redraw until they
			// complete).
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.skillsPickerOpen && m.skillsPicker != nil && m.skillsPicker.busy != "" {
			m.skillsPicker.busyFrame = m.spinner.View()
		}
		if m.skillsMenuOpen && m.skillsMenu != nil && m.skillsMenu.busy != "" {
			m.skillsMenu.busyFrame = m.spinner.View()
		}
		return m, cmd
	}
	if _, ok := msg.(cursorBlinkMsg); ok {
		// Toggle and re-arm. Unlike the spinner tick, the cursor
		// blink runs perpetually — there's no "idle" state where we
		// hide the input row, so the tick has work to do every
		// cycle. The 530ms cadence keeps redraw churn low compared
		// to the spinner's 100ms.
		m.cursorVisible = !m.cursorVisible
		return m, cursorBlinkCmd()
	}
	if probe, ok := msg.(providerProbeMsg); ok {
		m.connection = probeConnectionState(probe.result)
		if probe.result.Profile.Provider != "" {
			m.providerProfile = probe.result.Profile
		}
		if probe.announce {
			m.appendLine(formatProbeResult(probe.result))
		}
		return m, nil
	}
	if pr, ok := msg.(prStatusMsg); ok {
		m.currentPR = pr.number
		return m, nil
	}
	if done, ok := msg.(mcpStartupDoneMsg); ok {
		m.handleMCPStartupDone(done.results)
		return m, nil
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		wasReady := m.ready
		// m.width is the full terminal width minus the (now zero)
		// scrollbackLeftMargin — see the const's note on the flush-left
		// canvas. The subtraction stays so a future non-zero margin would
		// flow through every downstream width calc (tool cards, table
		// sizing, prose wrap) without further changes.
		m.width = msg.Width - scrollbackLeftMargin
		if m.width < 20 {
			m.width = msg.Width // terminal too narrow; bypass the margin
		}
		m.height = msg.Height
		m.md = newMarkdownRenderer(msg.Width - 4)
		m.textInput.SetWidth(liveContentWidth(msg.Width))
		m.fitTextareaHeight()
		if !m.ready {
			m.ready = true
			// First Update tick — flush the startup box + welcome to
			// scrollback. Gated on startupPrinted so subsequent resizes
			// don't reprint.
			if !m.startupPrinted {
				m.startupPrinted = true
				m.appendRawFlush(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.sess.ID, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width))
				// One blank line of breathing room between the card and
				// the input frame — matches the Phase 2 spacing target.
				m.queuePrintln("")
				// Construction-time entry banners (yolo mode,
				// plan/auto mode, custom-command errors) recorded into
				// historyLines before any width was known — queuePrintln
				// deferred their emission rather than wrap at the 80-col
				// fallback (see queuePrintlnIndented). Emit them now at the
				// real width, below the box, matching the box-then-history
				// order the resize replay uses so the layout is identical
				// on first boot and after every resize.
				for _, line := range m.historyLines {
					m.queuePrintln(line)
				}
				for _, n := range m.pendingStartupNotices {
					m.appendLine(n)
				}
				m.pendingStartupNotices = nil
				m.emitMemorySizeWarnings()
				// Resumed session: replay the prior user/assistant
				// transcript into scrollback so the user can see the
				// conversation they're continuing. The picker's
				// resumeSession path does this already; doing it here
				// covers the cobra `--resume` / `sessions resume <ref>`
				// entry points where the TUI starts pre-loaded.
				if !m.isFreshSession() {
					m.appendLine(styleAuto.Render(fmt.Sprintf("[resume] loaded %s (%d msgs)", m.sess.ID, len(m.sess.Messages))))
					rebuildTranscript(&m)
				}
			}
		}
		if wasReady {
			// Genuine resize (not the initial size on startup). Bubbletea's
			// inline-mode renderer can't reliably clean up a bordered live
			// frame when the width changes — the previous frame's border
			// characters smear into scrollback as stair-step "┌───" ghosts
			// at every prior width. tea.ClearScreen wipes the visible
			// viewport so the next View() draws clean.
			//
			// We then replay the conversation under freshly-rendered
			// startup chrome at the new width via repaintViewport — see
			// that helper for why the replay goes through queuePrintln
			// (clear-line prefix + re-wrap to the new width) rather than
			// a bare tea.Println.
			m.repaintViewport()
			return m, nil
		}
		return m, nil

	case tea.KeyMsg:
		// Bracketed pastes arrive with terminal line breaks — CR (or
		// CRLF), not LF. Normalize before ANY routing so every consumer
		// (pickers, tryImagePaste's multi-line rejection, the
		// large-paste detour, the textarea) sees the single '\n'
		// convention. A raw CR that survives to submit overprints the
		// transcript echo from column 0, mangling the line into garbage.
		if msg.Paste {
			msg.Runes = normalizePasteLineBreaks(msg.Runes)
		}
		if m.loopExitConfirmOpen {
			return m.updateLoopExitConfirm(msg)
		}
		if m.cheatsheetOpen {
			m.cheatsheetOpen = false
			return m, nil
		}
		if m.loopListOpen {
			// Any key dismisses the bare-/loop status panel (mirrors the
			// cheatsheet). Intercepted here, ahead of the Esc/Ctrl+C
			// loop-stop handlers, so closing the panel never disarms loops.
			m.loopListOpen = false
			return m, nil
		}
		if m.usageOpen {
			m.usageOpen = false
			m.usagePanel = ""
			return m, nil
		}
		if m.contextReportOpen {
			m.contextReportOpen = false
			m.contextReportBody = ""
			return m, nil
		}
		if m.permissionsOpen {
			return m.updatePermissionsPicker(msg)
		}
		// Picker overlays consume keystrokes ahead of the slash
		// palette so users can navigate the list without bleeding
		// into the cmdline. Each picker decides what to do with each
		// key; unknown keys fall through to a no-op rather than to
		// the textarea, since the textarea is hidden underneath.
		if m.modelPickerOpen {
			return m.updateModelPicker(msg)
		}
		if m.providerPickerOpen {
			return m.updateProviderPicker(msg)
		}
		if m.embedSetupOpen {
			return m.updateEmbedSetup(msg)
		}
		if m.memoryPickerOpen {
			return m.updateMemoryPicker(msg)
		}
		if m.recallPickerOpen {
			return m.updateRecallPicker(msg)
		}
		if m.sessionsPickerOpen {
			return m.updateSessionsPicker(msg)
		}
		if m.plansPickerOpen {
			return m.updatePlansPicker(msg)
		}
		if m.checkpointsPickerOpen {
			return m.updateCheckpointsPicker(msg)
		}
		if m.themePickerOpen {
			return m.updateThemePicker(msg)
		}
		if m.effortPickerOpen {
			return m.updateEffortPicker(msg)
		}
		if m.subagentsPickerOpen {
			return m.updateSubagentsPicker(msg)
		}
		if m.routerPickerOpen {
			return m.updateRouterPicker(msg)
		}
		if m.skillsMenuOpen {
			return m.updateSkillsMenu(msg)
		}
		if m.skillsPickerOpen {
			return m.updateSkillsPicker(msg)
		}
		if m.mcpPickerOpen {
			return m.updateMCPPicker(msg)
		}
		// Live-dock keyboard navigation. When focused, the dock consumes
		// keys (up/down/enter/esc) ahead of the cmdline. Tab focuses it —
		// but ONLY when no completion palette is active (palette owns Tab)
		// and a subagent is actually running, so normal typing/Tab is
		// untouched the rest of the time.
		// Drop stale focus if every subagent finished while the dock was
		// focused — otherwise this keypress would be swallowed by
		// updateDockFocus just to clear the flag, and the dock (which renders
		// nothing with no running tasks) can't show focus anyway.
		if m.dockFocused && !m.hasRunningSubagents() {
			m.dockFocused = false
			m.dockCursor = 0
		}
		if m.dockFocused {
			return m.updateDockFocus(msg)
		}
		if msg.Type == tea.KeyTab && !m.paletteOpen && !m.filePaletteOpen && m.hasRunningSubagents() {
			m.dockFocused = true
			m.dockCursor = 0
			return m, nil
		}
		// Intercept image pastes before any other handling. Terminals
		// paste image paths as file:/// URLs or raw paths — detect
		// these regardless of size and replace with a compact marker.
		if msg.Paste {
			if marker, ok := m.tryImagePaste(string(msg.Runes)); ok {
				syn := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(marker)}
				m.preGrowTextarea()
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(syn)
				m.fitTextareaHeight()
				return m, cmd
			}
		}
		// Intercept large bracketed pastes before any other handling.
		// Bubbletea sets msg.Paste=true with all the pasted runes in a
		// single KeyMsg. For anything over the threshold, we swap the
		// runes for a short marker and stash the original — keeps the
		// cmdline from stretching to fill the screen on a 5KB paste.
		if msg.Paste && (len(msg.Runes) > pasteThreshold || strings.ContainsRune(string(msg.Runes), '\n')) {
			return m.handleLargePaste(msg)
		}
		// While the path-trust modal is up, route keys to that handler
		// (further down) — not to the mid-turn textarea path below.
		// Without this exception, a turn in flight waiting on the
		// elevation decision would swallow "1"/"2"/"3" into the
		// textarea and route Esc to turnCancel instead of "reject",
		// leaving the modal unresponsive until the process is killed.
		// Palette navigation during a turn: let the user browse
		// /commands and @files while the model is thinking. Handled
		// before the mid-turn textarea block so Up/Down/Tab/Esc reach
		// the palette instead of the textarea.
		if m.turnActive && !m.awaitingApproval && !m.awaitingPathTrust && m.paletteOpen {
			switch msg.Type {
			case tea.KeyUp:
				if m.paletteIndex > 0 {
					m.paletteIndex--
					if m.paletteIndex < m.paletteOffset {
						m.paletteOffset = m.paletteIndex
					}
				}
				return m, nil
			case tea.KeyDown:
				if m.paletteIndex < len(m.paletteFiltered)-1 {
					m.paletteIndex++
					if m.paletteIndex >= m.paletteOffset+slashPaletteVisible {
						m.paletteOffset = m.paletteIndex - slashPaletteVisible + 1
					}
				}
				return m, nil
			case tea.KeyTab:
				if len(m.paletteFiltered) > 0 {
					c := m.paletteFiltered[m.paletteIndex]
					m.textInput.SetValue("/" + c.Name + " ")
					m.textInput.CursorEnd()
					m.paletteOpen = false
				}
				return m, nil
			case tea.KeyEsc:
				m.textInput.SetValue("")
				m.paletteOpen = false
				m.paletteIndex = 0
				m.paletteOffset = 0
				return m, nil
			}
			// Non-navigation keys (Enter, typing) fall through to
			// the mid-turn handler below.
		}
		if m.turnActive && !m.awaitingApproval && !m.awaitingPathTrust && m.filePaletteOpen {
			switch msg.Type {
			case tea.KeyUp:
				if m.filePaletteIndex > 0 {
					m.filePaletteIndex--
					if m.filePaletteIndex < m.filePaletteOffset {
						m.filePaletteOffset = m.filePaletteIndex
					}
				}
				return m, nil
			case tea.KeyDown:
				if m.filePaletteIndex < len(m.filePaletteFiltered)-1 {
					m.filePaletteIndex++
					if m.filePaletteIndex >= m.filePaletteOffset+filePaletteVisible {
						m.filePaletteOffset = m.filePaletteIndex - filePaletteVisible + 1
					}
				}
				return m, nil
			case tea.KeyTab, tea.KeyEnter:
				if len(m.filePaletteFiltered) > 0 {
					m.acceptFilePaletteChoice()
					return m, nil
				}
				if msg.Type == tea.KeyEsc {
					m.filePaletteOpen = false
					m.filePaletteIndex = 0
					m.filePaletteOffset = 0
					return m, nil
				}
			case tea.KeyEsc:
				m.filePaletteOpen = false
				m.filePaletteIndex = 0
				m.filePaletteOffset = 0
				return m, nil
			}
		}
		if m.turnActive && !m.awaitingApproval && !m.awaitingPathTrust {
			switch msg.Type {
			case tea.KeyUp:
				// Up while the active-turn textarea is empty is the edit handle for
				// a queued-but-undelivered user message. Draining userMsgCh removes
				// the pending delivery; pressing Enter after editing will enqueue the
				// revised text through the normal mid-turn path below.
				if strings.TrimSpace(m.textInput.Value()) == "" {
					select {
					case queued := <-m.userMsgCh:
						if queued != "" {
							m.textInput.SetValue(queued)
							m.textInput.CursorEnd()
							m.appendLine(styleAuto.Render("[queued] recalled for editing"))
							return m, nil
						}
					default:
					}
					if m.pendingInputAfterTurn != "" {
						queued := m.pendingInputAfterTurn
						m.pendingInputAfterTurn = ""
						m.textInput.SetValue(queued)
						m.textInput.CursorEnd()
						m.appendLine(styleAuto.Render("[queued] recalled for editing"))
						return m, nil
					}
				}
				m.preGrowTextarea()
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(msg)
				m.fitTextareaHeight()
				return m, cmd
			case tea.KeyCtrlC, tea.KeyEsc:
				// Esc mirrors Claude Code's cancel feel — same effect
				// as Ctrl+C while a turn is running: kill the
				// in-flight iteration, leave the textarea contents
				// alone, do NOT enqueue anything for resubmission.
				// Use Enter to interrupt-with-feedback; use Esc /
				// Ctrl+C to stop without sending.
				if m.turnCancel != nil {
					m.turnCancel()
				}
				m.pendingInputAfterTurn = ""
				// Esc/Ctrl+C also disarms a running /loop — otherwise the
				// turn cancel above fires turnEndedMsg and must not let a loop
				// continue after the user explicitly stopped it.
				m.disarmAllLoops("[loop] stopped")
				// Drain any queued-but-undelivered append message.
				select {
				case <-m.userMsgCh:
				default:
				}
				return m, nil
			case tea.KeyCtrlD:
				return m, tea.Quit
			case tea.KeyCtrlU:
				m.snapshotKillBeforeCursor()
				m.preGrowTextarea()
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(msg)
				m.fitTextareaHeight()
				return m, cmd
			case tea.KeyCtrlY:
				if m.killRing == "" {
					return m, nil
				}
				m.preGrowTextarea()
				m.textInput.InsertString(m.killRing)
				m.fitTextareaHeight()
				return m, nil
			case tea.KeyShiftTab:
				// Mode cycling works mid-turn (mirrors Claude Code):
				// the loop reads the mode atomics on every tool
				// dispatch and rebuilds the plan addendum per
				// iteration, so the flip applies from the agent's next
				// tool call — flip auto on to stop babysitting a long
				// implementation, or plan on to pull the agent back to
				// read-only without killing the turn. This branch
				// already excludes awaitingApproval/awaitingPathTrust,
				// so a pending decision the loop is blocked on can't
				// be yanked; palettes keep ownership of the keyboard.
				if m.paletteOpen || m.filePaletteOpen {
					return m, nil
				}
				return cycleAgentMode(m)
			case tea.KeyEnter:
				input := strings.TrimSpace(m.textInput.Value())
				// Palette selection: resolve the highlighted entry
				// so Enter picks the selected command, not the
				// partial text in the textarea.
				if m.paletteOpen && len(m.paletteFiltered) > 0 && !strings.Contains(input, " ") {
					chosen := m.paletteFiltered[m.paletteIndex]
					if chosen.Args != "" {
						m.textInput.SetValue("/" + chosen.Name + " ")
						m.textInput.CursorEnd()
						m.paletteOpen = false
						m.paletteIndex = 0
						m.paletteOffset = 0
						return m, nil
					}
					input = "/" + chosen.Name
				}
				if strings.HasPrefix(input, "/") {
					// Decide whether to cancel the active turn before
					// running this slash command.
					//
					//   - Known command with PreservesTurn=true (e.g.
					//     /subagents, /help) → don't cancel; it's a
					//     pure inspection that mustn't disrupt the
					//     in-flight work.
					//   - Known command with PreservesTurn=false (e.g.
					//     /clear, /model, /sessions) → cancel; the
					//     user is signaling "stop current work, apply
					//     this change."
					//   - Unknown command (typo, deprecated name) →
					//     don't cancel; we're about to surface a
					//     "unknown command" error and do nothing.
					//     Canceling the turn for a typo was the actual
					//     bug: `/subagent` (singular, typo'd) used to
					//     kill the user's in-flight foreground
					//     subagent before the error message even
					//     printed.
					shouldCancel := false
					if fields := strings.Fields(input); len(fields) > 0 {
						name := strings.TrimPrefix(fields[0], "/")
						if c := m.findSlash(name); c != nil && !c.PreservesTurn {
							shouldCancel = true
						}
					}
					if shouldCancel && m.turnCancel != nil {
						m.turnCancel()
					}
					// A slash command typed mid-turn is a fresh user
					// intent — drop any queued message so we don't
					// auto-submit stale input after the turn unwinds.
					m.pendingInputAfterTurn = ""
					select {
					case <-m.userMsgCh:
					default:
					}
					m.textInput.SetValue("")
					m.paletteOpen = false
					m.paletteIndex = 0
					m.paletteOffset = 0
					return m.runSlash(input)
				}
				// Plain Enter on non-empty input mid-turn:
				// queue the message for delivery at the next tool
				// round without cancelling the active turn. The agent
				// loop picks it up from userMsgCh between tool-call
				// batches and appends it to history as a user message.
				// If the buffer is full (a second message before the
				// first was consumed), fall back to the old
				// cancel+resubmit path.
				if input == "" {
					return m, nil
				}
				input = m.expandPastes(input)
				m.pastes = nil
				m.pastedImages = nil
				select {
				case m.userMsgCh <- input:
					m.textInput.SetValue("")
					m.paletteOpen = false
					m.paletteIndex = 0
					m.paletteOffset = 0
					m.appendLine(renderUserBlock(input, m.width))
					m.appendLine(styleAuto.Render("[queued] will be delivered at next tool round"))
					return m, nil
				default:
					// Buffer full — fall back to cancel+resubmit.
					m.pendingInputAfterTurn = input
					m.textInput.SetValue("")
					m.paletteOpen = false
					m.paletteIndex = 0
					m.paletteOffset = 0
					if m.turnCancel != nil {
						m.turnCancel()
					}
					return m, nil
				}
			default:
				m.preGrowTextarea()
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(msg)
				m.fitTextareaHeight()
				val := m.textInput.Value()
				if strings.HasPrefix(val, "/") {
					m.paletteFiltered = m.filterPaletteAll(val)
					m.paletteOpen = true
					if m.paletteIndex >= len(m.paletteFiltered) {
						m.paletteIndex = 0
						m.paletteOffset = 0
					}
					if m.paletteOffset > len(m.paletteFiltered)-slashPaletteVisible {
						m.paletteOffset = 0
					}
					m.filePaletteOpen = false
				} else {
					m.paletteOpen = false
					m.paletteIndex = 0
					m.paletteOffset = 0
					m.refreshFilePalette(val)
				}
				return m, cmd
			}
		}
		if m.awaitingPathTrust {
			// Inline path-trust elevation (Prompt 2). The three
			// choices are independent of the normal approval modal
			// — the model never sees an Allow/Deny here, only the
			// retry result on accept or the descriptive
			// ErrPathOutsideWorkspace on reject. See
			// yottacode-roadmap/folder-trust.md.
			answered := false
			switch msg.String() {
			case "1", "o", "O":
				addAllowedPathToWriteTools(m.cfg.Registry, m.pathTrustReq.Path)
				if !m.sendDecision(agent.PathAllowOnce) {
					return m, nil
				}
				m.appendLine(stylePathTrustAccept.Render("trusted for this write: ") + m.pathTrustReq.Path)
				answered = true
			case "2", "t", "T":
				dir := filepath.Dir(m.pathTrustReq.Path)
				addAllowedPathToWriteTools(m.cfg.Registry, dir)
				if !m.sendDecision(agent.PathTrustSession) {
					return m, nil
				}
				m.appendLine(stylePathTrustAccept.Render("trusted for this session: ") + dir)
				answered = true
			case "3", "n", "N", "esc":
				if !m.sendDecision(agent.Deny) {
					return m, nil
				}
				m.appendLine(stylePathTrustReject.Render("path trust denied — model sees error"))
				answered = true
			}
			if answered {
				return m, m.finishDecisionUI()
			}
			return m, nil
		}
		if m.awaitingApproval {
			// exit_plan_mode is a different shape of approval — its
			// keys mean "approve and execute" / "keep planning",
			// never "always allow" (which would derive a permission
			// rule that doesn't apply). Handle it before the generic
			// modal so the user never sees the always-allow path.
			if m.approvalTool == "exit_plan_mode" {
				answered := false
				switch msg.String() {
				case "a", "A", "enter":
					// Auto-approval: exit plan mode AND enter auto
					// mode for the implementation. Per-tool prompts
					// auto-allow for the rest of the turn except the
					// safety floor (run_bash, git_commit,
					// git_checkpoint, rollback). [A] is the default
					// (Enter) because users who approve a plan are
					// usually ready to let it run.
					exitPlanMode(&m)
					if m.cfg.AutoMode != nil {
						m.cfg.AutoMode.Active.Store(true)
						m.appendLine(styleAutoBannerLabel.Render(AutoModeIcon+" auto mode active") +
							" " + styleAutoBannerHint.Render("— implementing the approved plan; bash & commits still prompt"))
					}
					if !m.sendDecision(agent.AllowOnce) {
						return m, nil
					}
					answered = true
				case "m", "M":
					// Manual approval: flip plan mode off BEFORE
					// forwarding the decision so the next iteration's
					// tool dispatch sees the new state and the schema
					// filter stops advertising exit_plan_mode. Normal
					// per-tool approval prompts continue for the
					// implementation turn — user reviews each step.
					exitPlanMode(&m)
					if !m.sendDecision(agent.AllowOnce) {
						return m, nil
					}
					answered = true
				case "l", "L":
					// Save and implement later: flip plan mode off
					// (plan stays on disk for /plan list / --plan-resume)
					// AND send SaveForLater so the model returns a
					// firm "end this turn" message instead of
					// implementing. The plan body is already in
					// scrollback from emitPlanBodyToScrollback so we
					// don't need to re-emit on dismiss.
					exitPlanMode(&m)
					m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon+" plan saved for later") +
						" " + stylePlanBannerHint.Render("— resume via /plan list or `yottacode --plan-resume <slug>`"))
					if !m.sendDecision(agent.SaveForLater) {
						return m, nil
					}
					answered = true
				case "k", "K", "n", "N", "esc":
					// Keep planning: log a dismissal header so the
					// user sees what they just did. Plan body is
					// already in scrollback from
					// emitPlanBodyToScrollback (above the modal).
					m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon+" plan kept") +
						" " + stylePlanBannerHint.Render("— revise and call exit_plan_mode again when ready"))
					if !m.sendDecision(agent.Deny) {
						return m, nil
					}
					answered = true
				}
				if answered {
					return m, m.finishDecisionUI()
				}
				return m, nil
			}
			// enter_plan_mode is the mirror handshake: [Y] flips the
			// shared plan-mode state (and derives the plan file from
			// the in-flight user message) BEFORE forwarding AllowOnce,
			// so the tool's Execute — and every later gate decision in
			// this turn — sees the mode genuinely on. No "always
			// allow": a derived rule would silently auto-flip modes on
			// every future request, defeating the confirmation.
			if m.approvalTool == "enter_plan_mode" {
				answered := false
				switch msg.String() {
				case "y", "Y", "enter":
					enterPlanModeFromTool(&m)
					if !m.sendDecision(agent.AllowOnce) {
						return m, nil
					}
					answered = true
				case "n", "N", "esc":
					// The loop returns EnterPlanModeRefusalMessage so
					// the model continues in the current mode instead
					// of re-requesting.
					m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon+" plan mode declined") +
						" " + stylePlanBannerHint.Render("— continuing in the current mode"))
					if !m.sendDecision(agent.Deny) {
						return m, nil
					}
					answered = true
				}
				if answered {
					return m, m.finishDecisionUI()
				}
				return m, nil
			}
			answered := false
			switch msg.String() {
			case "y", "Y":
				if !m.sendDecision(agent.AllowOnce) {
					return m, nil
				}
				answered = true
			case "a", "A":
				if m.approvalAllowAlwaysOK {
					rule := m.approvalDerivedRule
					if !m.sendDecision(agent.AllowAlways) {
						return m, nil
					}
					answered = true
					// Toast — keeps the modal itself focused on
					// the immediate decision; the receipt of what
					// got persisted lands in scrollback so the
					// user can see exactly which rule was added.
					if rule != "" {
						m.appendLine(approvalToast(rule))
					}
				}
			case "d", "D":
				if m.approvalDenyAlwaysOK {
					rule := m.approvalDerivedDenyRule
					if !m.sendDecision(agent.DenyAlways) {
						return m, nil
					}
					answered = true
					// Receipt of the block that was just persisted, so
					// the user sees exactly which deny rule was added.
					if rule != "" {
						m.appendLine(approvalDenyToast(rule))
					}
				}
			case "n", "N", "esc":
				if !m.sendDecision(agent.Deny) {
					return m, nil
				}
				answered = true
			}
			if answered {
				return m, m.finishDecisionUI()
			}
			return m, nil
		}

		if m.paletteOpen {
			switch msg.Type {
			case tea.KeyUp:
				if m.paletteIndex > 0 {
					m.paletteIndex--
					if m.paletteIndex < m.paletteOffset {
						m.paletteOffset = m.paletteIndex
					}
				}
				return m, nil
			case tea.KeyDown:
				if m.paletteIndex < len(m.paletteFiltered)-1 {
					m.paletteIndex++
					if m.paletteIndex >= m.paletteOffset+slashPaletteVisible {
						m.paletteOffset = m.paletteIndex - slashPaletteVisible + 1
					}
				}
				return m, nil
			case tea.KeyTab:
				if len(m.paletteFiltered) > 0 {
					c := m.paletteFiltered[m.paletteIndex]
					m.textInput.SetValue("/" + c.Name + " ")
					m.textInput.CursorEnd()
					m.paletteOpen = false
				}
				return m, nil
			case tea.KeyEsc:
				m.textInput.SetValue("")
				m.paletteOpen = false
				m.paletteIndex = 0
				m.paletteOffset = 0
				return m, nil
			}
		} else if m.filePaletteOpen {
			// File palette navigation. Up/Down move the selection,
			// Tab/Enter splice the chosen path into the textarea
			// (replacing the active `@<query>` token), Esc closes the
			// palette but leaves the literal `@<query>` text in place
			// so the user can still submit a turn with a non-existent
			// path if they really meant to.
			switch msg.Type {
			case tea.KeyUp:
				if m.filePaletteIndex > 0 {
					m.filePaletteIndex--
					if m.filePaletteIndex < m.filePaletteOffset {
						m.filePaletteOffset = m.filePaletteIndex
					}
				}
				return m, nil
			case tea.KeyDown:
				if m.filePaletteIndex < len(m.filePaletteFiltered)-1 {
					m.filePaletteIndex++
					if m.filePaletteIndex >= m.filePaletteOffset+filePaletteVisible {
						m.filePaletteOffset = m.filePaletteIndex - filePaletteVisible + 1
					}
				}
				return m, nil
			case tea.KeyTab, tea.KeyEnter:
				if len(m.filePaletteFiltered) > 0 {
					m.acceptFilePaletteChoice()
					// Tab keeps the textarea focused for further
					// typing; Enter is treated as "I'm done picking"
					// — the next Enter (with no further chars) will
					// submit normally.
					if msg.Type == tea.KeyEnter {
						return m, nil
					}
					return m, nil
				}
				if msg.Type == tea.KeyEnter {
					// No matches: fall through to normal Enter so the
					// user can submit whatever they typed.
					m.filePaletteOpen = false
				} else {
					return m, nil
				}
			case tea.KeyEsc:
				m.filePaletteOpen = false
				m.filePaletteIndex = 0
				m.filePaletteOffset = 0
				return m, nil
			}
		} else {
			// Only claim Up/Down for history when the cursor is at the
			// natural edge of the textarea — first logical line for Up,
			// last for Down. In the middle of a multi-line draft the
			// keys must reach the textarea so the cursor moves between
			// rows like every other shell. Single-line drafts always
			// pass the gate (Line()==0 and Line()==LineCount()-1).
			switch msg.Type {
			case tea.KeyUp:
				if m.textInput.Line() == 0 {
					if newM, handled := m.historyBack(); handled {
						return newM, nil
					}
				}
			case tea.KeyDown:
				if m.textInput.Line() >= m.textInput.LineCount()-1 {
					if newM, handled := m.historyForward(); handled {
						return newM, nil
					}
				}
			}
		}

		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == '?' && m.textInput.Value() == "" {
			m.cheatsheetOpen = true
			return m, nil
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			if m.turnActive && m.turnCancel != nil {
				m.turnCancel()
				m.disarmAllLoops("[loop] stopped")
				return m, nil
			}
			// A first Ctrl+C stops an armed /loop instead of quitting; a
			// second (loop now disarmed) quits as usual.
			if m.activeLoopCount() > 0 {
				m.disarmAllLoops("[loop] stopped")
				return m, nil
			}
			// Ctrl+C is the "get me out NOW" gesture — always an
			// immediate quit, never the final memory turn. Sessions
			// ending this way are covered mid-flight by the periodic
			// capture reminder (captureReminderDue), not by
			// overloading this key with a model call.
			return m, tea.Quit
		case tea.KeyCtrlD:
			// Deliberate idle exit — same graceful path as /quit (final
			// memory turn when warranted).
			return requestGracefulExit(m)
		case tea.KeyEsc:
			// A single Esc stops an armed /loop, before the esc-esc chord
			// gets a chance to open /checkpoints.
			if m.activeLoopCount() > 0 {
				m.disarmAllLoops("[loop] stopped")
				return m, nil
			}
			// Esc-Esc within escChordWindow opens the /checkpoints picker
			// — mirrors Claude Code's double-escape. Single Esc has no
			// other meaning here (no overlay, no turn, no palette — those
			// branches return early above), so consuming it for the chord
			// is safe. Reset on chord fire so a third Esc starts a fresh
			// window instead of immediately retriggering.
			if !m.lastEscAt.IsZero() && time.Since(m.lastEscAt) <= escChordWindow {
				m.lastEscAt = time.Time{}
				m.openCheckpointsPicker()
				return m, nil
			}
			m.lastEscAt = time.Now()
			return m, nil
		case tea.KeyShiftTab:
			// Cycle through normal → auto → plan → normal. Mirrors
			// Claude Code's Shift+Tab, INCLUDING mid-turn: the loop
			// reads the mode atomics on every tool dispatch and
			// rebuilds the plan addendum per iteration, so a flip
			// while the agent is working takes effect on its very
			// next tool call — flip auto on to stop babysitting a
			// long implementation, or flip plan on to pull the agent
			// back to read-only without killing the turn. Suppressed
			// while a palette overlay is open (those own the
			// keyboard); a pending approval/path-trust modal consumes
			// keys in its own branch above, so a decision the loop is
			// blocked on can never be yanked out from under it.
			if m.paletteOpen || m.filePaletteOpen {
				return m, nil
			}
			return cycleAgentMode(m)
		case tea.KeyCtrlU:
			// Capture the line-before-cursor into the kill ring before
			// forwarding to bubbles' textarea (which performs the actual
			// delete via DeleteBeforeCursor). Ctrl+Y yanks it back.
			m.snapshotKillBeforeCursor()
			m.preGrowTextarea()
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(msg)
			m.fitTextareaHeight()
			return m, cmd
		case tea.KeyCtrlY:
			// Yank the most recent kill at the cursor. Bubbles doesn't
			// bind Ctrl+Y by default, so we own this key outright.
			if m.killRing == "" {
				return m, nil
			}
			m.preGrowTextarea()
			m.textInput.InsertString(m.killRing)
			m.fitTextareaHeight()
			return m, nil
		case tea.KeyEnter:
			if m.turnActive {
				return m, nil
			}
			input := strings.TrimSpace(m.textInput.Value())
			if m.paletteOpen && len(m.paletteFiltered) > 0 && !strings.Contains(input, " ") {
				chosen := m.paletteFiltered[m.paletteIndex]
				if chosen.Args != "" {
					m.textInput.SetValue("/" + chosen.Name + " ")
					m.textInput.CursorEnd()
					m.paletteOpen = false
					m.paletteIndex = 0
					m.paletteOffset = 0
					return m, nil
				}
				input = "/" + chosen.Name
			}
			if input == "" {
				return m, nil
			}
			// Block starting a new turn (but not slash commands) while a
			// summarize is running: the turn's user message + reply would
			// be silently dropped when summaryDoneMsg replaces history with
			// the pre-summarize snapshot. Return before clearing the box so
			// the typed text is preserved and sends once compression ends.
			if m.summarizing && !strings.HasPrefix(input, "/") {
				return m, nil
			}
			m.textInput.SetValue("")
			m.paletteOpen = false
			m.paletteIndex = 0
			m.paletteOffset = 0
			m.dropFilePalette()
			// Swap any [Pasted text #N: ...] markers back for their
			// original content before the message hits the agent.
			input = m.expandPastes(input)
			m.pastes = nil
			if strings.HasPrefix(input, "/") {
				m.pastedImages = nil
				return m.runSlash(input)
			}
			// pastedImages is consumed by startTurn → startTurnWithDisplay
			// via collectPastedImages(); no nil needed here.
			return m.startTurn(input)
		default:
			m.preGrowTextarea()
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(msg)
			m.fitTextareaHeight()
			val := m.textInput.Value()
			if strings.HasPrefix(val, "/") {
				m.paletteFiltered = m.filterPaletteAll(val)
				m.paletteOpen = true
				if m.paletteIndex >= len(m.paletteFiltered) {
					m.paletteIndex = 0
					m.paletteOffset = 0
				}
				// Clamp offset against the new filtered length so a
				// narrower match-set doesn't leave the window stuck
				// past the end of the list (would render an empty
				// box with just the ▲ N more hint).
				if m.paletteOffset > len(m.paletteFiltered)-slashPaletteVisible {
					m.paletteOffset = 0
				}
				m.filePaletteOpen = false
			} else {
				m.paletteOpen = false
				m.paletteIndex = 0
				m.paletteOffset = 0
				// Detect an active `@<query>` token at the end of the
				// textarea value and (re)compute the file palette. The
				// candidate list is walked once and cached; only the
				// filtered subset rebuilds per keystroke.
				m.refreshFilePalette(val)
			}
			return m, cmd
		}

	case agentEventMsg:
		return m.handleAgentEvent(msg.ev)

	case modelPickerLoadedMsg:
		return m.handleModelPickerLoaded(msg)

	case skillsCatalogRefreshDoneMsg:
		return handleSkillsCatalogRefreshDone(m, msg)

	case skillsOfficialInstallDoneMsg:
		return handleSkillsOfficialInstallDone(m, msg)

	case skillsMenuInstallDoneMsg:
		return handleSkillsMenuInstallDone(m, msg)

	case skillsMenuUpdateDoneMsg:
		return handleSkillsMenuUpdateDone(m, msg)

	case tea.MouseMsg:
		// Mouse capture stays enabled (so palette clicks could be wired up
		// later); we no longer forward to a viewport because the conversation
		// lives in the terminal's native scrollback. Wheel events fall
		// through to the terminal — Shift+drag selects text in scrollback as
		// the terminal owns it.
		return m, nil

	case loopTickMsg:
		// The /loop heartbeat targets one active local loop. Drop stale ticks from
		// stopped, expired, or replaced loops.
		ls, ok := m.loops[msg.id]
		if !ok || !ls.active {
			return m, nil
		}
		if !ls.expiresAt.IsZero() && time.Now().After(ls.expiresAt) {
			m.disarmLoop(msg.id, "[loop] "+msg.id+" expired after 5d")
			return m, nil
		}
		// A turn (or summarize) is still running — skip this cycle and re-arm
		// only this loop's next interval so cadence holds without stacking turns.
		if m.turnActive || m.summarizing {
			return m, loopTickCmd(ls.interval, msg.id)
		}
		next, tickCmd := m.fireLoopIteration(msg.id)
		m = next
		var loopCmds []tea.Cmd
		if tickCmd != nil {
			loopCmds = append(loopCmds, tickCmd)
		}
		// Re-arm unless the final bounded iteration or a no-turn prose payload
		// just disarmed this loop.
		if ls, ok := m.loops[msg.id]; ok && ls.active && !ls.isSlash && !m.turnActive && !m.summarizing {
			m.disarmLoop(msg.id, "[loop] "+msg.id+" stopped — payload started no turn")
		}
		if ls, ok := m.loops[msg.id]; ok && ls.active {
			loopCmds = append(loopCmds, loopTickCmd(ls.interval, msg.id))
		}
		return m, tea.Batch(loopCmds...)

	case turnEndedMsg:
		m.turnActive = false
		// If this turn was a /loop prose iteration and the agent called
		// loop_control{stop}, disarm that loop now that the turn is over. Also
		// clears the per-turn loop-control flag so the tool is hidden again.
		m.consumeLoopControl()
		m.clearPendingDecisionUI()
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		m.commitStreaming()
		// turnEndedMsg.err is intentionally NOT rendered here. agent.Turn
		// emits ErrorEvent for every user-visible error before returning,
		// so this path would duplicate the message. The only errors that
		// bypass ErrorEvent are ctx.Err() from internal send() failures —
		// those are user-initiated cancellations and don't warrant a red
		// "✗ context canceled" line.
		// Persist the subagent task index with the session so its task-ids
		// resolve on a later resume.
		if m.subagentTasks != nil {
			m.sess.SubagentTasks = m.subagentTasks.Export()
		}
		if err := m.sess.Save(); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("⚠ session save failed: %v", err)))
		}
		if m.recall != nil {
			if err := m.recall.IndexSession(m.sess); err != nil {
				m.appendLine(styleError.Render(fmt.Sprintf("⚠ recall index failed: %v", err)))
			}
			m.embedCurrentSessionAsync()
		}
		// The final memory turn on quit just ended (completed or
		// cancelled via Esc/Ctrl+C — both land here): finish the quit.
		// Session save + recall indexing above already ran, so the exit
		// turn itself is persisted; watermark checks and queued-input
		// resubmission are pointless on the way out.
		if m.exitSavePending {
			return m, tea.Quit
		}
		// Window-drift shrink: a context-overflow rejection the
		// estimator didn't see coming means the resolved window is
		// bigger than what the provider enforces. Pinning the
		// corrected value BEFORE the watermark check below is the
		// recovery path — the check re-resolves the now-smaller window
		// and fires auto-summarize in this same tick, so the session
		// heals instead of failing again on the next send.
		m.noteWindowOverflow(msg.err)

		// Self-healing: surface any background completion whose inbox event
		// was dropped (non-blocking send under burst, or while the loop was
		// suspended in the pager) by reconciling against the durable registry
		// state. Runs before the wake drain below so a recovered
		// notify_on_done task still gets its wake turn.
		m.reconcileSubagentCompletions()
		// Drain any user message that was queued but never consumed
		// by the agent loop (the model finished without a tool round).
		// Move it to pendingInputAfterTurn so the existing auto-submit
		// path handles it as a new turn. Drained BEFORE the watermark
		// check so the check knows whether a queued turn is imminent.
		select {
		case undelivered := <-m.userMsgCh:
			if undelivered != "" && m.pendingInputAfterTurn == "" {
				m.pendingInputAfterTurn = undelivered
			}
		default:
		}
		// Watermark check: post-turn, before the next prompt is
		// accepted. Either fires a yellow notice (warn threshold) or
		// returns a Cmd that runs auto-summarization (auto threshold).
		// When a queued message is about to start a fresh turn, the
		// auto branch is suppressed inside the check (allowAuto=false):
		// the queued submission is the user's explicit next-turn
		// intent, and compressing now would yank context out from
		// under the very message they just sent. The warn notice still
		// prints here so it lands above the new user block.
		ctxCmd := m.updateContextUsage(m.pendingInputAfterTurn == "")
		// If the user interrupted mid-turn by hitting Enter on a
		// non-empty message, agent.Turn has already preserved history
		// with synthetic tool_result entries — submit the queued
		// message now as a fresh turn so the model sees the feedback.
		if queued := m.pendingInputAfterTurn; queued != "" {
			m.pendingInputAfterTurn = ""
			next, cmd := m.startTurn(queued)
			return next, cmd
		}
		// Background completions that asked to wake the model (notify_on_done)
		// and landed while this turn was running now get their turn. Queued
		// user input above takes priority; the wake fires once nothing the
		// user explicitly typed is pending. Like that path, the watermark
		// ctxCmd is dropped — the wake is the next intended turn.
		if len(m.pendingSubagentWakes) > 0 && !m.summarizing {
			next, cmd := m.startSubagentWakeTurn()
			return next, cmd
		}
		if ctxCmd != nil {
			return m, ctxCmd
		}
		return m, nil

	case editorDoneMsg:
		// vim returned; reload memory so the edit takes effect now.
		if msg.err != nil {
			m.appendLine(styleError.Render("[memory] editor: " + msg.err.Error()))
			return m, nil
		}
		out, cmd := reloadMemoryNow(m, "[memory] reloaded after edit")
		out.emitMemorySizeWarnings()
		return out, cmd

	case pagerDoneMsg:
		// Pager exited. The error path always lands in scrollback so
		// a missing-pager / exec-failure isn't silently swallowed.
		// Successful exits are silent — the user already saw the
		// content; an extra "pager closed" line is just noise.
		if msg.err != nil {
			m.appendLine(styleError.Render("[subagents] pager exited with error: " + msg.err.Error()))
			return m, nil
		}
		return m, nil

	case setupDoneMsg:
		return handleSetupDone(m, msg)

	case inlineOpenAIAuthURLMsg:
		return handleInlineOpenAIAuthURL(m, msg)

	case inlineOpenAIAuthDoneMsg:
		return handleInlineOpenAIAuthDone(m, msg)

	case inlineOpenAIAuthScanDoneMsg:
		return handleInlineOpenAIAuthScanDone(m, msg)

	case inlineCopilotAuthCodeMsg:
		return handleInlineCopilotAuthCode(m, msg)

	case inlineCopilotAuthDoneMsg:
		return handleInlineCopilotAuthDone(m, msg)

	case mcpServerStartedMsg:
		// Off-thread /mcp Add finished (see startMCPServerCmd) — tools are
		// already registered; just render the result lines.
		for _, line := range msg.lines {
			m.appendLine(styleAuto.Render(line))
		}
		return m, nil

	case memoryReindexDoneMsg:
		// Off-thread memory reindex finished (see memoryReindexCmd).
		style := styleAuto
		if msg.isError {
			style = styleError
		}
		for _, line := range msg.lines {
			m.appendLine(style.Render(line))
		}
		return m, nil

	case embedSetupDoneMsg:
		return m.handleEmbedSetupDone(msg)

	case modelWindowProbedMsg:
		// Background overflow probe finished (kicked off at session start
		// or by commitModelChoice). Record the attempt either way so a
		// model whose probe failed isn't re-probed on every switch.
		if m.probedModels == nil {
			m.probedModels = map[string]bool{}
		}
		m.probedModels[msg.model] = true
		if msg.window <= 0 {
			// Don't leave it a silent fallback — say so once (probedModels
			// gates re-runs), with the reason, so the user knows detection
			// ran and how to override.
			detail := msg.detail
			if detail == "" {
				detail = "no window reported"
			}
			m.appendLine(styleAuto.Render(statusWarnLine("model", fmt.Sprintf(
				"context window unknown for %s; using %s",
				shortenMiddle(msg.model, 42), formatTokens(m.contextWindow())))))
			m.appendLine(styleAuto.Render(statusHintLine(detail)))
			return m, nil
		}
		// Persist the discovered window to the file-backed store overlay
		// (~/.yottacode/context-windows.json), NOT into the user's
		// config.toml. Auto-writing config.toml's provider models used to
		// turn a free-form provider into one whose models list excluded its
		// own default_model, corrupting the config; the store is keyed by
		// model and never trips that validation. ResolveWindow reads the
		// store (via WindowFor), so the bar/threshold pick it up immediately.
		changed, err := catalog.UpsertWindow(msg.model, msg.window)
		if err != nil {
			m.appendLine(styleError.Render(statusLine("model", fmt.Sprintf("save context window: %v", err))))
			return m, nil
		}
		// Only announce a genuinely NEW (or changed) discovery. Re-probing a
		// model whose window is already cached told the user nothing new, so
		// stay quiet rather than printing the same line every session.
		if changed {
			m.appendLine(styleAuto.Render(statusLine("model", fmt.Sprintf(
				"detected context window for %s: %s", msg.model, formatTokens(msg.window)))))
		}
		return m, nil

	case resumeWatermarkCheckMsg:
		// Startup watermark pass for a resumed session (see Init).
		if ctxCmd := m.updateContextUsage(true); ctxCmd != nil {
			return m, ctxCmd
		}
		return m, nil

	case summaryDoneMsg:
		m.summarizing = false
		if msg.err != nil {
			m.summarizeFailures++
		} else {
			m.summarizeFailures = 0
		}
		for _, fb := range msg.fallbacks {
			m.appendLine(renderFallbackLine(agent.Fallback{From: fb.FallbackFrom, To: fb.FallbackTo, Reason: fb.FallbackReason, Agent: "summarize"}))
		}
		if msg.degraded && msg.err == nil {
			m.appendLine(styleAuto.Render("[summarize] fast model failed repeatedly; used smart model for compaction"))
		}
		if msg.compactionSeq != m.compactionSeq {
			// A mid-turn compaction rewrote history after summarizeCmd took
			// its clone. Discard this stale result rather than overwriting
			// the newer compacted history; clear the watermark so a future
			// boundary check can retry if context is still high.
			m.lastWatermarkPct = 0
			m.lastContextSummary = "stale result discarded after mid-turn compaction"
			return m, nil
		}
		if msg.err != nil {
			// Failed attempts don't count as "already handled at this fill
			// level" — clearing the watermark lets the next turn-end
			// retry. Without this the gate in updateContextUsage stays
			// closed forever and we silently stop attempting compression.
			m.lastWatermarkPct = 0
			m.lastContextSummary = "failed: " + msg.err.Error()
			m.appendLine(styleError.Render("[summarize] " + msg.err.Error()))
			return m, nil
		}
		// Guard the slice write under histMu: /summarize is
		// PreservesTurn=false so the turn it cancelled may still be
		// unwinding on the Turn goroutine (which appends to this same
		// field), and mid-turn compaction now writes it too. estimated
		// tokens are computed from the value we own (msg.newMessages), not
		// a re-read of the field, so no lock is needed for that.
		m.histMu.Lock()
		m.sess.Messages = msg.newMessages
		m.histMu.Unlock()
		// Refresh estimated tokens from the new history so the status
		// bar reflects the compaction immediately.
		m.contextTokens = m.estimatedContextTokens(msg.newMessages)

		// Non-convergence guard: when the compacted history is STILL at
		// or above the auto threshold, the irreducible floor (system
		// prompt + the summary itself + the retained recent turns)
		// exceeds what this window holds. Re-firing auto-summarize every
		// turn would burn a multi-minute compression that never gets under
		// the line — but latching lastWatermarkPct high (the old behavior)
		// disabled auto for the ENTIRE session, so the user had to run
		// /summarize by hand forever. Instead re-arm lastWatermarkPct and
		// record the non-convergence per (fill, window): the gate in
		// updateContextUsage suppresses only re-runs at ~this fill, and
		// releases once fill grows or the window changes.
		window := m.contextWindow()
		converged := summaryConverged(m.contextTokens, window, m.fileCfg.Context.AutoThreshold)
		nonConvergentPct := 0.0
		m.lastWatermarkPct = 0
		if converged || window <= 0 {
			m.nonConvergentAt = 0
			m.nonConvergentWindow = 0
		} else {
			nonConvergentPct = float64(m.contextTokens) / float64(window)
			m.nonConvergentAt = nonConvergentPct
			m.nonConvergentWindow = window
		}

		if m.subagentTasks != nil {
			m.sess.SubagentTasks = m.subagentTasks.Export()
		}
		if err := m.sess.Save(); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("⚠ session save after summarize: %v", err)))
		}
		// Index the compressed session so /recall continues to surface
		// summary content from the now-shorter history.
		if m.recall != nil {
			if err := m.recall.IndexSession(m.sess); err != nil {
				m.appendLine(styleError.Render(fmt.Sprintf("⚠ recall index after summarize: %v", err)))
			}
			m.embedCurrentSessionAsync()
		}
		switch {
		case !converged:
			culprit, culpritTok := m.dominantContextBucket()
			m.lastContextSummary = fmt.Sprintf("non-convergent at %d%%; largest bucket %s (~%s)", int(nonConvergentPct*100), culprit, formatTokens(culpritTok))
			m.appendLine(styleWatermarkBox.Render(fmt.Sprintf(
				"⚡ Summarized, but context is still at %d%% — the biggest consumer is %s (~%s), which alone won't fit under the limit, so auto-summarize is paused to avoid re-running every turn (it resumes if context grows further or you switch models).\nFree space with /clear, disable unused MCP tools, set a larger context_window for this model, or switch to a bigger-window model.\nFull history saved to %s",
				int(nonConvergentPct*100), culprit, formatTokens(culpritTok), abbrevHome(msg.snapshotPath))))
		case msg.auto:
			m.lastContextSummary = fmt.Sprintf("auto summarized %s → %s", formatTokens(msg.tokensBefore), formatTokens(m.contextTokens))
			m.appendLine(styleWatermarkBox.Render(fmt.Sprintf(
				"⚡ Context auto-summarized.\nFull history saved to %s\nUse /recall <id> to search the compressed session.",
				abbrevHome(msg.snapshotPath))))
		default:
			m.lastContextSummary = fmt.Sprintf("manual summarized %s → %s", formatTokens(msg.tokensBefore), formatTokens(m.contextTokens))
			m.appendLine(styleAuto.Render(fmt.Sprintf(
				"[summarize] compressed history (~%d → ~%d tokens). Snapshot: %s",
				msg.tokensBefore, m.contextTokens, abbrevHome(msg.snapshotPath))))
		}
		if len(m.pendingSubagentWakes) > 0 {
			// A notify_on_done completion may have arrived while summarization
			// was running. Start it only after the compacted session has been
			// saved and recall-indexed so the two flows cannot race writes.
			next, cmd := m.startSubagentWakeTurn()
			return next, cmd
		}
		return m, nil
	}

	if !m.turnActive {
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// anyOverlayOpen reports whether a full-screen inline overlay is open —
// the set of flags that make View() short-circuit into renderInlineOverlay
// (a single panel that replaces the whole footer). Keep this in sync with
// the early-return chain at the top of View. The slash/file palettes are
// deliberately excluded: they render inline *above* the cmdline within the
// normal footer, not as a replacing overlay, so they don't drive the
// over-tall collapse this guards.
func (m Model) anyOverlayOpen() bool {
	return m.cheatsheetOpen || m.loopListOpen || m.usageOpen || m.contextReportOpen ||
		m.permissionsOpen || m.modelPickerOpen || m.providerPickerOpen ||
		m.embedSetupOpen || m.memoryPickerOpen || m.recallPickerOpen || m.sessionsPickerOpen ||
		m.plansPickerOpen || m.checkpointsPickerOpen || m.subagentsPickerOpen ||
		m.routerPickerOpen || m.themePickerOpen || m.effortPickerOpen || m.skillsMenuOpen ||
		m.skillsPickerOpen || m.mcpPickerOpen
}

// overlayClosed reports whether the transition from this model (pre-update)
// to `after` closed a full-screen inline overlay — open before, gone after.
func (m Model) overlayClosed(after Model) bool {
	return m.anyOverlayOpen() && !after.anyOverlayOpen()
}

// hasRunningSubagents reports whether any subagent task is currently
// running. Used to keep the spinner tick alive (driving live-dock redraws)
// after the parent turn ends, since a background dispatch detaches its
// workers and nothing else would trigger a redraw until they finish.
func (m Model) hasRunningSubagents() bool {
	return m.subagentTasks != nil && m.subagentTasks.ActiveCount() > 0
}

func (m Model) skillsBusy() bool {
	return (m.skillsPickerOpen && m.skillsPicker != nil && m.skillsPicker.busy != "") ||
		(m.skillsMenuOpen && m.skillsMenu != nil && m.skillsMenu.busy != "")
}

// View renders the live footer that redraws in place at the bottom of the
// terminal. Conversation history is NOT in here — those lines live in
// terminal scrollback courtesy of tea.Println from appendLine.
func (m Model) View() string {
	if !m.ready {
		return "initializing…"
	}

	// Inline-overlay layout shared by every picker (cheatsheet, model,
	// provider, memory): cmdline at the top so the user keeps visual
	// anchoring on the input frame, status bar directly below it (the
	// `● model · provider · cwd · mem · tokens` line), a thin
	// separator rule, then the picker itself. Pickers own all
	// keystrokes while open — the cmdline still renders but won't
	// receive input — so the user can read context (current cwd,
	// model, token count) without leaving the picker.
	if m.cheatsheetOpen {
		return m.renderInlineOverlay(renderCheatsheet(m.width))
	}
	if m.loopListOpen && m.activeLoopCount() > 0 {
		return m.renderInlineOverlay(m.renderLoopListPanel())
	}
	if m.usageOpen {
		return m.renderInlineOverlay(m.usagePanel)
	}
	if m.contextReportOpen {
		return m.renderInlineOverlay(m.contextReportBody)
	}
	if m.permissionsOpen {
		return m.renderInlineOverlay(renderPermissionsOverlay(m))
	}
	if m.modelPickerOpen && m.modelPicker != nil {
		return m.renderInlineOverlay(renderModelPicker(m.modelPicker, m.width))
	}
	if m.providerPickerOpen && m.providerPicker != nil {
		return m.renderInlineOverlay(renderProviderPicker(m.providerPicker, m.width))
	}
	if m.embedSetupOpen {
		return m.renderInlineOverlay(m.renderEmbedSetup())
	}
	if m.memoryPickerOpen && m.memoryPicker != nil {
		return m.renderInlineOverlay(renderMemoryPicker(m.memoryPicker, m.width))
	}
	if m.recallPickerOpen && m.recallPicker != nil {
		return m.renderInlineOverlay(renderRecallPicker(m.recallPicker, m.width))
	}
	if m.sessionsPickerOpen && m.sessionsPicker != nil {
		return m.renderInlineOverlay(renderSessionsPicker(m.sessionsPicker, m.width))
	}
	if m.plansPickerOpen && m.plansPicker != nil {
		return m.renderInlineOverlay(renderPlansPicker(m.plansPicker, m.width))
	}
	if m.checkpointsPickerOpen && m.checkpointsPicker != nil {
		return m.renderInlineOverlay(renderCheckpointsPicker(m.checkpointsPicker, m.width))
	}
	if m.subagentsPickerOpen && m.subagentsPicker != nil {
		return m.renderInlineOverlay(renderSubagentsPicker(m.subagentsPicker, m.width))
	}
	if m.routerPickerOpen && m.routerPicker != nil {
		return m.renderInlineOverlay(renderRouterPicker(m.routerPicker))
	}
	if m.themePickerOpen && m.themePicker != nil {
		return m.renderInlineOverlay(renderThemePicker(m.themePicker, m.width))
	}
	if m.effortPickerOpen && m.effortPicker != nil {
		return m.renderInlineOverlay(renderEffortPicker(m.effortPicker))
	}
	if m.skillsMenuOpen && m.skillsMenu != nil {
		return m.renderInlineOverlay(renderSkillsMenu(m.skillsMenu, m.width))
	}
	if m.skillsPickerOpen && m.skillsPicker != nil {
		return m.renderInlineOverlay(renderSkillsPicker(m.skillsPicker, m.width))
	}
	if m.mcpPickerOpen && m.mcpPicker != nil {
		return m.renderInlineOverlay(renderMCPPicker(m.mcpPicker, m.width))
	}

	parts := []string{}
	// During an active turn the live footer carries: a leading blank row
	// (separates the live area from the user's just-emitted message in
	// scrollback), the live reasoning preview if the model is thinking,
	// the streaming content preview if the model is writing, the
	// Thinking… indicator, and another blank row before the input box
	// (lifts the indicator off the input so it reads as its own line,
	// not stuck to the cmdline).
	if m.turnActive {
		parts = append(parts, "")
	}
	if preview := m.renderReasoningPreview(); preview != "" {
		parts = append(parts, preview)
	}
	if m.streamingMode == streamContent {
		// Inside a code block: show a quiet "writing code" notice in
		// the footer (no live feed of the lines, since they'll land
		// highlighted in scrollback at the closing fence). Outside,
		// show the trailing partial prose line, truncated to one
		// terminal row.
		if m.inCodeBlock {
			parts = append(parts, m.renderCodeBlockNotice())
		} else if m.inTable {
			parts = append(parts, m.renderTableNotice())
		} else if preview := m.renderStreamingPreview(); preview != "" {
			parts = append(parts, preview)
		}
	}
	if card := m.renderLivePlanCard(); card != "" {
		// Trailing blank gives the card breathing room from the
		// thinking row below (during a turn) or the input box
		// (between turns). Without it the card's `└ plan updated`
		// footer sits flush against the spinner row.
		parts = append(parts, card, "")
	}
	if m.turnActive {
		parts = append(parts, m.renderThinkingRow(), "")
	} else if m.summarizing {
		parts = append(parts, m.renderSummarizingRow(), "")
	}

	if m.awaitingPathTrust {
		// Prompt 2: inline path-trust elevation modal. Mutually
		// exclusive with the regular approval modal — only one of
		// awaitingPathTrust / awaitingApproval is ever true.
		parts = append(parts, renderPathTrustModal(m))
	} else if m.awaitingApproval {
		// The plan-mode boundary tools get their own decision cards —
		// just the hotkeys in a bordered box, never the generic
		// always-allow modal. For exit, the plan body itself is
		// emitted to scrollback above the box when ApprovalNeeded
		// fires (see handleAgentEvent), so the body persists naturally
		// after the modal dismisses and isn't cramped inside the box.
		switch m.approvalTool {
		case "exit_plan_mode":
			parts = append(parts, renderPlanApprovalCard(m.width))
		case "enter_plan_mode":
			parts = append(parts, renderEnterPlanApprovalCard(m.width))
		default:
			parts = append(parts, renderApprovalModal(m))
		}
	} else if m.loopExitConfirmOpen {
		parts = append(parts, renderLoopExitConfirm(m))
	} else {
		if m.paletteOpen {
			parts = append(parts, renderPalette(m.paletteFiltered, m.paletteIndex, m.paletteOffset, liveContentWidth(m.width)+4))
		}
		if m.filePaletteOpen {
			parts = append(parts, renderFilePalette(m.filePaletteFiltered, m.filePaletteIndex, m.filePaletteOffset, liveContentWidth(m.width)+4))
		}
		// Banner: one-line indicator above the input rule. Modes
		// (plan/auto) are mutually exclusive; yolo mode is an
		// orthogonal overlay flag whose `⚠ yolo mode` tag appends to
		// the active mode banner. When no mode is active but yolo
		// mode is, a standalone yolo banner shows so the user can
		// still see the "rails off" state. Suppressed entirely while a
		// palette is open (palettes already own the above-cmdline real
		// estate).
		yoloOn := m.cfg.YoloMode.IsActive()
		switch {
		case m.paletteOpen, m.filePaletteOpen:
			// suppressed
		case m.cfg.PlanMode.IsActive() && m.cfg.PlanMode.PlanFile != "":
			// Plan-mode banner is gated behind "the user has submitted
			// their first message in this plan session" — PlanFile is
			// empty between /plan-entry and the first user prompt, then
			// set by maybeFillPlanFile or the resume picker. Hiding
			// the banner during that window avoids stacking it
			// immediately below the entry log's "plan mode active —
			// read-only research…" line, which scanned as a duplicate.
			parts = append(parts, renderPlanModeBanner(computePlanBannerInfo(m), yoloOn, m.width))
		case m.cfg.AutoMode.IsActive():
			parts = append(parts, renderAutoModeBanner(yoloOn, m.width))
		case yoloOn:
			parts = append(parts, renderYoloStandaloneBanner(m.width))
		}
		// Loop banner is additive — a /loop can be armed in any mode, so it
		// stacks below the mode banner (if any) rather than replacing it.
		// Suppressed with the mode banners while a palette owns the
		// above-cmdline space.
		if m.activeLoopCount() > 0 && !m.paletteOpen && !m.filePaletteOpen {
			parts = append(parts, renderLoopBanner(m.loopBannerStates(), m.width))
		}
		parts = append(parts, m.renderInputFrame())
	}

	// Status bar sits directly below the cmdline. Earlier versions
	// inserted a blank line for breathing room — turned out to leave the
	// bar looking detached and floating; cuddling it against the rule
	// reads as "this is the chrome below the cmdline" without ambiguity.
	parts = append(parts, m.renderStatus())

	// Live subagent dock: rendered BELOW the status bar (the status bar is
	// the session's own chrome; the dock is the per-subagent fan-out under
	// it). Lists currently-running subagents (a dispatch fan-out, or any
	// in-flight Agent run) with model + context + activity, updating on
	// each spinner tick. Collapses to nothing when none are running, so it
	// costs zero rows on an idle/solo session.
	if m.subagentTasks != nil {
		if dock := renderSubagentDock(m.subagentTasks.List(), m.width, m.modelName, m.dockFocused, m.dockCursor); dock != "" {
			// Blank line between the status bar and the dock so the
			// per-subagent fan-out reads as its own region.
			parts = append(parts, "", dock)
			// Trailing blank line so the last dock row doesn't sit flush
			// against the terminal bottom (prevents cutoff on some terminals).
			parts = append(parts, "")
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderInlineOverlay stacks an inline picker (cheatsheet, model,
// provider, memory) below the cmdline + status bar with a thin
// separator rule between them. Layout:
//
//	[cmdline]
//	[status bar]
//	───────────────…
//	[overlay body]
//
// The overlay owns input while open; renderInputBox is included only
// for visual anchoring (so the user keeps sight of the input frame
// they're going to return to). The separator width tracks the
// terminal so the rule reads as "this is a different surface from
// the chat above" without floating mid-screen.
func (m Model) renderInlineOverlay(body string) string {
	width := m.width
	if width < 4 {
		width = 4
	}
	overlayRule := styleOverlayRule.Render(strings.Repeat("─", width))
	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderInputFrame(),
		m.renderStatus(),
		overlayRule,
		body,
	)
}

// renderInputBox renders the cmdline as a borderless input row capped at
// `min(120, terminalWidth - 4)`. We render the value ourselves rather
// than using m.textInput.View() because Bubbles textarea sizes Height by
// *logical* lines, not wrapped visual rows — long single-line input
// either gets clipped or padded. Wrapping with ansi.Hardwrap and
// rendering each row with a prompt-or-indent prefix gets us the "wrap
// below within the cap" behavior the cmdline actually wants. The textarea
// still owns the value and cursor state via its own Update; we just
// paint it differently.
func (m Model) renderInputBox() string {
	if m.width <= 0 {
		return m.textInput.View()
	}
	contentW := inputContentWidth(m.width)
	return m.renderInputBody(contentW)
}

// renderInputFrame wraps the cmdline body in a closed bordered box with
// rounded corners (┌/┐/└/┘) and side borders (│) that spans the full
// terminal width. The border uses colorDim (mid-gray) so the frame reads
// brightly — same visual family as the welcome card border.
func (m Model) renderInputFrame() string {
	if m.width <= 0 {
		return m.textInput.View()
	}
	ruleStyle := lipgloss.NewStyle().Foreground(colorDim)
	boxW := m.width
	innerW := boxW - 4 // space inside "│ " ... " │"
	if innerW < 1 {
		innerW = 1
	}
	top := ruleStyle.Render("┌" + strings.Repeat("─", boxW-2) + "┐")
	bot := ruleStyle.Render("└" + strings.Repeat("─", boxW-2) + "┘")
	body := m.renderInputBody(innerW)
	border := ruleStyle.Render("│")
	var bordered []string
	for _, row := range strings.Split(body, "\n") {
		visW := ansi.StringWidth(row)
		pad := innerW - visW
		if pad < 0 {
			pad = 0
		}
		bordered = append(bordered, border+" "+row+strings.Repeat(" ", pad)+" "+border)
	}
	return top + "\n" + strings.Join(bordered, "\n") + "\n" + bot
}

// renderInputRule paints a single horizontal `─` line spanning the full
// terminal width. Used by the inline overlay layout where the full-width
// divider is still appropriate. colorDim (mid-gray) so it reads as
// brightly as the cmdline box frame it stands in for.
func (m Model) renderInputRule() string {
	w := m.width
	if w < 1 {
		w = 1
	}
	return lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", w))
}

// renderEmptyCursor returns a single-cell cursor block for the empty
// input row. When `visible` is true the cell is reverse-video so it
// reads as a typical block cursor; when false a plain space holds the
// position so the placeholder/hints don't shift across blink phases.
func renderEmptyCursor(visible bool) string {
	if !visible {
		return " "
	}
	return lipgloss.NewStyle().Reverse(true).Render(" ")
}

// renderInputBody is the inside of the input box: prompt + wrapped value
// with a visible cursor block at the cursor position. contentW is the
// inner width of the box (terminal width minus border+padding).
func (m Model) renderInputBody(contentW int) string {
	const promptStr = "❯ "
	const maxRows = 6
	promptW := lipgloss.Width(promptStr)
	wrapW := contentW - promptW
	if wrapW < 1 {
		wrapW = 1
	}
	indent := strings.Repeat(" ", promptW)

	val := m.textInput.Value()
	if val == "" {
		// Empty state: chevron, blinking cursor block, dim italic
		// placeholder, and (until the user has sent a turn) the
		// onboarding hints inlined on the same row. The cursor block
		// is fixed at column 0 so the placeholder doesn't shift as
		// the cursor toggles visible/invisible.
		cur := renderEmptyCursor(m.cursorVisible)
		placeholder := "ask anything…"
		if m.cfg.PlanMode.IsActive() {
			// Surface the mode in the placeholder too — even if the
			// status bar / above-cmdline banner are hidden by a
			// narrow terminal or palette overlay, the empty-state
			// prompt will still call it out.
			placeholder = "ask anything… (plan mode)"
		}
		ph := styleInputPlaceholder.Render(placeholder)
		row := styleInputPrompt.Render(promptStr) + cur + ph
		if !m.firstMessageSent {
			row += styleInputHint.Render("    /  commands  ·  @ files  ·  ↑↓ history")
		}
		return row
	}

	// Compute cursor's logical (logicalRow, logicalCol) — column in the
	// cursor's logical line, derived from textarea LineInfo.
	cursorLogicalRow := m.textInput.Line()
	info := m.textInput.LineInfo()
	cursorLogicalCol := info.StartColumn + info.ColumnOffset

	// Wrap each \n-separated logical line to wrapW. Track which logical
	// row each visual row came from (so we know when to draw a fresh
	// prompt vs. indent) and the char offset where the visual row starts.
	type vrow struct {
		text      string
		logical   int
		startChar int
	}
	var rows []vrow
	for li, line := range strings.Split(val, "\n") {
		if line == "" {
			rows = append(rows, vrow{logical: li})
			continue
		}
		wrapped := ansi.Hardwrap(line, wrapW, true)
		offset := 0
		for _, r := range strings.Split(wrapped, "\n") {
			rows = append(rows, vrow{text: r, logical: li, startChar: offset})
			offset += len([]rune(r))
		}
	}

	// Find which visual row the cursor is on.
	cursorVisRow := -1
	for i, r := range rows {
		if r.logical != cursorLogicalRow {
			continue
		}
		end := r.startChar + len([]rune(r.text))
		if cursorLogicalCol >= r.startChar && cursorLogicalCol <= end {
			cursorVisRow = i
		}
	}

	// Cap visible rows at maxRows; scroll so cursor stays in view.
	start := 0
	if len(rows) > maxRows {
		start = cursorVisRow - (maxRows - 1)
		if start < 0 {
			start = 0
		}
		if start+maxRows > len(rows) {
			start = len(rows) - maxRows
		}
		rows = rows[start : start+maxRows]
	}

	out := make([]string, 0, len(rows))
	for i, r := range rows {
		first := i == 0 || rows[i-1].logical != r.logical
		var prefix string
		if first {
			prefix = styleInputPrompt.Render(promptStr)
		} else {
			prefix = indent
		}
		text := r.text
		if (i + start) == cursorVisRow {
			col := cursorLogicalCol - r.startChar
			text = insertCursor(text, col, m.cursorVisible)
		}
		out = append(out, prefix+text)
	}
	return strings.Join(out, "\n")
}

// insertCursor returns row with a cursor block at rune index col. The
// block is reverse-video when `visible` is true and a plain pass-through
// when false — across blink phases the row keeps the same width either
// way (the trailing space is appended in both branches when col is past
// the row's end), so the placeholder/content above doesn't shift on
// each tick.
func insertCursor(row string, col int, visible bool) string {
	rs := []rune(row)
	atEnd := col >= len(rs)
	if !visible {
		if atEnd {
			return row + " "
		}
		return row
	}
	cur := lipgloss.NewStyle().Reverse(true)
	if atEnd {
		return row + cur.Render(" ")
	}
	if col < 0 {
		col = 0
	}
	return string(rs[:col]) + cur.Render(string(rs[col])) + string(rs[col+1:])
}

// inputContentWidth caps the input row's wrap width at min(120, w-4). On
// a wide terminal the full-width input reads as an empty runway; capping
// it at 120 columns focuses attention on the typed text and matches the
// width the conversation surface uses for prose. The -4 keeps a column
// of breathing room on either side of the chevron + content.
func inputContentWidth(terminalWidth int) int {
	w := terminalWidth - 4
	if w > 120 {
		w = 120
	}
	if w < 1 {
		w = 1
	}
	return w
}

// liveContentWidth is the inner content width for a bordered live-frame
// element given the current terminal width: terminal width minus the box's
// border (2) and padding (2). Deliberately UNCAPPED: its callers (the
// textarea, the slash/file palettes, the streaming preview) are chrome
// that must share a right edge with the full-width input frame directly
// below them — a 120-capped palette over a full-width cmdline box reads
// as a rendering bug (user call, 2026-06-10). The Phase 6 120-column cap
// applies to content surfaces (tool cards, prose) and true modals, which
// cap at their own call sites.
func liveContentWidth(terminalWidth int) int {
	w := terminalWidth - 4
	if w < 1 {
		w = 1
	}
	return w
}

// renderReasoningPreview returns the live chain-of-thought text for the
// in-progress turn, capped to the most recent 6 lines so a verbose
// reasoning summary doesn't push the input box off the screen. Each
// line is rendered in faint italic muted style — present enough to
// follow the model's thinking but visibly subordinate to the answer.
// Returns "" when there's nothing to preview.
func (m Model) renderReasoningPreview() string {
	if m.reasoning.Len() == 0 {
		return ""
	}
	const maxLines = 6
	body := strings.TrimRight(m.reasoning.String(), "\n")
	lines := strings.Split(body, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return styleTurnFooter.Render(strings.Join(lines, "\n"))
}

// renderCodeBlockNotice is the in-progress indicator shown in the live
// footer while a code block is mid-stream. Buffered code doesn't go to
// scrollback line-by-line — it's collected until the closing ``` fence
// and then emitted highlighted. The notice keeps the user informed of
// progress (line count + language tag) without the line-by-line
// scroll that would lose the eventual highlighting.
func (m Model) renderCodeBlockNotice() string {
	lines := strings.Count(m.codeBlockBuf.String(), "\n")
	// Include any partial trailing line currently in m.streaming.
	if m.streaming.Len() > 0 {
		lines++
	}
	lang := m.codeBlockLang
	if lang == "" {
		lang = "code"
	}
	return styleTurnFooter.Render(fmt.Sprintf("…writing %s (%d lines, will format on close)", lang, lines))
}

func (m Model) renderTableNotice() string {
	rows := strings.Count(m.tableBuf.String(), "\n")
	if m.streaming.Len() > 0 {
		rows++
	}
	return styleTurnFooter.Render(fmt.Sprintf("…formatting table (%d rows, will render on close)", rows))
}

// renderStreamingPreview returns the trailing partial prose line capped
// to one terminal row. The model emits content incrementally; without
// hard line breaks the in-flight buffer can grow past the terminal
// width and wrap to multiple visual rows. Bubbletea's inline-mode
// renderer counts logical lines, not wrapped rows, so when the buffer
// commits and the live footer shrinks the renderer leaves the
// previously-wrapped rows on screen as ghost text below the
// scrollback emit — looks like the response is duplicated. Capping
// the preview to one row keeps the live footer's visual height
// matching its logical height.
//
// When the buffer exceeds the available width we show the trailing
// portion of the text (the most recent content) prefixed with "…".
func (m Model) renderStreamingPreview() string {
	s := m.streaming.String()
	if s == "" {
		return ""
	}
	width := liveContentWidth(m.width)
	if width <= 1 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return "…" + string(runes[len(runes)-(width-1):])
}

// renderThinkingRow is the live thinking indicator above the input, redrawn
// every spinner tick. Empty when idle.
func (m Model) renderThinkingRow() string {
	if !m.turnActive {
		return ""
	}
	indicator := styleSpinner.Render(m.renderTurnStatus())
	hint := lipgloss.NewStyle().Foreground(colorMuted).Render(" · Ctrl+C to cancel")
	return indicator + hint
}

// renderSummarizingRow is the live indicator shown between turns while a
// summarization (manual /summarize or auto) runs. Mirrors the thinking
// row — same spinner glyph and dim style — so the multi-minute
// compression no longer looks like a frozen UI. Indeterminate by design:
// summarization is one streaming call whose length isn't known ahead of
// time, so we show a spinner + elapsed time rather than a fake progress
// bar. Empty when not summarizing.
func (m Model) renderSummarizingRow() string {
	if !m.summarizing {
		return ""
	}
	status := fmt.Sprintf("%s summarizing context… %s",
		m.spinner.View(), formatDuration(time.Since(m.summarizeStart)))
	return styleSpinner.Render(status)
}

// renderLivePlanCard returns the in-flight todo card for the live
// frame, or "" when there is no plan to show. Re-rendered every
// View() tick — when an agent.TodoUpdate event flips a status,
// View() reads the new m.livePlan and the card content changes in
// place, without anything new landing in scrollback. The matching
// snapshot lands in scrollback at TurnDone (see the TurnDone case
// in handleAgentEvent) so the user gets one historical receipt per
// turn that touched the plan.
//
// Persists across turns: once seeded (either by an agent.TodoUpdate
// or by resume via livePlanInit), the card stays in the live frame
// until the agent calls todo_write with an empty list, which sets
// m.livePlan to nil and the card silently disappears.
func (m Model) renderLivePlanCard() string {
	if len(m.livePlan) == 0 {
		return ""
	}
	return renderTodoCardFromTodos(m.livePlan, m.width)
}

// renderToolStartLine produces the "▸ <tool> ..." line that lands in
// scrollback when a tool fires. Plain pass-through — there's no
// per-call annotation today.
func renderToolStartLine(toolName, preview string) string {
	_ = toolName
	return styleToolCall.Render("▸ " + preview)
}

// renderFallbackLine formats an agent.Fallback as a single warm-yellow
// line in scrollback so the user can see clearly when one provider
// degraded and the router took over. Silent fallback would hide a real
// provider issue; the design's whole point is to surface it loudly.
func renderFallbackLine(e agent.Fallback) string {
	head := "↻ fallback"
	if e.Agent != "" {
		head += " [" + e.Agent + "]"
	}
	head += fmt.Sprintf(": %s → %s", e.From, e.To)
	if e.Policy != "" {
		head += " [" + e.Policy + "]"
	}
	if e.Reason != "" {
		head += ": " + e.Reason
	}
	return lipgloss.NewStyle().Foreground(colorWarm).Bold(true).Render(head)
}

func renderProviderToolCard(toolName, phase, detail string, termWidth int) string {
	width := cardMaxWidthCap
	if termWidth > 0 && termWidth-4 < width {
		width = termWidth - 4
	}
	if width < cardMinUsefulCols {
		width = cardMinUsefulCols
	}

	// Provider-hosted tools are lifecycle events, not local yottacode tool
	// calls with args/output. Render them with the same modern card gutters as
	// local tools, but keep the copy terse and action-oriented. Neutral gutter
	// + no duration tag: there's no pass/fail result or measured elapsed to
	// tint or time here.
	header := renderCardHeader(providerToolHeader(toolName), neutralGutter(), 0, width)
	body := providerToolBody(toolName, phase, detail)
	gutter := styleCardGutter.Render("│ ")
	gutterWidth := ansi.StringWidth(gutter)
	bodyWidth := width - gutterWidth
	if bodyWidth < 20 {
		bodyWidth = 20
	}

	out := []string{header}
	appendBodyLine := func(line string) {
		styled := styleCardBody.Render(line)
		if ansi.StringWidth(styled) <= bodyWidth {
			out = append(out, gutter+styled)
			return
		}
		wrapped := ansi.Wrap(styled, bodyWidth, "")
		for _, row := range strings.Split(wrapped, "\n") {
			out = append(out, gutter+row)
		}
	}
	for _, line := range body {
		appendBodyLine(line)
	}
	footer := styleCardMeta.Render(providerToolFooter(toolName, phase))
	return strings.Join(append(out, styleCardGutter.Render("└ ")+footer), "\n")
}

func providerToolBody(toolName, phase, detail string) []string {
	line := providerToolActivity(toolName, phase)
	if detail == "" {
		return []string{line}
	}
	return []string{line, detail}
}

func providerToolActivity(toolName, phase string) string {
	status := providerToolStatus(phase)
	switch strings.TrimSpace(toolName) {
	case "web_search":
		if status == "done" {
			return "web search complete"
		}
		return "searching the web…"
	case "x_search":
		if status == "done" {
			return "X search complete"
		}
		return "searching X…"
	case "code_interpreter":
		if status == "done" {
			return "code interpreter complete"
		}
		return "running code interpreter…"
	default:
		if status == "done" {
			return "hosted tool complete"
		}
		return "hosted tool " + status + "…"
	}
}

func providerToolHeader(toolName string) string {
	switch strings.TrimSpace(toolName) {
	case "web_search":
		return "xAI Web Search"
	case "x_search":
		return "xAI X Search"
	case "code_interpreter":
		return "xAI Code Interpreter"
	case "":
		return "Hosted tool"
	default:
		return fmt.Sprintf("Hosted(%s)", toolName)
	}
}

func providerToolStatus(phase string) string {
	switch strings.TrimSpace(phase) {
	case "completed":
		return "done"
	case "":
		return "running"
	default:
		return phase
	}
}

func providerToolFooter(toolName, phase string) string {
	status := providerToolStatus(phase)
	switch strings.TrimSpace(toolName) {
	case "web_search":
		return "hosted web search"
	case "x_search":
		return "hosted X search"
	case "code_interpreter":
		return "hosted code interpreter"
	}
	if status == "done" {
		return "provider tool completed"
	}
	return "provider tool " + status
}

func renderCitations(citations []adapter.Citation) string {
	if len(citations) == 0 {
		return ""
	}
	lines := []string{"sources:"}
	for _, c := range citations {
		if label := citationLabel(c); label != "" {
			lines = append(lines, "  - "+label)
		}
	}
	if len(lines) == 1 {
		return ""
	}
	return styleTurnFooter.Render(strings.Join(lines, "\n"))
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

func formatDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, (s%3600)/60)
}

func summarizeToolOutput(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return "(no output)"
	}
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	if len(out) > 100 {
		out = out[:100] + "…"
	}
	return out
}

func abbrevHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// lastPathSegments returns the final n components of a slash path joined
// by "/", e.g. lastPathSegments("/home/me/go/src/foo/bar", 2) == "foo/bar".
// A path with n or fewer real components returns all of them; an empty
// path or the root returns "". The status-bar location chip uses it to
// show the working directory compactly — the full absolute path is too
// wide for the footer, but the trailing "foo/bar" is enough to orient
// which project you're in. Home abbreviation isn't applied: two trailing
// segments are already short, and a leading "~" would just consume one.
func lastPathSegments(p string, n int) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	kept := make([]string, 0, strings.Count(p, "/")+1)
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	if n > 0 && n < len(kept) {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "/")
}

// pasteThreshold is the byte count above which a bracketed paste gets
// hidden behind a placeholder marker instead of being inserted into the
// input box verbatim. Tuned to "would this paste obviously stretch the
// cmdline beyond a couple of lines?": ~200 chars is roughly 2 wrapped
// lines on an 80-col terminal, which feels like the right cutoff
// between "small enough to read inline" and "annoying."
const pasteThreshold = 200

// normalizePasteLineBreaks rewrites the CR and CRLF line breaks of a
// bracketed paste to LF. Terminals transmit paste line breaks as
// carriage returns, so a multi-line paste arrives "\r"-separated: raw
// CRs slip past every '\n' check downstream and, once submitted, each
// CR returns the terminal cursor to column 0 when the transcript echoes
// the message — every pasted line overprints the previous one into an
// unreadable chimera. Applied once at the KeyMsg boundary (update)
// before any paste routing.
func normalizePasteLineBreaks(rs []rune) []rune {
	if !strings.ContainsRune(string(rs), '\r') {
		return rs
	}
	s := strings.ReplaceAll(string(rs), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return []rune(s)
}

// handleLargePaste swaps a big bracketed paste for a short placeholder
// marker. The original content is stashed in m.pastes keyed by the
// marker text; expandPastes() puts it back on submit.
//
// Image file paths are detected by extension and loaded eagerly — the
// marker reads "[Image: filename.png]" and the decoded bytes are
// stashed in m.pastedImages for attachment on submit.
func (m Model) handleLargePaste(msg tea.KeyMsg) (Model, tea.Cmd) {
	content := string(msg.Runes)
	m.pasteSeq++
	lines := strings.Count(content, "\n") + 1
	marker := fmt.Sprintf("[Pasted text #%d: %d lines, %d bytes]", m.pasteSeq, lines, len(content))
	if m.pastes == nil {
		m.pastes = map[string]string{}
	}
	m.pastes[marker] = content
	syn := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(marker)}
	m.preGrowTextarea()
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(syn)
	m.fitTextareaHeight()
	return m, cmd
}

var pasteImageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// tryImagePaste checks whether the pasted content is a path or file://
// URL pointing to an image file. If so it reads the file, stashes the
// decoded bytes in m.pastedImages, and returns the compact marker.
// Returns ("", false) when the paste is not an image path.
func (m *Model) tryImagePaste(content string) (string, bool) {
	path := strings.TrimSpace(content)
	if strings.ContainsRune(path, '\n') {
		return "", false
	}
	path = fileURLToPath(path)
	ext := strings.ToLower(filepath.Ext(path))
	mediaType, ok := pasteImageExts[ext]
	if !ok {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	const maxPasteImageBytes = 20 * 1024 * 1024
	if info.Size() > maxPasteImageBytes {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	m.pasteSeq++
	marker := fmt.Sprintf("[Image #%d: %s]", m.pasteSeq, filepath.Base(path))
	if m.pastedImages == nil {
		m.pastedImages = map[string]adapter.ImageBlock{}
	}
	m.pastedImages[marker] = adapter.ImageBlock{Data: data, MediaType: mediaType}
	return marker, true
}

// fileURLToPath converts a file:// URL to a local path, decoding
// percent-encoded characters. Returns the input unchanged when it
// doesn't start with file://.
func fileURLToPath(s string) string {
	if !strings.HasPrefix(s, "file://") {
		return s
	}
	s = strings.TrimPrefix(s, "file://")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi := unhex(s[i+1])
			lo := unhex(s[i+2])
			if hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}

// expandPastes swaps any placeholder markers present in s for their
// stashed full content. No-op when there are no markers — safe to call
// on every submission. Image markers are left in place — they serve as
// readable labels for the model ("I pasted [Image #1: photo.png]").
func (m Model) expandPastes(s string) string {
	if len(m.pastes) == 0 {
		return s
	}
	for marker, content := range m.pastes {
		s = strings.ReplaceAll(s, marker, content)
	}
	return s
}

// collectPastedImages returns all image blocks stashed during paste
// handling and clears the stash. Called on submit alongside
// expandPastes.
func (m *Model) collectPastedImages() []adapter.ImageBlock {
	if len(m.pastedImages) == 0 {
		return nil
	}
	imgs := make([]adapter.ImageBlock, 0, len(m.pastedImages))
	for _, img := range m.pastedImages {
		imgs = append(imgs, img)
	}
	m.pastedImages = nil
	return imgs
}

const maxTextareaLines = 6

// preGrowTextarea expands the textarea to its max height before the next
// Update inserts a rune. Without this, when a keystroke wraps content to
// a new visual row, the textarea's internal repositionView (called inside
// Update) sees the cursor at row N+1 with viewport.Height = N and scrolls
// down by 1 — which leaves the chevron row off the top of the viewport
// even after fitTextareaHeight grows height afterward, because SetHeight
// does not reset viewport YOffset. Pre-growing means the cursor's new row
// is always within [YOffset, YOffset+Height-1] so no scroll happens.
func (m *Model) preGrowTextarea() {
	if m.textInput.Height() < maxTextareaLines {
		m.textInput.SetHeight(maxTextareaLines)
	}
}

// fitTextareaHeight grows the input area to fit explicit multi-line input
// (Ctrl+J inserts a real \n). Capped at 6 rows; once capped, the textarea's
// own viewport scrolls to keep the cursor row in view.
//
// We deliberately count *logical* lines (\n-separated) rather than wrapped
// visual rows. Bubbles textarea's Height is a logical-line count and it
// renders any unused logical-line slots as empty "❯ " prompt rows below
// the content — sizing height by visual-row count makes a long single
// line wrap to N rows AND adds N-1 empty padding rows below it. Letting
// height stay at 1 for single-line input means a long line scrolls
// horizontally inside the textarea (cursor area visible, earlier content
// to the left of the viewport), which is the textarea's clean default
// behavior.
func (m *Model) fitTextareaHeight() {
	const maxLines = 6
	desired := strings.Count(m.textInput.Value(), "\n") + 1
	if desired > maxLines {
		desired = maxLines
	}
	if desired != m.textInput.Height() {
		m.textInput.SetHeight(desired)
	}

	// bubbles/textarea updates its scroll offset before View() rebuilds the
	// viewport content. When a keypress creates a new wrapped row (especially
	// after the height is capped), the final row can be one render behind and the
	// box appears not to scroll. View() has the side effect of setting the
	// textarea viewport content; a following no-op Update re-runs repositionView
	// against that current content so the cursor row is visible immediately.
	m.syncTextareaViewport()
}

// snapshotKillBeforeCursor copies the text from the start of the textarea's
// current logical row up to the cursor into m.killRing so a subsequent
// Ctrl+Y can paste it back. Mirrors bubbles' Ctrl+U scope (per-logical-row,
// not per-visual-row). Returns false — and leaves the ring untouched — when
// there is nothing before the cursor on this row, so a stray Ctrl+U at
// column 0 doesn't blank a previously-killed payload.
func (m *Model) snapshotKillBeforeCursor() bool {
	rows := strings.Split(m.textInput.Value(), "\n")
	rowIdx := m.textInput.Line()
	if rowIdx < 0 || rowIdx >= len(rows) {
		return false
	}
	li := m.textInput.LineInfo()
	col := li.StartColumn + li.CharOffset
	if col <= 0 {
		return false
	}
	rowRunes := []rune(rows[rowIdx])
	if col > len(rowRunes) {
		col = len(rowRunes)
	}
	m.killRing = string(rowRunes[:col])
	return true
}

func (m *Model) syncTextareaViewport() {
	// textarea.View mutates its pointer-backed internal viewport with the latest
	// rendered/wrapped content. The returned string is discarded; renderInputBox
	// will call View() again for the actual frame.
	_ = m.textInput.View()
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(textareaSyncMsg{})
	_ = cmd
}

func (m *Model) recordHistory(input string) {
	if input == "" {
		return
	}
	if n := len(m.inputHistory); n > 0 && m.inputHistory[n-1] == input {
		m.inputHistoryIdx = -1
		m.inputDraft = ""
		return
	}
	m.inputHistory = append(m.inputHistory, input)
	m.inputHistoryIdx = -1
	m.inputDraft = ""
}

func (m Model) historyBack() (Model, bool) {
	if len(m.inputHistory) == 0 {
		return m, false
	}
	if m.inputHistoryIdx == -1 {
		m.inputDraft = m.textInput.Value()
		m.inputHistoryIdx = len(m.inputHistory) - 1
	} else if m.inputHistoryIdx > 0 {
		m.inputHistoryIdx--
	}
	m.textInput.SetValue(m.inputHistory[m.inputHistoryIdx])
	m.textInput.CursorEnd()
	return m, true
}

func (m Model) historyForward() (Model, bool) {
	if m.inputHistoryIdx == -1 {
		return m, false
	}
	if m.inputHistoryIdx < len(m.inputHistory)-1 {
		m.inputHistoryIdx++
		m.textInput.SetValue(m.inputHistory[m.inputHistoryIdx])
	} else {
		m.inputHistoryIdx = -1
		m.textInput.SetValue(m.inputDraft)
	}
	m.textInput.CursorEnd()
	return m, true
}

// renderStatus is the persistent footer line — live, redrawn on every
// Update. Two segments separated by Muted-colored middle dots:
//
//	● model · provider   ·   ctx ████░░ 4.3K / 128K (28%)
//
// Segment 1 carries the connection-state dot (the saturated state
// signal), the model name and the provider profile name both in
// Content (bright). Segment 2 is the visual context-window indicator —
// see renderContextBar for thresholds. The middle dots render in Dim —
// brighter than before, but a step below the Content segments so they
// still read as dividers rather than content.
//
// A location chip trails the context bar: the working directory's last
// two path segments (e.g. "foo/bar") plus the git branch in parens
// ("foo/bar (main)"). The full absolute path is too wide for the footer,
// but the trailing two segments are enough to orient which project you're
// in — and the branch answers "where am I committing" at a glance.
//
// On a narrow terminal the cascade drops chips in ascending order of
// at-a-glance value: provider tag, then the location chip (ambient — the
// splash header showed dir+branch once at startup), then effort, then PR,
// and finally the model's vendor prefix is stripped. The status dot,
// model, and context bar are never dropped — they're the most critical
// signals.
func (m Model) renderStatus() string {
	dot := renderConnDot(m.connection)
	modelName := m.modelName
	routingNote := ""
	if m.router != nil {
		smart, fast := shortModelTag(m.router.SmartModel), shortModelTag(m.router.FastModel)
		switch routerModeOrOff(m.routerMode) {
		case config.RouterModeAuto:
			if smart != "" && fast != "" && (m.modelName == m.router.SmartModel || m.modelName == smart) {
				modelName = smart + ":" + fast
			} else {
				routingNote = "routing: auto"
			}
		case config.RouterModeManual:
			routingNote = "routing: manual"
		}
	}
	model := renderModelName(modelName)
	tag := m.providerLabel
	if tag == "" {
		tag = m.provider
	}
	provider := renderProviderTag(tag)
	ctx := m.renderContextBar()
	pr := m.renderPRStatus()
	effort := m.renderEffortStatus()

	sep := lipgloss.NewStyle().Foreground(colorDim).Render("  ·  ")
	innerSep := lipgloss.NewStyle().Foreground(colorDim).Render(" · ")

	// First segment: dot + model + (optional) provider tag. The provider
	// is bound to the model so it survives the same narrow-screen
	// pressure until the explicit drop kicks in.
	first := dot + "  " + model
	if provider != "" {
		first += innerSep + provider
	}
	// Plan-mode indication lives in the banner above the cmdline (see
	// renderPlanModeBanner); we intentionally do NOT duplicate it as a
	// status-bar chip — one prominent signal beats two competing ones.

	// Worktree chip: when the session runs inside a yottacode-managed
	// worktree, render "worktree: <name>" AFTER the context counter so
	// the model+provider cluster stays adjacent to the ctx number on the
	// left and the worktree label trails on the right where the eye
	// looks last. Plain text per the no-emoji-in-TUI rule.
	worktreeSeg := ""
	if m.worktree != "" {
		worktreeSeg = lipgloss.NewStyle().Foreground(colorContent).Render("worktree: " + m.worktree)
	}

	// Location chip: cwd's last two segments + git branch, e.g.
	// "foo/bar (main)". Content-colored like the other trailing chips;
	// plain text, no glyphs, per the no-emoji-in-TUI rule. Empty when the
	// cwd is unknown (no chip rendered).
	locSeg := ""
	if dir := lastPathSegments(m.cwd, 2); dir != "" {
		label := dir
		if m.branch != "" {
			label += " (" + m.branch + ")"
		}
		locSeg = lipgloss.NewStyle().Foreground(colorContent).Render(label)
	}

	build := func(head string, includeLoc, includePR, includeEffort bool) string {
		segs := []string{head}
		if ctx != "" {
			segs = append(segs, ctx)
		}
		if includeLoc && locSeg != "" {
			segs = append(segs, locSeg)
		}
		if includePR && pr != "" {
			segs = append(segs, pr)
		}
		if includeEffort && effort != "" {
			segs = append(segs, effort)
		}
		if worktreeSeg != "" {
			segs = append(segs, worktreeSeg)
		}
		if routingNote != "" {
			segs = append(segs, lipgloss.NewStyle().Foreground(colorDim).Render(routingNote))
		}
		// Flush-left: the status dot sits at column 0, aligned with the
		// input frame's left border directly above it (and the flush-left
		// scrollback canvas). An earlier 2-space inset trailed the old
		// scrollback margin; with the canvas flush-left it just floated
		// the bar 2 columns off the box edge.
		return strings.Join(segs, sep)
	}

	w := m.width
	if w <= 0 {
		// No size info yet — render the full layout; truncation kicks in
		// once the first WindowSizeMsg lands.
		return build(first, true, true, true)
	}

	line := build(first, true, true, true)
	if lipgloss.Width(line) <= w {
		return line
	}
	// Drop the provider tag first.
	first = dot + "  " + model
	line = build(first, true, true, true)
	if lipgloss.Width(line) <= w {
		return line
	}
	// Drop the location chip next — dir + branch is ambient orientation
	// (the splash header already showed it once), so it yields before the
	// workflow chips.
	line = build(first, false, true, true)
	if lipgloss.Width(line) <= w {
		return line
	}
	// Then the workflow chips, before touching the core model and context
	// signals. PR is more workflow-critical than effort, so effort
	// disappears first, then PR if space is still tight.
	line = build(first, false, true, false)
	if lipgloss.Width(line) <= w {
		return line
	}
	line = build(first, false, false, false)
	if lipgloss.Width(line) <= w {
		return line
	}
	// Still too wide — strip the vendor prefix from the model (e.g.
	// `nvidia/nemotron-…` → `nemotron-…`).
	if idx := strings.LastIndex(m.modelName, "/"); idx >= 0 && idx < len(m.modelName)-1 {
		first = dot + "  " + renderModelName(m.modelName[idx+1:])
	}
	return build(first, false, false, false)
}

func (m Model) renderPRStatus() string {
	if m.currentPR <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colorContent).Render(fmt.Sprintf("PR #%d", m.currentPR))
}

// renderEffortStatus renders the current reasoning-effort level only
// when the active provider/model would actually accept an effort option.
// The applicability probe forces a representative non-default level:
// adapter.EffortInapplicableReason intentionally treats empty/default as
// never worth warning about, but the status bar needs to know whether the
// option would apply if the user picked low/medium/high.
func (m Model) renderEffortStatus() string {
	cfg := m.adapterConfig(m.modelName, m.baseURL)
	cfg.ReasoningEffort = "high"
	provider := m.providerProfile.Provider
	if provider == "" {
		provider = adapter.Provider(strings.TrimSpace(m.provider))
	}
	if adapter.EffortInapplicableReason(cfg, provider) != "" {
		return ""
	}

	level := normalizedEffort(m.reasoningEffort)
	if level == "" {
		level = "default"
	}
	return lipgloss.NewStyle().Foreground(colorContent).Render("effort: " + level)
}

func resolveCurrentPRCmd(ctx context.Context, gh githubapi.Interface, cwd string) tea.Cmd {
	return func() tea.Msg {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if gh != nil {
			pr, err := gh.ReadPR(probeCtx, githubapi.ReadPRRequest{})
			if err == nil {
				return prStatusMsg{number: pr.Number}
			}
		}
		if n := currentPRNumberFromGitHubEnv(probeCtx, cwd); n > 0 {
			return prStatusMsg{number: n}
		}
		return prStatusMsg{}
	}
}

func currentPRNumberFromGitHubEnv(ctx context.Context, cwd string) int {
	owner, repo, err := githubapi.DetectRepo(ctx, cwd)
	if err != nil {
		return 0
	}
	branch := gitBranch(ctx, cwd)
	if branch == "" {
		return 0
	}
	token, _, err := githubapi.NewTokenResolver().Resolve(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return 0
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=open&head=%s", owner, repo, url.QueryEscape(owner+":"+branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var pulls []struct {
		Number int `json:"number"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pulls); err != nil || len(pulls) == 0 {
		return 0
	}
	return pulls[0].Number
}

func prNumberFromToolOutput(output string) int {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "number=") {
			if n, err := parsePositiveDecimal(strings.TrimPrefix(line, "number=")); err == nil {
				return n
			}
		}
		if strings.HasPrefix(line, "pr_number=") {
			if n, err := parsePositiveDecimal(strings.TrimPrefix(line, "pr_number=")); err == nil {
				return n
			}
		}
		if !strings.HasPrefix(line, "created=true") && !strings.HasPrefix(line, "updated=true") {
			continue
		}
		for _, part := range strings.Fields(line) {
			if strings.HasPrefix(part, "number=") {
				if n, err := parsePositiveDecimal(strings.TrimPrefix(part, "number=")); err == nil {
					return n
				}
			}
			if strings.HasPrefix(part, "pr_number=") {
				if n, err := parsePositiveDecimal(strings.TrimPrefix(part, "pr_number=")); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func parsePositiveDecimal(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-decimal number")
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, fmt.Errorf("non-positive number")
	}
	return n, nil
}

// renderProviderTag styles the provider label that sits next to the
// model name in the status bar. Content — the status bar reads bright
// so every segment is easy to spot; the connection dot and threshold
// colors still carry the saturated "state" signal. Empty string when
// the provider is unknown so the status bar can omit it cleanly.
func renderProviderTag(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colorContent).Render(provider)
}

// renderModelName styles the model-name segment of the status bar.
// Plain Content (off-white) rather than Accent — the dot already does
// the "active" job, so painting the model loud too creates competition
// for the eye.
func renderModelName(name string) string {
	return styleStatusModel.Render(name)
}

// shortModelTag strips provider/catalog prefixes from router model labels so
// the status pair stays compact: "provider/model" and "provider:model" render
// as just "model".
func shortModelTag(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// renderTurnStatus composes the live thinking-row footer. Format is
// `<spinner> thinking · K tool queued · 23s · round N/M · 17 tok/s`.
// Round sits between elapsed and rate — the cluster reads as "time
// spent · how many model round-trips · current throughput"
// left-to-right. The design absorbs the meta lines (continuing,
// model requested N tool calls) that used to land in scrollback as
// separate rows.
//
// Elision rules (kept tight on purpose — pre-stream pauses are
// exactly when the user wants the reassurance signal that the
// process is alive):
//   - tool-queued: only when > 0 (no point announcing zero queue)
//   - round: only after IterationStart has fired (iterMax > 0); we
//     show "round 1/25" too — earlier we elided on iter 1 and the
//     row looked dead during long single-shot pauses
//   - tok/s: shown once elapsed > 0; reads "0.0 tok/s" before any
//     token streams (was elided pre-stream — same dead-row problem)
func (m Model) renderTurnStatus() string {
	if !m.turnActive {
		return ""
	}
	elapsed := time.Since(m.turnStart)
	parts := []string{"thinking"}
	// Auto-recall: surface how many prior conversations were pulled into this
	// turn's context, so the injection is legible rather than invisible.
	if m.recalledCount != nil {
		if n := m.recalledCount.Load(); n > 0 {
			noun := "conversations"
			if n == 1 {
				noun = "conversation"
			}
			parts = append(parts, fmt.Sprintf("recalled %d %s", n, noun))
		}
	}
	if queued := m.toolsRequested - m.toolsStarted; queued > 0 {
		noun := "tools"
		if queued == 1 {
			noun = "tool"
		}
		parts = append(parts, fmt.Sprintf("%d %s queued", queued, noun))
	}
	parts = append(parts, formatDuration(elapsed))
	if m.iterMax > 0 {
		parts = append(parts, fmt.Sprintf("round %d/%d", m.iterRound, m.iterMax))
	}
	if secs := elapsed.Seconds(); secs > 0 {
		// Decimal precision below 1 tok/s so a slow / pre-stream
		// row reads as "0.4 tok/s" / "0.0 tok/s" rather than
		// disappearing entirely or rounding the slow case to "0".
		rate := float64(m.turnTokens) / secs
		if rate >= 1 {
			parts = append(parts, fmt.Sprintf("%.0f tok/s", rate))
		} else {
			parts = append(parts, fmt.Sprintf("%.1f tok/s", rate))
		}
	}
	return fmt.Sprintf("%s %s", m.spinner.View(), strings.Join(parts, " · "))
}

// startTurn sends `input` to the agent as a user message and renders
// the same text in the transcript. Used by every prose submission and
// most slash commands that delegate to the model.
func (m Model) startTurn(input string) (tea.Model, tea.Cmd) {
	return m.startTurnWithDisplay(input, "")
}

// startSubagentWakeTurn drains the pending background-completion wakes
// (notify_on_done) into a single turn that injects their results, so the
// model is re-prompted to act on work it dispatched fire-and-forget. The
// scrollback completion banner already showed each result to the user, so
// the turn renders a compact display label rather than re-dumping the body.
// Returns (m, nil) when nothing is queued. Caller must ensure no turn is
// active (the idle inbox path, or the turnEndedMsg drain).
func (m Model) startSubagentWakeTurn() (tea.Model, tea.Cmd) {
	if m.summarizing {
		// Summarization owns a session save + recall index at completion. Keep
		// notify_on_done wakes queued until the compacted snapshot lands so wake
		// turns cannot overlap or out-order those writes.
		return m, nil
	}
	wakes := m.pendingSubagentWakes
	m.pendingSubagentWakes = nil
	if len(wakes) == 0 {
		return m, nil
	}
	input := buildSubagentWakeMessage(wakes)
	var label string
	if len(wakes) == 1 {
		label = fmt.Sprintf("↩ background subagent %s (%s) finished — acting on its result",
			wakes[0].AgentType, shortTaskID(wakes[0].TaskID))
	} else {
		label = fmt.Sprintf("↩ %d background subagents finished — acting on their results", len(wakes))
	}
	return m.startTurnWithDisplay(input, label)
}

// reconcileSubagentCompletions is the self-healing safety net for the
// background-completion inbox. SubagentBackgroundDone is delivered by a
// non-blocking send (run.go), so under a burst — or while the Update loop is
// suspended in the system pager — an event can be dropped and its banner (and
// any notify_on_done wake) lost. The registry records every task's terminal
// state durably, so this scans for terminal background tasks we never
// bannered, paints a banner for each, and queues a wake for any that asked
// for notify_on_done. Called at each turn boundary so a dropped completion
// self-heals rather than vanishing. Returns true if it surfaced anything.
func (m *Model) reconcileSubagentCompletions() bool {
	if m.subagentTasks == nil {
		return false
	}
	healed := false
	for _, t := range m.subagentTasks.List() {
		// Historical tasks are rehydrated from a PRIOR session — they were
		// surfaced (or not) then; re-bannering or re-waking on them at this
		// session's first turn boundary would be noise.
		if !t.Background || t.Historical || t.Status == subagents.TaskRunning || m.banneredSubagentDone[t.ID] {
			continue
		}
		m.banneredSubagentDone[t.ID] = true
		done := agent.SubagentBackgroundDone{
			TaskID:       t.ID,
			AgentType:    t.AgentType,
			Result:       t.Result,
			Errored:      t.Errored,
			Duration:     t.Duration(),
			TokensUsed:   t.TokensUsed,
			ToolCalls:    t.ToolCalls,
			Model:        t.Model,
			NotifyOnDone: t.NotifyOnDone,
		}
		m.appendLine("")
		m.appendLine(renderSubagentBackgroundDone(done))
		// Same rule as the inbox arm: user-canceled tasks banner but
		// never wake — the wake prompt invites a retry of work the
		// user deliberately killed.
		if t.NotifyOnDone && !t.CanceledByUser {
			m.pendingSubagentWakes = append(m.pendingSubagentWakes, done)
		}
		healed = true
	}
	return healed
}

// subagentCanceledByUser reports whether a background task was killed
// via /subagents stop. The SubagentBackgroundDone event doesn't carry
// the flag, so wake gating consults the registry record.
func (m Model) subagentCanceledByUser(taskID string) bool {
	if m.subagentTasks == nil {
		return false
	}
	t, ok := m.subagentTasks.Get(taskID)
	return ok && t.CanceledByUser
}

// buildSubagentWakeMessage renders one or more finished background subagents
// into the user-role message the wake turn injects. It states plainly that
// these are async completions (not the user typing) and carries each full
// result so the model can act without a separate get_subagent_result call.
func buildSubagentWakeMessage(wakes []agent.SubagentBackgroundDone) string {
	var b strings.Builder
	if len(wakes) == 1 {
		b.WriteString("A background subagent you dispatched has finished.\n\n")
	} else {
		fmt.Fprintf(&b, "%d background subagents you dispatched have finished.\n\n", len(wakes))
	}
	for i, w := range wakes {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		status := "completed"
		if w.Errored {
			status = "errored"
		}
		fmt.Fprintf(&b, "Subagent: %s (task %s) — %s\n\nResult:\n%s\n",
			w.AgentType, shortTaskID(w.TaskID), status, strings.TrimRight(w.Result, "\n"))
	}
	b.WriteString("\nReview the result(s) above and continue the work they were part of, or report back to the user as appropriate. If a result is empty or errored, decide whether to retry or move on.")
	return b.String()
}

// shortTaskID renders the 8-char task-id prefix used across the subagent
// surfaces, tolerating ids shorter than 8 chars (test fixtures).
func shortTaskID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// injectSubagentResult is the user-driven counterpart to notify_on_done:
// it starts a turn that feeds a finished subagent's result to the model so
// the user can pull a completed investigation into the conversation on their
// own terms (the /subagents picker's `i` key). Returns a non-empty status
// string (and no turn) when it can't inject — still running, no result, or a
// turn already in flight — for the caller to surface; otherwise it returns
// the started turn and an empty status.
func (m Model) injectSubagentResult(task subagents.Task) (tea.Model, tea.Cmd, string) {
	if task.Status == subagents.TaskRunning {
		return m, nil, fmt.Sprintf("task %s is still running — wait for it to finish", shortTaskID(task.ID))
	}
	if strings.TrimSpace(task.Result) == "" {
		return m, nil, fmt.Sprintf("task %s has no result to inject", shortTaskID(task.ID))
	}
	if m.turnActive {
		return m, nil, "a turn is running — wait for it to finish, then inject"
	}
	if m.summarizing {
		return m, nil, "context summarization is running — wait for it to finish, then inject"
	}
	m.pendingSubagentWakes = append(m.pendingSubagentWakes, agent.SubagentBackgroundDone{
		TaskID:    task.ID,
		AgentType: task.AgentType,
		Result:    task.Result,
		Errored:   task.Errored,
	})
	next, cmd := m.startSubagentWakeTurn()
	return next, cmd, ""
}

// startTurnWithDisplay sends `input` to the agent as a user message
// but renders `displayLabel` (when non-empty) in the transcript
// instead of the full input. Custom slash commands use this to show
// just "/git:commit-message" in scrollback instead of dumping their
// entire directive body, which is often 30-80 lines of instructions
// the agent needs to see but the user wrote once and doesn't want to
// re-read on every invocation.
//
// The session message, checkpoint snapshot, input history, and
// downstream prompts still carry the full input — only the visible
// scrollback rendering is compressed.
func (m Model) startTurnWithDisplay(input, displayLabel string) (tea.Model, tea.Cmd) {
	// Refuse the turn if the running session has no adapter — happens
	// after `/provider remove` deletes the only configured provider
	// (invalidateAdapter clears m.cfg.Adapter). Without this guard the
	// agent loop nil-derefs cfg.Adapter.ChatStream. Recovery path is
	// /provider add or /provider use.
	if m.cfg.Adapter == nil {
		m.appendLine(styleError.Render(
			"no provider configured — run /provider add or /provider use to set one up"))
		return m, nil
	}
	// NOTE: the per-turn system-prompt rebuild (memory retrieval) runs
	// inside the turn goroutine below, not here. The semantic strategy
	// embeds the query via Ollama, and a cold model load can block for
	// the full embed timeout — run synchronously on Enter, that froze
	// the whole UI until the user's message echoed. See the goroutine
	// body and rebuildSystemPromptForTurn for the ordering contract.

	// Detect @<path> tokens in the user input and inject the resolved
	// file contents into the system prompt before the turn fires. The
	// injection replaces any prior auto-injected block so the prompt
	// stays bounded across turns. Loaded files emit a muted notice so
	// the user can see what was attached; load failures emit an error
	// notice but do not block the turn — a missing/typoed path still
	// reaches the model with the failure annotated inline.
	//
	// We also strip the leading `@` from successfully-loaded paths in
	// the user message itself (filerefs.Rewrite). Without this, some
	// models see "explain @docs/foo.md", call read_file("@docs/foo.md")
	// — passing the literal `@` as part of the path — and fail to
	// resolve. Stripping leaves a plain path that pairs cleanly with
	// the section we just injected upstream in the system prompt.
	if refs := filerefs.Parse(input); len(refs) > 0 {
		refs = filerefs.Load(refs, m.cwd)
		m.injectFileRefs(refs)
		input = filerefs.Rewrite(input, refs)
	} else {
		// No refs this turn — strip any prior block left over from a
		// previous turn so the system prompt doesn't drift.
		m.clearFileRefs()
	}

	// In plan mode without a plan-file slug yet (user typed `/plan` with
	// no topic), derive the slug from this first user message so the
	// model can start writing to it.
	maybeFillPlanFile(&m, input)

	// Create a /checkpoints checkpoint for this turn BEFORE the user
	// message is appended — restore needs the conversation as it stood
	// when the user typed this prompt. Soft on failure: a checkpoint
	// store error must not block the turn (the feature simply doesn't
	// capture this prompt).
	var checkpointID string
	if store, ok := m.cfg.Checkpoints.(*checkpoint.Store); ok && store != nil {
		id, err := store.Begin(m.sess.ID, input, len(m.sess.Messages), m.sess.Messages)
		if err == nil {
			checkpointID = id
		}
	}

	var userImages []adapter.ImageBlock
	if m.providerProfile.SupportsImages {
		userImages = m.collectPastedImages()
	} else {
		m.pastedImages = nil
	}
	// Consume a pending pre-compaction memory reminder: it rides the
	// HISTORY copy of this message only — the checkpoint label, input
	// history, and transcript rendering below all keep the user's own
	// text. Appended after fileref rewriting so the reminder can't
	// interfere with @path resolution. Retrieval scoring is immune to
	// its wording: the turn goroutine's rebuild scores against the
	// `input` parameter, never this content string.
	// At most one memory reminder rides a given message. The
	// pre-compaction one wins when both are due: it is the more urgent
	// trigger (context is about to be compacted away) and asks for the
	// same thing, so appending both would just repeat itself.
	content := input
	switch {
	case m.memoryNudgePending:
		m.memoryNudgePending = false
		content += "\n\n" + preCompactionMemoryReminder
	case m.captureReminderDue():
		content += "\n\n" + captureReminderPrompt
	}
	m.sess.Messages = append(m.sess.Messages, adapter.Message{
		Role:    adapter.RoleUser,
		Content: content,
		Images:  userImages,
	})
	m.userTurnsThisLaunch++
	// First user submission this launch — drops the onboarding hint
	// footer below the input from now on.
	m.firstMessageSent = true
	// Thin colored left bar on the user block is enough of a visual
	// anchor between turns — no horizontal rule needed (the rule
	// fought with everything else above and below it). When the
	// caller passed an explicit displayLabel (custom slash commands),
	// render that compact label instead of the full input body so
	// scrollback isn't dominated by 80 lines of directive prompt.
	rendered := input
	if displayLabel != "" {
		rendered = displayLabel
	}
	m.appendLine(renderUserBlock(rendered, m.width))
	m.recordHistory(input)

	turnCtx, cancel := context.WithCancel(m.parentCtx)
	turnCtx = agent.WithCheckpoint(turnCtx, m.sess.ID, checkpointID)
	m.turnCancel = cancel
	m.turnActive = true
	m.turnStart = time.Now()
	m.turnTokens = 0
	m.turnToolCalls = 0
	m.turnUsage = adapter.Usage{}
	// Per-turn meta surfaced in the thinking row resets to zero so
	// nothing carries over from a previous turn.
	m.iterRound = 0
	m.iterMax = 0
	m.toolsRequested = 0
	m.toolsStarted = 0
	m.eventsCh = make(chan agent.Event, 64)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m.userMsgCh = make(chan string, 1)
	m.cfg.UserMessages = m.userMsgCh
	// Serialize the agent goroutine's history appends against this Update
	// goroutine's reads of sess.Messages (token estimate, /context,
	// system-prompt edits). Subagents leave this nil — their history is a
	// private slice owned by one goroutine.
	m.cfg.HistoryLock = m.histMu

	go func(ev chan agent.Event, dec chan agent.Decision, errCh chan error) {
		defer func() {
			if r := recover(); r != nil {
				// A panic inside the turn would otherwise crash the whole
				// TUI and leave ev unclosed, hanging waitForEvent forever.
				// Close the channel and report the panic as the turn error
				// so the session ends the turn gracefully and survives.
				close(ev)
				errCh <- fmt.Errorf("agent turn panicked: %v", r)
			}
		}()
		// Per-turn retrieval: re-render the system prompt with memory
		// bodies scored against this turn's user input. USER.md /
		// YOTTACODE.md and both MEMORY.md indexes always inject in
		// full; only per-entry memory bodies are filtered. Soft on
		// failure — a disk error leaves the existing prompt intact.
		//
		// Runs HERE — off the Update goroutine — because the semantic
		// strategy's Ollama embed can block 1-2s on a cold model load;
		// under the spinner that cost reads as ordinary turn latency
		// instead of frozen input, and turnCtx lets Esc abort the
		// embed. histMu serializes the Messages[0] rewrite against
		// Update-goroutine reads (watermark, /context, pickers); the
		// inner closure guarantees the unlock even if the rebuild
		// panics, so the outer recover can't strand a dead lock.
		func() {
			m.histMu.Lock()
			defer m.histMu.Unlock()
			m.rebuildSystemPromptForTurn(turnCtx, input)
		}()
		m.refreshTurnCompactionConfig()
		err := agent.Turn(turnCtx, m.cfg, &m.sess.Messages, ev, dec)
		close(ev)
		errCh <- err
	}(m.eventsCh, m.decisions, m.turnErrCh)

	// Re-arm the spinner tick now that there's a thinking indicator to
	// drive. The tick self-perpetuates via spinner.Update; it stops
	// firing once the turn ends and there's no further m.spinner.Tick
	// scheduled. See Init() for why we don't run this when idle.
	return m, tea.Batch(m.spinner.Tick, waitForEvent(m.eventsCh, m.turnErrCh))
}

func (m Model) handleAgentEvent(ev agent.Event) (tea.Model, tea.Cmd) {
	switch e := ev.(type) {
	case agent.IterationStart:
		// Step counter rolls into the thinking row instead of dumping
		// to scrollback — see renderTurnStatus. New iteration: reset
		// the per-iteration tool counters since they're scoped to the
		// model reply that opened this iteration.
		m.iterRound = e.Number
		m.iterMax = e.Max
		m.toolsRequested = 0
		m.toolsStarted = 0
		// Refresh the context-window estimate so the status-bar
		// percentage tracks per-round growth. Without this the
		// counter only updates at turnDone and a multi-round-trip
		// turn looks frozen at the pre-turn value, then snaps up
		// to the final number once the turn lands. Notices /
		// auto-summarize stay on the post-turn path — firing them
		// mid-stream would interrupt the model.
		m.refreshContextTokens()
	case agent.IterationContinue:
		switch e.Reason {
		case "tool_calls":
			// "K tool queued" rolls into the thinking row.
			m.toolsRequested = e.ToolCalls
			m.toolsStarted = 0
		case "truncated_output":
			// Rare edge case — the model's response hit the output
			// cap mid-reply and the loop is going around for a
			// continuation. Emit a one-line scrollback notice so the
			// user can tell the response was split (otherwise the
			// continuation looks like a fresh thought).
			m.appendLine(styleToolMeta.Render("response hit output limit, continuing…"))
		}
	case agent.ReasoningToken:
		m.reasoning.WriteString(e.Text)
		m.statsTokens++
		m.turnTokens++
	case agent.StreamProgress:
		// Heartbeat for in-flight tool-call argument generation — no
		// visible text, just keeps the live tok/s indicator moving on
		// turns where the model goes straight to a function call
		// without a reasoning summary (notably gpt-5* on the
		// Responses API with effort=minimal).
		m.statsTokens++
		m.turnTokens++
	case agent.ContentToken:
		// Model finished reasoning and started writing the answer. Clear
		// the reasoning preview so the live footer transitions cleanly
		// from "thinking" to "answering."
		m.reasoning.Reset()
		// First content token of this turn: emit a leading blank line
		// for breathing room from the previous block (the user message
		// or a tool result). After this, content streams line-by-line
		// directly into scrollback as each \n arrives — see
		// flushCompletedStreamLines below. Only the trailing partial
		// line stays in m.streaming for the live footer preview.
		if m.streamingMode != streamContent {
			m.appendLine("")
			m.paragraphStart = true
		}
		m.streamingMode = streamContent
		m.streaming.WriteString(e.Text)
		m.statsTokens++
		m.turnTokens++
		m.flushCompletedStreamLines()
	case agent.AssistantMessage:
		m.commitStreaming()
		if rendered := renderCitations(e.Message.Citations); rendered != "" {
			m.appendLine(rendered)
		}
		// Accumulate the turn's token usage on the session. Nil-safe
		// (adapter didn't observe usage → no-op). m.modelName beats
		// m.sess.Model because the user may have switched mid-session
		// via /provider use; the per-model breakdown wants the model
		// that actually produced the turn.
		m.sess.AddUsage(m.modelName, e.Message.Usage)
		// Same delta onto the per-turn accumulator so the end-of-turn
		// footer can show this turn's real token total.
		m.turnUsage.Add(e.Message.Usage)
		// Window-drift raise: the provider's exact input count proving
		// the resolved window too small (see window_drift.go).
		m.noteWindowUsage(e.Message.Usage)
	case agent.ProviderToolCall:
		m.reasoning.Reset()
		m.appendLine("")
		m.appendLine(renderProviderToolCard(e.ToolName, e.Phase, e.Detail, m.width))
	case agent.Fallback:
		m.appendLine(renderFallbackLine(e))
	case agent.ApprovalAuto:
		// Render only the first line of the preview — the full content
		// (file body, edit diff, etc.) is about to land in the unified
		// tool card, so dumping it twice would just be noise. Tools
		// emit a single-line invocation summary as the first line by
		// convention; if a tool ever ships a multi-line preview, we
		// still get a useful one-liner here.
		summary := e.Preview
		if i := strings.Index(summary, "\n"); i >= 0 {
			summary = summary[:i]
		}
		m.appendLine(styleAuto.Render(statusOKLine(e.Source, summary)))
	case agent.ApprovalNeeded:
		m.commitStreaming()
		// exit_plan_mode reads the plan from the resolved plan file
		// (single source of truth); the tool itself takes no arg. If
		// the file is missing or empty the model called the tool
		// prematurely — auto-deny with a console notice so the
		// refinement-hint message reaches the model next iteration.
		//
		// On success: emit the plan body to scrollback as a quoted
		// block ABOVE the decision modal so the user can see what
		// they're approving. The body persists in scrollback even
		// after they dismiss the modal — no need for a separate
		// archive helper.
		if e.ToolName == "exit_plan_mode" {
			body, denyReason := loadPlanForApproval(m.cfg.PlanMode)
			if denyReason != "" {
				m.appendLine(styleError.Render("[plan] " + denyReason + " — refusing exit_plan_mode"))
				if !m.sendDecision(agent.Deny) {
					return m, nil
				}
				return m, m.finishDecisionUI()
			}
			emitPlanBodyToScrollback(&m, body)
		}
		// write_file body emits to scrollback BEFORE the modal opens
		// so long files don't cram the box and so the contents
		// persist after the modal dismisses (visible by scrolling
		// back) regardless of approve/deny. The modal then renders
		// only a path + size summary.
		if e.ToolName == "write_file" {
			emitWriteFileBodyToScrollback(&m, e.ArgsJSON)
		}
		m.awaitingApproval = true
		m.approvalTool = e.ToolName
		m.approvalPreview = e.Preview
		m.approvalArgs = e.ArgsJSON
		// Pre-derive the "always allow" pattern so the modal can show
		// the user exactly what rule they'd be saving. Suppressed
		// (approvalAllowAlwaysOK = false) for compound shell commands
		// and other shapes where derivation would be a footgun — see
		// permissions.DeriveAllowRule.
		if rule, ok := permissions.DeriveAllowRule(e.ToolName, e.ArgsJSON, m.cwd, worktree.NormalizeForRule); ok && m.perms != nil {
			m.approvalAllowAlwaysOK = true
			m.approvalDerivedRule = rule
		} else {
			m.approvalAllowAlwaysOK = false
			m.approvalDerivedRule = ""
		}
		// Pre-derive the "never / block" rule (run_bash + git). Unlike the
		// allow side, this is offered for dangerous/compound commands too —
		// those are exactly what a user most wants to block permanently.
		if rule, ok := permissions.DeriveDenyRule(e.ToolName, e.ArgsJSON, m.cwd); ok && m.perms != nil {
			m.approvalDenyAlwaysOK = true
			m.approvalDerivedDenyRule = rule
		} else {
			m.approvalDenyAlwaysOK = false
			m.approvalDerivedDenyRule = ""
		}
		// Don't queue another waitForEvent until the user answers; the
		// approval keypress handler issues it.
		return m, nil
	case agent.PathTrustElevationNeeded:
		// Out-of-workspace write hit the validator. Park the
		// elevation request and render Prompt 2 (the inline
		// path-trust modal). The keypress handler in the input
		// switch consumes m.awaitingPathTrust, mutates the
		// registered write tools' AllowedPaths (on accept), and
		// sends the corresponding Decision back to the loop.
		m.commitStreaming()
		m.awaitingPathTrust = true
		m.pathTrustReq = e
		return m, nil
	case agent.ToolStart:
		// Reasoning ended and a tool fires — clear the live reasoning
		// preview so the user's view transitions cleanly. We DON'T
		// emit anything to scrollback yet; the live thinking row
		// already shows what's happening, and the unified tool card
		// (header + body + footer) renders as one block on
		// ToolResult so it doesn't get split across the scrollback.
		m.reasoning.Reset()
		m.pendingToolName = e.ToolName
		m.pendingToolPreview = e.Preview
		m.pendingToolArgs = e.ArgsJSON
		m.pendingToolStart = time.Now()
		m.statsToolCalls++
		m.toolsStarted++
		m.turnToolCalls++
	case agent.ToolResult:
		if e.ToolName == "gh_pr_read" || e.ToolName == "gh_pr_review_context" {
			m.currentPR = prNumberFromToolOutput(e.Output)
		}
		if e.ToolName == "gh_pr_create" || e.ToolName == "git_push" {
			if n := prNumberFromToolOutput(e.Output); n > 0 {
				m.currentPR = n
			}
		}
		// The Agent tool already gets its own visualization via the
		// SubagentStart / SubagentProgress / SubagentDone events that
		// fired while the child was running (the ▶/├/└ card). Rendering
		// the parent's standard tool card here would duplicate the
		// prompt as a header AND the child's final answer as the body
		// — interleaved with the subagent card because the events fire
		// at different points in the lifecycle, producing misaligned
		// box-drawing characters. Suppress the parent card for Agent
		// and let the subagent card stand on its own.
		if e.ToolName == agent.AgentToolName {
			m.pendingToolName = ""
			m.pendingToolPreview = ""
			m.pendingToolArgs = ""
			m.pendingToolStart = time.Time{}
			break
		}
		// todo_write fires its visualization through a different path:
		// the live plan card in View() (driven by the TodoUpdate event
		// that always trails each successful todo_write call). Rendering
		// the standard scrollback card here would stack a duplicate card
		// for every plan flip — exactly the visual noise this surface is
		// designed to avoid. The end-of-turn commit in TurnDone lands
		// one final snapshot in scrollback.
		if e.ToolName == "todo_write" {
			m.pendingToolName = ""
			m.pendingToolPreview = ""
			m.pendingToolArgs = ""
			m.pendingToolStart = time.Time{}
			break
		}
		// Render the buffered start info + this result as a unified
		// tool card. Leading blank line gives each card breathing
		// room from the previous emission.
		m.appendLine("")
		preview := m.pendingToolPreview
		if preview == "" {
			preview = e.ToolName
		}
		// Elapsed since ToolStart — the card header shows it only when the
		// call was slow (see slowDurationTag). Guard the zero start (a
		// ToolResult with no matching ToolStart shouldn't happen, but a
		// huge bogus duration would be worse than none).
		var toolDur time.Duration
		if !m.pendingToolStart.IsZero() {
			toolDur = time.Since(m.pendingToolStart)
		}
		m.appendLine(renderToolCard(e.ToolName, preview, m.pendingToolArgs, e.Output, e.Errored, m.width, m.cwd, toolDur))
		m.pendingToolName = ""
		m.pendingToolPreview = ""
		m.pendingToolArgs = ""
		m.pendingToolStart = time.Time{}
		// Reset streamingMode so the next ContentToken's "first content
		// after non-content" branch fires and inserts a blank line
		// between the just-rendered tool card and the resumed
		// assistant body. Without this, streamingMode stays at
		// streamContent from before the tool call, the blank-line
		// guard is skipped, and the assistant's content lands tight
		// against the card's `└ done` footer with no breathing room.
		m.streamingMode = streamIdle
	case agent.CwdChanged:
		// enter_worktree / exit_worktree swapped the session's working
		// directory mid-conversation. Refresh m.cwd so the status-line
		// worktree chip and any cwd-derived display state pick up the
		// new value on the next render. The agent-level cwdRef and the
		// process cwd are already in sync — the tool that emitted the
		// event handled both.
		// projectRoot deliberately does NOT move with it: entering or
		// leaving a worktree stays within the same repository identity,
		// which is exactly what auto-recall scopes on, and recomputing
		// would put a git subprocess on the event path. m.cwd still
		// updates, so the literal-cwd arm of the scope match follows.
		m.cwd = e.NewCwd
		m.currentPR = 0
		// Compute the worktree name from the new path: any path under
		// <some-repo>/.yottacode/worktrees/<name>/ identifies the name
		// without needing to re-resolve the repo root.
		m.worktree = worktreeNameFromPath(e.NewCwd)
		if m.sess != nil {
			m.sess.Cwd = e.NewCwd
			m.sess.Worktree = m.worktree
		}
		return m, tea.Batch(resolveCurrentPRCmd(m.parentCtx, m.githubClient, m.cwd), waitForEvent(m.eventsCh, m.turnErrCh))
	case agent.TodoUpdate:
		// Drive the live plan card in View(): livePlan is what
		// renderLivePlanCard reads on every redraw, livePlanTouched
		// arms the end-of-turn snapshot commit in TurnDone. An empty
		// e.Todos clears the live card immediately (the model just
		// called todo_write with []) without emitting anything to
		// scrollback — silently disappearing matches the "plan cleared"
		// semantics. Keep m.sess.Todos in sync so the per-turn Save
		// persists the list across resumes; the constructor reseeds
		// livePlan from sess.Todos so a resumed session sees the card
		// immediately.
		m.livePlan = e.Todos
		m.livePlanTouched = true
		if m.sess != nil {
			m.sess.Todos = e.Todos
		}
	case agent.IterCap:
		// Cap hit. The hint suggests doubling — the most common
		// recovery is "I want it to keep going." `e.Max` already
		// reflects the auto-mode multiplier (the loop computes the
		// effective cap before emitting), so the suggestion scales
		// naturally.
		suggest := e.Max * 2
		m.appendLine(styleError.Render(fmt.Sprintf(
			"[agent] hit %d/%d iterations · %d tool calls this turn",
			e.Max, e.Max, m.turnToolCalls)))
		m.appendLine(styleAuto.Render(fmt.Sprintf(
			"  raise with `/max-iterations %d` and ask me to continue, or pass --max-iterations %d at launch",
			suggest, suggest)))
	case agent.ContextCompacted:
		if e.Err != nil {
			m.lastContextCompaction = "skipped: " + e.Err.Error()
			m.appendLine(styleAuto.Render(fmt.Sprintf("[context] compaction skipped: %v", e.Err)))
			break
		}
		kind := "compacted"
		if e.Forced {
			kind = "force-compacted"
		}
		line := fmt.Sprintf("[context] %s mid-turn ~%s → ~%s tokens", kind, formatTokens(e.Before), formatTokens(e.After))
		if e.SnapshotPath != "" {
			line += " · snapshot " + abbrevHome(e.SnapshotPath)
		}
		if e.SnapshotErr != nil {
			line += fmt.Sprintf(" · snapshot failed: %v", e.SnapshotErr)
		}
		m.lastContextCompaction = fmt.Sprintf("%s %s → %s", kind, formatTokens(e.Before), formatTokens(e.After))
		if e.SnapshotPath != "" {
			m.lastContextCompaction += " · snapshot " + abbrevHome(e.SnapshotPath)
		}
		if e.SnapshotErr != nil {
			m.lastContextCompaction += fmt.Sprintf(" · snapshot failed: %v", e.SnapshotErr)
		}
		m.appendLine(styleAuto.Render(line))
		m.refreshContextTokens()
		m.compactionSeq++
	case agent.ErrorEvent:
		m.commitStreaming()
		// Multi-line errors (e.g. 429 with retry-after hint) render
		// each line with its own ✗ prefix so the second line
		// doesn't look orphaned in scrollback.
		for _, line := range strings.Split(strings.TrimRight(e.Err.Error(), "\n"), "\n") {
			m.appendLine(styleError.Render("✗ " + line))
		}
	case agent.UserMessageAppended:
		m.appendLine(styleAuto.Render("[delivered] " + truncateForRender(e.Content, 80)))
	case agent.TurnDone:
		m.commitStreaming()
		// If the agent touched the plan this turn, commit one full
		// snapshot of the final state to scrollback AND clear the
		// live card. Without the clear, View() keeps rendering
		// m.livePlan after the commit, so the user sees the same
		// plan twice — once in scrollback and once still in the
		// live frame — for the rest of the inter-turn idle period.
		// Clearing makes the handoff clean: live card during the
		// turn, scrollback snapshot after. The card reappears on
		// the next turn that fires todo_write (a fresh TodoUpdate
		// repopulates m.livePlan). A turn that did NOT touch the
		// plan leaves m.livePlan alone so a resumed-session plan
		// stays visible between turns until the agent touches it.
		if m.livePlanTouched && len(m.livePlan) > 0 {
			allDone := true
			for _, t := range m.livePlan {
				if t.Status != agent.TodoCompleted {
					allDone = false
					break
				}
			}
			if !allDone {
				m.appendLine("")
				m.appendLine(renderTodoCardFromTodos(m.livePlan, m.width))
			}
			m.livePlan = nil
		}
		m.livePlanTouched = false
		// Footnote: how long the turn took, end-to-end (model
		// thinking + tool execution). Rendered in the same
		// dim/italic style as other inline notices so it fades into
		// scrollback as a quiet receipt.
		if !m.turnStart.IsZero() {
			m.appendLine(styleTurnFooter.Render(renderTurnFooter(time.Since(m.turnStart), m.turnUsage)))
		}
	case agent.TurnInterrupted:
		// Calm marker, not a red error. The loop has already preserved
		// history (partial assistant content + synthetic tool_result
		// entries for any orphaned tool_use); this line just lets the
		// user see that the cancel landed cleanly. If pending input
		// was queued by Enter, turnEndedMsg will auto-submit it
		// immediately after — no extra prompt needed here.
		m.commitStreaming()
		// Don't commit a plan snapshot for an interrupted turn — the
		// in-flight state was, by definition, not what the agent
		// intended to ship. Reset the flag so the NEXT TurnDone
		// doesn't fire a stale snapshot referencing this turn's work.
		m.livePlanTouched = false
		msg := "↩ interrupted"
		if e.OrphanedCalls > 0 {
			noun := "tool calls"
			if e.OrphanedCalls == 1 {
				noun = "tool call"
			}
			msg = fmt.Sprintf("%s (%d %s cancelled)", msg, e.OrphanedCalls, noun)
		}
		m.appendLine(styleTurnFooter.Render(msg))
	case agent.SubagentStart:
		// Lead with a blank line so the subagent block reads as its own
		// section against the surrounding tool cards.
		m.appendLine("")
		m.appendLine(renderSubagentStart(e))
	case agent.SubagentProgress:
		// One-line tick. Multiple of these will land in quick
		// succession while a child works through its tool budget; let
		// scrollback collect them rather than overwriting.
		m.appendLine(renderSubagentProgress(e))
	case agent.SubagentDone:
		m.appendLine(renderSubagentDone(e))
	case agent.SubagentBackgroundDone:
		// Routed through the long-lived inbox so it can fire after the
		// parent turn ends. Always re-arm the inbox listener so the
		// next completion lands without delay; pair with the turn
		// listener when a turn is active so both streams stay drained.
		//
		// Dedup against reconcileSubagentCompletions first: MarkDone
		// lands in the registry strictly BEFORE the inbox send, and the
		// inbox message races the turnEndedMsg drain — so a completion
		// arriving at a turn boundary can be bannered (and its wake
		// queued) by the reconcile pass before this delivered event is
		// processed. Without the guard the same result double-banners
		// and fires a SECOND wake turn (duplicate spend, possibly
		// duplicate actions).
		if m.banneredSubagentDone[e.TaskID] {
			if m.turnActive {
				return m, tea.Batch(waitForSubagentInbox(m.subagentInbox), waitForEvent(m.eventsCh, m.turnErrCh))
			}
			return m, waitForSubagentInbox(m.subagentInbox)
		}
		m.appendLine("")
		m.appendLine(renderSubagentBackgroundDone(e))
		m.banneredSubagentDone[e.TaskID] = true
		if e.NotifyOnDone && !m.subagentCanceledByUser(e.TaskID) {
			// The spawner asked to be re-prompted with this result
			// (notify_on_done). Queue it; if the model is idle, wake it
			// now with a turn that injects the result so it can act —
			// otherwise the turnEndedMsg drain starts that turn when the
			// in-flight one finishes (a wake can't interrupt a live turn).
			// User-canceled tasks are bannered but never wake the model:
			// the wake message nudges "decide whether to retry", and
			// re-running work the user just killed is the wrong default.
			m.pendingSubagentWakes = append(m.pendingSubagentWakes, e)
			if !m.turnActive {
				next, cmd := m.startSubagentWakeTurn()
				return next, tea.Batch(cmd, waitForSubagentInbox(m.subagentInbox))
			}
		}
		if m.turnActive {
			return m, tea.Batch(waitForSubagentInbox(m.subagentInbox), waitForEvent(m.eventsCh, m.turnErrCh))
		}
		return m, waitForSubagentInbox(m.subagentInbox)
	}
	return m, waitForEvent(m.eventsCh, m.turnErrCh)
}

// commitStreaming finalizes an in-flight assistant turn. Flushes any
// trailing partial line and any unclosed code block into scrollback,
// then clears the streaming state. Called on AssistantMessage,
// ApprovalNeeded, ErrorEvent, and TurnDone.
//
// Unclosed code blocks (```“ opened but no closing fence) get
// emitted plain — the model probably crashed mid-block, so highlighting
// the partial body would be misleading. The user sees what was there.
func (m *Model) commitStreaming() {
	// Drain any final completed lines first.
	m.flushCompletedStreamLines()
	// Trailing partial line (no \n yet)?
	if m.streaming.Len() > 0 && m.streamingMode == streamContent {
		if m.inCodeBlock {
			// Partial line of an unclosed code block — append to buffer
			// and emit the whole buffer plain.
			m.codeBlockBuf.WriteString(m.streaming.String())
		} else if m.inTable {
			m.tableBuf.WriteString(m.streaming.String())
		} else {
			m.emitAssistantProse(m.streaming.String())
		}
	}
	if m.inTable {
		m.flushTable()
	}
	if m.inCodeBlock {
		m.flushCodeBlock(false /* highlight */)
	}
	m.streaming.Reset()
	m.streamingMode = streamIdle
	m.reasoning.Reset()
}

// flushCompletedStreamLines processes every line of the streaming
// buffer that's now complete (terminated by \n). Each line goes
// through handleStreamLine which decides:
//
//   - "regular prose" → emit to scrollback as today
//   - "fence open"    → start buffering code, hide fence marker
//   - "inside code"   → append to code block buffer (no live emit)
//   - "fence close"   → emit highlighted code block, hide fence marker
//
// The trailing partial line stays in m.streaming for the live footer
// preview. When inCodeBlock is true the footer shows a "…writing code"
// notice instead of the partial line itself (see View()).
func (m *Model) flushCompletedStreamLines() {
	s := m.streaming.String()
	nl := strings.LastIndexByte(s, '\n')
	if nl < 0 {
		return
	}
	completed := s[:nl]
	remaining := s[nl+1:]
	m.streaming.Reset()
	m.streaming.WriteString(remaining)
	for _, line := range strings.Split(completed, "\n") {
		m.handleStreamLine(line)
	}
}

// handleStreamLine processes a single fully-formed line of streaming
// content, transitioning the code-block state machine and either
// emitting to scrollback or buffering as appropriate.
func (m *Model) handleStreamLine(line string) {
	if m.inCodeBlock {
		if isFenceClose(line) {
			m.flushCodeBlock(true /* highlight */)
			return
		}
		m.codeBlockBuf.WriteString(line)
		m.codeBlockBuf.WriteByte('\n')
		return
	}
	if lang, ok := parseFenceOpen(line); ok {
		if m.inTable {
			m.flushTable()
		}
		m.inCodeBlock = true
		m.codeBlockLang = lang
		return
	}
	if m.inTable {
		if isTableLine(line) {
			m.tableBuf.WriteString(line)
			m.tableBuf.WriteByte('\n')
			return
		}
		// Blank lines between table rows don't end the table — the
		// LLM may resume on the next line. Swallowing them prevents
		// a single table from being split into multiple glamour
		// renders with inconsistent column widths.
		if strings.TrimSpace(line) == "" {
			return
		}
		m.flushTable()
	}
	if isTableLine(line) && !isTableSeparatorOnly(line) {
		m.inTable = true
		m.tableBuf.WriteString(line)
		m.tableBuf.WriteByte('\n')
		return
	}
	if isUnicodeTableLine(line) {
		m.emitUnicodeTableLine(line)
		return
	}
	m.emitAssistantProse(line)
}

// proseMaxWidth caps the wrap width for assistant prose so text doesn't
// run edge-to-edge on wide terminals. Matches the card system's
// cardMaxWidthCap for visual consistency.
const proseMaxWidth = 120

// scrollbackLeftMargin is a global column inset applied to every line
// emitted to scrollback. It is 0: the conversation canvas is flush-left,
// sharing a single column-0 edge with the chrome (startup box, input
// frame, status bar). Structural glyphs — card gutters (┌ │ └), the
// user-echo chevron (❯), banners — sit at column 0; the 2-space text
// indent users read comes from each element's own structure (the card
// gutter's trailing space, styleAssistantBody's PaddingLeft(2)), NOT
// from this margin.
//
// Kept as a named constant (rather than deleted) so queuePrintln and its
// flush variant stay one code path and the width math reads explicitly.
// A non-zero value would re-inset the whole canvas; doing so reintroduces
// the chrome-vs-content misalignment that flush-left removed.
const scrollbackLeftMargin = 0

// emitAssistantProse renders a prose line from the assistant, word-wraps
// it to a comfortable reading width, and appends the result to scrollback.
// The wrap width is capped at proseMaxWidth so wide terminals still get
// a readable column. Continuation lines after a wrap pick up a hanging
// indent that matches the first line's visible content column.
//
// Indent is owned by the per-line style (styleAssistantBody, etc., via
// PaddingLeft(2)). Earlier code added an extra "  " prefix to the first
// line of each paragraph here, which doubled the indent — paragraph
// openers landed at column 5 while continuations stayed at column 3.
// Removing the explicit prefix keeps every prose line at the same
// 2-space indent regardless of whether it opens a paragraph.
func (m *Model) emitAssistantProse(line string) {
	rendered := renderAssistantBlock(line)
	if strings.TrimSpace(rendered) == "" {
		m.paragraphStart = true
		m.appendLine(rendered)
		return
	}
	m.paragraphStart = false
	wrapW := m.width
	if wrapW <= 0 || wrapW > proseMaxWidth {
		wrapW = proseMaxWidth
	}
	if ansi.StringWidth(rendered) > wrapW {
		rendered = wrapProseHanging(rendered, wrapW)
	}
	m.appendLine(rendered)
}

// wrapProseHanging word-wraps s to fit within limit visible columns.
// Continuation lines are indented to match the first line's content
// start: for plain prose that's the body padding (2 spaces); for list
// items it's past the marker (4 for "- ", 5 for "1. ", etc.) so text
// stays aligned under the first word, not under the bullet.
func wrapProseHanging(s string, limit int) string {
	indentW := visibleContentStart(s)
	if indentW < 0 {
		indentW = 0
	}
	wrapAt := limit - indentW
	if wrapAt < 20 {
		wrapAt = 20
	}
	wrapped := ansi.Wrap(s, wrapAt, "")
	lines := strings.Split(wrapped, "\n")
	if len(lines) <= 1 {
		return wrapped
	}
	pad := strings.Repeat(" ", indentW)
	for i := 1; i < len(lines); i++ {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// visibleContentStart returns the column where the first "real" content
// character sits, skipping ANSI escape codes, leading whitespace, and
// common list markers (-, *, +, 1., 2)). For plain prose with 2-space
// body padding it returns 2; for "  - item" it returns 4; for
// "  1. item" it returns 5. Used by wrapProseHanging to compute the
// continuation indent.
func visibleContentStart(s string) int {
	col := 0
	inEsc := false
	phase := 0 // 0=leading spaces, 1=marker, 2=done
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		switch phase {
		case 0:
			if r == ' ' {
				col++
			} else if r == '-' || r == '*' || r == '+' || r == '•' || (r >= '0' && r <= '9') {
				phase = 1
				col++
			} else {
				return col
			}
		case 1:
			col++
			if r == ' ' {
				return col
			}
			if r == '.' || r == ')' {
				// "1." or "1)" — expect a space next
				continue
			}
			if r >= '0' && r <= '9' {
				// multi-digit number
				continue
			}
			// Not a recognized marker pattern; content started
			// at the first non-space char we saw in phase 0.
			return col - 1
		}
	}
	return col
}

// flushCodeBlock emits the buffered code block to scrollback and
// clears the buffer. When highlight is true, runs the body through
// chroma using m.codeBlockLang as the lexer hint; otherwise emits
// plain text (used for unclosed-block recovery in commitStreaming).
//
// Emits **one line per appendLine** rather than one multi-line block.
// Bubbletea's standard renderer in inline mode (no alt-screen)
// miscounts when a single tea.Println carries multiple \n-separated
// lines and any of them auto-wraps on a narrow terminal — the live
// frame redraw lands at the wrong cursor row and bleeds into the
// printed code (input-box borders and status-bar text overwriting
// leading whitespace of code lines). One-line-at-a-time emission
// keeps every print as a clean atomic render cycle.
func (m *Model) flushCodeBlock(highlight bool) {
	body := strings.TrimRight(m.codeBlockBuf.String(), "\n")
	m.codeBlockBuf.Reset()
	m.inCodeBlock = false
	m.codeBlockLang = ""
	if body == "" {
		return
	}
	_ = highlight // fenced blocks use a neutral container style, not syntax colors.
	for _, line := range strings.Split(body, "\n") {
		m.appendLine(renderCodeBlockLine(line))
	}
}

func (m *Model) flushTable() {
	raw := strings.TrimRight(m.tableBuf.String(), "\n")
	m.tableBuf.Reset()
	m.inTable = false
	if raw == "" {
		return
	}
	// Size the glamour renderer to the table's content rather than
	// the full terminal width. This prevents glamour from stretching
	// narrow tables across the entire screen.
	cols, idealWidth := tableMetrics(raw)
	if idealWidth > m.width {
		idealWidth = m.width
	}
	minWidth := cols*3 + cols + 1
	if minWidth < 20 {
		minWidth = 20
	}
	// Render → measure → shrink loop. Each iteration measures the
	// actual rendered output and shrinks by exactly the overflow
	// amount, converging in 2-3 passes without any hardcoded margin.
	width := idealWidth
	var bestLines []string
	for i := 0; i < 8 && width >= minWidth; i++ {
		r := newMarkdownRenderer(width)
		rendered := strings.TrimRight(r.render(raw), "\n")
		lines := strings.Split(rendered, "\n")
		maxW := 0
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > maxW {
				maxW = w
			}
		}
		bestLines = lines
		if maxW <= m.width {
			break
		}
		overflow := maxW - m.width
		width -= overflow + 1
	}
	if bestLines == nil {
		r := newMarkdownRenderer(minWidth)
		rendered := strings.TrimRight(r.render(raw), "\n")
		bestLines = strings.Split(rendered, "\n")
	}
	bestLines = fixTableAlignment(bestLines)
	m.emitTableLines(bestLines)
}

// fixTableAlignment corrects column-separator misalignment in glamour's
// rendered table output. Glamour uses byte length for column width
// calculations, not display width, so multi-byte characters (–, •, §,
// ≈, →, etc.) shift │ separators by 1-2 positions per occurrence. This
// function detects the canonical separator positions (mode across all
// data rows) and adjusts whitespace in misaligned rows to restore
// vertical alignment.
func fixTableAlignment(lines []string) []string {
	const sep = "│"
	type lineInfo struct {
		positions []int
	}
	infos := make([]lineInfo, len(lines))
	maxSepCount := 0
	for i, line := range lines {
		infos[i].positions = tableSepDisplayPositions(line, sep)
		if len(infos[i].positions) > maxSepCount {
			maxSepCount = len(infos[i].positions)
		}
	}
	if maxSepCount == 0 {
		return lines
	}

	// Canonical position per separator column = most-frequent position.
	canonical := make([]int, maxSepCount)
	for col := 0; col < maxSepCount; col++ {
		freq := map[int]int{}
		for _, info := range infos {
			if col < len(info.positions) {
				freq[info.positions[col]]++
			}
		}
		best, bestN := 0, 0
		for pos, n := range freq {
			if n > bestN || (n == bestN && pos < best) {
				best, bestN = pos, n
			}
		}
		canonical[col] = best
	}

	result := make([]string, len(lines))
	for i, line := range lines {
		seps := infos[i].positions
		if len(seps) != maxSepCount {
			result[i] = line
			continue
		}
		aligned := true
		for col := range seps {
			if seps[col] != canonical[col] {
				aligned = false
				break
			}
		}
		if aligned {
			result[i] = line
			continue
		}
		result[i] = realignTableRow(line, sep, canonical)
	}
	return result
}

// tableSepDisplayPositions returns the display-column positions of every
// occurrence of sep in line (ANSI-aware).
func tableSepDisplayPositions(line, sep string) []int {
	plain := ansi.Strip(line)
	var positions []int
	pos := 0
	for _, r := range plain {
		if string(r) == sep {
			positions = append(positions, pos)
		}
		pos += ansi.StringWidth(string(r))
	}
	return positions
}

// realignTableRow adjusts whitespace within a glamour-rendered table row
// so each │ separator lands at its canonical display-column position.
// Works on the raw ANSI string: splits by the literal sep character,
// trims/pads trailing spaces in each cell segment, then reassembles.
func realignTableRow(line, sep string, canonical []int) string {
	parts := strings.Split(line, sep)
	if len(parts) < 2 {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + 8)
	displayPos := 0
	for i, part := range parts {
		if i == len(parts)-1 {
			b.WriteString(part)
			break
		}
		target := canonical[i]
		partW := ansi.StringWidth(part)
		end := displayPos + partW

		if end == target {
			b.WriteString(part)
		} else if end < target {
			b.WriteString(part)
			for j := end; j < target; j++ {
				b.WriteByte(' ')
			}
		} else {
			// Trim trailing spaces to make room.
			trimmed := strings.TrimRight(part, " ")
			trimW := ansi.StringWidth(trimmed)
			b.WriteString(trimmed)
			pad := target - (displayPos + trimW)
			if pad < 0 {
				pad = 0
			}
			for j := 0; j < pad; j++ {
				b.WriteByte(' ')
			}
		}
		b.WriteString(sep)
		displayPos = target + ansi.StringWidth(sep)
	}
	return b.String()
}

// tableMetrics parses a markdown pipe table and returns the number of
// columns and the ideal rendering width derived from cell content.
// The ideal width is the sum of each column's widest cell plus
// per-column border and padding overhead.
func tableMetrics(raw string) (numCols int, idealWidth int) {
	var colMax []int
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isTableSeparatorOnly(line) {
			continue
		}
		line = strings.Trim(line, "|")
		cells := strings.Split(line, "|")
		for i, cell := range cells {
			w := ansi.StringWidth(strings.TrimSpace(cell))
			if i >= len(colMax) {
				colMax = append(colMax, w)
			} else if w > colMax[i] {
				colMax[i] = w
			}
		}
	}
	numCols = len(colMax)
	if numCols == 0 {
		return 0, 40
	}
	// border: (numCols+1) │ chars, padding: 2 per cell, glamour indent: 2
	total := numCols + 1 + 2
	for _, w := range colMax {
		total += w + 2
	}
	return numCols, total
}

// emitTableLines sends glamour-rendered table lines to scrollback.
// Lines that still exceed terminal width are hard-wrapped with
// continuation rows indented to preserve visual alignment within
// the table, rather than letting queuePrintln orphan words outside
// the table grid.
func (m *Model) emitTableLines(lines []string) {
	for _, line := range lines {
		m.emitUnicodeTableLine(line)
	}
}

// emitUnicodeTableLine prints an already-rendered table row without
// letting terminal hard-wrap orphan overflow at column zero. Data rows
// keep their left/table separators and split the overflowing cell onto
// continuation rows inside that same column.
func (m *Model) emitUnicodeTableLine(line string) {
	if m.width <= 0 || ansi.StringWidth(line) <= m.width || !strings.Contains(line, "│") {
		m.appendLine(line)
		return
	}
	for _, row := range wrapUnicodeTableRow(line, m.width) {
		m.appendLine(row)
	}
}

func wrapUnicodeTableRow(line string, width int) []string {
	if width <= 0 || ansi.StringWidth(line) <= width || !strings.Contains(line, "│") {
		return []string{line}
	}
	parts := strings.Split(line, "│")
	lastCell := -1
	for i, part := range parts {
		if strings.TrimSpace(ansi.Strip(part)) != "" {
			lastCell = i
		}
	}
	if lastCell < 0 {
		return []string{line}
	}
	prefix := ""
	if lastCell > 0 {
		prefix = strings.Join(parts[:lastCell], "│") + "│"
	}
	suffix := ""
	if lastCell < len(parts)-1 {
		suffix = "│" + strings.Join(parts[lastCell+1:], "│")
	}
	cellWidth := width - ansi.StringWidth(prefix) - ansi.StringWidth(suffix)
	if cellWidth < 4 {
		prefix = strings.Repeat(" ", ansi.StringWidth(prefix))
		cellWidth = width - ansi.StringWidth(prefix) - ansi.StringWidth(suffix)
		if cellWidth < 4 {
			return []string{line}
		}
	}
	cellText := strings.TrimSpace(parts[lastCell])
	rows := wrapWordsByDisplay(cellText, cellWidth)
	if len(rows) == 1 && ansi.StringWidth(prefix)+ansi.StringWidth(rows[0])+ansi.StringWidth(suffix) > width {
		prefix = strings.Repeat(" ", ansi.StringWidth(prefix))
		cellWidth = width - ansi.StringWidth(prefix) - ansi.StringWidth(suffix)
		rows = wrapWordsByDisplay(cellText, cellWidth)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row) == "" {
			continue
		}
		if ansi.StringWidth(prefix)+ansi.StringWidth(row)+ansi.StringWidth(suffix) > width {
			prefix = strings.Repeat(" ", ansi.StringWidth(prefix))
			cellWidth = width - ansi.StringWidth(prefix) - ansi.StringWidth(suffix)
		}
		out = append(out, prefix+padDisplay(row, cellWidth)+suffix)
	}
	if len(out) == 0 {
		return []string{line}
	}
	return out
}

func wrapWordsByDisplay(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var rows []string
	for _, word := range strings.Fields(text) {
		for ansi.StringWidth(word) > width {
			part, rest := splitDisplayWidth(word, width)
			if part == "" {
				break
			}
			rows = append(rows, part)
			word = rest
		}
		if word == "" {
			continue
		}
		if len(rows) == 0 {
			rows = append(rows, word)
			continue
		}
		last := rows[len(rows)-1]
		if ansi.StringWidth(last)+1+ansi.StringWidth(word) <= width {
			rows[len(rows)-1] = last + " " + word
			continue
		}
		rows = append(rows, word)
	}
	return rows
}

func splitDisplayWidth(s string, width int) (string, string) {
	if width <= 0 {
		return "", s
	}
	used := 0
	for i, r := range s {
		rw := ansi.StringWidth(string(r))
		if used+rw > width {
			return s[:i], s[i:]
		}
		used += rw
	}
	return s, ""
}

func padDisplay(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func renderCodeBlockLine(line string) string {
	return styleCodeBlock.Render(expandCodeTabs(line))
}

func indentCodeBlockLine(line string) string {
	return renderCodeBlockLine(line)
}

func expandCodeTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	return strings.ReplaceAll(s, "\t", "    ")
}

// parseFenceOpen returns (language, true) if the line is a markdown
// code-fence opener like "```go" or "```", and ("", false) otherwise.
// Lenient: trims surrounding whitespace, accepts any tail text after
// the language as part of the lang hint (chroma will normalize or
// fall back).
func parseFenceOpen(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "```") {
		return "", false
	}
	rest := strings.TrimPrefix(t, "```")
	// "```" alone is ambiguous — treat as open with no language hint.
	// Disambiguation between open and close happens in handleStreamLine
	// via the inCodeBlock state.
	return strings.TrimSpace(rest), true
}

// isFenceClose reports whether the line is the bare "```" fence marker
// used to close a code block. We're stricter for closes than opens —
// a line with extra text after the backticks (like "```py") is treated
// as opening a new fence, not closing the current one. In practice
// LLMs always emit a bare "```" close.
func isFenceClose(line string) bool {
	return strings.TrimSpace(line) == "```"
}

var (
	tableLineRE    = regexp.MustCompile(`^\s*\|.*\|`)
	tableSepOnlyRE = regexp.MustCompile(`^\s*\|[\s:|\-]+\|\s*$`)
	unicodeTableRE = regexp.MustCompile(`[│┼]`)
)

func isTableLine(line string) bool {
	return tableLineRE.MatchString(line)
}

func isTableSeparatorOnly(line string) bool {
	return tableSepOnlyRE.MatchString(line)
}

func isUnicodeTableLine(line string) bool {
	return unicodeTableRE.MatchString(line)
}

// renderUserBlock formats a user message for scrollback emission. The first
// line gets the same chevron prompt (❯) used by the live input bar, so
// scrollback echoes look like the text the user typed into the cmdline.
// Continuation rows — whether from hard newlines or wrapping a large paste —
// hang-indent under the prompt instead of repeating another chevron. This keeps
// pasted multi-line text from looking like multiple separate submissions.
// Leading and trailing "\n" give the block one blank line of separation
// on each side, so the user echo sits as a clear divider between the
// previous assistant/tool output and whatever follows it (the next tool
// card, an [auto-mode]/[plan-mode-allow] auto-approval line, etc.). No
// horizontal rule above: the chevron is enough of an anchor, and the rule
// fought with content on either side.
//
// Long lines are hard-wrapped at width-barWidth and continuation rows are
// hang-indented under the prompt, so a multi-line paste reads as one continuous
// quoted block instead of looking like a separate user submission on every
// pasted line.
func renderUserBlock(content string, width int) string {
	const prefix = "❯ "
	prefixWidth := ansi.StringWidth(prefix)
	bar := styleUserBar.Render(prefix)
	indent := strings.Repeat(" ", prefixWidth)
	bodyWidth := width - prefixWidth
	var lines []string
	firstLine := true
	for _, line := range strings.Split(content, "\n") {
		// Width <= prefix means the terminal is too narrow to host the
		// bar plus any content; fall back to the un-wrapped render
		// rather than producing zero-width rows.
		if bodyWidth <= 0 || ansi.StringWidth(line) <= bodyWidth {
			prefixText := indent
			if firstLine {
				prefixText = bar
			}
			lines = append(lines, prefixText+styleUserBody.Render(line))
			firstLine = false
			continue
		}
		wrapped := ansi.Hardwrap(line, bodyWidth, true)
		rows := strings.Split(wrapped, "\n")
		for _, row := range rows {
			prefixText := indent
			if firstLine {
				prefixText = bar
			}
			lines = append(lines, prefixText+styleUserBody.Render(row))
			firstLine = false
		}
	}
	return "\n" + strings.Join(lines, "\n") + "\n"
}

// renderAssistantBlock formats an assistant reply for scrollback emission.
// Plain prose stays lightweight; inline code/paths/commands get extra
// emphasis, and strongly code-looking lines get syntax highlighting even if
// the model forgot to fence them.
//
// The (renderedLine, extraBlank) pair from renderAssistantLineWithState
// encodes three cases:
//
//   - non-empty renderedLine, extraBlank=false: regular line.
//   - non-empty renderedLine, extraBlank=true: line followed by a forced
//     blank (e.g. headings — they need breathing room below).
//   - empty renderedLine, extraBlank=true: the source line was blank (we
//     preserve paragraph breaks) OR an open-fence marker we hid. Either
//     way, emit a single blank, never two — earlier the loop appended
//     "" twice for source blank lines, so any model output with a blank
//     between paragraphs got rendered with a doubled gap.
func renderAssistantBlock(rendered string) string {
	lines := strings.Split(rendered, "\n")
	out := make([]string, 0, len(lines))
	state := &assistantRenderState{}
	for _, line := range lines {
		renderedLine, extraBlank := renderAssistantLineWithState(line, state)
		if renderedLine == "" && !extraBlank {
			continue
		}
		out = append(out, renderedLine)
		if extraBlank && renderedLine != "" {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

var (
	inlineCodeRE = regexp.MustCompile("`[^`]+`")
	boldRE       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// inlinePathRE runs *after* inlineCodeRE has already injected ANSI
	// escapes into the line, so the character class for the first
	// alternative must exclude the ESC byte (\x1b) — otherwise the
	// greedy `[^...]+` swallows the trailing `\x1b[0m` reset of the
	// inline-code styling into the path match. Lipgloss then re-renders
	// the chunk with those bytes still inside, and the embedded `[0m`
	// (with its ESC eaten by adjacent escape sequences) leaks into the
	// terminal as visible literal text.
	inlinePathRE          = regexp.MustCompile("(^|[\\s(])((?:\\./|\\../|~/|/)[^\\s:;,)\\]\x1b]+|[A-Za-z0-9._/-]+\\.(?:go|md|txt|json|ya?ml|toml|ts|tsx|js|jsx|py|rs|sh|bash|zsh|sql|css|html|xml))")
	bulletLineRE          = regexp.MustCompile(`^•\s+(.*)$`)
	markdownBulletLineRE  = regexp.MustCompile(`^(\s*)([-*+])\s+(.*)$`)
	numberedListLineRE    = regexp.MustCompile(`^(\s*)(\d+[.)])\s+(.*)$`)
	markdownHeadingLineRE = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	blockquoteLineRE      = regexp.MustCompile(`^>\s?(.*)$`)
	actionSummaryRE       = regexp.MustCompile(`^(Edited|Explored|Read|Ran)\s+(.+)$`)
	treeLineRE            = regexp.MustCompile(`^\s*[└├│].*$`)
	separatorLineRE       = regexp.MustCompile(`^[\s─—-]{8,}$`)
	diffCountsRE          = regexp.MustCompile(`\(\+\d+\s+-\d+\)`)
)

func renderAssistantLine(line string) string {
	rendered, _ := renderAssistantLineWithState(line, nil)
	return rendered
}

func renderAssistantLineWithState(line string, state *assistantRenderState) (string, bool) {
	if state != nil && state.inCodeBlock {
		if isFenceClose(line) {
			state.inCodeBlock = false
			return "", false
		}
		return renderCodeBlockLine(line), false
	}
	if lang, ok := parseFenceOpen(line); ok {
		if state != nil {
			state.inCodeBlock = true
			state.codeLang = lang
		}
		return "", false
	}
	if line == "" {
		return "", true
	}
	if separatorLineRE.MatchString(strings.TrimSpace(line)) {
		return styleSeparator.Render(line), false
	}
	if heading := renderHeadingLine(line); heading != "" {
		return heading, true
	}
	if quote := renderBlockquoteLine(line); quote != "" {
		return quote, false
	}
	if bullet := renderBulletLine(line); bullet != "" {
		return bullet, false
	}
	if treeLineRE.MatchString(line) {
		return styleToolMeta.Render(line), false
	}
	if looksLikeCodeLine(line) {
		return renderCodeBlockLine(line), false
	}
	return highlightInlineText(line), false
}

type assistantRenderState struct {
	inCodeBlock bool
	codeLang    string
}

func renderHeadingLine(line string) string {
	m := markdownHeadingLineRE.FindStringSubmatch(line)
	if len(m) != 3 {
		return ""
	}
	return styleAssistantHeading.Render(m[1] + " " + m[2])
}

func renderBlockquoteLine(line string) string {
	m := blockquoteLineRE.FindStringSubmatch(line)
	if len(m) != 2 {
		return ""
	}
	return styleAssistantBody.Render(styleAssistantQuote.Render("│ ") + styleAssistantQuote.Render(renderInlineDecorations(m[1])))
}

func renderBulletLine(line string) string {
	m := bulletLineRE.FindStringSubmatch(line)
	if len(m) != 2 {
		return renderMarkdownListLine(line)
	}
	body := m[1]
	if action := actionSummaryRE.FindStringSubmatch(body); len(action) == 3 {
		tail := renderInlineDecorations(action[2])
		tail = diffCountsRE.ReplaceAllStringFunc(tail, func(s string) string {
			return styleTurnFooter.Render(s)
		})
		return styleAssistantBody.Render(styleBullet.Render("• ") + styleActionVerb.Render(action[1]+" ") + tail)
	}
	return styleAssistantBody.Render(styleBullet.Render("• ") + renderInlineDecorations(body))
}

func renderMarkdownListLine(line string) string {
	if m := markdownBulletLineRE.FindStringSubmatch(line); len(m) == 4 {
		indent, marker, body := m[1], m[2], m[3]
		return styleAssistantBody.Render(indent + styleListMarker.Render(marker+" ") + renderInlineDecorations(body))
	}
	if m := numberedListLineRE.FindStringSubmatch(line); len(m) == 4 {
		indent, marker, body := m[1], m[2], m[3]
		return styleAssistantBody.Render(indent + styleListMarker.Render(marker+" ") + renderInlineDecorations(body))
	}
	return ""
}

func highlightInlineText(line string) string {
	out := renderInlineDecorations(line)
	if looksLikeShellCommand(line) {
		return styleAssistantBody.Render(styleInlineCommand.Render(out))
	}
	return styleAssistantBody.Render(out)
}

func renderInlineDecorations(line string) string {
	out := renderInlineSpans(line)
	out = inlinePathRE.ReplaceAllStringFunc(out, func(s string) string {
		m := inlinePathRE.FindStringSubmatch(s)
		if len(m) != 3 {
			return s
		}
		return m[1] + styleInlinePath.Render(m[2])
	})
	return out
}

func renderInlineSpans(line string) string {
	out := line
	out = boldRE.ReplaceAllStringFunc(out, func(s string) string {
		m := boldRE.FindStringSubmatch(s)
		if len(m) != 2 {
			return s
		}
		return styleAssistantBold.Render(m[1])
	})
	out = inlineCodeRE.ReplaceAllStringFunc(out, func(s string) string {
		return styleInlineCode.Render(strings.Trim(s, "`"))
	})
	return out
}

func looksLikeCodeLine(line string) bool {
	trim := strings.TrimSpace(line)
	if trim == "" {
		return false
	}
	if looksLikeShellCommand(trim) {
		return true
	}
	for _, prefix := range []string{
		"func ", "package ", "import ", "type ", "var ", "const ",
		"if ", "for ", "switch ", "case ", "return ", "class ", "def ",
		"public ", "private ", "SELECT ", "INSERT ", "UPDATE ", "DELETE ",
	} {
		if strings.HasPrefix(trim, prefix) {
			return true
		}
	}
	return strings.Contains(trim, ":=") || strings.Contains(trim, "::")
}

func looksLikeShellCommand(line string) bool {
	trim := strings.TrimSpace(line)
	if trim == "" {
		return false
	}
	for _, prefix := range []string{
		"$ ", "> ", "go ", "git ", "npm ", "pnpm ", "yarn ", "cargo ",
		"make ", "python ", "pytest ", "node ", "bash ", "sh ", "docker ",
		"kubectl ", "terraform ", "uv ", "rg ", "grep ",
	} {
		if strings.HasPrefix(trim, prefix) {
			return true
		}
	}
	return false
}

// appendLine emits a conversation line to terminal scrollback (via
// tea.Println), records it in the transcript builder for test
// inspection, and tracks it in historyLines so it can be replayed after
// a terminal resize wipes the visible viewport.
//
// Multi-line inputs are split into per-line entries in historyLines so
// resize-replay also emits them line-by-line — same safety reason as
// the per-line Println split in appendRaw.
func (m *Model) appendLine(s string) {
	if strings.Contains(s, "\n") {
		for _, line := range strings.Split(s, "\n") {
			m.historyLines = append(m.historyLines, line)
		}
	} else {
		m.historyLines = append(m.historyLines, s)
	}
	m.appendRaw(s)
}

// appendRaw is the inner emitter — used by appendLine and by the
// startup-chrome path (box, hint, welcome). Startup chrome deliberately
// does NOT go through appendLine because we regenerate it fresh on every
// resize at the new terminal width, instead of replaying the original
// (which would have wrong border math at the new width).
//
// Splits multi-line inputs into per-line tea.Println calls. Bubbletea's
// standard renderer in inline mode (no alt-screen) miscounts the
// "linesRendered" bookkeeping when a single tea.Println carries
// multiple \n-separated lines and any of those lines auto-wraps on a
// narrow terminal — the live frame's redraw lands at the wrong cursor
// row and bleeds the input-box borders / status bar over the leading
// columns of just-printed content. One-line-per-Println keeps every
// print as a clean atomic render cycle the renderer can track.
func (m *Model) appendRaw(s string) {
	if m.transcript.Len() > 0 {
		m.transcript.WriteString("\n\n")
	}
	m.transcript.WriteString(s)
	m.queuePrintln(s)
}

// appendRawFlush is appendRaw for content that should emit at column 0
// instead of the scrollbackLeftMargin gutter — currently just the startup
// identity card, aligned with the flush-left input frame.
func (m *Model) appendRawFlush(s string) {
	if m.transcript.Len() > 0 {
		m.transcript.WriteString("\n\n")
	}
	m.transcript.WriteString(s)
	m.queuePrintlnFlush(s)
}

// queuePrintln queues s as one or more tea.Println commands — one per
// terminal-row-sized chunk — without touching the transcript builder.
// Used by appendRaw and by the resize replay path.
//
// Lines longer than the terminal width are hard-wrapped (ANSI-aware)
// so each Println maps to exactly one terminal row. This is the
// load-bearing invariant for inline-mode rendering: when a single
// Println carries content that the terminal then auto-wraps, Bubbletea
// counts 1 logical line but the terminal moved the cursor down N
// rows, the live frame redraws at the wrong row, and the wrapped tail
// of the printed line gets left as a "ghost" right under the new
// scrollback emit. Pre-wrapping here keeps the renderer's row count
// honest.
//
// Each printed line is prefixed with `\r\x1b[2K` (carriage-return +
// Erase-Entire-Line) so the cursor is at column 0 of a clean row
// before our content paints. Without this, Bubbletea's inline mode
// leaves the cursor at whatever column the previous live-frame
// render finished at, and any leading columns of the landing row not
// painted by our content show through as bleed — `cmdline border` /
// `status bar` / `thinking row` text appearing embedded at the start
// of indented lines, where our leading whitespace doesn't overwrite.
// (Bubbletea only emits `\r` to reset the cursor on the first frame;
// see standard_renderer.go:228. Subsequent queued message lines
// inherit whatever column the previous draw ended in.)
//
// Trailing erase-to-EOL is NOT added here — Bubbletea's renderer
// already appends `ansi.EraseLineRight` to every short queued
// message line (standard_renderer.go:202).
func (m *Model) queuePrintln(s string) {
	m.queuePrintlnIndented(s, scrollbackLeftMargin)
}

// queuePrintlnFlush emits s at column 0, bypassing scrollbackLeftMargin.
// Used for the startup identity card, which is deliberately aligned with
// the flush-left command-line input frame (also column 0) rather than the
// inset conversation canvas — the two read as the session's top/bottom
// chrome bookends.
func (m *Model) queuePrintlnFlush(s string) {
	m.queuePrintlnIndented(s, 0)
}

func (m *Model) queuePrintlnIndented(s string, leftMargin int) {
	width := m.width
	if width <= 0 {
		// Before the first WindowSizeMsg the terminal width is unknown.
		// Emitting now would hard-wrap at the 80-col fallback and the
		// queued lines would race with the startup box (the entry-banner
		// "wraps at 80 and interleaves with the welcome box" bug). Callers
		// at this stage are construction-time appendLine (mode/permission
		// entry banners, custom-command errors) which have already recorded
		// s into historyLines and the transcript — the startup handler
		// re-emits historyLines at the real width once it lands. So defer
		// rather than emit at the wrong width.
		return
	}
	const clearLine = "\r\x1b[2K"
	margin := strings.Repeat(" ", leftMargin)
	for _, line := range strings.Split(s, "\n") {
		// Blank rows emit as bare empty lines so visual paragraph
		// breaks stay clean — no trailing whitespace to scroll past.
		if line == "" {
			m.pendingCmds = append(m.pendingCmds, tea.Println(clearLine))
			continue
		}
		if ansi.StringWidth(line) <= width {
			m.pendingCmds = append(m.pendingCmds, tea.Println(clearLine+margin+line))
			continue
		}
		wrapped := ansi.Hardwrap(line, width, true)
		for _, row := range strings.Split(wrapped, "\n") {
			m.pendingCmds = append(m.pendingCmds, tea.Println(clearLine+margin+row))
		}
	}
}

// repaintViewport forces a clean inline redraw: wipe the visible viewport,
// then replay the startup chrome (fresh sessions only) followed by every
// recorded conversation line at the current width. Re-emitting the
// transcript from the top down scrolls the live frame back to the bottom of
// the terminal — the load-bearing side effect, since inline-mode Bubbletea
// (no alt-screen) doesn't re-anchor a frame on its own.
//
// Two callers need that re-anchor: a genuine resize (the live frame's
// border smears at the old width) and a quiet overlay close (the shrinking
// frame strands the footer mid-screen). Routing both here keeps the
// recovery sequence in one place.
//
// Each replay line goes through queuePrintln — the SAME path live emission
// uses — so it gets the \r\x1b[2K clear-line prefix (cursor reset, no
// column bleed) and is re-wrapped to the current width.
func (m *Model) repaintViewport() {
	m.pendingCmds = append(m.pendingCmds, tea.ClearScreen)
	if m.shouldShowStartupCard() {
		m.queuePrintlnFlush(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.sess.ID, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width))
		m.queuePrintln("")
	}
	for _, line := range m.historyLines {
		m.queuePrintln(line)
	}
}

// flushPending drains queued tea.Println cmds into a single sequenced Cmd.
func (m *Model) flushPending() tea.Cmd {
	if len(m.pendingCmds) == 0 {
		return nil
	}
	cmds := m.pendingCmds
	m.pendingCmds = nil
	if len(cmds) == 1 {
		return cmds[0]
	}
	return tea.Sequence(cmds...)
}
