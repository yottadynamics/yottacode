package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/trust"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// newWorktreeCmd builds the `yottacode worktree` subcommand tree.
//
// Worktrees let multiple yottacode sessions edit the same repo in
// parallel without colliding — each session lives in a separate
// directory under <repo>/.yottacode/worktrees/<name>/ on its own
// branch. The CLI surface mirrors `yottacode trust`: list / remove /
// prune for admin, plus `status` for a clean-or-dirty summary.
//
// The agent reaches the same machinery through the enter_worktree /
// exit_worktree / worktree_status tools.
func newWorktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage yottacode-managed git worktrees",
		Long: `Worktrees let parallel yottacode sessions edit the same repo
without colliding. Each lives under <repo>/.yottacode/worktrees/<name>/
on branch worktree-<name>. Launch with: yottacode --worktree <name>.

  list      print every yottacode worktree in this repo
  remove    delete a worktree (and its branch if it follows our naming)
  prune     run git worktree prune
  status    report clean / dirty state per worktree
  where     resolve slug → originating repo path (cross-repo)`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newWorktreeListCmd(),
		newWorktreeRemoveCmd(),
		newWorktreePruneCmd(),
		newWorktreeStatusCmd(),
		newWorktreeWhereCmd(),
	)
	return cmd
}

func newWorktreeWhereCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "where [slug]",
		Short: "Print the originating repo path for a slug; with no arg, list every slug → repo pair",
		Long: `Worktrees live at ~/.yottacode/worktrees/<slug>/<name>/ and the
slug is derived from a hash of the repo path. The slug dir carries
an .origin file naming the originating repo so this command can map
slug back to its source.

  yottacode worktree where                   # list every known slug
  yottacode worktree where yottacode-1a2b3c4d # one repo path`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			root := filepath.Join(home, ".yottacode", "worktrees")
			out := cmd.OutOrStdout()

			if len(args) == 1 {
				slug := args[0]
				origin, err := worktree.ReadOrigin(filepath.Join(root, slug))
				if err != nil {
					return fmt.Errorf("worktree where: %s: %w", slug, err)
				}
				fmt.Fprintln(out, origin)
				return nil
			}

			entries, err := os.ReadDir(root)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(out, "(no yottacode worktrees)")
					return nil
				}
				return err
			}
			any := false
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				origin, err := worktree.ReadOrigin(filepath.Join(root, e.Name()))
				if err != nil {
					// Slug dir without .origin (pre-feature worktree, or
					// .origin write failed). Surface with a placeholder
					// rather than dropping silently — the user can
					// recreate the file or clean up.
					fmt.Fprintf(out, "%s\t(no .origin)\n", e.Name())
					continue
				}
				fmt.Fprintf(out, "%s\t%s\n", e.Name(), origin)
				any = true
			}
			if !any && len(entries) == 0 {
				fmt.Fprintln(out, "(no yottacode worktrees)")
			}
			return nil
		},
	}
}

func newWorktreeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List yottacode worktrees in this repo",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := repoRootFromCwd(cmd.Context())
			if err != nil {
				return err
			}
			infos, err := worktree.List(cmd.Context(), repoRoot)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			any := false
			for _, w := range infos {
				name, ok := worktree.IsWorktreePath(repoRoot, w.Path)
				if !ok {
					continue
				}
				lock := ""
				if w.Locked {
					lock = "  [locked]"
				}
				fmt.Fprintf(out, "%s\tbranch=%s\tpath=%s%s\n", name, w.Branch, w.Path, lock)
				any = true
			}
			if !any {
				fmt.Fprintln(out, "(no yottacode worktrees)")
			}
			return nil
		},
	}
}

func newWorktreeRemoveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a yottacode worktree (and its branch if it follows worktree-* naming)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := worktree.ValidateName(name); err != nil {
				return err
			}
			repoRoot, err := repoRootFromCwd(cmd.Context())
			if err != nil {
				return err
			}
			wtDir := worktree.Dir(repoRoot, name)
			if err := worktree.Remove(cmd.Context(), repoRoot, wtDir, force); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed worktree %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force-remove even when the worktree has uncommitted / untracked / unpushed work")
	return cmd
}

func newWorktreePruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Run git worktree prune to remove stale administrative entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := repoRootFromCwd(cmd.Context())
			if err != nil {
				return err
			}
			if _, err := runGit(cmd.Context(), repoRoot, "worktree", "prune"); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "pruned stale worktree administrative data")
			return nil
		},
	}
}

func newWorktreeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [name]",
		Short: "Report clean / dirty state per yottacode worktree (all if no name)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := repoRootFromCwd(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			report := func(name, wtDir string) {
				st, err := worktree.DetectState(cmd.Context(), wtDir)
				if err != nil {
					fmt.Fprintf(out, "%s\terror: %v\n", name, err)
					return
				}
				tag := "clean"
				if !st.Clean() {
					tag = "dirty: " + strings.Join(st.Reasons(), ", ")
				}
				fmt.Fprintf(out, "%s\tbranch=%s\tpath=%s\t%s\n", name, worktree.Branch(name), wtDir, tag)
			}
			if len(args) == 1 {
				name := args[0]
				if err := worktree.ValidateName(name); err != nil {
					return err
				}
				report(name, worktree.Dir(repoRoot, name))
				return nil
			}
			infos, err := worktree.List(cmd.Context(), repoRoot)
			if err != nil {
				return err
			}
			any := false
			for _, w := range infos {
				name, ok := worktree.IsWorktreePath(repoRoot, w.Path)
				if !ok {
					continue
				}
				report(name, w.Path)
				any = true
			}
			if !any {
				fmt.Fprintln(out, "(no yottacode worktrees)")
			}
			return nil
		},
	}
}

// ensureWorktree is the startup hook that turns the --worktree flag
// into a usable session: trust-check the repo, materialize (or attach
// to) the worktree directory, copy .worktreeinclude files, and chdir
// so every downstream call sees the new cwd. Sets opts.Worktree to
// the final resolved name so session.Worktree is stamped correctly.
//
// Returns nil and leaves opts.Worktree empty when --worktree wasn't
// passed; downstream code treats that as "run in the main checkout."
func ensureWorktree(ctx context.Context, opts *cli.ChatOptions) error {
	if opts.Worktree == "" {
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Trust gate. The worktree dir doesn't exist yet, so we check the
	// repo root (worktrees inherit trust via pathUnder). This mirrors
	// folder-trust's "ordering vs. worktrees — decided" rule.
	if err := worktreeTrustCheck(opts, cwd); err != nil {
		return err
	}

	repoRoot, err := worktree.ResolveRepoRoot(ctx, cwd)
	if err != nil {
		return err
	}

	name := opts.Worktree
	if name == cli.WorktreeAutoGenerate {
		name, err = worktree.Generate(ctx, repoRoot)
		if err != nil {
			return fmt.Errorf("worktree: generate name: %w", err)
		}
	}
	if err := worktree.ValidateName(name); err != nil {
		return err
	}

	wtDir := worktree.Dir(repoRoot, name)

	// If the worktree already exists, attach; otherwise create.
	infos, err := worktree.List(ctx, repoRoot)
	if err != nil {
		return err
	}
	exists := false
	for _, w := range infos {
		if worktree.SamePath(w.Path, wtDir) {
			exists = true
			break
		}
	}
	if !exists {
		branch := worktree.Branch(name)
		// Resolve base ref: prefer origin/HEAD ("fresh") when present,
		// fall back to local HEAD. v1 doesn't surface a base-ref CLI
		// flag; the agent enter_worktree tool can pass it explicitly.
		baseRef := "HEAD"
		if _, err := runGit(ctx, repoRoot, "rev-parse", "--verify", "origin/HEAD"); err == nil {
			baseRef = "origin/HEAD"
		}
		if _, err := runGit(ctx, repoRoot, "worktree", "add", "-b", branch, wtDir, baseRef); err != nil {
			return fmt.Errorf("worktree: create: %w", err)
		}
	}

	if err := worktree.CopyIncluded(repoRoot, wtDir); err != nil {
		return fmt.Errorf("worktree: copy .worktreeinclude: %w", err)
	}

	// Record the originating repo path next to the slug dir so the
	// `yottacode worktree where` admin command can resolve slug → repo
	// without poking at git internals. Soft-fail: a missing .origin
	// just means `where` won't find this slug; the worktree itself is
	// fine.
	_ = worktree.WriteOrigin(worktree.SlugDir(repoRoot), repoRoot)

	if err := os.Chdir(wtDir); err != nil {
		return fmt.Errorf("worktree: chdir %s: %w", wtDir, err)
	}

	// Resolved name for downstream (session stamping, TUI status line).
	opts.Worktree = name
	return nil
}

// worktreeTrustCheck enforces the workspace-trust gate before
// materializing a worktree, mirroring the rule in folder-trust.md
// ("--worktree requires trust on the repo root"). Honors
// --allow-paths and the YOTTACODE_TRUST_ALL CI escape hatch the same
// way ensureWorkspaceTrust in internal/tui does.
func worktreeTrustCheck(opts *cli.ChatOptions, cwd string) error {
	storePath, err := trust.DefaultStorePath()
	if err != nil {
		return err
	}
	store, err := trust.Load(storePath)
	if err != nil {
		return err
	}
	allow := splitAllowPaths(opts.AllowPaths)
	if trust.IsTrusted(store, cwd, allow) {
		return nil
	}
	return fmt.Errorf(
		"workspace trust not accepted for %s\n\n"+
			"Run `yottacode` in this directory first to accept the trust prompt, "+
			"then re-run with --worktree.", cwd)
}

// splitAllowPaths mirrors the helper in internal/tui — kept private
// here so cmd/yottacode/ doesn't take an internal/tui import.
func splitAllowPaths(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// repoRootFromCwd is the shared helper for every worktree subcommand:
// figure out which repo we're in from the current cwd. Errors with a
// helpful message if cwd isn't inside a git repo at all.
func repoRootFromCwd(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := worktree.ResolveRepoRoot(ctx, cwd)
	if err != nil {
		return "", errors.New("not inside a git repository — `yottacode worktree` only works in a checkout")
	}
	return root, nil
}

// runGit is a thin wrapper over exec.CommandContext, used by the
// worktree CLI handlers and ensureWorktree. Mirrors internal/agent
// gitOutput but lives here so cmd/yottacode/ doesn't take a back-
// import on internal/agent (which would create an import cycle with
// internal/tui).
func runGit(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
