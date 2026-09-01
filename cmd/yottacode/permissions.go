package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/permissions"
)

// newPermissionsCmd builds the `yottacode permissions` subcommand tree.
//
// Permission rules live project-locally in .yottacode/permissions.json and
// .yottacode/permissions.local.json (see internal/permissions and
// docs/security-and-allow-lists.md). The matching semantics — per-segment
// Bash splitting, doublestar path globs vs. free-form string globs,
// ratcheted multi-target evaluation for batch calls — are non-obvious
// enough that a hand-written rule is easy to get subtly wrong. `test` runs
// a hypothetical tool call through the exact same Evaluate the agent loop
// uses, so a rule can be checked before it's trusted.
func newPermissionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "permissions",
		Short: "Inspect and test project permission rules",
		Long: `Permission rules live in .yottacode/permissions.json (team-shared) and
.yottacode/permissions.local.json (personal). See docs/security-and-allow-lists.md
for rule syntax.

  test      dry-run a hypothetical tool call against the loaded rules`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newPermissionsTestCmd())
	return cmd
}

func newPermissionsTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test <tool> [args-json]",
		Short: "Show the permission verdict for a hypothetical tool call",
		Long: `Loads .yottacode/permissions.json and permissions.local.json from the
current directory and evaluates a hypothetical call against them —
without executing anything — using the same Evaluate the agent loop
calls on every real tool call.

<tool> is the internal tool name (run_bash, write_file, edit_file, git,
read_file, fetch_url, ...; see docs/tools.md). [args-json] is that
tool's JSON argument object, e.g. '{"command":"git push origin main"}'.

As a shortcut, "bash" is accepted as an alias for run_bash, and its
argument may be a bare command string instead of JSON:

  yottacode permissions test bash "git push origin main --force"
  yottacode permissions test run_bash '{"command":"go test ./..."}'
  yottacode permissions test edit_file '{"path":"internal/foo.go"}'
  yottacode permissions test write_file '{"path":".env"}'`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			perms, err := permissions.Load(cwd)
			if err != nil {
				return err
			}
			tool, argsJSON, err := resolvePermissionsTestCall(args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !permissions.SupportsToolName(tool) {
				fmt.Fprintf(out, "tool:    %s\n", tool)
				fmt.Fprintln(out, "verdict: n/a — this tool name isn't subject to permission-file matching")
				return nil
			}
			verdict, rule := perms.EvaluateWithRule(tool, argsJSON)
			fmt.Fprintf(out, "tool:    %s\n", tool)
			fmt.Fprintf(out, "args:    %s\n", argsJSON)
			fmt.Fprintf(out, "verdict: %s\n", verdict)
			if rule.Tool != "" {
				fmt.Fprintf(out, "matched: %s(%s)  [%s]\n", rule.Tool, rule.Pattern, rule.Source)
			} else {
				fmt.Fprintf(out, "matched: (none) — falls back to %s's own approval policy\n", tool)
			}
			return nil
		},
	}
	return cmd
}

// resolvePermissionsTestCall turns the CLI's <tool> [args-json] pair into
// the (toolName, argsJSON) shape permissions.EvaluateWithRule expects.
// "bash" is accepted as a friendlier alias for the internal "run_bash"
// name, and — for that tool only — a second argument that fails to parse
// as JSON is treated as a bare command string and wrapped as
// {"command": "..."}, so testing the single most common rule type
// (Bash(...)) never requires hand-quoting JSON.
func resolvePermissionsTestCall(args []string) (tool, argsJSON string, err error) {
	tool = args[0]
	if tool == "bash" {
		tool = "run_bash"
	}
	if len(args) < 2 {
		return tool, "{}", nil
	}
	raw := args[1]
	if tool == "run_bash" && !json.Valid([]byte(raw)) {
		wrapped, err := json.Marshal(map[string]string{"command": raw})
		if err != nil {
			return "", "", err
		}
		return tool, string(wrapped), nil
	}
	if !json.Valid([]byte(raw)) {
		return "", "", fmt.Errorf("args-json is not valid JSON: %s", raw)
	}
	return tool, raw, nil
}
