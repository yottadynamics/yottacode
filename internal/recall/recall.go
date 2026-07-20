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
	"sync"
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

CREATE TABLE IF NOT EXISTS message_vectors (
    session_id   TEXT    NOT NULL,
    msg_index    INTEGER NOT NULL,
    model        TEXT    NOT NULL,
    content_hash INTEGER NOT NULL,
    vec          BLOB    NOT NULL,
    PRIMARY KEY (session_id, msg_index)
);
`

const (
	// indexSessionBusyAttempts bounds the retry loop for transient SQLite
	// writer contention. The sleep schedule is short enough for the TUI to
	// stay responsive, but long enough to ride out overlapping turn-end,
	// summarize, and startup backfill writes.
	indexSessionBusyAttempts = 6
	indexSessionBusyDelay    = 50 * time.Millisecond
)

// Index is the writable handle on the FTS5 database. Safe to share across
// goroutines; writes through one Index are serialized, and transient SQLite
// writer contention from other handles/processes is retried.
type Index struct {
	db      *sql.DB
	writeMu sync.Mutex
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
	// SQLite permits only one writer at a time. Keep this handle on a single
	// connection so its PRAGMA settings apply consistently and database/sql
	// cannot start two concurrent writer connections behind this Index.
	db.SetMaxOpenConns(1)
	// WAL mode allows concurrent reads during writes; busy_timeout gives
	// each attempt a short in-driver wait before IndexSession's retry loop
	// decides whether to back off and try again.
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=1000")
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

	// Serialize writes issued through this Index so turn-end saves, summarize
	// completion, and background backfill cannot overlap inside one handle. A
	// separate process or separately-opened handle can still hold SQLite's writer
	// lock, so the retry loop below handles SQLITE_BUSY from outside this mutex.
	idx.writeMu.Lock()
	defer idx.writeMu.Unlock()

	var err error
	for attempt := 0; attempt < indexSessionBusyAttempts; attempt++ {
		err = idx.indexSessionOnce(s)
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * indexSessionBusyDelay)
	}
	return err
}

func (idx *Index) indexSessionOnce(s *session.Session) error {
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

	// Drop vectors whose message no longer exists. The rewrite above is
	// DELETE+INSERT, so a session that shrank — auto-summarize replacing
	// Session.Messages with a synopsis, an edited session file — leaves
	// message_vectors rows with nothing to join. They are inert for search
	// (SearchSemantic inner-joins through messages) but accumulate forever and
	// are re-scanned by every semantic query.
	//
	// msg_index is sparse: system/tool messages and empty bodies are skipped
	// above, so indices have gaps and "delete everything at or past the new
	// message count" would be wrong. Match the surviving index set instead.
	//
	// The CAST is belt-and-braces. FTS5 columns have no declared affinity —
	// each value keeps the storage class it was bound with — and the insert
	// above binds a Go int, so msg_index does come back as INTEGER and the
	// comparison already matches. The cast keeps that true if the insert ever
	// starts binding a string, where an affinity mismatch would silently match
	// nothing and delete *every* vector for the session on each re-index.
	// TestIndexSession_ReindexUnchangedKeepsVectors is what would catch it.
	if _, err := tx.Exec(`
		DELETE FROM message_vectors
		WHERE session_id = ?
		  AND msg_index NOT IN (
		      SELECT CAST(msg_index AS INTEGER) FROM messages WHERE session_id = ?
		  )`, s.ID, s.ID); err != nil {
		return fmt.Errorf("recall: clear orphaned vectors: %w", err)
	}
	return tx.Commit()
}

// isSQLiteBusy recognizes modernc SQLite writer-contention errors after they
// have been wrapped with operation context by IndexSession.
func isSQLiteBusy(err error) bool {
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
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
func MustOpenForTest(t interface {
	TempDir() string
	Fatalf(string, ...any)
}) *Index {
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
	live := make(map[string]bool, len(infos))
	for _, info := range infos {
		live[info.ID] = true
		s, err := session.Load(info.ID)
		if err != nil {
			continue // skip corrupted/unreadable sessions silently
		}
		if err := idx.IndexSession(s); err != nil {
			return fmt.Errorf("recall: backfill %s: %w", info.ID, err)
		}
	}
	// Sessions the user deleted are still in the index: this loop only
	// visits files that exist, so nothing else would ever remove their
	// rows. Left alone they accumulate forever, keep answering /recall and
	// semantic searches with conversations whose transcript is gone, and
	// make every cosine scan wider. Prune them here — the full session list
	// is already in hand, which is the one place that knows what "deleted"
	// means.
	//
	// Note live is built from every info returned above, including ones
	// whose Load failed: a session that is present but temporarily
	// unreadable must not be mistaken for a deleted one and dropped.
	if err := idx.pruneMissingSessions(live); err != nil {
		return fmt.Errorf("recall: prune deleted sessions: %w", err)
	}
	return nil
}

// pruneMissingSessions deletes every indexed row belonging to a session id
// not present in live. Rows are removed from all three tables together,
// under the same write mutex and BUSY retry as IndexSession.
func (idx *Index) pruneMissingSessions(live map[string]bool) error {
	rows, err := idx.db.Query(`SELECT id FROM sessions`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !live[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}

	idx.writeMu.Lock()
	defer idx.writeMu.Unlock()
	for _, id := range stale {
		if err := idx.deleteSessionRows(id); err != nil {
			return err
		}
	}
	return nil
}

// deleteSessionRows removes one session from all three tables in a single
// transaction, retried on transient writer contention. Caller holds writeMu.
func (idx *Index) deleteSessionRows(sessionID string) error {
	var err error
	for attempt := 0; attempt < indexSessionBusyAttempts; attempt++ {
		err = idx.deleteSessionRowsOnce(sessionID)
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * indexSessionBusyDelay)
	}
	return err
}

func (idx *Index) deleteSessionRowsOnce(sessionID string) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM message_vectors WHERE session_id = ?`,
		`DELETE FROM messages WHERE session_id = ?`,
		`DELETE FROM sessions WHERE id = ?`,
	} {
		if _, err := tx.Exec(stmt, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
