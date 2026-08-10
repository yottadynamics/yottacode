package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// Session is one resumable conversation persisted as JSON in
// ~/.yottacode/sessions/<id>.json.
//
// Name is an optional human-readable label set via the /sessions
// picker's Rename action. It's a soft alias: Load will fall back to a
// Name match if the requested id doesn't resolve to a file.
type Session struct {
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	Model    string            `json:"model"`
	Created  time.Time         `json:"created"`
	Cwd      string            `json:"cwd"`
	Messages []adapter.Message `json:"messages"`
	// Todos is the working plan written by the todo_write tool. Omitted
	// from JSON when empty so older session files load unchanged.
	Todos []agent.Todo `json:"todos,omitempty"`
	// Worktree is the yottacode-managed worktree name this session was
	// launched in (via `yottacode --worktree <name>`). Empty for sessions
	// running against the main checkout. Stored so `sessions resume`
	// lands back in the correct worktree dir even if the user moved or
	// renamed the repo. Omitted from JSON when empty so existing session
	// files load unchanged.
	Worktree string `json:"worktree,omitempty"`
	// TotalUsage is the cumulative token tally across every assistant
	// turn in this session. Written by AddUsage on each EventDone.
	// Omitted from JSON when zero so existing session files load
	// byte-identical until the first usage is recorded.
	TotalUsage adapter.Usage `json:"total_usage,omitzero"`
	// ModelUsage is per-model breakdown — users mix models within a
	// session (Anthropic for code review, Gemini for grep, etc.) and
	// the cost calculator needs to know which model produced each
	// turn. Keyed by model ID exactly as the adapter reported it.
	ModelUsage map[string]adapter.Usage `json:"model_usage,omitempty"`
	// SubagentTasks is the persisted index of this session's subagent runs.
	// The live registry (internal/subagents) is in-memory and rebuilt empty
	// each launch; persisting a summary lets get_subagent_result and
	// /subagents resolve task-ids the model wrote into the conversation in a
	// prior session, and lets a startup sweep reclaim a crashed session's
	// empty dispatch worktrees. Omitted when empty so existing session files
	// load unchanged.
	SubagentTasks []subagents.TaskRecord `json:"subagent_tasks,omitempty"`
	// ToolStats is a per-tool-name breakdown of main-thread tool calls —
	// count, approximate output tokens, and error count. Keyed by tool
	// name. Deliberately main-thread only (mirrors TotalUsage/ModelUsage):
	// subagent tool calls stay a single count on subagents.Task.ToolCalls,
	// not broken out by name. Omitted from JSON when empty so existing
	// session files load unchanged.
	ToolStats map[string]ToolStat `json:"tool_stats,omitempty"`
	// CompactionEvents records each time this session's history was
	// compacted, from either mechanism: the TUI's turn-boundary
	// auto-summarize (or manual /summarize), and the main loop's own
	// in-loop agent.ContextCompacted mid-turn self-compaction (wired
	// whenever agentruntime.Build resolves a compaction window — not
	// subagent-only, contrary to an earlier assumption here). Both record
	// into the same slice so /usage can explain otherwise-invisible token
	// spend from re-summarization regardless of which path fired. Omitted
	// from JSON when empty.
	CompactionEvents []CompactionRecord `json:"compaction_events,omitempty"`
	// RestoredFrom records the archived snapshot this session was seeded
	// from, when it was restored rather than started fresh. Provenance only
	// — the restored session is a full independent session from here on.
	RestoredFrom string `json:"restored_from,omitempty"`

	path string // filled by New/Load, not serialized
}

func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, ".yottacode", "sessions")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

// New starts a fresh session and reserves its file path.
func New(model, cwd string) (*Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102-150405.000000")
	return &Session{
		ID:      id,
		Model:   model,
		Created: time.Now().UTC(),
		Cwd:     cwd,
		path:    filepath.Join(dir, id+".json"),
	}, nil
}

// SnapshotMarker tags the pre-compaction history dumps written alongside
// sessions as "<id>-pre-summary-<stamp>.json". Those files live in the
// sessions directory but are NOT sessions: their payload is keyed
// session_id/captured/messages, so decoding one into a Session yields an
// empty ID and a zero Created while still populating Messages.
//
// Exported so the writer (internal/tui's writePreSummarySnapshot) and the
// directory scans here name the pattern once. When they disagreed, every
// snapshot showed up in `sessions list` and the /sessions picker as a
// blank-id row — and selecting one called Load(""), which fell through to
// a name lookup that matched the first unnamed session on disk and
// silently resumed the wrong conversation.
const SnapshotMarker = "-pre-summary-"

// isSessionFile reports whether a directory entry is an actual session
// file rather than a snapshot or some other stray .json.
func isSessionFile(name string) bool {
	return strings.HasSuffix(name, ".json") && !strings.Contains(name, SnapshotMarker)
}

// isSnapshotFile / IsSnapshotID recognize the archive form. A snapshot's id
// is simply its filename without the .json — unique already, and it means
// loadByID's direct path join addresses one without any synthetic scheme.
func isSnapshotFile(name string) bool {
	return strings.HasSuffix(name, ".json") && strings.Contains(name, SnapshotMarker)
}

// IsSnapshotID reports whether a reference names an archived snapshot
// rather than a live session. Exported so the TUI can label what it loaded.
func IsSnapshotID(id string) bool { return strings.Contains(id, SnapshotMarker) }

// snapshotPayload is the on-disk shape written by the compaction flow.
// Note it has no id/cwd/model — only session_id, captured, messages — which
// is why decoding one straight into a Session yields a blank-id row.
type snapshotPayload struct {
	SessionID string            `json:"session_id"`
	Captured  time.Time         `json:"captured"`
	Messages  []adapter.Message `json:"messages"`
}

// snapshotParentID maps a snapshot filename/id back to the session it was
// captured from.
func snapshotParentID(name string) string {
	name = strings.TrimSuffix(name, ".json")
	if i := strings.Index(name, SnapshotMarker); i >= 0 {
		return name[:i]
	}
	return name
}

// loadSnapshot builds a NEW live session seeded from an archived snapshot.
//
// The returned session deliberately carries a fresh id and a fresh path: a
// snapshot is the ONLY surviving copy of the pre-compaction history (the
// live session kept just the summary), so resuming one must never be able
// to write back over it. Continuing the restored conversation saves to the
// new file and leaves the archive untouched.
//
// Model and Cwd are inherited from the parent session when it still exists,
// so provider routing and cwd-scoped features keep working; an orphaned
// snapshot restores with those empty.
func loadSnapshot(dir, id string) (*Session, error) {
	p, err := safePath(dir, id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var payload snapshotPayload
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, fmt.Errorf("snapshot parse %s: %w", id, err)
	}
	model, cwd := "", ""
	if parent, perr := loadByID(dir, snapshotParentID(id)); perr == nil {
		model, cwd = parent.Model, parent.Cwd
	}
	fresh, err := New(model, cwd)
	if err != nil {
		return nil, err
	}
	fresh.Messages = payload.Messages
	fresh.RestoredFrom = id
	return fresh, nil
}

// SummaryPreamble is the synthetic user turn that a compacted history opens
// with, standing in for the real conversation the summary replaced. Defined
// here (and referenced by the compaction code that writes it) so Summary can
// recognize and skip it: it's identical in every compacted session, so using
// it as a session's description would label them all the same.
const SummaryPreamble = "Summarize our conversation so far so we can continue with bounded context."

// summaryCap bounds the stored description. Well above any sane display
// width, but it keeps List from holding a whole prompt in memory per
// session — first messages in a real store reach 160KB.
const summaryCap = 160

// Summary returns a one-line gist of what the session is about, taken from
// the first real user prompt. Empty when there's nothing usable.
//
// Derived rather than stored, so it works on every session already on disk
// with no migration, and it costs nothing extra: List already decodes the
// full message log.
//
// Skips the synthetic compaction preamble, and collapses all whitespace so
// a multi-line prompt renders as a single scannable line.
func (s *Session) Summary() string {
	if s == nil {
		return ""
	}
	for _, m := range s.Messages {
		if m.Role != adapter.RoleUser {
			continue
		}
		line := strings.Join(strings.Fields(m.Content), " ")
		if line == "" || strings.HasPrefix(line, SummaryPreamble) {
			continue
		}
		return truncateRunes(line, summaryCap)
	}
	return ""
}

// truncateRunes cuts to max RUNES with a trailing ellipsis. Rune-based
// rather than byte-based: prompts are arbitrary user text, and slicing
// mid-rune emits invalid UTF-8 that renders as garbage in the terminal.
func truncateRunes(s string, max int) string {
	if max < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// HasExchange reports whether the session holds at least one user or
// assistant message — i.e. whether there is any conversation to come back
// to. System-only sessions don't count: launching yottacode composes a
// system prompt immediately, so a session that was opened and closed
// without a single turn is a ~48KB shell whose entire content is the
// system prompt.
//
// This is the "is this worth persisting / worth offering as resumable"
// predicate, and it gates three places: the at-exit save (don't create
// shells), LatestInCwd (`--continue` must not land on one), and List (the
// /sessions picker must not offer one). Resuming a shell looks like data
// loss to the user — the picker says "1 msgs" and the history is simply
// gone — so the fix is to never create or offer them.
func (s *Session) HasExchange() bool {
	if s == nil {
		return false
	}
	for _, msg := range s.Messages {
		if msg.Role == adapter.RoleUser || msg.Role == adapter.RoleAssistant {
			return true
		}
	}
	return false
}

// Load reads a stored session by id (filename match) or name (Name
// field match). The legacy "last" keyword shortcut was retired
// alongside the /resume slash command — the /sessions picker (and the
// `yottacode sessions resume <id|name>` cobra subcommand) is the canonical
// path for "load the most recent" now, with the picker defaulting
// the cursor to the newest entry.
func Load(id string) (*Session, error) {
	// An empty ref must never resolve. loadByName matches on s.Name == name,
	// and every session that was never renamed has Name == "" — so an empty
	// id would match the first unnamed session in readdir order and return
	// someone else's conversation with no error. Callers reach here with ""
	// whenever a picker row carries no id (see SnapshotMarker).
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("session load: empty session id")
	}
	// A session id is a plain filename stem — never a path. Refusing any
	// separator here closes the loadByID/loadSnapshot read primitive,
	// which used filepath.Join(dir, id+".json") and would have resolved
	// "../foo" up out of the sessions directory (filepath.Join Clean's
	// ".." forward, it doesn't neutralize it). The picker only ever
	// offers ids it read off disk, but --resume and oneshot take an
	// arbitrary string, so the guard belongs here, not at the callers.
	if strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("session load: id must not contain a path separator: %q", id)
	}
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	// An archived snapshot restores into a fresh session rather than
	// resolving to itself — see loadSnapshot on why it must never be
	// opened in place.
	if IsSnapshotID(id) {
		return loadSnapshot(dir, id)
	}
	if s, err := loadByID(dir, id); err == nil {
		return s, nil
	}
	if s, err := loadByName(dir, id); err == nil {
		return s, nil
	}
	return nil, fmt.Errorf("session load: no session with id or name %q", id)
}

// safePath builds <dir>/<id>.json and asserts the resolved path stays
// inside dir. id reaches here from user-controlled sources (--resume,
// oneshot, picker text input), and filepath.Join Clean's ".." forward
// rather than stripping it — so without this guard a payload like
// "../../.ssh/config" would resolve to ~/.ssh/config.json and, if it
// was valid JSON, decode into a Session. The separator check at Load
// is the primary defense; this is belt-and-braces for any future caller
// that reaches loadByID or loadSnapshot without going through Load.
func safePath(dir, id string) (string, error) {
	p := filepath.Join(dir, id+".json")
	// filepath.Rel expresses the resolved path as seen from dir; a leading
	// ".." path ELEMENT is the unambiguous "escaped the directory" signal.
	// Compare against the element, not the prefix: a bare HasPrefix("..")
	// also rejects legitimate names that merely start with dots, e.g. an id
	// of "..foo" resolves to "<dir>/..foo.json" and never leaves dir.
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("session path escapes sessions directory: %q", id)
	}
	return p, nil
}

func loadByID(dir, id string) (*Session, error) {
	p, err := safePath(dir, id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("session parse %s: %w", p, err)
	}
	s.path = p
	return &s, nil
}

func loadByName(dir, name string) (*Session, error) {
	// Defense in depth alongside the Load guard: an empty name matches every
	// unnamed session, so refuse it here too rather than relying on callers.
	if name == "" {
		return nil, errors.New("not found by name")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		if s.Name == name {
			s.path = p
			return &s, nil
		}
	}
	return nil, errors.New("not found by name")
}

// LatestInCwd returns the most recent saved session whose Cwd matches
// the given directory. Used by `yottacode --continue` (mirroring
// Claude Code's --continue) to skip the picker and resume the
// directory's last session directly. Returns an error wrapping
// errNoSessionInCwd when no saved session matches.
//
// "Most recent" is determined by sorting all matches descending by
// Session.Created, falling back to the timestamp-prefixed ID when two
// sessions share an identical Created (test fixtures). The Cwd
// comparison is exact-string match — symlinked or differently-resolved
// paths won't unify; users hit by that should pass the matching path
// explicitly.
func LatestInCwd(cwd string) (*Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var matches []Session
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		if s.Cwd != cwd {
			continue
		}
		// Skip system-only shells: `--continue` means "pick up where I left
		// off", and resuming a session with no exchange drops the user into
		// an empty transcript that looks like their history vanished.
		if !s.HasExchange() {
			continue
		}
		s.path = p
		matches = append(matches, s)
	}
	if len(matches) == 0 {
		return nil, ErrNoSessionInCwd
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Created.Equal(matches[j].Created) {
			return matches[i].ID > matches[j].ID
		}
		return matches[i].Created.After(matches[j].Created)
	})
	out := matches[0]
	return &out, nil
}

// ErrNoSessionInCwd is returned by LatestInCwd when no saved session
// has a Cwd field matching the requested directory. Sentinel so
// callers can present a friendly "no prior session in this directory"
// error without string matching.
var ErrNoSessionInCwd = errors.New("no saved session in this directory")

// ListOptions tunes what List returns.
//
// IncludeArchived adds one row per snapshotted session for its richest
// pre-compaction archive. It is OPT-IN because an archived row is not an
// ordinary session: its id resolves through loadSnapshot, so Load returns a
// freshly-minted session rather than the row you asked for. Callers that
// treat List output as "the set of live sessions" — recall.Backfill keying
// its index and prune by id, `yottacode sessions list` feeding scripts —
// break subtly when handed archives. Only the /sessions picker, which knows
// to render and resume them differently, asks for them.
type ListOptions struct {
	IncludeArchived bool
}

// List returns every saved live session's metadata, newest first. Doesn't
// load the full message log to keep this cheap for the /sessions slash
// command. Archived snapshots are excluded — see ListWith.
func List() ([]SessionInfo, error) {
	return ListWith(ListOptions{})
}

// ListWith is List with explicit options.
func ListWith(opts ListOptions) ([]SessionInfo, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	models := map[string]string{} // session id -> model, for labelling archives
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		models[s.ID] = s.Model
		// Shells have nothing to resume, so they'd be dead entries in the
		// /sessions picker and in `sessions list`. Newer builds no longer
		// write them, but a store built by older builds still holds them.
		if !s.HasExchange() {
			continue
		}
		out = append(out, SessionInfo{
			ID:       s.ID,
			Name:     s.Name,
			Model:    s.Model,
			Created:  s.Created,
			Messages: len(s.Messages),
			Worktree: s.Worktree,
			Summary:  s.Summary(),
		})
	}
	if opts.IncludeArchived {
		out = append(out, listSnapshots(dir, entries, models)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// listSnapshots returns one row per snapshotted session: the archive that
// holds the MOST history for that parent.
//
// Compaction rewrites the live session down to a summary, so for most
// snapshotted sessions the archive is the only surviving copy of the
// pre-compaction detail — measured across a real store, 14 of 21 snapshots
// held more messages than the session they came from, several by hundreds.
// That's why these are worth surfacing at all.
//
// One row per parent, not one per file: compaction fires repeatedly, so
// sessions accumulate 2-3 archives that are prefixes of each other. Listing
// them all would fill the picker with near-duplicates of one conversation
// and crowd out distinct sessions.
func listSnapshots(dir string, entries []os.DirEntry, models map[string]string) []SessionInfo {
	best := map[string]SessionInfo{}
	for _, e := range entries {
		if e.IsDir() || !isSnapshotFile(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var payload snapshotPayload
		if err := json.Unmarshal(b, &payload); err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		parent := snapshotParentID(id)
		probe := Session{Messages: payload.Messages}
		if !probe.HasExchange() {
			continue
		}
		if cur, ok := best[parent]; ok && cur.Messages >= len(payload.Messages) {
			continue
		}
		best[parent] = SessionInfo{
			ID:         id,
			Model:      models[parent],
			Created:    payload.Captured,
			Messages:   len(payload.Messages),
			Summary:    probe.Summary(),
			Archived:   true,
			ArchivedOf: parent,
		}
	}
	out := make([]SessionInfo, 0, len(best))
	for _, in := range best {
		out = append(out, in)
	}
	return out
}

// SessionInfo is a metadata-only view returned by List.
type SessionInfo struct {
	ID       string
	Name     string
	Model    string
	Created  time.Time
	Messages int
	// Worktree is the yottacode worktree name this session ran in, or
	// empty for the main checkout. Surfaced in `yottacode sessions list`
	// output so users can tell which sessions belong to which worktree.
	Worktree string
	// Summary is the one-line gist from the session's first real user
	// prompt — the "what was this one about?" that an id and a timestamp
	// can't answer. See Session.Summary. Empty when nothing usable.
	Summary string
	// Archived marks a pre-compaction snapshot rather than a live session.
	// Loading one restores its history into a NEW session; the archive
	// itself is never written to. ArchivedOf names the session it was
	// captured from (which may no longer exist).
	Archived   bool
	ArchivedOf string
}

// AddUsage records the per-turn usage that just landed on an
// assistant message into the session's running totals. Safe to call
// with a nil receiver or nil usage — both branches no-op. Caller is
// responsible for Save() if the new totals should be persisted now;
// most callers persist on the same cadence as Messages.
func (s *Session) AddUsage(model string, u *adapter.Usage) {
	if s == nil || u == nil {
		return
	}
	s.TotalUsage.Add(u)
	if model == "" {
		return
	}
	if s.ModelUsage == nil {
		s.ModelUsage = map[string]adapter.Usage{}
	}
	prev := s.ModelUsage[model]
	prev.Add(u)
	s.ModelUsage[model] = prev
}

// ToolStat is one tool's accumulated main-thread call stats: how many times
// it ran, its approximate combined output size in tokens (4-chars/token
// heuristic, matching the estimate agent/compaction.go and cmd_summarize.go
// already use for budgeting), and how many calls errored.
type ToolStat struct {
	Count        int   `json:"count"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	Errors       int   `json:"errors,omitempty"`
}

// AddToolStat records one main-thread tool call into the session's per-tool
// breakdown. outputChars is the raw length of the tool result string;
// converted to an approximate token count here so renderers don't repeat
// the conversion. Safe to call with a nil receiver or empty name.
func (s *Session) AddToolStat(name string, outputChars int, errored bool) {
	if s == nil || name == "" {
		return
	}
	if s.ToolStats == nil {
		s.ToolStats = map[string]ToolStat{}
	}
	stat := s.ToolStats[name]
	stat.Count++
	stat.OutputTokens += int64((outputChars + 3) / 4)
	if errored {
		stat.Errors++
	}
	s.ToolStats[name] = stat
}

// CompactionRecord is one firing of this session's turn-boundary
// auto-summarize (or manual /summarize). Before/After are the estimated
// token counts immediately before and after the rewrite — the same figures
// already surfaced in the scrollback receipt, just persisted here too.
type CompactionRecord struct {
	At     time.Time `json:"at"`
	Before int       `json:"before"`
	After  int       `json:"after"`
	Auto   bool      `json:"auto,omitempty"`
}

// RecordCompaction appends one compaction event to the session's history.
// Safe to call with a nil receiver.
func (s *Session) RecordCompaction(before, after int, auto bool) {
	if s == nil {
		return
	}
	s.CompactionEvents = append(s.CompactionEvents, CompactionRecord{
		At: time.Now(), Before: before, After: after, Auto: auto,
	})
}

// SubagentUsageRollup aggregates a session's subagent token spend from its
// persisted SubagentTasks index. It is kept DISTINCT from TotalUsage /
// ModelUsage — those track only the main assistant thread (session.AddUsage
// is called solely from the main loop) — so /usage can attribute spend to
// subagents separately and still present a combined total. Subagent turns
// that inherited the parent's model (empty Model on the record) are
// attributed to parentModel so the per-model breakdown stays meaningful.
type SubagentUsageRollup struct {
	Total      adapter.Usage
	ByModel    map[string]adapter.Usage
	AgentCount int // number of subagents that reported non-zero usage
}

// subagentUsageRollup sums the exact per-task Usage across records, skipping
// tasks that never reported usage (IsZero) so a provider that doesn't emit
// usage doesn't inflate the agent count with phantom zero rows.
func subagentUsageRollup(records []subagents.TaskRecord, parentModel string) SubagentUsageRollup {
	r := SubagentUsageRollup{ByModel: map[string]adapter.Usage{}}
	for i := range records {
		u := records[i].Usage
		if u.IsZero() {
			continue
		}
		model := records[i].Model
		if model == "" {
			model = parentModel
		}
		r.Total.Add(&u)
		prev := r.ByModel[model]
		prev.Add(&u)
		r.ByModel[model] = prev
		r.AgentCount++
	}
	return r
}

// SubagentUsage returns this session's subagent token rollup, attributing
// inherited-model runs to the session's headline model. Nil-safe.
func (s *Session) SubagentUsage() SubagentUsageRollup {
	if s == nil {
		return SubagentUsageRollup{ByModel: map[string]adapter.Usage{}}
	}
	return subagentUsageRollup(s.SubagentTasks, s.Model)
}

// SessionUsageSummary is a stripped per-session view used by the
// daily-rollup scan. We avoid decoding Messages (the heavy field) so
// /usage can scan dozens of session files cheaply.
type SessionUsageSummary struct {
	ID            string
	Name          string
	Model         string
	Created       time.Time
	TotalUsage    adapter.Usage
	ModelUsage    map[string]adapter.Usage
	SubagentTasks []subagents.TaskRecord
}

// SubagentUsage returns the subagent token rollup for this summary — the
// daily rollup adds it to TotalUsage so cross-session tallies include
// subagent spend, not just the main thread.
func (s SessionUsageSummary) SubagentUsage() SubagentUsageRollup {
	return subagentUsageRollup(s.SubagentTasks, s.Model)
}

// UsageSince scans every saved session newer than t and returns a
// per-session usage summary. Decodes only the lightweight metadata +
// usage fields — Messages stay on disk, keeping the scan cheap.
// Sessions older than t are filtered out by Created; sessions with
// no usage data still appear so the daily rollup can show "N
// sessions, no token data yet."
func UsageSince(t time.Time) ([]SessionUsageSummary, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionUsageSummary
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var stub struct {
			ID            string                   `json:"id"`
			Name          string                   `json:"name"`
			Model         string                   `json:"model"`
			Created       time.Time                `json:"created"`
			TotalUsage    adapter.Usage            `json:"total_usage"`
			ModelUsage    map[string]adapter.Usage `json:"model_usage"`
			SubagentTasks []subagents.TaskRecord   `json:"subagent_tasks"`
		}
		if err := json.Unmarshal(b, &stub); err != nil {
			continue
		}
		if stub.Created.Before(t) {
			continue
		}
		out = append(out, SessionUsageSummary{
			ID:            stub.ID,
			Name:          stub.Name,
			Model:         stub.Model,
			Created:       stub.Created,
			TotalUsage:    stub.TotalUsage,
			ModelUsage:    stub.ModelUsage,
			SubagentTasks: stub.SubagentTasks,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Save atomically writes the session to disk.
//
// The temp file gets a unique name (os.CreateTemp) rather than a fixed
// "<path>.tmp" suffix: two yottacode processes editing the same session
// (e.g. two terminals, or one `--continue` alongside a `--resume`) would
// otherwise write to the same temp path and clobber each other's
// in-flight write, so one process's history would be silently lost on
// rename. A per-write unique temp name makes concurrent saves
// last-writer-wins on the final file instead of corrupting it.
func (s *Session) Save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds; a
	// successful rename consumes tmpName so the Remove is a harmless no-op.
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	// fsync the data before rename, and the directory after, so a
	// crash/power-loss just after Save returns can't leave the
	// transcript (the highest-value durable artifact) zero-length or
	// stale — matching the memory layer's atomic writes.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	// Best-effort directory fsync so the rename itself is durable.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
