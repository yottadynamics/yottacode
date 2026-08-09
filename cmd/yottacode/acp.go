package main

import (
	"context"
	"os"
	"time"

	coderacp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/acp"
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
	cmd := &cobra.Command{
		Use:   "acp",
		Short: "Run yottacode as an Agent Client Protocol server (for editor embedding)",
		Long: `Serves the Agent Client Protocol (ACP) over stdio: a long-lived process
hosting concurrent sessions, for embedding yottacode in editors that speak
ACP (Zed, JetBrains, and others). This is a server, not a one-shot command
— it runs until the client disconnects.

Configuration is process-wide (no built-in defaults — must be set via
flag or env), seeding every session's defaults:
  --model      / $YOTTACODE_MODEL      model tag
  --base-url   / $YOTTACODE_BASE_URL   OpenAI-compatible endpoint
  --api-key    / $YOTTACODE_API_KEY    optional bearer token

Each session's working directory and MCP servers come from the client's
session/new request, not from CLI flags.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.Resolve(opts); err != nil {
				return err
			}
			srv := acp.NewServer(*opts)
			conn := coderacp.NewAgentSideConnection(srv, os.Stdout, os.Stdin)
			srv.SetConnection(conn)
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
	return cmd
}
