package recall

import (
	"path/filepath"
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
