package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCachedCorpus_HitAndInvalidate(t *testing.T) {
	resetCorpusCache()
	entries := []MemoryEntry{
		{Name: "a", Type: "user", Body: "alpha body about testing"},
		{Name: "b", Type: "reference", Body: "beta body about kubernetes"},
	}
	c1 := cachedCorpus(entries)
	if c1 != cachedCorpus(entries) {
		t.Error("same entry set should reuse the cached corpus instance")
	}
	// Identical content in a fresh slice must still hit (content fingerprint,
	// not slice identity).
	dup := []MemoryEntry{
		{Name: "a", Type: "user", Body: "alpha body about testing"},
		{Name: "b", Type: "reference", Body: "beta body about kubernetes"},
	}
	if cachedCorpus(dup) != c1 {
		t.Error("identical content should hit the cache regardless of slice identity")
	}
	// Changing a body must invalidate.
	entries[0].Body = "alpha body about testing CHANGED"
	if cachedCorpus(entries) == c1 {
		t.Error("changing a body must rebuild the corpus")
	}
	// Adding an entry must invalidate (count is part of the fingerprint).
	resetCorpusCache()
	base := cachedCorpus(entries)
	more := append([]MemoryEntry{}, entries...)
	more = append(more, MemoryEntry{Name: "c", Type: "user", Body: "gamma"})
	if cachedCorpus(more) == base {
		t.Error("adding an entry must rebuild the corpus")
	}
}

// TestCachedCorpus_DistinguishesScopeAndPath guards the regression where
// the fingerprint hashed only content (name/type/description/body): two
// byte-identical memories differing only in Scope/Path then collided, so
// a cache hit returned the wrong-scope entry — silently dropping a
// project memory from injection and loading the wrong scope's .vec. The
// fingerprint must distinguish Scope and Path.
func TestCachedCorpus_DistinguishesScopeAndPath(t *testing.T) {
	resetCorpusCache()
	user := []MemoryEntry{{Scope: "user", Path: "/u/dup.md", Name: "dup", Type: "feedback", Description: "d", Body: "same body content"}}
	project := []MemoryEntry{{Scope: "project", Path: "/p/dup.md", Name: "dup", Type: "feedback", Description: "d", Body: "same body content"}}

	cu := cachedCorpus(user)    // prime with the user-scope twin
	cp := cachedCorpus(project) // byte-identical content, different scope/path
	if cu == cp {
		t.Fatal("byte-identical content in different scopes must not share a cached corpus")
	}
	if cp.Docs[0].Entry.Scope != "project" || cp.Docs[0].Entry.Path != "/p/dup.md" {
		t.Errorf("cache returned a stale entry: scope=%q path=%q (want project, /p/dup.md)",
			cp.Docs[0].Entry.Scope, cp.Docs[0].Entry.Path)
	}
}

func TestReadVecMetaCached_StatValidated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.vec")

	// Missing file → (nil, zero, nil), matching ReadVecMeta.
	if vec, _, err := readVecMetaCached(path); err != nil || vec != nil {
		t.Fatalf("missing vec: vec=%v err=%v", vec, err)
	}

	if err := WriteVecWithModel(path, []float32{1, 2, 3}, "m1"); err != nil {
		t.Fatal(err)
	}
	vec, meta, err := readVecMetaCached(path)
	if err != nil || len(vec) != 3 || meta.Model != "m1" {
		t.Fatalf("first read: vec=%v meta=%+v err=%v", vec, meta, err)
	}
	// Cached read returns the same content.
	if v2, _, _ := readVecMetaCached(path); len(v2) != 3 || v2[0] != 1 {
		t.Fatalf("cached read mismatch: %v", v2)
	}

	// Rewrite with the SAME size + model but different content, and bump
	// mtime so the mtime+size validation detects the change (size alone
	// wouldn't here).
	if err := WriteVecWithModel(path, []float32{4, 5, 6}, "m1"); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	v3, _, _ := readVecMetaCached(path)
	if len(v3) != 3 || v3[0] != 4 {
		t.Errorf("mtime change should invalidate the vec cache; got %v", v3)
	}

	// Deleting the file → cache cleared, returns nil.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if v4, _, err := readVecMetaCached(path); err != nil || v4 != nil {
		t.Errorf("removed vec should return nil; got vec=%v err=%v", v4, err)
	}
}

// TestCachedCorpus_RankingEquivalence guards that caching does not change
// rankings: a cached run and a fresh-built run produce the same order.
func TestCachedCorpus_RankingEquivalence(t *testing.T) {
	entries := []MemoryEntry{
		entry("test-prefs", "prefer integration tests over mocks"),
		entry("db-config", "database connection settings"),
		entry("ui-style", "no emoji in the interface"),
	}
	q := StemExpandTokenize("testing database preferences")

	resetCorpusCache()
	cached := cachedCorpus(entries).Rank(q)
	fresh := BuildCorpus(entries).Rank(q)

	if len(cached) != len(fresh) {
		t.Fatalf("len mismatch: cached=%d fresh=%d", len(cached), len(fresh))
	}
	for i := range cached {
		if cached[i].Entry.Name != fresh[i].Entry.Name || cached[i].Score != fresh[i].Score {
			t.Errorf("ranking diverged at %d: cached=%+v fresh=%+v", i, cached[i], fresh[i])
		}
	}
}
