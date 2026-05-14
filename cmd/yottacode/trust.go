package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/trust"
)

// newTrustCmd builds the `yottacode trust` subcommand tree.
//
// Trust is user-scope: every entry lives in
// ~/.yottacode/trusted-roots.json. Trusted directories satisfy the
// first-launch trust gate (see yottacode-roadmap/folder-trust.md),
// and subfolders of any trusted root inherit trust automatically.
//
// Non-interactive: each subcommand prints to stdout and exits;
// failures go to stderr via cobra's default plumbing.
func newTrustCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage trusted workspace roots",
		Long: `Trust controls which directories yottacode will open without prompting.

  list      print every trusted root with its grant timestamp
  add       add a directory (default: cwd)
  remove    remove one entry — next launch in that directory prompts again
  clear     remove every entry`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newTrustListCmd(),
		newTrustAddCmd(),
		newTrustRemoveCmd(),
		newTrustClearCmd(),
	)
	return cmd
}

func newTrustListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List trusted workspace roots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := loadTrustStore()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(store.Roots) == 0 {
				fmt.Fprintln(out, "(no trusted roots)")
				return nil
			}
			roots := append([]trust.Root(nil), store.Roots...)
			sort.Slice(roots, func(i, j int) bool { return roots[i].Path < roots[j].Path })
			for _, r := range roots {
				fmt.Fprintf(out, "%s\t%s\n", r.TrustedAt.Format("2006-01-02 15:04:05Z"), r.Path)
			}
			return nil
		},
	}
}

func newTrustAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add [path]",
		Short: "Add a directory to the trust store",
		Long: `Add records a directory as trusted. With no argument, the
current working directory is used. The new entry covers every subfolder
via path inheritance, so adding a parent directory implicitly trusts
every project below it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, storePath, err := loadTrustStore()
			if err != nil {
				return err
			}
			path, err := resolveTrustPath(args)
			if err != nil {
				return err
			}
			added, err := store.Add(path)
			if err != nil {
				return err
			}
			if !added {
				fmt.Fprintf(cmd.OutOrStdout(), "already trusted: %s\n", path)
				return nil
			}
			if err := trust.Save(storePath, store); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "trusted: %s\n", path)
			return nil
		},
	}
}

func newTrustRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <path>",
		Short: "Remove a directory from the trust store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, storePath, err := loadTrustStore()
			if err != nil {
				return err
			}
			path, err := resolveTrustPath(args)
			if err != nil {
				return err
			}
			removed, err := store.Remove(path)
			if err != nil {
				return err
			}
			if !removed {
				return fmt.Errorf("not in trust store: %s", path)
			}
			if err := trust.Save(storePath, store); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", path)
			return nil
		},
	}
}

func newTrustClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove every entry from the trust store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, storePath, err := loadTrustStore()
			if err != nil {
				return err
			}
			n := len(store.Roots)
			store.Clear()
			if err := trust.Save(storePath, store); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cleared %d trusted root(s)\n", n)
			return nil
		},
	}
}

// loadTrustStore is the shared Load helper for every trust
// subcommand. Returns the store, the path it was loaded from
// (for a subsequent Save), and any error.
func loadTrustStore() (*trust.Store, string, error) {
	path, err := trust.DefaultStorePath()
	if err != nil {
		return nil, "", err
	}
	store, err := trust.Load(path)
	if err != nil {
		return nil, "", err
	}
	return store, path, nil
}

// resolveTrustPath returns the absolute path the user is operating
// on. Empty args → cwd. One arg → that arg.
func resolveTrustPath(args []string) (string, error) {
	if len(args) == 0 {
		return os.Getwd()
	}
	return args[0], nil
}
