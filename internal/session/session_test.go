package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	first.Save()
	time.Sleep(2 * time.Millisecond)
	second, _ := New("m", "/x")
	second.Name = "labelled"
	second.Save()

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
	if err := older.Save(); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	other, err := New("m", "/proj/b")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := other.Save(); err != nil {
		t.Fatalf("Save other: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	newer, err := New("m", "/proj/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := newer.Save(); err != nil {
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
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := LatestInCwd("/proj/elsewhere"); err == nil {
		t.Fatalf("expected error for non-matching cwd")
	} else if !errorsIs(err, ErrNoSessionInCwd) {
		t.Errorf("expected ErrNoSessionInCwd sentinel; got %v", err)
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
