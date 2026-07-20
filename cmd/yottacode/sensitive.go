package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/sensitive"
)

// newSensitiveCmd builds the `yottacode sensitive` subcommand tree.
//
// Sensitivity is user-scope: every entry lives in
// ~/.yottacode/sensitive-roots.json. Marking a project stops automatic session
// recall from carrying its conversations over the network in either direction
// (see internal/sensitive and yottacode-roadmap/memory-auto-recall.md fork 3).
// Subfolders of a marked root inherit the marking.
func newSensitiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sensitive",
		Short: "Manage projects excluded from automatic session recall",
		Long: `Sensitive projects are excluded from automatic session recall in both
directions: nothing is auto-injected into their prompts, and their
conversations never surface in any other project's recall — whatever
retrieval.session_recall.scope is set to.

Intended for PHI/medical and similarly regulated repositories. Sessions
are still indexed and the manual session_recall tool still reaches them;
the gate is about what leaves automatically.

  list      print every sensitive root with its marking timestamp
  add       mark a directory (default: cwd)
  remove    unmark one entry — recall resumes for it on the next launch
  clear     remove every entry`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newSensitiveListCmd(),
		newSensitiveAddCmd(),
		newSensitiveRemoveCmd(),
		newSensitiveClearCmd(),
	)
	return cmd
}

func newSensitiveListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List projects excluded from automatic session recall",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := loadSensitiveStore()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(store.Roots) == 0 {
				fmt.Fprintln(out, "(no sensitive projects)")
				return nil
			}
			roots := append([]sensitive.Root(nil), store.Roots...)
			sort.Slice(roots, func(i, j int) bool { return roots[i].Path < roots[j].Path })
			for _, r := range roots {
				fmt.Fprintf(out, "%s\t%s\n", r.MarkedAt.Format("2006-01-02 15:04:05Z"), r.Path)
			}
			return nil
		},
	}
}

func newSensitiveAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add [path]",
		Short: "Mark a directory as sensitive",
		Long: `Add marks a directory as sensitive. With no argument, the current
working directory is used. The entry covers every subfolder, so marking a
repository root is enough regardless of which subdirectory a session was
started in.

Takes effect on the next launch: the posture is resolved once at startup.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, storePath, err := loadSensitiveStore()
			if err != nil {
				return err
			}
			path, err := resolveSensitivePath(args)
			if err != nil {
				return err
			}
			added, err := store.Add(path)
			if err != nil {
				return err
			}
			if !added {
				fmt.Fprintf(cmd.OutOrStdout(), "already sensitive: %s\n", path)
				return nil
			}
			if err := sensitive.Save(storePath, store); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "marked sensitive: %s\n", path)
			return nil
		},
	}
}

func newSensitiveRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <path>",
		Short: "Unmark a directory as sensitive",
		Long: `Remove matches by exact path. A directory covered only by a broader
marked parent is not removable on its own — unmark the parent instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, storePath, err := loadSensitiveStore()
			if err != nil {
				return err
			}
			path, err := resolveSensitivePath(args)
			if err != nil {
				return err
			}
			removed, err := store.Remove(path)
			if err != nil {
				return err
			}
			if !removed {
				return fmt.Errorf("not marked sensitive: %s", path)
			}
			if err := sensitive.Save(storePath, store); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", path)
			return nil
		},
	}
}

func newSensitiveClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove every entry from the sensitive store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, storePath, err := loadSensitiveStore()
			if err != nil {
				return err
			}
			n := len(store.Roots)
			store.Clear()
			if err := sensitive.Save(storePath, store); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cleared %d sensitive project(s)\n", n)
			return nil
		},
	}
}

// loadSensitiveStore is the shared Load helper for every sensitive subcommand.
// Returns the store, the path it was loaded from (for a subsequent Save), and
// any error.
func loadSensitiveStore() (*sensitive.Store, string, error) {
	path, err := sensitive.DefaultStorePath()
	if err != nil {
		return nil, "", err
	}
	store, err := sensitive.Load(path)
	if err != nil {
		return nil, "", err
	}
	return store, path, nil
}

// resolveSensitivePath returns the path the user is operating on.
// Empty args → cwd. One arg → that arg.
func resolveSensitivePath(args []string) (string, error) {
	if len(args) == 0 {
		return os.Getwd()
	}
	return args[0], nil
}
