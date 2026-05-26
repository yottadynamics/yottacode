package memory

import (
	"testing"
)

func bm25Entry(name, desc, body string) MemoryEntry {
	return MemoryEntry{Name: name, Description: desc, Body: body, Type: "reference", Scope: "user"}
}

func TestBM25_EmptyCorpus(t *testing.T) {
	c := BuildCorpus(nil)
	if c.N != 0 {
		t.Errorf("empty corpus N should be 0; got %d", c.N)
	}
	if got := c.BM25(0, []string{"test"}); got != 0 {
		t.Errorf("empty corpus should score 0; got %f", got)
	}
}

func TestBM25_EmptyQuery(t *testing.T) {
	c := BuildCorpus([]MemoryEntry{bm25Entry("a", "desc", "body")})
	if got := c.BM25(0, nil); got != 0 {
		t.Errorf("empty query should score 0; got %f", got)
	}
}

func TestBM25_HeadlineBoost(t *testing.T) {
	entries := []MemoryEntry{
		bm25Entry("ripgrep", "search tool", "User prefers ripgrep over grep"),
		bm25Entry("tooling", "general tools", "Author favors ripgrep when searching"),
	}
	c := BuildCorpus(entries)
	q := stemTokenize("ripgrep")

	headlineScore := c.BM25(0, q)
	bodyOnlyScore := c.BM25(1, q)
	if headlineScore <= bodyOnlyScore {
		t.Errorf("headline match should outscore body-only: headline=%.4f body=%.4f",
			headlineScore, bodyOnlyScore)
	}
}

func TestBM25_IDFWeighting(t *testing.T) {
	// Both target terms appear in body only (not headlines) so the
	// comparison isolates IDF. "common" appears in 2/3 docs;
	// "rare" appears in 1/3 — rare should get a higher IDF score.
	entries := []MemoryEntry{
		bm25Entry("doc-a", "desc", "common word here"),
		bm25Entry("doc-b", "desc", "common word also"),
		bm25Entry("doc-c", "desc", "rare word here"),
	}
	c := BuildCorpus(entries)

	rareScore := c.BM25(2, stemTokenize("rare"))
	commonScore := c.BM25(0, stemTokenize("common"))
	if rareScore <= commonScore {
		t.Errorf("rare term should score higher via IDF: rare=%.4f common=%.4f",
			rareScore, commonScore)
	}
}

func TestBM25_TermSaturation(t *testing.T) {
	single := bm25Entry("a", "desc", "needle in a haystack")
	repeated := bm25Entry("b", "desc", "needle needle needle needle needle needle needle needle needle needle")
	c := BuildCorpus([]MemoryEntry{single, repeated})
	q := stemTokenize("needle")

	s1 := c.BM25(0, q)
	s2 := c.BM25(1, q)
	// Repeated mentions should score higher but not 10x higher (saturation).
	ratio := s2 / s1
	if ratio > 5.0 {
		t.Errorf("BM25 should saturate term frequency: ratio=%.2f (single=%.4f repeated=%.4f)",
			ratio, s1, s2)
	}
}

func TestBM25_NoOverlapZero(t *testing.T) {
	c := BuildCorpus([]MemoryEntry{bm25Entry("alpha", "desc", "completely different words")})
	q := stemTokenize("xyzzy")
	if got := c.BM25(0, q); got != 0 {
		t.Errorf("no overlap should score 0; got %f", got)
	}
}

func TestBM25_Rank_Order(t *testing.T) {
	entries := []MemoryEntry{
		bm25Entry("irrelevant", "no match", "kubernetes cluster details"),
		bm25Entry("ripgrep-prefs", "search preferences", "user prefers ripgrep"),
		bm25Entry("grep-tools", "grep tooling", "ripgrep and grep comparison"),
	}
	c := BuildCorpus(entries)
	ranked := c.Rank(stemTokenize("ripgrep search"))

	if ranked[0].Entry.Name != "ripgrep-prefs" {
		t.Errorf("best match should be ripgrep-prefs; got %s", ranked[0].Entry.Name)
	}
	if ranked[len(ranked)-1].Score != 0 {
		// "irrelevant" has no overlap
		if ranked[len(ranked)-1].Entry.Name != "irrelevant" {
			t.Errorf("worst match should be irrelevant; got %s", ranked[len(ranked)-1].Entry.Name)
		}
	}
}

func TestBM25_Rank_TieBreakAlphabetical(t *testing.T) {
	entries := []MemoryEntry{
		bm25Entry("charlie", "desc", "needle"),
		bm25Entry("alpha", "desc", "needle"),
		bm25Entry("bravo", "desc", "needle"),
	}
	c := BuildCorpus(entries)
	ranked := c.Rank(stemTokenize("needle"))
	got := []string{ranked[0].Entry.Name, ranked[1].Entry.Name, ranked[2].Entry.Name}
	want := []string{"alpha", "bravo", "charlie"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("tie-break order = %v, want %v", got, want)
			break
		}
	}
}

func TestBM25_StemmedMatching(t *testing.T) {
	entries := []MemoryEntry{
		bm25Entry("testing-prefs", "test preferences", "prefer integration tests over mocks"),
	}
	c := BuildCorpus(entries)

	// "testing" stems to "test", should match "tests" (also stems to "test")
	q := stemTokenize("testing patterns")
	if got := c.BM25(0, q); got == 0 {
		t.Errorf("stemmed query 'testing' should match doc with 'tests'; got 0")
	}
}

func TestCorpus_DocumentFrequencies(t *testing.T) {
	entries := []MemoryEntry{
		bm25Entry("a", "test desc", "test body"),
		bm25Entry("b", "other desc", "test content"),
		bm25Entry("c", "unrelated", "no match"),
	}
	c := BuildCorpus(entries)
	testDF := c.DF[Stem("test")]
	if testDF != 2 {
		t.Errorf("DF for 'test' should be 2; got %d", testDF)
	}
}

func TestCorpus_AverageDocLength(t *testing.T) {
	entries := []MemoryEntry{
		bm25Entry("a", "", "word"),
		bm25Entry("b", "", "word word word"),
	}
	c := BuildCorpus(entries)
	if c.AvgDL <= 0 {
		t.Errorf("AvgDL should be positive; got %f", c.AvgDL)
	}
}
