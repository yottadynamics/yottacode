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
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/dotenv"
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
}

// allSlash is the registry. Order = display order in /help and the palette.
// Populated in init() because some handlers (cmdHelp) iterate over allSlash,
// which would otherwise produce a Go init-cycle.
var allSlash []slashCommand

func init() {
	allSlash = []slashCommand{
		{Name: "help", Help: "show this list", Run: cmdHelp, PreservesTurn: true},
		{Name: "quit", Help: "exit yottacode", Run: cmdQuit},
		{Name: "clear", Help: "start a fresh session (current is saved)", Run: cmdClear},
		{Name: "permissions", Help: "show where permissions are configured", Run: cmdPermissions, PreservesTurn: true},
		{Name: "system", Help: "show the active system prompt", Run: cmdSystem, PreservesTurn: true},
		// /sessions opens the sessions sub-menu (Resume / Rename /
		// Export). The bare invocation lands on the picker; the
		// positional shortcut /sessions <id|name> resumes directly
		// without going through the menu, matching how /model <name>
		// works. Replaces the old /resume + /save + /export trio.
		{Name: "sessions", Help: "open the sessions menu (or /sessions <id|name> to resume directly)", Run: cmdSessions},
		// Args="" so palette Enter executes the bare form (opens
		// the picker overlay) instead of filling in a placeholder.
		// /model <name> still works as a power-user shortcut once
		// typed out manually.
		{Name: "model", Help: "open the model picker (subcommands: list [all], <name>)", Run: cmdModel},
		{Name: "provider", Help: "open the provider menu (subcommands: list, use, add, remove, models)", Run: cmdProviderEntry},
		{Name: "doctor", Help: "probe provider auth and model access", Run: cmdDoctor, PreservesTurn: true},
		{Name: "redo", Help: "edit and re-run the most recent message", Run: cmdRedo},
		{Name: "recall", Args: "<query>", Help: "full-text search across every saved session", Run: cmdRecall, PreservesTurn: true},
		{Name: "summarize", Help: "compress session history into a structured summary", Run: cmdSummarize},
		{Name: "checkpoints", Help: "open the checkpoints picker — also Esc Esc", Run: cmdCheckpoints},
		// Args="" so palette Enter executes the bare form (opens the
		// picker overlay). /memory takes no subcommands — the picker
		// covers project edit, user edit, and auto-folder location.
		{Name: "memory", Help: "open the memory picker (USER.md / YOTTACODE.md / saved memories)", Run: cmdMemory},
		{Name: "max-iterations", Args: "<N>", Help: "cap tool-call iterations per turn (default: 50; auto mode doubles)", Run: cmdMaxIterations},
		{Name: "setup", Help: "re-run the setup wizard (reloads config on return)", Run: cmdSetup},
		{Name: "init", Help: "draft .yottacode/YOTTACODE.md from the current repo", Run: cmdInit},
		// /plan toggles plan mode (read-only research + plan file +
		// exit_plan_mode for approval). Also bound to Shift+Tab — see
		// model.go's KeyMsg handler.
		// /plan with no Args means the palette executes the bare form
		// on Enter (one keystroke, direct entry). `/plan list` still
		// works when typed manually — cmdPlan branches on args[0].
		//
		// Auto mode and yolo are intentionally NOT slash-invocable
		// (mirroring Claude Code). Auto enters via Shift+Tab or
		// --permission-mode auto; yolo enters only via
		// --dangerously-skip-permissions at startup — no in-TUI toggle,
		// no palette entry, no accidental activation.
		{Name: "plan", Help: "toggle plan mode — also Shift+Tab. Type `/plan list` to resume an earlier plan.", Run: cmdPlan},
		// /subagents inspects the session's subagent task registry —
		// listing runs, viewing full transcripts (foreground transcripts
		// are written to disk because the parent doesn't ingest the
		// child's reasoning), and stopping a running task. Mirrors
		// Claude Code's UX of "subagents are first-class enough to have
		// their own listing command."
		//
		// PreservesTurn=true: viewing the subagent registry during an
		// active foreground subagent run must NOT cancel the parent
		// turn (which would kill the subagent we're trying to inspect).
		// The picker reads a snapshot of the task registry; no state
		// change happens, so the turn can keep streaming behind it.
		{Name: "subagents", Help: "open the subagents picker (Enter views · t toggles types · s stops · Esc closes)", Run: cmdSubagents, PreservesTurn: true},
	}
}

// findSlash returns the command with the given name, or nil if no match.
func findSlash(name string) *slashCommand {
	for i := range allSlash {
		if allSlash[i].Name == name {
			return &allSlash[i]
		}
	}
	return nil
}

// runSlash dispatches based on an input line ("/foo arg1 arg2").
func (m Model) runSlash(input string) (Model, tea.Cmd) {
	fields := strings.Fields(input)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return m, nil
	}
	// Record the slash invocation in input history so ↑ recalls it on
	// the next prompt. startTurn already records prose submissions; we
	// mirror that here so / commands are recall-able too. Recording
	// before dispatch covers unknown commands (typos) as well — the
	// user usually wants to recall and fix them.
	m.recordHistory(input)
	name := strings.TrimPrefix(fields[0], "/")
	cmd := findSlash(name)
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
	m.appendLine(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width))
	// Compute column width from actual content so the dashes always
	// align even when commands or args change. Avoids a hardcoded
	// padding that drifts as the registry grows.
	lefts := make([]string, len(allSlash))
	width := 0
	for i, c := range allSlash {
		left := "/" + c.Name
		if c.Args != "" {
			left += " " + c.Args
		}
		lefts[i] = left
		if w := len(left); w > width {
			width = w
		}
	}
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for i, c := range allSlash {
		b.WriteString(fmt.Sprintf("  %-*s   %s\n", width, lefts[i], c.Help))
	}
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return m, nil
}

func cmdQuit(m Model, _ []string) (Model, tea.Cmd) {
	return m, tea.Quit
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
	for _, msg := range m.sess.Messages {
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
	m.providerProfile = ad.Profile()
	m.sess.Model = newModel
	m, _ = reloadMemoryNow(m, "")
	m.appendLine(styleAuto.Render(fmt.Sprintf("[provider] switched to %s (model: %s)", name, newModel)))
	return m, runProviderProbe(m.parentCtx, m.adapterConfig(newModel, p.BaseURL), false)
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
		m.appendLine(styleAuto.Render("[provider] openai-auth: starting browser sign-in…"))
		m.appendLine(styleAuto.Render(fmt.Sprintf("(profile %q will be saved after sign-in completes)", p.Name)))
		return m, startInlineOpenAIAuthLoginCmd(m.parentCtx)
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
	m.appendLine(styleAuto.Render(fmt.Sprintf("[provider] added %q (%s)%s", name, identity, hint)))
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
		m.appendLine(styleError.Render(fmt.Sprintf("[provider] %v", err)))
		return m, nil
	}

	fallback := ""
	if wasActive {
		fallback = providerops.PickFallback(updated)
		if fallback != "" {
			updated, err = providerops.SetActive(updated, fallback)
			if err != nil {
				m.appendLine(styleError.Render(fmt.Sprintf("[provider] auto-switch: %v", err)))
				return m, nil
			}
		}
	}

	if err := config.Validate(updated); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[provider] config invalid: %v", err)))
		return m, nil
	}
	if err := writeConfig(updated); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("write config: %v", err)))
		return m, nil
	}

	m.appendLine(styleAuto.Render(fmt.Sprintf("[provider] removed %q", name)))
	if removedKind == "openai-auth" {
		m = cleanupOpenAIAuthTokenStore(m)
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
		m.appendLine(styleAuto.Render(
			"[provider] no other providers configured — run /provider add to set one up"))
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
		m.appendLine(styleAuto.Render("[provider] openai-auth: token store deleted (logged out)"))
	} else if !errors.Is(removeErr, os.ErrNotExist) {
		m.appendLine(styleError.Render(fmt.Sprintf("[provider] openai-auth: could not delete token store: %v", removeErr)))
		return m
	}
	if modelsPath, err := openaiauth.DefaultModelsPath(); err == nil {
		if removeErr := os.Remove(modelsPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			m.appendLine(styleError.Render(fmt.Sprintf("[provider] openai-auth: could not delete models file: %v", removeErr)))
		}
	}
	// Best-effort directory cleanup. os.Remove only succeeds when the
	// directory is empty, which is the right semantics — if a future
	// provider drops another token file in there we don't want to
	// blow it away.
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
			m.appendLine(styleAuto.Render(fmt.Sprintf(
				"(kept %s in ~/.yottacode/.env — still used by provider %q)",
				keyEnv, p.Name)))
			return m
		}
	}
	path, err := dotenv.DefaultPath()
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[provider] could not resolve .env path: %v", err)))
		return m
	}
	deleted, err := dotenv.DeleteKey(path, keyEnv)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[provider] could not clean %s from .env: %v", keyEnv, err)))
		return m
	}
	_ = os.Unsetenv(keyEnv)
	if deleted {
		m.appendLine(styleAuto.Render(fmt.Sprintf("(removed %s from %s)", keyEnv, path)))
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

// formatProviderList renders the configured-profile dump. As of
// the List/Use combine, the inline picker handles the typed
// `/provider list` shortcut directly — this helper is kept for
// callers (e.g. tests, future scripting paths) that want the
// flat-text shape. Visual language matches the inline pickers:
// title via renderMenuHeader, an "Active:" status row mirroring
// the picker's "Auto-memory: on/off" line, and one renderMenuItem
// row per provider with `✔` on whichever profile is currently
// driving the running session (which may differ from
// cfg.Active.Provider when the user mid-session swapped via
// /provider use without persisting).
//
// activeName is the profile whose base_url matches the session's
// current base_url; cfg.Active.Provider is the persisted default.
// Both are surfaced — ✔ tracks the live state, the Active row tracks
// what the next yottacode invocation will pick up.
func formatProviderList(cfg config.Config, activeName string) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Configured providers",
		"Pick one to switch with /provider use <name>."))
	b.WriteString("\n")

	if cfg.Active.Provider != "" {
		b.WriteString("  ")
		b.WriteString(stylePaletteEmpty.Render("Active: "))
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(cfg.Active.Provider))
		if cfg.Active.Model != "" {
			b.WriteString(stylePaletteEmpty.Render(" · model " + cfg.Active.Model))
		}
		b.WriteString("\n\n")
	}

	// Column-align the name field so kind/base-url line up across
	// rows. 12 is a sane floor (matches "anthropic" width plus a
	// pad); we widen if any configured name is longer.
	maxName := 12
	for _, p := range cfg.Providers {
		if len(p.Name) > maxName {
			maxName = len(p.Name)
		}
	}

	for i, p := range cfg.Providers {
		desc := p.Kind
		if p.BaseURL != "" {
			desc += " · " + p.BaseURL
		}
		if p.DefaultModel != "" {
			desc += " · default=" + p.DefaultModel
		}
		b.WriteString(renderMenuItem(menuItemOpts{
			Number:     i + 1,
			Label:      p.Name,
			LabelWidth: maxName,
			Desc:       desc,
			Checked:    p.Name == activeName,
		}))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatProviderModels lists one profile's catalog. For curated
// providers (anthropic/openai/gemini) it reads the embedded catalog;
// for everything else it shows the configured default + API-key
// status and points the user at /model for the live picker. /model
// list is sync (writes to the transcript directly) so we can't do a
// network round-trip here — that's the picker's job.
func formatProviderModels(p *config.Provider, activeModel string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "models for %s (%s):\n", p.Name, p.Kind)

	if catalog.IsCurated(*p) {
		models := catalog.Get(p.Kind)
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
		fmt.Fprintf(&b, "  API key: ✔ %s set\n", p.APIKeyEnv)
	default:
		fmt.Fprintf(&b, "  API key: ✘ %s missing — run /provider add or set in ~/.yottacode/.env\n", p.APIKeyEnv)
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
	case "gemini":
		return adapter.ProviderGemini
	case "ollama":
		return adapter.ProviderOllama
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
	m.appendLine(styleAuto.Render("[doctor] probing provider..."))
	return m, runProviderProbe(m.parentCtx, m.adapterConfig(m.modelName, m.baseURL), true)
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

func (m Model) adapterConfig(modelName, baseURL string) adapter.Config {
	return adapter.Config{
		BaseURL:                baseURL,
		APIKey:                 m.apiKey,
		Model:                  modelName,
		ProviderOverride:       adapter.Provider(strings.TrimSpace(m.provider)),
		ReasoningEffort:        m.reasoningEffort,
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
	if result.EndpointReachable && result.AuthOK && len(result.Issues) == 0 {
		return connOK
	}
	if result.EndpointReachable {
		return connDown
	}
	return connDown
}

func cmdClear(m Model, _ []string) (Model, tea.Cmd) {
	if err := m.sess.Save(); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("⚠ saving old session: %v", err)))
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
	// Wipe the viewport so /clear lands on a clean canvas instead
	// of tacking a confirmation line under the prior transcript.
	// Mirrors the resize-replay path: ClearScreen, then re-emit the
	// startup card under fresh-session chrome (the new session has
	// only a system prompt, so isFreshSession() is true).
	m.pendingCmds = append(m.pendingCmds, tea.ClearScreen)
	if m.shouldShowStartupCard() {
		m.appendRaw(renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width))
		m.queuePrintln("")
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf("[clear] new session %s", newSess.ID)))
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
// proper ╭/│/╰ cards instead of a bare orange "▸ name(...)" one-liner.
// Tools missing from the registry (renamed, removed, or registered
// only in a different binary) fall back to a one-line preview so the
// rebuild never crashes on an unknown name.
func rebuildTranscript(m *Model) {
	results := toolResultsByCallID(m.sess.Messages)
	for _, msg := range m.sess.Messages {
		switch msg.Role {
		case adapter.RoleUser:
			m.appendLine(renderUserBlock(msg.Content))
		case adapter.RoleAssistant:
			if msg.Content != "" {
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
// reproducing the canonical ╭ header / │ body / ╰ footer shape live
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
	summary := renderToolCard(tc.Name, preview, tc.ArgsJSON, result, false, m.width)
	if tc.Name == "write_file" {
		if body, ok := renderRebuiltWriteFileBodyCard(tc.ArgsJSON, result); ok {
			return body + "\n\n" + summary
		}
	}
	return summary
}

// renderRebuiltWriteFileBodyCard renders the replay equivalent of the
// pre-approval body emit: same ╭/│/╰ shape (header + highlighted
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
		styleCardGutter.Render("╭ ") +
			styleCardHeader.Render("Write("+a.Path+")") + " " +
			styleCardMeta.Render(fmt.Sprintf("(%d bytes · %d lines)", len(a.Content), lines)),
	}
	content := strings.ReplaceAll(a.Content, "\t", "    ")
	highlighted := strings.TrimRight(HighlightFromPath(content, a.Path), "\n")
	gutter := styleCardGutter.Render("│   ")
	for _, line := range strings.Split(highlighted, "\n") {
		rows = append(rows, gutter+line)
	}
	rows = append(rows, styleCardGutter.Render("╰ ")+styleCardMeta.Render(footer))
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

// cmdRecall queries the FTS5 index for matches across every saved session
// and renders the ranked snippets into the transcript. The footer hint
// points users at the two ways to reopen a hit: the positional shortcut
// `/sessions <id|name>` (fastest when you just copy/paste the id from
// the snippet line), or the picker's Resume action where you can type
// or paste the ref into a textinput. With no recall index attached
// (e.g., index init failed at startup), reports a friendly error instead of
// silently doing nothing.
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
	var b strings.Builder
	fmt.Fprintf(&b, "[recall] %d hit(s) for %q:\n", len(hits), query)
	for _, h := range hits {
		tag := h.SessionID
		if h.SessionName != "" {
			tag = fmt.Sprintf("%s (%s)", h.SessionID, h.SessionName)
		}
		age := formatRecallAge(time.Since(h.Created))
		fmt.Fprintf(&b, "\n  %s · %s · %s · %s\n",
			styleUserHeader.Render(string(h.Role)), tag, age, h.Model)
		fmt.Fprintf(&b, "    %s\n", strings.ReplaceAll(h.Snippet, "\n", " "))
	}
	b.WriteString("\n")
	b.WriteString(styleAuto.Render("/sessions <id|name> to resume directly · or /sessions → Resume to type the ref"))
	m.appendLine(b.String())
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

