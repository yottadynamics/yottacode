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

// ExplicitSearchMinScore is the default relevance floor for the agent-facing
// memory_search tool. Prompt injection keeps using retrieval.min_score; explicit
// tool searches need a small floor so tiny stem/synonym tail scores do not show
// unrelated memories as matches.
const ExplicitSearchMinScore = 0.05

// ExplicitSearchMatch reports whether a scored memory should be shown by the
// agent-facing memory_search tool. The agent can inspect ranked candidates and
// refine its query, so keep this conservative: drop only obvious low-score noise
// instead of applying brittle UI-style query heuristics.
func ExplicitSearchMatch(_ MemoryEntry, _ string, score float64) bool {
	return score >= ExplicitSearchMinScore
}

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
// score with alphabetical tie-breaking. Raw BM25 is unbounded, so the
// scores are normalized into [0,1] (see normalizeByMax) to honor the
// contract documented on Scored.
func scoreBM25(entries []MemoryEntry, query string) []Scored {
	corpus := cachedCorpus(entries)
	scored := corpus.rankWeighted(stemExpandWeighted(query))
	normalizeByMax(scored)
	return scored
}

// synonymWeight is the BM25 contribution multiplier for a synonym-derived
// query term, relative to 1.0 for the original stem. Query-side synonym
// expansion (StemExpandTokenize) boosts recall, but counting every
// synonym as a full term lets a document that touches several distinct
// synonyms of a group outrank one that uses the exact searched term. A
// fractional weight keeps the recall benefit while preserving exact-term
// precedence. 0.5 (synonyms count half) is a deliberately conservative
// default; tune it if a relevance eval warrants.
const synonymWeight = 0.5

// stemExpandWeighted is the weighted counterpart of StemExpandTokenize:
// it tokenizes + stems the query, then expands synonyms, tagging the
// original stems with weight 1.0 and synonyms with synonymWeight. When a
// term appears both as an original stem and as a synonym of another, the
// higher (original) weight wins.
func stemExpandWeighted(s string) []weightedStem {
	raw := tokenize(s)
	idx := make(map[string]int, len(raw)*3)
	out := make([]weightedStem, 0, len(raw)*3)
	add := func(stem string, w float64) {
		if i, ok := idx[stem]; ok {
			if w > out[i].weight {
				out[i].weight = w
			}
			return
		}
		idx[stem] = len(out)
		out = append(out, weightedStem{stem: stem, weight: w})
	}
	for _, t := range raw {
		st := Stem(t)
		add(st, 1.0)
		for _, syn := range Expand(st) {
			if syn == st {
				continue
			}
			add(syn, synonymWeight)
		}
	}
	return out
}

// normalizeByMax rescales scores into [0,1] by dividing every score by
// the maximum in the slice. Raw BM25 scores are unbounded, but Scored
// documents a [0,1] range and the MinScore filter + memory_search's
// displayed score both rely on it. Dividing by a single positive
// constant preserves rank order and the alphabetical tie-break. It is a
// no-op when the max is <= 0 (no query term matched any document — all
// scores are already 0).
func normalizeByMax(scored []Scored) {
	maxScore := 0.0
	for _, s := range scored {
		if s.Score > maxScore {
			maxScore = s.Score
		}
	}
	if maxScore <= 0 {
		return
	}
	for i := range scored {
		scored[i].Score /= maxScore
	}
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

// Select ranks entries against the query and returns at most cfg.TopK
// with score >= cfg.MinScore, further capped so the combined entry
// bodies stay within cfg.MaxBytes (0 = unlimited). Strategy selects the
// scoring algorithm: "keyword" uses the legacy exact-token scorer,
// "bm25" (default) uses BM25 with stemming and synonyms.
func Select(entries []MemoryEntry, query string, cfg config.RetrievalConfig) []MemoryEntry {
	// nil client → the embed path can't fire, so no caller-supplied ctx
	// is needed; Background keeps the legacy signature stable.
	return SelectWithEmbeddings(context.Background(), entries, query, cfg, nil)
}

// SelectWithEmbeddings is like Select but accepts an optional
// EmbedClient for semantic scoring. When embedClient is non-nil and
// strategy is "semantic", BM25 scores are combined with cosine
// similarity from vector embeddings. ctx bounds the embed call: the
// TUI passes the turn context so Esc cancels an in-flight embed
// instead of waiting out its timeout.
func SelectWithEmbeddings(ctx context.Context, entries []MemoryEntry, query string, cfg config.RetrievalConfig, embedClient *EmbedClient) []MemoryEntry {
	scored := SelectWithEmbeddingsScored(ctx, entries, query, cfg, embedClient)
	if scored == nil {
		return nil
	}
	out := make([]MemoryEntry, len(scored))
	for i, s := range scored {
		out[i] = s.Entry
	}
	return out
}

// SelectWithEmbeddingsScored is like SelectWithEmbeddings but returns
// Scored entries with their relevance scores preserved. Used by
// memory_search so the agent can see how well each memory matched.
func SelectWithEmbeddingsScored(ctx context.Context, entries []MemoryEntry, query string, cfg config.RetrievalConfig, embedClient *EmbedClient) []Scored {
	if entries == nil {
		return nil
	}
	if !cfg.Enabled || len(entries) == 0 {
		out := make([]Scored, len(entries))
		for i, e := range entries {
			out[i] = Scored{Entry: e, Score: 0}
		}
		return out
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
		// Normalize to top=1.0 like the other strategies so min_score
		// has one consistent meaning regardless of strategy. Score()'s
		// denominator is a theoretical all-headline-hit maximum, so its
		// raw top is usually well below 1.0.
		normalizeByMax(scored)
	case "semantic":
		scored = scoreSemantic(ctx, entries, query, embedClient, cfg.SemanticWeight)
	default:
		scored = scoreBM25(entries, query)
	}

	out := make([]Scored, 0, len(scored))
	usedBytes := 0
	for _, s := range scored {
		if s.Score < cfg.MinScore {
			continue
		}
		// Byte budget: stop once the accumulated bodies would exceed
		// MaxBytes. Entries are rank-ordered, so this drops the
		// least-relevant tail first. The top-ranked entry is always
		// admitted even if it alone exceeds the budget — dropping the
		// single most-relevant memory would be more surprising than
		// briefly overshooting. MaxBytes <= 0 means unlimited (count is
		// then bounded by TopK alone).
		if cfg.MaxBytes > 0 && len(out) > 0 && usedBytes+len(s.Entry.Body) > cfg.MaxBytes {
			break
		}
		out = append(out, s)
		usedBytes += len(s.Entry.Body)
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

// scoreSemantic combines BM25 scores with cosine similarity from vector
// embeddings, blended as (1-cosineWeight)*BM25 + cosineWeight*cosine and
// re-normalized to top=1.0. cosineWeight is retrieval.semantic_weight
// (clamped to [0,1]): 0 = pure BM25, 1 = pure cosine, 0.4 = the default
// 60/40 split. Entries without a matching-model .vec sidecar score on
// BM25 alone.
func scoreSemantic(ctx context.Context, entries []MemoryEntry, query string, client *EmbedClient, cosineWeight float64) []Scored {
	if cosineWeight < 0 {
		cosineWeight = 0
	}
	if cosineWeight > 1 {
		cosineWeight = 1
	}
	bm25Weight := 1 - cosineWeight
	bm25Scored := scoreBM25(entries, query)
	if client == nil {
		return bm25Scored
	}

	queryVec, err := client.Embed(ctx, query)
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

		cosine := entryCosine(queryVec, VecPath(bm25Scored[i].Entry.Path), client.Model)

		bm25Scored[i].Score = bm25Weight*normBM25 + cosineWeight*cosine
	}

	sort.SliceStable(bm25Scored, func(i, j int) bool {
		if bm25Scored[i].Score != bm25Scored[j].Score {
			return bm25Scored[i].Score > bm25Scored[j].Score
		}
		return bm25Scored[i].Entry.Name < bm25Scored[j].Entry.Name
	})
	// Re-normalize the blended score so the top match is 1.0, matching
	// the bm25 and keyword strategies. Without this the blend tops out
	// around 0.6–0.85, so a user-set min_score would mean something
	// different here than under bm25 — and would silently start dropping
	// matches the moment "auto" resolves to semantic (Ollama present).
	normalizeByMax(bm25Scored)
	return bm25Scored
}

// entryCosine returns the cosine similarity between the query vector
// and the entry's stored embedding — but only when that embedding was
// produced by currentModel. A legacy sidecar (no model recorded) or one
// embedded with a different model lives in an incompatible vector
// space, so blending its cosine into the score is meaningless: it would
// inject noise that looks like signal. In that case we return 0 and let
// BM25 carry the entry until `yottacode memory reindex` rebuilds the
// sidecar. Negative cosines clamp to 0. A missing or unreadable sidecar
// also yields 0.
func entryCosine(queryVec []float32, vecPath, currentModel string) float64 {
	entryVec, meta, err := readVecMetaCached(vecPath)
	if err != nil || entryVec == nil || meta.Model != currentModel {
		return 0
	}
	cosine := CosineSimilarity(queryVec, entryVec)
	if cosine < 0 {
		return 0
	}
	return cosine
}

// SystemPromptFor is the per-turn variant of SystemPrompt: USER.md
// and YOTTACODE.md inject in full as before, both MEMORY.md indexes
// inject in full (they're the table of contents), and per-entry
// bodies pass through Select(query, cfg) first.
func SystemPromptFor(base string, l Loaded, query string, cfg config.RetrievalConfig) string {
	// nil client → no embed call possible; Background is inert here.
	return SystemPromptForSemantic(context.Background(), base, l, query, cfg, nil)
}

// SystemPromptForSemantic is like SystemPromptFor but accepts an
// optional EmbedClient for semantic retrieval. Pass nil to use
// keyword/bm25 scoring only. ctx bounds the embed call — callers on
// an interactive path should pass a cancelable context (the TUI uses
// the turn context) so retrieval never outlives the work it serves.
func SystemPromptForSemantic(ctx context.Context, base string, l Loaded, query string, cfg config.RetrievalConfig, embedClient *EmbedClient) string {
	user, project := selectAcrossScopes(ctx, l.UserMemories, l.ProjectMemories, query, cfg, embedClient)
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

// ShadowUserByProject drops user-scope memories whose name also exists in
// project scope. In a given repo the project-scope memory of a name is
// authoritative, so the user-scope twin's body is not injected — it would
// otherwise duplicate or, worse, contradict the project version. This mirrors
// the project-shadows-user precedence slash commands and config layering
// already use. The user file stays on disk and still applies in every other
// repo (where no project twin exists). Shadowing keys on the full project set,
// so it applies whether or not the project twin ranked this turn — a name's
// owner doesn't flip based on relevance.
func ShadowUserByProject(user, project []MemoryEntry) []MemoryEntry {
	if len(user) == 0 || len(project) == 0 {
		return user
	}
	pNames := make(map[string]bool, len(project))
	for _, p := range project {
		pNames[p.Name] = true
	}
	out := make([]MemoryEntry, 0, len(user))
	for _, u := range user {
		if !pNames[u.Name] {
			out = append(out, u)
		}
	}
	return out
}

// EffectiveEntries returns the all-scope memory set after applying the same
// project-over-user precedence used by prompt injection.
func EffectiveEntries(user, project []MemoryEntry) []MemoryEntry {
	user = ShadowUserByProject(user, project)
	combined := make([]MemoryEntry, 0, len(user)+len(project))
	combined = append(combined, user...)
	combined = append(combined, project...)
	return combined
}

func shadowUserByProject(user, project []MemoryEntry) []MemoryEntry {
	return ShadowUserByProject(user, project)
}

// selectAcrossScopes ranks both pools jointly under one cfg.TopK
// budget, then partitions the result back into per-scope slices.
func selectAcrossScopes(ctx context.Context, user, project []MemoryEntry, query string, cfg config.RetrievalConfig, embedClient *EmbedClient) ([]MemoryEntry, []MemoryEntry) {
	// Project shadows user before ranking, so a shadowed user twin never
	// consumes a TopK slot or injects its body.
	user = shadowUserByProject(user, project)
	if !cfg.Enabled {
		return user, project
	}
	combined := make([]MemoryEntry, 0, len(user)+len(project))
	combined = append(combined, user...)
	combined = append(combined, project...)
	winners := SelectWithEmbeddings(ctx, combined, query, cfg, embedClient)
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
