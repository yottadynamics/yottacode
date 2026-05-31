package memory

// Relevance-evaluation harness.
//
// Until now retrieval quality was tuned by heuristics (headline boost,
// synonym weights, the 60/40 blend) with no way to MEASURE whether a change
// helped or hurt. This file pins a small labeled fixture — a corpus of
// memories plus query→expected-memory cases — and scores the ranker against
// it with standard IR metrics:
//
//	Hit@1  fraction of queries whose expected memory ranks #1
//	Hit@3  fraction whose expected memory lands in the top 3
//	MRR    mean reciprocal rank of the expected memory (1/rank, averaged)
//
//   - TestRetrievalRelevance_BM25 is the deterministic, dependency-free gate:
//     it runs the fixture through the BM25 path and fails if quality drops
//     below calibrated floors, so a regression in stemming/synonyms/headline
//     weighting is caught in CI.
//   - TestRetrievalRelevance_Semantic runs the SAME fixture through the
//     BM25+embedding blend when a local Ollama model is available (skipped
//     otherwise) and logs a BM25-vs-semantic comparison, so the accuracy
//     tradeoff is observable on whatever embedding model is configured.
//
// Run `go test ./internal/memory -run Relevance -v` to see per-query ranks.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

type evalMemory struct{ name, typ, desc, body string }

type evalCase struct {
	query string
	want  string // the memory that SHOULD rank first for this query
}

// relevanceCorpus is a deliberately topic-disjoint set so each case has one
// unambiguous best answer. Keep entries distinct when adding cases.
var relevanceCorpus = []evalMemory{
	{"jwt-refresh-flow", "project", "auth token refresh interacts with the token cache",
		"The refresh handler writes the new JWT to the cache before it returns. Tests that mock the cache must seed it ahead of the call or the refresh path 401s."},
	{"table-driven-tests", "user", "prefers table-driven Go tests",
		"Write Go tests as table-driven cases with a slice of structs and subtests rather than repeated assertions."},
	{"db-migration-runner", "project", "schema migration runner reads up.sql files",
		"The migration runner applies database schema files matching migrations/sql/*.up.sql, not plain *.sql. Down migrations live separately."},
	{"no-stack-traces", "feedback", "cut to the fix, no stack traces in final answers",
		"When reporting a bug fix, summarize the cause and the change. Do not paste long stack traces into the final answer."},
	{"bundled-pr-approach", "feedback", "approved bundling related changes into one pull request",
		"The user validated combining the code, tests, and docs for a change into a single pull request instead of splitting them apart."},
	{"error-soft-fail", "project", "best-effort soft-fail philosophy on disk reads",
		"Disk reads in the loader never fail the turn: on a read error we return best-effort empty content and keep going rather than propagating it."},
	{"ollama-embeddings", "reference", "local embedding model setup via Ollama",
		"Pull nomic-embed-text with Ollama; it runs on CPU and generates vector sidecars locally so no data leaves the machine."},
	{"commit-identity", "project", "commits authored by the organization",
		"All commits in this repo are authored by the YottaDynamics org identity; do not invent individual handles from local paths."},
	{"tui-flush-left", "project", "TUI shares a column-0 left edge",
		"Everything in the terminal UI aligns to a flush-left column-0 edge; indentation comes from element structure, not a global margin."},
	{"memory-permission", "project", "single Memory permission gates all memory operations",
		"Every memory tool (save, forget, search, get, recall) resolves to the Memory permission namespace, so one rule governs them all."},
	{"release-process", "reference", "version bump, tag, and release notes are manual",
		"Cutting a release means bumping the version, tagging, and writing release notes by hand; these steps are deliberately not automated."},
	{"semantic-blend", "reference", "retrieval blends BM25 and cosine similarity",
		"Semantic memory retrieval combines a BM25 keyword score with cosine similarity from embeddings, weighted 60/40, then re-normalized."},
}

// relevanceParaphraseCases phrase the need with near-synonyms and LOW
// lexical overlap with the target — the regime where BM25 is weakest and
// semantic cosine can help (or, with a weak model, hurt). Reported side by
// side, not asserted: the point is to make the BM25-vs-semantic tradeoff
// visible on whatever embedding model is configured.
var relevanceParaphraseCases = []evalCase{
	{"prevent sessions from logging users out unexpectedly", "jwt-refresh-flow"},
	{"avoid dumping huge error output at the user", "no-stack-traces"},
	{"keep working when a file on disk cannot be opened", "error-soft-fail"},
}

var relevanceCases = []evalCase{
	{"jwt token refresh cache", "jwt-refresh-flow"},
	{"table driven tests in go", "table-driven-tests"},
	{"database schema migrations", "db-migration-runner"},
	{"do not paste stack traces in the answer", "no-stack-traces"},
	{"bundle the changes into one pull request", "bundled-pr-approach"},
	{"how are disk read errors handled", "error-soft-fail"},
	{"ollama embedding model setup", "ollama-embeddings"},
	{"who authors the commits", "commit-identity"},
	{"flush left tui layout edge", "tui-flush-left"},
	{"permission rule for memory operations", "memory-permission"},
	{"release version bump and tagging", "release-process"},
	{"bm25 cosine blend weighting", "semantic-blend"},
}

// buildEvalEntries materializes the fixture as MemoryEntry values. When dir
// is non-empty each entry gets a Path under it, so the semantic path can
// find a .vec sidecar; for BM25 dir is "" (no files needed).
func buildEvalEntries(dir string) []MemoryEntry {
	out := make([]MemoryEntry, len(relevanceCorpus))
	for i, m := range relevanceCorpus {
		e := MemoryEntry{Name: m.name, Type: m.typ, Description: m.desc, Body: m.body, Scope: "user"}
		if dir != "" {
			e.Path = filepath.Join(dir, m.name+".md")
		}
		out[i] = e
	}
	return out
}

// evalRankNames returns every entry ranked best-first for query. TopK=0 /
// MaxBytes=0 / MinScore=0 disable the injection caps so we get the FULL
// ranking and can score the expected memory's true position.
func evalRankNames(entries []MemoryEntry, query, strategy string, client *EmbedClient) []string {
	cfg := config.RetrievalConfig{Enabled: true, TopK: 0, MaxBytes: 0, MinScore: 0, Strategy: strategy, SemanticWeight: 0.4}
	scored := SelectWithEmbeddingsScored(entries, query, cfg, client)
	names := make([]string, len(scored))
	for i, s := range scored {
		names[i] = s.Entry.Name
	}
	return names
}

// evalRankOf returns the 1-based rank of want in names, or 0 if absent.
func evalRankOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i + 1
		}
	}
	return 0
}

// evalMetrics scores the whole fixture and returns Hit@1, Hit@3, MRR plus a
// per-query report (rank + query + expected) for -v output.
func evalMetrics(entries []MemoryEntry, strategy string, client *EmbedClient) (hit1, hit3, mrr float64, report []string) {
	var sum float64
	var h1, h3 int
	for _, c := range relevanceCases {
		r := evalRankOf(evalRankNames(entries, c.query, strategy, client), c.want)
		if r == 1 {
			h1++
		}
		if r >= 1 && r <= 3 {
			h3++
		}
		if r > 0 {
			sum += 1.0 / float64(r)
		}
		mark := "·"
		if r == 1 {
			mark = "✓"
		} else if r == 0 || r > 3 {
			mark = "✗"
		}
		report = append(report, fmt.Sprintf("  rank=%-2d %s  %-42q → %s", r, mark, c.query, c.want))
	}
	n := float64(len(relevanceCases))
	return float64(h1) / n, float64(h3) / n, sum / n, report
}

func TestRetrievalRelevance_BM25(t *testing.T) {
	resetCorpusCache()
	entries := buildEvalEntries("")
	hit1, hit3, mrr, report := evalMetrics(entries, "bm25", nil)
	for _, line := range report {
		t.Log(line)
	}
	t.Logf("BM25 over %d cases: Hit@1=%.2f Hit@3=%.2f MRR=%.3f", len(relevanceCases), hit1, hit3, mrr)

	// Floors sit below measured BM25 performance on this fixture: they gate
	// regressions in the scorer (stemming, synonym expansion, headline boost)
	// without flaking on alphabetical tie-break jitter. Raise them if the
	// fixture is hardened, or investigate a drop before lowering them.
	const (
		minHit1 = 0.80
		minHit3 = 1.00
		minMRR  = 0.88
	)
	if hit1 < minHit1 {
		t.Errorf("BM25 Hit@1 = %.2f, want >= %.2f (relevance regression?)", hit1, minHit1)
	}
	if hit3 < minHit3 {
		t.Errorf("BM25 Hit@3 = %.2f, want >= %.2f (expected memory fell out of top 3)", hit3, minHit3)
	}
	if mrr < minMRR {
		t.Errorf("BM25 MRR = %.3f, want >= %.3f (ranking quality regression)", mrr, minMRR)
	}
}

func TestRetrievalRelevance_Semantic(t *testing.T) {
	client := NewEmbedClient("", "nomic-embed-text")
	if !client.Available(context.Background()) {
		t.Skip("semantic relevance eval needs a local Ollama embedding model; skipping (BM25 eval still gates)")
	}
	resetCorpusCache()
	dir := t.TempDir()
	entries := buildEvalEntries(dir)
	// Generate .vec sidecars exactly like `memory reindex`: embed
	// name + description + body per memory, tagged with the model name.
	for _, e := range entries {
		vec, err := client.Embed(context.Background(), e.Name+" "+e.Description+" "+e.Body)
		if err != nil {
			t.Fatalf("embed %s: %v", e.Name, err)
		}
		if err := WriteVecWithModel(VecPath(e.Path), vec, client.Model); err != nil {
			t.Fatalf("write vec %s: %v", e.Name, err)
		}
	}

	bH1, bH3, bMRR, _ := evalMetrics(entries, "bm25", nil)
	sH1, sH3, sMRR, sReport := evalMetrics(entries, "semantic", client)
	for _, line := range sReport {
		t.Log(line)
	}
	t.Logf("model=%s", client.Model)
	t.Logf("  BM25     Hit@1=%.2f Hit@3=%.2f MRR=%.3f", bH1, bH3, bMRR)
	t.Logf("  Semantic Hit@1=%.2f Hit@3=%.2f MRR=%.3f", sH1, sH3, sMRR)

	// Paraphrase / low-overlap regime: where keywords thin out, this is the
	// tradeoff that actually matters. Shown per-case (BM25 rank vs semantic
	// rank) so you can see whether the blend rescues the misses.
	t.Log("paraphrase (low lexical overlap) — BM25 rank vs semantic rank:")
	var pbSum, psSum float64
	for _, c := range relevanceParaphraseCases {
		br := evalRankOf(evalRankNames(entries, c.query, "bm25", nil), c.want)
		sr := evalRankOf(evalRankNames(entries, c.query, "semantic", client), c.want)
		if br > 0 {
			pbSum += 1.0 / float64(br)
		}
		if sr > 0 {
			psSum += 1.0 / float64(sr)
		}
		t.Logf("  bm25=%-2d semantic=%-2d  %q → %s", br, sr, c.query, c.want)
	}
	pn := float64(len(relevanceParaphraseCases))
	t.Logf("  paraphrase MRR: BM25=%.3f Semantic=%.3f", pbSum/pn, psSum/pn)

	// Only a loose sanity floor is asserted: the blend's quality depends on
	// the embedding model (a weak one need not beat BM25), so we don't gate
	// on "semantic > BM25". The logged comparison above is the deliverable —
	// it lets you measure the tradeoff on your configured model. A near-zero
	// MRR means the .vec wiring or embedding call is broken, which we DO fail.
	if sMRR < 0.30 {
		t.Errorf("semantic MRR = %.3f implausibly low — embedding/.vec wiring likely broken", sMRR)
	}
}
