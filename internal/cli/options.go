package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/dotenv"
)

// ChatOptions carries the flags the root TUI and `run` subcommand share.
// Lives in this package because both internal/tui and internal/oneshot
// consume it.
type ChatOptions struct {
	Model                  string
	BaseURL                string
	APIKey                 string
	SystemPrompt           string
	Resume                 string
	// Summarized requests that, when Resume is non-empty, the session
	// be loaded with its prior transcript replaced by a structured
	// summary (the same path /resume --summarized takes inside the
	// TUI). Defaults to false; only the `resume` subcommand sets it.
	Summarized             bool
	// BypassPermissions auto-approves every tool call without prompting.
	// DANGEROUS — model-emitted commands run without a human in the loop.
	// Explicit `deny` rules in .yottacode/permissions.json are still
	// honored, but every other approval gate is skipped. Reserved for
	// trusted CI / scripted contexts; never enable in shared shells.
	BypassPermissions      bool
	MaxIterations          int
	Provider               string
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

	// AllowPaths is a comma-separated list of additional roots the
	// model's filesystem write tools are allowed to mutate, beyond the
	// session's cwd. Useful when legitimate work spans sibling repos
	// or shared dirs. Empty by default — writes confine to cwd.
	AllowPaths string
}

// Env var names that back the flags. Exported so tests and the TUI
// splash can reference them without hand-typing the strings.
const (
	EnvModel                  = "YOTTACODE_MODEL"
	EnvBaseURL                = "YOTTACODE_BASE_URL"
	EnvAPIKey                 = "YOTTACODE_API_KEY"
	EnvAllowPaths             = "YOTTACODE_ALLOW_PATHS"
	EnvProvider               = "YOTTACODE_PROVIDER"
	EnvReasoningEffort        = "YOTTACODE_REASONING_EFFORT"
	EnvEnableWebSearch        = "YOTTACODE_ENABLE_WEB_SEARCH"
	EnvDisableWebSearch       = "YOTTACODE_DISABLE_WEB_SEARCH"
	EnvEnableXSearch          = "YOTTACODE_ENABLE_X_SEARCH"
	EnvEnableCodeInterpreter  = "YOTTACODE_ENABLE_CODE_INTERPRETER"
	EnvSearchAllowedDomains   = "YOTTACODE_SEARCH_ALLOWED_DOMAINS"
	EnvSearchExcludedDomains  = "YOTTACODE_SEARCH_EXCLUDED_DOMAINS"
	EnvXSearchAllowedHandles  = "YOTTACODE_X_SEARCH_ALLOWED_HANDLES"
	EnvXSearchExcludedHandles = "YOTTACODE_X_SEARCH_EXCLUDED_HANDLES"
	EnvXSearchFromDate        = "YOTTACODE_X_SEARCH_FROM_DATE"
	EnvXSearchToDate          = "YOTTACODE_X_SEARCH_TO_DATE"
)

// Resolve fills empty fields from environment variables and configured
// provider profiles, then errors out when required config (model, base
// URL) is still missing.
//
// Resolution order, lowest precedence first:
//
//  1. Configured provider profile (from ~/.yottacode/config.toml)
//  2. Generic env vars (YOTTACODE_MODEL / _BASE_URL / _API_KEY)
//  3. CLI flags (already populated on opts when this is called)
//
// Stated as fill-in-blanks: any field already set on opts wins; an
// empty field is filled from env, then from the matching profile.
//
// Before reading any env, .env files at ~/.yottacode/.env and
// <cwd>/.yottacode/.env are loaded into the process environment (cwd
// wins, OS env always wins over both). This is how API keys reach
// Resolve without ever living in config.toml.
//
// We deliberately do NOT bake in defaults like qwen3.5:latest or
// localhost:11434 — silently dialing localhost when the user forgot to
// set up Ollama is a worse failure mode than a clear "set --model or
// $YOTTACODE_MODEL" message.
func Resolve(opts *ChatOptions) error {
	loadDotEnvFiles()

	if opts.Model == "" {
		opts.Model = os.Getenv(EnvModel)
	}
	if opts.BaseURL == "" {
		opts.BaseURL = os.Getenv(EnvBaseURL)
	}
	if opts.APIKey == "" {
		opts.APIKey = os.Getenv(EnvAPIKey)
	}
	if opts.Provider == "" {
		opts.Provider = os.Getenv(EnvProvider)
	}

	applyProviderProfile(opts)
	if opts.ReasoningEffort == "" {
		opts.ReasoningEffort = os.Getenv(EnvReasoningEffort)
	}
	if !opts.EnableWebSearch && envTruthy(os.Getenv(EnvEnableWebSearch)) {
		opts.EnableWebSearch = true
	}
	if !opts.DisableWebSearch && envTruthy(os.Getenv(EnvDisableWebSearch)) {
		opts.DisableWebSearch = true
	}
	if !opts.EnableXSearch && envTruthy(os.Getenv(EnvEnableXSearch)) {
		opts.EnableXSearch = true
	}
	if !opts.EnableCodeInterpreter && envTruthy(os.Getenv(EnvEnableCodeInterpreter)) {
		opts.EnableCodeInterpreter = true
	}
	if opts.SearchAllowedDomains == "" {
		opts.SearchAllowedDomains = os.Getenv(EnvSearchAllowedDomains)
	}
	if opts.SearchExcludedDomains == "" {
		opts.SearchExcludedDomains = os.Getenv(EnvSearchExcludedDomains)
	}
	if opts.XSearchAllowedHandles == "" {
		opts.XSearchAllowedHandles = os.Getenv(EnvXSearchAllowedHandles)
	}
	if opts.XSearchExcludedHandles == "" {
		opts.XSearchExcludedHandles = os.Getenv(EnvXSearchExcludedHandles)
	}
	if opts.XSearchFromDate == "" {
		opts.XSearchFromDate = os.Getenv(EnvXSearchFromDate)
	}
	if opts.XSearchToDate == "" {
		opts.XSearchToDate = os.Getenv(EnvXSearchToDate)
	}
	if opts.AllowPaths == "" {
		opts.AllowPaths = os.Getenv(EnvAllowPaths)
	}
	if opts.Model == "" {
		return errors.New("no model set: pass --model <name>, export " + EnvModel + ", or run `yottacode setup` to set up a provider profile")
	}
	if opts.BaseURL == "" {
		return errors.New("no base URL set: pass --base-url <url>, export " + EnvBaseURL + ", or run `yottacode setup` to set up a provider profile")
	}
	if opts.EnableWebSearch && opts.DisableWebSearch {
		return errors.New("web search enable/disable flags are mutually exclusive")
	}
	if opts.SearchAllowedDomains != "" && opts.SearchExcludedDomains != "" {
		return errors.New("search allowed/excluded domains are mutually exclusive")
	}
	if opts.XSearchAllowedHandles != "" && opts.XSearchExcludedHandles != "" {
		return errors.New("x_search allowed/excluded handles are mutually exclusive")
	}
	switch strings.ToLower(strings.TrimSpace(opts.ReasoningEffort)) {
	case "", "low", "medium", "high":
	default:
		return errors.New("invalid reasoning effort: use low, medium, or high")
	}
	return nil
}

// envTruthy reports whether the given env-var value should be treated as
// "yes." Used for boolean env flags so users can write any of the
// usual incantations without us caring.
func envTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// loadDotEnvFiles merges ~/.yottacode/.env and <cwd>/.yottacode/.env
// into the process environment in that order (cwd wins between the
// files; the live OS env always wins over both because Apply skips
// already-set keys). Failures are silent — a malformed .env should not
// prevent yottacode from starting; the user will see the missing key
// downstream as a clearer auth-failure error.
func loadDotEnvFiles() {
	paths := make([]string, 0, 2)
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".yottacode", ".env"))
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".yottacode", ".env"))
	}
	if len(paths) == 0 {
		return
	}
	_, _ = dotenv.LoadAndApply(paths...)
}

// applyProviderProfile fills empty Model/BaseURL/APIKey fields from
// the configured provider profile that matches opts.Provider (or, if
// that's empty, from [active].provider). No-op when config.toml
// doesn't exist or doesn't define matching providers.
//
// API keys are looked up via the profile's api_key_env name — the
// actual secret is expected to live in the live OS environment
// (potentially populated by .env above).
func applyProviderProfile(opts *ChatOptions) {
	cfg, err := config.LoadDefault()
	if err != nil {
		// Surface the config load error to stderr so the user can
		// see WHY their providers aren't being applied. Without
		// this hint, a malformed config silently falls through to
		// "no model set: …run `yottacode setup`" — misleading,
		// because they DO have providers; the file just won't load.
		// Common causes: stale [active].provider after a manual
		// removal, unknown key from an older binary version,
		// validation error (e.g. provider missing base_url).
		fmt.Fprintf(os.Stderr, "warning: %v (using env/flags only)\n", err)
		return
	}
	profileName := opts.Provider
	if profileName == "" {
		profileName = cfg.Active.Provider
	}
	// Fall back to the first configured provider when nothing else
	// has named one. This is the recovery path for a config with
	// providers but no [active] block — e.g. configs hand-edited
	// down to bare entries, or older /provider add flows that
	// appended profiles without ever setting active. Without this,
	// users see "no model set: …run `yottacode setup`" while
	// staring at three working [[providers]] blocks. An explicit
	// --provider flag (or a non-empty active) always wins.
	if profileName == "" && len(cfg.Providers) > 0 {
		profileName = cfg.Providers[0].Name
	}
	if profileName == "" {
		return
	}
	p := cfg.FindProvider(profileName)
	if p == nil {
		return
	}
	// Propagate the resolved profile name so downstream consumers
	// (status-bar render, diagnostics, /provider command) know which
	// profile is active when the user relied on [active].provider
	// rather than passing --provider explicitly.
	if opts.Provider == "" {
		opts.Provider = p.Name
	}
	if opts.BaseURL == "" {
		opts.BaseURL = p.BaseURL
	}
	if opts.Model == "" {
		switch {
		case cfg.Active.Provider == profileName && cfg.Active.Model != "":
			opts.Model = cfg.Active.Model
		case p.DefaultModel != "":
			opts.Model = p.DefaultModel
		}
	}
	if opts.APIKey == "" && p.APIKeyEnv != "" {
		opts.APIKey = os.Getenv(p.APIKeyEnv)
	}
}

