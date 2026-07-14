package recall

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

// fakeEmbedder produces a deterministic bag-of-keywords vector: dimension i is
// the count of vocab[i] as a substring of the (lowercased) text. Texts that
// share keywords get non-zero cosine similarity, so ranking is meaningful and
// reproducible without a real embedding model.
type fakeEmbedder struct{ vocab []string }

func (f fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, len(f.vocab))
	lower := strings.ToLower(text)
	for i, w := range f.vocab {
		v[i] = float32(strings.Count(lower, w))
	}
	return v, nil
}

var testVocab = []string{"auth", "jwt", "docker", "kubernetes"}

func testEmbedder() fakeEmbedder { return fakeEmbedder{vocab: testVocab} }

func mustEmbed(t *testing.T, text string) []float32 {
	t.Helper()
	v, err := testEmbedder().Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("embed %q: %v", text, err)
	}
	return v
}

// indexSessionAt indexes a session with the given id, cwd, and message bodies.
func indexSessionAt(t *testing.T, idx *Index, id, cwd string, contents ...string) {
	t.Helper()
	s := &session.Session{ID: id, Model: "test-model", Created: time.Now(), Cwd: cwd}
	for i, body := range contents {
		role := adapter.RoleUser
		if i%2 == 1 {
			role = adapter.RoleAssistant
		}
		s.Messages = append(s.Messages, adapter.Message{Role: role, Content: body})
	}
	if err := idx.IndexSession(s); err != nil {
		t.Fatalf("IndexSession %s: %v", id, err)
	}
}

func TestEncodeDecodeVec_Roundtrip(t *testing.T) {
	in := []float32{0, 1, -2.5, 3.14159, 1e-7}
	got := decodeVec(encodeVec(in))
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("elem %d = %v, want %v", i, got[i], in[i])
		}
	}
}

func TestCosine(t *testing.T) {
	if c := cosine([]float32{1, 0}, []float32{1, 0}); math.Abs(c-1) > 1e-9 {
		t.Errorf("identical cosine = %v, want 1", c)
	}
	if c := cosine([]float32{1, 0}, []float32{0, 1}); c != 0 {
		t.Errorf("orthogonal cosine = %v, want 0", c)
	}
	if c := cosine([]float32{1, 2, 3}, []float32{1, 2}); c != 0 {
		t.Errorf("mismatched dims cosine = %v, want 0", c)
	}
}

func TestBackfillVectors_EmbedsAllAndIsIdempotent(t *testing.T) {
	idx := newIndex(t)
	indexSessionAt(t, idx, "s1", "/proj", "how do I handle auth", "use jwt tokens")

	pending, err := idx.UnvectoredMessages("test-model")
	if err != nil {
		t.Fatalf("UnvectoredMessages: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("before backfill: %d unvectored, want 2", len(pending))
	}

	if err := idx.BackfillVectors(context.Background(), testEmbedder(), "test-model"); err != nil {
		t.Fatalf("BackfillVectors: %v", err)
	}

	pending, err = idx.UnvectoredMessages("test-model")
	if err != nil {
		t.Fatalf("UnvectoredMessages after: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("after backfill: %d unvectored, want 0", len(pending))
	}

	// A second pass must re-embed nothing.
	countingEmbedder := &countingEmbedder{inner: testEmbedder()}
	if err := idx.BackfillVectors(context.Background(), countingEmbedder, "test-model"); err != nil {
		t.Fatalf("second BackfillVectors: %v", err)
	}
	if countingEmbedder.calls != 0 {
		t.Errorf("idempotent backfill embedded %d messages, want 0", countingEmbedder.calls)
	}
}

type countingEmbedder struct {
	inner fakeEmbedder
	calls int
}

func (c *countingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	c.calls++
	return c.inner.Embed(ctx, text)
}

func TestBackfillVectorsForSession_OnlyEmbedsThatSession(t *testing.T) {
	idx := newIndex(t)
	indexSessionAt(t, idx, "s1", "/proj", "auth one", "jwt two")
	indexSessionAt(t, idx, "s2", "/proj", "docker three", "kube four")

	if err := idx.BackfillVectorsForSession(context.Background(), testEmbedder(), "test-model", "s1"); err != nil {
		t.Fatalf("BackfillVectorsForSession: %v", err)
	}

	// s1 fully embedded; s2 still entirely pending.
	pending, err := idx.UnvectoredMessages("test-model")
	if err != nil {
		t.Fatalf("UnvectoredMessages: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("after scoped backfill: %d pending, want 2 (only s2)", len(pending))
	}
	for _, p := range pending {
		if p.SessionID != "s2" {
			t.Errorf("pending message from %s, want only s2", p.SessionID)
		}
	}

	// Empty session id is a no-op (guards against embedding the whole store).
	if err := idx.BackfillVectorsForSession(context.Background(), testEmbedder(), "test-model", ""); err != nil {
		t.Fatalf("empty-session backfill: %v", err)
	}
	if pending, _ := idx.UnvectoredMessages("test-model"); len(pending) != 2 {
		t.Errorf("empty session id should be a no-op; got %d pending, want 2", len(pending))
	}
}

func TestUnvectoredMessages_DetectsModelAndContentChange(t *testing.T) {
	idx := newIndex(t)
	indexSessionAt(t, idx, "s1", "/proj", "auth question", "jwt answer")
	if err := idx.BackfillVectors(context.Background(), testEmbedder(), "model-A"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Different model => everything is stale (incompatible vector space).
	if pending, _ := idx.UnvectoredMessages("model-B"); len(pending) != 2 {
		t.Errorf("model change: %d pending, want 2", len(pending))
	}

	// Re-index the same session with changed content at index 0; that message
	// (and only it) must now be reported stale for the same model.
	indexSessionAt(t, idx, "s1", "/proj", "docker question", "jwt answer")
	pending, err := idx.UnvectoredMessages("model-A")
	if err != nil {
		t.Fatalf("UnvectoredMessages: %v", err)
	}
	if len(pending) != 1 || pending[0].MsgIndex != 0 {
		t.Fatalf("content change: pending = %+v, want only msg_index 0", pending)
	}
}

func TestSearchSemantic_RanksByCosineAndFiltersMinScore(t *testing.T) {
	idx := newIndex(t)
	indexSessionAt(t, idx, "s-auth", "/proj", "everything about jwt auth tokens")
	indexSessionAt(t, idx, "s-docker", "/proj", "everything about docker and kubernetes")
	if err := idx.BackfillVectors(context.Background(), testEmbedder(), "test-model"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	q := mustEmbed(t, "auth jwt")
	hits, err := idx.SearchSemantic(q, SemanticSearchOpts{
		Model: "test-model", Scope: ScopeAll, Limit: 10, MinScore: 0.1,
	})
	if err != nil {
		t.Fatalf("SearchSemantic: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 (docker session below min_score)", len(hits))
	}
	if hits[0].SessionID != "s-auth" {
		t.Errorf("top hit = %q, want s-auth", hits[0].SessionID)
	}
	if hits[0].Score <= 0.1 {
		t.Errorf("top score = %v, want > min_score", hits[0].Score)
	}
}

func TestSearchSemantic_ProjectScopeAndExcludeSession(t *testing.T) {
	idx := newIndex(t)
	indexSessionAt(t, idx, "s-here", "/proj-A", "auth jwt discussion")
	indexSessionAt(t, idx, "s-there", "/proj-B", "auth jwt discussion")
	indexSessionAt(t, idx, "s-live", "/proj-A", "auth jwt discussion")
	if err := idx.BackfillVectors(context.Background(), testEmbedder(), "test-model"); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	q := mustEmbed(t, "auth jwt")

	// Project scope pinned to /proj-A, excluding the live session, must return
	// only s-here (s-there is another project, s-live is excluded).
	hits, err := idx.SearchSemantic(q, SemanticSearchOpts{
		Model: "test-model", Scope: ScopeProject, Cwd: "/proj-A",
		ExcludeSession: "s-live", Limit: 10, MinScore: 0.1,
	})
	if err != nil {
		t.Fatalf("SearchSemantic: %v", err)
	}
	if len(hits) != 1 || hits[0].SessionID != "s-here" {
		t.Fatalf("project+exclude scope hits = %+v, want only s-here", sessionIDs(hits))
	}

	// Widening to all-scope (still excluding live) now also surfaces s-there.
	all, err := idx.SearchSemantic(q, SemanticSearchOpts{
		Model: "test-model", Scope: ScopeAll, ExcludeSession: "s-live", Limit: 10, MinScore: 0.1,
	})
	if err != nil {
		t.Fatalf("SearchSemantic all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all scope hits = %+v, want s-here and s-there", sessionIDs(all))
	}
}

func sessionIDs(hits []ScoredHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.SessionID
	}
	return out
}

func TestExcerpt_CollapsesAndTruncates(t *testing.T) {
	got := excerpt("line one\n\nline   two\twith\ttabs")
	if strings.ContainsAny(got, "\n\t") {
		t.Errorf("excerpt still has control whitespace: %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("excerpt has doubled spaces: %q", got)
	}

	long := strings.Repeat("word ", 200)
	if trunc := excerpt(long); len([]rune(trunc)) > excerptMaxRunes+1 {
		t.Errorf("excerpt len = %d runes, want <= %d", len([]rune(trunc)), excerptMaxRunes+1)
	}
}
