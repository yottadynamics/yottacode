package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

func redirectHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

// withExchange gives a session the minimum content that makes it resumable.
// List and LatestInCwd deliberately skip system-only shells, so any test that
// expects a saved session to be offered back has to put a real turn in it.
func withExchange(s *Session) *Session {
	s.Messages = append(s.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello"},
	)
	return s
}

func TestNew_CreatesFreshID(t *testing.T) {
	redirectHome(t)
	s1, err := New("m1", "/cwd/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // guarantee distinct timestamp IDs
	s2, err := New("m1", "/cwd/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s1.ID == s2.ID {
		t.Errorf("consecutive IDs collided: %q", s1.ID)
	}
	if s1.Model != "m1" {
		t.Errorf("Model = %q, want m1", s1.Model)
	}
	if s1.Cwd != "/cwd/a" {
		t.Errorf("Cwd = %q, want /cwd/a", s1.Cwd)
	}
}

// TestSave_ConcurrentSavesNoCorruptionOrTempLeak simulates two
// yottacode processes editing the same session at once. With the old
// fixed "<path>.tmp" suffix they raced on one temp file and could
// rename a half-written or clobbered file into place; the unique-temp
// scheme makes concurrent saves last-writer-wins on a valid file. We
// assert: no Save errors, the final file still loads, and no leftover
// temp files are abandoned in the sessions dir.
func TestSave_ConcurrentSavesNoCorruptionOrTempLeak(t *testing.T) {
	home := redirectHome(t)
	s, err := New("m", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Two independent in-memory views of the same on-disk session.
	a, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load a: %v", err)
	}
	b, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load b: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for range 16 {
		for _, view := range []*Session{a, b} {
			wg.Add(1)
			go func(v *Session) {
				defer wg.Done()
				if e := v.Save(); e != nil {
					errs <- e
				}
			}(view)
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent Save error: %v", e)
	}

	// Final file must still be valid, loadable JSON.
	if _, err := Load(s.ID); err != nil {
		t.Fatalf("session unreadable after concurrent saves: %v", err)
	}

	// No abandoned temp files left behind.
	dir := filepath.Join(home, ".yottacode", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file in sessions dir: %s", e.Name())
		}
	}
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	redirectHome(t)
	s, err := New("qwen3.5:latest", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{ID: "c1", Name: "read_file", ArgsJSON: `{"path":"x"}`},
		}},
		{Role: adapter.RoleTool, Content: "result", ToolCallID: "c1"},
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != s.ID || loaded.Model != s.Model || loaded.Cwd != s.Cwd {
		t.Errorf("metadata mismatch: %+v vs %+v", loaded, s)
	}
	if len(loaded.Messages) != len(s.Messages) {
		t.Fatalf("messages len = %d, want %d", len(loaded.Messages), len(s.Messages))
	}
	if loaded.Messages[2].ToolCalls[0].Name != "read_file" {
		t.Errorf("tool_calls lost in roundtrip: %+v", loaded.Messages[2])
	}
}

// TestLoad_LastIsNoLongerMagic guards against regressions to the
// retired "last" keyword. The /sessions picker covers the
// "load most recent" workflow now; Load("last") must error like any
// other unknown id so `yottacode sessions resume last` fails with a
// clear "no session" message rather than silently grabbing the
// newest entry.
func TestLoad_LastIsNoLongerMagic(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Load("last"); err == nil {
		t.Errorf("Load(\"last\") should error now that the keyword is retired; got nil")
	}
}

func TestLoad_UnknownID(t *testing.T) {
	redirectHome(t)
	if _, err := Load("does-not-exist"); err == nil {
		t.Errorf("Load(nonexistent) should error")
	}
}

func TestLoad_ByName(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Name = "feature-branch"
	s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load("feature-branch")
	if err != nil {
		t.Fatalf("Load by name: %v", err)
	}
	if loaded.ID != s.ID {
		t.Errorf("loaded ID = %q, want %q", loaded.ID, s.ID)
	}
	if loaded.Name != "feature-branch" {
		t.Errorf("loaded Name = %q, want feature-branch", loaded.Name)
	}
}

// TestSaveLoad_TodosRoundtrip verifies the new Todos field is
// persisted and restored alongside the message history. Sessions
// written before this field existed must still load cleanly with
// an empty Todos slice — covered separately by
// TestSaveLoad_OldSessionWithoutTodos.
func TestSaveLoad_TodosRoundtrip(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Todos = []agent.Todo{
		{Content: "scan repo", Status: agent.TodoCompleted},
		{Content: "edit file", Status: agent.TodoInProgress},
		{Content: "run tests", Status: agent.TodoPending},
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Todos) != 3 {
		t.Fatalf("Todos len = %d, want 3", len(loaded.Todos))
	}
	if loaded.Todos[1].Status != agent.TodoInProgress || loaded.Todos[1].Content != "edit file" {
		t.Errorf("middle Todo did not round-trip: %+v", loaded.Todos[1])
	}
}

// TestSaveLoad_SubagentTasksRoundtrip: the persisted subagent task index
// survives Save/Load so its task-ids resolve on a later resume.
func TestSaveLoad_SubagentTasksRoundtrip(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.SubagentTasks = []subagents.TaskRecord{
		{ID: "task0000000000a1", AgentType: "review", Status: subagents.TaskCompleted, Result: "found 2 bugs", Background: true, TokensUsed: 4096},
		{ID: "task0000000000b2", AgentType: "code-verifier", Status: subagents.TaskErrored, Errored: true, Result: "could not refute"},
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.SubagentTasks) != 2 {
		t.Fatalf("SubagentTasks len = %d, want 2", len(loaded.SubagentTasks))
	}
	got := loaded.SubagentTasks[0]
	if got.ID != "task0000000000a1" || got.Status != subagents.TaskCompleted || got.Result != "found 2 bugs" || got.TokensUsed != 4096 {
		t.Errorf("first record did not round-trip: %+v", got)
	}
}

// TestSaveLoad_OldSessionWithoutTodos confirms the backwards
// compatibility promise: a session JSON file that predates the
// Todos field loads with an empty slice rather than failing.
func TestSaveLoad_OldSessionWithoutTodos(t *testing.T) {
	home := redirectHome(t)
	old := map[string]any{
		"id":       "20260101-000000.000000",
		"model":    "m",
		"created":  "2026-01-01T00:00:00Z",
		"cwd":      "/x",
		"messages": []any{},
	}
	b, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260101-000000.000000.json"), b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := Load("20260101-000000.000000")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Todos) != 0 {
		t.Errorf("Todos = %+v, want empty for legacy session", loaded.Todos)
	}
}

// TestSave_OmitsEmptyTodos guards the json:",omitempty" tag — old
// session JSONs should remain byte-identical after a save cycle when
// no todos were ever added, so the field doesn't surprise readers
// inspecting session files on disk.
func TestSave_OmitsEmptyTodos(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := string(b); strings.Contains(got, `"todos"`) {
		t.Errorf("save with empty Todos should omit the field; got %s", got)
	}
}

func TestList_NewestFirst(t *testing.T) {
	redirectHome(t)
	first, _ := New("m", "/x")
	withExchange(first).Save()
	time.Sleep(2 * time.Millisecond)
	second, _ := New("m", "/x")
	second.Name = "labelled"
	withExchange(second).Save()

	infos, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2", len(infos))
	}
	if infos[0].ID != second.ID {
		t.Errorf("expected newest first; got %q then %q", infos[0].ID, infos[1].ID)
	}
	if infos[0].Name != "labelled" {
		t.Errorf("Name didn't round-trip: %+v", infos[0])
	}
}

func TestListWith_PagesNewestFirst(t *testing.T) {
	redirectHome(t)
	var ids []string
	for i := range 5 {
		s, _ := New("m", "/x")
		s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: fmt.Sprintf("prompt %d", i)}}
		if err := s.Save(); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
		ids = append(ids, s.ID)
		time.Sleep(2 * time.Millisecond)
	}

	infos, err := ListPage(ListOptions{}, 1, 2)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("page len = %d, want 2", len(infos))
	}
	if infos[0].ID != ids[3] || infos[1].ID != ids[2] {
		t.Fatalf("page ids = [%s %s], want [%s %s]", infos[0].ID, infos[1].ID, ids[3], ids[2])
	}
	if infos[0].Summary != "prompt 3" {
		t.Errorf("paged row should still carry summary, got %q", infos[0].Summary)
	}
}

// LatestInCwd is the engine behind `yottacode --continue`: it picks
// the newest saved session whose Cwd matches the requested directory.
// Mismatched Cwd values are filtered out — that's the whole point —
// and the picker returns ErrNoSessionInCwd when nothing matches so
// the CLI can surface a clean error rather than a generic load
// failure.
func TestLatestInCwd_PicksNewestMatch(t *testing.T) {
	redirectHome(t)
	// Two sessions in /proj/a, one in /proj/b. Newest in /proj/a wins
	// for that cwd; /proj/b returns its only session; /proj/c returns
	// the sentinel.
	older, err := New("m", "/proj/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := withExchange(older).Save(); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	other, err := New("m", "/proj/b")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := withExchange(other).Save(); err != nil {
		t.Fatalf("Save other: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	newer, err := New("m", "/proj/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := withExchange(newer).Save(); err != nil {
		t.Fatalf("Save newer: %v", err)
	}

	got, err := LatestInCwd("/proj/a")
	if err != nil {
		t.Fatalf("LatestInCwd /proj/a: %v", err)
	}
	if got.ID != newer.ID {
		t.Errorf("LatestInCwd /proj/a = %q, want newer %q", got.ID, newer.ID)
	}

	got, err = LatestInCwd("/proj/b")
	if err != nil {
		t.Fatalf("LatestInCwd /proj/b: %v", err)
	}
	if got.ID != other.ID {
		t.Errorf("LatestInCwd /proj/b = %q, want %q", got.ID, other.ID)
	}
}

func TestLatestInCwd_NoMatchReturnsSentinel(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/proj/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := withExchange(s).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := LatestInCwd("/proj/elsewhere"); err == nil {
		t.Fatalf("expected error for non-matching cwd")
	} else if !errorsIs(err, ErrNoSessionInCwd) {
		t.Errorf("expected ErrNoSessionInCwd sentinel; got %v", err)
	}
}

// TestHasExchange_OnlyCountsUserOrAssistant pins the predicate that decides
// whether a session is worth persisting and worth offering as resumable.
func TestHasExchange_OnlyCountsUserOrAssistant(t *testing.T) {
	for _, tc := range []struct {
		name string
		msgs []adapter.Message
		want bool
	}{
		{"nil session", nil, false},
		{"system only", []adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}}, false},
		{"has user", []adapter.Message{
			{Role: adapter.RoleSystem, Content: "sys"},
			{Role: adapter.RoleUser, Content: "hi"},
		}, true},
		{"has assistant", []adapter.Message{
			{Role: adapter.RoleSystem, Content: "sys"},
			{Role: adapter.RoleAssistant, Content: "hello"},
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{Messages: tc.msgs}
			if got := s.HasExchange(); got != tc.want {
				t.Fatalf("HasExchange() = %v, want %v", got, tc.want)
			}
		})
	}
	var nilSess *Session
	if nilSess.HasExchange() {
		t.Errorf("nil session should report no exchange")
	}
}

// TestLatestInCwd_SkipsSystemOnlyShells is the regression guard for "I
// resumed and my history was gone". Older builds saved a session even when
// it held nothing but the system prompt, so `--continue` could pick that
// shell over the real conversation that came before it and open an empty
// transcript.
func TestLatestInCwd_SkipsSystemOnlyShells(t *testing.T) {
	redirectHome(t)
	real, err := New("m", "/proj/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save real: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	// Newer, but nothing in it but a system prompt — exactly the shell an
	// "open yottacode, quit straight away" launch used to leave behind.
	shell, err := New("m", "/proj/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shell.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}}
	if err := shell.Save(); err != nil {
		t.Fatalf("Save shell: %v", err)
	}

	got, err := LatestInCwd("/proj/a")
	if err != nil {
		t.Fatalf("LatestInCwd: %v", err)
	}
	if got.ID != real.ID {
		t.Errorf("--continue picked %q; want the real conversation %q", got.ID, real.ID)
	}
}

// TestList_SkipsSystemOnlyShells keeps shells written by older builds out of
// the /sessions picker — every entry it offers must have something to resume.
func TestList_SkipsSystemOnlyShells(t *testing.T) {
	redirectHome(t)
	real, _ := New("m", "/x")
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save real: %v", err)
	}
	shell, _ := New("m", "/x")
	shell.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}}
	if err := shell.Save(); err != nil {
		t.Fatalf("Save shell: %v", err)
	}

	infos, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("List returned %d entries, want only the resumable one", len(infos))
	}
	if infos[0].ID != real.ID {
		t.Errorf("List returned %q, want %q", infos[0].ID, real.ID)
	}
}

// writeSnapshot drops a pre-compaction snapshot next to the sessions, in
// the exact shape writePreSummarySnapshot produces: keyed session_id (not
// id), so decoding it as a Session yields an empty ID with real messages.
func writeSnapshot(t *testing.T, home, sessionID string) string {
	t.Helper()
	return writeSnapshotN(t, home, sessionID, "20260721-101010.000000000", 2)
}

// writeSnapshotN writes a snapshot with n messages at the given stamp, so
// tests can build several archives of differing richness for one parent.
func writeSnapshotN(t *testing.T, home, sessionID, stamp string, n int) string {
	t.Helper()
	msgs := make([]adapter.Message, 0, n)
	for i := range n {
		role := adapter.RoleUser
		content := "pre-compaction turn"
		if i%2 == 1 {
			role, content = adapter.RoleAssistant, "pre-compaction reply"
		}
		msgs = append(msgs, adapter.Message{Role: role, Content: content})
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	p := filepath.Join(dir, sessionID+SnapshotMarker+stamp+".json")
	body, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"captured":   time.Now().UTC(),
		"messages":   msgs,
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return p
}

// TestList_SurfacesSnapshotsAsArchived: snapshots appear as their own
// clearly-marked rows, because compaction leaves them holding history the
// live session no longer has. What must never come back is the blank-id row
// they used to produce — that one resolved to an arbitrary other session.
func TestList_SurfacesSnapshotsAsArchived(t *testing.T) {
	home := redirectHome(t)
	real, _ := New("m", "/x")
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeSnapshot(t, home, real.ID)

	infos, err := ListWith(ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListWith: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("ListWith returned %d entries, want the session + its archive", len(infos))
	}
	var archived *SessionInfo
	for i, in := range infos {
		if in.ID == "" {
			t.Fatalf("blank-id row is back — that silently resolved to the wrong session: %+v", in)
		}
		if in.Archived {
			archived = &infos[i]
		}
	}
	if archived == nil {
		t.Fatal("no archived row surfaced for the snapshot")
	}
	if archived.ArchivedOf != real.ID {
		t.Errorf("ArchivedOf = %q, want parent %q", archived.ArchivedOf, real.ID)
	}
	if !IsSnapshotID(archived.ID) {
		t.Errorf("archived row id %q should be addressable as a snapshot", archived.ID)
	}
	if archived.Summary == "" {
		t.Error("archived row should carry a gist like any other row")
	}
}

// TestList_OneArchivePerSession: compaction fires repeatedly, so a session
// accumulates several archives that are prefixes of each other. Only the
// richest is offered — listing every one would fill the picker with
// near-duplicates of a single conversation.
func TestList_OneArchivePerSession(t *testing.T) {
	home := redirectHome(t)
	real, _ := New("m", "/x")
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	small := writeSnapshotN(t, home, real.ID, "20260721-101010.000000000", 2)
	big := writeSnapshotN(t, home, real.ID, "20260721-111111.000000000", 6)
	_ = small

	infos, err := ListWith(ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListWith: %v", err)
	}
	var archives []SessionInfo
	for _, in := range infos {
		if in.Archived {
			archives = append(archives, in)
		}
	}
	if len(archives) != 1 {
		t.Fatalf("got %d archive rows, want exactly 1 (the richest)", len(archives))
	}
	if archives[0].Messages != 6 {
		t.Errorf("kept the %d-message archive; want the 6-message one (%s)", archives[0].Messages, big)
	}
}

// TestLoadSnapshot_RestoresIntoFreshSession is the archive-integrity
// guarantee: a snapshot is usually the ONLY copy of its pre-compaction
// history, so opening one must produce a NEW session. If it resolved to
// itself, the first save of the continued conversation would overwrite the
// archive — unrecoverably.
func TestLoadSnapshot_RestoresIntoFreshSession(t *testing.T) {
	home := redirectHome(t)
	parent, _ := New("gpt-5.5", "/proj/x")
	if err := withExchange(parent).Save(); err != nil {
		t.Fatalf("Save parent: %v", err)
	}
	snapPath := writeSnapshot(t, home, parent.ID)
	snapID := strings.TrimSuffix(filepath.Base(snapPath), ".json")
	before, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := Load(snapID)
	if err != nil {
		t.Fatalf("Load(snapshot): %v", err)
	}
	if restored.ID == snapID {
		t.Error("restored session must not adopt the snapshot's id")
	}
	if restored.RestoredFrom != snapID {
		t.Errorf("RestoredFrom = %q, want %q", restored.RestoredFrom, snapID)
	}
	if len(restored.Messages) != 2 {
		t.Errorf("restored %d messages, want the snapshot's 2", len(restored.Messages))
	}
	// Inherited from the parent so provider routing and cwd features work.
	if restored.Model != "gpt-5.5" || restored.Cwd != "/proj/x" {
		t.Errorf("model/cwd not inherited: %q %q", restored.Model, restored.Cwd)
	}

	// The critical assertion: continuing the restored session leaves the
	// archive byte-identical.
	restored.Messages = append(restored.Messages, adapter.Message{Role: adapter.RoleUser, Content: "carry on"})
	if err := restored.Save(); err != nil {
		t.Fatalf("Save restored: %v", err)
	}
	after, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("saving the restored session modified the archive — that history is unrecoverable")
	}
	if _, err := Load(parent.ID); err != nil {
		t.Errorf("parent session should be untouched: %v", err)
	}
}

// TestLoadSnapshot_OrphanRestoresWithoutParent covers the case that motivated
// this: the parent session is gone, so the archive is the only copy.
func TestLoadSnapshot_OrphanRestoresWithoutParent(t *testing.T) {
	home := redirectHome(t)
	snapPath := writeSnapshot(t, home, "20260721-024553.488321")
	snapID := strings.TrimSuffix(filepath.Base(snapPath), ".json")

	restored, err := Load(snapID)
	if err != nil {
		t.Fatalf("orphaned archive must still restore: %v", err)
	}
	if len(restored.Messages) != 2 {
		t.Errorf("restored %d messages, want 2", len(restored.Messages))
	}
}

// TestLatestInCwd_SkipsPreSummarySnapshots keeps --continue off snapshots.
// A snapshot decodes with a zero Created, but it still carries an exchange,
// so only the filename check keeps it out.
func TestLatestInCwd_SkipsPreSummarySnapshots(t *testing.T) {
	home := redirectHome(t)
	real, _ := New("m", "/proj/a")
	real.Cwd = "/proj/a"
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeSnapshot(t, home, real.ID)

	got, err := LatestInCwd("/proj/a")
	if err != nil {
		t.Fatalf("LatestInCwd: %v", err)
	}
	if got.ID != real.ID {
		t.Errorf("LatestInCwd = %q, want %q", got.ID, real.ID)
	}
}

// TestLoad_EmptyIDNeverResolves is the guard for the silent wrong-session
// resume. loadByName matches on Name == name, and every un-renamed session
// has Name == "", so Load("") used to return the first unnamed session on
// disk with no error — the user got somebody else's conversation.
func TestLoad_EmptyIDNeverResolves(t *testing.T) {
	redirectHome(t)
	for _, cwd := range []string{"/a", "/b", "/c"} {
		s, _ := New("m", cwd)
		if err := withExchange(s).Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	for _, ref := range []string{"", "   ", "\t"} {
		got, err := Load(ref)
		if err == nil {
			t.Fatalf("Load(%q) resolved to session %q; must error instead", ref, got.ID)
		}
	}
}

// TestLoad_RejectsPathSeparators is the path-traversal guard. loadByID and
// loadSnapshot build <dir>/<id>.json via filepath.Join, which Clean's ".."
// forward rather than neutralizing it — so without a separator check at
// Load a payload like "../../.ssh/config" would resolve to a file outside
// the sessions directory and, if it was valid JSON, decode into a Session.
// This test pins both the Load-level guard (separator rejected before any
// file is touched) and the safePath belt-and-braces (a relative path that
// escapes dir is refused even if the guard were bypassed).
func TestLoad_RejectsPathSeparators(t *testing.T) {
	redirectHome(t)
	// Seed one real session so the directory exists.
	real, _ := New("m", "/x")
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Every form of separator-bearing id is rejected at Load, and crucially
	// is rejected BEFORE a file is read — verified by pointing at a sibling
	// .json that does exist. A pre-traversal Load would have read it.
	for _, ref := range []string{"../" + real.ID, "../../" + real.ID, "sub/" + real.ID} {
		got, err := Load(ref)
		if err == nil {
			t.Fatalf("Load(%q) resolved to session %q; must reject the separator", ref, got.ID)
		}
		if got != nil {
			t.Fatalf("Load(%q) returned a non-nil session %q alongside the error", ref, got.ID)
		}
	}

	// safePath is the belt-and-braces layer. Verify it directly so a future
	// caller that reaches loadByID/loadSnapshot without Load still can't
	// escape: a ".." id must produce a path error, never a sibling read.
	dir, err := sessionsDir()
	if err != nil {
		t.Fatalf("sessionsDir: %v", err)
	}
	for _, id := range []string{"../" + real.ID, "../../" + real.ID} {
		if p, err := safePath(dir, id); err == nil {
			t.Errorf("safePath(%q) returned %q; must reject ids that escape the sessions dir", id, p)
		}
	}
	// Sanity: a plain id resolves to <dir>/<id>.json.
	if p, err := safePath(dir, real.ID); err != nil || p != filepath.Join(dir, real.ID+".json") {
		t.Errorf("safePath(plain) = %q, %v; want %q, nil", p, err, filepath.Join(dir, real.ID+".json"))
	}
}

func TestLatestInCwd_EmptyDirReturnsSentinel(t *testing.T) {
	redirectHome(t)
	_, err := LatestInCwd("/proj/a")
	if err == nil {
		t.Fatalf("expected error when no sessions exist at all")
	}
	if !errorsIs(err, ErrNoSessionInCwd) {
		t.Errorf("expected ErrNoSessionInCwd sentinel; got %v", err)
	}
}

// TestSubagentUsage_Rollup: the session folds per-subagent Usage into a
// DISTINCT rollup (kept separate from TotalUsage/ModelUsage, which track only
// the main thread). It sums the total, attributes inherited-model runs (empty
// Model) to the session's headline model, keeps explicit-model runs under
// their own model, and skips — and does not count — subagents that reported
// no usage.
func TestSubagentUsage_Rollup(t *testing.T) {
	s := &Session{
		Model: "parent-model",
		SubagentTasks: []subagents.TaskRecord{
			{ID: "a", Model: "", Usage: adapter.Usage{InputTokens: 100, OutputTokens: 20}},           // inherits parent-model
			{ID: "b", Model: "child-model", Usage: adapter.Usage{InputTokens: 40, OutputTokens: 10}}, // explicit model
			{ID: "c", Model: "parent-model", Usage: adapter.Usage{InputTokens: 5, OutputTokens: 1}},  // explicit, == parent
			{ID: "d", Model: "child-model"}, // no usage → skipped
		},
	}
	roll := s.SubagentUsage()

	if roll.AgentCount != 3 {
		t.Errorf("AgentCount = %d, want 3 (zero-usage task d skipped)", roll.AgentCount)
	}
	if roll.Total.InputTokens != 145 || roll.Total.OutputTokens != 31 {
		t.Errorf("Total in/out = %d/%d, want 145/31", roll.Total.InputTokens, roll.Total.OutputTokens)
	}
	// Inherited-model run (a) + same-model run (c) both land under parent-model.
	if got := roll.ByModel["parent-model"]; got.InputTokens != 105 || got.OutputTokens != 21 {
		t.Errorf("parent-model rollup = %+v, want in=105 out=21", got)
	}
	if got := roll.ByModel["child-model"]; got.InputTokens != 40 || got.OutputTokens != 10 {
		t.Errorf("child-model rollup = %+v, want in=40 out=10", got)
	}
}

// TestSaveLoad_UsageRoundtrip locks the per-session usage
// accumulation pipeline: AddUsage must sum into TotalUsage +
// ModelUsage, and a Save/Load cycle must preserve every field. This
// is the test the /usage slash command will fail silently against if
// the accumulator ever drops the cache fields.
func TestSaveLoad_UsageRoundtrip(t *testing.T) {
	redirectHome(t)
	s, err := New("claude-sonnet-4-5", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.AddUsage("claude-sonnet-4-5", &adapter.Usage{
		InputTokens:         1000,
		OutputTokens:        500,
		CacheCreationTokens: 200,
		CacheReadTokens:     800,
	})
	s.AddUsage("claude-sonnet-4-5", &adapter.Usage{
		InputTokens:  300,
		OutputTokens: 100,
	})
	s.AddUsage("gpt-5", &adapter.Usage{
		InputTokens:     50,
		OutputTokens:    20,
		ReasoningTokens: 10,
	})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := loaded.TotalUsage.InputTokens, int64(1350); got != want {
		t.Errorf("TotalUsage.InputTokens = %d, want %d", got, want)
	}
	if got, want := loaded.TotalUsage.OutputTokens, int64(620); got != want {
		t.Errorf("TotalUsage.OutputTokens = %d, want %d", got, want)
	}
	if got, want := loaded.TotalUsage.CacheCreationTokens, int64(200); got != want {
		t.Errorf("TotalUsage.CacheCreationTokens = %d, want %d", got, want)
	}
	if got, want := loaded.TotalUsage.CacheReadTokens, int64(800); got != want {
		t.Errorf("TotalUsage.CacheReadTokens = %d, want %d", got, want)
	}
	if got, want := loaded.TotalUsage.ReasoningTokens, int64(10); got != want {
		t.Errorf("TotalUsage.ReasoningTokens = %d, want %d", got, want)
	}
	if got, want := len(loaded.ModelUsage), 2; got != want {
		t.Errorf("ModelUsage has %d entries, want %d", got, want)
	}
	if got, want := loaded.ModelUsage["claude-sonnet-4-5"].InputTokens, int64(1300); got != want {
		t.Errorf("ModelUsage[claude].InputTokens = %d, want %d", got, want)
	}
	if got, want := loaded.ModelUsage["gpt-5"].ReasoningTokens, int64(10); got != want {
		t.Errorf("ModelUsage[gpt-5].ReasoningTokens = %d, want %d", got, want)
	}
}

// TestSaveLoad_OldSessionWithoutUsage is the backward-compat guard:
// existing session JSON files written before the usage fields landed
// must still load cleanly, with TotalUsage zero and ModelUsage nil.
// The omitzero/omitempty tags also mean a fresh-no-usage save stays
// byte-identical to the old shape.
func TestSaveLoad_OldSessionWithoutUsage(t *testing.T) {
	home := redirectHome(t)
	old := map[string]any{
		"id":       "20250101-000000.000000",
		"model":    "m",
		"created":  "2025-01-01T00:00:00Z",
		"cwd":      "/x",
		"messages": []any{},
	}
	b, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20250101-000000.000000.json"), b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := Load("20250101-000000.000000")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.TotalUsage.IsZero() {
		t.Errorf("TotalUsage = %+v, want zero for legacy session", loaded.TotalUsage)
	}
	if loaded.ModelUsage != nil {
		t.Errorf("ModelUsage = %+v, want nil for legacy session", loaded.ModelUsage)
	}
}

// TestSave_OmitsZeroUsage guards the byte-identical-on-disk promise
// for sessions that have never recorded a usage event.
func TestSave_OmitsZeroUsage(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(b)
	if strings.Contains(got, `"total_usage"`) {
		t.Errorf("save with zero TotalUsage should omit the field; got %s", got)
	}
	if strings.Contains(got, `"model_usage"`) {
		t.Errorf("save with nil ModelUsage should omit the field; got %s", got)
	}
}

// TestUsageSince_FiltersAndSorts covers the daily-rollup scan: only
// sessions created on or after t come back, newest first, with usage
// fields populated from the stripped-decode path.
func TestUsageSince_FiltersAndSorts(t *testing.T) {
	redirectHome(t)
	older, _ := New("m", "/proj")
	older.Created = time.Now().Add(-48 * time.Hour)
	older.AddUsage("m", &adapter.Usage{InputTokens: 100, OutputTokens: 50})
	if err := older.Save(); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	newer1, _ := New("m", "/proj")
	newer1.Created = time.Now().Add(-2 * time.Hour)
	newer1.AddUsage("m", &adapter.Usage{InputTokens: 200, OutputTokens: 80})
	if err := newer1.Save(); err != nil {
		t.Fatalf("Save newer1: %v", err)
	}
	newer2, _ := New("m", "/proj")
	newer2.Created = time.Now().Add(-30 * time.Minute)
	newer2.AddUsage("m", &adapter.Usage{InputTokens: 300, OutputTokens: 120})
	if err := newer2.Save(); err != nil {
		t.Fatalf("Save newer2: %v", err)
	}

	summaries, err := UsageSince(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("UsageSince: %v", err)
	}
	if got, want := len(summaries), 2; got != want {
		t.Fatalf("UsageSince returned %d; want %d (older session must be filtered out)", got, want)
	}
	// Newest first.
	if !summaries[0].Created.After(summaries[1].Created) {
		t.Errorf("summaries[0].Created (%v) must be after summaries[1].Created (%v)",
			summaries[0].Created, summaries[1].Created)
	}
	if got, want := summaries[0].TotalUsage.InputTokens, int64(300); got != want {
		t.Errorf("newest TotalUsage.InputTokens = %d, want %d", got, want)
	}
}

// errorsIs is a tiny local shim so the new tests don't have to import
// "errors" — keeps the existing import block minimal.
func errorsIs(err, target error) bool {
	for cur := err; cur != nil; {
		if cur == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}

// TestSummary_UsesFirstUserPrompt: the picker's "what was this about?"
// line comes from the session's own data — no model call, no stored field.
func TestSummary_UsesFirstUserPrompt(t *testing.T) {
	s := &Session{Messages: []adapter.Message{
		{Role: adapter.RoleSystem, Content: "you are helpful"},
		{Role: adapter.RoleUser, Content: "add retry logic to the fetcher"},
		{Role: adapter.RoleAssistant, Content: "sure"},
	}}
	if got := s.Summary(); got != "add retry logic to the fetcher" {
		t.Errorf("Summary() = %q", got)
	}
}

// TestSummary_CollapsesWhitespace: prompts are multi-line; the picker row
// is one line.
func TestSummary_CollapsesWhitespace(t *testing.T) {
	s := &Session{Messages: []adapter.Message{
		{Role: adapter.RoleUser, Content: "  fix the\n\n  parser   bug\t\tplease  "},
	}}
	if got := s.Summary(); got != "fix the parser bug please" {
		t.Errorf("Summary() = %q, want whitespace collapsed to single spaces", got)
	}
}

// TestSummary_SkipsCompactionPreamble: a compacted history opens with a
// synthetic user turn that is byte-identical in every compacted session.
// Using it would label them all the same, so Summary looks past it.
func TestSummary_SkipsCompactionPreamble(t *testing.T) {
	s := &Session{Messages: []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: SummaryPreamble},
		{Role: adapter.RoleAssistant, Content: "## Decisions made ..."},
		{Role: adapter.RoleUser, Content: "now wire it into the CLI"},
	}}
	if got := s.Summary(); got != "now wire it into the CLI" {
		t.Errorf("Summary() = %q, want the real prompt after the preamble", got)
	}
}

// TestSummary_TruncatesOnRuneBoundary: first prompts reach 160KB in a real
// store, and slicing mid-rune would emit invalid UTF-8 to the terminal.
func TestSummary_TruncatesOnRuneBoundary(t *testing.T) {
	s := &Session{Messages: []adapter.Message{
		{Role: adapter.RoleUser, Content: strings.Repeat("héllo wörld ", 500)},
	}}
	got := s.Summary()
	if r := []rune(got); len(r) != summaryCap {
		t.Errorf("Summary() is %d runes, want %d", len(r), summaryCap)
	}
	if !utf8.ValidString(got) {
		t.Errorf("Summary() produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated Summary should end in an ellipsis: %q", got)
	}
}

// TestSummary_EmptyWhenNothingUsable covers the shell/system-only case and
// a nil receiver.
func TestSummary_EmptyWhenNothingUsable(t *testing.T) {
	var nilSess *Session
	if nilSess.Summary() != "" {
		t.Error("nil session should have no summary")
	}
	s := &Session{Messages: []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "   \n\t  "},
	}}
	if got := s.Summary(); got != "" {
		t.Errorf("blank prompt should yield no summary, got %q", got)
	}
}

// TestList_PopulatesSummary wires it end to end through the picker's source.
func TestList_PopulatesSummary(t *testing.T) {
	redirectHome(t)
	s, _ := New("m", "/x")
	s.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "why is the build flaky?"},
		{Role: adapter.RoleAssistant, Content: "looking"},
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	infos, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d infos", len(infos))
	}
	if infos[0].Summary != "why is the build flaky?" {
		t.Errorf("SessionInfo.Summary = %q", infos[0].Summary)
	}
}

// TestList_ExcludesArchivedByDefault is the regression guard for handing
// archives to callers that can't cope with them.
//
// An archived row's id resolves through loadSnapshot, so Load returns a
// freshly-minted session rather than the row asked for. When List returned
// archives unconditionally, that broke every consumer that treats the list
// as "the live sessions": recall.Backfill indexed each archive under a new
// random id and then immediately pruned it (churn on every launch, and the
// archived content never actually searchable), `sessions list --json` grew
// rows that scripts had never seen, and `sessions rename <archive>` wrote a
// duplicate session instead of renaming. Archives are opt-in for that reason.
func TestList_ExcludesArchivedByDefault(t *testing.T) {
	home := redirectHome(t)
	real, _ := New("m", "/x")
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeSnapshot(t, home, real.ID)

	plain, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, in := range plain {
		if in.Archived {
			t.Errorf("List() must not return archives by default: %+v", in)
		}
		if IsSnapshotID(in.ID) {
			t.Errorf("List() returned a snapshot id %q", in.ID)
		}
	}
	if len(plain) != 1 {
		t.Errorf("List() = %d rows, want just the live session", len(plain))
	}

	// And the opt-in path still surfaces it.
	withArch, err := ListWith(ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListWith: %v", err)
	}
	if len(withArch) != 2 {
		t.Errorf("ListWith(IncludeArchived) = %d rows, want session + archive", len(withArch))
	}
}

// TestSafePath_AllowsDotPrefixedNames: the escape check must key on a
// leading ".." path element, not a ".." string prefix — an id like "..foo"
// resolves inside the directory and is legitimate.
func TestSafePath_AllowsDotPrefixedNames(t *testing.T) {
	redirectHome(t)
	dir, err := sessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"..foo", "...bar", ".hidden"} {
		p, err := safePath(dir, id)
		if err != nil {
			t.Errorf("safePath(%q) rejected a name that stays inside dir: %v", id, err)
			continue
		}
		if p != filepath.Join(dir, id+".json") {
			t.Errorf("safePath(%q) = %q, want %q", id, p, filepath.Join(dir, id+".json"))
		}
	}
	// Still refuses genuine escapes. Note a BARE ".." is not one: the
	// ".json" suffix is appended before the join, so it resolves to the
	// in-directory file "...json" rather than to dir's parent.
	for _, id := range []string{"../x", "../../x", "../" + strings.Repeat("a", 4)} {
		if p, err := safePath(dir, id); err == nil {
			t.Errorf("safePath(%q) = %q; must reject an escape", id, p)
		}
	}
}

// TestAddToolStat_Accumulates: repeated calls for the same tool name sum
// count and approximate output tokens (4-chars/token) and count errors,
// while a different tool name gets its own row. Nil-safe on both the
// receiver and an empty name.
func TestAddToolStat_Accumulates(t *testing.T) {
	var nilSess *Session
	nilSess.AddToolStat("read_file", 40, false) // must not panic

	s := &Session{}
	s.AddToolStat("", 40, false) // empty name is a no-op
	if s.ToolStats != nil {
		t.Fatalf("AddToolStat with empty name must not allocate ToolStats; got %+v", s.ToolStats)
	}

	s.AddToolStat("read_file", 40, false) // 10 tokens
	s.AddToolStat("read_file", 8, true)   // 2 tokens, errored
	s.AddToolStat("bash", 4, false)       // 1 token

	if got, want := len(s.ToolStats), 2; got != want {
		t.Fatalf("len(ToolStats) = %d, want %d", got, want)
	}
	rf := s.ToolStats["read_file"]
	if rf.Count != 2 || rf.OutputTokens != 12 || rf.Errors != 1 {
		t.Errorf("read_file stat = %+v, want {Count:2 OutputTokens:12 Errors:1}", rf)
	}
	b := s.ToolStats["bash"]
	if b.Count != 1 || b.OutputTokens != 1 || b.Errors != 0 {
		t.Errorf("bash stat = %+v, want {Count:1 OutputTokens:1 Errors:0}", b)
	}
}

// TestRecordCompaction_Accumulates: each call appends a record; nil
// receiver is a no-op rather than a panic.
func TestRecordCompaction_Accumulates(t *testing.T) {
	var nilSess *Session
	nilSess.RecordCompaction(100, 40, true) // must not panic

	s := &Session{}
	s.RecordCompaction(1000, 400, true)
	s.RecordCompaction(1200, 500, false)

	if got, want := len(s.CompactionEvents), 2; got != want {
		t.Fatalf("len(CompactionEvents) = %d, want %d", got, want)
	}
	if s.CompactionEvents[0].Before != 1000 || s.CompactionEvents[0].After != 400 || !s.CompactionEvents[0].Auto {
		t.Errorf("CompactionEvents[0] = %+v, want {Before:1000 After:400 Auto:true}", s.CompactionEvents[0])
	}
	if s.CompactionEvents[1].Before != 1200 || s.CompactionEvents[1].After != 500 || s.CompactionEvents[1].Auto {
		t.Errorf("CompactionEvents[1] = %+v, want {Before:1200 After:500 Auto:false}", s.CompactionEvents[1])
	}
}

// TestSaveLoad_ToolStatsAndCompactionRoundtrip locks the persistence path
// for the two new accumulators: a Save/Load cycle must preserve every
// field, and an old session file written before these fields existed must
// still load cleanly with both nil/empty.
func TestSaveLoad_ToolStatsAndCompactionRoundtrip(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.AddToolStat("read_file", 400, false)
	s.AddToolStat("bash", 40, true)
	s.RecordCompaction(2000, 700, true)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := loaded.ToolStats["read_file"].OutputTokens, int64(100); got != want {
		t.Errorf("loaded read_file OutputTokens = %d, want %d", got, want)
	}
	if got, want := loaded.ToolStats["bash"].Errors, 1; got != want {
		t.Errorf("loaded bash Errors = %d, want %d", got, want)
	}
	if got, want := len(loaded.CompactionEvents), 1; got != want {
		t.Fatalf("loaded CompactionEvents has %d entries, want %d", got, want)
	}
	if got, want := loaded.CompactionEvents[0].Before, 2000; got != want {
		t.Errorf("loaded CompactionEvents[0].Before = %d, want %d", got, want)
	}

	// A legacy session file written before these fields existed must still
	// load cleanly, with both fields nil.
	old := map[string]any{
		"id": "20250101-000000.000000", "model": "m", "created": "2025-01-01T00:00:00Z",
		"cwd": "/x", "messages": []any{},
	}
	b, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir, err := sessionsDir()
	if err != nil {
		t.Fatalf("sessionsDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20250101-000000.000000.json"), b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	legacy, err := Load("20250101-000000.000000")
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if legacy.ToolStats != nil {
		t.Errorf("legacy.ToolStats = %+v, want nil", legacy.ToolStats)
	}
	if legacy.CompactionEvents != nil {
		t.Errorf("legacy.CompactionEvents = %+v, want nil", legacy.CompactionEvents)
	}
}

// TestSessionCounts locks the shared basis SessionInfo's Turns/Tools/
// TotalTokens derive from: only assistant messages count as turns, tool
// calls sum across every assistant message, and tokens sum each message's
// own per-turn Usage (nil-safe — a turn the adapter didn't report usage
// for contributes 0, not a panic).
func TestSessionCounts(t *testing.T) {
	messages := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "hi"},
		{
			Role:      adapter.RoleAssistant,
			ToolCalls: []adapter.ToolCall{{Name: "read_file"}, {Name: "grep"}},
			Usage:     &adapter.Usage{InputTokens: 1_000, OutputTokens: 200, CacheReadTokens: 500},
		},
		{Role: adapter.RoleTool, Content: "result"},
		{Role: adapter.RoleAssistant, Usage: nil}, // no usage reported this turn
		{
			Role:  adapter.RoleAssistant,
			Usage: &adapter.Usage{InputTokens: 300, CacheCreationTokens: 100},
		},
	}
	turns, tools, tokens := sessionCounts(messages)
	if turns != 3 {
		t.Errorf("turns = %d, want 3 (assistant messages only)", turns)
	}
	if tools != 2 {
		t.Errorf("tools = %d, want 2 (sum of ToolCalls across assistant messages)", tools)
	}
	if want := int64(1_000 + 200 + 500 + 300 + 100); tokens != want {
		t.Errorf("tokens = %d, want %d", tokens, want)
	}
}

// TestList_PopulatesTokenAndActivityStats locks SessionInfo's new fields
// for a live session: Turns/Tools come from the message log, TotalTokens
// combines main-thread usage with subagent spend (same basis /usage's
// session total uses), and Subagents mirrors len(SubagentTasks).
func TestList_PopulatesTokenAndActivityStats(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "go"},
		{
			Role:      adapter.RoleAssistant,
			ToolCalls: []adapter.ToolCall{{Name: "bash"}},
			Usage:     &adapter.Usage{InputTokens: 10_000, OutputTokens: 500},
		},
	}
	s.SubagentTasks = []subagents.TaskRecord{
		{ID: "sub1", Usage: adapter.Usage{InputTokens: 2_000, OutputTokens: 100}},
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	infos, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d infos", len(infos))
	}
	got := infos[0]
	if got.Turns != 1 {
		t.Errorf("Turns = %d, want 1", got.Turns)
	}
	if got.Tools != 1 {
		t.Errorf("Tools = %d, want 1", got.Tools)
	}
	if got.Subagents != 1 {
		t.Errorf("Subagents = %d, want 1", got.Subagents)
	}
	if want := int64(10_000 + 500 + 2_000 + 100); got.TotalTokens != want {
		t.Errorf("TotalTokens = %d, want %d (main thread + subagent)", got.TotalTokens, want)
	}
}

// TestList_ArchivedRowsPopulateStatsFromMessages confirms an archived
// snapshot row computes Turns/Tools/TotalTokens from its own Messages —
// the one field snapshotPayload reliably carries, unlike TotalUsage or
// SubagentTasks, which aren't part of that payload shape at all.
func TestList_ArchivedRowsPopulateStatsFromMessages(t *testing.T) {
	home := redirectHome(t)
	real, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := withExchange(real).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snapMessages := []adapter.Message{
		{Role: adapter.RoleUser, Content: "pre-compaction turn"},
		{
			Role:      adapter.RoleAssistant,
			Content:   "pre-compaction reply",
			ToolCalls: []adapter.ToolCall{{Name: "grep"}},
			Usage:     &adapter.Usage{InputTokens: 5_000, OutputTokens: 200},
		},
	}
	payload := map[string]any{
		"session_id": real.ID,
		"captured":   time.Now().UTC(),
		"messages":   snapMessages,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	p := filepath.Join(dir, real.ID+SnapshotMarker+"20260721-101010.000000000.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	infos, err := ListWith(ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListWith: %v", err)
	}
	var archived *SessionInfo
	for i, in := range infos {
		if in.Archived {
			archived = &infos[i]
		}
	}
	if archived == nil {
		t.Fatal("no archived row surfaced for the snapshot")
	}
	if archived.Turns != 1 {
		t.Errorf("archived Turns = %d, want 1", archived.Turns)
	}
	if archived.Tools != 1 {
		t.Errorf("archived Tools = %d, want 1", archived.Tools)
	}
	if want := int64(5_000 + 200); archived.TotalTokens != want {
		t.Errorf("archived TotalTokens = %d, want %d", archived.TotalTokens, want)
	}
	if archived.Subagents != 0 {
		t.Errorf("archived Subagents = %d, want 0 (not tracked in snapshotPayload)", archived.Subagents)
	}
}
