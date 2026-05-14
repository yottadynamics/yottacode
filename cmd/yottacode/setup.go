package main

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/wizard"
)

// newSetupCmd builds the `yottacode setup` cobra command. The command
// resolves its flags into wizard.Options, then delegates to wizard.Run
// — which is also the function the `/setup` slash command and the
// first-run auto-trigger call into. One entry point keeps the
// behavior identical across launch surfaces.
func newSetupCmd() *cobra.Command {
	var (
		force          bool
		printOnly      bool
		skipValidation bool
		nonInteractive bool
		fromEnv        bool
		providers      string
		active         string
		enableRouter   string // empty | "fallback-chain" | "cheap-first"
		noRouter       bool
		configPath     string
		envPath        string
		noEnvWrite     bool
	)

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Run the first-run setup wizard",
		Long: `Setup walks you through a one-time configuration: pick providers, paste API
keys, choose a default model, optionally enable a multi-provider router, and
write ~/.yottacode/config.toml plus ~/.yottacode/.env (mode 0600).

Re-running setup without --force merges into your existing config — tunables
(context / retrieval) and unrelated providers are preserved.

Common flag-driven shapes:

  yottacode setup                          # interactive (the default)
  yottacode setup -y --from-env            # auto-enable every detected provider
  yottacode setup -y --provider anthropic --active anthropic
  yottacode setup --print-only             # render the would-be plan to stdout`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if enableRouter != "" && noRouter {
				return errors.New("--router and --no-router are mutually exclusive")
			}
			opts := wizard.Options{
				Force:          force,
				PrintOnly:      printOnly,
				SkipValidation: skipValidation,
				NonInteractive: nonInteractive,
				FromEnv:        fromEnv,
				Providers:      splitCSVTrim(providers),
				Active:         active,
				EnableRouter:   enableRouter != "",
				RouterPolicy:   enableRouter,
				NoRouter:       noRouter,
				ConfigPath:     configPath,
				EnvPath:        envPath,
				SkipEnvWrite:   noEnvWrite,
				Out:            cmd.OutOrStdout(),
			}
			return wizard.Run(cmd.Context(), opts)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&force, "force", false,
		"Replace existing config.toml (backed up to <path>.bak-<unix-ts>); without this flag, init merges")
	f.BoolVar(&printOnly, "print-only", false,
		"Render the would-be config.toml + .env plan to stdout; write nothing")
	f.BoolVar(&skipValidation, "skip-validation", false,
		"Skip the per-provider key-validation ping (default: 3s timeout, non-blocking)")
	f.BoolVarP(&nonInteractive, "non-interactive", "y", false,
		"Accept defaults; never prompt. Required fields not resolvable from flags+env produce a clear error")
	f.BoolVar(&fromEnv, "from-env", false,
		"Auto-enable every catalog provider whose API key is already exported, plus Ollama if reachable")
	f.StringVar(&providers, "provider", "",
		"Comma-separated provider names to enable (e.g. 'anthropic,gemini'); skips the multi-select step")
	f.StringVar(&active, "active", "",
		"Default '<provider>' or '<provider>:<model>'; skips the active-selection step")
	f.StringVar(&enableRouter, "router", "",
		"Enable multi-provider router with the given policy: 'fallback-chain' or 'cheap-first'")
	f.BoolVar(&noRouter, "no-router", false,
		"Disable the [router] block (mutually exclusive with --router)")
	f.StringVar(&configPath, "config", "",
		"Override config.toml destination (defaults to ~/.yottacode/config.toml)")
	f.StringVar(&envPath, "env-file", "",
		"Override .env destination (defaults to ~/.yottacode/.env)")
	f.BoolVar(&noEnvWrite, "no-env-write", false,
		"Don't write .env even when keys were entered (the runtime expects them in the live env)")

	// `yottacode setup github` — third-tier auth setup. Adds the
	// PAT-storage path the auth resolver reads at
	// ~/.yottacode/github.json. Sibling to the wizard above
	// rather than a step in it because GitHub auth is opt-in:
	// users who only push via gh or $GITHUB_TOKEN never need to
	// run it.
	cmd.AddCommand(newSetupGitHubCmd())

	return cmd
}

// splitCSVTrim splits a comma-separated flag value, trimming
// whitespace and dropping empty entries. Used by --provider.
func splitCSVTrim(s string) []string {
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
