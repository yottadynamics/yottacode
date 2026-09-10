package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
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
		Use:   "add NAME [CMD [ARGS...]] [--transport http|sse --url URL] [--header K=V]... [--env K=V]... [--disabled]",
		Short: "Add a new MCP server to config.toml",
		Long: `Adds a [[mcp_servers]] entry. For a stdio server, the first argument is
the server name, the second is the executable, and everything after is
passed as args. For a remote server, omit CMD/ARGS and pass --transport
http or --transport sse with --url instead. The server will start
automatically on the next yottacode session.

Flags (--command-style args and these may appear in any order after NAME,
except that once CMD begins every following token is passed to it verbatim):
  --transport   ""  (stdio, default) | "http" | "sse"
  --url         Server URL, required for --transport http/sse
  --header      HTTP header as KEY=VALUE (repeatable, http/sse only)
  --env         Environment variable as KEY=VALUE (repeatable, stdio only)
  --disabled    Add the entry without starting it

Examples:
  yottacode mcp add podman npx -y podman-mcp-server@latest
  yottacode mcp add filesystem npx -y @modelcontextprotocol/server-filesystem /workspace
  yottacode mcp add excalidraw node /path/to/dist/index.js --stdio
  yottacode mcp add linear --transport http --url https://mcp.linear.app/mcp \
      --header "Authorization=Bearer $LINEAR_API_KEY"`,
		DisableFlagParsing: true, // see parseAddArgs — flags may follow NAME, which pflag's
		// interspersed=false mode (needed so raw CMD args like "-y" pass through
		// untouched) would otherwise swallow as positional args too.
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, rawArgs []string) error {
			if len(rawArgs) > 0 && (rawArgs[0] == "-h" || rawArgs[0] == "--help") {
				return cmd.Help()
			}
			name, transport, url, cmdTokens, headers, envFlags, disabled, err := parseAddArgs(rawArgs)
			if err != nil {
				return err
			}
			if !config.MCPNameValid(name) {
				return fmt.Errorf("invalid name %q (must be lowercase letters, digits, hyphens, underscores; start with a letter)", name)
			}
			if len(cmdTokens) > 0 && url != "" {
				return fmt.Errorf("cannot combine a command with --url; pick one transport shape")
			}
			for _, tok := range stragglerFlagsIn(cmdTokens) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: %q looks like an `mcp add` flag but appeared after the command — it was passed to the command verbatim instead. Move it before CMD if that's not what you wanted.\n", tok)
			}

			headerMap, err := parseKeyValueFlags(headers)
			if err != nil {
				return fmt.Errorf("--header: %w", err)
			}
			envMap, err := parseKeyValueFlags(envFlags)
			if err != nil {
				return fmt.Errorf("--env: %w", err)
			}

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
				Name:      name,
				Transport: transport,
				URL:       url,
				Headers:   headerMap,
				Env:       envMap,
				Disabled:  disabled,
			}
			if len(cmdTokens) > 0 {
				server.Command = cmdTokens[0]
				server.Args = cmdTokens[1:]
			}

			if server.Transport == "http" || server.Transport == "sse" {
				policy := mcp.Policy{RequireTLS: cfg.MCP.RequireTLS, AllowedHosts: cfg.MCP.AllowedHosts}
				if err := policy.CheckURL(server.URL); err != nil {
					return err
				}
			}

			cfg.MCPServers = append(cfg.MCPServers, server)
			if err := config.Validate(cfg); err != nil {
				return fmt.Errorf("config invalid after add: %w", err)
			}
			if err := config.Save(cfg, ""); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "added MCP server %q to config.toml\n", name)
			switch {
			case server.URL != "":
				fmt.Fprintf(cmd.OutOrStdout(), "  transport: %s\n  url:       %s\n", orDefault(server.Transport, "stdio"), server.URL)
			case len(cmdTokens) > 0:
				fmt.Fprintf(cmd.OutOrStdout(), "  command: %s\n", cmdTokens[0])
				if len(cmdTokens) > 1 {
					fmt.Fprintf(cmd.OutOrStdout(), "  args:    %s\n", strings.Join(cmdTokens[1:], " "))
				}
			}
			return nil
		},
	}
	return cmd
}

// mcpAddRecognizedFlags is `mcp add`'s own flag vocabulary — used by
// stragglerFlagsIn to catch the ordering footgun where one of these,
// typed after the command starts, silently becomes a literal argument to
// that command instead of being parsed as a flag (see parseAddArgs).
var mcpAddRecognizedFlags = []string{"--transport", "--url", "--header", "--env", "--disabled"}

// stragglerFlagsIn scans a command's captured argument tokens for
// anything that looks like one of mcp add's own flags. It can't tell
// whether the user meant "pass --disabled to the command" or "I put
// --disabled in the wrong place" — so it returns candidates for a
// warning rather than blocking the add, since the former is a legitimate
// (if rare) thing a real executable's own flag could be.
func stragglerFlagsIn(cmdTokens []string) []string {
	var found []string
	for _, tok := range cmdTokens {
		for _, flag := range mcpAddRecognizedFlags {
			if tok == flag {
				found = append(found, tok)
				break
			}
		}
	}
	return found
}

// parseAddArgs hand-parses `mcp add`'s raw argument list (flag parsing is
// disabled on the command — see newMcpAddCmd's DisableFlagParsing comment).
// NAME is the first token that isn't a recognized flag; once a second such
// token appears, it and everything after it become cmdTokens verbatim
// (so a stdio executable's own flags, e.g. "-y", are never mistaken for
// `mcp add`'s flags). --transport/--url/--header/--env/--disabled may
// appear anywhere else, in any order, and --header/--env are repeatable.
func parseAddArgs(args []string) (name, transport, url string, cmdTokens, headers, envFlags []string, disabled bool, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--transport":
			i++
			if i >= len(args) {
				return "", "", "", nil, nil, nil, false, fmt.Errorf("--transport requires a value")
			}
			transport = args[i]
		case "--url":
			i++
			if i >= len(args) {
				return "", "", "", nil, nil, nil, false, fmt.Errorf("--url requires a value")
			}
			url = args[i]
		case "--header":
			i++
			if i >= len(args) {
				return "", "", "", nil, nil, nil, false, fmt.Errorf("--header requires a KEY=VALUE value")
			}
			headers = append(headers, args[i])
		case "--env":
			i++
			if i >= len(args) {
				return "", "", "", nil, nil, nil, false, fmt.Errorf("--env requires a KEY=VALUE value")
			}
			envFlags = append(envFlags, args[i])
		case "--disabled":
			disabled = true
		default:
			if name == "" {
				name = args[i]
			} else {
				cmdTokens = args[i:]
				return name, transport, url, cmdTokens, headers, envFlags, disabled, nil
			}
		}
	}
	return name, transport, url, cmdTokens, headers, envFlags, disabled, nil
}

// parseKeyValueFlags parses repeated "KEY=VALUE" flag values into a map.
// Returns nil (not an empty map) when kvs is empty, so callers can leave
// config.MCPServer.Headers/Env unset rather than an empty non-nil map.
func parseKeyValueFlags(kvs []string) (map[string]string, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("expected KEY=VALUE, got %q", kv)
		}
		out[k] = v
	}
	return out, nil
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
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
