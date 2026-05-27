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
	"context"
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
// against the query using the legacy keyword strategy. Pure and
// deterministic. Kept for backward compatibility with strategy="keyword".
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

// scoreBM25 scores entries against the query using BM25 with stemming
// and synonym expansion. Returns Scored slice sorted descending by
// score with alphabetical tie-breaking.
func scoreBM25(entries []MemoryEntry, query string) []Scored {
	corpus := BuildCorpus(entries)
	qStems := StemExpandTokenize(query)
	return corpus.Rank(qStems)
}

// StemExpandTokenize tokenizes, stems, and expands synonyms for the
// query side. Synonym expansion runs on queries only (not documents)
// to increase recall without inflating document frequencies.
func StemExpandTokenize(s string) []string {
	raw := tokenize(s)
	seen := make(map[string]struct{}, len(raw)*3)
	out := make([]string, 0, len(raw)*3)
	for _, t := range raw {
		stemmed := Stem(t)
		if _, ok := seen[stemmed]; ok {
			continue
		}
		seen[stemmed] = struct{}{}
		out = append(out, stemmed)
		for _, syn := range Expand(stemmed) {
			if syn == stemmed {
				continue
			}
			if _, ok := seen[syn]; ok {
				continue
			}
			seen[syn] = struct{}{}
			out = append(out, syn)
		}
	}
	return out
}

const (
	semanticBM25Weight   = 0.6
	semanticCosineWeight = 0.4
)

// Select ranks entries against the query and returns at most cfg.TopK
// with score >= cfg.MinScore. Strategy selects the scoring algorithm:
// "keyword" uses the legacy exact-token scorer, "bm25" (default) uses
// BM25 with stemming and synonyms.
func Select(entries []MemoryEntry, query string, cfg config.RetrievalConfig) []MemoryEntry {
	return SelectWithEmbeddings(entries, query, cfg, nil)
}

// SelectWithEmbeddings is like Select but accepts an optional
// EmbedClient for semantic scoring. When embedClient is non-nil and
// strategy is "semantic", BM25 scores are combined with cosine
// similarity from vector embeddings.
func SelectWithEmbeddings(entries []MemoryEntry, query string, cfg config.RetrievalConfig, embedClient *EmbedClient) []MemoryEntry {
	if !cfg.Enabled || len(entries) == 0 {
		return entries
	}

	strategy := resolveStrategy(cfg.Strategy, embedClient)

	var scored []Scored
	switch strategy {
	case "keyword":
		scored = make([]Scored, 0, len(entries))
		for _, e := range entries {
			scored = append(scored, Scored{Entry: e, Score: Score(e, query)})
		}
		sort.SliceStable(scored, func(i, j int) bool {
			if scored[i].Score != scored[j].Score {
				return scored[i].Score > scored[j].Score
			}
			return scored[i].Entry.Name < scored[j].Entry.Name
		})
	case "semantic":
		scored = scoreSemantic(entries, query, embedClient)
	default:
		scored = scoreBM25(entries, query)
	}

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

// resolveStrategy determines the effective scoring strategy. "auto"
// resolves to "semantic" when an embed client is available, otherwise
// "bm25". Empty defaults to "bm25".
func resolveStrategy(strategy string, embedClient *EmbedClient) string {
	switch strategy {
	case "semantic":
		if embedClient == nil {
			return "bm25"
		}
		return "semantic"
	case "auto":
		if embedClient != nil {
			return "semantic"
		}
		return "bm25"
	case "keyword":
		return "keyword"
	default:
		return "bm25"
	}
}

// scoreSemantic combines BM25 scores with cosine similarity from
// vector embeddings. Entries without .vec sidecars score on BM25 alone.
func scoreSemantic(entries []MemoryEntry, query string, client *EmbedClient) []Scored {
	bm25Scored := scoreBM25(entries, query)
	if client == nil {
		return bm25Scored
	}

	queryVec, err := client.Embed(context.Background(), query)
	if err != nil {
		return bm25Scored
	}

	maxBM25 := 0.0
	for _, s := range bm25Scored {
		if s.Score > maxBM25 {
			maxBM25 = s.Score
		}
	}

	for i := range bm25Scored {
		normBM25 := 0.0
		if maxBM25 > 0 {
			normBM25 = bm25Scored[i].Score / maxBM25
		}

		cosine := 0.0
		entryVec, _ := ReadVec(VecPath(bm25Scored[i].Entry.Path))
		if entryVec != nil {
			cosine = CosineSimilarity(queryVec, entryVec)
			if cosine < 0 {
				cosine = 0
			}
		}

		bm25Scored[i].Score = semanticBM25Weight*normBM25 + semanticCosineWeight*cosine
	}

	sort.SliceStable(bm25Scored, func(i, j int) bool {
		if bm25Scored[i].Score != bm25Scored[j].Score {
			return bm25Scored[i].Score > bm25Scored[j].Score
		}
		return bm25Scored[i].Entry.Name < bm25Scored[j].Entry.Name
	})
	return bm25Scored
}

// SystemPromptFor is the per-turn variant of SystemPrompt: USER.md
// and YOTTACODE.md inject in full as before, both MEMORY.md indexes
// inject in full (they're the table of contents), and per-entry
// bodies pass through Select(query, cfg) first.
func SystemPromptFor(base string, l Loaded, query string, cfg config.RetrievalConfig) string {
	return SystemPromptForSemantic(base, l, query, cfg, nil)
}

// SystemPromptForSemantic is like SystemPromptFor but accepts an
// optional EmbedClient for semantic retrieval. Pass nil to use
// keyword/bm25 scoring only.
func SystemPromptForSemantic(base string, l Loaded, query string, cfg config.RetrievalConfig, embedClient *EmbedClient) string {
	user, project := selectAcrossScopes(l.UserMemories, l.ProjectMemories, query, cfg, embedClient)
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
func selectAcrossScopes(user, project []MemoryEntry, query string, cfg config.RetrievalConfig, embedClient *EmbedClient) ([]MemoryEntry, []MemoryEntry) {
	if !cfg.Enabled {
		return user, project
	}
	combined := make([]MemoryEntry, 0, len(user)+len(project))
	combined = append(combined, user...)
	combined = append(combined, project...)
	winners := SelectWithEmbeddings(combined, query, cfg, embedClient)
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
