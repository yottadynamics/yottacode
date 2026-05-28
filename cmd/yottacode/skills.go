package main

// `yottacode skills` is the user-facing surface for managing Agent
// Skills outside the TUI. Mirrors the in-TUI `/skills install|list|
// show|uninstall` subcommands so authoring, scripting, and CI flows
// don't have to round-trip through a Bubbletea session.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/skills"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install, list, show, and remove Agent Skills",
		Long: `Manage SKILL.md-format Agent Skills stored under
~/.yottacode/skills/. Run without a subcommand for help.

  install <source>   install from a local path, https://.../SKILL.md URL,
                     or owner/repo[/path] (GitHub Contents API)
  list               list every loaded skill (built-in + user + project)
  show <name>        print the body of a loaded skill
  uninstall <name>   remove a user-installed skill from ~/.yottacode/skills/
  check [name]       compare installed bytes against ~/.yottacode/skills/.lock.json
  update [name]      re-fetch installed skills from their recorded source`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newSkillsInstallCmd(),
		newSkillsListCmd(),
		newSkillsShowCmd(),
		newSkillsUninstallCmd(),
		newSkillsCheckCmd(),
		newSkillsUpdateCmd(),
	)
	return cmd
}

func newSkillsInstallCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "install <source>",
		Short: "Install a SKILL.md from a local path, URL, or GitHub shorthand",
		Long: `Install one Agent Skill into ~/.yottacode/skills/<slug>/, where
<slug> is taken from the SKILL.md frontmatter ` + "`name`" + `.

Accepted source shapes:

  ./path/to/skill           local directory containing SKILL.md
  ./path/to/skill/SKILL.md  local SKILL.md file (no resources)
  https://.../SKILL.md      single-file fetch; resources are NOT walked
  owner/repo                GitHub repo root (must contain SKILL.md)
  owner/repo/path/to/skill  GitHub subpath (Contents API; copies
                            scripts/, references/, assets/)

Refuses to overwrite an existing install unless --force is set.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := skills.Install(skills.InstallOptions{
				Source: args[0],
				Force:  force,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s (%s) at %s\n",
				res.Skill.Name, res.SourceType, res.Dir)
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite an existing skill of the same name")
	return c
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every loaded skill (built-in + user + project)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			res, err := skills.LoadAll(cwd, nil)
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			out := cmd.OutOrStdout()
			if len(res.Skills) == 0 {
				fmt.Fprintln(out, "(no skills found)")
				return nil
			}
			maxName := 4
			for _, sk := range res.Skills {
				if l := len(sk.Name); l > maxName {
					maxName = l
				}
			}
			for _, sk := range res.Skills {
				fmt.Fprintf(out, "%-*s  %-9s  %s\n", maxName, sk.Name, sk.Source, sk.Description)
			}
			return nil
		},
	}
}

func newSkillsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print the body of a loaded skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			res, err := skills.LoadAll(cwd, nil)
			if err != nil {
				return err
			}
			sk := skills.Find(res.Skills, args[0])
			if sk == nil {
				return fmt.Errorf("no skill named %q (run 'yottacode skills list' to see what's loaded)", args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "# %s\n_%s — %s_\n\n%s\n",
				sk.Name, sk.Source, sk.SourcePath, sk.Body)
			return nil
		},
	}
}

func newSkillsUninstallCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "uninstall <name>",
		Aliases: []string{"remove", "rm"},
		Short:   "Remove a user-installed skill",
		Long: `Uninstall scopes to ~/.yottacode/skills/ only. Built-in skills are
embedded in the binary and cannot be removed; project-scope skills
under .yottacode/skills/ are committed source — remove them via git
or rm directly.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := skills.Uninstall(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", path)
			return nil
		},
	}
	return c
}

// newSkillsCheckCmd reports drift between the installed bytes and the
// lockfile's recorded hash. Read-only — exits non-zero only when
// invoked with a name that doesn't exist; per-skill modified/orphan
// statuses are reported but don't fail the command.
func newSkillsCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check [name]",
		Short: "Check installed skills for drift from the lockfile",
		Long: `Compare each installed skill's current bytes to the hash recorded
in ~/.yottacode/skills/.lock.json. Reports:

  ok                  installed bytes match the lockfile
  modified            on-disk copy was edited after install
  missing-lock        installed but not tracked (reinstall to record)
  orphaned-lock       lockfile entry exists but the dir is gone
  hash-error          couldn't read the installed dir

With no name argument, every tracked + installed skill is checked.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := skills.CheckOptions{}
			if len(args) == 1 {
				opts.Name = args[0]
			}
			results, err := skills.Check(opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(results) == 0 {
				fmt.Fprintln(out, "(no user-installed skills)")
				return nil
			}
			maxName := 4
			for _, r := range results {
				if l := len(r.Name); l > maxName {
					maxName = l
				}
			}
			for _, r := range results {
				line := fmt.Sprintf("%-*s  %s", maxName, r.Name, r.Status)
				if r.Error != "" {
					line += "  (" + r.Error + ")"
				}
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
}

// newSkillsUpdateCmd re-fetches installed skills from their recorded
// source. With no name argument, updates every tracked skill;
// otherwise updates just the named one. --force overrides the
// user-modified skip so an edited install can be reset to upstream.
func newSkillsUpdateCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "update [name]",
		Short: "Re-fetch installed skills from their recorded source",
		Long: `Walk the lockfile (~/.yottacode/skills/.lock.json) and re-run the
installer for each entry against its originally-recorded source.

By default skips any skill whose on-disk hash diverges from the
lockfile's recorded hash (so a hand-edit isn't silently overwritten);
pass --force to overwrite anyway.

Reports per skill:

  updated                 new bytes differ from what was installed
  unchanged               re-fetched, byte-identical
  skipped-user-modified   on-disk hash diverges; --force to overwrite
  skipped-no-lockfile     skill has no lockfile entry (pre-Phase 2)
  error                   refetch failed`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := skills.UpdateOptions{Force: force}
			if len(args) == 1 {
				opts.Name = args[0]
			}
			results, err := skills.Update(opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(results) == 0 {
				fmt.Fprintln(out, "(no tracked skills to update)")
				return nil
			}
			maxName := 4
			for _, r := range results {
				if l := len(r.Name); l > maxName {
					maxName = l
				}
			}
			for _, r := range results {
				line := fmt.Sprintf("%-*s  %s", maxName, r.Name, r.Status)
				if r.Message != "" {
					line += "  (" + r.Message + ")"
				}
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite user-modified installs")
	return c
}
