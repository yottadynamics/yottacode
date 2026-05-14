package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	"github.com/yottadynamics/yottacode/internal/checkpoint"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/providerops"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/usercmd"
)

// Config carries everything Run needs to build a Model. Bundling these into a
// struct is just ergonomics — there are too many fields for a positional
// argument list to stay readable.
type Config struct {
	Cfg                    agent.LoopConfig
	Session                *session.Session
	Permissions            *permissions.Permissions
	Recall                 *recall.Index // optional; nil disables /recall
	ModelName              string
	BaseURL                string
	APIKey                 string
	Provider               string
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
	// BypassPermissions auto-approves every tool call. DANGEROUS — see
	// the flag help on --dangerously-skip-permissions. Explicit `deny`
	// rules in .yottacode/permissions.json still apply.
	BypassPermissions      bool
	Version                string // e.g. "0.3.0" — shown in the header
	Commit                 string // short SHA the binary was built from; "" when unknown (go run, tarball)
	Dirty                  bool   // true when the build had uncommitted changes; renders a "*" beside the commit
	Branch                 string // current git branch (empty if not in a repo)
	MemorySummary          string // "USER", "YOTTA", "USER+YOTTA", "UMEM", "USER+UMEM", or "" if none
	BaseSystemPrompt       string // pre-memory prompt — needed by /memory reload to recompose

	// FileCfg holds tunables loaded from ~/.yottacode/config.toml
	// (context watermarks, retrieval). The TUI reads these at session
	// start and re-reads them on /memory reload.
	FileCfg config.Config

	// Subagents is the session task registry the /subagents slash
	// command inspects. Populated by run.go alongside Agent tool
	// registration; nil disables /subagents (tests that don't wire
	// subagents in are still valid).
	Subagents *subagents.Registry

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
}

// Model is the Bubbletea state for the chat TUI. The TUI runs in inline mode
// (no alt-screen): conversation lines flow into the terminal's native
// scrollback via tea.Println, while only the live footer (input box + status
// bar + transient overlays) redraws in place at the bottom. This means the
// terminal — not the app — owns history, so native selection, scroll-wheel,
// and copy work end-to-end.
type Model struct {
	parentCtx              context.Context
	cfg                    agent.LoopConfig
	modelName              string
	baseURL                string
	apiKey                 string
	provider               string
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
	bypassPermissions      bool
	cwd                    string
	perms                  *permissions.Permissions
	recall                 *recall.Index
	version                string
	commit                 string
	dirty                  bool
	branch                 string
	memorySummary          string
	baseSystemPrompt       string // pre-memory prompt; used by /memory reload

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

	// summarizing flips true while /summarize (manual or auto) is
	// running so we don't fire a second one on top of the first if
	// the user crosses another threshold while we're working.
	summarizing bool

	// Cumulative session stats — incremented as events stream in.
	statsTokens    int
	statsToolCalls int

	// Per-turn throughput tracking
	turnStart     time.Time
	turnTokens    int
	turnToolCalls int

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
	// every ToolResult.
	pendingToolName    string
	pendingToolPreview string
	pendingToolArgs    string

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

	// subagentTool is the live AgentTool registered on the loop's
	// registry. The TUI uses it to wire the background-done callback
	// and to introspect the available agent configs from /subagents.
	subagentTool *agent.AgentTool

	// customSlash carries the user-authored slash commands loaded
	// from ~/.yottacode/commands/ and <cwd>/.yottacode/commands/. The
	// dispatcher (m.findSlash) walks built-ins first, then this list.
	// /help groups them under a "Custom commands:" header so it's
	// obvious which entries come from user files.
	customSlash []slashCommand

	// subagentInbox is a long-lived channel the AgentTool pushes
	// SubagentBackgroundDone events onto from detached goroutines
	// when a background subagent finishes after the parent turn has
	// ended. The Model drains this in parallel with the per-turn
	// eventsCh via waitForSubagentInbox so notifications surface
	// without waiting for the next user input. Buffer is generous
	// (32) so a flurry of completions can't block the goroutines.
	subagentInbox chan agent.SubagentBackgroundDone

	// Persistent session
	sess *session.Session

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

	// pastes maps short placeholder markers to the full content of large
	// pastes. When the user pastes more than pasteThreshold bytes, we
	// insert a `[Pasted text #N: lines, bytes]` token into the input box
	// instead of the full body — keeps the cmdline from stretching to
	// fill the screen. On submit, expandPastes() swaps the markers back
	// for their original content before the message hits the agent.
	pastes   map[string]string
	pasteSeq int

	turnActive bool
	turnCancel context.CancelFunc
	eventsCh   chan agent.Event
	turnErrCh  chan error
	decisions  chan agent.Decision
	// pendingInputAfterTurn captures a user message typed and Enter'd
	// during an active turn. The Enter handler cancels the current turn
	// and stashes the input here; the turnEndedMsg handler picks it up
	// and starts a fresh turn so the model sees the new message after
	// history was preserved with synthetic tool_result entries by the
	// agent loop. Cleared when consumed and when a context-wiping slash
	// command (/clear, /sessions) preempts the queued submission.
	pendingInputAfterTurn string

	// Approval modal state
	awaitingApproval       bool
	approvalTool           string
	approvalPreview        string
	approvalArgs           string
	// approvalAllowAlwaysOK gates the [a]lways-allow keypress. Set
	// true when DeriveAllowRule can produce a sensible pattern from
	// this call; false for compound shell commands and other shapes
	// where a one-click blanket grant would be a footgun. Recomputed
	// each time a new ApprovalNeeded event lands.
	approvalAllowAlwaysOK  bool
	// approvalDerivedRule is the pattern the modal will save when the
	// user picks [a]. Shown in the modal so the user knows what
	// they're committing to.
	approvalDerivedRule    string

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
	// trip. Curated providers (anthropic/openai/gemini) read from
	// the embedded catalog instantly; openai-compatible / ollama
	// providers fetch live each open.
	pickerList      pickerListFn
	modelPickerOpen bool
	modelPicker     *modelPickerState

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

	// Connection probe state for the status footer dot
	connection connState

	// Slash command palette state
	paletteOpen     bool
	paletteFiltered []slashCommand
	paletteIndex    int

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

// New builds a Model wired with the given config.
func New(parent context.Context, c Config) Model {
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

	// Defensive: tests build Config without FileCfg, leaving the
	// thresholds at 0.0 — which would mean every status redraw colors
	// the token counter as if we were over budget. Detect the zero
	// value and substitute documented defaults so all callers see a
	// usable config without each having to remember to populate it.
	if c.FileCfg.Context.DefaultWindow == 0 {
		c.FileCfg = config.Default()
	}

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
		contextTokensInit = contextwindow.EstimateTokens(c.Session.Messages)
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
		bypassPermissions:      c.BypassPermissions,
		cwd:                    c.Cwd,
		perms:                  c.Permissions,
		recall:                 c.Recall,
		version:                c.Version,
		commit:                 c.Commit,
		dirty:                  c.Dirty,
		branch:                 c.Branch,
		memorySummary:          c.MemorySummary,
		baseSystemPrompt:       c.BaseSystemPrompt,
		fileCfg:                c.FileCfg,
		subagentTasks:          c.Subagents,
		subagentTool:           c.AgentTool,
		subagentInbox:          make(chan agent.SubagentBackgroundDone, 32),
		customSlash:            buildCustomSlash(c.CustomCommands),
		sess:                   c.Session,
		textInput:              ti,
		spinner:                sp,
		md:                     newMarkdownRenderer(80),
		inputHistoryIdx:        -1,
		transcript:             &strings.Builder{},
		streaming:              &strings.Builder{},
		reasoning:              &strings.Builder{},
		codeBlockBuf:           &strings.Builder{},
		livePlan:               livePlanInit,
		firstMessageSent:       firstMessageSent,
		contextTokens:          contextTokensInit,
		// Cursor starts visible — the first blink tick (530ms after
		// Init) flips it off, giving an unambiguous "yes the input is
		// focused" cue on the very first frame.
		cursorVisible: true,
	}
}

func (m Model) Init() tea.Cmd {
	// Deliberately do NOT start m.spinner.Tick here. The spinner's
	// thinking indicator is only visible during a turn (renderThinkingRow
	// returns "" when !m.turnActive), so a perpetual 10Hz tick when idle
	// just churns redraws of the live footer. Each redraw emits cursor
	// and line-clear ANSI which can invalidate active mouse selections
	// in scrollback on terminals/tmux. startTurn re-arms the tick.
	return tea.Batch(
		textarea.Blink,
		cursorBlinkCmd(),
		runProviderProbe(m.parentCtx, m.adapterConfig(m.modelName, m.baseURL), false),
		// Drain the long-lived subagent inbox for the life of the
		// program. A nil channel here is a programming error (New
		// always allocates it), so we don't guard.
		waitForSubagentInbox(m.subagentInbox),
	)
}

// Update is the public Bubbletea entry point. It delegates to update() then
// flushes any tea.Println commands appendLine queued during the tick. Single
// chokepoint keeps the rest of the model from threading Cmds by hand.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	out, cmd := m.update(msg)
	mm := out.(Model)
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
		if !m.turnActive {
			// Idle: drop the tick so we stop re-scheduling ourselves.
			// startTurn re-arms m.spinner.Tick when work begins.
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
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
	if probe, ok := msg.(connectionStatusMsg); ok {
		m.connection = probe.state
		return m, nil
	}
	if probe, ok := msg.(providerProbeMsg); ok {
		if probe.result.EndpointReachable && probe.result.AuthOK && len(probe.result.Issues) == 0 {
			m.connection = connOK
		} else if probe.result.EndpointReachable {
			m.connection = connDown
		} else {
			m.connection = connDown
		}
		if probe.result.Profile.Provider != "" {
			m.providerProfile = probe.result.Profile
		}
		if probe.announce {
			m.appendLine(formatProbeResult(probe.result))
		}
		return m, nil
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		wasReady := m.ready
		m.width = msg.Width
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
				m.appendRaw(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width))
				// One blank line of breathing room between the card and
				// the input frame — matches the Phase 2 spacing target.
				m.queuePrintln("")
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
			// characters smear into scrollback as stair-step "╭───" ghosts
			// at every prior width. tea.ClearScreen wipes the visible
			// viewport so the next View() draws clean.
			//
			// We then replay the conversation under freshly-rendered
			// startup chrome at the new width: startup box (only if
			// the session is still fresh — past the first user message
			// the status bar already carries model/provider/cwd, so
			// re-emitting the card on every resize would be redundant)
			// → every conversation line emitted so far, in order. Each
			// replay line is queued as its own per-line tea.Println via
			// queuePrintln rather than as one multi-line tea.Println —
			// Bubbletea's renderer in inline mode miscounts the row
			// delta when a single Println carries multiple
			// \n-separated lines or a line that auto-wraps, which is
			// what causes the "clipped/overpainted assistant text
			// after wrapping changes" symptom.
			m.pendingCmds = append(m.pendingCmds, tea.ClearScreen)
			if m.shouldShowStartupCard() {
				m.queuePrintln(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width))
				m.queuePrintln("")
			}
			for _, line := range m.historyLines {
				m.pendingCmds = append(m.pendingCmds, tea.Println(line))
			}
			return m, nil
		}
		return m, nil

	case tea.KeyMsg:
		if m.cheatsheetOpen {
			m.cheatsheetOpen = false
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
		if m.memoryPickerOpen {
			return m.updateMemoryPicker(msg)
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
		if m.subagentsPickerOpen {
			return m.updateSubagentsPicker(msg)
		}
		// Intercept large bracketed pastes before any other handling.
		// Bubbletea sets msg.Paste=true with all the pasted runes in a
		// single KeyMsg. For anything over the threshold, we swap the
		// runes for a short marker and stash the original — keeps the
		// cmdline from stretching to fill the screen on a 5KB paste.
		if msg.Paste && (len(msg.Runes) > pasteThreshold || strings.ContainsRune(string(msg.Runes), '\n')) {
			// Marker-out anything either large or multi-line — both stretch
			// the cmdline beyond a single readable row and we'd rather show
			// "[Pasted text #N: 5 lines, 87 bytes]" than the literal paste.
			return m.handleLargePaste(msg)
		}
		if m.turnActive && !m.awaitingApproval {
			switch msg.Type {
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
			case tea.KeyEnter:
				input := strings.TrimSpace(m.textInput.Value())
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
					// intent — drop any plain-text message that was
					// queued by an earlier Enter so we don't
					// double-submit after the turn unwinds.
					m.pendingInputAfterTurn = ""
					m.textInput.SetValue("")
					m.paletteOpen = false
					m.paletteIndex = 0
					return m.runSlash(input)
				}
				// Plain Enter on non-empty input mid-turn:
				// interrupt-with-feedback. Cancel the current iteration
				// (the agent loop's synthetic-tool_result policy keeps
				// history valid), stash the message, and let the
				// turnEndedMsg handler auto-submit it after the loop
				// unwinds. Empty Enter stays silent — Ctrl+C / Esc are
				// the explicit "stop without sending" surface.
				if input == "" {
					return m, nil
				}
				// Expand pasted-marker placeholders before stashing so
				// the queued message reaches the agent in the same
				// form as a normal submission. Mirrors the line near
				// the normal-Enter path (~1135).
				input = m.expandPastes(input)
				m.pastes = nil
				m.pendingInputAfterTurn = input
				m.textInput.SetValue("")
				m.paletteOpen = false
				m.paletteIndex = 0
				if m.turnCancel != nil {
					m.turnCancel()
				}
				return m, nil
			default:
				m.preGrowTextarea()
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(msg)
				m.fitTextareaHeight()
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
				m.decisions <- agent.PathAllowOnce
				m.appendLine(stylePathTrustAccept.Render("trusted for this write: ") + m.pathTrustReq.Path)
				m.awaitingPathTrust = false
				answered = true
			case "2", "t", "T":
				dir := filepath.Dir(m.pathTrustReq.Path)
				addAllowedPathToWriteTools(m.cfg.Registry, dir)
				m.decisions <- agent.PathTrustSession
				m.appendLine(stylePathTrustAccept.Render("trusted for this session: ") + dir)
				m.awaitingPathTrust = false
				answered = true
			case "3", "n", "N", "esc":
				m.decisions <- agent.Deny
				m.appendLine(stylePathTrustReject.Render("path trust denied — model sees error"))
				m.awaitingPathTrust = false
				answered = true
			}
			if answered {
				return m, waitForEvent(m.eventsCh, m.turnErrCh)
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
					// Approve and implement: flip plan mode off
					// BEFORE forwarding the decision so the next
					// iteration's tool dispatch sees the new state
					// and the schema filter stops advertising
					// exit_plan_mode. Normal per-tool approval
					// prompts continue for the implementation turn.
					exitPlanMode(&m)
					m.decisions <- agent.AllowOnce
					m.awaitingApproval = false
					answered = true
				case "y", "Y":
					// Approve and auto-implement: exit plan mode
					// AND enter auto mode for the implementation.
					// Per-tool prompts auto-allow for the rest of
					// the turn except the safety floor (run_bash,
					// git_commit, git_checkpoint, rollback).
					exitPlanMode(&m)
					if m.cfg.AutoMode != nil {
						m.cfg.AutoMode.Active.Store(true)
						m.appendLine(styleAutoBannerLabel.Render(AutoModeIcon+" auto mode active") +
							" " + styleAutoBannerHint.Render("— implementing the approved plan; bash & commits still prompt"))
					}
					m.decisions <- agent.AllowOnce
					m.awaitingApproval = false
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
					m.decisions <- agent.SaveForLater
					m.awaitingApproval = false
					answered = true
				case "k", "K", "n", "N", "esc":
					// Keep planning: log a dismissal header so the
					// user sees what they just did. Plan body is
					// already in scrollback from
					// emitPlanBodyToScrollback (above the modal).
					m.appendLine(stylePlanBannerLabel.Render(PlanModeIcon+" plan kept") +
						" " + stylePlanBannerHint.Render("— revise and call exit_plan_mode again when ready"))
					m.decisions <- agent.Deny
					m.awaitingApproval = false
					answered = true
				}
				if answered {
					return m, waitForEvent(m.eventsCh, m.turnErrCh)
				}
				return m, nil
			}
			answered := false
			switch msg.String() {
			case "y", "Y":
				m.decisions <- agent.AllowOnce
				m.awaitingApproval = false
				answered = true
			case "a", "A":
				if m.approvalAllowAlwaysOK {
					rule := m.approvalDerivedRule
					m.decisions <- agent.AllowAlways
					m.awaitingApproval = false
					answered = true
					// Toast — keeps the modal itself focused on
					// the immediate decision; the receipt of what
					// got persisted lands in scrollback so the
					// user can see exactly which rule was added.
					if rule != "" {
						m.appendLine(approvalToast(rule))
					}
				}
			case "n", "N", "esc":
				m.decisions <- agent.Deny
				m.awaitingApproval = false
				answered = true
			}
			if answered {
				return m, waitForEvent(m.eventsCh, m.turnErrCh)
			}
			return m, nil
		}

		if m.paletteOpen {
			switch msg.Type {
			case tea.KeyUp:
				if m.paletteIndex > 0 {
					m.paletteIndex--
				}
				return m, nil
			case tea.KeyDown:
				if m.paletteIndex < len(m.paletteFiltered)-1 {
					m.paletteIndex++
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
				return m, nil
			}
			return m, tea.Quit
		case tea.KeyCtrlD:
			return m, tea.Quit
		case tea.KeyEsc:
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
			// Claude Code's Shift+Tab. Suppressed while a palette
			// overlay is open (the slash/file palette branch above
			// intercepts navigation keys but lets unknown keys fall
			// through to here) or mid-turn (flipping while the
			// in-flight iteration's gate decisions are in motion
			// would be confusing).
			if m.turnActive || m.paletteOpen || m.filePaletteOpen {
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
					return m, nil
				}
				input = "/" + chosen.Name
			}
			if input == "" {
				return m, nil
			}
			m.textInput.SetValue("")
			m.paletteOpen = false
			m.paletteIndex = 0
			m.dropFilePalette()
			// Swap any [Pasted text #N: ...] markers back for their
			// original content before the message hits the agent.
			input = m.expandPastes(input)
			m.pastes = nil
			if strings.HasPrefix(input, "/") {
				return m.runSlash(input)
			}
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
				}
				m.filePaletteOpen = false
			} else {
				m.paletteOpen = false
				m.paletteIndex = 0
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

	case tea.MouseMsg:
		// Mouse capture stays enabled (so palette clicks could be wired up
		// later); we no longer forward to a viewport because the conversation
		// lives in the terminal's native scrollback. Wheel events fall
		// through to the terminal — Shift+drag selects text in scrollback as
		// the terminal owns it.
		return m, nil

	case turnEndedMsg:
		m.turnActive = false
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
		if err := m.sess.Save(); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("⚠ session save failed: %v", err)))
		}
		if m.recall != nil {
			if err := m.recall.IndexSession(m.sess); err != nil {
				m.appendLine(styleError.Render(fmt.Sprintf("⚠ recall index failed: %v", err)))
			}
		}
		// Watermark check: post-turn, before the next prompt is
		// accepted. Either fires a yellow notice (warn threshold) or
		// returns a Cmd that runs auto-summarization (auto threshold).
		ctxCmd := m.updateContextUsage()
		// If the user interrupted mid-turn by hitting Enter on a
		// non-empty message, agent.Turn has already preserved history
		// with synthetic tool_result entries — submit the queued
		// message now as a fresh turn so the model sees the feedback.
		// Run after sess.Save / index / watermark so any post-turn
		// warning lands above the new user block. Watermark Cmd is
		// dropped here: the queued submission is the user's explicit
		// next-turn intent, and stacking an auto-summarize Cmd in
		// front would yank context out from under the very message
		// they just sent. If watermark guidance matters, the user
		// will see it after the followup turn ends.
		if queued := m.pendingInputAfterTurn; queued != "" {
			m.pendingInputAfterTurn = ""
			next, cmd := m.startTurn(queued)
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

	case summaryDoneMsg:
		m.summarizing = false
		if msg.err != nil {
			// Failed attempts don't count as "already handled at this fill
			// level" — clearing the watermark lets the next turn-end
			// retry. Without this the gate in updateContextUsage stays
			// closed forever and we silently stop attempting compression.
			m.lastWatermarkPct = 0
			m.appendLine(styleError.Render("[summarize] " + msg.err.Error()))
			return m, nil
		}
		m.sess.Messages = msg.newMessages
		m.lastWatermarkPct = 0
		// Refresh estimated tokens from the new history so the status
		// bar drops immediately.
		m.contextTokens = contextwindow.EstimateTokens(m.sess.Messages)
		if err := m.sess.Save(); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("⚠ session save after summarize: %v", err)))
		}
		// Index the compressed session so /recall continues to surface
		// summary content from the now-shorter history.
		if m.recall != nil {
			if err := m.recall.IndexSession(m.sess); err != nil {
				m.appendLine(styleError.Render(fmt.Sprintf("⚠ recall index after summarize: %v", err)))
			}
		}
		if msg.auto {
			m.appendLine(styleWatermarkBox.Render(fmt.Sprintf(
				"⚡ Context auto-summarized.\nFull history saved to %s\nUse /recall <id> to search the compressed session.",
				abbrevHome(msg.snapshotPath))))
		} else {
			m.appendLine(styleAuto.Render(fmt.Sprintf(
				"[summarize] compressed history (~%d → ~%d tokens). Snapshot: %s",
				msg.tokensBefore, m.contextTokens, abbrevHome(msg.snapshotPath))))
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
	if m.permissionsOpen {
		return m.renderInlineOverlay(renderPermissionsOverlay(m))
	}
	if m.modelPickerOpen && m.modelPicker != nil {
		return m.renderInlineOverlay(renderModelPicker(m.modelPicker, m.width))
	}
	if m.providerPickerOpen && m.providerPicker != nil {
		return m.renderInlineOverlay(renderProviderPicker(m.providerPicker, m.width))
	}
	if m.memoryPickerOpen && m.memoryPicker != nil {
		return m.renderInlineOverlay(renderMemoryPicker(m.memoryPicker, m.width))
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
		} else if preview := m.renderStreamingPreview(); preview != "" {
			parts = append(parts, preview)
		}
	}
	if card := m.renderLivePlanCard(); card != "" {
		// Trailing blank gives the card breathing room from the
		// thinking row below (during a turn) or the input box
		// (between turns). Without it the card's `╰ plan updated`
		// footer sits flush against the spinner row.
		parts = append(parts, card, "")
	}
	if m.turnActive {
		parts = append(parts, m.renderThinkingRow(), "")
	}

	if m.awaitingPathTrust {
		// Prompt 2: inline path-trust elevation modal. Mutually
		// exclusive with the regular approval modal — only one of
		// awaitingPathTrust / awaitingApproval is ever true.
		parts = append(parts, renderPathTrustModal(m))
	} else if m.awaitingApproval {
		// exit_plan_mode gets its own decision card — just the four
		// hotkeys in a bordered box. The plan body itself is emitted
		// to scrollback above this box when ApprovalNeeded fires (see
		// handleAgentEvent), so the body persists naturally after the
		// modal dismisses and isn't cramped inside the box.
		if m.approvalTool == "exit_plan_mode" {
			parts = append(parts, renderPlanApprovalCard(m.width))
		} else {
			parts = append(parts, renderApprovalModal(m))
		}
	} else {
		if m.paletteOpen {
			parts = append(parts, renderPalette(m.paletteFiltered, m.paletteIndex, liveContentWidth(m.width)+4))
		}
		if m.filePaletteOpen {
			parts = append(parts, renderFilePalette(m.filePaletteFiltered, m.filePaletteIndex, m.filePaletteOffset, liveContentWidth(m.width)+4))
		}
		// Banner: one-line indicator above the input rule. Modes
		// (plan/auto) are mutually exclusive; yolo is an orthogonal
		// overlay flag whose `⚠ yolo` tag appends to the active mode
		// banner. When no mode is active but yolo is, a standalone
		// yolo banner shows so the user can still see the "rails
		// off" state. Suppressed entirely while a palette is open
		// (palettes already own the above-cmdline real estate).
		yoloOn := m.cfg.YoloMode.IsActive()
		switch {
		case m.paletteOpen, m.filePaletteOpen:
			// suppressed
		case m.cfg.PlanMode.IsActive():
			parts = append(parts, renderPlanModeBanner(computePlanBannerInfo(m), yoloOn, m.width))
		case m.cfg.AutoMode.IsActive():
			parts = append(parts, renderAutoModeBanner(yoloOn, m.width))
		case yoloOn:
			parts = append(parts, renderYoloStandaloneBanner(m.width))
		}
		// Bracket the input with thin dim rules above and below — gives
		// the cmdline visible containment without the visual weight of
		// a full rounded box. The hint line that used to ride below is
		// now inlined into the placeholder row when input is empty.
		parts = append(parts, m.renderInputRule(), m.renderInputBox(), m.renderInputRule())
	}

	// Status bar tucks immediately below the bottom rule. Earlier
	// versions inserted a blank line for breathing room — turned out
	// to leave the bar looking detached and floating; cuddling it
	// against the rule reads as "this is the chrome below the
	// cmdline" without ambiguity.
	parts = append(parts, m.renderStatus())
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
		m.renderInputRule(),
		m.renderInputBox(),
		m.renderInputRule(),
		m.renderStatus(),
		overlayRule,
		body,
	)
}

// renderInputBox renders the cmdline as a borderless input row capped at
// `min(120, terminalWidth - 4)`. The earlier design wrapped this in a
// saturated rounded border — the container ended up louder than its
// content, so the border is gone. The chevron prompt (Accent + bold) +
// dim placeholder/content carry the focal weight on their own.
//
// We render the value ourselves rather than using m.textInput.View()
// because Bubbles textarea sizes Height by *logical* lines, not wrapped
// visual rows — long single-line input either gets clipped (Height=1,
// horizontal scroll) or padded with empty "❯ " rows below the cursor
// (Height=N visual rows). Wrapping the value with ansi.Hardwrap and
// rendering each row with a prompt-or-indent prefix gets us the "wrap
// below within the cap" behavior the cmdline actually wants. The
// textarea still owns the value and cursor state via its own Update;
// we just paint it differently.
func (m Model) renderInputBox() string {
	if m.width <= 0 {
		return m.textInput.View()
	}
	contentW := inputContentWidth(m.width)
	return m.renderInputBody(contentW)
}

// renderInputRule paints a single dim horizontal `─` line spanning
// the full terminal width. Used as the top + bottom bracket around
// the cmdline so the input has visual containment without the
// weight of a full rounded box. Spans edge-to-edge (not the
// inputContentWidth cap the input itself uses) so the bracket reads
// as a screen-wide divider rather than a narrow underline floating
// in dead space. Color is colorRule (Muted, dark gray) — chrome,
// not content; the welcome card's border matches it so the two
// surfaces read as part of the same chrome family.
func (m Model) renderInputRule() string {
	w := m.width
	if w < 1 {
		w = 1
	}
	return lipgloss.NewStyle().Foreground(colorRule).Render(strings.Repeat("─", w))
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

// inputBoxFrameWidth is retained for any caller that still needs the
// outer-frame width of a hypothetical bordered cmdline (overlays size
// themselves relative to it). The live cmdline render no longer paints a
// border, so this is purely a sizing helper now.
func inputBoxFrameWidth(terminalWidth int) int {
	w := terminalWidth - 2
	if w < 1 {
		w = 1
	}
	return w
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
// border (2) and padding (2). No cap — the box stretches to the terminal edge.
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
	head := fmt.Sprintf("↻ fallback: %s → %s", e.From, e.To)
	if e.Policy != "" {
		head += " [" + e.Policy + "]"
	}
	if e.Reason != "" {
		head += ": " + e.Reason
	}
	return lipgloss.NewStyle().Foreground(colorWarm).Bold(true).Render(head)
}

func renderProviderToolLine(toolName, phase, detail string) string {
	head := fmt.Sprintf("▸ %s", toolName)
	switch phase {
	case "searching", "interpreting", "in_progress":
		head += " [" + phase + "]"
	case "completed":
		head += " [done]"
	case "code":
		head += " [code]"
	}
	if detail != "" {
		head += ": " + detail
	}
	return styleToolCall.Render(head)
}

func renderToolResultLine(summary string, errored bool) string {
	if errored {
		return styleError.Render("  ↳ " + summary)
	}
	return styleToolMeta.Render("  ↳ " + summary)
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

func (m Model) activatePaletteSelection() (Model, tea.Cmd) {
	if len(m.paletteFiltered) == 0 {
		return m, nil
	}
	chosen := m.paletteFiltered[m.paletteIndex]
	if chosen.Args != "" {
		m.textInput.SetValue("/" + chosen.Name + " ")
		m.textInput.CursorEnd()
		m.paletteOpen = false
		m.paletteIndex = 0
		return m, nil
	}
	m.textInput.SetValue("")
	m.paletteOpen = false
	m.paletteIndex = 0
	return m.runSlash("/" + chosen.Name)
}

// pasteThreshold is the byte count above which a bracketed paste gets
// hidden behind a placeholder marker instead of being inserted into the
// input box verbatim. Tuned to "would this paste obviously stretch the
// cmdline beyond a couple of lines?": ~200 chars is roughly 2 wrapped
// lines on an 80-col terminal, which feels like the right cutoff
// between "small enough to read inline" and "annoying."
const pasteThreshold = 200

// handleLargePaste swaps a big bracketed paste for a short placeholder
// marker. The original content is stashed in m.pastes keyed by the
// marker text; expandPastes() puts it back on submit.
func (m Model) handleLargePaste(msg tea.KeyMsg) (Model, tea.Cmd) {
	content := string(msg.Runes)
	m.pasteSeq++
	lines := strings.Count(content, "\n") + 1
	marker := fmt.Sprintf("[Pasted text #%d: %d lines, %d bytes]", m.pasteSeq, lines, len(content))
	if m.pastes == nil {
		m.pastes = map[string]string{}
	}
	m.pastes[marker] = content
	// Insert the marker into the textarea via a synthetic non-paste
	// KeyMsg. We strip Paste:true so the textarea treats this as
	// regular typed input — a bracketed-paste flag would re-trigger
	// any future paste-aware logic and we want the marker treated as
	// plain text.
	syn := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(marker)}
	m.preGrowTextarea()
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(syn)
	m.fitTextareaHeight()
	return m, cmd
}

// expandPastes swaps any placeholder markers present in s for their
// stashed full content. No-op when there are no markers — safe to call
// on every submission.
func (m Model) expandPastes(s string) string {
	if len(m.pastes) == 0 {
		return s
	}
	for marker, content := range m.pastes {
		s = strings.ReplaceAll(s, marker, content)
	}
	return s
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
// Segment 1 carries the connection-state dot (the only saturated
// element on the bar), the model name in Content, and the provider
// profile name in Dim (sub-separator). Segment 2 is the visual
// context-window indicator — see renderContextBar for thresholds.
//
// The working directory is intentionally NOT shown here — it ate too
// much horizontal space relative to its at-a-glance value, and the
// splash header still surfaces it once at startup.
//
// On a narrow terminal the cascade is: drop the provider tag, then
// strip the vendor prefix on the model name. The status dot, model,
// and context bar are never dropped — they're the most critical
// at-a-glance signals.
func (m Model) renderStatus() string {
	dot := renderConnDot(m.connection)
	model := renderModelName(m.modelName)
	tag := m.providerLabel
	if tag == "" {
		tag = m.provider
	}
	provider := renderProviderTag(tag)
	ctx := m.renderContextBar()

	sep := lipgloss.NewStyle().Foreground(colorRule).Render("  ·  ")
	innerSep := lipgloss.NewStyle().Foreground(colorRule).Render(" · ")

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

	build := func(head string) string {
		segs := []string{head}
		if ctx != "" {
			segs = append(segs, ctx)
		}
		return " " + strings.Join(segs, sep)
	}

	w := m.width
	if w <= 0 {
		// No size info yet — render the full layout; truncation kicks in
		// once the first WindowSizeMsg lands.
		return build(first)
	}

	line := build(first)
	if lipgloss.Width(line) <= w {
		return line
	}
	// Drop the provider tag first.
	first = dot + "  " + model
	line = build(first)
	if lipgloss.Width(line) <= w {
		return line
	}
	// Still too wide — strip the vendor prefix from the model (e.g.
	// `nvidia/nemotron-…` → `nemotron-…`).
	if idx := strings.LastIndex(m.modelName, "/"); idx >= 0 && idx < len(m.modelName)-1 {
		first = dot + "  " + renderModelName(m.modelName[idx+1:])
	}
	return build(first)
}

// renderProviderTag styles the provider label that sits next to the
// model name in the status bar. Dim — the model is the primary
// signal; provider is supporting context. Empty string when the
// provider is unknown so the status bar can omit it cleanly.
func renderProviderTag(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colorDim).Render(provider)
}

// renderModelName styles the model-name segment of the status bar.
// Plain Content (off-white) rather than Accent — the dot already does
// the "active" job, so painting the model loud too creates competition
// for the eye.
func renderModelName(name string) string {
	return styleStatusModel.Render(name)
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
	// Per-turn retrieval: re-render the system prompt with memory
	// bodies scored against this turn's user input. USER.md /
	// YOTTACODE.md and both MEMORY.md indexes always inject in full;
	// only per-entry memory bodies are filtered. See cmd_retrieval.go
	// for the rebuild logic. Soft on failure — a disk error leaves
	// the existing prompt intact and the turn proceeds.
	m.rebuildSystemPromptForTurn(input)

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

	m.sess.Messages = append(m.sess.Messages, adapter.Message{
		Role:    adapter.RoleUser,
		Content: input,
	})
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
	m.appendLine(renderUserBlock(rendered))
	m.recordHistory(input)

	turnCtx, cancel := context.WithCancel(m.parentCtx)
	turnCtx = agent.WithCheckpoint(turnCtx, m.sess.ID, checkpointID)
	m.turnCancel = cancel
	m.turnActive = true
	m.turnStart = time.Now()
	m.turnTokens = 0
	m.turnToolCalls = 0
	// Per-turn meta surfaced in the thinking row resets to zero so
	// nothing carries over from a previous turn.
	m.iterRound = 0
	m.iterMax = 0
	m.toolsRequested = 0
	m.toolsStarted = 0
	m.eventsCh = make(chan agent.Event, 64)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)

	go func(ev chan agent.Event, dec chan agent.Decision, errCh chan error) {
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
	case agent.ProviderToolCall:
		m.reasoning.Reset()
		m.appendLine("")
		m.appendLine(renderProviderToolLine(e.ToolName, e.Phase, e.Detail))
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
		m.appendLine(styleAuto.Render(fmt.Sprintf("[%s] %s", e.Source, summary)))
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
				// The decision channel may not have a slot yet — the
				// loop sends ApprovalNeeded and then reads from
				// decisions. We're synchronous in Update; the receiver
				// is already blocking. Sending here is safe.
				m.decisions <- agent.Deny
				return m, waitForEvent(m.eventsCh, m.turnErrCh)
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
		if rule, ok := permissions.DeriveAllowRule(e.ToolName, e.ArgsJSON, m.cwd); ok && m.perms != nil {
			m.approvalAllowAlwaysOK = true
			m.approvalDerivedRule = rule
		} else {
			m.approvalAllowAlwaysOK = false
			m.approvalDerivedRule = ""
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
		m.statsToolCalls++
		m.toolsStarted++
		m.turnToolCalls++
	case agent.ToolResult:
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
		m.appendLine(renderToolCard(e.ToolName, preview, m.pendingToolArgs, e.Output, e.Errored, m.width))
		m.pendingToolName = ""
		m.pendingToolPreview = ""
		m.pendingToolArgs = ""
		// Reset streamingMode so the next ContentToken's "first content
		// after non-content" branch fires and inserts a blank line
		// between the just-rendered tool card and the resumed
		// assistant body. Without this, streamingMode stays at
		// streamContent from before the tool call, the blank-line
		// guard is skipped, and the assistant's content lands tight
		// against the card's `╰ done` footer with no breathing room.
		m.streamingMode = streamIdle
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
	case agent.ErrorEvent:
		m.commitStreaming()
		// Multi-line errors (e.g. 429 with retry-after hint) render
		// each line with its own ✗ prefix so the second line
		// doesn't look orphaned in scrollback.
		for _, line := range strings.Split(strings.TrimRight(e.Err.Error(), "\n"), "\n") {
			m.appendLine(styleError.Render("✗ " + line))
		}
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
			m.appendLine("")
			m.appendLine(renderTodoCardFromTodos(m.livePlan, m.width))
			m.livePlan = nil
		}
		m.livePlanTouched = false
		// Footnote: how long the turn took, end-to-end (model
		// thinking + tool execution). Rendered in the same
		// dim/italic style as other inline notices so it fades into
		// scrollback as a quiet receipt.
		if !m.turnStart.IsZero() {
			m.appendLine(styleTurnFooter.Render("› Thought for " + formatDuration(time.Since(m.turnStart))))
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
		m.appendLine("")
		m.appendLine(renderSubagentBackgroundDone(e))
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
		} else {
			m.appendLine(renderAssistantBlock(m.streaming.String()))
		}
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
		m.inCodeBlock = true
		m.codeBlockLang = lang
		return
	}
	m.appendLine(renderAssistantBlock(line))
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

// renderUserBlock formats a user message for scrollback emission. Each
// line gets a thin colored left bar (▎) in Theme.Accent followed by the
// content in Dim — the user already knows what they typed, so the bar
// is enough of an anchor when scrolling back. Leading "\n" gives the
// block one blank line of separation from the preceding emission. No
// horizontal rule above: the bar is enough of an anchor, and the rule
// fought with content on either side.
func renderUserBlock(content string) string {
	bar := styleUserBar.Render("▎ ")
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, bar+styleUserBody.Render(line))
	}
	return "\n" + strings.Join(lines, "\n")
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
	inlineCodeRE          = regexp.MustCompile("`[^`]+`")
	boldRE                = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// inlinePathRE runs *after* inlineCodeRE has already injected ANSI
	// escapes into the line, so the character class for the first
	// alternative must exclude the ESC byte (\x1b) — otherwise the
	// greedy `[^...]+` swallows the trailing `\x1b[0m` reset of the
	// inline-code styling into the path match. Lipgloss then re-renders
	// the chunk with those bytes still inside, and the embedded `[0m`
	// (with its ESC eaten by adjacent escape sequences) leaks into the
	// terminal as visible literal text.
	inlinePathRE = regexp.MustCompile("(^|[\\s(])((?:\\./|\\../|~/|/)[^\\s:;,)\\]\x1b]+|[A-Za-z0-9._/-]+\\.(?:go|md|txt|json|ya?ml|toml|ts|tsx|js|jsx|py|rs|sh|bash|zsh|sql|css|html|xml))")
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
	return strings.Contains(trim, ":=") ||
		strings.Contains(trim, "->") ||
		strings.Contains(trim, "::") ||
		strings.Contains(trim, "{") ||
		strings.Contains(trim, "}") ||
		(strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t"))
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

func guessInlineCodeLanguage(line string) string {
	trim := strings.TrimSpace(line)
	switch {
	case looksLikeShellCommand(trim):
		return "bash"
	case strings.HasPrefix(trim, "func ") || strings.HasPrefix(trim, "package ") || strings.Contains(trim, ":="):
		return "go"
	case strings.HasPrefix(trim, "def ") || strings.HasPrefix(trim, "import "):
		return "python"
	case strings.HasPrefix(trim, "SELECT ") || strings.HasPrefix(trim, "INSERT ") || strings.HasPrefix(trim, "UPDATE ") || strings.HasPrefix(trim, "DELETE "):
		return "sql"
	default:
		return ""
	}
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
	width := m.width
	if width <= 0 {
		width = 80
	}
	const clearLine = "\r\x1b[2K"
	for _, line := range strings.Split(s, "\n") {
		if line == "" || ansi.StringWidth(line) <= width {
			m.pendingCmds = append(m.pendingCmds, tea.Println(clearLine+line))
			continue
		}
		wrapped := ansi.Hardwrap(line, width, true)
		for _, row := range strings.Split(wrapped, "\n") {
			m.pendingCmds = append(m.pendingCmds, tea.Println(clearLine+row))
		}
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
