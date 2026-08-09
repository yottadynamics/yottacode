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
	"time"

	"github.com/spf13/cobra"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
)

// errOpenAIAuthNotLoggedIn is returned by `status` when the token
// store is missing or empty so cobra's main loop can translate it
// into a non-zero exit. The friendly message is already printed to
// stdout/stderr by emitStatus before this returns; main's standard
// "error: <err>" prefix would double-up, so we set SilenceErrors on
// the status command.
var errOpenAIAuthNotLoggedIn = errors.New("openai-auth: not logged in")

// newOpenAIAuthCmd builds the `yottacode openai-auth` subcommand tree.
//
// Provider-namespaced rather than a generic `auth` subtree so a
// future second OAuth provider (e.g. anthropic-auth) gets its own
// peer command without colliding on a shared word. The three children
// mirror the lifecycle: login (creates the token store), status
// (reads it), logout (removes it).
func newOpenAIAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openai-auth",
		Short: "Manage OpenAI 'Sign in with ChatGPT' OAuth credentials",
		Long: `openai-auth manages the OAuth bearer + refresh tokens for the
openai-auth provider — OpenAI's "Sign in with ChatGPT" flow.

Model calls then bill against the user's ChatGPT subscription rather
than API tokens. Tokens persist at ~/.yottacode/auth/openai-auth.json
(mode 0600); the directory is in the agent's read+write deny lists so
model tools cannot exfiltrate or overwrite them.

  login   browser OAuth flow → save tokens
  logout  delete the token store (server-side refresh token expires naturally)
  status  show login state + expiry`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newOpenAIAuthLoginCmd(),
		newOpenAIAuthLogoutCmd(),
		newOpenAIAuthStatusCmd(),
	)
	return cmd
}

// newOpenAIAuthLoginCmd runs the PKCE + browser flow and persists
// the resulting token set to disk, then fetches this account's live
// model catalog and probes each candidate against the Responses
// endpoint to confirm this account can actually call it, persisting
// the confirmed set to a sibling JSON. The per-candidate table goes
// to stdout (script-parseable); progress + errors go to stderr.
func newOpenAIAuthLoginCmd() *cobra.Command {
	var storePath string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with ChatGPT (browser OAuth), save tokens, and discover available models",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveOpenAIAuthStorePath(storePath)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			// PreLogin prints the URL so the user can paste it
			// manually if the browser launcher fails. Goes to
			// stderr because stdout is reserved for the scan table
			// (which scripts may parse).
			ts, err := openaiauth.Login(ctx, openaiauth.LoginOptions{
				PreLogin: func(url string) {
					fmt.Fprintf(cmd.ErrOrStderr(), "opening browser at:\n  %s\n(if it doesn't open, copy the URL into your browser)\n", url)
				},
			})
			if err != nil {
				return fmt.Errorf("login: %w", err)
			}
			if err := openaiauth.Save(path, ts); err != nil {
				return fmt.Errorf("save tokens: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"logged in as %s (sub=%s)\ntoken expires at %s\nsaved to %s\n",
				orPlaceholder(ts.Email, "<no email claim>"),
				orPlaceholder(ts.AccountID, "<no sub claim>"),
				ts.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"),
				path,
			)

			fmt.Fprintf(cmd.ErrOrStderr(), "\nfetching model catalog and verifying account access via %s\n\n", openaiauth.DefaultCodexEndpoint)
			results, err := openaiauth.ScanWithToken(ctx, ts.AccessToken, openaiauth.ScanOptions{})
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "\nerror: %v; tokens remain saved, rerun login to retry\n", err)
				return err
			}
			renderScanTable(cmd.OutOrStdout(), results)

			ok := openaiauth.OKNames(results)
			if len(ok) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "\nerror: model scan accepted zero candidates; tokens remain saved, rerun login to retry")
				return openaiauth.ErrNoModelsAvailable
			}

			modelsPath, err := openaiauth.DefaultModelsPath()
			if err != nil {
				return fmt.Errorf("resolve models path: %w", err)
			}
			if err := openaiauth.SaveModels(modelsPath, openaiauth.ModelsFile{
				ScannedAt:    time.Now().UTC(),
				Models:       ok,
				DisplayNames: openaiauth.OKDisplayNames(results),
			}); err != nil {
				return fmt.Errorf("save models file: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "\npersisted %d models to %s\n", len(ok), modelsPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "Override token store path (default: ~/.yottacode/auth/openai-auth.json)")
	return cmd
}

// renderScanTable prints the per-candidate scan results in the same
// fixed-width table the standalone probe used to emit. Stdout so
// scripts can parse it; matches the format `awk '$2==\"OK\"'` users
// were already piping the probe's output through.
func renderScanTable(w io.Writer, results []openaiauth.ScanResult) {
	fmt.Fprintf(w, "%-22s %-7s %s\n", "MODEL", "STATUS", "DETAIL")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	for _, r := range results {
		fmt.Fprintf(w, "%-22s %-7s %s\n", r.Name, r.Status, r.Detail)
	}
}

// newOpenAIAuthLogoutCmd deletes the token store file. Does not
// revoke the refresh_token at the OAuth server — Codex CLI doesn't
// either; the token expires naturally per OpenAI's lifecycle.
//
// The confirmation prompt is interactive by default; --yes / -y
// skips it for scripting. A missing store is treated as "already
// logged out" — idempotent rather than an error.
func newOpenAIAuthLogoutCmd() *cobra.Command {
	var storePath string
	var yes bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Delete saved openai-auth tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveOpenAIAuthStorePath(storePath)
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(cmd.OutOrStdout(), "already logged out (no token store at %s)\n", path)
				return nil
			} else if err != nil {
				return fmt.Errorf("stat token store: %w", err)
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "delete %s? (y/N) ", path)
				var resp string
				_, _ = fmt.Fscanln(cmd.InOrStdin(), &resp)
				if !strings.EqualFold(strings.TrimSpace(resp), "y") &&
					!strings.EqualFold(strings.TrimSpace(resp), "yes") {
					fmt.Fprintln(cmd.OutOrStdout(), "cancelled")
					return nil
				}
			}
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("delete token store: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "logged out (deleted %s)\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "Override token store path (default: ~/.yottacode/auth/openai-auth.json)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

// statusReport is the on-disk shape `status --json` emits. Stable
// for scripting consumers; `valid` is the at-a-glance boolean
// (`expires_at` after now and access token present).
type statusReport struct {
	LoggedIn  bool      `json:"logged_in"`
	Valid     bool      `json:"valid"`
	Email     string    `json:"email,omitempty"`
	AccountID string    `json:"account_id,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	StorePath string    `json:"store_path"`
}

// newOpenAIAuthStatusCmd prints whether the user is logged in plus
// the JWT identity claims and expiry. Exit code 0 = logged in (even
// if the token is expired but a refresh is possible), 1 = not logged
// in. Returns errOpenAIAuthNotLoggedIn for the not-logged-in case so
// main translates it into a non-zero exit; SilenceErrors keeps cobra
// from re-printing the message we already wrote to stdout.
func newOpenAIAuthStatusCmd() *cobra.Command {
	var storePath string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Show openai-auth login state and token expiry",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveOpenAIAuthStorePath(storePath)
			if err != nil {
				return err
			}
			rep := statusReport{StorePath: path}

			ts, err := openaiauth.Load(path)
			if err != nil {
				if errors.Is(err, openaiauth.ErrNotFound) {
					return emitStatus(cmd, rep, jsonOutput)
				}
				return fmt.Errorf("load token store: %w", err)
			}
			rep.LoggedIn = ts.AccessToken != ""
			rep.Email = ts.Email
			rep.AccountID = ts.AccountID
			rep.ExpiresAt = ts.ExpiresAt
			// "Valid" = token is loaded and either fresh or has a
			// refresh_token to recover. Mirrors the diagnostics
			// probe's healthy condition.
			rep.Valid = rep.LoggedIn && (!ts.IsExpired(0) || ts.RefreshToken != "")
			return emitStatus(cmd, rep, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "Override token store path (default: ~/.yottacode/auth/openai-auth.json)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit status as JSON")
	return cmd
}

func emitStatus(cmd *cobra.Command, rep statusReport, jsonOutput bool) error {
	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		out := cmd.OutOrStdout()
		switch {
		case !rep.LoggedIn:
			fmt.Fprintf(out, "not logged in (no token at %s)\n", rep.StorePath)
		case !rep.Valid:
			fmt.Fprintf(out, "expired session — re-run `yottacode openai-auth login`\n  store: %s\n", rep.StorePath)
		default:
			expiresIn := time.Until(rep.ExpiresAt).Round(time.Minute)
			expirySummary := fmt.Sprintf("%s (in %s)", rep.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"), expiresIn)
			if rep.ExpiresAt.Before(time.Now()) {
				expirySummary = fmt.Sprintf("%s (expired; will auto-refresh)", rep.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"))
			}
			fmt.Fprintf(out, "logged in as %s (sub=%s)\n  expires: %s\n  store:   %s\n",
				orPlaceholder(rep.Email, "<no email claim>"),
				orPlaceholder(rep.AccountID, "<no sub claim>"),
				expirySummary,
				rep.StorePath,
			)
		}
	}
	if !rep.LoggedIn {
		// Sentinel error → main exits 1. SilenceErrors on the cmd
		// keeps cobra from printing this on top of the friendly
		// message we already emitted above.
		return errOpenAIAuthNotLoggedIn
	}
	return nil
}

func resolveOpenAIAuthStorePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return openaiauth.DefaultStorePath()
}

func orPlaceholder(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}
