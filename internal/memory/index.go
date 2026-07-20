package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MemoryEntry is one parsed memory file: enough to render the prompt
// and the MEMORY.md index. Body is raw markdown (frontmatter
// stripped).
type MemoryEntry struct {
	Path        string
	Scope       string // "user" | "project"
	Name        string // basename without .md
	Type        string // user | feedback | project | reference
	Description string
	Created     time.Time // zero when frontmatter omitted or held an invalid timestamp
	Body        string
}

// scanMemoryDir reads every *.md file in dir (skipping MEMORY.md),
// parses frontmatter, and returns entries sorted by name. Symlinks
// are skipped defensively. Missing dir returns (nil, nil) — empty is
// not an error.
func scanMemoryDir(dir, scope string) ([]MemoryEntry, error) {
	infos, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: read %q: %w", dir, err)
	}
	var entries []MemoryEntry
	for _, info := range infos {
		if info.IsDir() {
			continue
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".md") || name == indexFileName {
			continue
		}
		full := filepath.Join(dir, name)
		lstat, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if lstat.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		fm, body, _ := ParseFrontmatter(data)
		base := strings.TrimSuffix(name, ".md")
		typ := fm.Type
		if typ == "" {
			typ = "reference"
		}
		created := time.Time{}
		if fm.Created != "" {
			if ts, err := time.Parse(time.RFC3339, fm.Created); err == nil {
				created = ts
			}
		}
		// The filename is the entry's identity, not the frontmatter
		// `name:` field. The index renders links as [Name](Name.md) and
		// memory_forget resolves a memory by computing <Name>.md, so Name
		// MUST equal the basename or both break. Tool-written files keep
		// the two in sync (memory_save writes <name>.md with name: <name>),
		// but a hand-edited file whose frontmatter name drifts from its
		// filename would otherwise produce dead index links and an
		// un-forgettable memory. Trusting the basename closes that gap;
		// the frontmatter `name:` stays as human-facing redundancy.
		entries = append(entries, MemoryEntry{
			Path:        full,
			Scope:       scope,
			Name:        base,
			Type:        typ,
			Description: fm.Description,
			Created:     created,
			Body:        strings.TrimRight(body, "\n"),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// indexFileName is the basename of the auto-generated index file
// inside every memory directory.
const indexFileName = "MEMORY.md"

// RenderMemoryIndex builds the MEMORY.md text for one scope: a header,
// a stable preamble warning humans not to edit it, and a per-type
// section listing each entry as a markdown link with its description.
// Empty entries produce a minimal index (just the preamble) so the
// scanner still recognizes the file as ours.
func RenderMemoryIndex(scope string, entries []MemoryEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Memory Index (%s scope)\n\n", scope)
	b.WriteString("This file is auto-managed by yottacode. Edit individual memory files instead — this index regenerates on every memory_save / memory_forget call.\n\n")
	if len(entries) == 0 {
		return b.String()
	}
	byType := map[string][]MemoryEntry{}
	for _, e := range entries {
		byType[e.Type] = append(byType[e.Type], e)
	}
	// Render the conventional types first in canonical order, then any
	// free-form (custom) types alphabetically. Types are free-form, so
	// the renderer must cover whatever's present — iterating only the
	// conventional list would silently drop custom-typed memories from
	// the index even though their bodies still inject.
	conventional := []string{"user", "feedback", "project", "reference"}
	isConventional := map[string]bool{"user": true, "feedback": true, "project": true, "reference": true}
	var custom []string
	for typ := range byType {
		if !isConventional[typ] {
			custom = append(custom, typ)
		}
	}
	sort.Strings(custom)
	order := append(append([]string{}, conventional...), custom...)
	for _, t := range order {
		group := byType[t]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n", t)
		for _, e := range group {
			desc := e.Description
			if desc == "" {
				fmt.Fprintf(&b, "- [%s](%s.md)\n", e.Name, e.Name)
			} else {
				fmt.Fprintf(&b, "- [%s](%s.md) — %s\n", e.Name, e.Name, desc)
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RegenerateMemoryIndex re-scans the chosen scope's memory dir and
// rewrites MEMORY.md atomically. Used by memory_save and
// memory_forget after every mutation. If the dir is empty or missing
// after the mutation, MEMORY.md is removed instead of being written
// as an empty stub.
func RegenerateMemoryIndex(scope, cwd string) error {
	dir, err := memoryDirFor(scope, cwd)
	if err != nil {
		return err
	}
	entries, err := scanMemoryDir(dir, scope)
	if err != nil {
		return err
	}
	indexPath := filepath.Join(dir, indexFileName)
	if len(entries) == 0 {
		if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("memory: remove stale index %q: %w", indexPath, err)
		}
		return nil
	}
	body := RenderMemoryIndex(scope, entries)
	return AtomicWrite(indexPath, []byte(body), 0o600)
}

// memoryDirFor resolves the directory for the given scope. Project
// scope always derives from cwd via ProjectMemoryDir.
func memoryDirFor(scope, cwd string) (string, error) {
	switch scope {
	case "user":
		return UserMemoryDir()
	case "project":
		return ProjectMemoryDir(cwd)
	default:
		return "", fmt.Errorf("memory: invalid scope %q", scope)
	}
}

// Index files are written via the shared memory.AtomicWrite (see
// atomicwrite.go) so MEMORY.md gets the same unique-temp + fsync
// durability as every other memory write.
