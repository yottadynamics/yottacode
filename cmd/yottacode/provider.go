package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/dotenv"
	"github.com/yottadynamics/yottacode/internal/providerops"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

// newProviderCmd builds `yottacode provider …` — the cobra mirror of
// the in-TUI /provider sub-menu. Every operation the picker supports
// is reachable here too, with explicit flags so the surface is
// scriptable from CI / dotfiles. Mutations go through providerops to
// share the same validation rules as the picker.
func newProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage configured providers (list, use, add, remove, refresh)",
		Long: `Provider profiles live in ~/.yottacode/config.toml as [[providers]] blocks.
API keys live in ~/.yottacode/.env and are referenced by name only.
Inside the TUI, /provider opens the same operations as a sub-menu.`,
	}
	cmd.AddCommand(
		newProviderListCmd(),
		newProviderUseCmd(),
		newProviderAddCmd(),
		newProviderRemoveCmd(),
		newProviderRefreshCmd(),
	)
	return cmd
}

func newProviderListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			if jsonOut {
				out := struct {
					Active    config.Active     `json:"active"`
					Providers []config.Provider `json:"providers"`
				}{Active: cfg.Active, Providers: cfg.Providers}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			if len(cfg.Providers) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no providers configured — try `yottacode provider add`")
				return nil
			}
			for _, p := range cfg.Providers {
				marker := "  "
				if cfg.Active.Provider == p.Name {
					marker = "▸ "
				}
				def := ""
				if p.DefaultModel != "" {
					def = "  default=" + p.DefaultModel
				}
				identity := p.Kind
				if id := wizard.CatalogIdentity(p.Name); id != "" {
					identity = id
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s%-18s %-20s %s%s\n", marker, p.Name, identity, p.BaseURL, def)
			}
			if cfg.Active.Provider != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "active: %s (model: %s)\n", cfg.Active.Provider, cfg.Active.DefaultModel)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON for scripting")
	return cmd
}

func newProviderUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Switch the active provider in config.toml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			updated, err := providerops.SetActive(cfg, args[0])
			if err != nil {
				return err
			}
			if err := config.Validate(updated); err != nil {
				return fmt.Errorf("config invalid after switch: %w", err)
			}
			if err := config.Save(updated, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "switched active provider to %q (model: %s)\n",
				updated.Active.Provider, updated.Active.DefaultModel)
			return nil
		},
	}
}

func newProviderRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm"},
		Short:   "Delete a configured provider",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			prev := cfg.FindProvider(args[0])
			if prev == nil {
				return fmt.Errorf("provider %q not configured", args[0])
			}
			removedKeyEnv := prev.APIKeyEnv
			wasActive := cfg.Active.Provider == args[0]
			updated, err := providerops.Remove(cfg, args[0])
			if err != nil {
				return err
			}
			// When the active profile was just removed, auto-switch to
			// the first remaining (declaration order). Mirrors the TUI
			// behavior; without this the next `yottacode` invocation
			// errors out with "no model set" until the user manually
			// picks a new active.
			fallback := ""
			if wasActive {
				fallback = providerops.PickFallback(updated)
				if fallback != "" {
					updated, err = providerops.SetActive(updated, fallback)
					if err != nil {
						return fmt.Errorf("auto-switch after remove: %w", err)
					}
				}
			}
			if err := config.Validate(updated); err != nil {
				return fmt.Errorf("config invalid after remove: %w", err)
			}
			if err := config.Save(updated, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed provider %q\n", args[0])
			// Mirror the TUI cleanup: drop the matching API key from
			// ~/.yottacode/.env when no other remaining profile uses
			// it. Skipped silently for empty keyEnv (Ollama,
			// openai-auth) and when the env var is shared.
			if msg := cleanupRemovedAPIKey(removedKeyEnv, updated); msg != "" {
				fmt.Fprintln(cmd.OutOrStdout(), msg)
			}
			switch {
			case wasActive && fallback != "":
				fmt.Fprintf(cmd.OutOrStdout(), "switched active provider to %q (model: %s)\n",
					updated.Active.Provider, updated.Active.DefaultModel)
			case wasActive:
				fmt.Fprintln(cmd.OutOrStdout(), "no other providers configured — run `yottacode provider add` to set one up")
			}
			return nil
		},
	}
}

func newProviderAddCmd() *cobra.Command {
	var (
		kind         string
		baseURL      string
		keyEnv       string
		apiKey       string
		defaultModel string
		smallModel   string
		models       []string
	)
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add a new provider (cloud or self-hosted)",
		Long: `Cloud providers (Anthropic, OpenAI, Gemini, xAI) only need an API key.
Self-hosted / custom OpenAI-compatible providers need a base URL and at
least one model name. Pass --api-key to also write the key to
~/.yottacode/.env (chmod 0600).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kind == "" || baseURL == "" {
				return errors.New("--kind and --base-url are required")
			}
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			modelEntries := make([]config.Model, 0, len(models))
			for _, m := range models {
				m = strings.TrimSpace(m)
				if m == "" {
					continue
				}
				modelEntries = append(modelEntries, config.Model{Name: m})
			}
			// Auto-uniquify the env var name when --key-env (or the
			// kind default) is already taken by another profile. Two
			// NVIDIA NIM profiles can't share NVIDIA_API_KEY because
			// each model has its own bearer; without this, a second
			// profile silently overwrites the first profile's key in
			// .env. The first profile of a kind keeps the well-known
			// default. Empty input (Ollama, openai-auth) returns "".
			effectiveEnv := providerops.SuggestAPIKeyEnv(cfg, args[0], keyEnv)
			updated, err := providerops.Add(cfg, providerops.AddProvider{
				Name:         args[0],
				Kind:         kind,
				BaseURL:      baseURL,
				APIKeyEnv:    effectiveEnv,
				DefaultModel: defaultModel,
				Models:       modelEntries,
			})
			if err != nil {
				return err
			}
			if smallModel != "" {
				// SetActiveModel needs the new provider to BE active.
				// Simplest approach: persist the add first, then if
				// the active provider matches, layer the small_model
				// in via SetActiveModel.
			}
			if err := config.Validate(updated); err != nil {
				return fmt.Errorf("config invalid: %w", err)
			}
			if err := config.Save(updated, ""); err != nil {
				return err
			}
			if apiKey != "" {
				envName := effectiveEnv
				if envName == "" {
					envName = strings.ToUpper(strings.ReplaceAll(args[0], "-", "_")) + "_API_KEY"
				}
				if err := mergeAPIKeyIntoEnvFile(envName, apiKey); err != nil {
					return fmt.Errorf("config saved, but writing .env failed: %w", err)
				}
			}
			// Render the catalog identity (e.g. "nvidia-nim") when we
			// can derive it from the profile name; otherwise the kind.
			identity := kind
			if id := wizard.CatalogIdentity(args[0]); id != "" {
				identity = id
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added provider %q (%s)\n", args[0], identity)
			if effectiveEnv != "" && effectiveEnv != keyEnv && keyEnv != "" {
				// User passed --key-env=X but X was already taken;
				// surface the auto-mint so they don't go hunting for
				// the wrong env var name in .env.
				fmt.Fprintf(cmd.OutOrStdout(),
					"(api_key_env auto-changed to %s because %s is already used by another profile)\n",
					effectiveEnv, keyEnv)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "",
		fmt.Sprintf("Adapter family (one of %s)", strings.Join(config.ValidKinds, ", ")))
	cmd.Flags().StringVar(&baseURL, "base-url", "", "API endpoint URL (required)")
	cmd.Flags().StringVar(&keyEnv, "key-env", "", "Env-var name that holds the API key (e.g. ANTHROPIC_API_KEY)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key value to write to ~/.yottacode/.env (used with --key-env)")
	cmd.Flags().StringVar(&defaultModel, "default-model", "", "Model name to adopt as the default after switch")
	cmd.Flags().StringVar(&smallModel, "small-model", "", "Optional second slot for a cheaper model (Phase 1: persisted only)")
	cmd.Flags().StringSliceVar(&models, "models", nil, "Comma-separated model names to seed the catalog with")
	return cmd
}

func newProviderRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [NAME]",
		Short: "Re-fetch the live model list for a provider (or all)",
		Long:  `Hits the provider's /models endpoint and prints the merged list. No persisted state is changed.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			targets := []config.Provider{}
			switch len(args) {
			case 0:
				targets = cfg.Providers
			default:
				p := cfg.FindProvider(args[0])
				if p == nil {
					return fmt.Errorf("provider %q not configured", args[0])
				}
				targets = []config.Provider{*p}
			}
			if len(targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no providers configured")
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			for _, p := range targets {
				key := os.Getenv(p.APIKeyEnv)
				entries, err := catalog.List(ctx, p, key)
				fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", p.Name, p.Kind)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "  fetch error: %v\n", err)
				}
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
			}
			return nil
		},
	}
}

// mergeAPIKeyIntoEnvFile is a thin wrapper over dotenv.SetKey. The
// merge-and-write logic was duplicated across cmd/ and tui/ until
// the third caller (provider remove cleanup) made the case for
// lifting it; both call sites now go through internal/dotenv.
func mergeAPIKeyIntoEnvFile(name, value string) error {
	path, err := dotenv.DefaultPath()
	if err != nil {
		return err
	}
	return dotenv.SetKey(path, name, value)
}

// cleanupRemovedAPIKey is the cobra mirror of cleanupAPIKeyEnv in
// internal/tui — same three branches: empty keyEnv is a no-op
// (Ollama / openai-auth), shared keyEnv leaves the .env entry alone,
// otherwise the line is dropped from ~/.yottacode/.env and the
// running process is Unsetenv'd. Returns a single line for the
// caller to print, or "" when nothing user-visible happened.
// Errors surface as warnings — the on-disk provider removal already
// succeeded, so failing the whole command on a .env-write hiccup
// would be over-strict.
func cleanupRemovedAPIKey(keyEnv string, remaining config.Config) string {
	if strings.TrimSpace(keyEnv) == "" {
		return ""
	}
	for _, p := range remaining.Providers {
		if p.APIKeyEnv == keyEnv {
			return fmt.Sprintf("(kept %s in ~/.yottacode/.env — still used by provider %q)",
				keyEnv, p.Name)
		}
	}
	path, err := dotenv.DefaultPath()
	if err != nil {
		return fmt.Sprintf("warning: could not resolve .env path: %v", err)
	}
	deleted, err := dotenv.DeleteKey(path, keyEnv)
	if err != nil {
		return fmt.Sprintf("warning: could not clean %s from .env: %v", keyEnv, err)
	}
	_ = os.Unsetenv(keyEnv)
	if deleted {
		return fmt.Sprintf("(removed %s from %s)", keyEnv, path)
	}
	return ""
}

