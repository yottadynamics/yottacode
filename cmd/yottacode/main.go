package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/oneshot"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/tui"
	"github.com/yottadynamics/yottacode/internal/version"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	root := newCLI()

	if err := root.ExecuteContext(ctx); err != nil {
		// Some subcommands (e.g. `openai-auth status`) print their
		// own friendly message and use a sentinel to signal exit 1.
		// Suppress the generic "error: ..." prefix in that case so
		// the output stays clean.
		if !errors.Is(err, errOpenAIAuthNotLoggedIn) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

// newCLI wires the root command and its children. A single shared
// ChatOptions struct backs every flag, and the cross-cutting flags
// (--model, --base-url, --api-key, …) are registered as PersistentFlags
// on the root so each subcommand inherits them. With this layout, a
// subcommand's `Flags:` section only lists what is genuinely specific
// to it (e.g. --summarized for resume, --json for doctor), while the
// cross-cutting flags appear under `Global Flags:` — making it obvious
// at a glance which flags belong to which command.
func newCLI() *cobra.Command {
	opts := &cli.ChatOptions{}
	root := newRootCmd(opts)
	bindCommonPersistentFlags(root, opts)
	root.AddCommand(
		newRunCmd(opts),
		newSessionsCmd(opts),
		newMemoryCmd(),
		newDoctorCmd(opts),
		newSetupCmd(),
		newProviderCmd(),
		newModelCmd(),
		newOpenAIAuthCmd(),
		newVersionCmd(),
	)
	return root
}

// newRootCmd builds the top-level command. With no subcommand, `yottacode`
// starts the interactive TUI directly — there is no separate `chat`
// subcommand. `yottacode run "<prompt>"` still exists for one-shot /
// scripting use; it lives as a child command.
func newRootCmd(opts *cli.ChatOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "yottacode",
		Short:   "yottacode — model-agnostic terminal AI agent",
		Version: version.Full(),
		Long: `yottacode opens the Bubbletea TUI for interactive use. For piped
input, scripts, or CI use the non-interactive 'yottacode run' subcommand.

Configuration (no built-in defaults — must be set via flag or env):
  --model      / $YOTTACODE_MODEL      model tag, e.g. gpt-5, claude-..., qwen3.5:latest
  --base-url   / $YOTTACODE_BASE_URL   OpenAI-compatible endpoint
  --api-key    / $YOTTACODE_API_KEY    optional bearer token (Ollama ignores it)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.Resolve(opts); err != nil {
				if shouldAutoLaunchSetup(*opts) {
					if setupErr := runSetupThenResolve(cmd, opts); setupErr != nil {
						return setupErr
					}
				} else {
					return err
				}
			}
			if err := resolveContinue(opts); err != nil {
				return err
			}
			return tui.Run(cmd.Context(), *opts)
		},
	}
	return cmd
}

// shouldAutoLaunchSetup reports whether a Resolve failure should
// offer the wizard. We require: no config.toml on disk AND no
// model / no base-url given via flag or env. This is the
// first-run case — the user typed `yottacode` with nothing wired
// up. A returning user with a stale config gets the regular
// error so they can fix what's wrong rather than be dropped into
// the wizard.
func shouldAutoLaunchSetup(opts cli.ChatOptions) bool {
	if opts.Model != "" || opts.BaseURL != "" {
		return false
	}
	path, err := config.DefaultPath()
	if err != nil {
		return false
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return false
	}
	return true
}

// resolveContinue handles the --continue flag: when set (and --resume
// isn't), it looks up the newest session in the current directory and
// stuffs its ID into opts.Resume so downstream code (tui.Run /
// oneshot.Run) takes the regular resume path. Two error paths surface
// here so the user sees a clean message instead of a vague "session
// load failed" later: --continue + --resume together is a usage
// error; no matching session points the user at how to start fresh.
//
// No-op when --continue is false, so existing flows (--resume, no
// flag) are unaffected.
func resolveContinue(opts *cli.ChatOptions) error {
	if !opts.Continue {
		return nil
	}
	if opts.Resume != "" {
		return errors.New("--continue and --resume are mutually exclusive")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("--continue: %w", err)
	}
	sess, err := session.LatestInCwd(cwd)
	if err != nil {
		if errors.Is(err, session.ErrNoSessionInCwd) {
			return fmt.Errorf("--continue: no saved session in %s — start a new one with `yottacode` (no flag) or list with `yottacode sessions resume`", cwd)
		}
		return fmt.Errorf("--continue: %w", err)
	}
	opts.Resume = sess.ID
	label := sess.ID
	if sess.Name != "" {
		label = fmt.Sprintf("%s (%s)", sess.ID, sess.Name)
	}
	fmt.Fprintf(os.Stderr, "[continue] resuming %s\n", label)
	return nil
}

// runSetupThenResolve runs the wizard then re-resolves opts so the
// just-written config.toml takes effect. The user sees the wizard,
// finishes it, and lands directly in the chat — no second
// invocation needed.
func runSetupThenResolve(cmd *cobra.Command, opts *cli.ChatOptions) error {
	fmt.Fprintln(os.Stderr, "no configuration found — launching setup wizard.")
	fmt.Fprintln(os.Stderr, "(if you already have keys exported, press Esc to abort and try again.)")
	if err := wizard.Run(cmd.Context(), wizard.Options{Out: cmd.OutOrStdout()}); err != nil {
		return err
	}
	return cli.Resolve(opts)
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the yottacode version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", cmd.Root().Name(), version.Full())
			return nil
		},
	}
}

func newRunCmd(opts *cli.ChatOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "Run a single prompt non-interactively (for scripts and CI)",
		Long: `Run executes one prompt against the agent and prints the answer to stdout.
The prompt may be passed as an argument or piped via stdin. Reasoning, tool
status, and errors go to stderr so 'yottacode run "..." > out.md' produces a
clean file. Tool calls that require approval will fail unless
--dangerously-skip-permissions is set (DANGEROUS — see flag help).

Configuration (no built-in defaults — must be set via flag or env):
  --model      / $YOTTACODE_MODEL      model tag
  --base-url   / $YOTTACODE_BASE_URL   OpenAI-compatible endpoint
  --api-key    / $YOTTACODE_API_KEY    optional bearer token`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.Resolve(opts); err != nil {
				return err
			}
			if err := resolveContinue(opts); err != nil {
				return err
			}
			var prompt string
			switch {
			case len(args) == 1:
				prompt = args[0]
			default:
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				prompt = string(data)
			}
			if strings.TrimSpace(prompt) == "" {
				return errors.New("no prompt provided (pass as argument or via stdin)")
			}
			return oneshot.Run(cmd.Context(), *opts, prompt)
		},
	}
	return cmd
}

func newDoctorCmd(opts *cli.ChatOptions) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe provider auth, model visibility, and resolved diagnostics",
		Long: `Doctor runs a lightweight active probe against the configured endpoint.
It resolves the provider profile, calls /models, and reports:

  - endpoint reachability
  - authentication status
  - whether the selected model is visible
  - resolved provider-native capability diagnostics

Use --json for scripting.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.Resolve(opts); err != nil {
				return err
			}
			result := adapter.Probe(cmd.Context(), adapterConfigFromOptions(*opts))
			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), formatDoctorResult(result))
			}
			if len(result.Issues) > 0 {
				return errors.New("doctor found issues")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit doctor results as JSON")
	return cmd
}

// bindCommonPersistentFlags wires every cross-cutting flag onto the
// root command's PersistentFlags so each subcommand inherits them. The
// shared *opts pointer means a flag set on any command writes through
// to the same struct, which is what every subcommand's RunE consumes.
//
// Flags default to "" so cli.Resolve can fall back to YOTTACODE_* env
// vars; missing required config errors out cleanly at startup instead
// of silently dialing a localhost Ollama that might not be running.
func bindCommonPersistentFlags(cmd *cobra.Command, opts *cli.ChatOptions) {
	f := cmd.PersistentFlags()
	f.StringVarP(&opts.Model, "model", "m", "", "Model tag (or set $YOTTACODE_MODEL)")
	f.StringVar(&opts.BaseURL, "base-url", "", "OpenAI-compatible endpoint (or set $YOTTACODE_BASE_URL)")
	f.StringVar(&opts.APIKey, "api-key", "", "Bearer token for the endpoint (or set $YOTTACODE_API_KEY); Ollama ignores it")
	f.StringVar(&opts.Provider, "provider", "", "Provider override: openai | openai-auth | anthropic | gemini | xai | ollama | openai-compatible (or set $YOTTACODE_PROVIDER). openai-auth is the ChatGPT-subscription path (no API key) — log in via 'yottacode openai-auth login' before first use.")
	f.StringVar(&opts.SystemPrompt, "system", "", "Override the default system prompt")
	f.StringVar(&opts.Resume, "resume", "", "Resume session by id or name (set via the /sessions Rename action)")
	f.BoolVarP(&opts.Continue, "continue", "c", false, "Pick up where you left off: resume the most recent session you ran in this directory, without going through the picker. Matches the cwd recorded when the session was created. Use --resume <id|name> to pick a specific session instead. Errors if you pass both, or if no prior session exists here.")
	// --dangerously-skip-permissions mirrors Claude Code's flag of the
	// same name (the user-facing surface). Every approval prompt is
	// skipped and model-emitted commands run without a human in the
	// loop. Explicit `deny` rules in .yottacode/permissions.json are
	// still honored. Use only in trusted CI / scripted contexts.
	f.BoolVar(&opts.BypassPermissions, "dangerously-skip-permissions", false,
		"DANGEROUS: auto-approve every tool call without prompting (deny rules still apply). Reserved for trusted CI / scripted contexts. Mirrors Claude Code's --dangerously-skip-permissions.")
	f.IntVar(&opts.MaxIterations, "max-iterations", 50, "Max tool-call iterations per turn (raise for complex implementation work; runaway-loop guard). Auto mode effectively doubles this.")
	f.StringVar(&opts.ReasoningEffort, "reasoning-effort", "", "Reasoning effort for supported reasoning models: low | medium | high (or set $YOTTACODE_REASONING_EFFORT)")
	f.BoolVar(&opts.EnableWebSearch, "enable-web-search", false, "Enable provider-native web search when the selected provider supports it (enabled by default for OpenAI/xAI)")
	f.BoolVar(&opts.DisableWebSearch, "disable-web-search", false, "Disable provider-native web search even when the selected provider would enable it by default")
	f.BoolVar(&opts.EnableXSearch, "enable-x-search", false, "Enable provider-native X search when the selected provider supports it")
	f.BoolVar(&opts.EnableCodeInterpreter, "enable-code-interpreter", false, "Enable provider-native code interpreter when the selected provider supports it")
	f.StringVar(&opts.SearchAllowedDomains, "search-allowed-domains", "", "Comma-separated domain allowlist for provider-native web search when supported")
	f.StringVar(&opts.SearchExcludedDomains, "search-excluded-domains", "", "Comma-separated domain blocklist for provider-native web search when supported")
	f.StringVar(&opts.XSearchAllowedHandles, "x-search-allowed-handles", "", "Comma-separated X handle allowlist for provider-native x_search")
	f.StringVar(&opts.XSearchExcludedHandles, "x-search-excluded-handles", "", "Comma-separated X handle blocklist for provider-native x_search")
	f.StringVar(&opts.XSearchFromDate, "x-search-from-date", "", "Inclusive YYYY-MM-DD lower bound for provider-native x_search")
	f.StringVar(&opts.XSearchToDate, "x-search-to-date", "", "Inclusive YYYY-MM-DD upper bound for provider-native x_search")
	f.StringVar(&opts.AllowPaths, "allow-paths", "", "Comma-separated additional roots the model's write tools may mutate (or set $YOTTACODE_ALLOW_PATHS); cwd is always allowed")
	f.StringVar(&opts.PermissionMode, "permission-mode", "", "Startup permission `mode`: default | plan | auto. 'plan' starts in read-only research mode (Shift+Tab to exit); 'auto' starts with edits auto-allowed (bash & commits still prompt). Mirrors Claude Code's --permission-mode. No-op for yottacode run.")
	f.StringVar(&opts.PlanResume, "plan-resume", "", "Resume an existing plan by `slug` or substring (matched newest-first against ~/.yottacode/plans/). Implies --permission-mode plan. No-op for yottacode run.")
}

func adapterConfigFromOptions(opts cli.ChatOptions) adapter.Config {
	return adapter.Config{
		BaseURL:                opts.BaseURL,
		APIKey:                 opts.APIKey,
		Model:                  opts.Model,
		ProviderOverride:       adapter.Provider(strings.TrimSpace(opts.Provider)),
		ReasoningEffort:        opts.ReasoningEffort,
		EnableWebSearch:        opts.EnableWebSearch,
		DisableWebSearch:       opts.DisableWebSearch,
		EnableXSearch:          opts.EnableXSearch,
		EnableCodeInterpreter:  opts.EnableCodeInterpreter,
		SearchAllowedDomains:   splitCSV(opts.SearchAllowedDomains),
		SearchExcludedDomains:  splitCSV(opts.SearchExcludedDomains),
		XSearchAllowedHandles:  splitCSV(opts.XSearchAllowedHandles),
		XSearchExcludedHandles: splitCSV(opts.XSearchExcludedHandles),
		XSearchFromDate:        strings.TrimSpace(opts.XSearchFromDate),
		XSearchToDate:          strings.TrimSpace(opts.XSearchToDate),
	}
}

func splitCSV(s string) []string {
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

func formatDoctorResult(result adapter.ProbeResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "provider: %s\n", providerLabel(result.Profile.Provider))
	if result.Profile.UsesResponsesAPI {
		b.WriteString("api-style: responses\n")
	} else {
		b.WriteString("api-style: chat-completions\n")
	}
	if result.BaseURL != "" {
		fmt.Fprintf(&b, "base-url: %s\n", result.BaseURL)
	}
	if result.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", result.Model)
	}
	if len(result.Profile.EnabledBuiltinTools) > 0 {
		parts := make([]string, 0, len(result.Profile.EnabledBuiltinTools))
		for _, tool := range result.Profile.EnabledBuiltinTools {
			parts = append(parts, string(tool))
		}
		fmt.Fprintf(&b, "enabled tools: %s\n", strings.Join(parts, " + "))
	} else {
		b.WriteString("enabled tools: none\n")
	}
	fmt.Fprintf(&b, "probe: endpoint=%s auth=%s model-visible=%s",
		yesNo(result.EndpointReachable),
		yesNo(result.AuthOK),
		yesNo(result.ModelVisible),
	)
	if result.HTTPStatus != 0 {
		fmt.Fprintf(&b, " status=%d", result.HTTPStatus)
	}
	if len(result.AvailableModels) > 0 {
		fmt.Fprintf(&b, "\nmodels: %s", strings.Join(result.AvailableModels, ", "))
	}
	if len(result.Issues) == 0 && len(result.Warnings) == 0 {
		b.WriteString("\nresult: ok")
	} else {
		for _, issue := range result.Issues {
			fmt.Fprintf(&b, "\nissue: %s", issue)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(&b, "\nwarning: %s", warning)
		}
	}
	return b.String()
}

func providerLabel(provider adapter.Provider) string {
	if provider == "" {
		return string(adapter.ProviderOpenAICompatible)
	}
	return string(provider)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
