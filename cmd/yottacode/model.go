package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/dotenv"
	"github.com/yottadynamics/yottacode/internal/providerops"
)

// loadEnvFiles applies ~/.yottacode/.env and <cwd>/.yottacode/.env into
// the process environment (already-set vars win) so CLI commands that
// read provider keys via os.Getenv — like `model fetch`, whose probe
// needs auth — work the same as the TUI, which loads them via
// cli.Resolve. Silent on failure; a missing/malformed .env is not fatal.
func loadEnvFiles() {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".yottacode", ".env"))
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".yottacode", ".env"))
	}
	_, _ = dotenv.LoadAndApply(paths...)
}

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
		newModelProbeWindowsCmd(),
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
		Use:   "fetch [PROVIDER] [MODEL]",
		Short: "Hit the live /models endpoint for a provider and print the merged list",
		Long: `Without arguments, fetches the active provider and probes the active
model's context window when /models doesn't report one. Pass a PROVIDER to
target another configured provider, and a MODEL to probe a specific model
(defaults to the active model for the active provider, else the provider's
default model).`,
		Args: cobra.MaximumNArgs(2),
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
			// Pick the model to probe for a window: an explicit MODEL arg wins;
			// otherwise the active model when this is the active provider (the
			// model you're actually running), else the provider's default. This
			// avoids probing the wrong model when you've switched models under
			// the same provider (e.g. running deepseek under an nvidia-nim
			// provider whose default_model is nemotron).
			probeModel := p.DefaultModel
			if name == cfg.Active.Provider && cfg.Active.DefaultModel != "" {
				probeModel = cfg.Active.DefaultModel
			}
			if len(args) >= 2 {
				probeModel = args[1]
			}
			explicitModel := len(args) >= 2
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			loadEnvFiles() // ensure the provider key from .env is available for the probe's auth
			key := os.Getenv(p.APIKeyEnv)
			entries, err := catalog.List(ctx, *p, key)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "fetch warning: %v\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", p.Name, p.Kind)
			for _, e := range entries {
				if explicitModel && e.ID != probeModel {
					continue
				}
				line := e.Label()
				if e.DisplayName != "" && e.DisplayName != e.ID {
					line += "  (" + e.ID + ")"
				}
				if e.ContextWindow > 0 {
					line += fmt.Sprintf("  ctx=%d", e.ContextWindow)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", line)
			}
			// When the catalog carried no window for this provider's default
			// model (NVIDIA NIM, thin proxies whose /v1/models lists only ids,
			// or any Ollama model), discover it and persist so the status bar +
			// auto-summarize threshold size correctly.
			//
			// Gated to openai-compatible + ollama — the only kinds live
			// discovery speaks (catalog.DiscoverContextWindow: list-models /
			// models.dev for openai-compatible, /api/show for ollama). Curated
			// kinds carry windows in the catalog. The result is written to the
			// file-backed window store (~/.yottacode/context-windows.json), NOT
			// config.toml, so it can't invalidate a free-form provider's config.
			// Probe only runtime-discovered providers. Curated kinds, including xai,
			// already carry catalog/backfilled windows from yotta-models refresh.
			if probeModel != "" && (p.Kind == "openai-compatible" || p.Kind == "ollama") && !modelHasWindow(entries, probeModel) {
				pctx, pcancel := context.WithTimeout(cmd.Context(), 20*time.Second)
				w, detail := catalog.DiscoverContextWindow(pctx, *p, key, probeModel)
				pcancel()
				switch {
				case w > 0:
					if _, err := catalog.UpsertWindow(probeModel, w); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "  save window warning: %v\n", err)
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), "  %s → context_window=%d (cached) [%s]\n", probeModel, w, detail)
					}
				default:
					// Print why so a windowless model isn't a silent no-op.
					fmt.Fprintf(cmd.OutOrStdout(), "  could not determine a context window for %s [%s]\n", probeModel, detail)
				}
			}
			return nil
		},
	}
}

// modelHasWindow reports whether the live catalog already carries a
// context window for the given model id.
func modelHasWindow(entries []catalog.Model, id string) bool {
	for _, e := range entries {
		if e.ID == id && e.ContextWindow > 0 {
			return true
		}
	}
	return false
}

// newModelProbeWindowsCmd fetches each model from a provider's live API and
// probes its context window, persisting the results to the file-backed
// window store (~/.yottacode/context-windows.json by default, or a full
// regenerate of the committed baseline via --output). Only openai-compatible
// and ollama providers are probed; curated providers carry windows in the
// embedded catalog.
func newModelProbeWindowsCmd() *cobra.Command {
	var all bool
	var output string
	var refresh bool
	cmd := &cobra.Command{
		Use:   "probe-windows [PROVIDER]",
		Short: "Probe each model's context window and save it to the window store",
		Long: `Fetches each model from the provider's live /models endpoint and probes its
context window (input-overflow probe for openai-compatible, /api/show for
ollama), then writes the results to ~/.yottacode/context-windows.json so they
serve as the fallback table in later sessions.

Pass --all to sweep every configured provider, or --output PATH to regenerate
the committed baseline (internal/catalog/context-windows.json) before
publishing — existing entries in that file are preserved and updated. Only
openai-compatible and ollama providers are probed; curated providers
(Anthropic, OpenAI, Gemini) carry windows in the catalog.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			loadEnvFiles() // make provider keys from .env available to the probe

			// Most hosted windows come from the local models.dev copy
			// (resolved inside DiscoverContextWindow); --refresh forces it
			// up to date first so the sweep reflects the latest catalog.
			if refresh {
				if n, rerr := catalog.RefreshModelsDev(); rerr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "models.dev refresh failed (using cached copy): %v\n", rerr)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "refreshed models.dev: %d providers\n", n)
				}
			}

			var targets []config.Provider
			if all {
				targets = append(targets, cfg.Providers...)
			} else {
				name := cfg.Active.Provider
				if len(args) >= 1 {
					name = args[0]
				}
				p := cfg.FindProvider(name)
				if p == nil {
					return fmt.Errorf("provider %q not configured", name)
				}
				targets = append(targets, *p)
			}

			out := cmd.OutOrStdout()
			probed := map[string]int{} // exact model id -> window
			for _, p := range targets {
				if p.Kind != "openai-compatible" && p.Kind != "ollama" {
					fmt.Fprintf(out, "%s (%s): skipped — windows come from the catalog\n", p.Name, p.Kind)
					continue
				}
				key := os.Getenv(p.APIKeyEnv)
				lctx, lcancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				entries, lerr := catalog.List(lctx, p, key)
				lcancel()
				if lerr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: list models: %v\n", p.Name, lerr)
					continue
				}
				fmt.Fprintf(out, "%s (%s): %d models\n", p.Name, p.Kind, len(entries))
				for _, e := range entries {
					if e.ContextWindow > 0 {
						probed[e.ID] = e.ContextWindow
						fmt.Fprintf(out, "  %s → %d (from list-models)\n", e.ID, e.ContextWindow)
						continue
					}
					pctx, pcancel := context.WithTimeout(cmd.Context(), 30*time.Second)
					w, detail := catalog.DiscoverContextWindow(pctx, p, key, e.ID)
					pcancel()
					if w > 0 {
						probed[e.ID] = w
						fmt.Fprintf(out, "  %s → %d [%s]\n", e.ID, w, detail)
					} else {
						fmt.Fprintf(out, "  %s → (undetermined) [%s]\n", e.ID, detail)
					}
				}
			}

			if len(probed) == 0 {
				fmt.Fprintln(out, "no windows determined; nothing written")
				return nil
			}

			if output == "" {
				// Merge into the runtime overlay, one upsert per model.
				for id, w := range probed {
					if _, err := catalog.UpsertWindow(id, w); err != nil {
						return fmt.Errorf("write window store: %w", err)
					}
				}
				home, _ := os.UserHomeDir()
				fmt.Fprintf(out, "saved %d windows to %s\n", len(probed),
					filepath.Join(home, ".yottacode", "context-windows.json"))
				return nil
			}

			// --output: merge probed windows into whatever the target file
			// already holds (preserving hand-authored family prefixes) and
			// rewrite it. Intended for regenerating the committed baseline.
			// The existing file's _comment is carried forward — it documents
			// entry conventions (provider-qualified prefixes, measurement
			// provenance) that a regeneration must not erase.
			merged := mergeWindowEntries(catalog.LoadEntriesFromFile(output), probed)
			comment := catalog.StoreComment(output)
			if comment == "" {
				comment = "Fallback context-window table, embedded at build time. Regenerate with `yottacode model probe-windows --output internal/catalog/context-windows.json`. Longest matching prefix wins."
			}
			if err := catalog.WriteWindowStore(output, merged, comment); err != nil {
				return fmt.Errorf("write %s: %w", output, err)
			}
			fmt.Fprintf(out, "wrote %d entries to %s\n", len(merged), output)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "probe every configured provider")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "force-refresh the local models.dev copy before resolving")
	cmd.Flags().StringVar(&output, "output", "",
		"write a full window-store file to PATH (e.g. internal/catalog/context-windows.json) instead of the runtime overlay")
	return cmd
}

// mergeWindowEntries upserts probed exact-id windows into existing entries,
// updating a matching prefix or appending a new exact-id row.
func mergeWindowEntries(existing []catalog.WindowStoreEntry, probed map[string]int) []catalog.WindowStoreEntry {
	out := append([]catalog.WindowStoreEntry(nil), existing...)
	for id, w := range probed {
		lid := strings.ToLower(id)
		found := false
		for i := range out {
			if strings.ToLower(out[i].Prefix) == lid {
				out[i].Window = w
				found = true
				break
			}
		}
		if !found {
			out = append(out, catalog.WindowStoreEntry{Prefix: id, Window: w})
		}
	}
	return out
}
