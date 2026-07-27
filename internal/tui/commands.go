package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/dotenv"
	"github.com/yottadynamics/yottacode/internal/experimental"
	"github.com/yottadynamics/yottacode/internal/lsp"
	"github.com/yottadynamics/yottacode/internal/providerops"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

// slashCommand binds a name to a help blurb and a handler.
type slashCommand struct {
	Name string
	Args string // for help rendering only
	Help string
	Run  func(m Model, args []string) (Model, tea.Cmd)

	// PreservesTurn marks commands that are safe to invoke while a
	// turn is active without canceling it. The default cancel-on-
	// slash behavior is right for state-changing commands
	// (/clear, /model, /sessions, /provider) — the user is signaling
	// "stop the current work, apply this change instead." But
	// informational commands (/subagents, /help, /system,
	// /permissions) just inspect read-only snapshots; canceling the
	// turn to render them is destructive. Those opt in via this
	// field so the Enter-on-slash handler can skip turnCancel().
	PreservesTurn bool

	// Source is the absolute path to the markdown file a custom
	// command was loaded from. Empty for built-ins. /help renders the
	// source path next to custom-command entries so users can find
	// and edit the file behind a command they invoked.
	Source string

	// IsCustom is true for commands loaded from ~/.yottacode/commands/
	// or <cwd>/.yottacode/commands/ via the usercmd package. Drives
	// the /help two-section split (built-ins first, custom second).
	IsCustom bool
}

// allSlash is the registry. Order = display order in /help and the palette.
// Populated in init() because some handlers (cmdHelp) iterate over allSlash,
// which would otherwise produce a Go init-cycle.
var allSlash []slashCommand

func init() {
	// Ordered by reach frequency during an active session:
	// workflow → config → git → utilities → meta.
	allSlash = []slashCommand{
		// Workflow — most reached-for during active coding.
		// Auto mode is intentionally NOT slash-invocable (mirroring
		// Claude Code): auto via Shift+Tab or --permission-mode auto.
		{Name: "plan", Help: "toggle plan mode — also Shift+Tab. Type `/plan list` to resume an earlier plan.", Run: cmdPlan},
		{Name: "model", Help: "open the model picker (subcommands: list [all], <name>)", Run: cmdModel},
		{Name: "provider", Help: "select a new provider (subcommands: list, use, add, remove, models)", Run: cmdProviderEntry},
		{Name: "effort", Help: "set reasoning effort for providers that support it (default · low · medium · high)", Run: cmdEffort},
		{Name: "router", Help: "show or toggle cache-safe model routing between fast and smart (subcommands: on, off)", Run: cmdRouter},
		{Name: "sessions", Help: "open the sessions menu (or /sessions <id|name> to resume directly)", Run: cmdSessions},
		// No Args: a bare /memory must execute on Enter (one keystroke) to
		// open the picker. The `search` subcommand is surfaced in Help and
		// still works when typed manually (`/memory search <query>`) —
		// same pattern as /plan, /model, /sessions.
		{Name: "memory", Help: "open the memory picker; `/memory search <q>` ranks saved memories", Run: cmdMemory},
		{Name: "map", Args: "[query]", Help: "open the code map: directory → file → symbol structure", Run: cmdMap, PreservesTurn: true},
		{Name: "video", Args: "[path]", Help: "guide a marketing-video workflow", Run: cmdVideo},
		{Name: "summarize", Help: "compress session history into a structured summary", Run: cmdSummarize},
		{Name: "skills", Help: "open the skills menu", Run: cmdSkills, PreservesTurn: true},
		{Name: "subagents", Help: "open the subagents picker (Enter views · t toggles types · s stops · Esc closes)", Run: cmdSubagents, PreservesTurn: true},
		{Name: "init", Help: "draft .yottacode/YOTTACODE.md from the current repo", Run: cmdInit},
		{Name: "permissions", Help: "show where permissions are configured", Run: cmdPermissions, PreservesTurn: true},
		{Name: "theme", Help: "change the theme", Run: cmdThemes, PreservesTurn: true},
		{Name: "loop", Args: "<dur> [Nx] <prompt>", Help: "repeat on interval; stop <id> or stop all", Run: cmdLoop, PreservesTurn: true},

		// Git workflow.
		// Palette order mirrors the daily flow: commit → push →
		// create PR → update PR, then the review/implement pair.
		{Name: "git-commit", Help: "compose and run a one-line git commit", Run: cmdGitCommit},
		{Name: "git-push", Help: "push the current branch to origin (sets upstream on first push; surfaces the PR URL when one exists)", Run: cmdGitPush},
		{Name: "git-create-pr", Args: "[base]", Help: "open a pull request for the current branch", Run: cmdGitCreatePR},
		{Name: "git-update-pr", Args: "[ref]", Help: "refresh a PR's title and body to match the current commit list", Run: cmdGitUpdatePR},
		{Name: "git-create-issue", Args: "[title]", Help: "create a GitHub issue in the current repo", Run: cmdGitCreateIssue},
		{Name: "git-review-pr", Args: "[ref]", Help: "review a pull request (number or branch; defaults to current branch's PR)", Run: cmdGitReviewPR},
		{Name: "code-review", Help: "multi-agent review of the current diff — effort low · medium · high (background subagents GA)", Run: cmdCodeReview},
		{Name: "git-implement-issue", Args: "<n>", Help: "implement a GitHub issue end-to-end: fetch → plan → branch → code → tests → commit → push → draft PR", Run: cmdGitImplementIssue},
		// /mcp inspects the live MCP server manager: list configured
		// servers, their start status + tool counts, and dump stderr
		// from a misbehaving one. PreservesTurn=true: read-only on
		// the manager state; safe to invoke mid-turn.
		{Name: "mcp", Help: "Manage MCP servers", Run: cmdMCP, PreservesTurn: true},

		// Utilities.
		{Name: "clear", Help: "start a fresh session (current is saved)", Run: cmdClear},
		{Name: "system", Help: "show the active system prompt", Run: cmdSystem, PreservesTurn: true},
		{Name: "context", Help: "show context window usage breakdown", Run: cmdContext, PreservesTurn: true},
		{Name: "experimental", Help: "list experimental features and which are enabled this session", Run: cmdExperimental, PreservesTurn: true},
		{Name: "usage", Help: "show per-session token usage, today's rollup, and estimated cost", Run: cmdUsage, PreservesTurn: true},
		{Name: "doctor", Help: "probe provider auth and model access", Run: cmdDoctor, PreservesTurn: true},
		{Name: "redo", Help: "edit and re-run the most recent message", Run: cmdRedo},
		{Name: "recall", Args: "<query>", Help: "full-text search across every saved session", Run: cmdRecall, PreservesTurn: true},
		{Name: "checkpoints", Help: "open the checkpoints picker — also Esc Esc", Run: cmdCheckpoints},
		{Name: "max-iterations", Args: "<N>", Help: "cap tool-call iterations per turn (default: 100; auto mode 4×)", Run: cmdMaxIterations},
		{Name: "setup", Help: "re-run the setup wizard (reloads config on return)", Run: cmdSetup},

		// Meta — always last.
		// Yolo enters via --yolo at startup AND /yolo mid-session; keep it near
		// the bottom of the palette because it is a dangerous overlay, not a
		// routine workflow command.
		{Name: "yolo", Help: "toggle yolo mode — NO safety floor; deny rules still win", Run: cmdYolo},
		{Name: "help", Help: "show this list", Run: cmdHelp, PreservesTurn: true},
		{Name: "quit", Help: "exit yottacode", Run: cmdQuit},
	}
}

// findSlash searches the built-in registry only. Used by registry
// coverage tests that lock the built-in set ("is /init registered?",
// "is /auto NOT registered?") and don't care about a session's
// custom commands. The dispatcher uses the Model-bound variant below
// so it can see custom commands too.
func findSlash(name string) *slashCommand {
	for i := range allSlash {
		if allSlash[i].Name == name {
			return &allSlash[i]
		}
	}
	return nil
}

// findSlash (method) walks built-ins first, then the model's custom
// commands, then the model's skill-derived commands, returning the
// first name match (or nil). Built-ins always shadow custom commands
// and skills; custom commands shadow skills. The usercmd loader and
// the skills loader both refuse to register a name that would
// collide with a built-in, so the second layer of defense here is
// belt-and-suspenders for any future loader drift.
func (m *Model) findSlash(name string) *slashCommand {
	if c := findSlash(name); c != nil {
		return c
	}
	for i := range m.customSlash {
		if m.customSlash[i].Name == name {
			return &m.customSlash[i]
		}
	}
	for i := range m.skillSlash {
		if m.skillSlash[i].Name == name {
			return &m.skillSlash[i]
		}
	}
	return nil
}

// runSlash dispatches based on an input line ("/foo arg1 arg2"), recording
// it in input history first. This is the user-typed entry point.
func (m Model) runSlash(input string) (Model, tea.Cmd) {
	if fields := strings.Fields(input); len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return m, nil
	}
	// Record the slash invocation in input history so ↑ recalls it on
	// the next prompt. startTurn already records prose submissions; we
	// mirror that here so / commands are recall-able too. Recording
	// before dispatch covers unknown commands (typos) as well — the
	// user usually wants to recall and fix them.
	m.recordHistory(input)
	return m.dispatchSlash(input)
}

// dispatchSlash resolves and runs a "/foo arg1 arg2" line WITHOUT recording
// it in input history. runSlash wraps this for user-typed commands; the
// /loop scheduler calls it directly so a repeating loop doesn't stuff the
// same command into ↑-history on every iteration.
func (m Model) dispatchSlash(input string) (Model, tea.Cmd) {
	fields := strings.Fields(input)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return m, nil
	}
	name := strings.TrimPrefix(fields[0], "/")
	cmd := m.findSlash(name)
	if cmd == nil {
		m.appendLine(styleError.Render(fmt.Sprintf("unknown command: /%s — try /help", name)))
		return m, nil
	}
	return cmd.Run(m, fields[1:])
}

// --- handlers --------------------------------------------------------------

// cmdHelp prints the command list, and re-emits the startup card so
// the user gets a refreshed full context summary mid-session. The
// card hides its onboarding tip on non-fresh sessions (startupTip()
// returns "" past the first user message), so on resumed sessions
// /help shows just the bare context summary above the help list.
func cmdHelp(m Model, _ []string) (Model, tea.Cmd) {
	m.appendLine(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.sess.ID, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width, m.experimentalEnabled...))

	// Compute one shared column width across BOTH built-ins and custom
	// commands so the help text dashes line up across the two
	// sections — avoids the visual jaggedness of two independent
	// column widths.
	leftFor := func(c slashCommand) string {
		left := "/" + c.Name
		if c.Args != "" {
			left += " " + c.Args
		}
		return left
	}
	width := 0
	for _, c := range allSlash {
		if w := len(leftFor(c)); w > width {
			width = w
		}
	}
	for _, c := range m.customSlash {
		if w := len(leftFor(c)); w > width {
			width = w
		}
	}
	for _, c := range m.skillSlash {
		if w := len(leftFor(c)); w > width {
			width = w
		}
	}

	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, c := range allSlash {
		b.WriteString(fmt.Sprintf("  %-*s   %s\n", width, leftFor(c), c.Help))
	}
	if len(m.customSlash) > 0 {
		b.WriteString("\nCustom commands:\n")
		for _, c := range m.customSlash {
			line := fmt.Sprintf("  %-*s   %s", width, leftFor(c), c.Help)
			if c.Source != "" {
				line += styleMeta.Render("  ·  " + displayPath(c.Source, m.cwd))
			}
			b.WriteString(line + "\n")
		}
	}
	if len(m.skillSlash) > 0 {
		b.WriteString("\nSkills:\n")
		for _, c := range m.skillSlash {
			line := fmt.Sprintf("  %-*s   %s", width, leftFor(c), c.Help)
			if c.Source != "" {
				line += styleMeta.Render("  ·  " + displayPath(c.Source, m.cwd))
			}
			b.WriteString(line + "\n")
		}
	}
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return m, nil
}

// displayPath shortens an absolute path for readability: replaces the
// home prefix with "~" and the cwd prefix with "." when applicable.
// Falls back to the absolute path when neither prefix matches. Also
// handles the exact-match cases (abs == cwd → ".", abs == home → "~")
// so a write/read/list of the project root itself renders cleanly.
func displayPath(abs, cwd string) string {
	if cwd != "" {
		if abs == cwd {
			return "."
		}
		if strings.HasPrefix(abs, cwd+"/") {
			return "./" + strings.TrimPrefix(abs, cwd+"/")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if abs == home {
			return "~"
		}
		if strings.HasPrefix(abs, home+"/") {
			return "~/" + strings.TrimPrefix(abs, home+"/")
		}
	}
	return abs
}

// shortenCwdInText replaces occurrences of cwd inside a freeform string
// (e.g. a shell command body, a tool's output line) with ".", and
// occurrences of $HOME with "~". The match is word-boundary aware so
// `/cwd` doesn't accidentally hit `/cwd-suffix/...` paths that share
// the same prefix.
//
// Display-only — never mutates what the agent sends to a tool. Used by
// run_bash command rendering (approval modal + header), tool card
// header path rendering, grep/glob result body lines, and footers that
// bake in absolute paths.
func shortenCwdInText(s, cwd string) string {
	if cwd != "" {
		s = replaceAtBoundary(s, cwd, ".")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = replaceAtBoundary(s, home, "~")
	}
	return s
}

// replaceAtBoundary replaces every occurrence of `old` in `s` with `new`,
// but only when the surrounding characters can't extend the match into
// a different token. A path-continuation char (alphanumeric, `_`, `-`,
// `.`) on either side of the match cancels the replacement — that's
// what keeps `/a/b` from clobbering `/a/b-sibling/x`.
//
// A trailing `/` is NOT a path-continuation char here, by design: when
// the cwd is `/a/b` and the input is `/a/b/sub`, we WANT the match to
// fire so `/a/b` collapses to `.` and the survivor reads `./sub`.
func replaceAtBoundary(s, old, new string) string {
	if old == "" {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			prevOK := i == 0 || !isPathContinuation(s[i-1])
			nextOK := i+len(old) == len(s) || !isPathContinuation(s[i+len(old)])
			if prevOK && nextOK {
				b.WriteString(new)
				i += len(old)
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// isPathContinuation reports whether a byte could extend a filesystem
// path token (so we refuse to break the token by replacing only its
// prefix). `/` is intentionally NOT included — see replaceAtBoundary's
// docstring.
func isPathContinuation(c byte) bool {
	switch {
	case c == '_' || c == '-' || c == '.':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	return false
}

func cmdQuit(m Model, _ []string) (Model, tea.Cmd) {
	// Graceful exit: give the model one final turn to persist durable
	// memories before the session context is gone (config
	// [memory] final_turn_on_quit; skipped for low-activity sessions).
	out, cmd := requestGracefulExit(m)
	return out.(Model), cmd
}

// cmdMaxIterations adjusts the runaway-loop guard mid-session. With no
// args, prints the current value. The cap stops the agent loop after N
// tool calls per turn — too low and complex implementation work hits
// `[agent] hit max-iterations=N` mid-task; too high and a buggy model
// stuck in a loop burns through tokens before the user notices.
func cmdMaxIterations(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendLine(styleAuto.Render(fmt.Sprintf(
			"[max-iterations] currently %d (per turn). Pass a number to change.",
			m.cfg.MaxIterations)))
		return m, nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		m.appendLine(styleError.Render("usage: /max-iterations <positive integer>"))
		return m, nil
	}
	const sanityCap = 500
	if n > sanityCap {
		m.appendLine(styleError.Render(fmt.Sprintf(
			"[max-iterations] %d is above the sanity cap of %d — refusing. Pass a smaller value.",
			n, sanityCap)))
		return m, nil
	}
	old := m.cfg.MaxIterations
	m.cfg.MaxIterations = n
	m.appendLine(styleAuto.Render(fmt.Sprintf(
		"[max-iterations] %d → %d (takes effect on the next turn)", old, n)))
	return m, nil
}

func cmdSystem(m Model, _ []string) (Model, tea.Cmd) {
	var sys string
	for _, msg := range m.lockedMessages() {
		if msg.Role == adapter.RoleSystem {
			sys = msg.Content
			break
		}
	}
	if sys == "" {
		m.appendLine(styleError.Render("(no system prompt set)"))
	} else {
		m.appendLine(styleAssistantHeader.Render("system prompt") + "\n" + sys)
	}
	return m, nil
}

// cmdSessions is the entry point for the /sessions sub-menu picker.
// With no args, opens the layered Resume/Rename/Export picker. With
// a positional id-or-name, bypasses the picker and resumes directly
// — the power-user shortcut that mirrors how /model <name> works
// alongside the bare /model picker.
func cmdSessions(m Model, args []string) (Model, tea.Cmd) {
	if len(args) > 0 {
		return m.resumeSession(args[0], false)
	}
	m.openSessionsPicker()
	return m, nil
}

// statusLine keeps transcript notices compact and grep-friendly.
func statusLine(tag, msg string) string {
	return styleSystemTrace.Render(tag + ": " + msg)
}

func statusOKLine(tag, msg string) string {
	return styleSystemSuccess.Render("✓") + " " + styleSystemTrace.Render(tag+": "+msg)
}

func statusWarnLine(tag, msg string) string {
	return styleSystemWarn.Render("◆") + " " + styleSystemTrace.Render(tag+": "+msg)
}

func statusActionLine(tag, msg string) string {
	return styleSystemTrace.Render("→ " + tag + ": " + msg)
}

func statusHintLine(msg string) string {
	return styleSystemTrace.Render("  hint: " + msg)
}

func statusErrorLine(tag, msg string) string {
	return styleSystemError.Render("✗") + " " + styleSystemTrace.Render(tag+": "+msg)
}

func statusTraceLine(tag, msg string) string {
	return styleSystemTrace.Render(tag + ": " + msg)
}

func shortenMiddle(s string, max int) string {
	if max <= 3 || len(s) <= max {
		return s
	}
	left := (max - 3) / 2
	right := max - 3 - left
	return s[:left] + "..." + s[len(s)-right:]
}

// providerUse switches the active provider profile, adopts its
// default_model (or active.model when [active] points at the same
// profile), and refreshes the adapter + connection probe.
func providerUse(m Model, name string) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	p := cfg.FindProvider(name)
	if p == nil {
		m.appendLine(styleError.Render(fmt.Sprintf("provider %q not found in config.toml", name)))
		return m, nil
	}
	newModel := m.modelName
	switch {
	case cfg.Active.Provider == name && cfg.Active.Model != "":
		newModel = cfg.Active.Model
	case p.DefaultModel != "":
		newModel = p.DefaultModel
	}
	newKey := m.apiKey
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			newKey = v
		}
	}
	m.baseURL = p.BaseURL
	m.apiKey = newKey
	m.modelName = newModel
	m.provider = string(detectKindAsProvider(p.Kind))
	m.providerLabel = wizard.CatalogIdentity(p.Name)
	ad := adapter.NewWithConfig(m.adapterConfig(newModel, p.BaseURL))
	m.cfg.Adapter = ad
	// Also update the AgentTool's Adapter so subagents inherit the new provider
	if m.subagentTool != nil {
		m.subagentTool.Adapter = ad
	}
	m.providerProfile = ad.Profile()
	m.sess.Model = newModel
	m, _ = reloadMemoryNow(m, "")
	m.appendLine(styleAuto.Render(statusOKLine("provider", fmt.Sprintf("switched to %s (model: %s)", name, newModel))))
	cmds := []tea.Cmd{runProviderProbe(m.parentCtx, m.adapterConfig(newModel, p.BaseURL), false)}
	// A provider switch changes the active model outside the picker path —
	// discover its window from the live API too (the picker reads it from
	// the list-models row; this path otherwise never would).
	if m.shouldProbeWindow(p.Kind, newModel) {
		if m.probedModels == nil {
			m.probedModels = map[string]bool{}
		}
		m.probedModels[newModel] = true
		cmds = append(cmds, discoverWindowCmd(m.parentCtx, *p, m.apiKey, newModel))
	}
	return m, tea.Batch(cmds...)
}

// providerAdd appends a new [[providers]] block to ~/.yottacode/config.toml
// and ensures <cwd>/.yottacode/.gitignore lists .env. Required flags:
// --kind, --base-url. Optional: --key-env, --models, --default-model.
func providerAdd(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendLine(styleError.Render("usage: /provider add <name> --kind <k> --base-url <url> [--key-env NAME] [--models a,b,c] [--default-model NAME]"))
		return m, nil
	}
	name := args[0]
	flags := parseProviderFlags(args[1:])
	if flags.kind == "" || flags.baseURL == "" {
		m.appendLine(styleError.Render("--kind and --base-url are required"))
		return m, nil
	}
	if !inSlice(config.ValidKinds, flags.kind) {
		m.appendLine(styleError.Render(fmt.Sprintf("invalid kind %q (use one of %s)", flags.kind, strings.Join(config.ValidKinds, ", "))))
		return m, nil
	}
	cfg := loadConfigForCommand(m)
	if cfg.FindProvider(name) != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("provider %q already exists; remove it first", name)))
		return m, nil
	}
	// Auto-uniquify the env var name when the user-supplied --key-env
	// (or the kind default) is already taken by another profile. Two
	// NVIDIA NIM profiles can't share NVIDIA_API_KEY because each
	// model has its own bearer; without this, a second profile
	// silently overwrites the first profile's key in .env. The first
	// profile of a kind keeps the well-known default.
	keyEnv := providerops.SuggestAPIKeyEnv(cfg, name, flags.keyEnv)
	p := config.Provider{
		Name:         name,
		Kind:         flags.kind,
		BaseURL:      flags.baseURL,
		APIKeyEnv:    keyEnv,
		DefaultModel: flags.defaultModel,
	}
	for _, mn := range flags.models {
		mn = strings.TrimSpace(mn)
		if mn == "" {
			continue
		}
		p.Models = append(p.Models, config.Model{Name: mn})
	}
	if p.DefaultModel != "" {
		hit := false
		for _, mm := range p.Models {
			if mm.Name == p.DefaultModel {
				hit = true
				break
			}
		}
		if !hit {
			m.appendLine(styleError.Render("--default-model must appear in --models"))
			return m, nil
		}
	}
	// openai-auth defers the on-disk write until the OAuth callback
	// completes — without this, a cancelled or failed sign-in leaves
	// a broken profile in config.toml that the next chat turn 401s
	// against. Stash the AddProvider on the Model so
	// handleInlineOpenAIAuthDone can replay the persist on success or
	// drop it on failure.
	if p.Kind == "openai-auth" {
		m.openAIAuthPendingAdd = &pendingOpenAIAuthAdd{
			add: providerops.AddProvider{
				Name:         p.Name,
				Kind:         p.Kind,
				BaseURL:      p.BaseURL,
				APIKeyEnv:    p.APIKeyEnv,
				DefaultModel: p.DefaultModel,
				Models:       append([]config.Model(nil), p.Models...),
			},
			becomesActive: cfg.Active.Provider == "",
			fromPicker:    false,
		}
		m.appendLine(styleAuto.Render(statusActionLine("openai-auth", "starting browser sign-in…")))
		m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("profile %q will be saved after sign-in", p.Name))))
		// Reclaim the loopback port from any sign-in the user abandoned
		// (e.g. closed the browser) so this retry can bind.
		m.closePendingOpenAIAuthLogin()
		return m, startInlineOpenAIAuthLoginCmd(m.parentCtx)
	}
	if p.Kind == "copilot" {
		m.copilotPendingAdd = &pendingCopilotAdd{
			add: providerops.AddProvider{
				Name:         p.Name,
				Kind:         p.Kind,
				BaseURL:      p.BaseURL,
				APIKeyEnv:    p.APIKeyEnv,
				DefaultModel: p.DefaultModel,
				Models:       append([]config.Model(nil), p.Models...),
			},
			becomesActive: cfg.Active.Provider == "",
			fromPicker:    false,
		}
		m.appendLine(styleAuto.Render(statusActionLine("copilot", "starting device code sign-in…")))
		m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("profile %q will be saved after sign-in", p.Name))))
		return m, startInlineCopilotAuthCmd(m.parentCtx)
	}
	cfg.Providers = append(cfg.Providers, p)
	// First add wins active — same behavior as the picker. Without
	// this, the user can `/provider add foo …` from a freshly
	// /setup-less terminal and have the next `yottacode` invocation
	// still error out with "no model set". becameActive feeds the
	// in-memory rebuild below so the running session picks up the new
	// profile without a manual /provider use.
	becameActive := false
	if cfg.Active.Provider == "" {
		cfg.Active.Provider = p.Name
		if p.DefaultModel != "" {
			cfg.Active.Model = p.DefaultModel
			cfg.Active.DefaultModel = p.DefaultModel
		}
		becameActive = true
	}
	if err := config.Validate(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("validation: %v", err)))
		return m, nil
	}
	if err := writeConfig(cfg); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("write config: %v", err)))
		return m, nil
	}
	if err := ensureGitignoreCoversDotEnv(m.cwd); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("warning: gitignore: %v", err)))
	}
	hint := ""
	if p.APIKeyEnv != "" {
		hint = fmt.Sprintf(" — set %s in ~/.yottacode/.env or export it", p.APIKeyEnv)
	}
	// Render the catalog identity (e.g. "nvidia-nim") in the post-add
	// log when we can derive it; falls back to Kind for hand-rolled
	// profiles that don't trace back to a catalog entry.
	identity := p.Kind
	if id := wizard.CatalogIdentity(p.Name); id != "" {
		identity = id
	}
	m.appendLine(styleAuto.Render(statusOKLine("provider", fmt.Sprintf("added %q (%s)%s", name, identity, hint))))
	// First-add (or empty-state recovery): rebuild the in-memory
	// adapter so the next chat turn doesn't hit the nil-adapter
	// guard. providerUse loads cfg fresh from disk (we just wrote
	// it), rebuilds the adapter, runs the probe, reloads memory,
	// and appends its own "[provider] switched to …" line.
	if becameActive {
		return providerUse(m, p.Name)
	}
	return m, nil
}

func providerRemove(m Model, name string) (Model, tea.Cmd) {
	return applyProviderRemove(m, name)
}

// applyProviderRemove is the shared post-remove orchestrator used by
// both the slash-command path (providerRemove) and the picker path
// (commitProviderRemove). Three outcomes:
//
//  1. Removing a non-active provider — drop it, persist, log.
//  2. Removing the active provider with at least one other configured
//     — drop it, auto-switch to the first remaining (declaration
//     order), rebuild the adapter via providerUse so the running
//     session pivots cleanly. Two log lines so the switch isn't
//     silent.
//  3. Removing the only/last provider — drop it, invalidate the
//     in-memory adapter so the next chat turn errors with "no
//     provider configured" instead of streaming against the
//     just-deleted profile (the bug class this whole flow exists
//     to prevent).
//
// openai-auth-specific cleanup (token + models files) hangs off the
// removedKind branch and runs in every outcome.
func applyProviderRemove(m Model, name string) (Model, tea.Cmd) {
	cfg := loadConfigForCommand(m)
	prev := cfg.FindProvider(name)
	if prev == nil {
		m.appendLine(styleError.Render(fmt.Sprintf("provider %q not found", name)))
		return m, nil
	}
	removedKind := prev.Kind
	removedKeyEnv := prev.APIKeyEnv
	wasActive := cfg.Active.Provider == name

	updated, err := providerops.Remove(cfg, name)
	if err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("%v", err))))
		return m, nil
	}

	fallback := ""
	if wasActive {
		fallback = providerops.PickFallback(updated)
		if fallback != "" {
			updated, err = providerops.SetActive(updated, fallback)
			if err != nil {
				m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("auto-switch: %v", err))))
				return m, nil
			}
		}
	}

	if err := config.Validate(updated); err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("config invalid: %v", err))))
		return m, nil
	}
	if err := writeConfig(updated); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("write config: %v", err)))
		return m, nil
	}

	m.appendLine(styleAuto.Render(statusOKLine("provider", fmt.Sprintf("removed %q", name))))
	if removedKind == "openai-auth" {
		m = cleanupOpenAIAuthTokenStore(m)
	}
	if removedKind == "copilot" {
		m = cleanupCopilotTokenStore(m)
	}
	// Drop the matching API key from ~/.yottacode/.env when no other
	// remaining profile still references it. Skips when shared
	// (legacy configs predating per-profile env-var minting can have
	// two profiles on one bearer; deleting that bearer would silently
	// break the surviving profile). Also unsets from the running
	// process so a probe right after the remove doesn't pick up a
	// stale value.
	m = cleanupAPIKeyEnv(m, removedKeyEnv, updated)

	switch {
	case wasActive && fallback != "":
		// Hand off to providerUse for the in-memory adapter rebuild +
		// probe + memory reload. providerUse loads cfg fresh from disk
		// (which we just wrote), so it picks up the new active.
		return providerUse(m, fallback)
	case wasActive:
		// No fallback exists. Without invalidating the in-memory
		// adapter the user could still chat against the just-removed
		// provider — that's the bug we're fixing.
		m = invalidateAdapter(m)
		m.appendLine(styleAuto.Render(statusLine("provider",
			"no other providers configured — run /provider add to set one up")))
	}
	return m, nil
}

// invalidateAdapter clears every runtime field a chat turn or status
// bar reads (adapter, model name, base URL, API key, provider profile,
// status-bar provider tag, connection probe state). After this,
// startTurn refuses to fire and the status bar renders an empty model
// slot with a muted/unknown dot — no leftover "nvidia-nim" tag from
// the just-removed profile. Recovered by /provider use or
// /provider add.
func invalidateAdapter(m Model) Model {
	m.cfg.Adapter = nil
	// Also clear the AgentTool's Adapter so subagents don't use stale config
	if m.subagentTool != nil {
		m.subagentTool.Adapter = nil
	}
	m.modelName = ""
	m.baseURL = ""
	m.apiKey = ""
	m.provider = ""
	m.providerLabel = ""
	m.providerProfile = adapter.ProviderProfile{}
	m.sess.Model = ""
	m.connection = connUnknown
	return m
}

// cleanupOpenAIAuthTokenStore deletes ~/.yottacode/auth/openai-auth.json
// AND its sibling openai-auth-models.json (the post-login per-user
// allow-list), then removes the auth/ directory if it's now empty.
// Both files share the token's lifetime — once the bearer is gone,
// the discovered model list is meaningless and would otherwise mislead
// the next openai-auth setup. Missing-file cases are silent: the cobra
// `openai-auth logout` may have run first, or the user removed a
// profile they never signed in with.
func cleanupOpenAIAuthTokenStore(m Model) Model {
	path, err := openaiauth.DefaultStorePath()
	if err != nil {
		return m
	}
	if removeErr := os.Remove(path); removeErr == nil {
		m.appendLine(styleAuto.Render(statusOKLine("openai-auth", "token store deleted; logged out")))
	} else if !errors.Is(removeErr, os.ErrNotExist) {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("openai-auth: could not delete token store: %v", removeErr))))
		return m
	}
	if modelsPath, err := openaiauth.DefaultModelsPath(); err == nil {
		if removeErr := os.Remove(modelsPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("openai-auth: could not delete models file: %v", removeErr))))
		}
	}
	// Best-effort directory cleanup. os.Remove only succeeds when the
	// directory is empty, which is the right semantics — if a future
	// provider drops another token file in there we don't want to
	// blow it away.
	_ = os.Remove(filepath.Dir(path))
	return m
}

func cleanupCopilotTokenStore(m Model) Model {
	path, err := copilotauth.DefaultStorePath()
	if err != nil {
		return m
	}
	if removeErr := os.Remove(path); removeErr == nil {
		m.appendLine(styleAuto.Render(statusOKLine("copilot", "token store deleted; logged out")))
	} else if !errors.Is(removeErr, os.ErrNotExist) {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("copilot: could not delete token store: %v", removeErr))))
	}
	if modelsPath, err := copilotauth.DefaultModelsPath(); err == nil {
		if removeErr := os.Remove(modelsPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("copilot: could not delete models cache: %v", removeErr))))
		}
	}
	_ = os.Remove(filepath.Dir(path))
	return m
}

// cleanupAPIKeyEnv drops the env-var slot the just-removed profile
// owned. Three branches:
//
//  1. Empty keyEnv (Ollama, openai-auth) — nothing to do; those
//     providers don't have an API-key slot in .env.
//  2. Some remaining profile still references the same env var
//     (legacy shared-key configs from before SuggestAPIKeyEnv) —
//     leave the .env entry alone, log the skip so the user knows
//     why their key file wasn't touched.
//  3. No remaining profile references it — delete the line from
//     ~/.yottacode/.env and Unsetenv it from the running process.
//     Log the deletion loudly so a user who exported the same env
//     var for non-yottacode reasons sees what happened and can
//     recover (the value is in their git/.env history).
//
// Errors are surfaced as warnings, not blockers. The provider
// removal already succeeded on disk — failing the whole operation
// because we couldn't rewrite .env would be over-strict.
func cleanupAPIKeyEnv(m Model, keyEnv string, remaining config.Config) Model {
	if strings.TrimSpace(keyEnv) == "" {
		return m
	}
	for _, p := range remaining.Providers {
		if p.APIKeyEnv == keyEnv {
			m.appendLine(styleAuto.Render(statusLine("env", fmt.Sprintf(
				"kept %s in ~/.yottacode/.env — still used by provider %q",
				keyEnv, p.Name))))
			return m
		}
	}
	path, err := dotenv.DefaultPath()
	if err != nil {
		m.appendLine(styleError.Render(statusLine("env", fmt.Sprintf("could not resolve .env path: %v", err))))
		return m
	}
	deleted, err := dotenv.DeleteKey(path, keyEnv)
	if err != nil {
		m.appendLine(styleError.Render(statusLine("env", fmt.Sprintf("could not clean %s from .env: %v", keyEnv, err))))
		return m
	}
	_ = os.Unsetenv(keyEnv)
	if deleted {
		m.appendLine(styleAuto.Render(statusLine("env", fmt.Sprintf("removed %s from %s", keyEnv, path))))
	}
	return m
}

type providerAddFlags struct {
	kind         string
	baseURL      string
	keyEnv       string
	defaultModel string
	models       []string
}

func parseProviderFlags(args []string) providerAddFlags {
	var f providerAddFlags
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			i++
			if i >= len(args) {
				return ""
			}
			return args[i]
		}
		switch {
		case a == "--kind":
			f.kind = next()
		case strings.HasPrefix(a, "--kind="):
			f.kind = strings.TrimPrefix(a, "--kind=")
		case a == "--base-url":
			f.baseURL = next()
		case strings.HasPrefix(a, "--base-url="):
			f.baseURL = strings.TrimPrefix(a, "--base-url=")
		case a == "--key-env":
			f.keyEnv = next()
		case strings.HasPrefix(a, "--key-env="):
			f.keyEnv = strings.TrimPrefix(a, "--key-env=")
		case a == "--default-model":
			f.defaultModel = next()
		case strings.HasPrefix(a, "--default-model="):
			f.defaultModel = strings.TrimPrefix(a, "--default-model=")
		case a == "--models":
			f.models = append(f.models, splitCSV(next())...)
		case strings.HasPrefix(a, "--models="):
			f.models = append(f.models, splitCSV(strings.TrimPrefix(a, "--models="))...)
		}
	}
	return f
}

// formatProviderModels lists one profile's catalog. For curated
// providers (anthropic/openai/gemini/xai) it reads the curated catalog
// (embedded, plus the local models.dev snapshot for Gemini); for
// everything else it shows the configured default + API-key
// status and points the user at /model for the live picker. /model
// list is sync (writes to the transcript directly) so we can't do a
// network round-trip here — that's the picker's job.
func formatProviderModels(p *config.Provider, activeModel string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "models for %s (%s):\n", p.Name, p.Kind)

	if catalog.IsCurated(*p) {
		models := catalog.Curated(p.Kind)
		if len(models) == 0 {
			if p.Kind == "openai-auth" {
				fmt.Fprintln(&b, "  (no models discovered yet — run `yottacode openai-auth login` to populate)")
			} else {
				fmt.Fprintln(&b, "  (catalog empty — run `go run ./cmd/yotta-models refresh` to populate)")
			}
		}
		for _, m := range models {
			marker := "  "
			if m.ID == activeModel {
				marker = "▸ "
			}
			line := m.Label()
			if m.DisplayName != "" && m.DisplayName != m.ID {
				line += "  (" + m.ID + ")"
			}
			if m.ContextWindow > 0 {
				line += fmt.Sprintf("  ctx=%d", m.ContextWindow)
			}
			if m.Disabled {
				line += "  [upgrade plan]"
			}
			fmt.Fprintf(&b, "%s%s\n", marker, line)
		}
	} else {
		// Free-form providers (NVIDIA NIM, Ollama, custom proxies):
		// surface the configured default + API-key status so the user
		// sees their setup is wired up. The live model list belongs in
		// /model (which can do an async fetch); /model list stays sync.
		if p.DefaultModel != "" {
			marker := "  "
			if p.DefaultModel == activeModel {
				marker = "▸ "
			}
			fmt.Fprintf(&b, "%s%s  (configured default)\n", marker, p.DefaultModel)
		} else {
			fmt.Fprintln(&b, "  (no default model set)")
		}
		fmt.Fprintln(&b, "  (open /model for the live catalog)")
	}

	// API-key status — useful for both curated and free-form so the
	// user can confirm the provider is fully wired.
	switch {
	case p.APIKeyEnv == "":
		fmt.Fprintln(&b, "  API key: not required")
	case os.Getenv(p.APIKeyEnv) != "":
		fmt.Fprintf(&b, "  API key: ✓ %s set\n", p.APIKeyEnv)
	default:
		fmt.Fprintf(&b, "  API key: ✗ %s missing — run /provider add or set in ~/.yottacode/.env\n", p.APIKeyEnv)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatModelList renders /model output. activeOnly = list active
// provider's models; allProviders = group every provider.
func formatModelList(cfg config.Config, activeModel string, allProviders bool) string {
	if len(cfg.Providers) == 0 {
		return "no providers configured — try /provider add"
	}
	var b strings.Builder
	if allProviders {
		names := make([]string, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			names = append(names, p.Name)
		}
		sort.Strings(names)
		for _, n := range names {
			p := cfg.FindProvider(n)
			b.WriteString(formatProviderModels(p, activeModel))
			b.WriteByte('\n')
			b.WriteByte('\n')
		}
		return strings.TrimRight(b.String(), "\n")
	}
	// Single-provider list: prefer active.provider, else the first.
	target := cfg.Active.Provider
	if target == "" {
		target = cfg.Providers[0].Name
	}
	p := cfg.FindProvider(target)
	if p == nil {
		return "active provider not found in catalog — try /model list all"
	}
	return formatProviderModels(p, activeModel)
}

// loadConfigForCommand returns the on-disk config or a usable empty
// Default() if loading fails. Errors here are reported by the
// dedicated /memory config path; here we silently degrade.
func loadConfigForCommand(_ Model) config.Config {
	cfg, err := config.LoadDefault()
	if err != nil {
		return config.Default()
	}
	return cfg
}

// profileForActiveBaseURL finds the configured profile whose base_url
// matches the session's current base_url. Used to mark the active row
// in /provider output. Returns "" when no match.
func profileForActiveBaseURL(cfg config.Config, baseURL string) string {
	for _, p := range cfg.Providers {
		if p.BaseURL == baseURL {
			return p.Name
		}
	}
	return ""
}

// detectKindAsProvider maps a config kind to the adapter-layer
// capability tag the existing ProviderOverride field expects. Must
// list every entry in config.ValidKinds — a fall-through to
// openai-compatible looks harmless but breaks per-provider behavior
// like diagnostics, the probe path, and the status-bar provider tag.
func detectKindAsProvider(kind string) adapter.Provider {
	switch kind {
	case "anthropic":
		return adapter.ProviderAnthropic
	case "openai":
		return adapter.ProviderOpenAI
	case "openai-auth":
		return adapter.ProviderOpenAIAuth
	case "copilot":
		return adapter.ProviderCopilot
	case "gemini":
		return adapter.ProviderGemini
	case "xai":
		return adapter.ProviderXAI
	case "ollama":
		return adapter.ProviderOllama
	case "vertex":
		return adapter.ProviderVertex
	case "vertex-anthropic":
		return adapter.ProviderVertexAnthropic
	default:
		return adapter.ProviderOpenAICompatible
	}
}

// writeConfig serializes cfg back to ~/.yottacode/config.toml via
// config.Save (atomic tmp+rename). Used by /provider add/remove and
// the /model picker confirm path. Replaces the BurntSushi-encoded
// version that lost section ordering and the documentation header
// on every round-trip.
func writeConfig(cfg config.Config) error {
	return config.Save(cfg, "")
}

// ensureGitignoreCoversDotEnv appends `.env` to <cwd>/.yottacode/.gitignore
// if it doesn't already match. We never overwrite — only append. No-op
// when cwd is empty (one-shot non-interactive runs).
func ensureGitignoreCoversDotEnv(cwd string) error {
	if cwd == "" {
		return nil
	}
	dir := filepath.Join(cwd, ".yottacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".gitignore")
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == ".env" {
			return nil
		}
	}
	if len(body) > 0 && !strings.HasSuffix(string(body), "\n") {
		body = append(body, '\n')
	}
	body = append(body, []byte(".env\n")...)
	return os.WriteFile(path, body, 0o644)
}

// inSlice — local copy because the config package's helper is
// unexported. Tiny duplication, not worth re-architecting.
func inSlice(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func cmdDoctor(m Model, _ []string) (Model, tea.Cmd) {
	m.appendLine(styleAuto.Render("[doctor] probing provider and local code intelligence..."))
	return m, runDoctorProbe(m.parentCtx, m)
}

func renderProviderCommandValue(provider adapter.Provider) string {
	if provider == "" {
		return string(adapter.ProviderOpenAICompatible)
	}
	return string(provider)
}

func renderBool(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func renderConnectionSummary(state connState) string {
	switch state {
	case connOK:
		return "reachable"
	case connDegraded:
		return "degraded"
	case connDown:
		return "unreachable"
	default:
		return "unknown"
	}
}

// sessionCacheKey returns the stable prompt_cache_key for this session
// (its id), or "" when there is no session. Mirrors how the TUI/oneshot
// runners seed opts.CacheKey so a mid-session model switch keeps the
// same server-side cache shard.
func (m Model) sessionCacheKey() string {
	if m.sess == nil {
		return ""
	}
	return m.sess.ID
}

func (m Model) adapterConfig(modelName, baseURL string) adapter.Config {
	maxOutput, supportsThinking := catalog.ReasoningInfo(modelName)
	return adapter.Config{
		BaseURL:                baseURL,
		APIKey:                 m.apiKey,
		Model:                  modelName,
		ProviderOverride:       adapter.Provider(strings.TrimSpace(m.provider)),
		ReasoningEffort:        m.reasoningEffort,
		CacheKey:               m.sessionCacheKey(),
		ModelMaxOutput:         maxOutput,
		ModelSupportsThinking:  supportsThinking,
		EnableWebSearch:        m.enableWebSearch,
		DisableWebSearch:       m.disableWebSearch,
		EnableXSearch:          m.enableXSearch,
		EnableCodeInterpreter:  m.enableCodeInterpreter,
		SearchAllowedDomains:   splitCSV(m.searchAllowedDomains),
		SearchExcludedDomains:  splitCSV(m.searchExcludedDomains),
		XSearchAllowedHandles:  splitCSV(m.xSearchAllowedHandles),
		XSearchExcludedHandles: splitCSV(m.xSearchExcludedHandles),
		XSearchFromDate:        strings.TrimSpace(m.xSearchFromDate),
		XSearchToDate:          strings.TrimSpace(m.xSearchToDate),
	}
}

func runProviderProbe(ctx context.Context, cfg adapter.Config, announce bool) tea.Cmd {
	return func() tea.Msg {
		return providerProbeMsg{
			result:   adapter.Probe(ctx, cfg),
			announce: announce,
		}
	}
}

type tuiLSPDoctorResult struct {
	Enabled   bool
	Languages []tuiLSPDoctorLanguage
	MaxServer int
	Note      string
	Error     string
}

type tuiLSPDoctorLanguage struct {
	Name            string
	Files           int
	Command         []string
	ServerAvailable bool
	InstallHint     string
	Override        bool
}

func runDoctorProbe(ctx context.Context, m Model) tea.Cmd {
	providerCfg := m.adapterConfig(m.modelName, m.baseURL)
	cwd := m.cwd
	fileCfg := m.fileCfg
	experimentalEnabled := append([]string(nil), m.experimentalEnabled...)
	return func() tea.Msg {
		return doctorProbeMsg{
			provider: adapter.Probe(ctx, providerCfg),
			lsp:      probeTUILSPDoctor(ctx, cwd, fileCfg, experimentalEnabled),
		}
	}
}

func probeTUILSPDoctor(ctx context.Context, cwd string, cfg config.Config, enabledNames []string) tuiLSPDoctorResult {
	enabled := inSlice(enabledNames, string(experimental.LSPCodeIntelligence))
	if cfg.Experimental[string(experimental.LSPCodeIntelligence)] {
		enabled = true
	}
	root := cwd
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	langs, err := lsp.DetectWorkspace(ctx, root, 2000)
	if err != nil {
		return tuiLSPDoctorResult{Enabled: enabled, Error: err.Error()}
	}
	langs = lsp.ApplyOverridesToDetected(langs, cfg.LSP.Servers)
	out := tuiLSPDoctorResult{Enabled: enabled, MaxServer: lsp.DefaultManagerMaxServers()}
	if !enabled {
		out.Note = "enable with --experimental lsp_code_intelligence or [experimental].lsp_code_intelligence = true"
	}
	if enabled {
		out.Note = "servers are local subprocesses and are never auto-installed"
	}
	if len(langs) == 0 {
		out.Note = "no supported languages detected in this workspace"
		if !enabled {
			out.Note = "disabled; no supported languages detected in this workspace"
		}
	}
	for _, lang := range langs {
		hint := ""
		if !lang.ServerAvailable {
			hint = lang.InstallHint
		}
		out.Languages = append(out.Languages, tuiLSPDoctorLanguage{
			Name:            lang.Name,
			Files:           lang.FilesAvailable,
			Command:         append([]string(nil), lang.Command...),
			ServerAvailable: lang.ServerAvailable,
			InstallHint:     hint,
			Override:        len(cfg.LSP.Servers[lang.ID]) > 0,
		})
	}
	return out
}

func renderTUILSPDoctor(result tuiLSPDoctorResult) string {
	var b strings.Builder
	b.WriteString("LSP Code Intelligence:\n")
	fmt.Fprintf(&b, "feature: %s", renderBool(result.Enabled))
	if result.Note != "" {
		fmt.Fprintf(&b, " (%s)", result.Note)
	}
	if result.Error != "" {
		fmt.Fprintf(&b, "\nerror: %s", result.Error)
		return b.String()
	}
	if result.Enabled && result.MaxServer > 0 {
		fmt.Fprintf(&b, "\nmanager: max_servers=%d", result.MaxServer)
	}
	if len(result.Languages) == 0 {
		return b.String()
	}
	for _, lang := range result.Languages {
		status := "missing"
		if lang.ServerAvailable {
			status = "installed"
		}
		override := ""
		if lang.Override {
			override = " override=yes"
		}
		fmt.Fprintf(&b, "\n- %s: files=%d server=%s status=%s%s", lang.Name, lang.Files, strings.Join(lang.Command, " "), status, override)
		if !lang.ServerAvailable && lang.InstallHint != "" {
			fmt.Fprintf(&b, "\n  hint: %s", lang.InstallHint)
		}
	}
	return b.String()
}

func formatProviderProfile(profile adapter.ProviderProfile, baseURL string, connection connState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "provider: %s\n", renderProviderCommandValue(profile.Provider))
	if profile.UsesResponsesAPI {
		b.WriteString("api-style: responses\n")
	} else {
		b.WriteString("api-style: chat-completions\n")
	}
	if baseURL != "" {
		fmt.Fprintf(&b, "base-url: %s\n", baseURL)
	}
	if tools := renderBuiltinTools(profile); tools != "" {
		fmt.Fprintf(&b, "enabled tools: %s\n", tools)
	} else {
		b.WriteString("enabled tools: none\n")
	}
	b.WriteString("capabilities:\n")
	fmt.Fprintf(&b, "  reasoning=%s web_search=%s x_search=%s code_interpreter=%s\n",
		renderBool(profile.SupportsReasoning),
		renderBool(profile.SupportsWebSearch),
		renderBool(profile.SupportsXSearch),
		renderBool(profile.SupportsCodeInterpreter),
	)
	b.WriteString("connection: ")
	b.WriteString(renderConnectionSummary(connection))
	b.WriteByte('\n')
	if len(profile.Issues) == 0 && len(profile.Warnings) == 0 {
		b.WriteString("checks: ok")
	} else {
		for _, issue := range profile.Issues {
			fmt.Fprintf(&b, "issue: %s\n", issue)
		}
		for _, warning := range profile.Warnings {
			fmt.Fprintf(&b, "warning: %s\n", warning)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatProbeResult(result adapter.ProbeResult) string {
	var b strings.Builder
	b.WriteString(formatProviderProfile(result.Profile, result.BaseURL, probeConnectionState(result)))
	b.WriteByte('\n')
	fmt.Fprintf(&b, "probe: endpoint=%s auth=%s model-visible=%s",
		renderBool(result.EndpointReachable),
		renderBool(result.AuthOK),
		renderBool(result.ModelVisible),
	)
	if result.HTTPStatus != 0 {
		fmt.Fprintf(&b, " status=%d", result.HTTPStatus)
	}
	if len(result.AvailableModels) > 0 {
		fmt.Fprintf(&b, "\nmodels: %s", strings.Join(result.AvailableModels, ", "))
	}
	if len(result.Issues) == 0 && len(result.Warnings) == 0 {
		b.WriteString("\nresult: ok")
	}
	return strings.TrimRight(b.String(), "\n")
}

func probeConnectionState(result adapter.ProbeResult) connState {
	switch {
	case result.EndpointReachable && result.AuthOK && len(result.Issues) == 0:
		return connOK
	case result.EndpointReachable && result.AuthOK:
		// Reachable and the key works, but something's off — typically the
		// configured model wasn't found in the provider's model list
		// (pagination, an Ollama :latest tag mismatch, or a stale
		// allow-list). The connection itself is healthy, so that's
		// degraded (amber), not down (red).
		return connDegraded
	default:
		return connDown
	}
}

func cmdClear(m Model, _ []string) (Model, tea.Cmd) {
	// Preserve the outgoing conversation before starting a fresh one — but
	// only if there was one. /clear on a session that never got a turn
	// (launch, then immediately clear) would otherwise persist a
	// system-prompt-only shell that shows up as resumable in /sessions and
	// opens with an empty transcript. Same rule as the at-exit save.
	if m.sess.HasExchange() {
		if err := m.sess.Save(); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("⚠ saving old session: %v", err)))
		}
	}
	var sysContent string
	for _, msg := range m.sess.Messages {
		if msg.Role == adapter.RoleSystem {
			sysContent = msg.Content
			break
		}
	}
	newSess, err := session.New(m.modelName, m.cwd)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("✗ %v", err)))
		return m, nil
	}
	if sysContent != "" {
		newSess.Messages = append(newSess.Messages, adapter.Message{
			Role:    adapter.RoleSystem,
			Content: sysContent,
		})
	}
	m.sess = newSess
	m.transcript.Reset()
	m.streaming.Reset()
	m.streamingMode = streamIdle
	m.historyLines = nil
	// Reseed ctx % from the (now near-empty) session — typically
	// just the system-prompt message. Resets the bar to a small
	// non-zero number rather than leaving the pre-clear value
	// stuck on screen.
	m.refreshContextTokens()
	// Reset the auto-summarize/warn gate immediately. /clear empties the
	// history, so the next crossing should fire fresh — without this the
	// gate self-heals only on the next turn's updateContextUsage, and a
	// gate left pinned high by a prior non-convergent summarize (see the
	// summaryDoneMsg handler) would otherwise survive the clear.
	m.lastWatermarkPct = 0
	m.nonConvergentAt = 0
	m.nonConvergentWindow = 0
	// Wipe the viewport so /clear lands on a clean canvas instead
	// of tacking a confirmation line under the prior transcript.
	// Mirrors the resize-replay path: ClearScreen, then re-emit the
	// startup card under fresh-session chrome (the new session has
	// only a system prompt, so isFreshSession() is true).
	m.pendingCmds = append(m.pendingCmds, tea.ClearScreen)
	if m.shouldShowStartupCard() {
		m.appendRaw(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.sess.ID, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width, m.experimentalEnabled...))
		m.queuePrintln("")
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf("[clear] new session %s", newSess.ID)))
	// A /clear starts a fresh conversation, so an armed /loop from the old
	// session must not bleed into it (and its arm line was just wiped, so
	// the user couldn't see it anyway). Disarm and drop its pending tick.
	m.disarmAllLoops("[loop] stopped — session cleared")
	return m, nil
}

// rebuildTranscript replays a session's prior user/assistant exchange
// into scrollback after a resume. System messages are skipped to keep
// the replay readable; tool-role messages aren't emitted directly
// either — their content is folded into the matching tool call's card
// footer/body so the replay reads like the live transcript instead of
// a flat dump. Called from sessionsPicker's resumeSession helper.
//
// Tool calls are rendered via renderToolCard — the same path the live
// transcript uses on agent.ToolResult — so a resumed session shows
// proper ┌/│/└ cards instead of a bare orange "▸ name(...)" one-liner.
// Tools missing from the registry (renamed, removed, or registered
// only in a different binary) fall back to a one-line preview so the
// rebuild never crashes on an unknown name.
func rebuildTranscript(m *Model) {
	results := toolResultsByCallID(m.sess.Messages)
	for _, msg := range m.sess.Messages {
		switch msg.Role {
		case adapter.RoleUser:
			m.appendLine(renderUserBlock(msg.Content, m.width))
		case adapter.RoleAssistant:
			if msg.Content != "" && len(msg.ToolCalls) == 0 {
				m.appendLine(renderAssistantBlock(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				m.appendLine("")
				m.appendLine(renderRebuiltToolCard(m, tc, results[tc.ID]))
			}
		}
	}
}

// toolResultsByCallID indexes a session's tool-role messages by the
// ToolCallID they answer, so rebuildTranscript can fold each tool call
// into a single card carrying its own result instead of dumping the
// raw tool messages as separate scrollback rows.
func toolResultsByCallID(msgs []adapter.Message) map[string]string {
	out := make(map[string]string, len(msgs))
	for _, msg := range msgs {
		if msg.Role == adapter.RoleTool && msg.ToolCallID != "" {
			out[msg.ToolCallID] = msg.Content
		}
	}
	return out
}

// renderRebuiltToolCard produces the scrollback card for one tool call
// during a session replay. Resolves the tool via the registry to call
// PreviewCall (matching the live header), then runs the full
// renderToolCard with the persisted tool result as the output —
// reproducing the canonical ┌ header / │ body / └ footer shape live
// execution emits on agent.ToolResult.
//
// write_file is special-cased to reproduce the live two-card stack:
// the pre-approval body card (header + highlighted body + footer
// flipped from "awaiting approval" to "approved" / "denied" since the
// decision has already been made) plus the post-execution summary
// card. That keeps the resumed view as close as possible to what the
// user actually saw mid-session.
//
// Errored is hard-coded to false because the persisted tool message
// only stores the result content, not the agent's errored flag. The
// cosmetic mismatch for errored tool calls on replay (no red footer)
// is acceptable — the failure text still renders inside the body.
//
// Falls back to a one-line "[tool] <name>(...)" when the tool isn't
// registered (defensive) so the rebuild never crashes on an unknown
// name.
func renderRebuiltToolCard(m *Model, tc adapter.ToolCall, result string) string {
	preview := tc.Name
	if m.cfg.Registry != nil {
		if tool, ok := m.cfg.Registry.Get(tc.Name); ok {
			if p := strings.TrimSpace(tool.PreviewCall(tc.ArgsJSON)); p != "" {
				preview = p
			}
		} else {
			return styleToolCall.Render("[tool] " + tc.Name + "(...)")
		}
	}
	// Duration 0: replayed cards carry no timing — the persisted tool
	// message stores only the result content, not how long the call took
	// (same reason errored is hard-coded false above). No tag renders.
	summary := renderToolCard(tc.Name, preview, tc.ArgsJSON, result, false, m.width, m.cwd, 0)
	if tc.Name == "write_file" {
		if body, ok := renderRebuiltWriteFileBodyCard(tc.ArgsJSON, result); ok {
			return body + "\n\n" + summary
		}
	}
	return summary
}

// renderRebuiltWriteFileBodyCard renders the replay equivalent of the
// pre-approval body emit: same ┌/│/└ shape (header + highlighted
// content + footer) as emitWriteFileBodyToScrollback, but the footer
// reflects the actual decision recorded in the session ("approved"
// when the file was written, "denied" when the agent recorded
// "denied by user"). Falls back to ("", false) when args don't parse
// or content is empty — the caller still emits the summary card on
// its own.
func renderRebuiltWriteFileBodyCard(argsJSON, result string) (string, bool) {
	var a writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", false
	}
	if a.Path == "" || a.Content == "" {
		return "", false
	}
	lines := strings.Count(a.Content, "\n") + 1
	footer := "approved"
	if strings.TrimSpace(result) == "denied by user" {
		footer = "denied"
	}
	rows := []string{
		styleCardGutter.Render("┌ ") +
			styleCardHeader.Render("Write("+a.Path+")") + " " +
			styleCardMeta.Render(fmt.Sprintf("(%d bytes · %d lines)", len(a.Content), lines)),
	}
	content := strings.ReplaceAll(a.Content, "\t", "    ")
	highlighted := strings.TrimRight(HighlightFromPath(content, a.Path), "\n")
	gutter := styleCardGutter.Render("│ ")
	for _, line := range strings.Split(highlighted, "\n") {
		rows = append(rows, gutter+line)
	}
	rows = append(rows, styleCardGutter.Render("└ ")+styleCardMeta.Render(footer))
	return strings.Join(rows, "\n"), true
}

// cmdRedo finds the most recent user message, drops it (and everything that
// followed — assistant replies, tool calls, tool results), and loads its
// text back into the input box for editing. Submitting from there appends
// a new user message and runs a fresh turn from that point. Useful for
// iterating on a prompt without restarting the whole session.
func cmdRedo(m Model, _ []string) (Model, tea.Cmd) {
	lastUserIdx := -1
	for i := len(m.sess.Messages) - 1; i >= 0; i-- {
		if m.sess.Messages[i].Role == adapter.RoleUser {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		m.appendLine(styleError.Render("[redo] no previous user message in this session"))
		return m, nil
	}

	lastUserText := m.sess.Messages[lastUserIdx].Content
	m.sess.Messages = m.sess.Messages[:lastUserIdx]

	// Reset rendered state and rebuild the transcript from what's left so
	// the viewport reflects the rewound history.
	m.transcript.Reset()
	m.streaming.Reset()
	m.streamingMode = streamIdle
	rebuildTranscript(&m)

	m.textInput.SetValue(lastUserText)
	m.textInput.CursorEnd()

	m.appendLine(styleAuto.Render("[redo] previous message loaded — edit and submit to re-run"))
	return m, nil
}

// cmdRecall queries the FTS5 index for matches across every saved session.
// Non-empty results open a transient picker below the cmdline instead of
// writing search/navigation output into the persistent session transcript. The
// picker points users at the same resume flow as /sessions: Enter resumes the
// highlighted hit; Esc closes back to the slash palette. With no recall index
// attached (e.g., index init failed at startup), reports a friendly error
// instead of silently doing nothing.
func cmdRecall(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendLine(styleError.Render("usage: /recall <query>"))
		return m, nil
	}
	if m.recall == nil {
		m.appendLine(styleError.Render("[recall] index unavailable"))
		return m, nil
	}
	query := strings.Join(args, " ")
	hits, err := m.recall.Search(query, 10)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[recall] %v", err)))
		return m, nil
	}
	if len(hits) == 0 {
		m.appendLine(styleAuto.Render(fmt.Sprintf("[recall] no matches for %q", query)))
		return m, nil
	}
	m.openRecallPicker(query, hits)
	return m, nil
}

// formatRecallAge renders a short relative time like "5m ago" for hits.
func formatRecallAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
