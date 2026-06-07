// Package memory loads optional context files that are auto-injected into
// the system prompt at session start.
//
// Layout:
//
//	~/.yottacode/USER.md                                      — global preferences (curated, human-only)
//	<cwd>/.yottacode/YOTTACODE.md                             — per-repo context (curated, agent-writable)
//	~/.yottacode/memory/<name>.md                             — user-scope agent-managed memories
//	~/.yottacode/projects/<project_slug>/memory/<name>.md     — project-scope agent-managed memories
//
// USER.md and YOTTACODE.md are the trust anchor: they always inject in
// full. The agent-managed memory dirs are managed via the memory_save /
// memory_forget tools — the agent decides in-band when something is
// worth remembering. MEMORY.md inside each memory dir is an
// auto-generated index that always renders too; the per-file bodies are
// filtered per turn by the retrieval orchestrator (see retrieve.go).
//
// The project_slug is derived by ProjectSlug(cwd) — see path.go.
package memory

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Loaded carries the raw contents (and resolved paths) of every memory
// source read at session start. The two trust anchors (USER.md and
// YOTTACODE.md) sit alongside the user-scope and project-scope
// agent-managed memory directories. ProjectPath / ProjectText refer to
// YOTTACODE.md (the field name is historical from the YOTTACODE.md era).
type Loaded struct {
	UserPath    string
	UserText    string
	ProjectPath string // YOTTACODE.md
	ProjectText string

	// User-scope agent-managed memories.
	UserMemoryDir   string
	UserMemoryIndex string // raw MEMORY.md text (rendered if missing on disk)
	UserMemories    []MemoryEntry

	// Project-scope agent-managed memories (per ProjectSlug(cwd)).
	ProjectMemoryDir   string
	ProjectMemoryIndex string
	ProjectMemories    []MemoryEntry
}

// Load reads every memory source for the session: USER.md, YOTTACODE.md,
// and both agent-managed memory dirs. Missing files / dirs leave the
// corresponding fields empty without returning an error.
func Load(cwd string) (Loaded, error) {
	var out Loaded

	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".yottacode", "USER.md")
		text, err := readOptional(userPath)
		if err != nil {
			return out, err
		}
		if text != "" {
			out.UserPath = userPath
			out.UserText = text
		}
	}

	projectPath := filepath.Join(cwd, ".yottacode", "YOTTACODE.md")
	if text, err := readOptional(projectPath); err != nil {
		return out, err
	} else if text != "" {
		out.ProjectPath = projectPath
		out.ProjectText = text
	}

	if dir, err := UserMemoryDir(); err == nil {
		out.UserMemoryDir = dir
		entries, err := scanMemoryDir(dir, "user")
		if err != nil {
			return out, err
		}
		out.UserMemories = entries
		out.UserMemoryIndex = loadOrRenderIndex(dir, "user", entries)
	}

	if dir, err := ProjectMemoryDir(cwd); err == nil {
		out.ProjectMemoryDir = dir
		entries, err := scanMemoryDir(dir, "project")
		if err != nil {
			return out, err
		}
		out.ProjectMemories = entries
		out.ProjectMemoryIndex = loadOrRenderIndex(dir, "project", entries)
	}

	return out, nil
}

// loadOrRenderIndex returns the on-disk MEMORY.md text when it exists,
// otherwise renders one in memory from the scanned entries.
func loadOrRenderIndex(dir, scope string, entries []MemoryEntry) string {
	indexPath := filepath.Join(dir, indexFileName)
	if data, err := os.ReadFile(indexPath); err == nil {
		return strings.TrimRight(string(data), "\n")
	}
	if len(entries) == 0 {
		return ""
	}
	return strings.TrimRight(RenderMemoryIndex(scope, entries), "\n")
}

// readOptional returns the file's contents, an empty string if the file does
// not exist, or an error for any other I/O failure.
func readOptional(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SystemPrompt composes the base prompt with every loaded memory
// section. Each section is framed as background reference — not as a
// topic to describe.
func SystemPrompt(base string, l Loaded) string {
	// Project shadows user: never inject a user-scope body whose name is
	// owned by a project-scope memory in this repo. The per-turn path
	// already shadowed before ranking (selectAcrossScopes); this covers
	// the unfiltered paths (session start, skills change, /memory reload)
	// and is a no-op when the user list was already shadowed.
	l.UserMemories = shadowUserByProject(l.UserMemories, l.ProjectMemories)
	var b strings.Builder
	b.WriteString(base)
	wroteSection := false
	maybeOpenBackground := func() {
		if wroteSection {
			return
		}
		b.WriteString("\n\n---\nThe following is BACKGROUND REFERENCE only. Do not summarize, recite, or describe it. Use it silently to inform tool choices when relevant; otherwise ignore it and act on the user's request with the tools available.")
		wroteSection = true
	}
	if l.UserText != "" {
		maybeOpenBackground()
		b.WriteString("\n\n## User preferences (background — do not recite)\n")
		b.WriteString(strings.TrimSpace(l.UserText))
	}
	if l.ProjectText != "" {
		maybeOpenBackground()
		b.WriteString("\n\n## Project context (background — do not describe or narrate the agent's architecture)\n")
		b.WriteString(strings.TrimSpace(l.ProjectText))
	}
	writeMemorySection(&b, "User memory index (background — do not narrate)", l.UserMemoryIndex, l.UserMemories, maybeOpenBackground)
	writeMemorySection(&b, "Project memory index (background — do not narrate)", l.ProjectMemoryIndex, l.ProjectMemories, maybeOpenBackground)
	if wroteSection {
		b.WriteString("\n\n---\nEnd of background. Take the user's request and act on it with the tools available. Do not narrate or describe how the agent works. ")
		b.WriteString(saveNudge)
	} else {
		// Cold start: no USER.md, no YOTTACODE.md, no saved memories.
		// This is when the nudge matters MOST — a store that never
		// receives its first save never bootstraps — so the save nudge
		// is appended even when there is no background block to close.
		b.WriteString("\n\n")
		b.WriteString(saveNudge)
	}
	return b.String()
}

// saveNudge is the proactive-memory closing line of the composed system
// prompt. It sits at the very end on purpose: the closing words are the
// most-attended instruction real estate in the prompt, and ending on
// pure act-on-the-request framing taught models to treat memory_save as
// out of scope for the turn. Appended both after the background block
// and on cold start (see SystemPrompt).
const saveNudge = "If this turn taught you something durable about the user or project — a preference, a correction, a validated approach, a fact you'd otherwise re-derive — persist it with memory_save before ending the turn."

// writeMemorySection appends one scope's index + per-entry bodies.
func writeMemorySection(b *strings.Builder, header, indexText string, entries []MemoryEntry, openBackground func()) {
	if indexText == "" && len(entries) == 0 {
		return
	}
	openBackground()
	b.WriteString("\n\n## ")
	b.WriteString(header)
	if indexText != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(indexText))
	}
	for _, e := range entries {
		b.WriteString("\n\n### ")
		b.WriteString(e.Name)
		if e.Type != "" {
			b.WriteString(" [")
			b.WriteString(e.Type)
			b.WriteString("]")
		}
		if e.Description != "" {
			b.WriteString(" — ")
			b.WriteString(e.Description)
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(e.Body))
	}
}

// Summary is the short tag used by the TUI status bar — a
// human-friendly one-liner describing which memory sources are active.
type Summary struct {
	User       bool
	Project    bool
	UserMem    int
	ProjectMem int
}

func (l Loaded) Summary() Summary {
	return Summary{
		User:       l.UserText != "",
		Project:    l.ProjectText != "",
		UserMem:    len(l.UserMemories),
		ProjectMem: len(l.ProjectMemories),
	}
}

// String renders the Summary as something compact like "USER+YOTTA" or
// "USER+UMEM(3)+PMEM(7)" or "" (when nothing was loaded).
func (s Summary) String() string {
	var parts []string
	if s.User {
		parts = append(parts, "USER")
	}
	if s.Project {
		parts = append(parts, "YOTTA")
	}
	if s.UserMem > 0 {
		parts = append(parts, "UMEM")
	}
	if s.ProjectMem > 0 {
		parts = append(parts, "PMEM")
	}
	return strings.Join(parts, "+")
}
