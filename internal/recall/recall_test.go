package recall

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

// newIndex returns an Index over a fresh in-temp-dir SQLite file.
func newIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := openAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("openAt: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

// fakeSession returns a Session populated with the given user/assistant
// content pairs. Order in the slice maps to msg_index in the FTS table.
func fakeSession(id string, contents ...string) *session.Session {
	s := &session.Session{
		ID:      id,
		Model:   "qwen3.5",
		Created: time.Now(),
		Cwd:     "/tmp",
	}
	for i, body := range contents {
		role := adapter.RoleUser
		if i%2 == 1 {
			role = adapter.RoleAssistant
		}
		s.Messages = append(s.Messages, adapter.Message{Role: role, Content: body})
	}
	return s
}

func TestIndex_OpenCreatesSchema(t *testing.T) {
	idx := newIndex(t)
	// Tables exist if a Search on an empty corpus succeeds without erroring.
	hits, err := idx.Search("anything", 5)
	if err != nil {
		t.Fatalf("Search on empty index: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("empty index should yield no hits; got %d", len(hits))
	}
}

func TestIndex_FindsSimpleWord(t *testing.T) {
	idx := newIndex(t)
	s := fakeSession("sess-001", "how do I handle authentication", "use JWT tokens")
	if err := idx.IndexSession(s); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	hits, err := idx.Search("authentication", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit")
	}
	if hits[0].SessionID != "sess-001" {
		t.Errorf("hit session id = %q, want sess-001", hits[0].SessionID)
	}
	if !strings.Contains(strings.ToLower(hits[0].Snippet), "authentication") {
		t.Errorf("snippet should contain matched term: %q", hits[0].Snippet)
	}
}

func TestIndex_RankingPlacesBestMatchFirst(t *testing.T) {
	idx := newIndex(t)
	idx.IndexSession(fakeSession("a", "totally unrelated content about cats"))
	idx.IndexSession(fakeSession("b", "auth auth auth: how to authenticate users using auth"))
	idx.IndexSession(fakeSession("c", "the word auth appears once"))

	hits, err := idx.Search("auth", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected ≥2 hits; got %d", len(hits))
	}
	if hits[0].SessionID != "b" {
		t.Errorf("most-relevant should be 'b' (densest 'auth' usage); got %q", hits[0].SessionID)
	}
}

func TestIndex_ReindexReplacesPriorRows(t *testing.T) {
	idx := newIndex(t)
	s := fakeSession("evolves", "I am trying to debug the auth flow")
	idx.IndexSession(s)

	// Now mutate the session — the user edited their first message.
	s.Messages[0].Content = "I am writing a poem about spring"
	idx.IndexSession(s)

	authHits, _ := idx.Search("auth", 10)
	if len(authHits) != 0 {
		t.Errorf("old content should be evicted; got %d hits", len(authHits))
	}
	springHits, _ := idx.Search("spring", 10)
	if len(springHits) == 0 {
		t.Errorf("new content should be indexed")
	}
}

func TestIndex_SkipsSystemAndToolMessages(t *testing.T) {
	idx := newIndex(t)
	s := &session.Session{
		ID:      "mixed",
		Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleSystem, Content: "you are helpful"},
			{Role: adapter.RoleUser, Content: "hello"},
			{Role: adapter.RoleAssistant, Content: "hi there"},
			{Role: adapter.RoleTool, Content: "tool output blob"},
		},
	}
	if err := idx.IndexSession(s); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	if hits, _ := idx.Search("helpful", 10); len(hits) != 0 {
		t.Errorf("system messages should not be indexed; got %d hits", len(hits))
	}
	if hits, _ := idx.Search("blob", 10); len(hits) != 0 {
		t.Errorf("tool output should not be indexed; got %d hits", len(hits))
	}
	if hits, _ := idx.Search("hello", 10); len(hits) == 0 {
		t.Errorf("user message should be indexed")
	}
}

func TestIndex_HitMetadataPopulated(t *testing.T) {
	idx := newIndex(t)
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	s := &session.Session{
		ID:      "meta-test",
		Name:    "auth-investigation",
		Model:   "qwen3.5",
		Cwd:     "/proj",
		Created: now,
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "investigating auth bug"},
		},
	}
	idx.IndexSession(s)
	hits, err := idx.Search("auth", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("expected hit; got err=%v len=%d", err, len(hits))
	}
	h := hits[0]
	if h.SessionName != "auth-investigation" {
		t.Errorf("SessionName = %q", h.SessionName)
	}
	if h.Model != "qwen3.5" {
		t.Errorf("Model = %q", h.Model)
	}
	if !h.Created.Equal(now) && h.Created.Unix() != now.Unix() {
		t.Errorf("Created = %v, want %v", h.Created, now)
	}
	if h.Role != adapter.RoleUser {
		t.Errorf("Role = %q", h.Role)
	}
}

func TestIndex_EmptyQueryReturnsError(t *testing.T) {
	idx := newIndex(t)
	if _, err := idx.Search("", 10); err == nil {
		t.Errorf("empty query should error")
	}
}

func TestIndex_NilSessionReturnsError(t *testing.T) {
	idx := newIndex(t)
	if err := idx.IndexSession(nil); err == nil {
		t.Errorf("nil session should error")
	}
}

func TestIndex_LimitRespected(t *testing.T) {
	idx := newIndex(t)
	for i := 0; i < 5; i++ {
		idx.IndexSession(fakeSession(string(rune('a'+i)), "needle in haystack number "+string(rune('0'+i))))
	}
	hits, _ := idx.Search("needle", 3)
	if len(hits) != 3 {
		t.Errorf("limit=3 yielded %d hits", len(hits))
	}
}

func TestBackfill_IndexesEverySession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	a, _ := session.New("qwen3.5", "/x")
	a.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "first message about elephants"}}
	if err := a.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, _ := session.New("qwen3.5", "/x")
	b.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "second message about giraffes"}}
	if err := b.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	idx, err := openAt(filepath.Join(tmp, "ix.sqlite"))
	if err != nil {
		t.Fatalf("openAt: %v", err)
	}
	defer idx.Close()
	if err := Backfill(idx); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if hits, _ := idx.Search("elephants", 10); len(hits) == 0 {
		t.Errorf("session a not indexed by backfill")
	}
	if hits, _ := idx.Search("giraffes", 10); len(hits) == 0 {
		t.Errorf("session b not indexed by backfill")
	}
}

// Deleting a session's JSON must eventually evict it from the index too —
// otherwise /recall and semantic search keep surfacing a conversation whose
// transcript is gone, and the rows accumulate forever.
func TestBackfill_PrunesDeletedSessions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	keep, _ := session.New("qwen3.5", "/x")
	keep.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "message about elephants"}}
	if err := keep.Save(); err != nil {
		t.Fatalf("Save keep: %v", err)
	}
	gone, _ := session.New("qwen3.5", "/x")
	gone.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "message about giraffes"}}
	if err := gone.Save(); err != nil {
		t.Fatalf("Save gone: %v", err)
	}

	idx, err := openAt(filepath.Join(tmp, "ix.sqlite"))
	if err != nil {
		t.Fatalf("openAt: %v", err)
	}
	defer idx.Close()
	if err := Backfill(idx); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	embedAll(t, idx)
	if hits, _ := idx.Search("giraffes", 10); len(hits) == 0 {
		t.Fatal("precondition: both sessions should be indexed")
	}
	if got := vectorIndices(t, idx, gone.ID); len(got) == 0 {
		t.Fatal("precondition: deleted-to-be session should have vectors")
	}

	// The user deletes the session file.
	if err := os.Remove(filepath.Join(tmp, ".yottacode", "sessions", gone.ID+".json")); err != nil {
		t.Fatalf("remove session file: %v", err)
	}
	if err := Backfill(idx); err != nil {
		t.Fatalf("Backfill after delete: %v", err)
	}

	if hits, _ := idx.Search("giraffes", 10); len(hits) != 0 {
		t.Errorf("deleted session still searchable: %+v", hits)
	}
	if got := vectorIndices(t, idx, gone.ID); len(got) != 0 {
		t.Errorf("deleted session left %v vector rows behind", got)
	}
	var n int
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, gone.ID).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 0 {
		t.Errorf("deleted session row survived in sessions table")
	}

	// The surviving session is untouched.
	if hits, _ := idx.Search("elephants", 10); len(hits) == 0 {
		t.Errorf("pruning removed the session that still exists")
	}
	if got := vectorIndices(t, idx, keep.ID); len(got) == 0 {
		t.Errorf("pruning removed the surviving session's vectors")
	}
}

// vectorIndices returns the msg_index values that currently have a stored
// vector for the session, ascending. Same package, so it reads idx.db directly.
func vectorIndices(t *testing.T, idx *Index, sessionID string) []int {
	t.Helper()
	rows, err := idx.db.Query(
		`SELECT msg_index FROM message_vectors WHERE session_id = ? ORDER BY msg_index`, sessionID)
	if err != nil {
		t.Fatalf("query vectors: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			t.Fatalf("scan msg_index: %v", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// embedAll vectors every currently-indexed message with the shared test
// embedder, so the orphan-cleanup tests start from a fully-vectored index.
func embedAll(t *testing.T, idx *Index) {
	t.Helper()
	if err := idx.BackfillVectors(context.Background(), testEmbedder(), "test-model"); err != nil {
		t.Fatalf("BackfillVectors: %v", err)
	}
}

// Re-indexing an unchanged session must leave its vectors alone. This is the
// guard for the CAST in indexSessionOnce's orphan cleanup: messages is an FTS5
// virtual table that returns msg_index as TEXT, so without the cast the NOT IN
// matches nothing and every vector is deleted on every turn-end re-index.
func TestIndexSession_ReindexUnchangedKeepsVectors(t *testing.T) {
	idx := newIndex(t)
	s := fakeSession("sess-keep", "auth question", "jwt answer", "docker follow-up")
	if err := idx.IndexSession(s); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	embedAll(t, idx)
	if got := vectorIndices(t, idx, "sess-keep"); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("after initial embed, vector indices = %v, want [0 1 2]", got)
	}

	if err := idx.IndexSession(s); err != nil {
		t.Fatalf("re-IndexSession: %v", err)
	}
	if got := vectorIndices(t, idx, "sess-keep"); !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("re-indexing an unchanged session dropped vectors: got %v, want [0 1 2]", got)
	}
	// The stronger statement: nothing needs re-embedding, so the next backfill
	// is a no-op. A broken cast would surface here as three stale messages.
	refs, err := idx.UnvectoredMessages("test-model")
	if err != nil {
		t.Fatalf("UnvectoredMessages: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("unchanged re-index left %d messages needing re-embedding, want 0", len(refs))
	}
}

func TestIndexSession_DeletesOrphanedVectorsOnShrink(t *testing.T) {
	idx := newIndex(t)
	if err := idx.IndexSession(fakeSession("sess-shrink", "auth", "jwt", "docker")); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	embedAll(t, idx)

	if err := idx.IndexSession(fakeSession("sess-shrink", "auth")); err != nil {
		t.Fatalf("shrink to one: %v", err)
	}
	if got := vectorIndices(t, idx, "sess-shrink"); !slices.Equal(got, []int{0}) {
		t.Errorf("after shrinking to one message, vector indices = %v, want [0]", got)
	}

	if err := idx.IndexSession(fakeSession("sess-shrink")); err != nil {
		t.Fatalf("shrink to zero: %v", err)
	}
	if got := vectorIndices(t, idx, "sess-shrink"); len(got) != 0 {
		t.Errorf("after shrinking to zero messages, vector indices = %v, want none", got)
	}
}

// msg_index is the position in Session.Messages, and system/tool messages are
// skipped — so the indexed set has gaps. Cleanup must match the surviving
// index set, not a count: here one message survives but its index is 1.
func TestIndexSession_OrphanCleanupHandlesSparseIndices(t *testing.T) {
	idx := newIndex(t)
	full := &session.Session{ID: "sess-sparse", Model: "test-model", Created: time.Now(), Cwd: "/tmp"}
	full.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "system preamble"},
		{Role: adapter.RoleUser, Content: "auth question"},
		{Role: adapter.RoleTool, Content: "tool output", ToolCallID: "t1"},
		{Role: adapter.RoleAssistant, Content: "jwt answer"},
	}
	if err := idx.IndexSession(full); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	embedAll(t, idx)
	if got := vectorIndices(t, idx, "sess-sparse"); !slices.Equal(got, []int{1, 3}) {
		t.Fatalf("sparse indexing produced vector indices %v, want [1 3]", got)
	}

	// Drop the trailing assistant turn; the user message at index 1 survives.
	shrunk := &session.Session{ID: "sess-sparse", Model: "test-model", Created: full.Created, Cwd: "/tmp"}
	shrunk.Messages = full.Messages[:3]
	if err := idx.IndexSession(shrunk); err != nil {
		t.Fatalf("re-IndexSession: %v", err)
	}
	// A count-based delete ("drop msg_index >= 1") would wrongly clear this.
	if got := vectorIndices(t, idx, "sess-sparse"); !slices.Equal(got, []int{1}) {
		t.Errorf("sparse orphan cleanup left %v, want [1]", got)
	}
}

func TestIndexSession_OrphanCleanupLeavesOtherSessions(t *testing.T) {
	idx := newIndex(t)
	if err := idx.IndexSession(fakeSession("sess-a", "auth", "jwt")); err != nil {
		t.Fatalf("IndexSession a: %v", err)
	}
	if err := idx.IndexSession(fakeSession("sess-b", "docker", "kubernetes")); err != nil {
		t.Fatalf("IndexSession b: %v", err)
	}
	embedAll(t, idx)

	if err := idx.IndexSession(fakeSession("sess-a")); err != nil {
		t.Fatalf("shrink a: %v", err)
	}
	if got := vectorIndices(t, idx, "sess-a"); len(got) != 0 {
		t.Errorf("sess-a vectors = %v, want none", got)
	}
	if got := vectorIndices(t, idx, "sess-b"); !slices.Equal(got, []int{0, 1}) {
		t.Errorf("shrinking sess-a disturbed sess-b: got %v, want [0 1]", got)
	}
}
