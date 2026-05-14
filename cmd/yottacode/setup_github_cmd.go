package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	ghpkg "github.com/yottadynamics/yottacode/internal/github"
)

// newSetupGitHubCmd builds the `yottacode setup github` subcommand
// — tier-3 of the auth precedence chain
// ($GITHUB_TOKEN → gh auth token → ~/.yottacode/github.json).
//
// Two non-interactive shapes:
//
//	yottacode setup github --token "ghp_..."     # write the token directly
//	yottacode setup github --remove              # delete the saved token
//
// Default shape is interactive: prints the GitHub PAT-creation
// URL with the recommended scope pre-selected, reads the token
// from stdin with masked input (golang.org/x/term), verifies
// it works via a single Users.Get call, then atomically writes
// the file via WriteTokenFile.
//
// We deliberately don't surface device-OAuth flow as an
// option: PAT requires no yottadynamics-side infrastructure
// (no OAuth app to register/maintain), and adding device flow
// later is additive — the token slot in the file is the same
// either way.
func newSetupGitHubCmd() *cobra.Command {
	var (
		token  string
		remove bool
	)

	cmd := &cobra.Command{
		Use:   "github",
		Short: "Set up GitHub auth (tier-3 of the auth precedence chain)",
		Long: `Configure yottacode's GitHub access by saving a Personal Access
Token to ~/.yottacode/github.json — the third tier in the auth
resolver, behind $GITHUB_TOKEN and 'gh auth token'.

Interactive (default): prints the GitHub PAT-creation URL with
recommended scopes, prompts for the token (input hidden), verifies
the token works against GitHub's API, and writes it atomically
with 0600 permissions.

Non-interactive:

  yottacode setup github --token "ghp_..."      # save the supplied token (still verified)
  yottacode setup github --remove               # delete the saved token

After setup, every yottacode invocation can use the token without
having 'gh' installed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if remove {
				return runSetupGitHubRemove(out)
			}
			supplied := strings.TrimSpace(token)
			if supplied == "" {
				// Interactive path: explain, prompt, mask.
				var err error
				supplied, err = promptForToken(out, cmd.InOrStdin())
				if err != nil {
					return err
				}
			}
			if supplied == "" {
				return errors.New("no token provided")
			}
			return runSetupGitHubSave(cmd.Context(), out, supplied)
		},
	}

	f := cmd.Flags()
	f.StringVar(&token, "token", "",
		"PAT to save (bypasses the interactive prompt; CI-friendly). Still verified before write.")
	f.BoolVar(&remove, "remove", false,
		"Delete the saved token at ~/.yottacode/github.json. No-op if the file doesn't exist.")

	return cmd
}

// promptForToken is the interactive input path: prints
// guidance + the URL, reads the token via term.ReadPassword
// (hides keystrokes), trims whitespace. Returns the typed
// token or an error from the input layer.
//
// stdin is treated as a *os.File when possible so we can pass
// its fd to term.ReadPassword. Non-tty inputs (piped tests)
// fall through to a line-buffered read so the command stays
// usable in scripts.
func promptForToken(out io.Writer, in io.Reader) (string, error) {
	fmt.Fprintln(out, `Set up a GitHub Personal Access Token (PAT).

Step 1: Open this URL in your browser to create one:
  https://github.com/settings/tokens/new?scopes=repo&description=yottacode

Recommended:
  - Scope: repo (full control of repositories you push to)
  - Expiration: 90 days — re-run this command to refresh

Step 2: Paste the token below (input hidden).`)
	fmt.Fprint(out, "Token: ")

	// Prefer term.ReadPassword when stdin is a real terminal —
	// hides the token from the screen and from shell history.
	// Falls through to a buffered read otherwise (CI/scripts
	// piping the token via stdin).
	if f, ok := in.(*os.File); ok {
		if term.IsTerminal(int(f.Fd())) {
			line, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(out) // newline after the hidden input
			if err != nil {
				return "", fmt.Errorf("read token: %w", err)
			}
			return strings.TrimSpace(string(line)), nil
		}
	}

	// Non-tty fallback: read one line up to the first newline.
	// Stays usable when the caller pipes the token via stdin
	// (`echo $TOKEN | yottacode setup github`). Note that
	// piping defeats the input-hiding — that's the caller's
	// problem, not ours.
	var b []byte
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b = append(b, buf[0])
		}
		if err != nil {
			break
		}
	}
	return strings.TrimSpace(string(b)), nil
}

// runSetupGitHubSave verifies + persists, then prints the
// human-readable success block. Pulled out of RunE so tests
// can drive the persistence path without going through cobra
// flag parsing.
func runSetupGitHubSave(ctx context.Context, out io.Writer, token string) error {
	fmt.Fprintln(out, "\nVerifying token...")
	path, login, err := ghpkg.SaveVerifiedToken(ctx, token)
	if err != nil {
		return fmt.Errorf("setup github: %w", err)
	}
	fmt.Fprintf(out, "  Authenticated as @%s\n", login)
	fmt.Fprintf(out, "  Saved to %s (mode 0600)\n\n", path)
	fmt.Fprintln(out, "yottacode now uses this token for GitHub API calls.")
	fmt.Fprintln(out, "Revoke any time at https://github.com/settings/tokens.")
	return nil
}

// runSetupGitHubRemove implements --remove. Idempotent: missing
// file is reported as "Already empty" rather than an error.
func runSetupGitHubRemove(out io.Writer) error {
	path, existed, err := ghpkg.RemoveTokenFile()
	if err != nil {
		return fmt.Errorf("setup github --remove: %w", err)
	}
	if !existed {
		fmt.Fprintf(out, "No saved token at %s — nothing to remove.\n", path)
		return nil
	}
	fmt.Fprintf(out, "Removed saved token at %s.\n", path)
	return nil
}

