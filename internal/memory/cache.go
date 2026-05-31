package memory

import (
	"hash/fnv"
	"os"
	"strconv"
	"sync"
	"time"
)

// This file holds the per-turn retrieval caches. The retrieval path runs
// on every turn (the TUI re-ranks memory against the new user input) and
// on every memory_search call, but the memory set changes only on
// save/forget. Without caching, each turn re-tokenizes + Porter-stems
// every memory body (BuildCorpus) and reads every .vec sidecar from disk
// (scoreSemantic) — O(corpus) work that dominates at a few hundred
// memories. Both caches below are SELF-INVALIDATING (no mutation hook to
// keep in sync) and preserve rankings exactly — they cache inputs, not
// scores, so a stale entry can never change a result, only force a
// recompute.

// --- BM25 corpus cache -----------------------------------------------------
//
// Keyed by a content fingerprint of the entry set. The fingerprint is a
// byte hash of every entry's headline + body — cheap relative to the
// tokenize+stem it guards (roughly an order of magnitude fewer ops per
// byte, no per-word allocation), and content-based so any edit/save that
// changes a body changes the hash and rebuilds. Single-slot: the hot path
// (per-turn injection) reuses one set turn over turn; an occasional
// memory_search over a different scope subset just forces one rebuild. The
// cached *Corpus is read-only after BuildCorpus, so sharing it across
// concurrent callers (a parallel memory_search and the injection path) is
// safe.
var (
	corpusCacheMu  sync.Mutex
	corpusCacheFP  string
	corpusCacheVal *Corpus
)

func cachedCorpus(entries []MemoryEntry) *Corpus {
	fp := corpusFingerprint(entries)
	corpusCacheMu.Lock()
	defer corpusCacheMu.Unlock()
	if corpusCacheVal != nil && corpusCacheFP == fp {
		return corpusCacheVal
	}
	c := BuildCorpus(entries)
	corpusCacheFP = fp
	corpusCacheVal = c
	return c
}

func corpusFingerprint(entries []MemoryEntry) string {
	h := fnv.New64a()
	sep := []byte{0}
	for _, e := range entries {
		// Scope and Path MUST be in the fingerprint: a cache hit returns
		// the previously-cached entry objects verbatim, and callers
		// depend on Entry.Scope (selectAcrossScopes partitions winners by
		// it) and Entry.Path (scoreSemantic loads the .vec from it). Two
		// sets with identical text but different Scope/Path are NOT
		// interchangeable — omitting these let a stale user-scope entry
		// stand in for its byte-identical project twin, silently dropping
		// the project memory from injection.
		h.Write([]byte(e.Scope))
		h.Write(sep)
		h.Write([]byte(e.Path))
		h.Write(sep)
		h.Write([]byte(e.Name))
		h.Write(sep)
		h.Write([]byte(e.Type))
		h.Write(sep)
		h.Write([]byte(e.Description))
		h.Write(sep)
		h.Write([]byte(e.Body))
		h.Write(sep)
	}
	// Append the count so a trailing empty entry can't collide with a
	// shorter set.
	return strconv.FormatUint(h.Sum64(), 16) + ":" + strconv.Itoa(len(entries))
}

// resetCorpusCache clears the corpus cache. Exposed for tests that need a
// clean slate; production never needs it (the fingerprint self-invalidates).
func resetCorpusCache() {
	corpusCacheMu.Lock()
	corpusCacheFP, corpusCacheVal = "", nil
	corpusCacheMu.Unlock()
}

// --- vector sidecar cache --------------------------------------------------
//
// scoreSemantic reads every entry's .vec to compute cosine. We cache the
// parsed vector keyed by the .vec path and validated by its mtime+size, so
// a steady-state turn does an os.Stat per entry (cheap) instead of a full
// read+parse, and a hit returns the in-memory vector. A re-embed
// (memory_save) or an out-of-process `memory reindex` rewrites the .vec,
// changing mtime/size and invalidating the cached entry. Cached vectors
// are read-only (CosineSimilarity never mutates), so sharing is safe.
type vecCacheEntry struct {
	modTime time.Time
	size    int64
	vec     []float32
	meta    VecMeta
}

var vecCache sync.Map // path string -> vecCacheEntry

// readVecMetaCached is a caching wrapper over ReadVecMeta. It returns
// (nil, VecMeta{}, nil) when the file does not exist, matching ReadVecMeta.
func readVecMetaCached(path string) ([]float32, VecMeta, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			vecCache.Delete(path)
			return nil, VecMeta{}, nil
		}
		// Stat failed for some other reason — fall back to a direct read
		// rather than caching a transient error.
		return ReadVecMeta(path)
	}
	if v, ok := vecCache.Load(path); ok {
		e := v.(vecCacheEntry)
		if e.size == info.Size() && e.modTime.Equal(info.ModTime()) {
			return e.vec, e.meta, nil
		}
	}
	vec, meta, err := ReadVecMeta(path)
	if err != nil {
		return vec, meta, err
	}
	vecCache.Store(path, vecCacheEntry{modTime: info.ModTime(), size: info.Size(), vec: vec, meta: meta})
	return vec, meta, nil
}
