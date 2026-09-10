package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

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
		newMcpAuthCmd(),
		newMcpLogoutCmd(),
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
  --transport            ""  (stdio, default) | "http" | "sse"
  --url                  Server URL, required for --transport http/sse
  --header               HTTP header as KEY=VALUE (repeatable, http/sse only)
  --env                  Environment variable as KEY=VALUE (repeatable, stdio only)
  --disabled             Add the entry without starting it
  --auth                 "" (default) | "static-header" | "oauth" — http/sse only
  --oauth-client-id      OAuth client ID pre-registered with the server's
                         authorization server; required for --auth oauth
  --oauth-client-secret  Paired client secret, when the authorization
                         server issued one (optional for a public client)
  --oauth-scope          OAuth scope to request (repeatable); omit to
                         request the server's full advertised scope set

Examples:
  yottacode mcp add podman npx -y podman-mcp-server@latest
  yottacode mcp add filesystem npx -y @modelcontextprotocol/server-filesystem /workspace
  yottacode mcp add excalidraw node /path/to/dist/index.js --stdio
  yottacode mcp add linear --transport http --url https://mcp.linear.app/mcp \
      --header "Authorization=Bearer $LINEAR_API_KEY"
  yottacode mcp add gmail --transport http --url https://gmailmcp.googleapis.com/mcp/v1 \
      --auth oauth --oauth-client-id "$GOOGLE_MCP_CLIENT_ID" \
      --oauth-client-secret "$GOOGLE_MCP_CLIENT_SECRET" \
      --oauth-scope https://www.googleapis.com/auth/gmail.readonly
      # then: yottacode mcp auth gmail`,
		DisableFlagParsing: true, // see parseAddArgs — flags may follow NAME, which pflag's
		// interspersed=false mode (needed so raw CMD args like "-y" pass through
		// untouched) would otherwise swallow as positional args too.
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, rawArgs []string) error {
			if len(rawArgs) > 0 && (rawArgs[0] == "-h" || rawArgs[0] == "--help") {
				return cmd.Help()
			}
			a, err := parseAddArgs(rawArgs)
			if err != nil {
				return err
			}
			if !config.MCPNameValid(a.name) {
				return fmt.Errorf("invalid name %q (must be lowercase letters, digits, hyphens, underscores; start with a letter)", a.name)
			}
			if len(a.cmdTokens) > 0 && a.url != "" {
				return fmt.Errorf("cannot combine a command with --url; pick one transport shape")
			}
			for _, tok := range stragglerFlagsIn(a.cmdTokens) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: %q looks like an `mcp add` flag but appeared after the command — it was passed to the command verbatim instead. Move it before CMD if that's not what you wanted.\n", tok)
			}

			headerMap, err := parseKeyValueFlags(a.headers)
			if err != nil {
				return fmt.Errorf("--header: %w", err)
			}
			envMap, err := parseKeyValueFlags(a.envFlags)
			if err != nil {
				return fmt.Errorf("--env: %w", err)
			}

			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			for _, s := range cfg.MCPServers {
				if s.Name == a.name {
					return fmt.Errorf("MCP server %q already exists; remove it first with `yottacode mcp remove %s`", a.name, a.name)
				}
			}

			server := config.MCPServer{
				Name:              a.name,
				Transport:         a.transport,
				URL:               a.url,
				Headers:           headerMap,
				Env:               envMap,
				Disabled:          a.disabled,
				Auth:              a.auth,
				OAuthClientID:     a.oauthClientID,
				OAuthClientSecret: a.oauthClientSecret,
				OAuthScopes:       a.oauthScopes,
			}
			if len(a.cmdTokens) > 0 {
				server.Command = a.cmdTokens[0]
				server.Args = a.cmdTokens[1:]
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

			fmt.Fprintf(cmd.OutOrStdout(), "added MCP server %q to config.toml\n", a.name)
			switch {
			case server.URL != "":
				fmt.Fprintf(cmd.OutOrStdout(), "  transport: %s\n  url:       %s\n", orDefault(server.Transport, "stdio"), server.URL)
			case len(a.cmdTokens) > 0:
				fmt.Fprintf(cmd.OutOrStdout(), "  command: %s\n", a.cmdTokens[0])
				if len(a.cmdTokens) > 1 {
					fmt.Fprintf(cmd.OutOrStdout(), "  args:    %s\n", strings.Join(a.cmdTokens[1:], " "))
				}
			}
			if server.Auth == "oauth" {
				fmt.Fprintf(cmd.OutOrStdout(), "  auth:      oauth — run `yottacode mcp auth %s` to sign in\n", a.name)
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
var mcpAddRecognizedFlags = []string{
	"--transport", "--url", "--header", "--env", "--disabled",
	"--auth", "--oauth-client-id", "--oauth-client-secret", "--oauth-scope",
}

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

// addArgs is the parsed result of `mcp add`'s hand-rolled flag parsing —
// a struct rather than parseAddArgs's original long positional-return
// list, which stopped scaling once auth/oauth_* flags were added.
type addArgs struct {
	name, transport, url                      string
	cmdTokens, headers, envFlags, oauthScopes []string
	disabled                                  bool
	auth, oauthClientID, oauthClientSecret    string
}

// parseAddArgs hand-parses `mcp add`'s raw argument list (flag parsing is
// disabled on the command — see newMcpAddCmd's DisableFlagParsing comment).
// NAME is the first token that isn't a recognized flag; once a second such
// token appears, it and everything after it become cmdTokens verbatim
// (so a stdio executable's own flags, e.g. "-y", are never mistaken for
// `mcp add`'s flags). Every other recognized flag may appear anywhere
// else, in any order; --header/--env/--oauth-scope are repeatable.
func parseAddArgs(args []string) (addArgs, error) {
	var a addArgs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--transport":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--transport requires a value")
			}
			a.transport = args[i]
		case "--url":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--url requires a value")
			}
			a.url = args[i]
		case "--header":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--header requires a KEY=VALUE value")
			}
			a.headers = append(a.headers, args[i])
		case "--env":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--env requires a KEY=VALUE value")
			}
			a.envFlags = append(a.envFlags, args[i])
		case "--disabled":
			a.disabled = true
		case "--auth":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--auth requires a value")
			}
			a.auth = args[i]
		case "--oauth-client-id":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--oauth-client-id requires a value")
			}
			a.oauthClientID = args[i]
		case "--oauth-client-secret":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--oauth-client-secret requires a value")
			}
			a.oauthClientSecret = args[i]
		case "--oauth-scope":
			i++
			if i >= len(args) {
				return addArgs{}, fmt.Errorf("--oauth-scope requires a value")
			}
			a.oauthScopes = append(a.oauthScopes, args[i])
		default:
			if a.name == "" {
				a.name = args[i]
			} else {
				a.cmdTokens = args[i:]
				return a, nil
			}
		}
	}
	return a, nil
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

// newMcpAuthCmd implements `yottacode mcp auth NAME`: runs the OAuth 2.1
// authorization-code + PKCE sign-in for an auth = "oauth" server and
// persists the resulting token to ~/.yottacode/mcp-auth/<name>.json. A
// browser opens (best-effort); the URL is also printed so a headless or
// remote session can complete sign-in by pasting it elsewhere. Blocks
// until sign-in completes, fails, or ctx times out.
func newMcpAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth NAME",
		Short: "Sign in to an auth = \"oauth\" MCP server",
		Long: `Runs the OAuth 2.1 authorization-code + PKCE flow for the named server and
persists the resulting access/refresh token to ~/.yottacode/mcp-auth/<name>.json
(mode 0600, never written to config.toml). A later session reuses the
persisted token automatically — rerun this only to sign in the first time,
switch accounts, or after ` + "`mcp logout`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			var server *config.MCPServer
			for i := range cfg.MCPServers {
				if cfg.MCPServers[i].Name == name {
					server = &cfg.MCPServers[i]
					break
				}
			}
			if server == nil {
				return fmt.Errorf("no MCP server named %q in config.toml", name)
			}
			if server.Auth != "oauth" {
				return fmt.Errorf("MCP server %q has auth = %q, not \"oauth\" — nothing to sign in to", name, orDefault(server.Auth, "none"))
			}
			policy := mcp.Policy{RequireTLS: cfg.MCP.RequireTLS, AllowedHosts: cfg.MCP.AllowedHosts}
			if err := policy.CheckURL(server.URL); err != nil {
				return err
			}

			opts := mcp.OAuthOptions{
				ClientID:     os.Expand(server.OAuthClientID, os.Getenv),
				ClientSecret: os.Expand(server.OAuthClientSecret, os.Getenv),
				Scopes:       server.OAuthScopes,
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			tok, err := mcp.RunOAuthLogin(ctx, name, server.URL, opts, &http.Client{}, func(u string) {
				fmt.Fprintf(cmd.OutOrStdout(), "opening browser to sign in to %q — if it didn't open, paste this URL:\n  %s\n", name, u)
			})
			if err != nil {
				return fmt.Errorf("sign-in failed: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "signed in to %q (token type %s)\n", name, orDefault(tok.TokenType, "Bearer"))
			return nil
		},
	}
	return cmd
}

// newMcpLogoutCmd implements `yottacode mcp logout NAME`: deletes the
// persisted OAuth token, if any. The next tool call against that server
// (or the next `mcp auth NAME`) re-triggers interactive sign-in.
func newMcpLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout NAME",
		Short: "Delete a server's persisted OAuth token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			existed, err := mcp.DeleteToken(name)
			if err != nil {
				return err
			}
			if existed {
				fmt.Fprintf(cmd.OutOrStdout(), "logged out %q\n", name)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%q was already logged out\n", name)
			}
			return nil
		},
	}
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
