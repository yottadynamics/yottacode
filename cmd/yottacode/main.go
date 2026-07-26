package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/oneshot"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/tui"
	"github.com/yottadynamics/yottacode/internal/update"
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
		newCopilotAuthCmd(),
		newTrustCmd(),
		newSensitiveCmd(),
		newWorktreeCmd(),
		newMcpCmd(),
		newSkillsCmd(),
		newVersionCmd(),
	)
	applyWrappedUsageTemplate(root)
	return root
}

// applyWrappedUsageTemplate installs a Cobra usage template that wraps
// flag descriptions to the current terminal width (via pflag's
// FlagUsagesWrapped). The default template calls FlagUsages without
// wrapping, which produced one giant unbroken line per flag — readable
// only if your terminal happens to be wide enough. Detected width
// falls back to 100 for non-TTY callers (pipes, CI) so output stays
// stable.
//
// Applied to the root + every subcommand because Cobra resolves the
// usage template per-command, not via inheritance.
func applyWrappedUsageTemplate(root *cobra.Command) {
	width := terminalWidth()
	tmpl := wrappedUsageTemplate(width)
	root.SetUsageTemplate(tmpl)
	for _, c := range root.Commands() {
		c.SetUsageTemplate(tmpl)
	}
}

// terminalWidth returns the current stdout column count, clamped to a
// readable range. Used only for help-text wrapping; the TUI computes
// its own width via Bubbletea's WindowSizeMsg.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 40 {
		if w > 140 {
			return 140
		}
		return w
	}
	return 100
}

// wrappedUsageTemplate is Cobra's default template with two changes:
// `.LocalFlags.FlagUsages` → `.LocalFlags.FlagUsagesWrapped <width>`
// and the same swap for `.InheritedFlags`. Everything else (Usage,
// Aliases, Examples, Available Commands, footer) stays identical so
// `yottacode --help` keeps its familiar shape.
func wrappedUsageTemplate(width int) string {
	return `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
` + fmt.Sprintf("{{.LocalFlags.FlagUsagesWrapped %d | trimTrailingWhitespaces}}", width) + `{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
` + fmt.Sprintf("{{.InheritedFlags.FlagUsagesWrapped %d | trimTrailingWhitespaces}}", width) + `{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
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
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Fire the update check early so cache-miss latency overlaps
			// with cli.Resolve / wizard / continue-resolution. Returns
			// nil when the check is opted out — caller treats nil as
			// "nothing to consume."
			updateCh := maybeStartUpdateCheck(cmd.Context())
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
			// --worktree: materialize (or attach to) the named worktree,
			// chdir into it, and stamp opts.Worktree with the resolved
			// name so tui.Run records it on the session. ensureWorktree
			// is a no-op when --worktree wasn't passed. Runs AFTER
			// resolveContinue so --continue + --worktree composes
			// correctly (find the prior session for this directory,
			// then enter the worktree to run it).
			if err := ensureWorktree(cmd.Context(), opts); err != nil {
				return err
			}
			maybePromptUpgrade(cmd.Context(), updateCh)
			return tui.Run(cmd.Context(), *opts)
		},
	}
	return cmd
}

// shouldRunUpdateCheck gates the once-a-day GitHub poll. We skip when:
//   - YOTTACODE_NO_UPDATE_CHECK=1 (documented opt-out for paranoid
//     users, CI, sandboxes)
//   - stdin or stdout isn't a terminal (pipes, CI, scripts — there's no
//     one to answer the prompt)
//   - version.Current is empty (defensive; release builds always set it)
func shouldRunUpdateCheck() bool {
	if os.Getenv("YOTTACODE_NO_UPDATE_CHECK") == "1" {
		return false
	}
	if version.Current == "" {
		return false
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func maybeStartUpdateCheck(ctx context.Context) <-chan update.Result {
	if !shouldRunUpdateCheck() {
		return nil
	}
	return update.CheckBackground(ctx)
}

// maybePromptUpgrade consumes the update channel with a short timeout
// and, if a newer release exists, asks the user whether to install it.
// On "y" the function downloads the release, verifies it against the
// published SHA256SUMS, replaces the running binary, and exits the process
// on success — the running binary does not exec the new one. On anything
// else (including timeout, channel close, or "n"), control returns to
// the caller and the TUI launches normally.
func maybePromptUpgrade(ctx context.Context, ch <-chan update.Result) {
	if ch == nil {
		return
	}
	var r update.Result
	select {
	case res, ok := <-ch:
		if !ok {
			return
		}
		r = res
	case <-time.After(1500 * time.Millisecond):
		return
	}
	if !r.NewVersion {
		return
	}
	fmt.Fprintf(os.Stderr, "yottacode %s is available (you have %s).\n", r.Latest, r.Current)
	if r.URL != "" {
		fmt.Fprintf(os.Stderr, "Release notes: %s\n", r.URL)
	}
	fmt.Fprint(os.Stderr, "Install now? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		return
	}
	fmt.Fprintln(os.Stderr, "Downloading and verifying release...")
	if err := runInstaller(ctx, r.Latest); err != nil {
		fmt.Fprintf(os.Stderr, "Upgrade failed (%v). Continuing with current version.\n", err)
		fmt.Fprintln(os.Stderr, "To upgrade manually: curl -fsSL https://raw.githubusercontent.com/yottadynamics/yottacode/main/install.sh | bash")
		return
	}
	fmt.Fprintln(os.Stderr, "yottacode upgraded. Run 'yottacode' again to start with the new version.")
	os.Exit(0)
}

// runInstaller upgrades in-process: download the release archive for the
// requested version, verify it against the release's SHA256SUMS, and replace
// the running binary. Unlike the previous `curl … | bash` path, nothing is
// piped to a shell and an unverified download is rejected.
func runInstaller(ctx context.Context, ver string) error {
	return update.InstallRelease(ctx, ver)
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
--yolo is set (DANGEROUS — see flag help).

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
			// --worktree: same handling as the TUI launch path. In
			// oneshot mode the session won't auto-clean the worktree
			// at exit (no interactive keep/remove prompt is possible);
			// users run `yottacode worktree remove <name>` afterward.
			if err := ensureWorktree(cmd.Context(), opts); err != nil {
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
	var noGitHub bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe provider auth, model visibility, and resolved diagnostics",
		Long: `Doctor runs a lightweight active probe against the configured endpoint.
It resolves the provider profile, calls /models, and reports:

  - endpoint reachability
  - authentication status
  - whether the selected model is visible
  - resolved provider-native capability diagnostics
  - GitHub auth + rate-limit snapshot (skip with --no-github)

Use --json for scripting.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.Resolve(opts); err != nil {
				return err
			}
			fileCfg := config.Default()
			if loaded, err := config.LoadDefault(); err == nil {
				fileCfg = loaded
			}
			mediaResult := probeMediaDoctor(cmd.Context())
			lspResult := probeLSPDoctor(cmd.Context(), *opts, fileCfg)
			result := adapter.Probe(cmd.Context(), adapterConfigFromOptions(*opts))
			// --no-github skips the GitHub side entirely. Used by
			// scripted / CI invocations that don't have a token
			// configured and shouldn't have doctor exit non-zero on
			// "no GitHub token found". Also keeps doctor side-effect
			// free for callers that only care about the provider
			// probe.
			if noGitHub {
				if jsonOutput {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					combined := struct {
						adapter.ProbeResult
						LSP   LSPDoctorResult   `json:"lsp_code_intelligence"`
						Media MediaDoctorResult `json:"media_editing"`
					}{ProbeResult: result, LSP: lspResult, Media: mediaResult}
					if err := enc.Encode(combined); err != nil {
						return err
					}
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), formatDoctorResult(result)+renderLSPDoctor(lspResult)+renderMediaDoctor(mediaResult))
				}
				if len(result.Issues) > 0 {
					return errors.New("doctor found issues")
				}
				return nil
			}
			ghResult := probeGitHub(cmd.Context())
			if jsonOutput {
				// JSON envelope: provider-probe fields stay at top
				// level (back-compat — external scripts already
				// parse `endpoint_reachable`, `auth_ok`, etc.), and
				// the GitHub section lives under a sibling `github`
				// key. Embedding ProbeResult flattens its fields.
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				combined := struct {
					adapter.ProbeResult
					GitHub GitHubProbeResult `json:"github"`
					LSP    LSPDoctorResult   `json:"lsp_code_intelligence"`
					Media  MediaDoctorResult `json:"media_editing"`
				}{
					ProbeResult: result,
					GitHub:      ghResult,
					LSP:         lspResult,
					Media:       mediaResult,
				}
				if err := enc.Encode(combined); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), formatDoctorResult(result)+renderGitHubProbe(ghResult)+renderLSPDoctor(lspResult)+renderMediaDoctor(mediaResult))
			}
			if len(result.Issues) > 0 || len(ghResult.Issues) > 0 {
				return errors.New("doctor found issues")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit doctor results as JSON")
	cmd.Flags().BoolVar(&noGitHub, "no-github", false, "Skip the GitHub auth + rate-limit probe (use in CI / no-token environments)")
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
	// Help-text style: one-line summary per flag, env-var pointer in
	// parentheses when applicable, no embedded backticks around literal
	// command references (pflag treats backticks as the type-name
	// placeholder and rewrites the flag header — `--allow-paths` had
	// shown up as `--allow-paths yottacode trust add <path>` because of
	// that). Full prose lives in docs/cli.md.
	f.StringVarP(&opts.Model, "model", "m", "", "Model tag (env: YOTTACODE_MODEL)")
	f.StringVar(&opts.BaseURL, "base-url", "", "OpenAI-compatible endpoint (env: YOTTACODE_BASE_URL)")
	f.StringVar(&opts.APIKey, "api-key", "", "Bearer token; Ollama ignores it (env: YOTTACODE_API_KEY)")
	f.StringVar(&opts.Provider, "provider", "", "openai | openai-auth | copilot | anthropic | gemini | xai | ollama | openai-compatible (env: YOTTACODE_PROVIDER)")
	f.StringVar(&opts.SystemPrompt, "system", "", "Override the default system prompt")
	f.StringVar(&opts.Resume, "resume", "", "Resume a saved session by id or name")
	f.BoolVarP(&opts.Continue, "continue", "c", false, "Resume the newest session created in this cwd (use --resume for a specific one)")
	f.BoolVar(&opts.BypassPermissions, "yolo", false,
		"DANGEROUS: auto-approve every tool call (deny rules still apply). For trusted CI only")
	f.IntVar(&opts.MaxIterations, "max-iterations", 100, "Max tool-call iterations per turn; auto mode raises the effective cap 4×")
	f.StringVar(&opts.ReasoningEffort, "reasoning-effort", "", "low | medium | high (env: YOTTACODE_REASONING_EFFORT)")
	f.BoolVar(&opts.EnableWebSearch, "enable-web-search", false, "Enable provider-native web search (on by default for OpenAI/xAI)")
	f.BoolVar(&opts.DisableWebSearch, "disable-web-search", false, "Force-disable provider-native web search")
	f.BoolVar(&opts.EnableXSearch, "enable-x-search", false, "Enable provider-native X search where supported")
	f.BoolVar(&opts.EnableCodeInterpreter, "enable-code-interpreter", false, "Enable provider-native code interpreter where supported")
	f.StringVar(&opts.SearchAllowedDomains, "search-allowed-domains", "", "Comma-separated domain allowlist for provider web search")
	f.StringVar(&opts.SearchExcludedDomains, "search-excluded-domains", "", "Comma-separated domain blocklist for provider web search")
	f.StringVar(&opts.XSearchAllowedHandles, "x-search-allowed-handles", "", "Comma-separated X handle allowlist for x_search")
	f.StringVar(&opts.XSearchExcludedHandles, "x-search-excluded-handles", "", "Comma-separated X handle blocklist for x_search")
	f.StringVar(&opts.XSearchFromDate, "x-search-from-date", "", "YYYY-MM-DD lower bound for x_search")
	f.StringVar(&opts.XSearchToDate, "x-search-to-date", "", "YYYY-MM-DD upper bound for x_search")
	f.StringVar(&opts.AllowPaths, "allow-paths", "", "Comma-separated dir roots writable for this session; use 'yottacode trust add' to persist (env: YOTTACODE_ALLOW_PATHS)")
	f.StringVar(&opts.PermissionMode, "permission-mode", "", "Startup `mode`: default | plan | auto (TUI only)")
	f.StringVar(&opts.PlanResume, "plan-resume", "", "Resume an existing plan by `slug` or substring; implies --permission-mode plan")
	f.StringSliceVar(&opts.Experimental, "experimental", nil, "Enable experimental feature(s); repeatable or comma-separated (see docs/experimental.md)")
	f.StringVarP(&opts.Worktree, "worktree", "w", "",
		"Run inside a yottacode-managed git worktree at <repo>/.yottacode/worktrees/<name>/ (auto-generates name if no value; copies .worktreeinclude entries)")
	// Cobra's NoOptDefVal lets `--worktree` (no argument) parse to the
	// sentinel that means "auto-generate". Without this, `--worktree`
	// alone would consume the next positional and produce surprising
	// behavior.
	if wt := cmd.PersistentFlags().Lookup("worktree"); wt != nil {
		wt.NoOptDefVal = cli.WorktreeAutoGenerate
	}
}

func adapterConfigFromOptions(opts cli.ChatOptions) adapter.Config {
	providerOverride := strings.TrimSpace(opts.ProviderKind)
	if providerOverride == "" {
		providerOverride = strings.TrimSpace(opts.Provider)
	}
	return adapter.Config{
		BaseURL:                opts.BaseURL,
		APIKey:                 opts.APIKey,
		Model:                  opts.Model,
		ProviderOverride:       adapter.Provider(providerOverride),
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
