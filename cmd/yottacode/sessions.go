package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/tui"
)

// newSessionsCmd builds the `yottacode sessions` subcommand tree.
// Mirrors the in-TUI /sessions picker (Resume / Rename / Export) plus
// a `list` for scripting / non-interactive workflows. The picker
// actions all have a non-interactive equivalent here.
//
// The bare `yottacode sessions` invocation prints help (cobra
// default) — there's no obvious "default action" for a command tree
// with four siblings.
func newSessionsCmd(opts *cli.ChatOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Inspect, resume, rename, or export saved sessions",
		Long: `Sessions wraps the four actions the in-TUI /sessions picker exposes
into non-interactive cobra subcommands so they can be scripted or
called from CI.

  list     list saved sessions (newest first), optionally as JSON
  resume   launch the TUI on a saved session
  rename   set the friendly name on a saved session
  export   write a saved session to disk as Markdown`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newSessionsListCmd(),
		newSessionsResumeCmd(opts),
		newSessionsRenameCmd(),
		newSessionsExportCmd(),
	)
	return cmd
}

// newSessionsListCmd prints metadata for every saved session,
// newest first. Plain-text by default; --json emits a stable shape
// for scripting. Mirrors the recent-list shown by the in-TUI picker
// (no 10-row cap here since CI / piping into `head` covers that).
func newSessionsListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved sessions newest-first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			infos, err := session.List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(infos)
			}
			if len(infos) == 0 {
				fmt.Fprintln(out, "(no saved sessions)")
				return nil
			}
			for _, s := range infos {
				name := s.Name
				if name == "" {
					name = "—"
				}
				wt := s.Worktree
				if wt == "" {
					wt = "—"
				}
				fmt.Fprintf(out, "%s\t%s\t%s\t%d msgs\tworktree=%s\t%s\n",
					s.ID,
					name,
					s.Model,
					s.Messages,
					wt,
					s.Created.Format(time.RFC3339),
				)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit session list as JSON")
	return cmd
}

// newSessionsResumeCmd launches the TUI on a saved session. Replaces
// the now-removed top-level `yottacode resume` — every picker action
// lives under `yottacode sessions <verb>` so the cobra surface
// matches the slash surface (where `/sessions` is the single entry
// point for Resume / Rename / Export).
func newSessionsResumeCmd(opts *cli.ChatOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <id|name>",
		Short: "Launch the TUI on a saved session",
		Long: `Resume reopens a saved session in the interactive TUI. The session
identifier may be a session id or a name set via 'yottacode sessions
rename' or the in-TUI /sessions Rename action.

With --summarized (-s), the prior transcript is replaced by a
four-section summary (Decisions / Code changes / Open questions /
Preferences) injected into the system prompt — useful when the saved
session is large enough that replaying it would crowd the context
window.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Resume = args[0]
			if err := cli.Resolve(opts); err != nil {
				return err
			}
			return tui.Run(cmd.Context(), *opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.Summarized, "summarized", "s", false,
		"Inject a structured summary of the prior transcript instead of replaying it verbatim")
	return cmd
}

// newSessionsRenameCmd sets the friendly Name field on a saved
// session. Non-interactive: load → mutate → save → exit. Mirrors
// the in-TUI /sessions Rename action without launching the picker.
func newSessionsRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <id|name> <new-name>",
		Short: "Set the friendly name on a saved session",
		Long: `Rename writes a new Name field onto the saved session's JSON file.
Identifier may be the session id or its current name; new-name is the
label you want to find it by next time.

Names are not required to be unique — the canonical key is the
session id; names are convenience labels for 'yottacode sessions
resume <name>'.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, newName := args[0], args[1]
			loaded, err := session.Load(id)
			if err != nil {
				return err
			}
			loaded.Name = newName
			if err := loaded.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "renamed %s → %q\n", loaded.ID, newName)
			return nil
		},
	}
}

// newSessionsExportCmd writes a saved session out as Markdown.
// Path argument is optional; when omitted, the file lands in the
// current working directory as <name-or-id>.md — matches the
// default the in-TUI picker pre-fills into its path textinput.
//
// --force overrides the "refuse to overwrite" guard so scripted
// runs that re-export the same session don't fail noisily.
func newSessionsExportCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "export <id|name> [path]",
		Short: "Write a saved session out as Markdown",
		Long: `Export renders a saved session into a Markdown document with each
turn formatted (system messages omitted, tool output fenced). Useful
for sharing in PRs/issues or archiving outside the JSON session store.

Identifier may be the session id or its name. When path is omitted
the file lands in the current working directory as
"<name-or-id>.md". Relative paths are resolved against cwd. The
command refuses to overwrite an existing file unless --force is
passed.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			loaded, err := session.Load(id)
			if err != nil {
				return err
			}

			path := ""
			if len(args) == 2 {
				path = args[1]
			}
			if path == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				base := loaded.ID
				if loaded.Name != "" {
					base = fmt.Sprintf("%s-%s", loaded.Name, loaded.ID)
				}
				path = filepath.Join(cwd, base+".md")
			} else if !filepath.IsAbs(path) {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				path = filepath.Join(cwd, path)
			}

			if !force {
				if _, statErr := os.Stat(path); statErr == nil {
					return fmt.Errorf("refusing to overwrite %s (pass --force to replace)", path)
				} else if !errors.Is(statErr, os.ErrNotExist) {
					return statErr
				}
			}

			md := session.ExportMarkdown(loaded)
			if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %d bytes to %s\n", len(md), path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite the destination if it already exists")
	return cmd
}
