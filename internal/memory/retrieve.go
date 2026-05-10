// Package memory — retrieval orchestrator.
//
// Agent-managed memory grows over time. By v0.4 a heavy user can
// accumulate dozens of typed memory files; concatenating every body
// into the system prompt on every turn taxes both the context window
// and the model's attention.
//
// This file scores each MemoryEntry against the current user prompt
// and returns a ranked, length-bounded subset. It deliberately does
// NOT touch USER.md / YOTTACODE.md — those are the trust anchor:
// short, curated, and always injected in full. USER.md is human-only;
// YOTTACODE.md is human-seeded and the agent keeps it fresh as the
// project evolves (parity with how Claude Code maintains CLAUDE.md).
//
// MEMORY.md (the per-scope index) is also unfiltered — it's the
// table of contents, and the model needs to know what files exist
// even when their bodies aren't injected. Only per-entry bodies pass
// through Select.
package memory

import (
	"sort"
	"strings"
	"unicode"

	"github.com/yottadynamics/yottacode/internal/config"
)

// Scored pairs a memory entry with the relevance score the
// orchestrator assigned it for a particular query. Score is in
// [0.0, 1.0]; deterministic across runs.
type Scored struct {
	Entry MemoryEntry
	Score float64
}

// Score returns a relevance score in [0.0, 1.0] for the given entry
// against the query. Pure and deterministic.
func Score(entry MemoryEntry, query string) float64 {
	qTokens := tokenize(query)
	if len(qTokens) == 0 {
		return 0
	}
	bodyTokens := tokenSet(tokenize(entry.Body))
	headlineTokens := tokenSet(tokenize(entry.Name + " " + entry.Type + " " + entry.Description))

	const headlineWeight = 3.0
	var hits float64
	for _, t := range qTokens {
		if _, ok := headlineTokens[t]; ok {
			hits += headlineWeight
			continue
		}
		if _, ok := bodyTokens[t]; ok {
			hits++
		}
	}
	max := float64(len(qTokens)) * headlineWeight
	score := hits / max
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// Select ranks entries against the query and returns at most cfg.TopK
// with score >= cfg.MinScore.
func Select(entries []MemoryEntry, query string, cfg config.RetrievalConfig) []MemoryEntry {
	if !cfg.Enabled || len(entries) == 0 {
		return entries
	}
	scored := make([]Scored, 0, len(entries))
	for _, e := range entries {
		scored = append(scored, Scored{Entry: e, Score: Score(e, query)})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Entry.Name < scored[j].Entry.Name
	})

	out := make([]MemoryEntry, 0, len(scored))
	for _, s := range scored {
		if s.Score < cfg.MinScore {
			continue
		}
		out = append(out, s.Entry)
		if cfg.TopK > 0 && len(out) >= cfg.TopK {
			break
		}
	}
	return out
}

// SystemPromptFor is the per-turn variant of SystemPrompt: USER.md
// and YOTTACODE.md inject in full as before, both MEMORY.md indexes
// inject in full (they're the table of contents), and per-entry
// bodies pass through Select(query, cfg) first.
func SystemPromptFor(base string, l Loaded, query string, cfg config.RetrievalConfig) string {
	user, project := selectAcrossScopes(l.UserMemories, l.ProjectMemories, query, cfg)
	filtered := Loaded{
		UserPath:           l.UserPath,
		UserText:           l.UserText,
		ProjectPath:        l.ProjectPath,
		ProjectText:        l.ProjectText,
		UserMemoryDir:      l.UserMemoryDir,
		UserMemoryIndex:    l.UserMemoryIndex,
		UserMemories:       user,
		ProjectMemoryDir:   l.ProjectMemoryDir,
		ProjectMemoryIndex: l.ProjectMemoryIndex,
		ProjectMemories:    project,
	}
	return SystemPrompt(base, filtered)
}

// selectAcrossScopes ranks both pools jointly under one cfg.TopK
// budget, then partitions the result back into per-scope slices.
func selectAcrossScopes(user, project []MemoryEntry, query string, cfg config.RetrievalConfig) ([]MemoryEntry, []MemoryEntry) {
	if !cfg.Enabled {
		return user, project
	}
	combined := make([]MemoryEntry, 0, len(user)+len(project))
	combined = append(combined, user...)
	combined = append(combined, project...)
	winners := Select(combined, query, cfg)
	wantUser := make(map[string]bool, len(winners))
	wantProject := make(map[string]bool, len(winners))
	for _, w := range winners {
		switch w.Scope {
		case "user":
			wantUser[w.Name] = true
		case "project":
			wantProject[w.Name] = true
		}
	}
	filterByName := func(entries []MemoryEntry, want map[string]bool) []MemoryEntry {
		out := make([]MemoryEntry, 0, len(entries))
		for _, e := range entries {
			if want[e.Name] {
				out = append(out, e)
			}
		}
		return out
	}
	return filterByName(user, wantUser), filterByName(project, wantProject)
}

// tokenize lowercases, splits on non-alphanumeric, drops stopwords and
// tokens shorter than 3 chars.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	lower := strings.ToLower(s)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 3 {
			continue
		}
		if _, stop := stopwords[f]; stop {
			continue
		}
		out = append(out, f)
	}
	return out
}

func tokenSet(tokens []string) map[string]struct{} {
	if len(tokens) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		out[t] = struct{}{}
	}
	return out
}

var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "are": {}, "but": {}, "not": {}, "you": {},
	"all": {}, "any": {}, "can": {}, "had": {}, "her": {}, "was": {}, "one": {},
	"our": {}, "out": {}, "day": {}, "get": {}, "has": {}, "him": {}, "his": {},
	"how": {}, "man": {}, "new": {}, "now": {}, "old": {}, "see": {}, "two": {},
	"way": {}, "who": {}, "boy": {}, "did": {}, "its": {}, "let": {}, "put": {},
	"say": {}, "she": {}, "too": {}, "use": {}, "with": {}, "this": {},
	"that": {}, "have": {}, "from": {}, "they": {}, "will": {}, "would": {},
	"there": {}, "their": {}, "what": {}, "about": {}, "which": {}, "when": {},
	"your": {}, "these": {}, "want": {}, "been": {}, "than": {}, "them": {},
	"like": {}, "into": {}, "just": {}, "also": {}, "should": {}, "could": {},
	"some": {}, "more": {}, "very": {}, "make": {}, "made": {}, "does": {},
	"each": {}, "many": {}, "such": {}, "only": {}, "those": {}, "well": {},
}
