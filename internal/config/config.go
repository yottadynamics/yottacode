// Package config loads ~/.yottacode/config.toml — the single tunable
// surface for context-window watermarks, retrieval, and provider
// profiles.
//
// The file is parsed with github.com/BurntSushi/toml so we get arrays of
// tables ([[providers]], [[providers.models]]) without writing a parser.
// Unknown keys and unknown sections are rejected at load time so a typo
// like `enbled = ture` doesn't silently pass — we walk the metadata's
// Undecoded() set after decoding.
//
// API keys NEVER live in this file. Each [[providers]] block names an
// environment variable via api_key_env; the actual key is provided
// either via that OS env var or via ~/.yottacode/.env or
// <repo>/.yottacode/.env. Inline api_key fields are refused at load.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

// Config bundles every tunable yottacode reads from disk. Sub-structs map
// 1:1 to TOML sections so the file shape mirrors the Go shape.
type Config struct {
	Context     ContextConfig     `toml:"context"`
	Retrieval   RetrievalConfig   `toml:"retrieval"`
	Memory      MemoryConfig      `toml:"memory"`
	Router      RouterConfig      `toml:"router"`
	Active      Active            `toml:"active"`
	Providers   []Provider        `toml:"providers"`
	Checkpoints CheckpointsConfig `toml:"checkpoints"`
	// MCPServers lists Model Context Protocol servers launched at
	// session start. Each entry becomes a stdio subprocess whose
	// advertised tools register into the agent tool registry under
	// the mcp/<name>/<tool> namespace. v1 supports stdio transport
	// only; absence of a `transport` field means adding HTTP/SSE
	// later is non-breaking.
	MCPServers []MCPServer `toml:"mcp_servers"`
	Theme      ThemeConfig `toml:"theme"`
	// LSP carries optional command overrides for the experimental
	// language-server code-intelligence tools. Defaults remain built in;
	// this only exists for Nix/devcontainer/custom-toolchain paths.
	LSP LSPConfig `toml:"lsp"`
	// Experimental gates non-default features behind named opt-ins.
	// Mirrors the --experimental CLI flag and the
	// $YOTTACODE_EXPERIMENTAL env var. Each entry is a feature name
	// from internal/experimental; values must be `true` to enable.
	// Unrecognized names load without error and emit a startup
	// warning so graduated/removed feature names don't break old
	// configs.
	Experimental map[string]bool `toml:"experimental"`
	// Skills carries persistent Agent Skills preferences — currently
	// just the default-on list seeded into SkillTool.SetEnabled at
	// session start. Absent block keeps the default-off behavior so
	// users without preferences see today's small-prompt experience.
	Skills SkillsConfig `toml:"skills"`
	// Subagents tunes the subagent subsystem (the Agent tool + background
	// runs). Absent block falls through to the defaults below.
	Subagents SubagentsConfig `toml:"subagents"`
}

// SubagentsConfig tunes the subagent subsystem. SessionTokenBudget caps
// the cumulative ESTIMATED tokens spent across ALL subagent runs in one
// session — a backstop against an enthusiastic or adversarial prompt
// fanning out unbounded child loops on the user's API key (the per-child
// iteration cap and the concurrency cap bound one wave, not the session
// total). <=0 falls through to DefaultSubagentSessionTokenBudget.
type SubagentsConfig struct {
	SessionTokenBudget int `toml:"session_token_budget"`
}

// LSPConfig contains optional per-language server command overrides. Keys are
// stable language IDs such as "go", "typescript", "python", and "rust".
type LSPConfig struct {
	Servers  map[string][]string `toml:"servers"`
	Disabled []string            `toml:"disabled"`
}

// DefaultSubagentSessionTokenBudget bounds cumulative subagent spend per
// session. Generous — a normal session's delegations sum well under it —
// but it stops a runaway fan-out from issuing unbounded provider calls on
// the user's key. Override via `[subagents] session_token_budget = N`
// (or 0 in code paths that want it unbounded). The figure is in estimated
// tokens (the same 4-chars-per-token heuristic the status bar uses).
const DefaultSubagentSessionTokenBudget = 8_000_000

// SubagentSessionTokenBudget resolves the configured cap, applying the
// generous default when unset (<=0).
func (c Config) SubagentSessionTokenBudget() int {
	if c.Subagents.SessionTokenBudget > 0 {
		return c.Subagents.SessionTokenBudget
	}
	return DefaultSubagentSessionTokenBudget
}

// SkillsConfig declares persistent Agent Skills behavior. DefaultOn
// is the set of skill names to mark as enabled when each new TUI
// session starts (or a session is resumed). Names that don't match
// any loaded skill produce a startup warning so a typo surfaces
// instead of silently no-op'ing — same pattern as Experimental.
type SkillsConfig struct {
	DefaultOn []string `toml:"default_on"`
}

// ThemeConfig selects the TUI color palette. Name must match a theme
// registered in internal/tui/themes (terminal, catppuccin, dimmed,
// gruvbox, high-contrast, low-contrast, no-color, nord, one-dark,
// solarized-dark, tokyo-night). Empty value falls through to the
// package default; unknown values are rejected at load time so a
// typo surfaces immediately instead of silently snapping back to
// the default.
type ThemeConfig struct {
	Name string `toml:"name"`
}

// CheckpointsConfig tunes the per-prompt file/conversation snapshot
// store behind /checkpoints + Esc Esc. RetentionDays<=0 falls through
// to DefaultCheckpointRetentionDays so the on-disk default doesn't
// require users to write a [checkpoints] block.
type CheckpointsConfig struct {
	RetentionDays int `toml:"retention_days"`
}

// DefaultCheckpointRetentionDays mirrors Claude Code's 30-day TTL —
// long enough to step back through a few days of work, short enough
// that blob storage doesn't grow without bound. Override per-host via
// `[checkpoints] retention_days = N` in config.toml.
const DefaultCheckpointRetentionDays = 30

// RouterConfig describes the multi-provider routing policy. When
// Enabled is false, yottacode dispatches to the single configured
// provider (the legacy behavior). When true, Candidates names an
// ordered list of "<provider>" or "<provider>:<model>" entries and
// Policy selects the dispatch strategy.
//
// Capability gating across providers is a Phase 2 concern: candidates
// listed here must be capability-aligned (e.g. all support
// web_search, or none do) for predictable system-prompt composition.
// The first candidate is the representative for connection probes and
// system-prompt rendering.
//
// HealthWindowSeconds and HealthFailureThreshold control the
// router-level sliding-window failure tracker. After
// HealthFailureThreshold failures within HealthWindowSeconds for a
// candidate, the router demotes that candidate to the back of the
// dispatch order on subsequent requests. A successful turn clears the
// candidate's failure history. Set either to 0 to disable observation
// entirely; defaults are 60 seconds / 3 failures.
type RouterConfig struct {
	Enabled                bool     `toml:"enabled"`
	Policy                 string   `toml:"policy"`
	Candidates             []string `toml:"candidates"`
	HealthWindowSeconds    int      `toml:"health_window_seconds"`
	HealthFailureThreshold int      `toml:"health_failure_threshold"`

	// Mode controls cache-safe task routing between an advisor and an
	// implementer model. "off" (default) disables it entirely. "manual"
	// resolves the role models but only routes when an agent declares an
	// explicit model. "auto" makes the advisor the reasoning/planning model
	// and the implementer the fast coding/subagent/summarization model. The
	// router changes the main-thread model only at explicit session/mode
	// boundaries; child/advisor-consult contexts are isolated.
	Mode string `toml:"mode"`
	// AdvisorModel and ImplementerModel name the role models as
	// "<provider>" or "<provider>:<model>" (same grammar as Candidates).
	// Required when Mode is not "off" (unless the plural form is set).
	AdvisorModel     string `toml:"advisor_model"`
	ImplementerModel string `toml:"implementer_model"`
	// AdvisorModels and ImplementerModels are failover-chain forms: the
	// first entry is the primary, the rest are fallbacks tried in order
	// when the primary fails. A slot may set the singular OR plural form,
	// not both. Empty plural → the singular is used as a one-element chain.
	AdvisorModels     []string `toml:"advisor_models"`
	ImplementerModels []string `toml:"implementer_models"`
	// Fast*/Smart* are legacy aliases for Implementer*/Advisor* kept so
	// existing configs load. New writes use the role-named fields.
	FastModel   string   `toml:"fast_model"`
	SmartModel  string   `toml:"smart_model"`
	FastModels  []string `toml:"fast_models"`
	SmartModels []string `toml:"smart_models"`
}

// RouterMode values for RouterConfig.Mode.
const (
	RouterModeOff    = "off"
	RouterModeManual = "manual"
	RouterModeAuto   = "auto"
)

// ValidRouterModes is the whitelist for RouterConfig.Mode. Empty is
// treated as the default ("off") at load time.
var ValidRouterModes = []string{RouterModeOff, RouterModeManual, RouterModeAuto}

// RoutingEnabled reports whether task routing is active (Mode is
// "manual" or "auto"). Empty/"off" means disabled.
func (r RouterConfig) RoutingEnabled() bool {
	return r.Mode == RouterModeManual || r.Mode == RouterModeAuto
}

// RoutingAuto reports whether automatic (heuristic) routing of
// subagents and summarization is active.
func (r RouterConfig) RoutingAuto() bool {
	return r.Mode == RouterModeAuto
}

// AdvisorChain returns the advisor-model failover chain, falling back to the
// legacy smart_* aliases when the canonical advisor_* fields are absent.
// ImplementerChain is the same for implementer_* with legacy fast_* aliases.
func (r RouterConfig) AdvisorChain() []string {
	if hasModelSlot(r.AdvisorModel, r.AdvisorModels) {
		return modelChain(r.AdvisorModels, r.AdvisorModel)
	}
	return modelChain(r.SmartModels, r.SmartModel)
}

func (r RouterConfig) ImplementerChain() []string {
	if hasModelSlot(r.ImplementerModel, r.ImplementerModels) {
		return modelChain(r.ImplementerModels, r.ImplementerModel)
	}
	return modelChain(r.FastModels, r.FastModel)
}

// FastChain and SmartChain are compatibility accessors for older callers and
// tests. Fast maps to implementer; smart maps to advisor.
func (r RouterConfig) FastChain() []string  { return r.ImplementerChain() }
func (r RouterConfig) SmartChain() []string { return r.AdvisorChain() }

func hasModelSlot(single string, plural []string) bool {
	return strings.TrimSpace(single) != "" || len(modelChain(plural, "")) > 0
}

// modelChain coalesces the plural/singular forms of a router slot into a
// single ordered chain, dropping blank entries.
func modelChain(plural []string, single string) []string {
	src := plural
	if len(src) == 0 {
		src = []string{single}
	}
	out := make([]string, 0, len(src))
	for _, m := range src {
		if t := strings.TrimSpace(m); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// DefaultRouterHealthWindowSeconds is the sliding-window length the
// router uses when the user enables routing without specifying
// health_window_seconds.
const DefaultRouterHealthWindowSeconds = 60

// DefaultRouterHealthFailureThreshold is the number of failures within
// the window that mark a candidate as degraded. Set to 0 in the
// config file to disable observation entirely.
const DefaultRouterHealthFailureThreshold = 3

// ValidPolicies is the whitelist for RouterConfig.Policy. Empty is
// treated as the default (fallback-chain) at construction time.
var ValidPolicies = []string{"fallback-chain", "cheap-first"}

// RetrievalConfig governs the per-turn retrieval orchestrator that
// scores agent-managed memory entries against the user's prompt and
// injects only the most relevant ones into the system prompt.
type RetrievalConfig struct {
	Enabled        bool    `toml:"enabled"`
	TopK           int     `toml:"top_k"`
	MaxBytes       int     `toml:"max_bytes"`
	MinScore       float64 `toml:"min_score"`
	Strategy       string  `toml:"strategy"`
	EmbeddingModel string  `toml:"embedding_model"`

	// SemanticWeight is the fraction of the "semantic" blend given to
	// embedding cosine similarity; BM25 keyword scoring gets the remaining
	// (1 - SemanticWeight). Range [0,1]; default 0.4 (the classic 60/40
	// BM25/cosine split). 0 = pure BM25, 1 = pure cosine. Only used when the
	// effective strategy is "semantic". The blended score is re-normalized
	// afterward, so only the ratio matters — one knob covers the full space.
	SemanticWeight float64 `toml:"semantic_weight"`

	// SessionRecall governs automatic recall of prior conversations — the
	// episodic counterpart to the memory retrieval above. When enabled, each
	// turn semantically searches past sessions and injects the most relevant
	// excerpts into the system prompt, so the agent "remembers" earlier
	// discussions without the model having to call session_recall itself.
	SessionRecall SessionRecallConfig `toml:"session_recall"`
}

// SessionRecallConfig governs automatic injection of relevant past-conversation
// excerpts each turn. Requires semantic embeddings (Ollama), so it is inert
// when the embedding model is unavailable — retrieval degrades to the manual
// session_recall tool. Only reads past sessions; it never writes memory.
type SessionRecallConfig struct {
	// Auto enables per-turn injection. Default true. Set to false to keep the
	// manual session_recall tool but stop automatic injection.
	Auto bool `toml:"auto"`

	// Scope restricts which sessions are searched: "project" (sessions from
	// the current repository — its root and everything below it, so a session
	// started in a subdirectory still counts; the safe default that never
	// mixes projects), "user"/"all" (the whole local store). Empty defaults to
	// "project".
	Scope string `toml:"scope"`

	// TopK caps how many prior-conversation excerpts are injected per turn.
	// Default 3; 0 injects nothing (unlike max_bytes below, where 0 means
	// "no bound" — a cap of zero excerpts reads as none, so that is what it
	// does).
	TopK int `toml:"top_k"`

	// MinScore is the cosine-similarity floor (0.0–1.0) an excerpt must clear
	// to be injected. Default 0.6 — calibrated for nomic-embed-text, whose
	// cosines are compressed (a strongly on-topic paraphrase lands ~0.65,
	// unrelated text ~0.37). High enough that only genuinely relevant prior
	// conversations surface, so the block stays empty when nothing matches
	// rather than padding the prompt with noise.
	MinScore float64 `toml:"min_score"`

	// MaxBytes caps the combined size of the injected block. Default 2000.
	// 0 removes the byte bound (TopK still applies).
	MaxBytes int `toml:"max_bytes"`
}

// ValidStrategies is the whitelist for RetrievalConfig.Strategy.
// Empty is coerced to the default ("auto") at load time.
var ValidStrategies = []string{"keyword", "bm25", "semantic", "auto"}

// ValidSessionRecallScopes is the whitelist for SessionRecallConfig.Scope.
// Empty is coerced to the default ("project") at load time. "user" and "all"
// both search the whole local store (it is already per-user).
var ValidSessionRecallScopes = []string{"project", "user", "all"}

// MemoryConfig governs proactive agent-managed memory behavior beyond
// retrieval (which has its own [retrieval] section).
type MemoryConfig struct {
	// FinalTurnOnQuit, when true, runs one last agent turn on a
	// graceful exit (/quit or Ctrl+D while idle) prompting the model
	// to persist durable learnings via memory_save before the session
	// context is gone. The turn renders in the transcript like any
	// other and is skippable (Ctrl+C / Esc cancels it and quits).
	// Ctrl+C as the quit gesture always exits immediately without the
	// final turn. Default true; set false to make every exit immediate.
	FinalTurnOnQuit bool `toml:"final_turn_on_quit"`

	// CaptureReminderEveryTurns rides a memory-capture reminder on every
	// Nth user message (history-only, like the pre-compaction reminder)
	// so sessions that never hit the summarize watermark still get
	// periodic reinforcement to persist durable learnings. It is not an
	// extra turn and not a per-turn nudge — it appends to a message the
	// user was sending anyway. 0 disables. Default 6.
	CaptureReminderEveryTurns int `toml:"capture_reminder_every_turns"`
}

// ContextConfig governs context-window watermark behavior.
type ContextConfig struct {
	WarnThreshold         float64 `toml:"warn_threshold"`
	AutoThreshold         float64 `toml:"auto_threshold"`
	CompactionThreshold   float64 `toml:"compaction_threshold"`
	CompactionTargetRatio float64 `toml:"compaction_target_ratio"`
	DefaultWindow         int     `toml:"default_window"`
}

// Active selects which configured provider + model is the session
// default. All fields are optional — if Provider is empty the user is
// expected to pass --model / --base-url / --provider via flag or env.
//
// Two TOML keys spell the active model: the new canonical
// `default_model` and the legacy `model`. Both populate the same
// in-memory value: after Load(), Model and DefaultModel are kept in
// sync — whichever the file set (with default_model winning if both
// appear) is mirrored into the other so existing readers (cfg.Active.Model)
// keep working unchanged.
type Active struct {
	Provider     string `toml:"provider"`
	Model        string `toml:"model"`
	DefaultModel string `toml:"default_model"`
}

// Provider describes one upstream model vendor configuration. Two
// provider entries can share the same Kind (e.g. OpenRouter and
// Together both Kind = "openai-compatible") but differ in name +
// base_url + models.
type Provider struct {
	// Name is the user-chosen label for this configuration, unique
	// within the file. Used by /provider use <name>.
	Name string `toml:"name"`

	// Kind selects the adapter family. One of:
	//
	//   anthropic           — native Messages API (claude-*)
	//   openai              — OpenAI's own endpoints (chat completions
	//                         + responses; auto-routes for o-series and
	//                         gpt-5*)
	//   openai-compatible   — anything that speaks /v1/chat/completions
	//                         (vLLM, Llama Stack, OpenRouter, Together,
	//                         NVIDIA NIM, Groq, …)
	//   xai                 — xAI's OpenAI-compatible Grok endpoint
	//   ollama              — Ollama's local server (OpenAI-shim variant)
	//   vertex              — Gemini on Google Vertex AI, via the
	//                         project's OpenAI-compatible chat shim
	//   vertex-anthropic    — Claude on Google Vertex AI, via
	//                         :streamRawPredict (native Messages API)
	//
	// The two vertex kinds authenticate with Application Default
	// Credentials rather than an api_key_env, and carry their GCP project
	// and location inside base_url.
	Kind string `toml:"kind"`

	// BaseURL is the HTTPS endpoint for the API. For Anthropic this is
	// typically https://api.anthropic.com; for OpenAI-compatible
	// endpoints include the /v1 suffix where the upstream expects it.
	BaseURL string `toml:"base_url"`

	// APIKeyEnv is the name of the OS environment variable that holds
	// the bearer token. Empty for local providers like Ollama. Looked
	// up at adapter-construction time, NOT stored here.
	APIKeyEnv string `toml:"api_key_env"`

	// DefaultModel is the model name to adopt when /provider use
	// switches to this provider. Must appear in Models when set.
	DefaultModel string `toml:"default_model"`

	// Models is the catalog of models available through this provider.
	// Used by /model list and as the source of truth for the future
	// auto-router.
	Models []Model `toml:"models"`

	// APIKey is reserved as a tripwire: declaring it inline produces a
	// load-time error that points the user at .env. We don't read its
	// value — declaring it at all is the failure.
	APIKey string `toml:"api_key"`
}

// Model is one entry in a provider's catalog.
type Model struct {
	// Name is the model identifier passed to the API (e.g.
	// "claude-sonnet-4-6"). Required.
	Name string `toml:"name"`

	// Tier is a coarse cost/capability bucket used by the future
	// auto-router. Empty means unspecified. Validated at load time
	// against the whitelist below.
	Tier string `toml:"tier"`

	// ContextWindow overrides yottacode's built-in context-window
	// table for this model. 0 means use the built-in fallback.
	ContextWindow int `toml:"context_window"`
}

// MCPServer describes one Model Context Protocol server launched as a
// stdio subprocess at session start. The MCP client connects over
// stdin/stdout, lists the server's tools, and registers each tool in
// the agent tool registry under mcp/<Name>/<tool>.
//
// The exec.LookPath / spawn / initialize-handshake is performed at
// session start by internal/mcp.Manager — config.Validate only checks
// structural well-formedness. Runtime failures (missing binary, init
// timeout, subprocess crash) are surfaced via /mcp and the next tool
// invocation rather than refusing the entire session.
type MCPServer struct {
	// Name is the unique identifier used in tool namespacing and in
	// the /mcp slash command. Must match mcpNameRE.
	Name string `toml:"name"`

	// Command is the executable that runs the MCP server. Resolved
	// via exec.LookPath at session start.
	Command string `toml:"command"`

	// Args are passed to Command verbatim. Typical shape:
	//   command = "npx"
	//   args    = ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
	Args []string `toml:"args"`

	// Env supplies additional environment variables to the subprocess
	// on top of yottacode's inherited environment. Values may use
	// $VAR substitution from yottacode's process env, resolved at
	// spawn time. Unresolved $VARs surface a startup warning but
	// don't fail load.
	Env map[string]string `toml:"env"`

	// Disabled skips this entry at session start without removing it
	// from the config file. Useful for temporarily quieting a
	// misbehaving server.
	Disabled bool `toml:"disabled"`
}

// mcpNameRE constrains MCPServer.Name to a lowercase-kebab/underscore
// identifier so the tool namespace (mcp/<name>/<tool>) stays
// glob-friendly for permission rules.
var mcpNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// MCPNameValid reports whether name matches the MCP server name
// constraint (lowercase letters, digits, hyphens, underscores;
// must start with a letter).
func MCPNameValid(name string) bool { return mcpNameRE.MatchString(name) }

// ValidTiers is the whitelist for Model.Tier. Empty is also accepted
// (treated as unspecified).
var ValidTiers = []string{"cheap", "balanced", "expensive"}

// ValidKinds is the whitelist for Provider.Kind.
var ValidKinds = []string{"anthropic", "openai", "openai-auth", "copilot", "openai-compatible", "ollama", "gemini", "xai", "vertex", "vertex-anthropic"}

// Default returns a Config populated with the documented defaults.
// defaultThemeName resolves the theme used when the user hasn't
// configured one: the monochrome palette when the NO_COLOR convention
// (https://no-color.org/) is active in the environment — present and
// non-empty, per the spec — and the standard default otherwise. An
// explicit [theme] name in config.toml always wins; no-color.org
// states that user-level configuration overrides the env var.
func defaultThemeName() string {
	if os.Getenv("NO_COLOR") != "" {
		return "no-color"
	}
	return themes.DefaultName
}

func Default() Config {
	return Config{
		Context: ContextConfig{
			WarnThreshold:         0.65,
			AutoThreshold:         0.85,
			CompactionThreshold:   0.70,
			CompactionTargetRatio: 0.35,
			DefaultWindow:         128000,
		},
		Retrieval: RetrievalConfig{
			Enabled:        true,
			TopK:           10,
			MaxBytes:       24000,
			MinScore:       0.0,
			Strategy:       "auto",
			EmbeddingModel: "nomic-embed-text",
			SemanticWeight: 0.4,
			SessionRecall: SessionRecallConfig{
				Auto:     true,
				Scope:    "project",
				TopK:     3,
				MinScore: 0.6,
				MaxBytes: 2000,
			},
		},
		Memory: MemoryConfig{
			FinalTurnOnQuit:           true,
			CaptureReminderEveryTurns: 6,
		},
		Theme: ThemeConfig{
			Name: defaultThemeName(),
		},
	}
}

// DefaultPath returns ~/.yottacode/config.toml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", "config.toml"), nil
}

// Load reads config.toml at the given path, returning the parsed config
// merged onto Default(). A missing file is not an error — defaults are
// returned. Invalid values (out-of-range, malformed, unknown sections)
// ARE an error.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	meta, err := toml.Decode(string(b), &cfg)
	if err != nil {
		return Default(), fmt.Errorf("config: %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		// BurntSushi gives us each unknown key as a Key path. Report
		// the first one — typical user error is a single typo and
		// listing all of them is just noise.
		k := undecoded[0]
		head := strings.SplitN(k.String(), ".", 2)
		switch len(head) {
		case 1:
			return Default(), fmt.Errorf("config: %s: unknown section [%s]", path, head[0])
		default:
			return Default(), fmt.Errorf("config: %s: unknown key %s", path, k.String())
		}
	}
	cfg.Active.normalize()
	if strings.TrimSpace(cfg.Theme.Name) == "" {
		cfg.Theme.Name = defaultThemeName()
	}
	if strings.TrimSpace(cfg.Retrieval.Strategy) == "" {
		cfg.Retrieval.Strategy = "auto"
	}
	if strings.TrimSpace(cfg.Retrieval.EmbeddingModel) == "" {
		cfg.Retrieval.EmbeddingModel = "nomic-embed-text"
	}
	if strings.TrimSpace(cfg.Retrieval.SessionRecall.Scope) == "" {
		cfg.Retrieval.SessionRecall.Scope = "project"
	}
	if err := Validate(cfg); err != nil {
		return Default(), fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// normalize keeps Model and DefaultModel in sync after parsing. The
// canonical key going forward is default_model; legacy configs that
// only set `model` keep working because we mirror the value into both
// fields. If both are set, default_model wins (the user has migrated
// and the legacy line is stale).
func (a *Active) normalize() {
	switch {
	case a.DefaultModel != "":
		a.Model = a.DefaultModel
	case a.Model != "":
		a.DefaultModel = a.Model
	}
}

// LoadDefault loads the file at ~/.yottacode/config.toml.
func LoadDefault() (Config, error) {
	path, err := DefaultPath()
	if err != nil {
		return Default(), err
	}
	return Load(path)
}

// EnsureDefault writes the documented default config.toml at path if
// none exists. Returns the resolved path either way so callers can show
// it to the user. Idempotent — never overwrites an existing file.
func EnsureDefault(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	if err := os.WriteFile(path, []byte(DefaultsTOML), 0o644); err != nil {
		return path, err
	}
	return path, nil
}

// Validate enforces ranges and consistency across the loaded config.
// Returns a clean error rather than silently clamping — clamping means
// the user's intent is lost.
func Validate(cfg Config) error {
	if cfg.Context.WarnThreshold < 0 || cfg.Context.WarnThreshold > 1 {
		return fmt.Errorf("context.warn_threshold = %.3f out of range (0.0–1.0)", cfg.Context.WarnThreshold)
	}
	if cfg.Context.AutoThreshold < 0 || cfg.Context.AutoThreshold > 1 {
		return fmt.Errorf("context.auto_threshold = %.3f out of range (0.0–1.0)", cfg.Context.AutoThreshold)
	}
	if cfg.Context.CompactionThreshold < 0 || cfg.Context.CompactionThreshold > 1 {
		return fmt.Errorf("context.compaction_threshold = %.3f out of range (0.0–1.0)", cfg.Context.CompactionThreshold)
	}
	if cfg.Context.CompactionTargetRatio < 0.10 || cfg.Context.CompactionTargetRatio > 0.80 {
		return fmt.Errorf("context.compaction_target_ratio = %.3f out of range (0.10–0.80)", cfg.Context.CompactionTargetRatio)
	}
	if cfg.Context.DefaultWindow < 1024 {
		return fmt.Errorf("context.default_window = %d too small (minimum 1024)", cfg.Context.DefaultWindow)
	}
	if cfg.Context.AutoThreshold < 1.0 && cfg.Context.WarnThreshold > cfg.Context.AutoThreshold {
		return fmt.Errorf("context.warn_threshold (%.3f) must be <= context.auto_threshold (%.3f) when auto-summarization is enabled",
			cfg.Context.WarnThreshold, cfg.Context.AutoThreshold)
	}
	if cfg.Retrieval.TopK < 0 {
		return fmt.Errorf("retrieval.top_k = %d must be >= 0", cfg.Retrieval.TopK)
	}
	if cfg.Retrieval.MaxBytes < 0 {
		return fmt.Errorf("retrieval.max_bytes = %d must be >= 0 (0 = unlimited)", cfg.Retrieval.MaxBytes)
	}
	if cfg.Retrieval.MinScore < 0 || cfg.Retrieval.MinScore > 1 {
		return fmt.Errorf("retrieval.min_score = %.3f out of range (0.0–1.0)", cfg.Retrieval.MinScore)
	}
	if cfg.Retrieval.SemanticWeight < 0 || cfg.Retrieval.SemanticWeight > 1 {
		return fmt.Errorf("retrieval.semantic_weight = %.3f out of range (0.0–1.0)", cfg.Retrieval.SemanticWeight)
	}
	if cfg.Retrieval.Strategy != "" && !inSlice(ValidStrategies, cfg.Retrieval.Strategy) {
		return fmt.Errorf("retrieval.strategy = %q invalid (expected one of %s)",
			cfg.Retrieval.Strategy, strings.Join(ValidStrategies, ", "))
	}
	if sr := cfg.Retrieval.SessionRecall; sr.Scope != "" && !inSlice(ValidSessionRecallScopes, sr.Scope) {
		return fmt.Errorf("retrieval.session_recall.scope = %q invalid (expected one of %s)",
			sr.Scope, strings.Join(ValidSessionRecallScopes, ", "))
	}
	if cfg.Retrieval.SessionRecall.TopK < 0 {
		return fmt.Errorf("retrieval.session_recall.top_k = %d must be >= 0", cfg.Retrieval.SessionRecall.TopK)
	}
	if cfg.Retrieval.SessionRecall.MaxBytes < 0 {
		return fmt.Errorf("retrieval.session_recall.max_bytes = %d must be >= 0 (0 = unlimited)", cfg.Retrieval.SessionRecall.MaxBytes)
	}
	if cfg.Retrieval.SessionRecall.MinScore < 0 || cfg.Retrieval.SessionRecall.MinScore > 1 {
		return fmt.Errorf("retrieval.session_recall.min_score = %.3f out of range (0.0–1.0)", cfg.Retrieval.SessionRecall.MinScore)
	}
	if cfg.Memory.CaptureReminderEveryTurns < 0 {
		return fmt.Errorf("memory.capture_reminder_every_turns = %d must be >= 0 (0 = disabled)",
			cfg.Memory.CaptureReminderEveryTurns)
	}
	validLSP := map[string]bool{}
	for _, lang := range []string{"go", "typescript", "python", "rust"} {
		validLSP[lang] = true
	}
	for id, cmd := range cfg.LSP.Servers {
		if !validLSP[id] {
			return fmt.Errorf("lsp.servers.%s unknown (expected one of go, typescript, python, rust)", id)
		}
		if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
			return fmt.Errorf("lsp.servers.%s must name a command", id)
		}
	}
	for _, id := range cfg.LSP.Disabled {
		if !validLSP[id] {
			return fmt.Errorf("lsp.disabled contains unknown language %q (expected one of go, typescript, python, rust)", id)
		}
	}

	if name := strings.TrimSpace(cfg.Theme.Name); name != "" && !themes.IsValid(name) {
		return fmt.Errorf("theme.name = %q is not a registered theme (try one of %s)",
			name, strings.Join(themes.Names(), ", "))
	}

	seen := make(map[string]struct{}, len(cfg.Providers))
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if strings.TrimSpace(p.APIKey) != "" {
			return fmt.Errorf("providers[%d] (%q): inline api_key is not supported — set %s in ~/.yottacode/.env or your shell environment instead",
				i, p.Name, providerKeyEnvHint(p))
		}
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("providers[%d]: name is required", i)
		}
		if _, dupe := seen[p.Name]; dupe {
			return fmt.Errorf("providers[%d]: duplicate provider name %q", i, p.Name)
		}
		seen[p.Name] = struct{}{}
		if !inSlice(ValidKinds, p.Kind) {
			return fmt.Errorf("providers[%q]: invalid kind %q (expected one of %s)",
				p.Name, p.Kind, strings.Join(ValidKinds, ", "))
		}
		if strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("providers[%q]: base_url is required", p.Name)
		}
		modelNames := make(map[string]struct{}, len(p.Models))
		for j, m := range p.Models {
			if strings.TrimSpace(m.Name) == "" {
				return fmt.Errorf("providers[%q].models[%d]: name is required", p.Name, j)
			}
			if _, dupe := modelNames[m.Name]; dupe {
				return fmt.Errorf("providers[%q]: duplicate model %q", p.Name, m.Name)
			}
			modelNames[m.Name] = struct{}{}
			if m.Tier != "" && !inSlice(ValidTiers, m.Tier) {
				return fmt.Errorf("providers[%q].models[%q]: invalid tier %q (expected one of %s)",
					p.Name, m.Name, m.Tier, strings.Join(ValidTiers, ", "))
			}
			if m.ContextWindow != 0 && m.ContextWindow < 1024 {
				return fmt.Errorf("providers[%q].models[%q]: context_window = %d too small (minimum 1024)",
					p.Name, m.Name, m.ContextWindow)
			}
		}
		// Free-form providers (Ollama, NVIDIA NIM, custom proxies)
		// carry an empty Models[] catalog; the live model list is
		// filled from /api/tags or /v1/models at runtime. Skip the
		// membership check in that case so the user can declare
		// `default_model = "..."` for a model the catalog hasn't
		// been seeded with — config.Validate would otherwise reject
		// every otherwise-valid free-form add.
		if p.DefaultModel != "" && len(p.Models) > 0 {
			if _, ok := modelNames[p.DefaultModel]; !ok {
				return fmt.Errorf("providers[%q]: default_model %q is not in models",
					p.Name, p.DefaultModel)
			}
		}
	}

	if cfg.Active.Provider != "" {
		if _, ok := seen[cfg.Active.Provider]; !ok {
			return fmt.Errorf("active.provider %q does not match any [[providers]].name",
				cfg.Active.Provider)
		}
		// Validate that active.default_model (when set) appears in the
		// active provider's models catalog. Skipped when the provider's
		// catalog is empty — that's the free-form shape (Ollama, NVIDIA
		// NIM, custom proxies) where the catalog is filled at runtime
		// via live /models fetch.
		if active := cfg.FindProvider(cfg.Active.Provider); active != nil && len(active.Models) > 0 {
			catalog := make(map[string]struct{}, len(active.Models))
			for _, m := range active.Models {
				catalog[m.Name] = struct{}{}
			}
			if cfg.Active.DefaultModel != "" {
				if _, ok := catalog[cfg.Active.DefaultModel]; !ok {
					return fmt.Errorf("active.default_model %q is not in providers[%q].models",
						cfg.Active.DefaultModel, cfg.Active.Provider)
				}
			}
		}
	}

	seenMCP := make(map[string]struct{}, len(cfg.MCPServers))
	for i := range cfg.MCPServers {
		s := &cfg.MCPServers[i]
		if !mcpNameRE.MatchString(s.Name) {
			return fmt.Errorf("mcp_servers[%d]: name %q invalid (must match %s)",
				i, s.Name, mcpNameRE.String())
		}
		if _, dup := seenMCP[s.Name]; dup {
			return fmt.Errorf("mcp_servers[%d]: duplicate name %q", i, s.Name)
		}
		seenMCP[s.Name] = struct{}{}
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("mcp_servers[%q]: command is required", s.Name)
		}
	}

	if cfg.Router.Enabled {
		if cfg.Router.Policy != "" && !inSlice(ValidPolicies, cfg.Router.Policy) {
			return fmt.Errorf("router.policy %q invalid (expected one of %s)",
				cfg.Router.Policy, strings.Join(ValidPolicies, ", "))
		}
		if len(cfg.Router.Candidates) == 0 {
			return errors.New("router.enabled = true requires a non-empty router.candidates list")
		}
		if cfg.Router.HealthWindowSeconds < 0 {
			return fmt.Errorf("router.health_window_seconds = %d must be >= 0",
				cfg.Router.HealthWindowSeconds)
		}
		if cfg.Router.HealthFailureThreshold < 0 {
			return fmt.Errorf("router.health_failure_threshold = %d must be >= 0",
				cfg.Router.HealthFailureThreshold)
		}
		for i, raw := range cfg.Router.Candidates {
			provider, model, err := ParseCandidate(raw)
			if err != nil {
				return fmt.Errorf("router.candidates[%d]: %w", i, err)
			}
			p := cfg.FindProvider(provider)
			if p == nil {
				return fmt.Errorf("router.candidates[%d]: provider %q not found in [[providers]]",
					i, provider)
			}
			if model != "" {
				found := false
				for _, m := range p.Models {
					if m.Name == model {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("router.candidates[%d]: model %q not in providers[%q].models",
						i, model, provider)
				}
			} else if p.DefaultModel == "" {
				return fmt.Errorf("router.candidates[%d]: provider %q has no default_model; specify <provider>:<model>",
					i, provider)
			}
		}
	}

	if cfg.Router.Mode != "" && !inSlice(ValidRouterModes, cfg.Router.Mode) {
		return fmt.Errorf("router.mode %q invalid (expected one of %s)",
			cfg.Router.Mode, strings.Join(ValidRouterModes, ", "))
	}
	// A role slot uses the singular OR the plural (chain) form, not both.
	if cfg.Router.ImplementerModel != "" && len(cfg.Router.ImplementerModels) > 0 {
		return fmt.Errorf("router: set either implementer_model or implementer_models, not both")
	}
	if cfg.Router.AdvisorModel != "" && len(cfg.Router.AdvisorModels) > 0 {
		return fmt.Errorf("router: set either advisor_model or advisor_models, not both")
	}
	if cfg.Router.FastModel != "" && len(cfg.Router.FastModels) > 0 {
		return fmt.Errorf("router: set either fast_model or fast_models, not both")
	}
	if cfg.Router.SmartModel != "" && len(cfg.Router.SmartModels) > 0 {
		return fmt.Errorf("router: set either smart_model or smart_models, not both")
	}
	if hasModelSlot(cfg.Router.ImplementerModel, cfg.Router.ImplementerModels) && hasModelSlot(cfg.Router.FastModel, cfg.Router.FastModels) {
		return fmt.Errorf("router: set implementer_model(s) or legacy fast_model(s), not both")
	}
	if hasModelSlot(cfg.Router.AdvisorModel, cfg.Router.AdvisorModels) && hasModelSlot(cfg.Router.SmartModel, cfg.Router.SmartModels) {
		return fmt.Errorf("router: set advisor_model(s) or legacy smart_model(s), not both")
	}
	if cfg.Router.RoutingEnabled() {
		implementerChain := cfg.Router.ImplementerChain()
		advisorChain := cfg.Router.AdvisorChain()
		if len(implementerChain) == 0 {
			return fmt.Errorf("router.mode = %q requires router.implementer_model (or implementer_models)", cfg.Router.Mode)
		}
		if len(advisorChain) == 0 {
			return fmt.Errorf("router.mode = %q requires router.advisor_model (or advisor_models)", cfg.Router.Mode)
		}
		for i, ref := range implementerChain {
			if err := cfg.validateModelRef(ref); err != nil {
				return fmt.Errorf("router.implementer_model(s)[%d]: %w", i, err)
			}
		}
		for i, ref := range advisorChain {
			if err := cfg.validateModelRef(ref); err != nil {
				return fmt.Errorf("router.advisor_model(s)[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// ParseCandidate splits a "provider" or "provider:model" router
// candidate string. Empty model means "use the provider's default".
// Whitespace is trimmed; an empty input is rejected.
func ParseCandidate(raw string) (provider, model string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("empty candidate")
	}
	parts := strings.SplitN(raw, ":", 2)
	provider = strings.TrimSpace(parts[0])
	if provider == "" {
		return "", "", fmt.Errorf("candidate %q: provider name is empty", raw)
	}
	if len(parts) == 2 {
		model = strings.TrimSpace(parts[1])
		if model == "" {
			return "", "", fmt.Errorf("candidate %q: model after ':' is empty", raw)
		}
	}
	return provider, model, nil
}

// ResolvedCandidate is the fully-resolved view of one router.candidates
// entry: provider profile + concrete model + tier (looked up from
// providers.models). Returned by ResolveCandidates so callers
// (cli.BuildRouter) don't repeat the lookup.
type ResolvedCandidate struct {
	Provider Provider
	Model    string
	Tier     string
}

// ResolveCandidates parses each router.candidates entry and resolves it
// against the provider catalog. Validate has already been called by
// Load, so the caller knows every candidate refers to a real provider
// and a real model — but ResolveCandidates is callable independently
// for tests and for /router introspection.
func (c *Config) ResolveCandidates() ([]ResolvedCandidate, error) {
	out := make([]ResolvedCandidate, 0, len(c.Router.Candidates))
	for i, raw := range c.Router.Candidates {
		rc, err := c.resolveCandidate(raw)
		if err != nil {
			return nil, fmt.Errorf("router.candidates[%d]: %w", i, err)
		}
		out = append(out, rc)
	}
	return out, nil
}

// resolveCandidate parses one "<provider>" or "<provider>:<model>"
// string and resolves it to a concrete provider profile + model + tier.
// Shared by ResolveCandidates and ResolveRouterModels.
func (c *Config) resolveCandidate(raw string) (ResolvedCandidate, error) {
	providerName, model, err := ParseCandidate(raw)
	if err != nil {
		return ResolvedCandidate{}, err
	}
	p := c.FindProvider(providerName)
	if p == nil {
		return ResolvedCandidate{}, fmt.Errorf("provider %q not configured", providerName)
	}
	if model == "" {
		model = p.DefaultModel
	}
	if model == "" {
		return ResolvedCandidate{}, fmt.Errorf("provider %q has no default_model; specify <provider>:<model>", providerName)
	}
	var tier string
	for _, m := range p.Models {
		if m.Name == model {
			tier = m.Tier
			break
		}
	}
	return ResolvedCandidate{Provider: *p, Model: model, Tier: tier}, nil
}

// validateModelRef strictly validates a "<provider>" or
// "<provider>:<model>" reference: the provider must exist and the model
// (when given) must be listed in providers.models. Mirrors the
// router.candidates membership check so router.fast_model / smart_model
// reject typos at load time rather than silently routing to a model the
// provider never declared.
func (c *Config) validateModelRef(raw string) error {
	provider, model, err := ParseCandidate(raw)
	if err != nil {
		return err
	}
	p := c.FindProvider(provider)
	if p == nil {
		return fmt.Errorf("provider %q not found in [[providers]]", provider)
	}
	if model == "" {
		if p.DefaultModel == "" {
			return fmt.Errorf("provider %q has no default_model; specify <provider>:<model>", provider)
		}
		return nil
	}
	for _, m := range p.Models {
		if m.Name == model {
			return nil
		}
	}
	return fmt.Errorf("model %q not in providers[%q].models", model, provider)
}

// ResolveRouterModels resolves the implementer and advisor primary models named
// in the [router] block. Callable only when routing is enabled (Mode != off);
// Validate has already confirmed both strings resolve.
func (c *Config) ResolveRouterModels() (implementer, advisor ResolvedCandidate, err error) {
	implementerChain := c.Router.ImplementerChain()
	if len(implementerChain) == 0 {
		return ResolvedCandidate{}, ResolvedCandidate{}, fmt.Errorf("router.implementer_model: empty")
	}
	advisorChain := c.Router.AdvisorChain()
	if len(advisorChain) == 0 {
		return ResolvedCandidate{}, ResolvedCandidate{}, fmt.Errorf("router.advisor_model: empty")
	}
	implementer, err = c.resolveCandidate(implementerChain[0])
	if err != nil {
		return ResolvedCandidate{}, ResolvedCandidate{}, fmt.Errorf("router.implementer_model: %w", err)
	}
	advisor, err = c.resolveCandidate(advisorChain[0])
	if err != nil {
		return ResolvedCandidate{}, ResolvedCandidate{}, fmt.Errorf("router.advisor_model: %w", err)
	}
	return implementer, advisor, nil
}

// ResolveRouterChains resolves the implementer and advisor failover chains
// to ordered candidate lists — primary first, then fallbacks. Validate has
// confirmed every entry resolves.
func (c *Config) ResolveRouterChains() (implementer, advisor []ResolvedCandidate, err error) {
	if implementer, err = c.resolveChain(c.Router.ImplementerChain()); err != nil {
		return nil, nil, fmt.Errorf("router.implementer_model(s): %w", err)
	}
	if advisor, err = c.resolveChain(c.Router.AdvisorChain()); err != nil {
		return nil, nil, fmt.Errorf("router.advisor_model(s): %w", err)
	}
	return implementer, advisor, nil
}

// resolveChain resolves each ref in a model chain. Mirrors
// ResolveCandidates but for an arbitrary list.
func (c *Config) resolveChain(refs []string) ([]ResolvedCandidate, error) {
	out := make([]ResolvedCandidate, 0, len(refs))
	for i, raw := range refs {
		rc, err := c.resolveCandidate(raw)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out = append(out, rc)
	}
	return out, nil
}

// providerKeyEnvHint returns the env var name to suggest in error
// messages — the user-declared one if present, otherwise a sensible
// default per kind.
func providerKeyEnvHint(p *Provider) string {
	if p.APIKeyEnv != "" {
		return p.APIKeyEnv
	}
	switch p.Kind {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "xai":
		return "XAI_API_KEY"
	default:
		return strings.ToUpper(strings.ReplaceAll(p.Name, "-", "_")) + "_API_KEY"
	}
}

func inSlice(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// FindProvider returns a pointer to the provider with the given name,
// or nil. Pointer receiver lets callers mutate the slice element if
// they need to (e.g. /provider use updating Active.Model on switch).
func (c *Config) FindProvider(name string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}

// ContextWindowOverride returns the user-configured context_window for
// the given model, or 0 when no provider entry sets one. The active
// provider's entry wins when the same model name is listed under more
// than one provider (e.g. two proxies fronting the same model). 0 means
// "no override" — the caller then falls back to the model-tag table and
// default_window via contextwindow.EffectiveWindow.
//
// This is the read side of the per-model window override: the wizard's
// registration probe writes Model.ContextWindow (captured from the
// provider's list-models endpoint), and this surfaces it to the window
// math so the status bar and auto-summarize threshold honor the real,
// provider-reported window instead of a stale built-in guess.
func (c Config) ContextWindowOverride(model string) int {
	if model == "" {
		return 0
	}
	// Active provider first — it's the one the running session uses, so
	// its entry is the authoritative one on a name collision.
	if c.Active.Provider != "" {
		for _, p := range c.Providers {
			if p.Name == c.Active.Provider {
				if w := modelWindow(p.Models, model); w > 0 {
					return w
				}
			}
		}
	}
	// Fall back to any provider that lists the model with an override —
	// covers /resume of a session whose provider isn't currently active.
	for _, p := range c.Providers {
		if w := modelWindow(p.Models, model); w > 0 {
			return w
		}
	}
	return 0
}

// modelWindow returns the first positive ContextWindow among models
// whose Name matches, or 0.
func modelWindow(models []Model, name string) int {
	for _, m := range models {
		if m.Name == name && m.ContextWindow > 0 {
			return m.ContextWindow
		}
	}
	return 0
}

// ProviderKindForModel returns the Kind of the provider entry that
// serves the given model: the active provider when it explicitly names
// the model (models list or default_model), then any provider that
// does, then — for models config never enumerates, like openai-auth's
// scanned set or ollama's local tags — the active provider's Kind.
// Empty only when nothing matches and no provider is active.
//
// Keying per-backend facts on Kind is what keeps namesake models
// separated: gpt-5.5 served through "openai-auth" must not inherit
// numbers from gpt-5.5 served through "openai" (see
// catalog.ResolveWindowForProvider).
func (c Config) ProviderKindForModel(model string) string {
	if model == "" {
		return ""
	}
	var activeKind string
	if c.Active.Provider != "" {
		for _, p := range c.Providers {
			if p.Name == c.Active.Provider {
				activeKind = p.Kind
				if providerServesModel(p, model) {
					return p.Kind
				}
			}
		}
	}
	for _, p := range c.Providers {
		if providerServesModel(p, model) {
			return p.Kind
		}
	}
	return activeKind
}

// providerServesModel reports whether the provider entry explicitly
// names the model, in its models list or as its default_model.
func providerServesModel(p Provider, model string) bool {
	if p.DefaultModel == model {
		return true
	}
	for _, m := range p.Models {
		if m.Name == model {
			return true
		}
	}
	return false
}

// DefaultsTOML is the documented default file written by EnsureDefault.
const DefaultsTOML = `# yottacode configuration
#
# Loaded at session start. Changes apply on the next session start, or
# after running /setup in the TUI.
#
# Values out of range are rejected at load time, not silently clamped.
# Unknown sections and keys are also rejected so typos surface
# immediately.

[context]
# Context-window watermarks. As the running conversation fills the active
# model's context window, yottacode first warns (status bar) and then
# auto-summarizes. All thresholds below are fractions of that window
# (0.0–1.0).
#
# Status bar token counter turns yellow at this fraction of the model's
# context window; a one-time muted notice fires when first crossed and
# again on each 5% increment after. Set to 1.0 to disable warnings.
warn_threshold = 0.65

# Auto-summarization fires before the next turn at this fraction. Must
# be >= warn_threshold when enabled. Set to 1.0 to disable auto-summarization.
auto_threshold = 0.85

# Mid-turn in-loop compaction fires at this fraction while a single long turn is
# still running. It is intentionally earlier than auto_threshold: auto-summary
# remains the richer turn-boundary path, while busy-turn compaction keeps long
# tool loops away from provider hard limits. Set to 1.0 to disable preemptive
# mid-turn compaction; provider-overflow recovery can still force one attempt.
compaction_threshold = 0.70

# Share of the active context window kept verbatim as the recent tail after
# mid-turn compaction. The rest of the budget covers the system prompt, original
# task, compacted progress note, tool schemas, and the next model response.
# Range: 0.10–0.80.
compaction_target_ratio = 0.35

# Fallback context-window size (tokens) for models yottacode does not
# know.
default_window = 128000

[retrieval]
# Per-turn relevance scoring over agent-managed memory entries.
# USER.md, YOTTACODE.md, and both MEMORY.md indexes are always
# injected in full and are NOT subject to retrieval.
enabled = true

# Cap on the number of memory bodies injected per turn (shared across
# user and project scopes). Set to 0 to remove the bound.
top_k = 10

# Cap on the combined bytes of injected memory bodies per turn. Applied
# together with top_k — retrieval stops at whichever binds first — though
# the single top-ranked memory is always admitted even if it alone exceeds
# this. Set to 0 for no byte cap.
max_bytes = 24000

# Minimum relevance score (0.0–1.0) an entry must reach to be
# injected.
min_score = 0.0

# Scoring strategy: "keyword" (legacy exact-token), "bm25" (stemming +
# synonyms + BM25 ranking), "semantic" (bm25 + local Ollama embeddings),
# "auto" (semantic if Ollama + embedding model detected, otherwise bm25).
strategy = "auto"

# Embedding model for semantic retrieval. Only used when strategy is
# "semantic" or "auto". Must be installed in Ollama.
# embedding_model = "nomic-embed-text"

# How much the semantic blend leans on embeddings vs keywords. It's the
# weight given to embedding cosine similarity; BM25 keyword scoring gets
# the rest (1 - semantic_weight). Range 0.0-1.0; default 0.4 (the classic
# 60% BM25 / 40% cosine split). Raise it to trust meaning-based matches
# more (helps paraphrased / low-keyword-overlap queries), lower it to lean
# on exact keywords. 0.0 = pure BM25, 1.0 = pure cosine. Only applies when
# the effective strategy is "semantic".
# semantic_weight = 0.4

# Automatic recall of prior conversations — the episodic counterpart to the
# memory scoring above. When enabled, each turn semantically searches your past
# sessions and injects the most relevant excerpts into the system prompt, so the
# agent "remembers" earlier discussions without having to be asked. Requires a
# local embedding model (Ollama); when unavailable it silently falls back to the
# manual session_recall tool. Reads past sessions only — never writes memory.
[retrieval.session_recall]
# Turn per-turn injection on/off. The manual session_recall tool stays available
# either way.
auto = true

# Which sessions to search: "project" (sessions from the current repository —
# its root and every subdirectory below it, so a session you started deeper in
# the tree still counts; never mixes projects), or "user"/"all" (the whole
# local store).
scope = "project"

# Max prior-conversation excerpts injected per turn. 0 injects nothing.
top_k = 3

# Cosine-similarity floor (0.0–1.0) an excerpt must clear to be injected.
# Calibrated for nomic-embed-text (a strongly on-topic paraphrase lands ~0.65,
# unrelated text ~0.37): only genuinely relevant conversations surface, and the
# block stays empty when nothing matches. Raise it to be stricter.
min_score = 0.6

# Cap on the combined size of the injected block. 0 removes the byte bound.
max_bytes = 2000

[memory]
# Run one final agent turn on a graceful exit (/quit or Ctrl+D while
# idle) prompting the model to persist durable learnings via memory_save
# before the session context is gone. The turn is visible in the
# transcript and skippable (Esc or Ctrl+C cancels it and completes the
# quit); Ctrl+C as the quit gesture itself always exits immediately.
# A session with no turns started this launch skips it. Set to false for
# instant exits.
final_turn_on_quit = true

# Ride a memory-capture reminder on every Nth user message, so sessions
# that never reach the auto-summarize watermark (and end on Ctrl+C, which
# never runs the final turn above) still get periodic reinforcement to
# persist what they learned. It is appended to a message you were sending
# anyway — not an extra turn, and not a per-turn nudge. 0 disables.
capture_reminder_every_turns = 6

# ---------------------------------------------------------------------
# TUI color theme. Uncomment to pin a palette; omit the section to
# ride the default ("terminal"). One of: terminal | catppuccin | grey
# | gruvbox | high-contrast | low-contrast | no-color | nord
# | one-dark | solarized-dark | tokyo-night. Switch interactively
# with /theme.
# ---------------------------------------------------------------------

# [theme]
# name = "catppuccin"

# ---------------------------------------------------------------------
# Provider profiles (uncomment + customize). API keys are NEVER stored
# here — they live in ~/.yottacode/.env or <repo>/.yottacode/.env, or
# the shell environment. Each provider names the env var it expects via
# api_key_env. Multiple providers may share a kind (e.g. two distinct
# openai-compatible endpoints).
#
# Tiers cheap | balanced | expensive let the future auto-router pick a
# model by cost class. Empty tier = unspecified.
# ---------------------------------------------------------------------

# [active]
# provider      = "anthropic"
# default_model = "claude-sonnet-4-6"  # primary model for the session

# [[providers]]
# name          = "anthropic"
# kind          = "anthropic"
# base_url      = "https://api.anthropic.com"
# api_key_env   = "ANTHROPIC_API_KEY"
# default_model = "claude-sonnet-4-6"
#
#   [[providers.models]]
#   name           = "claude-opus-4-7"
#   tier           = "expensive"
#   context_window = 200000
#
#   [[providers.models]]
#   name = "claude-sonnet-4-6"
#   tier = "balanced"
#
#   [[providers.models]]
#   name = "claude-haiku-4-5"
#   tier = "cheap"

# [[providers]]
# name          = "openai"
# kind          = "openai"
# base_url      = "https://api.openai.com/v1"
# api_key_env   = "OPENAI_API_KEY"
# default_model = "gpt-4o"
#
#   [[providers.models]]
#   name = "gpt-4o"
#   tier = "balanced"
#
#   [[providers.models]]
#   name = "o3"
#   tier = "expensive"

# [[providers]]
# name          = "gemini"
# kind          = "gemini"
# base_url      = "https://generativelanguage.googleapis.com"
# api_key_env   = "GEMINI_API_KEY"
# default_model = "gemini-2.5-flash"
#
#   [[providers.models]]
#   name = "gemini-2.5-pro"
#   tier = "expensive"
#
#   [[providers.models]]
#   name = "gemini-2.5-flash"
#   tier = "cheap"

# [[providers]]
# name          = "ollama"
# kind          = "ollama"
# base_url      = "http://localhost:11434/v1"
# default_model = "llama3.1:8b"
#
#   [[providers.models]]
#   name = "llama3.1:8b"
#   tier = "cheap"

# ---------------------------------------------------------------------
# Multi-provider router (opt-in). When enabled, ChatStream calls are
# dispatched across the listed candidates per the chosen policy.
#
# Policies:
#   fallback-chain — try in declared order; fall through on early
#                    failure (before any tokens stream).
#   cheap-first    — sort candidates by tier ascending (cheap →
#                    balanced → expensive); fall through on early
#                    failure.
#
# Each candidate is "<provider>" (uses default_model) or
# "<provider>:<model>" (explicit). Provider names refer to
# [[providers]].name above. Mid-stream failures (after tokens have
# streamed) are terminal — silent restart on a different provider
# would corrupt the user-visible reply. Capability gating is a Phase 2
# concern; today the first candidate's profile is the representative
# for system-prompt composition, so list capability-aligned candidates.
#
# Health observation: after health_failure_threshold failures within
# health_window_seconds for a candidate, the router demotes that
# candidate to the back of the dispatch order on subsequent requests.
# A successful turn clears the candidate's failure history. Set either
# value to 0 to disable observation entirely. Defaults are
# 60 seconds / 3 failures.
# ---------------------------------------------------------------------

# [router]
# enabled                  = true
# policy                   = "fallback-chain"
# candidates               = ["anthropic:claude-haiku-4-5", "openai:gpt-4o"]
# health_window_seconds    = 60
# health_failure_threshold = 3

# ---------------------------------------------------------------------
# Cache-safe role routing (opt-in). Assigns explicit roles to models:
# advisor for planning/design/reasoning, implementer for fast coding,
# subagents, and history compaction. The main-thread model only changes
# at explicit session/mode boundaries (startup, /plan, /auto, /model,
# /router selection), not mid-turn.
#
#   mode = "off"    — disabled (default).
#   mode = "manual" — resolve advisor/implementer; route only subagents
#                     whose definition declares explicit model: frontmatter.
#   mode = "auto"   — startup and /plan use advisor_model; auto-mode work,
#                     summarization, and delegated subagents use
#                     implementer_model. Implementer children can call
#                     consult_advisor for bounded no-tools guidance.
#
# advisor_model / implementer_model use the same "<provider>" or
# "<provider>:<model>" grammar as candidates above. Both are required when
# mode != "off". Legacy smart_model / fast_model aliases still load, but
# new writes use the role-named keys. Reasoning effort is global via
# /effort or --reasoning-effort; router has no per-role effort fields.
#
# Either slot can be a FAILOVER CHAIN instead of a single model via the
# plural form advisor_models / implementer_models = ["<primary>",
# "<fallback>", …]: on failure/timeout the call falls through to the next
# entry using the same health knobs as candidates above. A slot uses the
# singular OR the plural form, not both.
#
# Toggle routing live with the /router command (off <-> auto). The value
# here is the session's starting state.
# ---------------------------------------------------------------------

# [router]
# mode              = "auto"
# advisor_model     = "anthropic:claude-opus-4-6"
# implementer_model = "anthropic:claude-haiku-4-5"
# # or a failover chain for a slot:
# # advisor_models = ["anthropic:claude-opus-4-6", "openai:gpt-4o"]
`
