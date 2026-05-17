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
)

// Config bundles every tunable yottacode reads from disk. Sub-structs map
// 1:1 to TOML sections so the file shape mirrors the Go shape.
type Config struct {
	Context      ContextConfig     `toml:"context"`
	Retrieval    RetrievalConfig   `toml:"retrieval"`
	Router       RouterConfig      `toml:"router"`
	Active       Active            `toml:"active"`
	Providers    []Provider        `toml:"providers"`
	Checkpoints  CheckpointsConfig `toml:"checkpoints"`
	// MCPServers lists Model Context Protocol servers launched at
	// session start. Each entry becomes a stdio subprocess whose
	// advertised tools register into the agent tool registry under
	// the mcp/<name>/<tool> namespace. v1 supports stdio transport
	// only; absence of a `transport` field means adding HTTP/SSE
	// later is non-breaking.
	MCPServers []MCPServer `toml:"mcp_servers"`
	// Experimental gates non-default features behind named opt-ins.
	// Mirrors the --experimental CLI flag and the
	// $YOTTACODE_EXPERIMENTAL env var. Each entry is a feature name
	// from internal/experimental; values must be `true` to enable.
	// Unrecognized names load without error and emit a startup
	// warning so graduated/removed feature names don't break old
	// configs.
	Experimental map[string]bool `toml:"experimental"`
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
	Enabled  bool    `toml:"enabled"`
	TopK     int     `toml:"top_k"`
	MinScore float64 `toml:"min_score"`
}

// ContextConfig governs context-window watermark behavior.
type ContextConfig struct {
	WarnThreshold float64 `toml:"warn_threshold"`
	AutoThreshold float64 `toml:"auto_threshold"`
	DefaultWindow int     `toml:"default_window"`
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
	//                         xAI, NVIDIA NIM, Groq, …)
	//   ollama              — Ollama's local server (OpenAI-shim variant)
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

// ValidTiers is the whitelist for Model.Tier. Empty is also accepted
// (treated as unspecified).
var ValidTiers = []string{"cheap", "balanced", "expensive"}

// ValidKinds is the whitelist for Provider.Kind.
var ValidKinds = []string{"anthropic", "openai", "openai-auth", "openai-compatible", "ollama", "gemini"}

// Default returns a Config populated with the documented defaults.
func Default() Config {
	return Config{
		Context: ContextConfig{
			WarnThreshold: 0.65,
			AutoThreshold: 0.85,
			DefaultWindow: 128000,
		},
		Retrieval: RetrievalConfig{
			Enabled:  true,
			TopK:     10,
			MinScore: 0.0,
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
	if cfg.Context.DefaultWindow < 1024 {
		return fmt.Errorf("context.default_window = %d too small (minimum 1024)", cfg.Context.DefaultWindow)
	}
	if cfg.Context.WarnThreshold > cfg.Context.AutoThreshold {
		return fmt.Errorf("context.warn_threshold (%.3f) must be <= context.auto_threshold (%.3f)",
			cfg.Context.WarnThreshold, cfg.Context.AutoThreshold)
	}
	if cfg.Retrieval.TopK < 0 {
		return fmt.Errorf("retrieval.top_k = %d must be >= 0", cfg.Retrieval.TopK)
	}
	if cfg.Retrieval.MinScore < 0 || cfg.Retrieval.MinScore > 1 {
		return fmt.Errorf("retrieval.min_score = %.3f out of range (0.0–1.0)", cfg.Retrieval.MinScore)
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
		providerName, model, err := ParseCandidate(raw)
		if err != nil {
			return nil, fmt.Errorf("router.candidates[%d]: %w", i, err)
		}
		p := c.FindProvider(providerName)
		if p == nil {
			return nil, fmt.Errorf("router.candidates[%d]: provider %q not configured", i, providerName)
		}
		if model == "" {
			model = p.DefaultModel
		}
		var tier string
		for _, m := range p.Models {
			if m.Name == model {
				tier = m.Tier
				break
			}
		}
		out = append(out, ResolvedCandidate{Provider: *p, Model: model, Tier: tier})
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

// DefaultsTOML is the documented default file written by EnsureDefault.
const DefaultsTOML = `# yottacode configuration
#
# Loaded at session start. Edit and run /memory reload (in the TUI) to
# apply changes mid-session — no restart required.
#
# Values out of range are rejected at load time, not silently clamped.
# Unknown sections and keys are also rejected so typos surface
# immediately.

[context]
# Status bar token counter turns yellow at this fraction of the model's
# context window; a one-time muted notice fires when first crossed and
# again on each 5% increment after. Set to 1.0 to disable warnings.
warn_threshold = 0.65

# Auto-summarization fires before the next turn at this fraction. Must
# be >= warn_threshold. Set to 1.0 to disable auto-summarization.
auto_threshold = 0.85

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

# Minimum relevance score (0.0–1.0) an entry must reach to be
# injected.
min_score = 0.0

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
`
