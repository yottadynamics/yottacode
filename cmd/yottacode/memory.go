package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/config"
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
		newMemoryReindexCmd(),
		newMemorySearchCmd(),
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
~/.yottacode/memory/user/<name>.md (user scope) or
~/.yottacode/memory/projects/<project_slug>/<name>.md (project scope).
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
			memory.DeleteVec(path)
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

// newMemoryReindexCmd generates vector embeddings for all memories
// missing .vec sidecar files. Requires a local Ollama server with an
// embedding model installed.
func newMemoryReindexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Generate vector embeddings for semantic memory retrieval",
		Long: `Reindex scans all user-scope and project-scope memories and generates
embedding vectors for any entries missing a .vec sidecar file. Requires
a local Ollama server with the configured embedding model installed
(default: nomic-embed-text).

Configure the model via [retrieval] embedding_model in
~/.yottacode/config.toml.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, _ := config.LoadDefault()
			client := memory.NewEmbedClient("", cfg.Retrieval.EmbeddingModel)

			ctx := context.Background()
			if !client.Available(ctx) {
				return fmt.Errorf("embedding model %q not available — is Ollama running with the model installed?\n  Try: ollama pull %s",
					client.Model, client.Model)
			}

			loaded, err := memory.Load(cwd)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			var all []memory.MemoryEntry
			all = append(all, loaded.UserMemories...)
			all = append(all, loaded.ProjectMemories...)

			if len(all) == 0 {
				fmt.Fprintln(out, "no memories to index")
				return nil
			}

			fmt.Fprintf(out, "embedding %d memories via %s...\n", len(all), client.Model)
			var indexed, skipped int
			for _, e := range all {
				vecPath := memory.VecPath(e.Path)
				if !memory.NeedsReembed(vecPath, client.Model) {
					skipped++
					continue
				}
				text := e.Name + " " + e.Description + " " + e.Body
				vec, err := client.Embed(ctx, text)
				if err != nil {
					fmt.Fprintf(out, "  skip %s: %v\n", e.Name, err)
					continue
				}
				if err := memory.WriteVecWithModel(vecPath, vec, client.Model); err != nil {
					fmt.Fprintf(out, "  skip %s: %v\n", e.Name, err)
					continue
				}
				indexed++
			}
			fmt.Fprintf(out, "done: %d indexed, %d up-to-date\n", indexed, skipped)
			return nil
		},
	}
}

// newMemorySearchCmd scores all memories against a query and displays
// ranked results — the same view the agent's retrieval sees.
func newMemorySearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Search memories using BM25 scoring (stemming + synonyms)",
		Long: `Search scores all user-scope and project-scope memories against the
query using the BM25 retrieval algorithm with Porter stemming and
synonym expansion. Shows the same ranked results the agent's
per-turn retrieval would return.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			loaded, err := memory.Load(cwd)
			if err != nil {
				return err
			}

			var all []memory.MemoryEntry
			all = append(all, loaded.UserMemories...)
			all = append(all, loaded.ProjectMemories...)
			if len(all) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no memories)")
				return nil
			}

			corpus := memory.BuildCorpus(all)
			qStems := memory.StemExpandTokenize(query)
			ranked := corpus.Rank(qStems)

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "query: %q  (%d memories scored)\n\n", query, len(ranked))
			shown := 0
			for _, s := range ranked {
				if s.Score <= 0 {
					continue
				}
				shown++
				scope := s.Entry.Scope
				if scope == "" {
					scope = "?"
				}
				fmt.Fprintf(out, "  %.3f  %-8s %-10s %s", s.Score, scope, s.Entry.Type, s.Entry.Name)
				if s.Entry.Description != "" {
					fmt.Fprintf(out, " — %s", s.Entry.Description)
				}
				fmt.Fprintln(out)
				if shown >= 20 {
					break
				}
			}
			if shown == 0 {
				fmt.Fprintln(out, "  (no matches)")
			}
			return nil
		},
	}
}
