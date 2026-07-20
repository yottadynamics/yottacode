package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HistoryDirName stores append-only curation history beside live memories.
const HistoryDirName = ".history"

// ArchiveEntry describes one archived prior memory version.
type ArchiveEntry struct {
	Scope   string
	Memory  string
	Path    string
	ModTime time.Time
	Size    int64
}

// ArchiveSummary groups archive inventory by memory name.
type ArchiveSummary struct {
	Scope    string
	Memory   string
	Count    int
	Oldest   time.Time
	Newest   time.Time
	Bytes    int64
	Archives []ArchiveEntry
}

// ArchivePruneOptions controls explicit archive pruning. Zero values mean no
// age cutoff and no per-memory keep floor.
type ArchivePruneOptions struct {
	Scope         string
	OlderThanDays int
	KeepLatest    int
	DryRun        bool
}

// ArchivePruneResult reports what an archive prune selected and optionally
// deleted.
type ArchivePruneResult struct {
	Matched int
	Deleted int
	Bytes   int64
	Entries []ArchiveEntry
}

// CurationHistoryRecord is one JSONL event explaining a memory maintenance
// action. It is stored outside the live memory frontmatter so history can grow
// without bloating prompt-injected memory bodies.
type CurationHistoryRecord struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"`
	Scope  string    `json:"scope,omitempty"`
	Name   string    `json:"name,omitempty"`
	From   string    `json:"from,omitempty"`
	To     string    `json:"to,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// ListArchiveSummaries inventories .archive files for the requested scope.
func ListArchiveSummaries(scope, cwd string) ([]ArchiveSummary, error) {
	entries, err := listArchiveEntries(scope, cwd)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*ArchiveSummary{}
	for _, e := range entries {
		key := e.Scope + "/" + e.Memory
		s := byKey[key]
		if s == nil {
			s = &ArchiveSummary{Scope: e.Scope, Memory: e.Memory, Oldest: e.ModTime, Newest: e.ModTime}
			byKey[key] = s
		}
		s.Count++
		s.Bytes += e.Size
		if e.ModTime.Before(s.Oldest) {
			s.Oldest = e.ModTime
		}
		if e.ModTime.After(s.Newest) {
			s.Newest = e.ModTime
		}
		s.Archives = append(s.Archives, e)
	}
	out := make([]ArchiveSummary, 0, len(byKey))
	for _, s := range byKey {
		sortArchiveEntries(s.Archives)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Memory < out[j].Memory
	})
	return out, nil
}

// PruneArchives deletes selected archive files only when DryRun is false.
func PruneArchives(cwd string, opts ArchivePruneOptions) (ArchivePruneResult, error) {
	entries, err := listArchiveEntries(opts.Scope, cwd)
	if err != nil {
		return ArchivePruneResult{}, err
	}
	cutoff := time.Time{}
	if opts.OlderThanDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -opts.OlderThanDays)
	}
	byKey := map[string][]ArchiveEntry{}
	for _, e := range entries {
		byKey[e.Scope+"/"+e.Memory] = append(byKey[e.Scope+"/"+e.Memory], e)
	}
	var selected []ArchiveEntry
	for _, group := range byKey {
		sortArchiveEntries(group)
		keep := opts.KeepLatest
		if keep < 0 {
			keep = 0
		}
		for i, e := range group {
			if keep > 0 && i < keep {
				continue
			}
			if !cutoff.IsZero() && !e.ModTime.Before(cutoff) {
				continue
			}
			selected = append(selected, e)
		}
	}
	sortArchiveEntries(selected)
	res := ArchivePruneResult{Matched: len(selected), Entries: selected}
	for _, e := range selected {
		res.Bytes += e.Size
		if opts.DryRun {
			continue
		}
		if err := os.Remove(e.Path); err != nil {
			return res, fmt.Errorf("memory archive prune: remove %q: %w", e.Path, err)
		}
		res.Deleted++
	}
	return res, nil
}

// RecordCurationHistory appends one curation event under the requested scope.
func RecordCurationHistory(scope, name, cwd string, rec CurationHistoryRecord) error {
	dir, err := memoryDirFor(scope, cwd)
	if err != nil {
		return err
	}
	if !topicNamePattern.MatchString(name) {
		return fmt.Errorf("memory history: invalid name %q", name)
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now().UTC()
	}
	if rec.Scope == "" {
		rec.Scope = scope
	}
	if rec.Name == "" {
		rec.Name = name
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	historyDir := filepath.Join(dir, HistoryDirName)
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(historyDir, name+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func listArchiveEntries(scope, cwd string) ([]ArchiveEntry, error) {
	scopes, err := archiveScopes(scope)
	if err != nil {
		return nil, err
	}
	var out []ArchiveEntry
	for _, sc := range scopes {
		dir, err := memoryDirFor(sc, cwd)
		if err != nil {
			return nil, err
		}
		archiveDir := filepath.Join(dir, ArchiveDirName)
		infos, err := os.ReadDir(archiveDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("memory archive: read %q: %w", archiveDir, err)
		}
		for _, info := range infos {
			if info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
				continue
			}
			full := filepath.Join(archiveDir, info.Name())
			st, err := os.Lstat(full)
			if err != nil || st.Mode()&os.ModeSymlink != 0 {
				continue
			}
			out = append(out, ArchiveEntry{Scope: sc, Memory: archiveMemoryName(info.Name()), Path: full, ModTime: st.ModTime(), Size: st.Size()})
		}
	}
	return out, nil
}

// FormatArchiveTime renders archive timestamps for CLI/tool output.
func FormatArchiveTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.DateOnly)
}

func archiveScopes(scope string) ([]string, error) {
	switch scope {
	case "", "all":
		return []string{"user", "project"}, nil
	case "user", "project":
		return []string{scope}, nil
	default:
		return nil, fmt.Errorf("memory archive: invalid scope %q (want all, user, or project)", scope)
	}
}

func archiveMemoryName(file string) string {
	base := strings.TrimSuffix(file, ".md")
	name, _, ok := strings.Cut(base, ".")
	if !ok || name == "" {
		return base
	}
	return name
}

func sortArchiveEntries(entries []ArchiveEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		return entries[i].Path < entries[j].Path
	})
}
