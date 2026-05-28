package wizard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/config"
)

// Options is what the cobra command translates flags into. Both the
// interactive and non-interactive paths receive the same struct; the
// interactive program reads from it for pre-fills (so re-running with
// `--provider anthropic` skips the picker), and the non-interactive
// path treats it as the complete answer set.
type Options struct {
	// Force replaces an existing config.toml (after backing up).
	// Without this flag, the wizard merges into the existing file.
	Force bool

	// PrintOnly renders the would-be config.toml + .env plan to
	// out and returns without writing anything.
	PrintOnly bool

	// SkipValidation suppresses the per-key validation pings.
	SkipValidation bool

	// NonInteractive accepts defaults and never prompts. Required
	// fields that can't be resolved from flags + env + catalog
	// produce an error rather than a prompt.
	NonInteractive bool

	// FromEnv pre-enables every catalog entry whose API key is in
	// the live environment, with the catalog's default model.
	// Useful in CI / dotfile bootstrap. Composes with -y.
	FromEnv bool

	// Providers is the comma-separated list passed via --provider.
	// Empty means "let the wizard ask."
	Providers []string

	// Active is the "<provider>" or "<provider>:<model>" form passed
	// via --active.
	Active string

	// EnableRouter / RouterPolicy / NoRouter mirror the flags. The
	// EnableRouter+NoRouter pair is enforced mutually-exclusive at
	// the cobra layer.
	EnableRouter bool
	RouterPolicy string
	NoRouter     bool

	// ConfigPath / EnvPath override defaults (~/.yottacode/...).
	ConfigPath string
	EnvPath    string

	// SkipEnvWrite prevents .env from being written. The wizard
	// still records api_key_env names on each provider so the
	// runtime knows where to look.
	SkipEnvWrite bool

	// Out is where stdout-style messages go: "wrote /path", the
	// print-only TOML body, the post-write hint. nil → os.Stdout.
	Out io.Writer
}

// Resolve fills in defaults and returns the canonical option set.
// Specifically: ConfigPath / EnvPath default to the standard
// locations under $HOME, RouterPolicy defaults to "fallback-chain",
// and Out defaults to os.Stdout.
func (o Options) Resolve() (Options, error) {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.RouterPolicy == "" {
		o.RouterPolicy = "fallback-chain"
	}
	if o.ConfigPath == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return o, err
		}
		o.ConfigPath = path
	}
	if o.EnvPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return o, fmt.Errorf("wizard: home dir: %w", err)
		}
		o.EnvPath = filepath.Join(home, ".yottacode", ".env")
	}
	if o.EnableRouter && o.NoRouter {
		return o, errors.New("--router and --no-router are mutually exclusive")
	}
	return o, nil
}

// Run is the top-level entry point. It dispatches to the interactive
// Bubbletea program or the non-interactive resolver based on opts,
// then writes (or prints) the resulting Plan.
//
// The non-interactive path is also exercised by `--print-only` —
// even an interactive `yottacode setup --print-only` skips the UI and
// just reports what defaults would produce, since printing is its
// own use case (CI bootstrap, "what would this do" inspection).
func Run(ctx context.Context, opts Options) error {
	resolved, err := opts.Resolve()
	if err != nil {
		return err
	}
	out := resolved.Out

	plan, err := buildPlan(ctx, resolved)
	if err != nil {
		return err
	}

	if resolved.PrintOnly {
		fmt.Fprintln(out, "# config.toml")
		fmt.Fprint(out, plan.RenderTOML())
		if envBody := plan.RenderEnv(); envBody != "" {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "# .env")
			fmt.Fprint(out, envBody)
		}
		return nil
	}

	outcome, err := Apply(plan, WriteOptions{
		ConfigPath:   resolved.ConfigPath,
		EnvPath:      resolved.EnvPath,
		Force:        resolved.Force,
		SkipEnvWrite: resolved.SkipEnvWrite || plan.SkipEnvWrite,
	})
	if err != nil {
		return err
	}
	reportOutcome(out, outcome, plan)
	return nil
}

// buildPlan dispatches. Interactive flows go through runInteractive
// (Bubbletea); -y / --from-env flows go through buildPlanFromOptions.
// PrintOnly is a non-interactive build by definition — we don't open
// a UI just to dump TOML.
func buildPlan(ctx context.Context, opts Options) (Plan, error) {
	if opts.NonInteractive || opts.PrintOnly || opts.FromEnv {
		return buildPlanFromOptions(ctx, opts)
	}
	return runInteractive(ctx, opts)
}

// reportOutcome prints the post-write summary. Aligned columns +
// styled checkmarks pick up the polish the interactive review block
// uses; lipgloss falls back to plain text on a non-TTY out so test
// captures and pipes stay clean. Provider/model is rendered with
// " · " (mid-dot) instead of "provider:model" because ollama tags
// already contain colons (qwen3.5:9b) and the joined form is
// ambiguous.
func reportOutcome(out io.Writer, oc WriteOutcome, plan Plan) {
	const labelW = 9 // "wrote" / "active" / "router" + padding to align

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+styleOK.Render("✓")+" "+styleSelected.Render("Setup complete."))
	fmt.Fprintln(out)

	writeLine := func(verb, body string) {
		fmt.Fprintf(out, "    %s %-*s %s\n",
			styleOK.Render("✓"),
			labelW, styleMuted.Render(verb),
			body)
	}

	if oc.WroteBackup {
		writeLine("backed up", stylePathHL.Render(oc.BackupPath))
	}
	if oc.WroteConfig {
		writeLine("wrote", stylePathHL.Render(oc.ConfigPath))
	}
	if oc.WroteEnv {
		writeLine("wrote",
			stylePathHL.Render(oc.EnvPath)+" "+styleDim.Render("(0600)"))
	}
	if plan.ActiveProvider != "" {
		body := styleSelected.Render(plan.ActiveProvider)
		if plan.ActiveModel != "" {
			body += " " + styleMuted.Render("·") + " " + styleSelected.Render(plan.ActiveModel)
		}
		writeLine("active", body)
	}
	if plan.EnableRouter && len(plan.RouterCandList) > 0 {
		body := styleSelected.Render(plan.RouterPolicy) +
			" " + styleMuted.Render("["+strings.Join(plan.RouterCandList, ", ")+"]")
		writeLine("router", body)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+styleHint.Render("Launch with:")+" "+styleSelected.Render("yottacode"))
	fmt.Fprintln(out)
}

// providersForOptions returns the catalog entries that should be
// enabled given opts. Logic:
//
//   - explicit --provider list wins, with unknown names rejected
//   - else if --from-env, every catalog entry whose APIKeyEnv is in
//     the live environment plus Ollama if probe-reachable
//   - else default to "anthropic" alone (the path of least surprise
//     for non-interactive defaulting; users with no key will hit a
//     resolution error downstream and that's the right cliff)
func providersForOptions(ctx context.Context, opts Options) ([]CatalogEntry, error) {
	if len(opts.Providers) > 0 {
		out := make([]CatalogEntry, 0, len(opts.Providers))
		for _, name := range opts.Providers {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			e := FindCatalogEntry(name)
			if e == nil {
				return nil, fmt.Errorf("unknown provider %q (known: %s)", name, knownProviders())
			}
			out = append(out, *e)
		}
		return out, nil
	}
	if opts.FromEnv {
		out := []CatalogEntry{}
		snap := SnapshotEnv()
		seen := map[string]bool{}
		for _, name := range snap.SuggestedProviders {
			if seen[name] {
				continue
			}
			seen[name] = true
			if e := FindCatalogEntry(name); e != nil {
				out = append(out, *e)
			}
		}
		// Always probe Ollama in --from-env: it's the local provider
		// case where there's no env key to detect, but if the daemon
		// is up we should offer it.
		if probe := ProbeOllama(ctx, ""); probe.Reachable {
			if !seen["ollama"] {
				if e := FindCatalogEntry("ollama"); e != nil {
					out = append(out, *e)
				}
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("--from-env: no provider keys detected in environment (set ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY / XAI_API_KEY / NVIDIA_API_KEY, or run Ollama)")
		}
		return out, nil
	}
	// Default: pick the first catalog entry whose key is in env. If
	// none, error out asking for explicit --provider.
	snap := SnapshotEnv()
	if len(snap.SuggestedProviders) > 0 {
		if e := FindCatalogEntry(snap.SuggestedProviders[0]); e != nil {
			return []CatalogEntry{*e}, nil
		}
	}
	return nil, fmt.Errorf("--non-interactive: no provider chosen (pass --provider <name> or set a known *_API_KEY env var)")
}

func knownProviders() string {
	names := make([]string, 0, len(Catalog))
	for _, e := range Catalog {
		names = append(names, e.Name)
	}
	return strings.Join(names, ", ")
}

// buildPlanFromOptions assembles a Plan from flags + env without
// opening the UI. This is the path -y, --from-env, and --print-only
// take.
func buildPlanFromOptions(ctx context.Context, opts Options) (Plan, error) {
	entries, err := providersForOptions(ctx, opts)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		EnvKeys:      map[string]string{},
		SkipEnvWrite: opts.SkipEnvWrite,
	}
	snap := SnapshotEnv()
	for _, e := range entries {
		pp := PlanProvider{
			Name:      e.Name,
			Kind:      e.Kind,
			BaseURL:   e.BaseURL,
			APIKeyEnv: e.APIKeyEnv,
		}
		// Non-interactive setup (-y, --from-env) doesn't pick a
		// default model — there's no curated list to pick a default
		// from anymore (catalog is consumed lazily by the picker, not
		// preselected here) and a free-form provider has no defensible
		// guess. Users running --print-only get a config with empty
		// default_model that they can fill in by hand or via a follow-
		// up `yottacode setup`.
		if e.APIKeyEnv != "" && snap.Present[e.APIKeyEnv] {
			pp.EnvAlreadySet = true
		}
		// Free-form providers without explicit models stay as-is
		// (the runtime accepts a provider with no models entry as
		// long as default_model is empty too — but the wizard's
		// non-interactive path can't pick a free-form model out of
		// thin air, so error if that's the active provider).
		plan.Providers = append(plan.Providers, pp)
	}

	// Active resolution — explicit flag, else first enabled, else err.
	if opts.Active != "" {
		provider, model, err := config.ParseCandidate(opts.Active)
		if err != nil {
			return Plan{}, fmt.Errorf("--active: %w", err)
		}
		plan.ActiveProvider = provider
		plan.ActiveModel = model
		// Validate provider was actually enabled.
		hit := false
		for _, pp := range plan.Providers {
			if pp.Name == provider {
				hit = true
				if model == "" {
					plan.ActiveModel = pp.DefaultModel
				}
				break
			}
		}
		if !hit {
			return Plan{}, fmt.Errorf("--active provider %q not in --provider list", provider)
		}
	} else {
		// Default: first enabled with a default_model. Free-form
		// without a model is a hard error in -y mode.
		for _, pp := range plan.Providers {
			if pp.DefaultModel != "" {
				plan.ActiveProvider = pp.Name
				plan.ActiveModel = pp.DefaultModel
				break
			}
		}
		if plan.ActiveProvider == "" {
			return Plan{}, fmt.Errorf("--non-interactive: no provider has a default model (pass --active <provider>:<model>)")
		}
	}

	// Router: --router enables, --no-router disables, neither leaves it off.
	if opts.EnableRouter {
		plan.EnableRouter = true
		plan.RouterPolicy = opts.RouterPolicy
		// Candidate list = enabled providers in the order they appear,
		// each with their default model.
		for _, pp := range plan.Providers {
			if pp.DefaultModel != "" {
				plan.RouterCandList = append(plan.RouterCandList, pp.Name+":"+pp.DefaultModel)
			} else {
				plan.RouterCandList = append(plan.RouterCandList, pp.Name)
			}
		}
	}

	// Retrieval: when Ollama is among the enabled providers and is
	// reachable, check for installed embedding models. If found, enable
	// semantic search. Don't auto-pull (consistency: we never auto-pull
	// chat models in non-interactive mode either).
	for _, pp := range plan.Providers {
		if pp.Kind == "ollama" {
			probe := ProbeOllama(ctx, "")
			if probe.Reachable {
				detected := DetectEmbeddingModels(probe.Models)
				if len(detected) > 0 {
					plan.RetrievalStrategy = "auto"
					plan.EmbeddingModel = detected[0]
				}
			}
			break
		}
	}

	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

