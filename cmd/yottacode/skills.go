package main

// `yottacode skills` is the user-facing surface for managing Agent
// Skills outside the TUI. Mirrors the in-TUI `/skills install|list|
// show|uninstall` subcommands so authoring, scripting, and CI flows
// don't have to round-trip through a Bubbletea session.

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/yottadynamics/yottacode/internal/skills"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install, list, show, and remove Agent Skills",
		Long: `Manage SKILL.md-format Agent Skills stored under
~/.yottacode/skills/. Run without a subcommand for help.

  install <source>   install from official/<name>, a skills.sh page,
                     a GitHub URL/shorthand, a local path, or
                     https://.../SKILL.md URL
  list               list every loaded skill (built-in + user + project)
  show <name>        print the body of a loaded skill
  uninstall <name>   remove a user-installed skill from ~/.yottacode/skills/
  check [name]       compare installed bytes against ~/.yottacode/skills/.lock.json
  update [name]      re-fetch installed skills from their recorded source
  catalog refresh    refresh the offline official catalog metadata cache
  new <slug>         scaffold a starter SKILL.md under ~/.yottacode/skills/<slug>/
  validate <path>    parse a SKILL.md (or skill dir) and report errors`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newSkillsInstallCmd(),
		newSkillsListCmd(),
		newSkillsShowCmd(),
		newSkillsUninstallCmd(),
		newSkillsCheckCmd(),
		newSkillsUpdateCmd(),
		newSkillsCatalogCmd(),
		newSkillsNewCmd(),
		newSkillsValidateCmd(),
	)
	return cmd
}

func newSkillsInstallCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "install <source>",
		Short: "Install a SKILL.md from official/<name>, a local path, URL, or GitHub shorthand",
		Long: `Install one Agent Skill into ~/.yottacode/skills/<slug>/, where
<slug> is taken from the SKILL.md frontmatter ` + "`name`" + `.

Accepted source shapes:

  official/<name>            public official yottacode-skills catalog
  https://www.skills.sh/<owner>/<repo>/<skill>
                              skills.sh page URL (copies resources)
  ./path/to/skill           local directory containing SKILL.md
  ./path/to/skill/SKILL.md  local SKILL.md file (copies sibling resources)
  https://.../SKILL.md      non-GitHub single-file fetch
  owner/repo/path/to/skill  GitHub shorthand (copies resources from archive)
  https://github.com/owner/repo/tree/<ref>/path/to/skill
  https://github.com/owner/repo/blob/<ref>/path/to/skill/SKILL.md
  https://raw.githubusercontent.com/owner/repo/<ref>/path/to/skill/SKILL.md
                              GitHub URLs (copies resources from archive)

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

// truncateRunes returns at most n runes of s, suffixed with "…" when
// truncation happens. Used by `skills list` to fit each row inside
// the terminal width. Counts runes so multi-byte characters don't
// blow up the visual column width.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
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
			// Column width budget: when stdout is a TTY, truncate the
			// description so each row fits one line — the previous
			// uncapped print soft-wrapped mid-word in narrow
			// terminals and broke the column alignment for every
			// subsequent skill. Piped output (CI, `| grep`,
			// scripting) keeps full descriptions so callers can
			// still match against the full text.
			descMax := 0
			if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && term.IsTerminal(int(os.Stdout.Fd())) {
				// Layout: <name> <2sp> <source padded to 9> <2sp> <description>
				const layoutOverhead = 2 + 9 + 2
				descMax = w - maxName - layoutOverhead
				if descMax < 20 {
					// Very narrow terminal — skip truncation rather
					// than render near-empty descriptions.
					descMax = 0
				}
			}
			for _, sk := range res.Skills {
				desc := sk.Description
				if descMax > 0 {
					desc = truncateRunes(desc, descMax)
				}
				fmt.Fprintf(out, "%-*s  %-9s  %s\n", maxName, sk.Name, sk.Source, desc)
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
			fmt.Fprint(cmd.OutOrStdout(), renderSkillShow(*sk))
			return nil
		},
	}
}

// renderSkillShow formats a Skill for the `show` surface. Splits
// metadata across its own block above the body so attribution +
// upstream pointers are easy to spot when vetting a community
// skill. Empty fields are dropped so the header stays compact for
// built-in skills (which generally don't populate them).
func renderSkillShow(sk skills.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n_%s — %s_\n\n", sk.Name, sk.Source, sk.SourcePath)
	if author := strings.TrimSpace(sk.Metadata["author"]); author != "" {
		fmt.Fprintf(&b, "Author: %s\n", author)
	}
	if src := strings.TrimSpace(sk.Metadata["source-url"]); src != "" {
		fmt.Fprintf(&b, "Source: %s\n", src)
	}
	// Trailing newline before the body when any metadata was
	// rendered, so the body's `#` heading visually separates from
	// the metadata block.
	if b.Len() > len("# "+sk.Name+"\n_"+string(sk.Source)+" — "+sk.SourcePath+"_\n\n") {
		b.WriteByte('\n')
	}
	b.WriteString(sk.Body)
	b.WriteByte('\n')
	return b.String()
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

// newSkillsNewCmd scaffolds a starter SKILL.md so a new author
// doesn't have to copy/paste frontmatter from another skill. Refuses
// to overwrite an existing dir unless --force is set.
func newSkillsNewCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "new <slug>",
		Short: "Scaffold a starter SKILL.md under ~/.yottacode/skills/<slug>/",
		Long: `Create ~/.yottacode/skills/<slug>/SKILL.md with placeholder
frontmatter (name, description, license, author, source-url) and a
body template. Fill in the TODO markers, then test with
` + "`yottacode skills validate ~/.yottacode/skills/<slug>`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := skills.NewSkill(skills.NewSkillOptions{
				Slug:  args[0],
				Force: force,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", res.SkillFile)
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite an existing dir of the same slug")
	return c
}

// newSkillsValidateCmd runs the loader's parser against a SKILL.md
// (or a skill dir) and reports the first error or "ok". Useful in
// CI for community-contributed skills.
func newSkillsValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Parse a SKILL.md (or skill dir) and report errors",
		Long: `Validates a SKILL.md against the same rules the runtime loader
applies (frontmatter shape, required fields, name pattern, length
caps, body non-empty). When path is a directory, the name field
must match the directory name; when path points at a bare SKILL.md
file, the name/dir match is skipped (CI / authoring use case).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res := skills.Validate(args[0])
			out := cmd.OutOrStdout()
			if res.Err != nil {
				fmt.Fprintf(out, "%s: %v\n", res.Path, res.Err)
				return res.Err
			}
			fmt.Fprintf(out, "%s: ok (%s — %s)\n", res.Path, res.Skill.Name, res.Skill.Description)
			return nil
		},
	}
}

func newSkillsCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Manage the offline official skills catalog metadata",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Refresh official catalog metadata from yottacode-skills",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := skills.RefreshOfficialCatalog(skills.OfficialCatalogOptions{})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "refreshed official catalog (%d skills)\n", len(rows))
			return nil
		},
	})
	return cmd
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
