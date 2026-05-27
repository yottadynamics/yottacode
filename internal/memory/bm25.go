package memory

import (
	"math"
	"sort"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75

	headlineBoost = 3.0
)

// Corpus holds precomputed statistics for BM25 scoring. Built once
// per retrieval pass from the current memory set via BuildCorpus.
type Corpus struct {
	N     int
	AvgDL float64
	DF    map[string]int
	Docs  []document
}

type document struct {
	Entry          MemoryEntry
	HeadlineStems  []string
	BodyStems      []string
	HeadlineFreq   map[string]int
	BodyFreq       map[string]int
	Length         int
}

// BuildCorpus tokenizes and stems all entries, computing document
// frequencies and average document length. O(n * avg_tokens).
func BuildCorpus(entries []MemoryEntry) *Corpus {
	c := &Corpus{
		N:    len(entries),
		DF:   make(map[string]int),
		Docs: make([]document, len(entries)),
	}
	if len(entries) == 0 {
		return c
	}

	totalLen := 0
	for i, e := range entries {
		headline := e.Name + " " + e.Type + " " + e.Description
		hStems := stemTokenize(headline)
		bStems := stemTokenize(e.Body)

		d := document{
			Entry:         e,
			HeadlineStems: hStems,
			BodyStems:     bStems,
			HeadlineFreq:  freq(hStems),
			BodyFreq:      freq(bStems),
			Length:        len(hStems) + len(bStems),
		}
		c.Docs[i] = d
		totalLen += d.Length

		seen := make(map[string]struct{}, len(hStems)+len(bStems))
		for _, s := range hStems {
			seen[s] = struct{}{}
		}
		for _, s := range bStems {
			seen[s] = struct{}{}
		}
		for s := range seen {
			c.DF[s]++
		}
	}
	c.AvgDL = float64(totalLen) / float64(c.N)
	return c
}

// BM25 scores query stems against document at docIdx. Returns a
// non-negative score; higher means more relevant. The score combines
// a headline field (boosted) and a body field.
func (c *Corpus) BM25(docIdx int, queryStems []string) float64 {
	if c.N == 0 || len(queryStems) == 0 || docIdx < 0 || docIdx >= len(c.Docs) || c.AvgDL == 0 {
		return 0
	}
	d := c.Docs[docIdx]
	var score float64
	for _, q := range queryStems {
		idf := c.idf(q)
		hTF := float64(d.HeadlineFreq[q])
		bTF := float64(d.BodyFreq[q])
		dl := float64(d.Length)

		hScore := idf * (hTF * (bm25K1 + 1)) / (hTF + bm25K1*(1-bm25B+bm25B*dl/c.AvgDL))
		bScore := idf * (bTF * (bm25K1 + 1)) / (bTF + bm25K1*(1-bm25B+bm25B*dl/c.AvgDL))

		score += hScore*headlineBoost + bScore
	}
	return score
}

func (c *Corpus) idf(term string) float64 {
	df := float64(c.DF[term])
	return math.Log(1 + (float64(c.N)-df+0.5)/(df+0.5))
}

// Rank scores all documents against the query and returns them sorted
// by descending score with alphabetical tie-breaking by entry name.
func (c *Corpus) Rank(queryStems []string) []Scored {
	scored := make([]Scored, c.N)
	for i := range c.Docs {
		scored[i] = Scored{Entry: c.Docs[i].Entry, Score: c.BM25(i, queryStems)}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Entry.Name < scored[j].Entry.Name
	})
	return scored
}

// stemTokenize lowercases, splits, drops stopwords/short tokens, and
// stems each remaining token. Used internally by the corpus builder.
func stemTokenize(s string) []string {
	raw := tokenize(s)
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		out = append(out, Stem(t))
	}
	return out
}

func freq(tokens []string) map[string]int {
	if len(tokens) == 0 {
		return nil
	}
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m
}
