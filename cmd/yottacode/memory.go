package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

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
  search   search memories using BM25 scoring
  audit    report memories that need curation
  forget   delete a saved memory by name`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newMemoryListCmd(),
		newMemoryForgetCmd(),
		newMemoryReindexCmd(),
		newMemorySearchCmd(),
		newMemoryAuditCmd(),
		newMemoryHealthCmd(),
		newMemoryArchiveCmd(),
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

func renderAuditHealth(health memory.AuditHealth) string {
	var b strings.Builder
	fmt.Fprintf(&b, "memory health: %d memories, %d issue(s)\n", health.TotalMemories, health.TotalIssues)
	fmt.Fprintf(&b, "quick notes: %d (%d old)\n", health.QuickNotes, health.OldQuickNotes)
	fmt.Fprintf(&b, "duplicates: %d\n", health.DuplicateDescriptions)
	fmt.Fprintf(&b, "vague bodies: %d\n", health.VagueBodies)
	fmt.Fprintf(&b, "empty bodies: %d\n", health.EmptyBodies)
	fmt.Fprintf(&b, "portable scope mistakes: %d", health.PortableScopeMistakes)
	return b.String()
}

func formatAuditIssueSource(issue memory.AuditIssue) string {
	if issue.SourceSession == "" {
		return ""
	}
	if issue.SourceTurn != "" {
		return fmt.Sprintf(" session %s turn %s", issue.SourceSession, issue.SourceTurn)
	}
	return " session " + issue.SourceSession
}

func formatProposalSource(src memory.ProposalSource) string {
	base := fmt.Sprintf("%s/%s [%s] — %s", src.Scope, src.Name, src.Type, src.Excerpt)
	if src.SourceSession == "" {
		return base
	}
	if src.SourceTurn != "" {
		return fmt.Sprintf("%s (source: session %s turn %s)", base, src.SourceSession, src.SourceTurn)
	}
	return fmt.Sprintf("%s (source: session %s)", base, src.SourceSession)
}

func renderAuditProposals(loaded memory.Loaded, report memory.AuditReport) string {
	proposals := memory.ProposeCuration(loaded, report)
	var b strings.Builder
	fmt.Fprintf(&b, "curation proposals: %d proposal(s)\n", len(proposals))
	if len(proposals) == 0 {
		b.WriteString("no subjective curation proposals; memory store looks curated or only mechanical fixes remain")
		return b.String()
	}
	b.WriteString("not applied: review proposals, then use memory_get/memory_save/memory_forget or memory_curate_apply explicitly")
	for i, p := range proposals {
		fmt.Fprintf(&b, "\n\n%d. %s: %s\n", i+1, p.Problem, p.Action)
		fmt.Fprintf(&b, "   rationale: %s\n", p.Rationale)
		if p.Uncertainty != "" {
			fmt.Fprintf(&b, "   uncertainty: %s\n", p.Uncertainty)
		}
		for _, src := range p.Sources {
			fmt.Fprintf(&b, "   source: %s\n", formatProposalSource(src))
		}
		if p.ProposedMemory != nil {
			m := p.ProposedMemory
			fmt.Fprintf(&b, "   proposed memory_save: scope=%s type=%s name=%s\n", m.Scope, m.Type, m.Name)
			fmt.Fprintf(&b, "   description: %s\n", m.Description)
			fmt.Fprintf(&b, "   content: %s\n", strings.ReplaceAll(m.Content, "\n", "\\n"))
		}
		for _, f := range p.Forget {
			fmt.Fprintf(&b, "   proposed forget after save: %s/%s\n", f.Scope, f.Name)
		}
	}
	return b.String()
}

func renderAuditPlan(report memory.AuditReport) string {
	plan := memory.PlanCuration(report)
	var b strings.Builder
	fmt.Fprintf(&b, "curation plan: %d issue(s), %d batch(es)\n", plan.TotalIssues, len(plan.Batches))
	if len(plan.Batches) == 0 {
		b.WriteString("memory store looks curated")
		return b.String()
	}
	for i, batch := range plan.Batches {
		fmt.Fprintf(&b, "\n%d. %s (%d issue(s))\n", i+1, batch.Title, len(batch.Issues))
		fmt.Fprintf(&b, "   action: %s\n", batch.Action)
		for _, issue := range batch.Issues {
			fmt.Fprintf(&b, "   - %s/%s [%s] %s, created %s (%s old)%s\n",
				issue.Scope, issue.Name, issue.Type, issue.Problem,
				memory.FormatAuditCreated(issue.Created), memory.FormatAuditAge(issue.AgeDays), formatAuditIssueSource(issue))
		}
	}
	return b.String()
}

func newMemoryHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Show compact memory health counts",
		Long: `Health summarizes memory-store curation counts without dumping the full audit queue.
It is read-only and reports totals for quick notes, old notes, duplicates,
vague bodies, empty bodies, and portable scope mistakes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			loaded, err := memory.Load(cwd)
			if err != nil {
				return err
			}
			report := memory.Audit(loaded)
			fmt.Fprintln(cmd.OutOrStdout(), renderAuditHealth(report.Health))
			return nil
		},
	}
}

func newMemoryArchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Inspect and prune archived memory versions",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newMemoryArchiveListCmd(), newMemoryArchivePruneCmd())
	return cmd
}

func newMemoryArchiveListCmd() *cobra.Command {
	var scope string
	c := &cobra.Command{
		Use:   "list",
		Short: "List archived memory versions by memory name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			summaries, err := memory.ListArchiveSummaries(scope, cwd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(summaries) == 0 {
				fmt.Fprintln(out, "(no archived memories)")
				return nil
			}
			fmt.Fprintln(out, "scope\tmemory\tarchives\toldest\tnewest\tbytes")
			for _, s := range summaries {
				fmt.Fprintf(out, "%s\t%s\t%d\t%s\t%s\t%d\n", s.Scope, s.Memory, s.Count, formatArchiveTime(s.Oldest), formatArchiveTime(s.Newest), s.Bytes)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "all", "memory scope (all, user, or project)")
	return c
}

func newMemoryArchivePruneCmd() *cobra.Command {
	var scope string
	var olderThanDays int
	var keepLatest int
	var dryRun bool
	c := &cobra.Command{
		Use:   "prune",
		Short: "Prune archived memory versions with explicit retention flags",
		Long: `Prune deletes only files under memory .archive directories. It never deletes live
memory files. By default it is a dry run; pass --dry-run=false to delete the
selected archive files after reviewing the output.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if olderThanDays <= 0 && keepLatest <= 0 {
				return fmt.Errorf("memory archive prune requires --older-than-days or --keep-latest")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			res, err := memory.PruneArchives(cwd, memory.ArchivePruneOptions{Scope: scope, OlderThanDays: olderThanDays, KeepLatest: keepLatest, DryRun: dryRun})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			verb := "would delete"
			if !dryRun {
				verb = "deleted"
			}
			fmt.Fprintf(out, "%s %d archive file(s), %d bytes\n", verb, res.Matched, res.Bytes)
			for _, e := range res.Entries {
				fmt.Fprintf(out, "- %s/%s %s %d bytes %s\n", e.Scope, e.Memory, formatArchiveTime(e.ModTime), e.Size, e.Path)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "all", "memory scope (all, user, or project)")
	c.Flags().IntVar(&olderThanDays, "older-than-days", 0, "select archives older than this many days")
	c.Flags().IntVar(&keepLatest, "keep-latest", 0, "keep this many newest archives per memory")
	c.Flags().BoolVar(&dryRun, "dry-run", true, "show what would be deleted without removing files")
	return c
}

func formatArchiveTime(t time.Time) string {
	return memory.FormatArchiveTime(t)
}

// newMemoryAuditCmd reports memory entries that should be curated. It is
// read-only: humans or agents can use the queue to merge quick captures,
// correct scope, or delete stale entries without the command silently changing
// long-term context.
func newMemoryAuditCmd() *cobra.Command {
	var plan bool
	var propose bool
	c := &cobra.Command{
		Use:   "audit",
		Short: "Report memories that need curation",
		Long: `Audit scans user-scope and project-scope memories for curation issues:
quick-capture notes, duplicate descriptions, empty or description-only bodies,
and portable user/feedback memories saved to project scope.

It never edits memory files. Use the report as a queue for memory_save /
memory_forget curation or for a later curator-agent pass.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			loaded, err := memory.Load(cwd)
			if err != nil {
				return err
			}
			report := memory.Audit(loaded)
			out := cmd.OutOrStdout()
			if plan {
				fmt.Fprintln(out, renderAuditPlan(report))
				return nil
			}
			if propose {
				fmt.Fprintln(out, renderAuditProposals(loaded, report))
				return nil
			}
			fmt.Fprintf(out, "memories: %d total, %d quick note(s), %d issue(s)\n", report.Total, report.QuickNotes, len(report.Issues))
			if len(report.Issues) == 0 {
				fmt.Fprintln(out, "memory store looks curated")
				return nil
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, "scope\ttype\tname\tcreated\tage\tsource\tissue\tdetail\taction")
			for _, issue := range report.Issues {
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", issue.Scope, issue.Type, issue.Name, memory.FormatAuditCreated(issue.Created), memory.FormatAuditAge(issue.AgeDays), formatAuditIssueSource(issue), issue.Problem, issue.Detail, issue.Action)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&plan, "plan", false, "group audit issues into a read-only curation plan")
	c.Flags().BoolVar(&propose, "propose", false, "draft read-only proposals for subjective curation issues")
	return c
}
