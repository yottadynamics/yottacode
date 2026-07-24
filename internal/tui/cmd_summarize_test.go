package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// TestRenderFallbackLine_AgentLabel: a main-thread fallback has no agent
// tag; a delegated/summarization fallback names its context.
func TestRenderFallbackLine_AgentLabel(t *testing.T) {
	main := stripANSI(renderFallbackLine(agent.Fallback{From: "a", To: "b", Policy: "fallback-chain"}))
	if !strings.Contains(main, "fallback: a → b") || strings.Contains(main, "fallback [") {
		t.Errorf("main-thread fallback should have no agent tag: %q", main)
	}
	sub := stripANSI(renderFallbackLine(agent.Fallback{From: "a", To: "b", Agent: "Explore"}))
	if !strings.Contains(sub, "fallback [Explore]: a → b") {
		t.Errorf("agent fallback should be tagged: %q", sub)
	}
}

// TestChooseSummarizer_DegradesAfterFailures: in auto mode, summarization
// runs on the fast model until it has failed summarizeDegradeThreshold
// times in a row, then degrades to the smart model so a fast-provider
// outage can't block compaction. Off mode never degrades.
func TestChooseSummarizer_DegradesAfterFailures(t *testing.T) {
	ra := testRouterAdapters(t) // fast + smart, defined in cmd_router_test.go
	m := Model{
		routerMode:        config.RouterModeAuto,
		summarizerAdapter: ra.Fast,
		router:            ra,
	}

	// Below the threshold → the fast model, no degrade.
	if ad, degraded := m.chooseSummarizer(); degraded || ad != agentStreamer(ra.Fast) {
		t.Errorf("below threshold should use fast (degraded=%v)", degraded)
	}

	// At the threshold → degrade to the smart model.
	m.summarizeFailures = summarizeDegradeThreshold
	ad, degraded := m.chooseSummarizer()
	if !degraded {
		t.Fatal("at the threshold summarization should degrade to smart")
	}
	if ad != agentStreamer(ra.Smart) {
		t.Error("degraded summarizer should be the smart adapter")
	}

	// Off mode never degrades (summarization runs on the active model).
	m.routerMode = config.RouterModeOff
	if _, degraded := m.chooseSummarizer(); degraded {
		t.Error("off mode must not degrade to smart")
	}
}

// TestRunSummarization_CapturesFallback: an EventFallback during
// compaction is collected so the caller can surface it.
func TestRunSummarization_CapturesFallback(t *testing.T) {
	stub := &scriptedAdapter{turns: [][]adapter.StreamEvent{{
		{Kind: adapter.EventFallback, FallbackFrom: "anthropic/haiku", FallbackTo: "openai/gpt-4o-mini", FallbackReason: "timeout"},
		{Kind: adapter.EventTokenDelta, Token: "## Decisions made\n(none)"},
		{Kind: adapter.EventDone},
	}}}
	_, fallbacks, err := runSummarization(context.Background(), stub, []adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
	}, 0)
	if err != nil {
		t.Fatalf("runSummarization: %v", err)
	}
	if len(fallbacks) != 1 {
		t.Fatalf("expected 1 captured fallback, got %d", len(fallbacks))
	}
	if fallbacks[0].FallbackTo != "openai/gpt-4o-mini" {
		t.Errorf("captured fallback To = %q, want openai/gpt-4o-mini", fallbacks[0].FallbackTo)
	}
}

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

	out := composeSummarizedHistory(history, "## Decisions made\n(none)\n## Code changes\n(none)\n## Open questions\n(none)\n## Preferences expressed\n(none)", 0)

	// First message must be the preserved system prompt.
	if out[0].Role != adapter.RoleSystem || out[0].Content != "SYS" {
		t.Errorf("system prompt should be preserved; got %+v", out[0])
	}
	// A synthetic user preamble precedes the summary so the history is
	// user-first (Claude/Gemini reject a leading assistant turn).
	if out[1].Role != adapter.RoleUser || out[1].Content != summaryUserPreamble {
		t.Errorf("expected synthetic user preamble at index 1; got %+v", out[1])
	}
	// The summary follows, marked as such.
	if out[2].Role != adapter.RoleAssistant {
		t.Errorf("expected synthetic assistant summary at index 2; got role %s", out[2].Role)
	}
	if !strings.HasPrefix(out[2].Content, "[Session summary — compressed at") {
		t.Errorf("summary message should be marked; got %q", out[2].Content)
	}
	// Last 5 user turns × 2 messages each = 10 messages retained.
	tail := out[3:]
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

// TestComposeSummarizedHistory_ProviderValid guards the compressed history
// against the shapes strict providers reject: it must not begin (after the
// system prompt) with an assistant turn (Claude requires a user-first
// messages array; Gemini requires user-first alternation), and it must
// never contain two consecutive assistant turns.
func TestComposeSummarizedHistory_ProviderValid(t *testing.T) {
	cases := map[string][]adapter.Message{
		"with_turns": {
			{Role: adapter.RoleSystem, Content: "SYS"},
			{Role: adapter.RoleUser, Content: "q1"},
			{Role: adapter.RoleAssistant, Content: "a1"},
			{Role: adapter.RoleUser, Content: "q2"},
			{Role: adapter.RoleAssistant, Content: "a2"},
		},
		"no_user_turns": {
			{Role: adapter.RoleSystem, Content: "SYS"},
			{Role: adapter.RoleAssistant, Content: "orphan"},
		},
	}
	for name, history := range cases {
		t.Run(name, func(t *testing.T) {
			out := composeSummarizedHistory(history, "## Decisions made\n(none)", 128_000)
			firstNonSystem := -1
			for i, m := range out {
				if m.Role != adapter.RoleSystem {
					firstNonSystem = i
					break
				}
			}
			if firstNonSystem < 0 {
				t.Fatal("no non-system message in compressed history")
			}
			if out[firstNonSystem].Role != adapter.RoleUser {
				t.Errorf("first non-system message role = %s, want user (Claude/Gemini reject a leading assistant)", out[firstNonSystem].Role)
			}
			for i := 1; i < len(out); i++ {
				if out[i-1].Role == adapter.RoleAssistant && out[i].Role == adapter.RoleAssistant {
					t.Errorf("consecutive assistant turns at %d/%d — provider-invalid", i-1, i)
				}
			}
		})
	}
}

// TestComposeSummarizedHistory_ByteBudgetDropsOldest pins Fix A's
// load-bearing behavior: when retained turns would together exceed
// the byte budget derived from the model window, oldest turns are
// dropped so the compressed history actually shrinks. Before Fix A
// composeSummarizedHistory would keep every retained turn verbatim,
// which made compression a no-op for agent-heavy sessions where a
// handful of turns each carried tens of thousands of tokens of tool
// output.
func TestComposeSummarizedHistory_ByteBudgetDropsOldest(t *testing.T) {
	// Build 5 user turns, each carrying a 60K-char tool result.
	// With windowTokens=128K, retainBudgetFraction=0.4 gives a budget
	// of ~51K tokens — room for roughly 3 turns at 15K tokens each
	// (60K chars ≈ 15K tokens). The two oldest turns must be dropped.
	const turnChars = 60_000
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
	}
	for i := 0; i < 5; i++ {
		marker := string(rune('A' + i))
		history = append(history,
			adapter.Message{Role: adapter.RoleUser, Content: "Q" + marker},
			adapter.Message{Role: adapter.RoleTool, Content: marker + strings.Repeat("x", turnChars-1), ToolCallID: "t" + marker},
			adapter.Message{Role: adapter.RoleAssistant, Content: "A" + marker},
		)
	}

	out := composeSummarizedHistory(history, "## Decisions made\n(none)", 128_000)

	// System + summary are always emitted; tail is the retained turns.
	if len(out) < 2 {
		t.Fatalf("output too short: %d", len(out))
	}
	// Most recent turn (E) must survive.
	tailStr := ""
	for _, m := range out[2:] {
		tailStr += m.Content
	}
	if !strings.Contains(tailStr, "QE") {
		t.Errorf("most-recent turn must be retained; tail had no QE: %q", firstNChars(tailStr, 200))
	}
	// Oldest turn (A) must be dropped.
	if strings.Contains(tailStr, "QA") {
		t.Errorf("oldest turn should be dropped under budget; tail still contains QA")
	}
	// Compression must actually reduce the size: output token count
	// should be well under the input.
	inputTokens := contextwindow.EstimateTokens(history)
	outputTokens := contextwindow.EstimateTokens(out)
	if outputTokens >= inputTokens {
		t.Errorf("compression should reduce tokens; in=%d out=%d", inputTokens, outputTokens)
	}
	// Output must fit comfortably in the budget plus the synthetic
	// summary + system message.
	budget := retainBudgetFor(128_000)
	if outputTokens > budget+2000 { // +2000 for summary + system overhead
		t.Errorf("output (%d tokens) exceeds retainBudget %d + overhead", outputTokens, budget)
	}
}

// TestComposeSummarizedHistory_TruncatesGiantToolResult guards the
// per-message cap inside Fix A: a single tool result larger than
// maxRetainedToolTokens is truncated in place with the compaction
// marker, so one runaway payload can't dominate retention even when
// it lives inside the most-recent (always-retained) turn.
func TestComposeSummarizedHistory_TruncatesGiantToolResult(t *testing.T) {
	// One user turn whose tool result is 200K chars (~50K tokens) —
	// well above maxRetainedToolTokens (4K). The recent-turn always-
	// keep rule applies, but the tool message itself gets truncated.
	giant := strings.Repeat("y", 200_000)
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
		{Role: adapter.RoleUser, Content: "ask"},
		{Role: adapter.RoleTool, Content: giant, ToolCallID: "t1"},
		{Role: adapter.RoleAssistant, Content: "ack"},
	}
	out := composeSummarizedHistory(history, "## Decisions made\n(none)", 128_000)

	var toolMsg *adapter.Message
	for i := range out {
		if out[i].Role == adapter.RoleTool {
			toolMsg = &out[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatalf("retained tail should still contain the tool message")
	}
	if !strings.HasSuffix(toolMsg.Content, compactionMarker) {
		t.Errorf("oversize tool result should be truncated with marker; got tail %q", lastNChars(toolMsg.Content, 80))
	}
	if estimateMessageTokens(*toolMsg) > maxRetainedToolTokens+50 {
		t.Errorf("truncated tool tokens = %d, want ≤ %d", estimateMessageTokens(*toolMsg), maxRetainedToolTokens+50)
	}
}

// TestComposeSummarizedHistory_AlwaysRetainsMostRecentUserTurn locks
// the "current ask survives" invariant: even when the most recent
// turn's own size exceeds the budget, it is kept so the model can
// still see the user's pending question.
func TestComposeSummarizedHistory_AlwaysRetainsMostRecentUserTurn(t *testing.T) {
	// Single user turn far larger than the retain budget. With
	// windowTokens=8192 the budget is min budget (~4K tokens), and
	// the user turn alone is ~12K tokens.
	bigAsk := strings.Repeat("z", 48_000)
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
		{Role: adapter.RoleUser, Content: bigAsk},
		{Role: adapter.RoleAssistant, Content: "ack"},
	}
	out := composeSummarizedHistory(history, "## Decisions made\n(none)", 8192)

	foundUser := false
	for _, m := range out {
		if m.Role == adapter.RoleUser && m.Content == bigAsk {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Errorf("most-recent user turn must always be retained even when oversize")
	}
}

// TestRunSummarization_BudgetsInputAgainstWindow guards Fix C: when
// the rendered history exceeds the summarize input budget, oldest
// segments are dropped before the call so the request fits inside
// the model window. Without this, a session that grew past the
// model's real window would 400 on the summarize call instead of
// freeing context.
func TestRunSummarization_BudgetsInputAgainstWindow(t *testing.T) {
	captured := make(chan []adapter.Message, 1)
	stub := &capturingAdapter{
		out: []adapter.StreamEvent{
			{Kind: adapter.EventDone, Final: &adapter.Message{Role: adapter.RoleAssistant, Content: "## Decisions made\nbudgeted"}},
		},
		captured: captured,
	}

	// Build a history whose flattened size would exceed the 32K
	// window's input budget: 30 user turns, each carrying a 4K-char
	// user message. Total ~30K tokens of input segments, vs. a budget
	// of ~21K (32K window − summarize prompt − 8K reserved output −
	// 2K safety).
	history := []adapter.Message{}
	for i := 0; i < 30; i++ {
		marker := fmt.Sprintf("U%02d", i)
		history = append(history,
			adapter.Message{Role: adapter.RoleUser, Content: marker + strings.Repeat("p", 4000)},
			adapter.Message{Role: adapter.RoleAssistant, Content: "ok " + marker},
		)
	}

	_, _, err := runSummarization(context.Background(), stub, history, 32_000)
	if err != nil {
		t.Fatalf("runSummarization: %v", err)
	}
	msgs := <-captured
	if len(msgs) < 2 {
		t.Fatalf("expected at least system+user messages; got %d", len(msgs))
	}
	// The flattened user payload is messages[1]. Its size must fit
	// inside the budget so the provider doesn't 400.
	body := msgs[1].Content
	bodyTokens := (len(body) + 3) / 4
	budget := summarizeInputBudget(32_000)
	if bodyTokens > budget+500 { // small slack for the dropped-marker line
		t.Errorf("summarize input not budgeted: bodyTokens=%d budget=%d", bodyTokens, budget)
	}
	// The oldest turn must be dropped; the most recent must survive.
	if strings.Contains(body, "U00") {
		t.Errorf("oldest turn (U00) should be dropped under budget")
	}
	if !strings.Contains(body, "U29") {
		t.Errorf("most recent turn (U29) must survive")
	}
	if !strings.Contains(body, "earlier turns omitted") {
		t.Errorf("dropped-content marker missing from rendered body")
	}
}

// capturingAdapter is a scriptedAdapter variant that also records
// the messages sent into ChatStream, so input-budget tests can
// assert what the adapter actually received.
type capturingAdapter struct {
	out      []adapter.StreamEvent
	captured chan<- []adapter.Message
}

func (c *capturingAdapter) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	ch := make(chan adapter.StreamEvent, len(c.out)+1)
	go func() {
		defer close(ch)
		// Best-effort send; don't block the producer if the test
		// already drained.
		select {
		case c.captured <- msgs:
		default:
		}
		for _, ev := range c.out {
			ch <- ev
		}
	}()
	return ch
}

func firstNChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func lastNChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
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
	_, _, err := runSummarization(context.Background(), stub, nil, 0)
	if err == nil {
		t.Errorf("empty history should error")
	}
}

func TestRunSummarization_StreamsAndStructures(t *testing.T) {
	stub := &scriptedAdapter{turns: [][]adapter.StreamEvent{{
		{Kind: adapter.EventTokenDelta, Token: "## Decisions made\n(none)\n"},
		{Kind: adapter.EventDone, Final: &adapter.Message{Role: adapter.RoleAssistant, Content: "## Decisions made\n(none)"}},
	}}}
	out, _, err := runSummarization(context.Background(), stub, []adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, Content: "hello"},
	}, 0)
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
	cmd := m.updateContextUsage(true)
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
	cmd := m.updateContextUsage(true)
	if cmd != nil {
		t.Errorf("below threshold should not fire any cmd")
	}
	if strings.Contains(m.transcript.String(), "context at") {
		t.Errorf("no notice expected; got %q", m.transcript.String())
	}
}

// TestUpdateContextUsage_QueuedTurnSuppressesAuto is the regression
// test for the queued-input wedge: with a queued message about to
// start a turn (allowAuto=false), the auto branch must not fire at all
// — no banner, no summarizing flag — and the watermark gate must stay
// open so the suppressed check re-arms at the queued turn's end.
// Previously the caller discarded the returned Cmd instead, leaving
// the banner printed and m.summarizing wedged true with no
// summarization running.
func TestUpdateContextUsage_QueuedTurnSuppressesAuto(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Default()
	m.fileCfg.Context.WarnThreshold = 0.05
	m.fileCfg.Context.AutoThreshold = 0.1
	m.modelName = "gpt-4o"
	huge := strings.Repeat("x", 60_000) // ~15K tokens ≥ 10% of 128K
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: huge}}

	if cmd := m.updateContextUsage(false); cmd != nil {
		t.Fatal("allowAuto=false must not return a Cmd")
	}
	if m.summarizing {
		t.Error("allowAuto=false must not flip m.summarizing")
	}
	if strings.Contains(m.transcript.String(), "auto-summarizing") {
		t.Errorf("no auto banner expected; got %q", m.transcript.String())
	}
	if m.lastWatermarkPct != 0 {
		t.Errorf("watermark gate must stay open, got lastWatermarkPct=%f", m.lastWatermarkPct)
	}

	// The next unsuppressed check (the queued turn's end) fires normally.
	if cmd := m.updateContextUsage(true); cmd == nil {
		t.Fatal("follow-up unsuppressed check should fire auto-summarize")
	}
	if !m.summarizing {
		t.Error("follow-up check should flip m.summarizing")
	}
}

// TestResumeWatermarkCheck_HealsOversizedSession: a session resumed
// already past the auto threshold must start compressing at load time
// — not after the first send fails with the provider's
// context-overflow error.
func TestResumeWatermarkCheck_HealsOversizedSession(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Default()
	m.fileCfg.Context.AutoThreshold = 0.1
	m.modelName = "gpt-4o"
	huge := strings.Repeat("x", 60_000)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: huge}}

	out, cmd := m.update(resumeWatermarkCheckMsg{})
	mm := out.(Model)
	if cmd == nil {
		t.Fatal("resume check over auto threshold should return the summarize Cmd")
	}
	if !mm.summarizing {
		t.Error("resume check should flip summarizing")
	}
	if !strings.Contains(mm.transcript.String(), "auto-summarizing") {
		t.Errorf("expected auto-summarize banner; got %q", mm.transcript.String())
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
	cmd := m.updateContextUsage(true)
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
