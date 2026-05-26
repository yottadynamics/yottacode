package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
)

var errCopilotNotLoggedIn = errors.New("copilot: not logged in")

func newCopilotAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copilot-auth",
		Short: "Manage GitHub Copilot OAuth credentials",
		Long: `copilot-auth manages the GitHub OAuth token used for the copilot
provider — GitHub's device code flow.

Model calls bill against the user's GitHub Copilot subscription.
Tokens persist at ~/.yottacode/auth/copilot.json (mode 0600).

  login   device code flow → save GitHub token
  logout  delete the token store
  status  show login state
  models  list models available on your subscription`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newCopilotAuthLoginCmd(),
		newCopilotAuthLogoutCmd(),
		newCopilotAuthStatusCmd(),
		newCopilotAuthModelsCmd(),
	)
	return cmd
}

func newCopilotAuthLoginCmd() *cobra.Command {
	var storePath string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with GitHub (device code flow) and save token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveCopilotStorePath(storePath)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			token, err := copilotauth.Login(ctx, copilotauth.LoginOptions{
				OnDeviceCode: func(dc copilotauth.DeviceCode) {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"open %s in your browser and enter code: %s\n",
						dc.VerificationURI, dc.UserCode)
				},
			})
			if err != nil {
				return fmt.Errorf("login: %w", err)
			}

			ts := copilotauth.TokenSet{GitHubToken: token}
			if err := copilotauth.Save(path, ts); err != nil {
				return fmt.Errorf("save token: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "logged in successfully\nsaved to %s\n", path)

			fmt.Fprintf(cmd.ErrOrStderr(), "verifying Copilot access...\n")
			ct, err := copilotauth.FetchCopilotToken(ctx, nil, token)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not verify Copilot subscription: %v\n", err)
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Copilot access verified (endpoint: %s)\n", ct.Endpoints.API)

			fmt.Fprintf(cmd.ErrOrStderr(), "caching available models...\n")
			if _, err := copilotauth.FetchAndCacheModels(ctx, ct); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not cache models: %v\n", err)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "model cache updated\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "Override token store path (default: ~/.yottacode/auth/copilot.json)")
	return cmd
}

func newCopilotAuthLogoutCmd() *cobra.Command {
	var storePath string
	var yes bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Delete saved copilot tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveCopilotStorePath(storePath)
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
	cmd.Flags().StringVar(&storePath, "store", "", "Override token store path (default: ~/.yottacode/auth/copilot.json)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

type copilotStatusReport struct {
	LoggedIn  bool   `json:"logged_in"`
	Valid     bool   `json:"valid"`
	StorePath string `json:"store_path"`
}

func newCopilotAuthStatusCmd() *cobra.Command {
	var storePath string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Show copilot login state",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveCopilotStorePath(storePath)
			if err != nil {
				return err
			}
			rep := copilotStatusReport{StorePath: path}

			ts, err := copilotauth.Load(path)
			if err != nil {
				if errors.Is(err, copilotauth.ErrNotFound) {
					return emitCopilotStatus(cmd, rep, jsonOutput)
				}
				return fmt.Errorf("load token store: %w", err)
			}
			rep.LoggedIn = ts.GitHubToken != ""
			rep.Valid = rep.LoggedIn
			return emitCopilotStatus(cmd, rep, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "Override token store path (default: ~/.yottacode/auth/copilot.json)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit status as JSON")
	return cmd
}

func emitCopilotStatus(cmd *cobra.Command, rep copilotStatusReport, jsonOutput bool) error {
	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		out := cmd.OutOrStdout()
		if !rep.LoggedIn {
			fmt.Fprintf(out, "not logged in (no token at %s)\n", rep.StorePath)
		} else {
			fmt.Fprintf(out, "logged in\n  store: %s\n", rep.StorePath)
		}
	}
	if !rep.LoggedIn {
		return errCopilotNotLoggedIn
	}
	return nil
}

func newCopilotAuthModelsCmd() *cobra.Command {
	var storePath string
	var jsonOutput bool
	var rawOutput bool
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List models available on your Copilot subscription",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveCopilotStorePath(storePath)
			if err != nil {
				return err
			}
			ts, err := copilotauth.Load(path)
			if err != nil {
				if errors.Is(err, copilotauth.ErrNotFound) {
					return fmt.Errorf("not logged in — run `yottacode copilot-auth login` first")
				}
				return fmt.Errorf("load token store: %w", err)
			}
			if ts.GitHubToken == "" {
				return fmt.Errorf("token store has no GitHub token — run `yottacode copilot-auth login`")
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			ct, err := copilotauth.FetchCopilotToken(ctx, nil, ts.GitHubToken)
			if err != nil {
				return fmt.Errorf("get Copilot token: %w", err)
			}

			if rawOutput {
				return fetchCopilotModelsRaw(ctx, ct, cmd.OutOrStdout())
			}

			detailed, err := copilotauth.FetchAndCacheModels(ctx, ct)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(detailed)
			}
			if len(detailed) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no models returned")
				return nil
			}
			for _, m := range detailed {
				label := m.ID
				if m.Disabled {
					label += "  [upgrade plan]"
				}
				fmt.Fprintln(cmd.OutOrStdout(), label)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "Override token store path")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit model list as JSON")
	cmd.Flags().BoolVar(&rawOutput, "raw", false, "Dump the full API response (for inspecting metadata)")
	return cmd
}

func fetchCopilotModelsRaw(ctx context.Context, ct copilotauth.CopilotToken, w io.Writer) error {
	endpoint := ct.Endpoints.API
	if endpoint == "" {
		endpoint = copilotauth.CopilotAPIEndpoint
	}
	url := strings.TrimRight(endpoint, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ct.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Editor-Version", "vscode/1.95.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.250.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var pretty json.RawMessage
	if json.Unmarshal(raw, &pretty) == nil {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(pretty)
	}
	_, err = w.Write(raw)
	return err
}


func resolveCopilotStorePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return copilotauth.DefaultStorePath()
}
