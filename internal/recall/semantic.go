package recall

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// Scope values for SearchSemantic. "project" restricts recall to sessions
// whose cwd matches the caller's; "user"/"all" (and anything else) search the
// whole store. The store is already per-user under ~/.yottacode, so "user" and
// "all" are equivalent today — both are accepted so the config vocabulary in
// the plan stays intact.
const (
	ScopeProject = "project"
	ScopeUser    = "user"
	ScopeAll     = "all"
	ScopeOff     = "off"
)

// Embedder is the narrow slice of memory.EmbedClient that vector backfill
// needs. Kept as an interface here so the recall package doesn't import
// memory (which would risk an import cycle and pull an HTTP client into a
// storage package). *memory.EmbedClient satisfies it.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// MsgRef identifies one indexed message that still needs an embedding.
type MsgRef struct {
	SessionID string
	MsgIndex  int
	Content   string
}

// ScoredHit is a semantic search result: a Hit plus its cosine score against
// the query vector.
type ScoredHit struct {
	Hit
	Score float64
}

// SemanticSearchOpts parameterizes SearchSemantic.
type SemanticSearchOpts struct {
	Model          string // embed model; only vectors from this model are ranked
	Scope          string // ScopeProject | ScopeUser | ScopeAll
	Cwd            string // used when Scope == ScopeProject
	ExcludeSession string // session id to omit (usually the live session)
	Limit          int    // max hits; defaults to 10 when <= 0
	MinScore       float64 // cosine floor; hits below this are dropped
}

// contentHash is a cheap, stable fingerprint of a message body. Stored
// alongside each vector so re-indexing can tell whether the text a vector was
// built from has changed (in which case the vector is stale and re-embedded),
// without diffing full message bodies.
func contentHash(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64())
}

// encodeVec serializes a float32 slice as little-endian bytes for BLOB storage.
func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec is the inverse of encodeVec.
func decodeVec(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// cosine returns the cosine similarity of two equal-length vectors, or 0 for
// mismatched/empty/zero vectors. Duplicated (rather than imported from
// memory) to keep recall dependency-free; it is ten lines of pure math.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// UnvectoredMessages returns indexed messages across all sessions that lack a
// current embedding — see unvectoredMessages for the staleness rules.
func (idx *Index) UnvectoredMessages(model string) ([]MsgRef, error) {
	return idx.unvectoredMessages(model, "")
}

// unvectoredMessages returns indexed messages that lack a current embedding —
// either no vector row, a vector from a different model (incompatible vector
// space), or a vector whose stored content hash no longer matches the message
// body (the text changed since it was embedded). A non-empty sessionID scopes
// the scan to one session (the cheap per-turn incremental path); empty scans
// the whole index (the startup backfill). The corpus is a few thousand rows.
func (idx *Index) unvectoredMessages(model, sessionID string) ([]MsgRef, error) {
	query := `
		SELECT m.session_id, m.msg_index, m.content, v.model, v.content_hash
		FROM messages m
		LEFT JOIN message_vectors v
		  ON v.session_id = m.session_id AND v.msg_index = m.msg_index`
	var args []any
	if sessionID != "" {
		query += " WHERE m.session_id = ?"
		args = append(args, sessionID)
	}
	rows, err := idx.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("recall: scan unvectored: %w", err)
	}
	defer rows.Close()

	var out []MsgRef
	for rows.Next() {
		var (
			ref      MsgRef
			vecModel string
			vecHash  int64
			hasVec   = true
		)
		// v.* are NULL when the LEFT JOIN found no vector row. Scan into
		// nullable holders so a missing vector isn't a scan error.
		var nm nullString
		var nh nullInt64
		if err := rows.Scan(&ref.SessionID, &ref.MsgIndex, &ref.Content, &nm, &nh); err != nil {
			return nil, err
		}
		if !nm.Valid || !nh.Valid {
			hasVec = false
		} else {
			vecModel, vecHash = nm.String, nh.Int64
		}
		if !hasVec || vecModel != model || vecHash != contentHash(ref.Content) {
			out = append(out, ref)
		}
	}
	return out, rows.Err()
}

// PutVector upserts one message embedding, recording the content hash so a
// later re-index can detect if the underlying message text changed. Serialized
// through the write mutex and retried on SQLite writer contention, matching
// IndexSession.
func (idx *Index) PutVector(sessionID string, msgIndex int, model, content string, vec []float32) error {
	idx.writeMu.Lock()
	defer idx.writeMu.Unlock()

	var err error
	for attempt := 0; attempt < indexSessionBusyAttempts; attempt++ {
		_, err = idx.db.Exec(`
			INSERT INTO message_vectors(session_id, msg_index, model, content_hash, vec)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(session_id, msg_index) DO UPDATE SET
				model = excluded.model,
				content_hash = excluded.content_hash,
				vec = excluded.vec`,
			sessionID, msgIndex, model, contentHash(content), encodeVec(vec))
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * indexSessionBusyDelay)
	}
	return err
}

// BackfillVectors embeds every message across all sessions that is missing or
// stale for the given model. Intended to run in a background goroutine at
// startup.
func (idx *Index) BackfillVectors(ctx context.Context, e Embedder, model string) error {
	return idx.backfillVectors(ctx, e, model, "")
}

// BackfillVectorsForSession embeds only the given session's missing/stale
// messages — the cheap incremental path run after each turn, so a conversation
// becomes recallable in later sessions without waiting for the next startup
// backfill. Typically embeds just the turn's new user/assistant messages.
func (idx *Index) BackfillVectorsForSession(ctx context.Context, e Embedder, model, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return idx.backfillVectors(ctx, e, model, sessionID)
}

// backfillVectors embeds each message that unvectoredMessages reports as
// missing/stale (optionally scoped to sessionID), writing each vector as it
// goes. ctx cancellation stops it promptly. A single embed failure (e.g. Ollama
// went away mid-run) ends the pass rather than hammering a down server — the
// next backfill resumes where this left off, since already-written vectors are
// skipped.
func (idx *Index) backfillVectors(ctx context.Context, e Embedder, model, sessionID string) error {
	if e == nil || model == "" {
		return nil
	}
	refs, err := idx.unvectoredMessages(model, sessionID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		vec, err := e.Embed(ctx, ref.Content)
		if err != nil {
			return fmt.Errorf("recall: backfill embed %s#%d: %w", ref.SessionID, ref.MsgIndex, err)
		}
		if err := idx.PutVector(ref.SessionID, ref.MsgIndex, model, ref.Content, vec); err != nil {
			return err
		}
	}
	return nil
}

// SearchSemantic ranks stored message embeddings by cosine similarity to
// queryVec and returns the top hits at or above opts.MinScore. Only vectors
// produced by opts.Model are considered — a vector from another embedding
// model lives in a different space and its cosine would be noise. Project
// scope restricts to sessions sharing opts.Cwd; the live session is excluded
// via opts.ExcludeSession.
func (idx *Index) SearchSemantic(queryVec []float32, opts SemanticSearchOpts) ([]ScoredHit, error) {
	if len(queryVec) == 0 {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	where := "v.model = ?"
	args := []any{opts.Model}
	if opts.Scope == ScopeProject && opts.Cwd != "" {
		where += " AND s.cwd = ?"
		args = append(args, opts.Cwd)
	}
	if opts.ExcludeSession != "" {
		where += " AND m.session_id != ?"
		args = append(args, opts.ExcludeSession)
	}

	rows, err := idx.db.Query(`
		SELECT m.session_id, m.msg_index, m.role, m.content, s.name, s.model, s.created, v.vec
		FROM message_vectors v
		JOIN messages m ON m.session_id = v.session_id AND m.msg_index = v.msg_index
		JOIN sessions s ON s.id = v.session_id
		WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("recall: semantic search: %w", err)
	}
	defer rows.Close()

	var hits []ScoredHit
	for rows.Next() {
		var (
			h           ScoredHit
			roleStr     string
			content     string
			name        nullString
			model       nullString
			createdUnix int64
			blob        []byte
		)
		if err := rows.Scan(&h.SessionID, &h.MsgIndex, &roleStr, &content, &name, &model, &createdUnix, &blob); err != nil {
			return nil, err
		}
		score := cosine(queryVec, decodeVec(blob))
		if score < opts.MinScore {
			continue
		}
		if name.Valid {
			h.SessionName = name.String
		}
		if model.Valid {
			h.Model = model.String
		}
		h.Role = adapter.Role(roleStr)
		h.Created = time.Unix(createdUnix, 0)
		h.Snippet = excerpt(content)
		h.Score = score
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Created.After(hits[j].Created)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// excerptMaxRunes bounds a rendered excerpt so a single long message can't
// dominate the injected recall block.
const excerptMaxRunes = 240

// excerpt collapses a message body to a single trimmed line, truncated to
// excerptMaxRunes. Collapsing to one line also keeps recalled text from
// carrying markdown headings that could collide with the system prompt's
// section markers when injected.
func excerpt(s string) string {
	var b []rune
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		b = append(b, r)
		if len(b) >= excerptMaxRunes {
			b = append(b, '…')
			break
		}
	}
	// Trim a leading/trailing space introduced by collapsing.
	out := string(b)
	for len(out) > 0 && out[0] == ' ' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return out
}

// nullString / nullInt64 are minimal local sql.Null* stand-ins used so the
// vector-join scans stay in this file without dragging database/sql's Null
// types across the package's public surface.
type nullString struct {
	String string
	Valid  bool
}

func (n *nullString) Scan(v any) error {
	if v == nil {
		n.String, n.Valid = "", false
		return nil
	}
	switch s := v.(type) {
	case string:
		n.String, n.Valid = s, true
	case []byte:
		n.String, n.Valid = string(s), true
	default:
		return fmt.Errorf("recall: cannot scan %T into string", v)
	}
	return nil
}

type nullInt64 struct {
	Int64 int64
	Valid bool
}

func (n *nullInt64) Scan(v any) error {
	if v == nil {
		n.Int64, n.Valid = 0, false
		return nil
	}
	switch i := v.(type) {
	case int64:
		n.Int64, n.Valid = i, true
	default:
		return fmt.Errorf("recall: cannot scan %T into int64", v)
	}
	return nil
}
