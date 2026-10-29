package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/acp/daemon"
	"github.com/yottadynamics/yottacode/internal/cli"
)

func newAcpDaemonCmd(opts *cli.ChatOptions) *cobra.Command {
	var statePath string
	cmd := &cobra.Command{Use: "daemon", Short: "Manage the ACP WSS daemon", Args: cobra.NoArgs}
	cmd.PersistentFlags().StringVar(&statePath, "state", "~/.yottacode/acp-daemon.state", "daemon state file")
	cmd.AddCommand(
		&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			path := expandHome(statePath)
			state, err := daemon.ReadState(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pid=%d\naddress=%s\nrunning=%t\n", state.PID, state.Address, daemon.Alive(state.PID))
			return nil
		}},
		&cobra.Command{Use: "stop", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return daemon.Stop(expandHome(statePath))
		}},
		&cobra.Command{Use: "clean-stale", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return daemon.RemoveStale(expandHome(statePath))
		}},
	)
	return cmd
}

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}
