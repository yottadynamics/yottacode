package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// scriptedAdapter is the smallest stub that satisfies the agent loop's
// Streamer expectations. Each call to ChatStream pops the next pre-set
// turn from `turns`. Mirrors stubStreamer in the memory tests.
type scriptedAdapter struct {
	mu    sync.Mutex
	turns [][]adapter.StreamEvent
	next  int
}

func (s *scriptedAdapter) ChatStream(_ context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(chan adapter.StreamEvent, 16)
	if s.next >= len(s.turns) {
		close(out)
		return out
	}
	turn := s.turns[s.next]
	s.next++
	go func() {
		defer close(out)
		for _, ev := range turn {
			out <- ev
		}
	}()
	return out
}

func TestEnsureFourSections_AddsMissingHeaders(t *testing.T) {
	got := ensureFourSections("## Decisions made\nuser shipped X")
	for _, h := range []string{
		"## Decisions made",
		"## Code changes",
		"## Open questions",
		"## Preferences expressed",
	} {
		if !strings.Contains(got, h) {
			t.Errorf("missing header %q in output: %s", h, got)
		}
	}
}

func TestComposeSummarizedHistory_PreservesSystemAndRecent(t *testing.T) {
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
	}
	// Add 8 user/assistant pairs so we can verify retention to 5.
	for i := 0; i < 8; i++ {
		history = append(history,
			adapter.Message{Role: adapter.RoleUser, Content: "Q" + string(rune('A'+i))},
			adapter.Message{Role: adapter.RoleAssistant, Content: "A" + string(rune('A'+i))},
		)
	}

	out := composeSummarizedHistory(history, "## Decisions made\n(none)\n## Code changes\n(none)\n## Open questions\n(none)\n## Preferences expressed\n(none)")

	// First message must be the preserved system prompt.
	if out[0].Role != adapter.RoleSystem || out[0].Content != "SYS" {
		t.Errorf("system prompt should be preserved; got %+v", out[0])
	}
	// Second message must be the synthetic summary, marked as such.
	if out[1].Role != adapter.RoleAssistant {
		t.Errorf("expected synthetic assistant summary at index 1; got role %s", out[1].Role)
	}
	if !strings.HasPrefix(out[1].Content, "[Session summary — compressed at") {
		t.Errorf("summary message should be marked; got %q", out[1].Content)
	}
	// Last 5 user turns × 2 messages each = 10 messages retained.
	tail := out[2:]
	users := 0
	for _, m := range tail {
		if m.Role == adapter.RoleUser {
			users++
		}
	}
	if users != retainTurnsAfterSummary {
		t.Errorf("expected %d retained user turns, got %d", retainTurnsAfterSummary, users)
	}
	// Should retain the most recent (Q H), not the earliest (Q A).
	tailStr := ""
	for _, m := range tail {
		tailStr += m.Content + " "
	}
	if !strings.Contains(tailStr, "QH") {
		t.Errorf("retention should keep most recent turn; got %q", tailStr)
	}
	if strings.Contains(tailStr, "QA") {
		t.Errorf("retention should drop oldest turn; got %q", tailStr)
	}
}

func TestWritePreSummarySnapshot_RoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	history := []adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, Content: "yo"},
	}
	path, err := writePreSummarySnapshot("20260428-120000.000000", history)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(path, "pre-summary") {
		t.Errorf("path should contain pre-summary marker, got %q", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var payload struct {
		SessionID string            `json:"session_id"`
		Messages  []adapter.Message `json:"messages"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if payload.SessionID != "20260428-120000.000000" {
		t.Errorf("session id mismatch: %q", payload.SessionID)
	}
	if len(payload.Messages) != 2 {
		t.Errorf("messages roundtrip lost data; got %d", len(payload.Messages))
	}
}

func TestInjectSummaryIntoSystemPrompt_AppendsSection(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "BASE PROMPT"},
		{Role: adapter.RoleUser, Content: "ignored on inject"},
	}
	out := injectSummaryIntoSystemPrompt(msgs, "## Decisions made\nshipped X")
	if len(out) != 1 {
		t.Fatalf("expected single system message; got %d", len(out))
	}
	if !strings.Contains(out[0].Content, "BASE PROMPT") {
		t.Errorf("base prompt should be retained")
	}
	if !strings.Contains(out[0].Content, "## Prior session context (summarized)") {
		t.Errorf("section header not injected")
	}
	if !strings.Contains(out[0].Content, "shipped X") {
		t.Errorf("summary body not injected")
	}
}

func TestInjectSummaryIntoSystemPrompt_IsIdempotent(t *testing.T) {
	msgs := []adapter.Message{{Role: adapter.RoleSystem, Content: "BASE"}}
	once := injectSummaryIntoSystemPrompt(msgs, "FIRST")
	twice := injectSummaryIntoSystemPrompt(once, "SECOND")
	if strings.Count(twice[0].Content, "## Prior session context (summarized)") != 1 {
		t.Errorf("section should appear exactly once after re-injection: %q", twice[0].Content)
	}
}

func TestRunSummarization_EmptyHistoryErrors(t *testing.T) {
	stub := &scriptedAdapter{}
	_, err := runSummarization(context.Background(), stub, nil)
	if err == nil {
		t.Errorf("empty history should error")
	}
}

func TestRunSummarization_StreamsAndStructures(t *testing.T) {
	stub := &scriptedAdapter{turns: [][]adapter.StreamEvent{{
		{Kind: adapter.EventTokenDelta, Token: "## Decisions made\n(none)\n"},
		{Kind: adapter.EventDone, Final: &adapter.Message{Role: adapter.RoleAssistant, Content: "## Decisions made\n(none)"}},
	}}}
	out, err := runSummarization(context.Background(), stub, []adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, Content: "hello"},
	})
	if err != nil {
		t.Fatalf("runSummarization: %v", err)
	}
	for _, want := range []string{
		"## Decisions made",
		"## Code changes",
		"## Open questions",
		"## Preferences expressed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing section %q; got %q", want, out)
		}
	}
}

func TestUpdateContextUsage_FiresWarningAtThreshold(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Default()
	m.fileCfg.Context.WarnThreshold = 0.1 // very low — easy to cross
	m.fileCfg.Context.AutoThreshold = 0.99
	m.modelName = "gpt-4o"
	// Build a synthetic history that pushes past 10% of 128K = ~12800 tokens.
	huge := strings.Repeat("x", 60_000) // ~15K tokens
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: huge},
	}
	cmd := m.updateContextUsage()
	if cmd != nil {
		t.Errorf("warning crossing should NOT trigger auto-summarize: %v", cmd)
	}
	if !strings.Contains(m.transcript.String(), "context at") {
		t.Errorf("expected context watermark notice in transcript; got %q", m.transcript.String())
	}
	want := contextwindow.EstimateTokens(m.sess.Messages)
	if m.contextTokens != want {
		t.Errorf("contextTokens = %d, want %d", m.contextTokens, want)
	}
}

func TestUpdateContextUsage_BelowThresholdIsSilent(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Default()
	m.modelName = "gpt-4o"
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
	cmd := m.updateContextUsage()
	if cmd != nil {
		t.Errorf("below threshold should not fire any cmd")
	}
	if strings.Contains(m.transcript.String(), "context at") {
		t.Errorf("no notice expected; got %q", m.transcript.String())
	}
}

func TestUpdateContextUsage_DisabledByThreshold1(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Default()
	m.fileCfg.Context.WarnThreshold = 1.0
	m.fileCfg.Context.AutoThreshold = 1.0
	m.modelName = "gpt-4o"
	huge := strings.Repeat("x", 60_000)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: huge}}
	cmd := m.updateContextUsage()
	if cmd != nil {
		t.Errorf("threshold=1.0 should disable auto-summarize")
	}
	if strings.Contains(m.transcript.String(), "context at") {
		t.Errorf("threshold=1.0 should suppress warnings; got %q", m.transcript.String())
	}
}

func TestSummaryDoneMsg_ErrorResetsWatermark(t *testing.T) {
	// Regression: a failed auto-summarize must clear lastWatermarkPct so
	// the next turn-end can retry. Before this fix the watermark stayed
	// stamped at the failed-attempt fill level, which left the gate in
	// updateContextUsage permanently closed (lastWatermarkPct >= autoThr)
	// and silently disabled auto-summarization for the rest of the
	// session.
	m := newTestModel(t)
	m.summarizing = true
	m.lastWatermarkPct = 0.86

	m, _ = applyMsg(m, summaryDoneMsg{auto: true, err: context.DeadlineExceeded})

	if m.summarizing {
		t.Errorf("summarizing flag should be cleared after error")
	}
	if m.lastWatermarkPct != 0 {
		t.Errorf("lastWatermarkPct should reset to 0 on error; got %v", m.lastWatermarkPct)
	}
	if !strings.Contains(m.transcript.String(), "[summarize]") {
		t.Errorf("error should be surfaced in transcript; got %q", m.transcript.String())
	}
}

func TestReadNewestSnapshot_ReturnsEmbeddedSummary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "fake")
	_ = dir // not used; HOME drives the path inside the helper

	// Manually construct a snapshot whose final assistant message is a
	// compression artifact, so readNewestSnapshot returns the body.
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".yottacode", "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := map[string]any{
		"session_id": "abc",
		"messages": []adapter.Message{
			{Role: adapter.RoleUser, Content: "hi"},
			{Role: adapter.RoleAssistant, Content: "[Session summary — compressed at turn 3]\n\n## Decisions made\nuser likes Go"},
		},
	}
	b, _ := json.Marshal(payload)
	path := filepath.Join(home, ".yottacode", "sessions", "abc-pre-summary-20260101-000000.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readNewestSnapshot("abc")
	if err != nil {
		t.Fatalf("readNewestSnapshot: %v", err)
	}
	if !strings.Contains(got, "user likes Go") {
		t.Errorf("expected summary body; got %q", got)
	}
}

func TestReadNewestSnapshot_NoSnapshotIsSoft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := readNewestSnapshot("nope")
	if err != nil {
		t.Errorf("missing snapshot should not error; got %v", err)
	}
	if got != "" {
		t.Errorf("missing snapshot should return empty; got %q", got)
	}
}
