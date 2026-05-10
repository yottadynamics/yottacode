package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/providerops"
)

// printAPIKeyStatus writes a one-line "API key" status to out for
// the given provider: ✔ when the env var is set, ✘ when it's
// missing, or "not required" for providers like Ollama that don't
// declare an api_key_env. Used by `model list` so users can see
// whether a configured provider is wired up end-to-end without
// having to also run `doctor` or shell-test the env.
func printAPIKeyStatus(out io.Writer, p config.Provider) {
	switch {
	case p.APIKeyEnv == "":
		fmt.Fprintln(out, "  API key: not required")
	case os.Getenv(p.APIKeyEnv) != "":
		fmt.Fprintf(out, "  API key: ✔ %s set\n", p.APIKeyEnv)
	default:
		fmt.Fprintf(out, "  API key: ✘ %s missing — run `yottacode provider add` or set in ~/.yottacode/.env\n", p.APIKeyEnv)
	}
}

// newModelCmd builds `yottacode model …` — the cobra mirror of the
// in-TUI /model picker. `model use` writes default_model in [active];
// `model list` dumps catalogs; `model fetch` re-runs the live /models
// call for one provider.
func newModelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Manage the active model (list, use, fetch)",
		Long:  `default_model in [active] is the model yottacode boots into. Use the picker (/model) or model use NAME to swap it.`,
	}
	cmd.AddCommand(
		newModelListCmd(),
		newModelUseCmd(),
		newModelFetchCmd(),
	)
	return cmd
}

func newModelListCmd() *cobra.Command {
	var (
		showAll bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List models for the active provider (or all with --all)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			if len(cfg.Providers) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no providers configured — try `yottacode provider add`")
				return nil
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg.Providers)
			}
			targets := []config.Provider{}
			if showAll {
				targets = cfg.Providers
			} else {
				name := cfg.Active.Provider
				if name == "" {
					name = cfg.Providers[0].Name
				}
				p := cfg.FindProvider(name)
				if p == nil {
					return fmt.Errorf("active.provider %q not in providers", name)
				}
				targets = []config.Provider{*p}
			}
			for _, p := range targets {
				fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", p.Name, p.Kind)
				// Free-form providers carry no static catalog —
				// surface what we do know (configured default_model +
				// API-key status) so the list isn't a dead-end. Live
				// browse goes through `model fetch` or the picker.
				if len(p.Models) == 0 {
					if p.DefaultModel != "" {
						marker := "  "
						if p.DefaultModel == cfg.Active.DefaultModel {
							marker = "▸ "
						}
						fmt.Fprintf(cmd.OutOrStdout(), "%s%s  (configured default)\n", marker, p.DefaultModel)
					} else {
						fmt.Fprintln(cmd.OutOrStdout(), "  (no default model set)")
					}
					printAPIKeyStatus(cmd.OutOrStdout(), p)
					fmt.Fprintf(cmd.OutOrStdout(), "  (free-form — run `yottacode model fetch %s` for the live catalog)\n", p.Name)
					continue
				}
				for _, m := range p.Models {
					marker := "  "
					if m.Name == cfg.Active.DefaultModel {
						marker = "▸ "
					}
					tier := m.Tier
					if tier == "" {
						tier = "—"
					}
					ctx := ""
					if m.ContextWindow > 0 {
						ctx = fmt.Sprintf(" ctx=%dk", m.ContextWindow/1000)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s%-32s [%s]%s\n", marker, m.Name, tier, ctx)
				}
				printAPIKeyStatus(cmd.OutOrStdout(), p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&showAll, "all", false, "List models for every configured provider")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON for scripting")
	return cmd
}

func newModelUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Set the active default_model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			updated, err := providerops.SetActiveModel(cfg, args[0])
			if err != nil {
				return err
			}
			if err := config.Validate(updated); err != nil {
				return fmt.Errorf("config invalid: %w", err)
			}
			if err := config.Save(updated, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set default_model = %q (provider: %s)\n", args[0], updated.Active.Provider)
			return nil
		},
	}
}

func newModelFetchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch [PROVIDER]",
		Short: "Hit the live /models endpoint for a provider and print the merged list",
		Long: `Without an argument, fetches the active provider. Useful for verifying
auth and discovering new models the upstream has shipped.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			name := cfg.Active.Provider
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("no active provider; pass a name or run `yottacode provider use`")
			}
			p := cfg.FindProvider(name)
			if p == nil {
				return fmt.Errorf("provider %q not configured", name)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			key := os.Getenv(p.APIKeyEnv)
			entries, err := catalog.List(ctx, *p, key)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "fetch warning: %v\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", p.Name, p.Kind)
			for _, e := range entries {
				line := e.Label()
				if e.DisplayName != "" && e.DisplayName != e.ID {
					line += "  (" + e.ID + ")"
				}
				if e.ContextWindow > 0 {
					line += fmt.Sprintf("  ctx=%d", e.ContextWindow)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", line)
			}
			return nil
		},
	}
}
