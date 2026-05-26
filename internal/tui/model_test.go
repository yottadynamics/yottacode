package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/session"
)

// newTestModel returns a Model with stubbed wiring suitable for unit tests
// that exercise UI mechanics (key handling, rendering, state transitions).
// The adapter is nil — startTurn must not be triggered in these tests.
func newTestModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	sess, err := session.New("test-model", "/cwd")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	cfg := agent.LoopConfig{Registry: agent.NewRegistry(), MaxIterations: 5}
	cwd := t.TempDir()
	// Empty permissions store anchored at the test cwd. Tests that need
	// rules can stuff them in via permissionsLoadHelper or by writing
	// directly to <cwd>/.yottacode/permissions.json before calling Load.
	perms := permissions.LoadEmpty(cwd)
	m := New(context.Background(), Config{
		Cfg:               cfg,
		Session:           sess,
		Permissions:       perms,
		ModelName:         "test-model",
		BaseURL:           "http://test/v1",
		Cwd:               cwd,
		BypassPermissions: false,
	})
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

// applyMsg wraps Update for tests so they don't need to type-assert tea.Model.
func applyMsg(m Model, msg tea.Msg) (Model, tea.Cmd) {
	out, cmd := m.Update(msg)
	return out.(Model), cmd
}

func TestModel_WindowSizeMakesItReady(t *testing.T) {
	m := newTestModel(t)
	if !m.ready {
		t.Errorf("expected ready=true after WindowSizeMsg")
	}
	if m.width != 80 {
		t.Errorf("model width = %d, want 80", m.width)
	}
}

func TestModel_ViewBeforeReadyDoesNotCrash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sess, _ := session.New("m", "/cwd")
	cfg := agent.LoopConfig{Registry: agent.NewRegistry(), MaxIterations: 5}
	m := New(context.Background(), Config{
		Cfg:     cfg,
		Session: sess,
	})
	out := m.View()
	if !strings.Contains(out, "initializing") {
		t.Errorf("pre-ready View should say initializing; got %q", out)
	}
}

func TestModel_LargePasteShowsPlaceholderInsteadOfStretchingCmdline(t *testing.T) {
	m := newTestModel(t)
	// Simulate a bracketed paste of a large block — well over the
	// threshold. The visible textarea should show a short marker, not
	// the full content.
	big := strings.Repeat("x", 1000)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(big), Paste: true})

	val := m.textInput.Value()
	if strings.Contains(val, "x") && !strings.Contains(val, "Pasted text") {
		// Sanity check — if we see lots of 'x' AND no marker, the
		// paste went in raw.
		if len(val) > 50 {
			t.Errorf("large paste should have been replaced by a marker; got %d chars in input", len(val))
		}
	}
	if !strings.Contains(val, "[Pasted text #1") {
		t.Errorf("expected marker in input, got %q", val)
	}
	if !strings.Contains(val, "1000 bytes") {
		t.Errorf("marker should mention the byte count, got %q", val)
	}
}

func TestModel_SmallPasteGoesInVerbatim(t *testing.T) {
	m := newTestModel(t)
	// Below the threshold — should land in the textarea unchanged.
	small := "hello"
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(small), Paste: true})
	if got := m.textInput.Value(); got != small {
		t.Errorf("small paste should be inserted verbatim; got %q", got)
	}
	if strings.Contains(m.textInput.Value(), "Pasted text") {
		t.Errorf("small paste should not get a marker")
	}
}

func TestModel_PasteMarkerExpandsOnSubmit(t *testing.T) {
	m := newTestModel(t)
	big := strings.Repeat("y", 500)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(big), Paste: true})
	// Expand the markers manually (the same path used on submit).
	expanded := m.expandPastes(m.textInput.Value())
	if !strings.Contains(expanded, big) {
		t.Errorf("expandPastes should restore the original content; got %q", expanded)
	}
}

func TestModel_TypingBuffersInTextInput(t *testing.T) {
	m := newTestModel(t)
	for _, r := range "hello" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.textInput.Value(); got != "hello" {
		t.Errorf("textInput value = %q, want 'hello'", got)
	}
}

func TestModel_EnterOnEmptyInputIsNoop(t *testing.T) {
	m := newTestModel(t)
	out, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if out.turnActive {
		t.Errorf("empty input should not start a turn")
	}
	if cmd != nil {
		t.Errorf("expected no Cmd from empty submit; got %T", cmd)
	}
}

func TestModel_QuitSlashCommandReturnsQuit(t *testing.T) {
	m := newTestModel(t)
	for _, r := range "/quit" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected a tea.Quit Cmd")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("/quit should produce QuitMsg; got %T", msg)
	}
}

func TestModel_CtrlDQuits(t *testing.T) {
	m := newTestModel(t)
	_, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatalf("Ctrl+D should produce a Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Ctrl+D should QuitMsg")
	}
}

func TestModel_StreamingTokensRenderInTranscript(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "Hello "}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "world"}})
	v := m.View()
	if !strings.Contains(v, "Hello world") {
		t.Errorf("streaming tokens not rendered: %q", v)
	}
}

func TestModel_StreamingFlushesCompletedLinesToScrollback(t *testing.T) {
	// Lines terminated by \n should land in scrollback (transcript)
	// as they arrive, not stay buffered until end-of-turn. Only the
	// trailing partial line stays in the live footer.
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "line one\nline tw"}})
	if !strings.Contains(m.transcript.String(), "line one") {
		t.Errorf("completed line should flush to scrollback: %q", m.transcript.String())
	}
	if strings.Contains(m.transcript.String(), "line tw") {
		t.Errorf("partial trailing line should NOT be in scrollback yet: %q", m.transcript.String())
	}
	// Partial line should be visible in the live footer.
	if !strings.Contains(m.View(), "line tw") {
		t.Errorf("partial line should be in live footer preview: %q", m.View())
	}
}

func TestModel_StreamingMultipleNewlinesAtOnce(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "a\nb\nc"}})
	transcript := m.transcript.String()
	for _, want := range []string{"a", "b"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("scrollback should contain %q: %q", want, transcript)
		}
	}
	// "c" has no trailing newline so it stays partial.
	if !strings.Contains(m.View(), "c") {
		t.Errorf("partial 'c' should be in live preview: %q", m.View())
	}
}

func TestModel_CommitStreamingFlushesPartialLine(t *testing.T) {
	// At end-of-turn, any trailing partial line in the streaming buffer
	// should be flushed to scrollback by commitStreaming.
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "no newline at end"}})
	// Partial line not yet in scrollback.
	if strings.Contains(m.transcript.String(), "no newline at end") {
		t.Fatalf("partial line should NOT be in scrollback before commit: %q", m.transcript.String())
	}
	// AssistantMessage triggers commitStreaming.
	m, _ = applyMsg(m, agentEventMsg{ev: agent.AssistantMessage{}})
	if !strings.Contains(m.transcript.String(), "no newline at end") {
		t.Errorf("commit should flush partial line: %q", m.transcript.String())
	}
}

func TestModel_StreamingCodeBlockBuffersUntilClose(t *testing.T) {
	// Inside a code block, lines should NOT go to scrollback live.
	// They buffer until the closing ``` and emit highlighted.
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{
		Text: "Here's some code:\n```go\nfunc main() {}\n",
	}})
	transcript := m.transcript.String()
	// Prose before the fence should be in scrollback.
	if !strings.Contains(transcript, "Here's some code:") {
		t.Errorf("prose before fence should stream live: %q", transcript)
	}
	// Fence markers are control syntax, not transcript content.
	if strings.Contains(transcript, "```go") || strings.Contains(transcript, "```") {
		t.Errorf("fence markers should not land in scrollback: %q", transcript)
	}
	// Code body should NOT yet be in scrollback — it's buffered.
	if strings.Contains(transcript, "func main()") {
		t.Errorf("code body should NOT be in scrollback yet (buffered): %q", transcript)
	}
	if !m.inCodeBlock {
		t.Errorf("model should be in code block state")
	}
	if m.codeBlockLang != "go" {
		t.Errorf("language hint = %q, want go", m.codeBlockLang)
	}
}

func TestExpandCodeTabs(t *testing.T) {
	plain := stripANSI(indentCodeBlockLine(expandCodeTabs("\tfmt.Println()")))
	if !strings.Contains(plain, "      fmt.Println()") {
		t.Fatalf("code tabs should expand before printing, got %q", plain)
	}
	if !strings.HasPrefix(plain, "│") {
		t.Fatalf("code block line should include left border, got %q", plain)
	}
}

func TestModel_StreamingCodeBlockEmitsHighlightedAtClose(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{
		Text: "```go\nfunc main() {\n\tfmt.Println(\"Hello, world!\")\n}\n```\n",
	}})
	transcript := m.transcript.String()
	// Highlighting produces ANSI escape codes between tokens, so the
	// raw substring "func main()" won't appear contiguously. Strip
	// ANSI before checking content; check for ANSI presence
	// separately.
	plain := stripANSI(transcript)
	if !strings.Contains(plain, "  func main()") {
		t.Errorf("closed code block should land in scrollback with indent: %q", plain)
	}
	if !strings.Contains(plain, "      fmt.Println") {
		t.Errorf("tab-indented code should expand to spaces under the code block indent: %q", plain)
	}
	if !strings.Contains(plain, "│") {
		t.Errorf("code block should include a left border: %q", plain)
	}
	if strings.Contains(plain, "```") {
		t.Errorf("fence markers should be hidden from scrollback: %q", plain)
	}
	if m.inCodeBlock {
		t.Errorf("closing fence should exit code block state")
	}
}

func TestModel_StreamingCodeBlockShowsLiveFooterNotice(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{
		Text: "```python\nprint('hi')\n",
	}})
	view := m.View()
	if !strings.Contains(view, "writing python") {
		t.Errorf("footer should show 'writing python' notice: %q", view)
	}
	if !strings.Contains(view, "lines") {
		t.Errorf("footer should mention line count: %q", view)
	}
}

func TestModel_StreamingPreviewCappedToOneRow(t *testing.T) {
	// Long single-line streams must not render past one terminal row in
	// the live footer — Bubbletea's inline renderer counts logical lines,
	// not wrapped rows, so a wrapping live footer leaves ghost rows in
	// scrollback when it shrinks at commit. The preview shows a "…" +
	// trailing slice of the in-flight buffer instead of the full text.
	m := newTestModel(t)
	long := strings.Repeat("a", 500)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: long}})
	preview := m.renderStreamingPreview()
	if preview == "" {
		t.Fatalf("preview should not be empty for a long in-flight buffer")
	}
	width := liveContentWidth(m.width)
	if got := len([]rune(preview)); got > width {
		t.Errorf("preview rune width = %d, want <= %d", got, width)
	}
	if !strings.HasPrefix(preview, "…") {
		t.Errorf("truncated preview should be prefixed with …, got %q", preview)
	}
}

func TestModel_StreamingPreviewShortBufferReturnedVerbatim(t *testing.T) {
	m := newTestModel(t)
	short := "hi there"
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: short}})
	if got := m.renderStreamingPreview(); got != short {
		t.Errorf("short buffer should pass through unchanged, got %q", got)
	}
}

func TestModel_StreamingUnclosedCodeBlockEmitsPlainOnCommit(t *testing.T) {
	// If the model crashes mid-block (no closing fence), commitStreaming
	// should flush the buffered code as plain text, not highlighted —
	// the body is incomplete and highlighting partial code can be
	// misleading.
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{
		Text: "```go\nfunc broken(",
	}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.AssistantMessage{}})
	transcript := stripANSI(m.transcript.String())
	if !strings.Contains(transcript, "  func broken(") {
		t.Errorf("partial code should still land in scrollback with indent: %q", transcript)
	}
}

func TestModel_StreamingDropsAtCommitGlamourReRender(t *testing.T) {
	// Plain text streamed line-by-line is the truth — at commit we
	// should NOT re-emit a glamour-rendered version (would double-paint).
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "**bold**\n"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.AssistantMessage{}})
	transcript := m.transcript.String()
	plain := stripANSI(transcript)
	if strings.Contains(plain, "**") {
		t.Errorf("bold markdown markers should be stripped: %q", plain)
	}
	if !strings.Contains(plain, "bold") {
		t.Errorf("bold text should remain visible: %q", plain)
	}
	// And the scrollback shouldn't contain TWO copies (raw + rendered).
	if strings.Count(plain, "bold") > 1 {
		t.Errorf("expected one copy of 'bold', got duplicated content: %q", plain)
	}
}

func TestModel_ReasoningTokensCountedAndStreamedLive(t *testing.T) {
	// Reasoning streams live in the footer above the input so the user
	// can watch the model think. It also ticks the token counter and
	// the Thinking indicator; it does NOT enter scrollback (the
	// transcript builder), only the in-flight live preview.
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-2 * time.Second)
	startTokens := m.statsTokens

	m, _ = applyMsg(m, agentEventMsg{ev: agent.ReasoningToken{Text: "lengthy chain of thought"}})

	if m.statsTokens != startTokens+1 {
		t.Errorf("reasoning should count toward stats; got %d", m.statsTokens)
	}
	if !strings.Contains(m.View(), "lengthy chain of thought") {
		t.Errorf("reasoning preview should appear live in View: %q", m.View())
	}
	if !strings.Contains(m.View(), "thinking") {
		t.Errorf("indicator row should still show 'Thinking…': %q", m.View())
	}
	if strings.Contains(m.transcript.String(), "lengthy chain of thought") {
		t.Errorf("reasoning must NOT enter scrollback (transcript): %q", m.transcript.String())
	}
}

func TestModel_ReasoningPreviewClearsOnContent(t *testing.T) {
	// Once the model finishes reasoning and starts writing the answer,
	// the live reasoning preview should disappear so the view
	// transitions cleanly from "thinking" to "answering."
	m := newTestModel(t)
	m.turnActive = true
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ReasoningToken{Text: "thinking..."}})
	if !strings.Contains(m.View(), "thinking...") {
		t.Fatalf("reasoning preview missing before content: %q", m.View())
	}
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "Answer."}})
	if strings.Contains(m.View(), "thinking...") {
		t.Errorf("reasoning preview should clear when content streams: %q", m.View())
	}
}

func TestModel_ReasoningPreviewClearsOnToolStart(t *testing.T) {
	// If the model reasons then calls a tool, the reasoning preview
	// should clear so the tool-call line in the transcript stands alone.
	m := newTestModel(t)
	m.turnActive = true
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ReasoningToken{Text: "deciding..."}})
	if !strings.Contains(m.View(), "deciding...") {
		t.Fatalf("reasoning preview missing before tool call: %q", m.View())
	}
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "read_file", Preview: "read_file(x.go)"}})
	if strings.Contains(m.View(), "deciding...") {
		t.Errorf("reasoning preview should clear when a tool fires: %q", m.View())
	}
}

func TestModel_ThinkingRowAboveInputWhenActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now()
	v := m.View()
	if !strings.Contains(v, "thinking") {
		t.Errorf("active turn should show 'Thinking…' indicator: %q", v)
	}
	if !strings.Contains(v, "Ctrl+C to cancel") {
		t.Errorf("thinking row should mention cancel hotkey: %q", v)
	}
	// The textarea (with its placeholder) must remain visible — the user
	// can compose their next message while the agent works.
	if !strings.Contains(v, "ask anything") {
		t.Errorf("input row should still be visible during a turn; got %q", v)
	}
	// Verify the Thinking indicator appears BEFORE the input prompt in
	// the rendered output (i.e., above it).
	thinkingIdx := strings.Index(v, "thinking")
	promptIdx := strings.Index(v, "ask anything")
	if thinkingIdx >= promptIdx {
		t.Errorf("thinking row should render above input prompt; thinking@%d input@%d", thinkingIdx, promptIdx)
	}
}

func TestModel_TypingAllowedWhileThinking(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	for _, r := range "queued" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.textInput.Value() != "queued" {
		t.Errorf("typing during thinking should accumulate in textarea; got %q", m.textInput.Value())
	}
}

func TestModel_EnterMidTurnQueuesNotSubmits(t *testing.T) {
	// Enter mid-turn no longer just stays silent — it captures the
	// message into pendingInputAfterTurn so turnEndedMsg can auto-
	// submit after the cancel unwinds. The textarea is cleared
	// because the input was consumed (it's the queue's job to hold
	// it now). What this test guards is the invariant that no NEW
	// session message gets appended directly from this key press —
	// the actual append happens later via startTurn's normal path.
	m := newTestModel(t)
	m.turnActive = true
	m.turnCancel = func() {}
	for _, r := range "queued" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	beforeMsgs := len(m.sess.Messages)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.sess.Messages) != beforeMsgs {
		t.Errorf("Enter during a turn should not directly append a session message; msgs grew from %d to %d", beforeMsgs, len(m.sess.Messages))
	}
	if m.pendingInputAfterTurn != "queued" {
		t.Errorf("Enter during a turn should stash input in pendingInputAfterTurn; got %q", m.pendingInputAfterTurn)
	}
	if m.textInput.Value() != "" {
		t.Errorf("textarea should clear after queueing; got %q", m.textInput.Value())
	}
}

func TestModel_CtrlDDuringThinkingQuits(t *testing.T) {
	// Reverted policy: Ctrl+D always quits. Trapping the user during a
	// turn was annoying — terminal apps should always honor the quit
	// shortcut.
	m := newTestModel(t)
	m.turnActive = true
	m.turnCancel = func() {}
	_, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatalf("Ctrl+D during a turn should still produce a Quit cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Ctrl+D during a turn should QuitMsg; got %T", cmd())
	}
}

func TestModel_StateChangingSlashDuringThinkingCancelsTurnAndRuns(t *testing.T) {
	// While the agent is mid-turn, a state-changing slash command (one
	// without PreservesTurn) should still work — but cancel the in-flight
	// call first so the agent goroutine doesn't keep scribbling
	// alongside. /sessions is a representative mutator: it can rename,
	// resume, export — definitely a "stop current work, do this" intent.
	m := newTestModel(t)
	m.turnActive = true
	canceled := false
	m.turnCancel = func() { canceled = true }

	for _, r := range "/sessions" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !canceled {
		t.Errorf("state-changing slash command during turn should cancel the turn first")
	}
}

func TestModel_PreservesTurnSlashDuringThinkingDoesNotCancel(t *testing.T) {
	// Commands marked PreservesTurn (e.g. /subagents, /help, /system,
	// /permissions) are read-only inspections — submitting them during
	// an active turn must NOT cancel the turn, otherwise viewing the
	// status of an in-flight foreground subagent would kill it. The
	// fix that introduced PreservesTurn was prompted by exactly this:
	// opening /subagents during a foreground Agent call cascaded a ctx
	// cancellation down through the parent loop and into the child
	// Turn, leaving the user staring at "runner_canceled (parent-turn-
	// canceled-or-deadline)" in the transcript.
	m := newTestModel(t)
	m.turnActive = true
	canceled := false
	m.turnCancel = func() { canceled = true }

	beforeLen := len(m.historyLines)
	for _, r := range "/help" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if canceled {
		t.Errorf("PreservesTurn slash command must NOT cancel the active turn")
	}
	if len(m.historyLines) <= beforeLen {
		t.Errorf("/help should still produce output even when the turn is preserved")
	}
}

func TestModel_UnknownSlashCommandDuringThinkingDoesNotCancel(t *testing.T) {
	// Typos on slash commands (e.g. /subagent singular instead of
	// /subagents) must NOT cancel the active turn. The command isn't
	// going to do anything meaningful — it'll just produce an
	// "unknown command" error — so canceling an in-flight foreground
	// subagent over a typo is pure user damage. Regression for the
	// specific case where `/subagent` killed a running subagent
	// before the error message even printed.
	m := newTestModel(t)
	m.turnActive = true
	canceled := false
	m.turnCancel = func() { canceled = true }

	for _, r := range "/subagent" { // typo: missing the trailing s
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if canceled {
		t.Errorf("unknown slash command must NOT cancel the active turn (a typo shouldn't kill in-flight work)")
	}
}

func TestModel_RegularMessageMidTurnQueuesForResubmission(t *testing.T) {
	// Non-slash Enter mid-turn must not submit a new session message
	// directly; it queues into pendingInputAfterTurn so the auto-
	// submit path in turnEndedMsg can run startTurn after the cancel
	// resolves. Mirrors the slash-mid-turn cancel-then-run flow but
	// for plain text.
	m := newTestModel(t)
	m.turnActive = true
	m.turnCancel = func() {}
	for _, r := range "hello world" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	beforeMsgs := len(m.sess.Messages)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.sess.Messages) != beforeMsgs {
		t.Errorf("regular Enter during a turn should not directly submit")
	}
	if m.pendingInputAfterTurn != "hello world" {
		t.Errorf("regular Enter should stash into pendingInputAfterTurn; got %q", m.pendingInputAfterTurn)
	}
	if m.textInput.Value() != "" {
		t.Errorf("textarea should clear after queueing; got %q", m.textInput.Value())
	}
}

// When permissions auto-allow a tool call, the [permissions] notice
// should summarize on a single line — the full preview body (file
// content, edit diff, etc.) is about to land in the unified tool card,
// so dumping it here too would just be visual noise. Regression for the
// case where a verbose multi-line preview (write_file's old shape, or
// any future tool that emits embedded content) would dump the entire
// file twice in scrollback.
func TestModel_ApprovalAutoRendersSingleLineSummary(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	multilinePreview := "write_file(main.go, 1234 bytes)\n  │ package main\n  │ \n  │ func main() {}\n"
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ApprovalAuto{
		ToolName: "write_file",
		Preview:  multilinePreview,
		Source:   "permissions",
	}})
	v := m.transcript.String()
	if !strings.Contains(v, "[permissions] write_file(main.go, 1234 bytes)") {
		t.Errorf("expected one-line auto-approval summary; got %q", v)
	}
	if strings.Contains(v, "│ package main") || strings.Contains(v, "func main()") {
		t.Errorf("auto-approval line must not embed the full preview body: %q", v)
	}
}

func TestModel_ApprovalAnswerResumesEventStream(t *testing.T) {
	// Regression test: after the user answers an approval prompt, the
	// model must re-issue waitForEvent so the agent's follow-up events
	// (ToolStart, ToolResult, content tokens, TurnDone) keep flowing into
	// the UI. Without this, approval-gated tools "appeared to do nothing"
	// because the goroutine ran but Bubbletea stopped reading.
	m := newTestModel(t)
	m.turnActive = true
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m.awaitingApproval = true
	m.approvalTool = "write_file"

	_, cmd := applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatalf("approval answer must return a Cmd that resumes the event pump")
	}
	// Drain the decision the model sent.
	select {
	case d := <-m.decisions:
		if d != agent.AllowOnce {
			t.Errorf("decision = %v, want AllowOnce", d)
		}
	default:
		t.Errorf("expected a decision on the channel")
	}
}

func TestModel_CtrlCDuringThinkingCancelsTurn(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	canceled := false
	m.turnCancel = func() { canceled = true }
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !canceled {
		t.Errorf("Ctrl+C during a turn should call turnCancel")
	}
}

// TurnDone no longer emits a "run completed in Ns" scrollback footer
// — the duration is shown in the live thinking indicator while the
// turn runs, and (after Phase 4d) in the tool-card headers. Catches a
// regression where someone re-adds the redundant footer.
func TestModel_TurnDoneEmitsNoRunCompletedFooter(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-7 * time.Second)
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})
	if strings.Contains(m.transcript.String(), "run completed in") {
		t.Errorf("TurnDone should not emit a 'run completed in ...' footer: %q", m.transcript.String())
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{194 * time.Second, "3m 14s"},
		{3661 * time.Second, "1h 01m"},
	}
	for _, c := range tests {
		if got := formatDuration(c.d); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestModel_ApprovalNeededShowsModalAndBlocksInput(t *testing.T) {
	m := newTestModel(t)
	m.decisions = make(chan agent.Decision, 1)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ApprovalNeeded{
		ToolName: "run_bash",
		Preview:  "rm -rf /",
	}})
	if !m.awaitingApproval {
		t.Fatalf("awaitingApproval should be true after ApprovalNeeded")
	}
	v := m.View()
	if !strings.Contains(v, "Approval needed") {
		t.Errorf("approval modal not rendered: %q", v)
	}
	if !strings.Contains(v, "rm -rf /") {
		t.Errorf("preview missing from modal: %q", v)
	}
	// Tool name lives in the header (right side, dim).
	if !strings.Contains(v, "run_bash") {
		t.Errorf("approval header should show the tool name: %q", v)
	}
	// Brackets-first hotkey style.
	if !strings.Contains(v, "[Y] yes") || !strings.Contains(v, "[N] no") {
		t.Errorf("approval modal should expose [Y]/[N] hotkeys: %q", v)
	}
}

func TestModel_ApprovalKeyPressYesEmitsAllowOnce(t *testing.T) {
	m := newTestModel(t)
	m.decisions = make(chan agent.Decision, 1)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ApprovalNeeded{ToolName: "x", Preview: "p"}})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if m.awaitingApproval {
		t.Errorf("modal should clear after y")
	}
	select {
	case d := <-m.decisions:
		if d != agent.AllowOnce {
			t.Errorf("y should send AllowOnce; got %v", d)
		}
	default:
		t.Errorf("expected a Decision on the channel")
	}
}

func TestModel_ApprovalKeyPressAlwaysEmitsAllowAlways(t *testing.T) {
	m := newTestModel(t)
	m.decisions = make(chan agent.Decision, 1)
	// Use a real run_bash call so DeriveAllowRule produces a sensible
	// rule (Bash(echo *)) — required for approvalAllowAlwaysOK to flip
	// true and the [a] keypress to be honored.
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ApprovalNeeded{
		ToolName: "run_bash",
		Preview:  "run_bash: echo hi",
		ArgsJSON: `{"command":"echo hi"}`,
	}})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	select {
	case d := <-m.decisions:
		if d != agent.AllowAlways {
			t.Errorf("a should send AllowAlways; got %v", d)
		}
	default:
		t.Errorf("expected a Decision on the channel")
	}
}

func TestModel_ApprovalKeyPressAlwaysSuppressedForCompoundBash(t *testing.T) {
	// Compound shell command: DeriveAllowRule returns ok=false, so the
	// modal disables [a] and a keypress is dropped (no Decision sent).
	m := newTestModel(t)
	m.decisions = make(chan agent.Decision, 1)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ApprovalNeeded{
		ToolName: "run_bash",
		Preview:  "run_bash: cd /tmp && rm -rf x",
		ArgsJSON: `{"command":"cd /tmp && rm -rf x"}`,
	}})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	select {
	case d := <-m.decisions:
		t.Errorf("compound bash should suppress [a]; got %v", d)
	default:
		// Expected: no decision emitted.
	}
}

func TestModel_ApprovalKeyPressNoEmitsDeny(t *testing.T) {
	m := newTestModel(t)
	m.decisions = make(chan agent.Decision, 1)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ApprovalNeeded{ToolName: "x", Preview: "p"}})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	select {
	case d := <-m.decisions:
		if d != agent.Deny {
			t.Errorf("n should send Deny; got %v", d)
		}
	default:
		t.Errorf("expected a Decision on the channel")
	}
}

// ToolStart now buffers — the unified tool card is emitted on
// ToolResult so the header + body + footer render as one block.
// While buffered, no scrollback is added; the live thinking row
// shows what's happening.
func TestModel_ToolStartBuffersUntilResult(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "read_file", Preview: "read_file(x.go)"}})
	if v := m.transcript.String(); strings.Contains(v, "read_file") {
		t.Errorf("ToolStart should not render scrollback before result: %q", v)
	}
	if m.pendingToolName != "read_file" {
		t.Errorf("ToolStart should buffer pendingToolName, got %q", m.pendingToolName)
	}
}

func TestModel_ErrorEventRendersInline(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ErrorEvent{Err: errSentinel}})
	v := m.transcript.String()
	if !strings.Contains(v, "boom") {
		t.Errorf("error not rendered: %q", v)
	}
}

// The status bar carries the dot+model+(provider tag) segment and a
// `ctx ████░░ <used> / <max> (<pct>%)` segment. Cwd, branch, and the
// old "provider=foo" form all used to live here — removed because
// they're set-and-forget keystroke noise. Surface for those lives in
// the welcome card and the slash commands now.
func TestModel_StatusBarShowsModelAndContext(t *testing.T) {
	m := newTestModel(t)
	plain := stripANSI(m.renderStatus())
	if !strings.Contains(plain, "test-model") {
		t.Errorf("status bar missing model name: %q", plain)
	}
	if !strings.Contains(plain, "ctx ") {
		t.Errorf("status bar missing ctx segment: %q", plain)
	}
	if !strings.Contains(plain, "%") {
		t.Errorf("status bar should render a percentage in the ctx segment: %q", plain)
	}
	// Negative invariants — these were removed in earlier passes and
	// should not regress.
	if strings.Contains(plain, "…") {
		t.Errorf("status bar should NOT include the truncated cwd glyph: %q", plain)
	}
	if strings.Contains(plain, "provider=") {
		t.Errorf("status bar should not include the legacy 'provider=' label form: %q", plain)
	}
	if strings.Contains(plain, "ctx=") {
		t.Errorf("status bar should not render the legacy ctx=N/N form: %q", plain)
	}
	if strings.Contains(plain, "▓") {
		t.Errorf("status bar should not render the legacy ▓ block character: %q", plain)
	}
}

func TestModel_HeaderShowsBrandingAndVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sess, _ := session.New("m", "/cwd")
	cfg := agent.LoopConfig{Registry: agent.NewRegistry(), MaxIterations: 5}
	m := New(context.Background(), Config{
		Cfg:       cfg,
		Session:   sess,
		ModelName: "qwen3.5",
		Cwd:       "/some/path",
		Version:   "9.9.9",
	})
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	// The startup box is emitted to scrollback on the first WindowSizeMsg —
	// inspect via the transcript builder, not the live footer.
	v := m.transcript.String()
	if !strings.Contains(v, "YottaCode") || !strings.Contains(v, "v9.9.9") {
		t.Errorf("startup box missing branding/version: %q", v)
	}
	if !strings.Contains(v, "qwen3.5") {
		t.Errorf("startup box missing model: %q", v)
	}
	if !strings.Contains(v, "/some/path") {
		t.Errorf("startup box missing cwd: %q", v)
	}
}

// CLI `--resume` builds the Model with a Session that already has a
// message history. The constructor must seed contextTokens from
// that history so the status-bar % is correct on the very first
// frame — without this fix a resumed session reads `ctx 0%` until
// the user fires a turn (which might never happen if they're
// resuming just to re-read scrollback).
func TestModel_NewSeedsContextTokensFromResumedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sess, _ := session.New("test-model", "/cwd")
	sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "system prompt"},
		{Role: adapter.RoleUser, Content: strings.Repeat("a", 4000)}, // ~1000 tokens
		{Role: adapter.RoleAssistant, Content: strings.Repeat("b", 8000)}, // ~2000 tokens
	}
	cfg := agent.LoopConfig{Registry: agent.NewRegistry(), MaxIterations: 5}
	m := New(context.Background(), Config{
		Cfg: cfg, Session: sess, ModelName: "test-model", Cwd: "/cwd",
	})
	if m.contextTokens == 0 {
		t.Errorf("New() should seed contextTokens from session messages; got 0")
	}
	if m.contextTokens < 2500 {
		t.Errorf("contextTokens (%d) should reflect ~3000-token history", m.contextTokens)
	}
}

func TestModel_SplashShowsProductName(t *testing.T) {
	m := newTestModel(t)
	v := m.transcript.String()
	if !strings.Contains(v, "YottaCode") {
		t.Errorf("startup box should show product name: %q", v)
	}
}

func TestModel_PromptIsChevron(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	if !strings.Contains(v, "❯") {
		t.Errorf("input prompt should use ❯ chevron: %q", v)
	}
}

// TestModel_PromptOnlyOnFirstWrappedRow guards against a regression where
// the textarea repeated the "❯ " prompt on every soft-wrapped row of a
// long unbroken input. The expected behavior: chevron only on the first
// display row, continuations get blank indent.
func TestModel_PromptOnlyOnFirstWrappedRow(t *testing.T) {
	m := newTestModel(t)
	long := strings.Repeat("e", 400)
	m.textInput.SetValue(long)
	m.fitTextareaHeight()
	v := m.View()
	if got := strings.Count(v, "❯"); got != 1 {
		t.Errorf("expected exactly 1 ❯ in view for soft-wrapped input, got %d:\n%s", got, v)
	}
}

// TestModel_FitTextareaHeightCountsLogicalLines locks in the contract
// that fitTextareaHeight sizes by logical (\n-separated) lines rather
// than visual wrapped rows. Visual wrapping is painted by
// renderInputBody, not by giving the textarea more rows; counting wrapped
// rows here would add empty padding rows below the cursor on long single
// lines.
func TestModel_FitTextareaHeightCountsLogicalLines(t *testing.T) {
	m := newTestModel(t)
	wrap := m.textInput.Width()
	if wrap < 2 {
		t.Fatalf("textarea width too small to test: %d", wrap)
	}
	// A long unbroken single line stays at height 1 — visual wrapping is
	// the renderer's job, not the textarea's height.
	m.textInput.SetValue(strings.Repeat("e", wrap*3))
	m.fitTextareaHeight()
	if got := m.textInput.Height(); got != 1 {
		t.Errorf("single logical line should keep height=1 regardless of wrap; got %d", got)
	}
	// Two logical lines (one explicit \n) should bump to 2.
	m.textInput.SetValue("first\nsecond")
	m.fitTextareaHeight()
	if got := m.textInput.Height(); got != 2 {
		t.Errorf("two logical lines should give height=2; got %d", got)
	}
}

// TestModel_TypingPastWrapKeepsChevronVisible guards against the
// "first line disappears as I cross the wrap boundary" regression. The
// textarea's internal repositionView (called inside Update) scrolls the
// viewport down when the cursor moves to a row outside the current
// viewport range; SetHeight afterward does not reset YOffset. Without
// pre-growing height before Update, typing one char past the wrap
// boundary scrolled the chevron-bearing row 0 off the top and it stayed
// there until the user typed more chars and triggered a different
// repaint. We simulate one-char-at-a-time typing across the boundary
// and assert the chevron remains in the rendered view at every step.
func TestModel_TypingPastWrapKeepsChevronVisible(t *testing.T) {
	m := newTestModel(t)
	wrap := m.textInput.Width()
	if wrap < 4 {
		t.Fatalf("textarea width too small to test: %d", wrap)
	}
	// Type wrap+5 chars one rune at a time (covers the boundary at
	// rs == wrap and rs == wrap+1 where the bug manifests).
	for i := 0; i < wrap+5; i++ {
		var c tea.Model = m
		c, _ = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		m = c.(Model)
		if !strings.Contains(m.View(), "❯") {
			t.Fatalf("chevron missing from view after typing char %d (content len=%d, wrap=%d, height=%d)", i+1, i+1, wrap, m.textInput.Height())
		}
	}
}

// TestModel_InputBoxNoPhantomBlankRows guards against a regression where
// the lipgloss frame's Width was equal to the textarea's SetWidth value;
// since lipgloss Width() includes padding while textarea SetWidth() does
// not, the frame's content area was 2 cols too narrow and lipgloss
// soft-wrapped each textarea row, producing blank rows between visible
// content. The fix is to set the box's Width to (terminal width - border)
// so the inside-padding area matches the textarea's output width.
func TestModel_InputBoxNoPhantomBlankRows(t *testing.T) {
	m := newTestModel(t)
	m.textInput.SetValue(strings.Repeat("e", 400))
	m.fitTextareaHeight()
	box := m.renderInputBox()
	rows := strings.Split(box, "\n")
	// Strip border rows (first and last). Each remaining row should
	// contain at least one 'e' — no blank rows in the middle of the
	// content block.
	if len(rows) < 3 {
		t.Fatalf("expected at least 3 rows in box, got %d:\n%s", len(rows), box)
	}
	content := rows[1 : len(rows)-1]
	// Walk content rows; a row counts as "content" if it has an 'e'.
	// Once we've seen a content row, no later row should be blank up
	// until the trailing buffer-fill area.
	sawContent := false
	for i, row := range content {
		hasE := strings.Contains(row, "e")
		if hasE {
			sawContent = true
			continue
		}
		// Blank/empty row encountered. If we've already started seeing
		// content, this would only be acceptable as part of the
		// trailing buffer fill. Look ahead: if any following row has
		// 'e', this is a phantom blank row in the middle of content.
		if sawContent {
			for _, later := range content[i+1:] {
				if strings.Contains(later, "e") {
					t.Errorf("phantom blank row detected between content rows in input box:\n%s", box)
					return
				}
			}
		}
	}
}

func TestModel_AssistantMessageRendersSources(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.AssistantMessage{Message: adapter.Message{
		Citations: []adapter.Citation{
			{Type: "url_citation", Title: "Example", URL: "https://example.com"},
			{Type: "file_citation", Filename: "notes.txt", FileID: "file_123"},
		},
	}}})
	got := m.transcript.String()
	for _, want := range []string{"sources:", "Example (https://example.com)", "notes.txt (file_123)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q:\n%s", want, got)
		}
	}
}

// Status bar segments are middle-dot separated, not pipe separated.
// Catches a regression to `│` or `|` (table-divider feel) per the
// Phase 3 spec.
func TestModel_StatusBarUsesMiddleDotSeparator(t *testing.T) {
	m := newTestModel(t)
	got := m.renderStatus()
	if !strings.Contains(got, " · ") {
		t.Errorf("status bar should join segments with middle-dot ` · `: %q", got)
	}
	if strings.Contains(got, " │ ") || strings.Contains(got, " | ") {
		t.Errorf("status bar should not contain pipe/box-drawing separators: %q", got)
	}
}

func TestModel_HeaderOmitsBranchWhenEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sess, _ := session.New("m", "/cwd")
	cfg := agent.LoopConfig{Registry: agent.NewRegistry()}
	m := New(context.Background(), Config{
		Cfg: cfg, Session: sess,
		Branch: "",
	})
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	if strings.Contains(m.transcript.String(), "branch=") {
		t.Errorf("startup box should not include 'branch=' when empty")
	}
}

func TestModel_HeaderShowsMemoryWhenLoaded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sess, _ := session.New("m", "/cwd")
	cfg := agent.LoopConfig{Registry: agent.NewRegistry()}
	m := New(context.Background(), Config{
		Cfg: cfg, Session: sess,
		MemorySummary: "USER+PROJECT",
	})
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	if !strings.Contains(m.transcript.String(), "mem=USER+PROJECT") {
		t.Errorf("startup box should show memory summary")
	}
}

func TestModel_StatsTokenCounterIncrements(t *testing.T) {
	m := newTestModel(t)
	if m.statsTokens != 0 {
		t.Fatalf("fresh model statsTokens = %d, want 0", m.statsTokens)
	}
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "a"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ContentToken{Text: "b"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ReasoningToken{Text: "c"}})
	if m.statsTokens != 3 {
		t.Errorf("statsTokens = %d, want 3", m.statsTokens)
	}
}

func TestModel_StatsToolCounterIncrements(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "x", Preview: "x()"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "y", Preview: "y()"}})
	if m.statsToolCalls != 2 {
		t.Errorf("statsToolCalls = %d, want 2", m.statsToolCalls)
	}
}

func TestModel_StatusShowsThinkingWhileTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = m.turnStart.Add(-1) // ensure non-zero elapsed
	m.turnTokens = 50
	got := m.renderTurnStatus()
	if !strings.Contains(got, "thinking") {
		t.Errorf("active turn should mention 'Thinking': %q", got)
	}
	if !strings.Contains(got, "tok/s") {
		t.Errorf("active turn should show tok/s: %q", got)
	}
}

func TestModel_TurnStatusShowsElapsedSeconds(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-7 * time.Second)
	got := m.renderTurnStatus()
	// 7s elapsed should appear in the indicator so the user can see it
	// climbing — that's the reassurance signal during long thinks.
	if !strings.Contains(got, "7s") {
		t.Errorf("elapsed seconds missing: %q", got)
	}
}

func TestModel_TurnStatusEmptyWhenIdle(t *testing.T) {
	m := newTestModel(t)
	if got := m.renderTurnStatus(); got != "" {
		t.Errorf("idle renderTurnStatus should be empty (callers hide it); got %q", got)
	}
}

// The round-trip counter (formerly "step N/M") sits AFTER the elapsed
// seconds and BEFORE the tok/s rate. Order matters: the cluster reads
// as "time spent · how many model round-trips · current throughput"
// left-to-right. A previous version put round before elapsed which
// made the row read as "thinking · round 5/25 · 23s" — fine in
// isolation but inconsistent with how the user thinks about a
// long-running turn (time first, then "where are we" detail).
func TestModel_TurnStatusRoundAppearsAfterElapsed(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-7 * time.Second)
	m.turnTokens = 50
	m.iterRound = 5
	m.iterMax = 25
	got := stripANSI(m.renderTurnStatus())
	idxElapsed := strings.Index(got, "7s")
	idxRound := strings.Index(got, "round 5/25")
	idxRate := strings.Index(got, "tok/s")
	if idxElapsed < 0 || idxRound < 0 || idxRate < 0 {
		t.Fatalf("missing segment: elapsed=%d round=%d rate=%d in %q", idxElapsed, idxRound, idxRate, got)
	}
	if !(idxElapsed < idxRound && idxRound < idxRate) {
		t.Errorf("expected order elapsed → round → rate, got positions elapsed=%d round=%d rate=%d in %q", idxElapsed, idxRound, idxRate, got)
	}
}

// Round-trip counter renders on iteration 1 too — earlier we
// elided on iter 1 to avoid "flashing meta" on simple turns, but
// the row looked dead during long pre-stream pauses where the
// model is still on its first response. Showing "round 1/25"
// gives the user a "the process is alive and on round 1 of 25"
// reassurance signal during the wait. The elision now only fires
// before IterationStart has run (iterMax == 0).
func TestModel_TurnStatusRoundShowsOnIterationOne(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-1 * time.Second)
	m.iterRound = 1
	m.iterMax = 25
	got := stripANSI(m.renderTurnStatus())
	if !strings.Contains(got, "round 1/25") {
		t.Errorf("iteration 1 should render 'round 1/25': %q", got)
	}
}

// Before IterationStart fires (iterMax == 0) the round segment
// elides — we'd render "round 0/0" which is meaningless. This
// window is short (the agent's first event of any turn) but the
// guard matters for the pre-event frames.
func TestModel_TurnStatusRoundElidesBeforeIterationStart(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-1 * time.Second)
	// iterRound and iterMax both 0 — no IterationStart yet.
	got := stripANSI(m.renderTurnStatus())
	if strings.Contains(got, "round ") {
		t.Errorf("pre-IterationStart row should not surface the round counter: %q", got)
	}
}

// Tok/s renders pre-stream too — reads as "0.0 tok/s" until the
// model starts emitting. Same "row looks dead during long pauses"
// fix as the round elision.
func TestModel_TurnStatusTokRateShownPreStream(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-3 * time.Second)
	m.turnTokens = 0
	got := stripANSI(m.renderTurnStatus())
	if !strings.Contains(got, "0.0 tok/s") {
		t.Errorf("pre-stream row should render '0.0 tok/s': %q", got)
	}
}

// TurnDone leaves a "› Thought for Ns" footnote in scrollback so the
// user has a quiet receipt of how long the turn took once it ends.
func TestModel_TurnDoneAppendsThoughtForFootnote(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-7 * time.Second)

	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "› Thought for") {
		t.Errorf("TurnDone should append the 'Thought for Ns' footnote: %q", got)
	}
	if !strings.Contains(got, "7s") {
		t.Errorf("footnote should include the elapsed seconds: %q", got)
	}
}

// Regression: tok/s must keep climbing on a tool-call-only turn where
// the model produces no reasoning summary and no body text — only
// function-call argument deltas. The Responses adapter (gpt-5* at low
// effort) emits StreamProgress heartbeats so the live indicator
// doesn't sit at "0.0 tok/s" the whole turn.
func TestModel_StreamProgressIncrementsTokRate(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-1 * time.Second)
	m.turnTokens = 0

	for i := 0; i < 5; i++ {
		m, _ = applyMsg(m, agentEventMsg{ev: agent.StreamProgress{}})
	}
	if m.turnTokens != 5 {
		t.Errorf("turnTokens = %d, want 5 (one per StreamProgress)", m.turnTokens)
	}
}

// "step N/M" was the old wording — the user had to ask what it meant.
// Renamed to "round N/M" because "round of model calls" is plain
// English. Guard against a regression to the ambiguous form.
func TestModel_TurnStatusUsesRoundNotStep(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now().Add(-1 * time.Second)
	m.iterRound = 3
	m.iterMax = 25
	got := stripANSI(m.renderTurnStatus())
	if !strings.Contains(got, "round 3/25") {
		t.Errorf("counter should read 'round 3/25': %q", got)
	}
	if strings.Contains(got, "step ") {
		t.Errorf("counter should not regress to 'step N/M': %q", got)
	}
}

func TestModel_HistoryUpRecallsPreviousPrompt(t *testing.T) {
	m := newTestModel(t)
	m.recordHistory("first")
	m.recordHistory("second")
	m, ok := m.historyBack()
	if !ok {
		t.Fatalf("historyBack should consume Up")
	}
	if got := m.textInput.Value(); got != "second" {
		t.Errorf("Up #1 should show most recent; got %q", got)
	}
	m, _ = m.historyBack()
	if got := m.textInput.Value(); got != "first" {
		t.Errorf("Up #2 should show older entry; got %q", got)
	}
	// Going further back at the start is a no-op.
	m, _ = m.historyBack()
	if got := m.textInput.Value(); got != "first" {
		t.Errorf("Up at oldest should stay; got %q", got)
	}
}

func TestModel_HistoryDownReturnsToDraft(t *testing.T) {
	m := newTestModel(t)
	m.textInput.SetValue("draft in progress")
	m.recordHistory("entry one")
	m, _ = m.historyBack() // draft saved, shows "entry one"
	if got := m.textInput.Value(); got != "entry one" {
		t.Fatalf("Up should show history; got %q", got)
	}
	m, ok := m.historyForward() // back to draft
	if !ok {
		t.Fatalf("historyForward should consume Down")
	}
	if got := m.textInput.Value(); got != "draft in progress" {
		t.Errorf("Down past most-recent should restore draft; got %q", got)
	}
}

func TestModel_HistoryDedupesAdjacentDuplicates(t *testing.T) {
	m := newTestModel(t)
	m.recordHistory("hello")
	m.recordHistory("hello") // same as previous — should not add
	if got := len(m.inputHistory); got != 1 {
		t.Errorf("adjacent duplicates should not double up; len = %d", got)
	}
}

func TestModel_HistoryEmptyIsNoop(t *testing.T) {
	m := newTestModel(t)
	_, ok := m.historyBack()
	if ok {
		t.Errorf("historyBack with empty history should return false")
	}
}

func TestModel_RecordHistorySkipsEmpty(t *testing.T) {
	m := newTestModel(t)
	m.recordHistory("")
	if len(m.inputHistory) != 0 {
		t.Errorf("empty input should not be recorded")
	}
}

// Up at the first logical line of a multi-line draft must reach history;
// Up while the cursor is on a later logical line must reach the textarea
// (where it moves the cursor up one row instead). This is the regression
// from the "history is broken" report — the dispatcher used to claim
// Up/Down unconditionally, swallowing intra-textarea cursor movement.
func TestModel_HistoryUpRespectsTextareaCursorMidDraft(t *testing.T) {
	m := newTestModel(t)
	m.recordHistory("earlier prompt")
	// Two-line draft. Cursor lands on line 1 (the second line).
	m.textInput.SetValue("line one\nline two")
	if got := m.textInput.Line(); got != 1 {
		t.Fatalf("setup: expected cursor on line 1, got %d", got)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	// History must NOT have replaced the value.
	if got := m.textInput.Value(); got != "line one\nline two" {
		t.Errorf("Up mid-draft should not steal to history; value = %q", got)
	}
	// Now move cursor to the first logical line and try again — Up should
	// claim the keystroke and replace the value with history.
	for m.textInput.Line() > 0 {
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(tea.KeyMsg{Type: tea.KeyUp})
		_ = cmd
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.textInput.Value(); got != "earlier prompt" {
		t.Errorf("Up at line 0 should recall history; value = %q", got)
	}
}

// Down at the last logical line must reach history (forward); Down on an
// earlier line must reach the textarea so the cursor moves down a row.
func TestModel_HistoryDownRespectsTextareaCursorMidDraft(t *testing.T) {
	m := newTestModel(t)
	m.textInput.SetValue("first line\nsecond line")
	// Move cursor to line 0 (first line) by sending Up through textarea
	// directly so the dispatcher's gating doesn't fire.
	for m.textInput.Line() > 0 {
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(tea.KeyMsg{Type: tea.KeyUp})
		_ = cmd
	}
	// Prime history so historyForward could fire if the gate let it.
	m.recordHistory("scratch")
	m, _ = m.historyBack()                                 // index now pointing inside history
	m.textInput.SetValue("first line\nsecond line")        // restore the multi-line draft
	for m.textInput.Line() > 0 {                           // re-anchor cursor on line 0
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(tea.KeyMsg{Type: tea.KeyUp})
		_ = cmd
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	// History should NOT have replaced the value mid-draft.
	if got := m.textInput.Value(); got != "first line\nsecond line" {
		t.Errorf("Down mid-draft should not steal to history; value = %q", got)
	}
}

// Single-line drafts always satisfy both edge conditions, so history
// keeps working as before for the common case.
func TestModel_HistoryUpStillWorksOnSingleLineDraft(t *testing.T) {
	m := newTestModel(t)
	m.recordHistory("prior")
	m.textInput.SetValue("draft")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.textInput.Value(); got != "prior" {
		t.Errorf("single-line Up should reach history; value = %q", got)
	}
}

// Slash invocations get recorded so ↑ recalls them. Without this, users
// could never recall a /provider use foo or /model claude-haiku-4-5
// they ran moments earlier.
func TestModel_SlashCommandIsRecordedInHistory(t *testing.T) {
	m := newTestModel(t)
	// /help is a no-op handler that just appends to the transcript —
	// safe to invoke in tests without a live agent.
	m, _ = m.runSlash("/help")
	if got := len(m.inputHistory); got != 1 {
		t.Fatalf("expected /help to be recorded; history len = %d", got)
	}
	if m.inputHistory[0] != "/help" {
		t.Errorf("history should preserve full slash input; got %q", m.inputHistory[0])
	}
}

// Even an unknown command should be recall-able so the user can fix the
// typo without retyping.
func TestModel_UnknownSlashCommandIsRecorded(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.runSlash("/nosuch arg1")
	if got := len(m.inputHistory); got != 1 || m.inputHistory[0] != "/nosuch arg1" {
		t.Errorf("unknown slash should still record; history = %v", m.inputHistory)
	}
}

func TestModel_TextareaAcceptsCtrlJForNewline(t *testing.T) {
	// We bound textarea.KeyMap.InsertNewline to ctrl+j so plain Enter
	// remains "submit." Type some text, send Ctrl+J, type more, then verify
	// the input value contains a newline.
	m := newTestModel(t)
	for _, r := range "first" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	for _, r := range "second" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	got := m.textInput.Value()
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("input missing typed words: %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("Ctrl+J should have inserted a newline; got %q", got)
	}
}

func TestSummarizeToolOutput_FirstLineTruncated(t *testing.T) {
	got := summarizeToolOutput("first line\nsecond line\nthird")
	if got != "first line" {
		t.Errorf("summary = %q, want 'first line'", got)
	}
}

func TestSummarizeToolOutput_LongLineCut(t *testing.T) {
	long := strings.Repeat("x", 250)
	got := summarizeToolOutput(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long line should be truncated with ellipsis; got %q", got)
	}
}

func TestSummarizeToolOutput_EmptyShowsPlaceholder(t *testing.T) {
	got := summarizeToolOutput("")
	if got != "(no output)" {
		t.Errorf("empty output = %q, want '(no output)'", got)
	}
}

func TestRenderAssistantLine_BulletSummaryPreserved(t *testing.T) {
	got := renderAssistantLine("• Edited internal/tui/model.go (+7 -1)")
	plain := stripANSI(got)
	if !strings.Contains(plain, "  •") || !strings.Contains(plain, "Edited") || !strings.Contains(plain, "internal/tui/model.go") {
		t.Errorf("assistant bullet summary should stay structured with indent: %q", plain)
	}
}

func TestRenderAssistantLine_SeparatorPreserved(t *testing.T) {
	got := renderAssistantLine("────────────────────")
	if !strings.Contains(got, "────────────────────") {
		t.Errorf("separator line should remain visible: %q", got)
	}
}

func TestRenderAssistantLine_MarkdownListMarkersPreserved(t *testing.T) {
	for _, line := range []string{"- item", "* item", "1. item"} {
		got := renderAssistantLine(line)
		plain := stripANSI(got)
		if !strings.Contains(plain, "  "+line) {
			t.Errorf("list line should preserve marker/body with assistant indent: got %q, want %q", plain, "  "+line)
		}
	}
}

func TestRenderAssistantLine_HeadingAndBlockquotePreserved(t *testing.T) {
	heading := stripANSI(renderAssistantLine("## Summary"))
	if !strings.Contains(heading, "  ## Summary") {
		t.Errorf("heading should stay visible with assistant indent: %q", heading)
	}
	quote := stripANSI(renderAssistantLine("> note"))
	if !strings.Contains(quote, "  │ note") {
		t.Errorf("blockquote should render with indented quote bar: %q", quote)
	}
}

// Regression: inlinePathRE used to greedily consume the trailing
// `\x1b[0m` reset of an inline-code-styled segment because its
// `[^\s:;,)\]]+` character class did not exclude the ESC byte. The
// reset's bytes (`[0m`) ended up inside the path replacement and
// surfaced in the terminal as visible literal `[0m` fragments after
// rendered inline code (e.g. "go test ./...[0m passes."). The regex
// now stops at any ESC byte so styled segments stay self-contained.
func TestInlinePathRE_DoesNotConsumeAdjacentAnsiReset(t *testing.T) {
	styled := "Run " + styleInlineCode.Render("go test ./...") + " passes."
	for _, m := range inlinePathRE.FindAllStringIndex(styled, -1) {
		if strings.ContainsRune(styled[m[0]:m[1]], '\x1b') {
			t.Errorf("inlinePathRE crossed an ANSI escape: matched %q",
				strings.ReplaceAll(styled[m[0]:m[1]], "\x1b", "<ESC>"))
		}
	}
}

// A single blank line between two prose paragraphs should render as
// exactly one blank line, not two. The previous version of
// renderAssistantBlock appended "" twice per source blank — a
// blank-line carrier returned (renderedLine="", extraBlank=true) and
// the loop unconditionally appended renderedLine plus another "" for
// the extraBlank flag. Bullet lists in particular ended up with a
// double gap above them, e.g.:
//
//	The project contains the following files:
//	[blank]
//	[blank]            ← bug; this second blank is the duplicate
//	  - go.mod
func TestRenderAssistantBlock_SingleBlankBetweenParagraphsNotDoubled(t *testing.T) {
	src := "The project contains the following files:\n\n- go.mod\n- go.sum\n"
	plain := stripANSI(renderAssistantBlock(src))
	if strings.Contains(plain, "\n\n\n") {
		t.Errorf("source blank lines should not be doubled in rendered output: %q", plain)
	}
	// And confirm we still preserve the single blank — paragraph
	// breaks should survive.
	if !strings.Contains(plain, "files:") || !strings.Contains(plain, "\n\n") {
		t.Errorf("paragraph break should still render as a single blank: %q", plain)
	}
}

// Forced blanks below headings still render — that path uses
// extraBlank=true with a non-empty renderedLine, distinct from the
// blank-source-line case.
func TestRenderAssistantBlock_HeadingStillFollowedByBlank(t *testing.T) {
	src := "## Summary\nbody line\n"
	plain := stripANSI(renderAssistantBlock(src))
	// Heading should be followed by a blank, then the body.
	if !strings.Contains(plain, "Summary") || !strings.Contains(plain, "body line") {
		t.Fatalf("expected heading and body to render: %q", plain)
	}
	idxHeading := strings.Index(plain, "Summary")
	idxBody := strings.Index(plain, "body line")
	between := plain[idxHeading:idxBody]
	if !strings.Contains(between, "\n\n") {
		t.Errorf("heading should be followed by a blank line before the body: %q", between)
	}
}

// ToolResult emits the unified tool card. Header carries the preview
// and duration; body / footer depend on the tool. We assert
// structural invariants here: the rounded-corner gutter chars (╭ ╰)
// appear, the preview is in the header, and the output ends up
// somewhere in the card.
func TestModel_ToolResultRendersUnifiedCard(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "x", Preview: "x()"}})
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{
		ToolName: "x",
		Output:   "wrote 11 bytes to /tmp/y",
	}})
	v := stripANSI(m.transcript.String())
	if !strings.Contains(v, "╭ x()") {
		t.Errorf("card header should carry the preview: %q", v)
	}
	if !strings.Contains(v, "│   wrote 11 bytes to /tmp/y") {
		t.Errorf("card body should carry the tool output: %q", v)
	}
	if !strings.Contains(v, "╰ ") {
		t.Errorf("card should end with a footer gutter `╰`: %q", v)
	}
}

func TestModel_TextareaStaysSingleRowForLongLine(t *testing.T) {
	// A long single-line input should NOT grow the textarea vertically.
	// Bubbles textarea Height is logical-line count and renders unused
	// slots as empty "❯ " padding rows; growing on visual-row count
	// produces an N-row tall input with only the last row of typed
	// text plus N-1 empty prompt rows below it. Letting height stay at
	// 1 means the textarea handles overflow via its own horizontal scroll.
	m := newTestModel(t)
	if h := m.textInput.Height(); h != 1 {
		t.Fatalf("initial input height = %d, want 1", h)
	}
	long := strings.Repeat("x", m.textInput.Width()*3)
	for _, r := range long {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if h := m.textInput.Height(); h != 1 {
		t.Errorf("textarea should stay at height 1 for a single long line; got %d", h)
	}
}

func TestModel_TextareaGrowsOnExplicitNewlines(t *testing.T) {
	// Ctrl+J inserts a real \n. Each newline adds a logical line slot
	// to the textarea, so a multi-line message renders with all rows
	// visible below the prompt — up to the 6-row cap.
	m := newTestModel(t)
	for i := 0; i < 3; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	if h := m.textInput.Height(); h != 4 {
		t.Errorf("3 explicit newlines should produce height 4; got %d", h)
	}
}

func TestModel_TextareaHeightCappedAtSixOnManyNewlines(t *testing.T) {
	// Even with 100 explicit \n's the textarea must not eat the screen.
	m := newTestModel(t)
	for i := 0; i < 100; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	if h := m.textInput.Height(); h > 6 {
		t.Errorf("textarea height should cap at 6; got %d", h)
	}
}

// The borderless input is left-aligned and capped at min(120, w-4).
// On a wide terminal it stops well short of the right edge so the
// empty input doesn't read as an empty runway. We assert the cap
// rather than terminal-equality since the previous `== m.width`
// behavior assumed a full-width bordered box.
func TestModel_InputBoxRespectsWidthCap(t *testing.T) {
	m := newTestModel(t)
	// At width 80, cap is 80-4 = 76. Empty input renders the chevron +
	// placeholder so the line itself is short — the cap is an upper
	// bound, not a target. Assert ≤ cap and that there's no border
	// drawing left over.
	view := m.renderInputBox()
	cap := inputContentWidth(m.width)
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > cap {
			t.Errorf("input row width = %d, want ≤ cap %d: %q", got, cap, line)
		}
		if strings.ContainsAny(line, "╭╮╯╰│") {
			t.Errorf("input should be borderless; rounded-frame chars found: %q", line)
		}
	}
}

// At a wide terminal width the cap is hard at 120 columns regardless
// of the actual terminal size. Catches regressions to a full-width
// input layout that would expand into "empty runway" territory.
func TestModel_InputBoxCapsAt120OnWideTerminal(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 200, Height: 24})
	if got := inputContentWidth(m.width); got != 120 {
		t.Errorf("input width cap on wide terminal = %d, want 120", got)
	}
}

func TestModel_MouseEventDoesNotPanic(t *testing.T) {
	// In inline mode the terminal owns scrollback; the model treats mouse
	// wheel as a no-op. This test just guards against a panic regression.
	m := newTestModel(t)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("mouse handler panicked: %v", r)
		}
	}()
	m, _ = applyMsg(m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
	})
}

// User block renders with a thin colored left bar (▎) per Phase 4a.
// The old inverse-pill ❯ marker is gone — the bar is enough of a
// visual anchor when scrolling back. Multi-line input gets a bar on
// every wrapped row.
func TestModel_UserBlockUsesThinLeftBar(t *testing.T) {
	m := newTestModel(t)
	m.appendLine(renderUserBlock("hello\nworld", m.width))
	v := stripANSI(m.transcript.String())
	if !strings.Contains(v, "▎ hello") || !strings.Contains(v, "▎ world") {
		t.Errorf("user block should render with ▎ left-bar marker: %q", v)
	}
	if strings.Contains(v, "❯ hello") {
		t.Errorf("user block should not use the legacy ❯ marker: %q", v)
	}
}

// A user input longer than the terminal width must be hard-wrapped
// and every wrapped row must carry the ▎ bar prefix. Without this,
// the terminal auto-wraps continuation rows to column 0 and the quoted
// block loses its left-margin alignment partway through.
func TestRenderUserBlock_LongLineHangIndentsUnderBar(t *testing.T) {
	long := "Can you scan the current codebase and identify any gaps in the core components such as memory, permissions, models, providers, any of the Github integrations etc"
	out := stripANSI(renderUserBlock(long, 80))
	rows := strings.Split(strings.Trim(out, "\n"), "\n")
	if len(rows) < 2 {
		t.Fatalf("expected at least two rows after wrap, got %d: %q", len(rows), rows)
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, "▎ ") {
			t.Errorf("row %d missing ▎ prefix: %q", i, row)
		}
	}
}

func TestAbbrevHome_ReplacesPrefix(t *testing.T) {
	home := "/home/test"
	t.Setenv("HOME", home)
	got := abbrevHome(home + "/proj/sub")
	if got != "~/proj/sub" {
		t.Errorf("abbrevHome = %q, want '~/proj/sub'", got)
	}
}

func TestAbbrevHome_LeavesNonHomeAlone(t *testing.T) {
	t.Setenv("HOME", "/home/other")
	got := abbrevHome("/etc/passwd")
	if got != "/etc/passwd" {
		t.Errorf("abbrevHome = %q, want path unchanged", got)
	}
}

// errSentinel is a stable error value used by the error-render test.
var errSentinel = &simpleErr{msg: "boom"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// TestModel_NoValueBuilderFields guards against a regression where someone
// makes a Model field a value-typed strings.Builder. Bubbletea's Update is a
// value-receiver method, so every Update call gets a fresh stack copy of
// Model. strings.Builder panics on copy-by-value once it has been written
// to, which manifests as a runtime panic in the TUI even when unit tests
// pass (test stack frames often happen to land at the same address by
// coincidence). The fix is to use *strings.Builder; this test enforces it.
func TestModel_NoValueBuilderFields(t *testing.T) {
	builderType := reflect.TypeOf(strings.Builder{})
	mt := reflect.TypeOf(Model{})
	for i := 0; i < mt.NumField(); i++ {
		f := mt.Field(i)
		if f.Type == builderType {
			t.Errorf("Model.%s is a value-typed strings.Builder; switch it to *strings.Builder — the value form crashes under Bubbletea's value-receiver Update", f.Name)
		}
	}
}

// typeRunes is a small helper that pumps each rune of s into the model as a
// separate KeyRunes message — the same shape bubbletea would deliver for a
// real keyboard. Single-rune messages mean each keystroke triggers the
// palette/pre-grow logic exactly once, matching production.
func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestModel_CtrlUKillsLineAndPopulatesRing(t *testing.T) {
	m := newTestModel(t)
	m = typeRunes(t, m, "hello world")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.textInput.Value(); got != "" {
		t.Errorf("Ctrl+U should clear the line; got %q", got)
	}
	if got := m.killRing; got != "hello world" {
		t.Errorf("kill ring = %q, want %q", got, "hello world")
	}
}

func TestModel_CtrlYYanksFromRing(t *testing.T) {
	m := newTestModel(t)
	m = typeRunes(t, m, "abc")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlY})
	if got := m.textInput.Value(); got != "abc" {
		t.Errorf("Ctrl+Y should restore killed text; got %q", got)
	}
	if got := m.killRing; got != "abc" {
		t.Errorf("kill ring should survive yank; got %q", got)
	}
}

func TestModel_CtrlUAtColumnZeroPreservesRing(t *testing.T) {
	m := newTestModel(t)
	m = typeRunes(t, m, "first")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	// Now the line is empty and the ring holds "first". A second Ctrl+U
	// at column 0 must not blank the ring.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.killRing; got != "first" {
		t.Errorf("Ctrl+U at column 0 should not overwrite ring; got %q", got)
	}
}

func TestModel_CtrlYWithEmptyRingIsNoop(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlY})
	if got := m.textInput.Value(); got != "" {
		t.Errorf("Ctrl+Y with empty ring should leave input empty; got %q", got)
	}
}

func TestModel_CtrlUKillsOnlyTextBeforeCursor(t *testing.T) {
	m := newTestModel(t)
	m = typeRunes(t, m, "left right")
	// Move cursor to between "left" and " right" — 4 lefts.
	for i := 0; i < len("right"); i++ {
		m.textInput, _ = m.textInput.Update(tea.KeyMsg{Type: tea.KeyLeft})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.textInput.Value(); got != "right" {
		t.Errorf("Ctrl+U should kill only text-before-cursor; got %q", got)
	}
	if got := m.killRing; got != "left " {
		t.Errorf("kill ring should be %q, got %q", "left ", got)
	}
}

func TestModel_CtrlUThenYankRoundTripWithUnicode(t *testing.T) {
	m := newTestModel(t)
	// Multi-byte runes — kill-and-yank must preserve them char-for-char.
	m = typeRunes(t, m, "héllo ✓ 漢字")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlY})
	if got := m.textInput.Value(); got != "héllo ✓ 漢字" {
		t.Errorf("round-trip should preserve unicode; got %q", got)
	}
}
