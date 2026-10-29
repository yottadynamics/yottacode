package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	coderacp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/acp/daemon"
	"github.com/yottadynamics/yottacode/internal/acp/transport"

	"github.com/yottadynamics/yottacode/internal/acp"
	acpauth "github.com/yottadynamics/yottacode/internal/acp/auth"
	"github.com/yottadynamics/yottacode/internal/cli"
)

// acpShutdownTimeout bounds how long the process waits for every live
// session to cancel and persist on exit — matches the SDK's own
// CommandTransport shutdown ladder in spirit (bounded, not infinite).
const acpShutdownTimeout = 10 * time.Second

// newAcpCmd builds the `yottacode acp` subcommand: a long-lived Agent
// Client Protocol server hosting N concurrent sessions over stdio, for
// embedding yottacode in editors that speak ACP (Zed, JetBrains, …). See
// roadmap/acp-adapter.md.
//
// Unlike `run`/the root TUI command, this RunE deliberately skips
// resolveContinue and ensureWorktree: --resume/--continue and --worktree
// are single-session, process-wide CLI concepts, but acp hosts many
// sessions with independent cwds resolved per session/new call from the
// client — there is no one "the" cwd to apply either to at startup.
// ensureWorktree in particular calls os.Chdir, which would be actively
// unsafe to run once and apply to every subsequent session.
func newAcpCmd(opts *cli.ChatOptions) *cobra.Command {
	var listenAddr string
	var bearerToken string
	var tlsCert string
	var tlsKey string
	var tokenName string
	var tokenScopes []string
	var tokenProjects []string
	var tokenExpires time.Duration
	var tokenCreate bool
	var stdio bool
	var daemonState string
	cmd := &cobra.Command{
		Use:   "acp",
		Short: "Run yottacode as an Agent Client Protocol server (for editor embedding)",
		Long: `Serves the Agent Client Protocol (ACP) over stdio: a long-lived process
hosting concurrent sessions, for embedding yottacode in editors that speak
ACP (Zed, JetBrains, and others). This is a server, not a one-shot command
— it runs until the client disconnects.

Process-wide defaults come from flags, env, then the active provider in
config.toml; they seed every session:
  --model      / $YOTTACODE_MODEL      model tag
  --provider   / $YOTTACODE_PROVIDER   provider profile from config.toml
  --base-url   / $YOTTACODE_BASE_URL   OpenAI-compatible endpoint
  --api-key    / $YOTTACODE_API_KEY    optional bearer token
Pass --model together with --provider when the model belongs to a
non-active provider.

Clients can also change a session's model: the server advertises a "model"
session config option (every model from the configured providers), so hosts
such as Buzz can select one per session (buzz-acp --model <id>). That
override is session-only and never written to config.toml.

Each session's working directory and MCP servers come from the client's
session/new request, not from CLI flags.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if daemonState != "" {
				if err := daemon.WriteState(daemonState, listenAddr); err != nil {
					return err
				}
				defer os.Remove(daemonState)
			}
			if tokenCreate {
				store, err := acpauth.NewStore()
				if err != nil {
					return err
				}
				scopes := make([]acpauth.Scope, 0, len(tokenScopes))
				for _, scope := range tokenScopes {
					scopes = append(scopes, acpauth.Scope(scope))
				}
				record, raw, err := store.Create(tokenName, scopes, tokenProjects, time.Now().Add(tokenExpires))
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "token_id=%s\nname=%s\nscopes=%s\ntoken=%s\n", record.ID, record.Name, strings.Join(tokenScopes, ","), raw)
				return nil
			}
			if !tokenCreate {
				if err := cli.Resolve(opts); err != nil {
					return err
				}
			}
			if tokenCreate {
				return nil
			}
			if !stdio && listenAddr == "" {
				stdio = true // Preserve the existing editor-integration default.
			}
			if stdio && listenAddr != "" {
				return errors.New("acp: --stdio and --listen are mutually exclusive")
			}
			srv := acp.NewServer(*opts)
			if !stdio {
				if bearerToken == "" {
					return errors.New("acp: --token is required for WSS mode; create one with --token-create --token-name <name>")
				}
				if tlsCert == "" || tlsKey == "" {
					return errors.New("acp: --tls-cert and --tls-key are required for WSS mode")
				}
				store, err := acpauth.NewStore()
				if err != nil {
					return err
				}
				handler := &transport.WSSHandler{Server: srv, BearerToken: bearerToken, TokenStore: store}
				server := &http.Server{
					Addr:              listenAddr,
					Handler:           http.HandlerFunc(handler.ServeHTTP),
					ReadHeaderTimeout: 10 * time.Second,
					ReadTimeout:       5 * time.Minute,
					WriteTimeout:      5 * time.Minute,
					IdleTimeout:       2 * time.Minute,
					MaxHeaderBytes:    16 << 10,
				}
				go func() {
					<-cmd.Context().Done()
					_ = server.Close()
				}()
				if err := server.ListenAndServeTLS(tlsCert, tlsKey); err != http.ErrServerClosed {
					return err
				}
				shutdownCtx, cancel := context.WithTimeout(context.Background(), acpShutdownTimeout)
				defer cancel()
				srv.Shutdown(shutdownCtx)
				return nil
			}
			ref := acp.NewConnectionRef()
			conn := coderacp.NewAgentSideConnection(srv.Agent(ref), os.Stdout, os.Stdin)
			ref.Set(conn)
			select {
			case <-cmd.Context().Done():
			case <-conn.Done():
			}
			// Drain every still-registered session (cancel + persist) on
			// exit — a client that disconnects (stdin EOF) or a process
			// signal without individually closing each session must not
			// silently drop unsaved state.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), acpShutdownTimeout)
			defer cancel()
			srv.Shutdown(shutdownCtx)
			return nil
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", "", "WSS listen address (for example 127.0.0.1:8443)")
	cmd.Flags().StringVar(&bearerToken, "token", "", "Bearer token required by WSS clients")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate path for WSS mode")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS private key path for WSS mode")
	cmd.Flags().StringVar(&tokenName, "token-name", "", "Create a named ACP bearer token")
	cmd.Flags().StringSliceVar(&tokenScopes, "token-scope", []string{"read", "prompt", "approve"}, "Scopes for a created token")
	cmd.Flags().DurationVar(&tokenExpires, "token-expires", 30*24*time.Hour, "Lifetime for a created token")
	cmd.Flags().StringSliceVar(&tokenProjects, "token-project", nil, "Project root allowed for a created token; repeatable")
	cmd.Flags().BoolVar(&tokenCreate, "token-create", false, "Create and persist a bearer token, then exit")
	cmd.Flags().BoolVar(&stdio, "stdio", false, "Use stdio ACP transport instead of WSS")
	cmd.Flags().StringVar(&daemonState, "daemon-state", "", "Write daemon PID/address state to this file")
	cmd.AddCommand(newAcpAttachCmd(), newAcpTokenCmd(), newAcpDaemonCmd(opts))
	return cmd
}

func newAcpTokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage ACP bearer tokens", Args: cobra.NoArgs}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List ACP tokens", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				store, err := acpauth.NewStore()
				if err != nil {
					return err
				}
				records, err := store.List()
				if err != nil {
					return err
				}
				for _, record := range records {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%v\t%v\n", record.ID, record.Name, strings.Join(scopeStrings(record.Scopes), ","), record.ExpiresAt, record.RevokedAt)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "revoke <token-id>", Short: "Revoke an ACP token", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				store, err := acpauth.NewStore()
				if err != nil {
					return err
				}
				return store.Revoke(args[0], time.Now())
			},
		},
		&cobra.Command{
			Use: "rotate <token-id>", Short: "Rotate an ACP token", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				store, err := acpauth.NewStore()
				if err != nil {
					return err
				}
				record, raw, err := store.Rotate(args[0], time.Now())
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "token_id=%s\nname=%s\nscopes=%s\ntoken=%s\n", record.ID, record.Name, strings.Join(scopeStrings(record.Scopes), ","), raw)
				return nil
			},
		},
	)
	return cmd
}

func scopeStrings(scopes []acpauth.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, string(scope))
	}
	return out
}
