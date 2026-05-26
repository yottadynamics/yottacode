package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/config"
)

func newMcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP servers (list, add, remove)",
		Long: `MCP server entries live in ~/.yottacode/config.toml as [[mcp_servers]] blocks.
Inside the TUI, /mcp opens the same operations as an interactive picker.`,
	}
	cmd.AddCommand(
		newMcpListCmd(),
		newMcpAddCmd(),
		newMcpRemoveCmd(),
	)
	return cmd
}

func newMcpListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			if jsonOut {
				out := struct {
					MCPServers []config.MCPServer `json:"mcp_servers"`
				}{MCPServers: cfg.MCPServers}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			if len(cfg.MCPServers) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no MCP servers configured — try `yottacode mcp add`")
				return nil
			}
			for _, s := range cfg.MCPServers {
				status := ""
				if s.Disabled {
					status = " (disabled)"
				}
				argsStr := ""
				if len(s.Args) > 0 {
					argsStr = " " + strings.Join(s.Args, " ")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s%s%s\n",
					s.Name, s.Command, argsStr, status)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON for scripting")
	return cmd
}

func newMcpAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add NAME CMD [ARGS...]",
		Short: "Add a new MCP server to config.toml",
		Long: `Adds a [[mcp_servers]] entry. The first argument is the server name,
the second is the executable, and everything after is passed as args.
The server will start automatically on the next yottacode session.

Examples:
  yottacode mcp add podman npx -y podman-mcp-server@latest
  yottacode mcp add filesystem npx -y @modelcontextprotocol/server-filesystem /workspace
  yottacode mcp add excalidraw node /path/to/dist/index.js --stdio`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !config.MCPNameValid(name) {
				return fmt.Errorf("invalid name %q (must be lowercase letters, digits, hyphens, underscores; start with a letter)", name)
			}

			cmdTokens := args[1:]

			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			for _, s := range cfg.MCPServers {
				if s.Name == name {
					return fmt.Errorf("MCP server %q already exists; remove it first with `yottacode mcp remove %s`", name, name)
				}
			}

			server := config.MCPServer{
				Name:    name,
				Command: cmdTokens[0],
				Args:    cmdTokens[1:],
			}
			cfg.MCPServers = append(cfg.MCPServers, server)

			if err := config.Validate(cfg); err != nil {
				return fmt.Errorf("config invalid after add: %w", err)
			}
			if err := config.Save(cfg, ""); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "added MCP server %q to config.toml\n", name)
			fmt.Fprintf(cmd.OutOrStdout(), "  command: %s\n", cmdTokens[0])
			if len(cmdTokens) > 1 {
				fmt.Fprintf(cmd.OutOrStdout(), "  args:    %s\n", strings.Join(cmdTokens[1:], " "))
			}
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func newMcpRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm"},
		Short:   "Remove an MCP server from config.toml",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}

			found := -1
			for i, s := range cfg.MCPServers {
				if s.Name == name {
					found = i
					break
				}
			}
			if found < 0 {
				return fmt.Errorf("no MCP server named %q in config.toml", name)
			}

			cfg.MCPServers = append(cfg.MCPServers[:found], cfg.MCPServers[found+1:]...)
			if err := config.Save(cfg, ""); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "removed MCP server %q from config.toml\n", name)
			return nil
		},
	}
}
