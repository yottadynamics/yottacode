package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// newMemoryCmd builds the `yottacode memory` subcommand tree.
func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Inspect or prune saved agent memories",
		Long: `Memory wraps the picker actions exposed by the in-TUI /memory picker
into non-interactive cobra subcommands.

  list     list saved memories (defaults to project scope)
  forget   delete a saved memory by name`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newMemoryListCmd(),
		newMemoryForgetCmd(),
	)
	return cmd
}

// newMemoryListCmd prints saved memories for the chosen scope.
func newMemoryListCmd() *cobra.Command {
	var scope string
	c := &cobra.Command{
		Use:   "list",
		Short: "List saved agent memories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			loaded, err := memory.Load(cwd)
			if err != nil {
				return err
			}
			var entries []memory.MemoryEntry
			switch scope {
			case "user":
				entries = loaded.UserMemories
			case "project":
				entries = loaded.ProjectMemories
			default:
				return fmt.Errorf("unknown scope %q (want \"user\" or \"project\")", scope)
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 {
				fmt.Fprintln(out, "(no memories)")
				return nil
			}
			for _, e := range entries {
				fmt.Fprintf(out, "%s\t%s\t%s\n", e.Name, e.Type, e.Path)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "project", "memory scope (\"user\" or \"project\")")
	return c
}

// newMemoryForgetCmd deletes a single memory by name.
func newMemoryForgetCmd() *cobra.Command {
	var scope string
	c := &cobra.Command{
		Use:   "forget <name>",
		Short: "Delete a saved memory by name",
		Long: `Forget removes the memory file at
~/.yottacode/memory/<name>.md (user scope) or
~/.yottacode/projects/<project_slug>/memory/<name>.md (project scope).
The MEMORY.md index for the chosen scope is regenerated.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			name := args[0]
			path, err := memory.MemoryFilePath(scope, name, cwd)
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("no %s memory named %q", scope, name)
				}
				return err
			}
			if err := memory.RegenerateMemoryIndex(scope, cwd); err != nil {
				return fmt.Errorf("removed %s but failed to refresh index: %w", name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "forgot %s memory %s\n", scope, name)
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "project", "memory scope (\"user\" or \"project\")")
	return c
}
