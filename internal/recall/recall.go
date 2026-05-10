// Package recall is a tiny full-text index over saved sessions. It powers
// the /recall slash command — search across every conversation you've had
// with yottacode, ranked by FTS5 relevance.
//
// The index lives at ~/.yottacode/index.sqlite and is rebuilt incrementally
// whenever a session is saved. At startup the TUI re-indexes every session
// in a background goroutine so historical sessions become searchable.
package recall

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,
    name         TEXT,
    model        TEXT,
    cwd          TEXT,
    created      INTEGER,
    last_indexed INTEGER
);

CREATE VIRTUAL TABLE IF NOT EXISTS messages USING fts5(
    session_id UNINDEXED,
    msg_index  UNINDEXED,
    role       UNINDEXED,
    content,
    tokenize = 'porter unicode61'
);
`

// Index is the writable handle on the FTS5 database. Safe to share across
// goroutines because database/sql serializes operations.
type Index struct {
	db *sql.DB
}

// Open returns the index living at ~/.yottacode/index.sqlite, creating the
// directory and schema as needed.
func Open() (*Index, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".yottacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return openAt(filepath.Join(dir, "index.sqlite"))
}

// openAt is the testable constructor — a custom path lets tests redirect
// the index to t.TempDir() without HOME shenanigans.
func openAt(path string) (*Index, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recall: ensure schema: %w", err)
	}
	return &Index{db: db}, nil
}

// Close releases the underlying database handle. It's fine to ignore the
// error during shutdown.
func (idx *Index) Close() error { return idx.db.Close() }

// IndexSession upserts every user/assistant message body for the given
// session. Replaces any prior FTS rows for that session id, so re-indexing
// after a turn is correct (and cheap — sessions are at most a few KB).
func (idx *Index) IndexSession(s *session.Session) error {
	if s == nil || s.ID == "" {
		return errors.New("recall: nil or empty-id session")
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`
		INSERT INTO sessions(id, name, model, cwd, created, last_indexed)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			model = excluded.model,
			cwd = excluded.cwd,
			last_indexed = excluded.last_indexed`,
		s.ID, s.Name, s.Model, s.Cwd, s.Created.Unix(), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("recall: upsert session: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, s.ID); err != nil {
		return fmt.Errorf("recall: clear prior messages: %w", err)
	}

	for i, msg := range s.Messages {
		if msg.Role != adapter.RoleUser && msg.Role != adapter.RoleAssistant {
			continue
		}
		if msg.Content == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO messages(session_id, msg_index, role, content) VALUES (?, ?, ?, ?)`,
			s.ID, i, string(msg.Role), msg.Content,
		); err != nil {
			return fmt.Errorf("recall: insert message %d: %w", i, err)
		}
	}
	return tx.Commit()
}

// Hit is one search result: the message that matched plus the surrounding
// session metadata. Snippet contains FTS5-highlighted match context with
// `[…]` brackets around the matched terms.
type Hit struct {
	SessionID   string
	SessionName string
	Model       string
	Created     time.Time
	Role        adapter.Role
	MsgIndex    int
	Snippet     string
}

// Search runs an FTS5 MATCH query and returns up to `limit` hits sorted by
// rank (most relevant first). limit defaults to 10 when ≤ 0.
//
// The first attempt uses the raw query so power users can still write
// FTS5-native expressions ("auth OR jwt", "\"exact phrase\"", etc.). On a
// syntax failure we retry with a sanitized version so naive inputs like
// "nothing-matches-this" — which FTS5 reads as a NOT operator — still
// produce a clean empty result instead of a SQL error.
func (idx *Index) Search(query string, limit int) ([]Hit, error) {
	if query == "" {
		return nil, errors.New("recall: empty query")
	}
	if limit <= 0 {
		limit = 10
	}
	hits, err := idx.searchRaw(query, limit)
	if err == nil {
		return hits, nil
	}
	sanitized := sanitizeFTS5(query)
	if sanitized == "" || sanitized == query {
		return nil, fmt.Errorf("recall: search: %w", err)
	}
	return idx.searchRaw(sanitized, limit)
}

// sanitizeFTS5 strips characters that have meaning in FTS5 query syntax
// (operators, quotes, column scoping). Replaces them with spaces so the
// remaining tokens still match.
func sanitizeFTS5(q string) string {
	var b strings.Builder
	for _, r := range q {
		switch r {
		case '"', '(', ')', '*', '+', '-', ':', '!', '^', '\'':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func (idx *Index) searchRaw(query string, limit int) ([]Hit, error) {
	rows, err := idx.db.Query(`
		SELECT
			m.session_id,
			m.msg_index,
			m.role,
			snippet(messages, 3, '[', ']', '…', 16),
			s.name,
			s.model,
			s.created
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE messages MATCH ?
		ORDER BY rank
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var (
			h           Hit
			roleStr     string
			createdUnix int64
			name        sql.NullString
			model       sql.NullString
		)
		if err := rows.Scan(&h.SessionID, &h.MsgIndex, &roleStr, &h.Snippet, &name, &model, &createdUnix); err != nil {
			return nil, err
		}
		if name.Valid {
			h.SessionName = name.String
		}
		if model.Valid {
			h.Model = model.String
		}
		h.Role = adapter.Role(roleStr)
		h.Created = time.Unix(createdUnix, 0)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// MustOpenForTest opens a fresh in-temp-dir index for use from another
// package's tests. Panics on failure (which would mean the test fixture
// itself is broken). Lives in the production file so it's exported, but
// is only intended to be called from *_test.go.
func MustOpenForTest(t interface{ TempDir() string; Fatalf(string, ...any) }) *Index {
	idx, err := openAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("recall.MustOpenForTest: %v", err)
	}
	return idx
}

// Backfill walks every saved session via session.List + session.Load and
// re-indexes them. Cheap when the corpus is small (a few dozen sessions);
// expected to run as a background goroutine at TUI startup.
func Backfill(idx *Index) error {
	infos, err := session.List()
	if err != nil {
		return err
	}
	for _, info := range infos {
		s, err := session.Load(info.ID)
		if err != nil {
			continue // skip corrupted/unreadable sessions silently
		}
		if err := idx.IndexSession(s); err != nil {
			return fmt.Errorf("recall: backfill %s: %w", info.ID, err)
		}
	}
	return nil
}
