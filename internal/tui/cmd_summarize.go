package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/session"
)

// retainBudgetFraction is the fraction of the model's context window
// allocated to verbatim-retained turns after compression. The
// remaining capacity is reserved for the system prompt, the synthetic
// summary, future user turns, and per-turn output. 0.4 keeps the
// compressed history far enough below the auto-summarize threshold
// that several more turns can land before compression fires again —
// without it, an agent-heavy session would re-trigger summarization
// on essentially unchanged content.
const retainBudgetFraction = 0.4

// minRetainBudget is the floor on the retain budget, used when the
// caller doesn't know the model's window (test paths, exotic
// providers). Picked to fit a single small turn comfortably.
const minRetainBudget = 4096

// maxRetainedToolTokens caps any single retained tool result so one
// oversized payload (Agent report, large file read) can't blow the
// retention budget for the whole tail. The truncation marker keeps
// the model aware that content was dropped rather than silently
// shortening.
const maxRetainedToolTokens = 4096

// minRetainedToolTokens is the floor below which a retained tool
// result's budget-aware cap will not shrink further — enough to keep a
// meaningful fragment (not just the truncation marker) visible even
// when many oversized tool messages compete for the same budget.
const minRetainedToolTokens = 256

// compactionMarker is appended to retained tool content that was
// truncated by the budget. Distinct from the inline summarize-time
// truncation marker so an audit can tell the two apart.
const compactionMarker = "…(truncated by compaction)"

// summaryUserPreamble is a tiny synthetic user turn placed immediately
// before the assistant summary in a compressed history. A history must not
// begin (after the system prompt) with an assistant turn: Claude rejects a
// messages array whose first entry isn't the user role, and Gemini requires
// strict user-first alternation — only OpenAI-style backends (and NVIDIA
// NIM) tolerate a leading assistant. The preamble makes the assistant
// summary a valid reply for every provider, mirroring how the subagent
// compaction path folds its summary into the task to the same end. Kept
// terse so it costs almost nothing.
const summaryUserPreamble = session.SummaryPreamble

// summarizeInputSafety is the absolute token margin held back beyond
// accounted-for overhead when budgeting the summarize input. Covers
// 4-chars-per-token estimation drift plus the chat-completions
// request envelope (role wrappers, JSON framing).
const summarizeInputSafety = 2048

// retainTurnsAfterSummary is the number of recent user/assistant turns
// kept verbatim after the synthetic summary message. Tuned to "enough
// recency context that the next prompt has working memory of the very
// last exchange, but few enough that the compression actually saves
// tokens." Tool messages associated with a retained assistant turn
// travel with it. It is also an upper bound — the byte-budget walk in
// composeSummarizedHistory may keep fewer turns when retained content
// would exceed the budget.
const retainTurnsAfterSummary = 5

// summarizeTimeout is the wall-clock cap on a single compression call.
// Auto-summarization runs against a transcript that, by definition,
// is near the model's context window. On slow providers (NVIDIA NIM,
// remote Ollama, anything CPU-bound) prefilling a 200K+ token prompt
// can take well over two minutes before the first output token lands.
// Five minutes matches the longest reasonable wait for a single
// compression while still surfacing as "stuck" if the stream truly
// stalls.
const summarizeTimeout = 5 * time.Minute

// summarizationPrompt is the system message asking the model for a
// structured four-section compression of the session so far. The
// shape is deliberately fixed so callers can rely on the headers
// existing even when a section is empty.
const summarizationPrompt = `You compress an in-progress coding-agent session into a structured
summary so the agent can keep working with bounded context.

Produce EXACTLY these four sections, in this order, even if a section
is empty (write "(none)" in that case):

## Decisions made
Finalized choices the user committed to.

## Code changes
Files touched, functions added or removed, patterns established.

## Open questions
Unresolved items the user raised.

## Preferences expressed
Style, tooling, or workflow preferences the user stated.

Be concrete and terse. Omit pleasantries, restatements of the prompt,
and exploratory tangents. Do not invent. If a section truly has
nothing to record, write "(none)".`

// summaryDoneMsg fires when a /summarize compression run finishes.
// Carries the new history (system + summary + recent turns), the path
// where the pre-compression snapshot was written, and any error. A
// non-nil err is rendered to the transcript and the existing history
// is preserved.
type summaryDoneMsg struct {
	auto         bool
	newMessages  []adapter.Message
	snapshotPath string
	tokensBefore int
	tokensAfter  int
	err          error
	fallbacks    []adapter.StreamEvent
	degraded     bool
	// compactionSeq is the mid-turn compaction generation captured when
	// summarizeCmd cloned history; a changed value makes the result stale.
	compactionSeq int
}

// cmdSummarize is the /summarize handler. Kicks off summarizeCmd and
// reports the in-progress state immediately so the user sees the
// command was accepted.
func cmdSummarize(m Model, _ []string) (Model, tea.Cmd) {
	if m.summarizing {
		m.appendLine(styleAuto.Render(SysMsg(SysState, "summarize", "already running")))
		return m, nil
	}
	if !hasSummarizableHistory(m.sess.Messages) {
		m.appendLine(styleAuto.Render(SysMsg(SysState, "summarize", "nothing to compress yet")))
		return m, nil
	}
	m.summarizing = true
	m.summarizeStart = time.Now()
	m.appendLine(styleAuto.Render(SysMsg(SysProgress, "summarize", "compressing session")))
	// Batch the spinner tick so the live summarizing row animates while
	// the (multi-minute) compression runs between turns.
	return m, tea.Batch(m.spinner.Tick, m.summarizeCmd(false))
}

// hasSummarizableHistory reports whether the session has enough
// non-system turns to bother compressing. A fresh session with only a
// system prompt has nothing to summarize.
func hasSummarizableHistory(messages []adapter.Message) bool {
	for _, m := range messages {
		if m.Role == adapter.RoleUser || m.Role == adapter.RoleAssistant {
			return true
		}
	}
	return false
}

// summarizeCmd is the worker that performs one compression round.
// Snapshots history, calls the model, splices the synthetic summary +
// recent turns back into the session, indexes the summary into
// /recall, and returns a summaryDoneMsg. Errors are soft — the caller
// shows them but keeps the original history.
func (m Model) summarizeCmd(auto bool) tea.Cmd {
	// Snapshot under histMu: /summarize is PreservesTurn=false, so it
	// cancels the active turn and then runs here while that turn's agent
	// goroutine may still be unwinding and appending to sess.Messages under
	// the same lock. An unlocked clone races the append's slice-header
	// reassignment (and can crash on an append-triggered realloc).
	m.histMu.Lock()
	history := slices.Clone(m.sess.Messages)
	m.histMu.Unlock()
	// Compaction is a single isolated call on a near-full context. Auto
	// routing uses the fast model until repeated failures degrade to smart.
	ad, degraded := m.chooseSummarizer()
	parentCtx := m.parentCtx
	sessID := m.sess.ID
	seq := m.compactionSeq
	tokensBefore := m.contextTokens
	// Snapshot the window outside the goroutine so we still hold a
	// stable view of model + fileCfg. Used to budget both the
	// summarize input and the retained tail.
	windowTokens := catalog.ResolveWindowForProvider(m.fileCfg.ProviderKindForModel(m.modelName), m.modelName, m.fileCfg.ContextWindowOverride(m.modelName), m.fileCfg.Context.DefaultWindow)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, summarizeTimeout)
		defer cancel()

		summary, fallbacks, err := runSummarization(ctx, ad, history, windowTokens)
		if err != nil {
			return summaryDoneMsg{auto: auto, err: err, degraded: degraded}
		}
		snapshotPath, err := writePreSummarySnapshot(sessID, history)
		if err != nil {
			return summaryDoneMsg{auto: auto, err: fmt.Errorf("snapshot: %w", err), degraded: degraded}
		}

		newHistory := composeSummarizedHistory(history, summary, windowTokens)
		return summaryDoneMsg{
			auto:          auto,
			newMessages:   newHistory,
			snapshotPath:  snapshotPath,
			tokensBefore:  tokensBefore,
			compactionSeq: seq,
			fallbacks:     fallbacks,
			degraded:      degraded,
		}
	}
}

// runSummarization streams the compression call and returns the model's
// output. Drops the system message from the input — we pass our own
// summarization prompt instead. windowTokens is the resolved context
// window for the active model; when > 0 the rendered transcript is
// budgeted so input + summarization prompt + reserved output fits
// inside the window. Pass 0 to disable budgeting (test paths,
// scripted stubs).
func runSummarization(ctx context.Context, ad agentStreamer, history []adapter.Message, windowTokens int) (string, []adapter.StreamEvent, error) {
	body := renderHistoryForSummarization(history, windowTokens)
	if strings.TrimSpace(body) == "" {
		return "", nil, errors.New("history is empty")
	}
	messages := []adapter.Message{
		{Role: adapter.RoleSystem, Content: summarizationPrompt},
		{Role: adapter.RoleUser, Content: body},
	}
	out := ad.ChatStream(ctx, messages, nil)
	var content strings.Builder
	var fallbacks []adapter.StreamEvent
	for ev := range out {
		switch ev.Kind {
		case adapter.EventTokenDelta:
			content.WriteString(ev.Token)
		case adapter.EventFallback:
			fallbacks = append(fallbacks, ev)
		case adapter.EventErr:
			return "", fallbacks, ev.Err
		case adapter.EventDone:
			if content.Len() == 0 && ev.Final != nil {
				content.WriteString(ev.Final.Content)
			}
		}
	}
	out2 := strings.TrimSpace(content.String())
	if out2 == "" {
		return "", fallbacks, errors.New("model returned empty summary")
	}
	return ensureFourSections(out2), fallbacks, nil
}

// agentStreamer is the slim interface the summarizer needs from the
// adapter. Mirrors the agent's Streamer interface so tests can pass a
// stub without depending on the agent package.
type agentStreamer interface {
	ChatStream(ctx context.Context, messages []adapter.Message, tools []adapter.Tool) <-chan adapter.StreamEvent
}

// summarizeDegradeThreshold is the number of consecutive fast-model
// summarization failures before auto routing degrades compaction to smart.
const summarizeDegradeThreshold = 2

// chooseSummarizer picks the streamer for a compaction call. In auto routing,
// repeated failures on the fast model degrade to the smart model so
// compaction keeps working during a fast-provider outage; off/manual falls
// through to the active model via summarizerAdapter/cfg.Adapter.
func (m Model) chooseSummarizer() (agentStreamer, bool) {
	if m.routerMode == config.RouterModeAuto && m.summarizeFailures >= summarizeDegradeThreshold && m.router != nil && m.router.Smart != nil {
		return m.router.Smart, true
	}
	if m.summarizerAdapter != nil {
		return m.summarizerAdapter, false
	}
	return m.cfg.Adapter, false
}

// summarizerOrDefault picks the routed summarizer adapter when present,
// else falls back to the session's main adapter. Keeps New() callers
// that don't wire routing (tests, oneshot) on the legacy behavior.
func summarizerOrDefault(routed, fallback agentStreamer) agentStreamer {
	if routed != nil {
		return routed
	}
	return fallback
}

// renderHistoryForSummarization flattens the message history into a
// single user message the compression model can read. Strips system
// messages (we send our own summarization prompt) and tool-call args
// (rarely useful for compression).
//
// When windowTokens > 0 the body is capped at a budget derived from
// the model's context window, leaving room for the summarization
// system prompt, the reserved per-turn output, and an estimation
// margin. Oldest segments are dropped first — the summarizer cares
// more about recent decisions and open questions than the early
// exploratory prompts. A leading marker is inserted whenever
// segments are dropped so the model knows the input is partial.
func renderHistoryForSummarization(history []adapter.Message, windowTokens int) string {
	segments := make([]string, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case adapter.RoleUser:
			segments = append(segments, "USER: "+strings.TrimSpace(m.Content)+"\n\n")
		case adapter.RoleAssistant:
			var seg strings.Builder
			if m.Content != "" {
				seg.WriteString("ASSISTANT: ")
				seg.WriteString(strings.TrimSpace(m.Content))
				seg.WriteString("\n\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&seg, "ASSISTANT used tool: %s\n", tc.Name)
			}
			if seg.Len() > 0 {
				segments = append(segments, seg.String())
			}
		case adapter.RoleTool:
			out := strings.TrimSpace(m.Content)
			if len(out) > 400 {
				out = out[:400] + "…(truncated)"
			}
			segments = append(segments, "TOOL_RESULT: "+out+"\n\n")
		}
	}

	if windowTokens <= 0 {
		return strings.TrimSpace(strings.Join(segments, ""))
	}
	budget := summarizeInputBudget(windowTokens)
	if budget <= 0 {
		// Window is so small even the prompt overhead doesn't fit.
		// Return the full body and let the provider surface the
		// error — silently producing an empty body would be worse.
		return strings.TrimSpace(strings.Join(segments, ""))
	}

	used := 0
	keepFromIdx := len(segments)
	for i := len(segments) - 1; i >= 0; i-- {
		cost := (len(segments[i]) + 3) / 4
		if used+cost > budget && keepFromIdx < len(segments) {
			break
		}
		used += cost
		keepFromIdx = i
	}
	if keepFromIdx == 0 {
		return strings.TrimSpace(strings.Join(segments, ""))
	}
	var b strings.Builder
	b.WriteString("[earlier turns omitted due to compaction budget]\n\n")
	for i := keepFromIdx; i < len(segments); i++ {
		b.WriteString(segments[i])
	}
	return strings.TrimSpace(b.String())
}

// summarizeInputBudget computes how many tokens we can spend on the
// flattened transcript, leaving headroom for the summarization
// system prompt, the reserved per-turn output (matched to the chat
// adapter's default), and an estimation margin.
func summarizeInputBudget(windowTokens int) int {
	overhead := (len(summarizationPrompt) + 3) / 4
	return windowTokens - overhead - int(adapter.ChatDefaultMaxTokens) - summarizeInputSafety
}

// ensureFourSections adds any missing section header so downstream
// code can rely on the structure. The model is told to always emit
// all four; this catches drift gracefully without rejecting an
// otherwise-useful summary.
func ensureFourSections(s string) string {
	headers := []string{
		"## Decisions made",
		"## Code changes",
		"## Open questions",
		"## Preferences expressed",
	}
	out := s
	for _, h := range headers {
		if !strings.Contains(out, h) {
			out += "\n\n" + h + "\n(none)"
		}
	}
	return strings.TrimSpace(out)
}

// composeSummarizedHistory builds the new message list that replaces
// the session: system prompt (preserved) + a synthetic assistant
// summary message + the most recent user/assistant turns that fit
// within a budget derived from windowTokens.
//
// "Turn" here means a user message and the assistant reply that
// followed (plus any tool messages between them). Retention is
// byte-budgeted, not turn-count-only: in agent-heavy sessions a
// single user turn can carry hundreds of thousands of tokens worth
// of tool results, and keeping such a turn verbatim would leave the
// compressed history barely smaller than the original. The budget
// caps the retained tail so compression always makes useful progress
// regardless of how the agent-tool traffic is distributed across
// turns. retainTurnsAfterSummary remains an upper bound on turn
// count, and oversize tool results are truncated in place rather
// than dropped wholesale (so the model still sees that *something*
// happened, just not its full payload).
//
// The single most recent user turn is always retained, even if its
// own size exceeds the budget — the alternative would orphan the
// model's view of "what is the user currently asking?".
func composeSummarizedHistory(history []adapter.Message, summary string, windowTokens int) []adapter.Message {
	out := make([]adapter.Message, 0, len(history))
	for _, m := range history {
		if m.Role == adapter.RoleSystem {
			out = append(out, m)
		}
	}

	var userIdxs []int
	for i, m := range history {
		if m.Role == adapter.RoleUser {
			userIdxs = append(userIdxs, i)
		}
	}

	// Synthetic summary message — clearly labeled as a compression
	// artifact so the model knows it isn't its own prior reply. Turn
	// number is included so the user can correlate to a session
	// transcript. A tiny synthetic user turn precedes it so the
	// compressed history never opens (after the system prompt) with an
	// assistant turn — invalid for Claude/Gemini (see summaryUserPreamble).
	turnLabel := fmt.Sprintf("turn %d", len(userIdxs))
	compressedAt := time.Now()
	out = append(out,
		adapter.Message{Role: adapter.RoleUser, Content: summaryUserPreamble, Timestamp: &compressedAt},
		adapter.Message{
			Role:      adapter.RoleAssistant,
			Content:   "[Session summary — compressed at " + turnLabel + "]\n\n" + summary,
			Timestamp: &compressedAt,
		},
	)

	if len(userIdxs) == 0 {
		return out
	}

	budget := retainBudgetFor(windowTokens)
	keepFrom := chooseRetainStart(history, userIdxs, budget, retainTurnsAfterSummary)
	if keepFrom < 0 {
		return out
	}
	for _, m := range capRetainedToolBudget(history[keepFrom:], budget) {
		if m.Role == adapter.RoleSystem {
			continue
		}
		out = append(out, m)
	}
	return out
}

// retainBudgetFor derives the token budget for retained turns from
// the model's context window. Falls back to a small floor when the
// caller passes 0 (test paths, exotic providers without a known
// window).
func retainBudgetFor(windowTokens int) int {
	if windowTokens <= 0 {
		return minRetainBudget
	}
	budget := int(float64(windowTokens) * retainBudgetFraction)
	if budget < minRetainBudget {
		budget = minRetainBudget
	}
	return budget
}

// chooseRetainStart walks the user-turn index list from newest to
// oldest, accumulating turns until either the byte budget is
// exceeded or the turn cap is reached. Returns the history index at
// which retention should begin, or -1 when no turns should be kept.
// The most-recent turn is always included regardless of size — it
// represents the user's current ask and dropping it would leave the
// model without context for the next reply.
func chooseRetainStart(history []adapter.Message, userIdxs []int, budgetTokens, turnCap int) int {
	if len(userIdxs) == 0 {
		return -1
	}
	n := len(userIdxs)
	keepFrom := -1
	used := 0
	turnsKept := 0
	for i := n - 1; i >= 0; i-- {
		if turnsKept >= turnCap {
			break
		}
		turnStart := userIdxs[i]
		turnEnd := len(history)
		if i+1 < n {
			turnEnd = userIdxs[i+1]
		}
		turnTokens := 0
		for j := turnStart; j < turnEnd; j++ {
			if history[j].Role == adapter.RoleSystem {
				continue
			}
			turnTokens += estimateMessageTokens(history[j])
		}
		if turnsKept > 0 && used+turnTokens > budgetTokens {
			break
		}
		keepFrom = turnStart
		used += turnTokens
		turnsKept++
	}
	return keepFrom
}

// truncateToolMessage truncates an oversize tool result to capTokens so a
// giant payload (Agent report, large file read) can't blow a retention
// budget on its own. Leaves user/assistant messages untouched — those
// carry decisions and reasoning that survive cleanly past compression.
// Returns a copy with capped Content rather than mutating the input so
// callers can compare before/after.
//
// The size check below is redundant on the one call site today —
// capRetainedToolBudget already establishes raw > capTokens using a
// cached estimate before calling this — but it's what makes the
// function safe to call unconditionally for any future caller, and
// EstimateMessage is a cheap len()-based calculation, not a content
// scan, so paying it again here is not worth threading a "trust me"
// bool through the signature to avoid.
func truncateToolMessage(m adapter.Message, capTokens int) adapter.Message {
	if m.Role != adapter.RoleTool || estimateMessageTokens(m) <= capTokens {
		return m
	}
	maxChars := capTokens * 4
	if maxChars <= len(compactionMarker) {
		m.Content = compactionMarker
		return m
	}
	m.Content = m.Content[:maxChars-len(compactionMarker)] + compactionMarker
	return m
}

// capRetainedToolBudget shrinks the retained tail's tool messages so their
// COMBINED token cost fits budgetTokens whenever achievable without
// truncating any one below minRetainedToolTokens, via the shared
// contextwindow.ToolBudgetCaps — also used by the agent package's
// capRetainedToolMessages (internal/agent/compaction.go), which needs the
// identical algorithm for its own tail. Only the message-shaping details
// below (truncation marker, lazy-copy plumbing, return shape) differ per
// caller. Replaces a per-message-only cap (each oversize tool message
// truncated independently to maxRetainedToolTokens): correct for one giant
// payload, blind to a turn with MANY moderately-large tool results (e.g.
// 30+ tool calls in the single always-retained newest turn), whose fixed
// per-message caps could still sum to several times the retain budget.
// Non-tool messages are untouched.
func capRetainedToolBudget(tail []adapter.Message, budgetTokens int) []adapter.Message {
	toolIdxs, raw, caps := contextwindow.ToolBudgetCaps(tail, budgetTokens, maxRetainedToolTokens, minRetainedToolTokens)
	if len(toolIdxs) == 0 {
		return tail
	}

	// Lazily allocate: the common case (a tail that already fits) needs
	// no copy at all.
	var out []adapter.Message
	for k, i := range toolIdxs {
		if raw[k] <= caps[k] {
			continue
		}
		if out == nil {
			out = append([]adapter.Message(nil), tail...)
		}
		out[i] = truncateToolMessage(out[i], caps[k])
	}
	if out == nil {
		return tail
	}
	return out
}

// estimateMessageTokens estimates the token count of a single message.
// Delegates to contextwindow.EstimateMessage, which is the per-message
// (non-slice) form — so chooseRetainStart can still call this hundreds of
// times for a long history without a per-call allocation, while image cost
// stays accounted for identically everywhere.
func estimateMessageTokens(m adapter.Message) int {
	return contextwindow.EstimateMessage(m)
}

// writePreSummarySnapshot writes the pre-compression history to
// ~/.yottacode/sessions/<id>-pre-summary-<timestamp>.json so the user
// can recover or audit later. Returns the absolute path written.
func writePreSummarySnapshot(sessionID string, history []adapter.Message) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	path := filepath.Join(dir, sessionID+session.SnapshotMarker+stamp+".json")
	payload := struct {
		SessionID string            `json:"session_id"`
		Captured  time.Time         `json:"captured"`
		Messages  []adapter.Message `json:"messages"`
	}{
		SessionID: sessionID,
		Captured:  time.Now().UTC(),
		Messages:  history,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// snapshotResumeHint turns a pre-summary snapshot path into a copy-
// pasteable command that actually restores the full pre-compression
// history: `yottacode sessions resume <id>`, where id is the snapshot's
// filename stem (written by writePreSummarySnapshot as
// "<sessionID>-pre-summary-<stamp>"). session.Load already special-cases
// an id containing session.SnapshotMarker (session.IsSnapshotID) and
// routes it through loadSnapshot, which seeds a FRESH session from the
// archived messages — the same mechanism internal/tui/run.go's
// resumeHint uses for a live session's own id, just pointed at the
// archive instead; this mirrors that established phrasing. A prior
// version of this banner suggested "/recall <session-id>" instead — a
// truncated id fed into cmdRecall's unrelated full-text FTS5 search,
// which reliably produced "no matches" since /recall searches message
// content, not session ids.
func snapshotResumeHint(snapshotPath string) string {
	if strings.TrimSpace(snapshotPath) == "" {
		return ""
	}
	id := strings.TrimSuffix(filepath.Base(snapshotPath), ".json")
	// Guard against a malformed/unexpected path the same way the id-
	// extraction this replaced did: only emit a command for something
	// session.Load will actually resolve as a snapshot (IsSnapshotID),
	// rather than risk suggesting "yottacode sessions resume <garbage>".
	if !session.IsSnapshotID(id) {
		return ""
	}
	return "yottacode sessions resume " + id
}

// summarizeDeps carries the dependencies loadSummarizedSession needs.
// Pulled out so callers that don't hold a Model (e.g. CLI startup
// before the TUI Model is constructed) can drive the same path.
type summarizeDeps struct {
	ctx     context.Context
	adapter agentStreamer
	fileCfg config.Config
}

func (m Model) summarizeDeps() summarizeDeps {
	ad := m.summarizerAdapter
	if ad == nil {
		ad = m.cfg.Adapter
	}
	return summarizeDeps{
		ctx:     m.parentCtx,
		adapter: ad,
		fileCfg: m.fileCfg,
	}
}

// loadSummarizedSession is /resume --summarized's loader. Returns a
// new session whose user/assistant history is replaced by the saved
// summary (or one freshly produced from the prior transcript when no
// snapshot exists). The system prompt is rewritten to include a
// "## Prior session context (summarized)" section.
//
// If the prior session is short enough to fit under the warn
// threshold, the function reports that and returns the original
// session unmodified — there's no benefit to compressing what already
// fits.
func loadSummarizedSession(deps summarizeDeps, loaded *session.Session) (*session.Session, string, string, error) {
	tokens := contextwindow.EstimateTokens(loaded.Messages)
	window := catalog.ResolveWindowForProvider(deps.fileCfg.ProviderKindForModel(loaded.Model), loaded.Model, deps.fileCfg.ContextWindowOverride(loaded.Model), deps.fileCfg.Context.DefaultWindow)
	warnThr := deps.fileCfg.Context.WarnThreshold
	if warnThr <= 0 || warnThr > 1 {
		warnThr = 0.65
	}
	if window > 0 && float64(tokens) < float64(window)*warnThr {
		// Fits comfortably — no compression needed.
		return loaded, "", fmt.Sprintf("[resume --summarized] full transcript fits under %d%% — loading verbatim",
			int(warnThr*100)), nil
	}

	summary, source, err := findOrComputeSummary(deps, loaded)
	if err != nil {
		return nil, "", "", err
	}

	newMessages := injectSummaryIntoSystemPrompt(loaded.Messages, summary)
	loaded.Messages = newMessages
	note := fmt.Sprintf("[resume --summarized] injected %s (~%d → ~%d tokens)",
		source, tokens, contextwindow.EstimateTokens(newMessages))
	return loaded, summary, note, nil
}

// findOrComputeSummary looks for the newest pre-summary snapshot for
// the given session. If one exists, the summary inside its
// post-compression payload is returned. Otherwise the prior transcript
// is compressed on the fly. Source is a short label ("snapshot" /
// "fresh summary") for the audit notice.
func findOrComputeSummary(deps summarizeDeps, sess *session.Session) (string, string, error) {
	if snap, err := readNewestSnapshot(sess.ID); err == nil && snap != "" {
		return snap, "snapshot", nil
	}

	// On-the-fly: run the compression now against the loaded transcript.
	// Skip if there's nothing useful to compress.
	if !hasSummarizableHistory(sess.Messages) {
		return "(no prior turns to summarize)", "empty", nil
	}
	ctx, cancel := context.WithTimeout(deps.ctx, summarizeTimeout)
	defer cancel()
	windowTokens := catalog.ResolveWindowForProvider(deps.fileCfg.ProviderKindForModel(sess.Model), sess.Model, deps.fileCfg.ContextWindowOverride(sess.Model), deps.fileCfg.Context.DefaultWindow)
	summary, _, err := runSummarization(ctx, deps.adapter, sess.Messages, windowTokens)
	if err != nil {
		return "", "", fmt.Errorf("on-the-fly summarize: %w", err)
	}
	// Best-effort: persist the freshly-computed summary as a snapshot
	// so subsequent /resume --summarized calls reuse it.
	if _, werr := writePreSummarySnapshot(sess.ID, sess.Messages); werr != nil {
		// Snapshot save failure is soft — log to transcript later if
		// needed; the summary we have is still valid.
		_ = werr
	}
	return summary, "fresh summary", nil
}

// readNewestSnapshot scans ~/.yottacode/sessions/ for files matching
// "<id>-pre-summary-*.json" and returns the embedded summary message
// from the newest one. Returns "" with no error when no snapshot
// exists for this session.
func readNewestSnapshot(sessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	prefix := sessionID + session.SnapshotMarker
	var newest string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if name > newest {
			newest = name
		}
	}
	if newest == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(dir, newest))
	if err != nil {
		return "", err
	}
	var payload struct {
		Messages []adapter.Message `json:"messages"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return "", fmt.Errorf("snapshot parse: %w", err)
	}
	// Walk backward looking for the synthetic summary message that the
	// compression flow wrote — identifiable by the prefix.
	for i := len(payload.Messages) - 1; i >= 0; i-- {
		msg := payload.Messages[i]
		if msg.Role == adapter.RoleAssistant && strings.HasPrefix(msg.Content, "[Session summary — compressed at") {
			// Drop the leading marker line so callers get just the
			// four sections.
			body := msg.Content
			if i := strings.Index(body, "\n\n"); i >= 0 {
				body = body[i+2:]
			}
			return strings.TrimSpace(body), nil
		}
	}
	// No embedded summary: the snapshot represents the pre-compression
	// state, which is too long to inject as-is. Fall back to "no
	// snapshot" so the caller computes a fresh summary.
	return "", nil
}

// injectSummaryIntoSystemPrompt rewrites the system message so it
// carries a labeled "## Prior session context (summarized)" section
// containing the compression artifact. Drops every non-system message
// — the summary IS the carried-forward state.
func injectSummaryIntoSystemPrompt(messages []adapter.Message, summary string) []adapter.Message {
	out := make([]adapter.Message, 0, 1)
	for _, m := range messages {
		if m.Role != adapter.RoleSystem {
			continue
		}
		base := strings.TrimRight(m.Content, "\n")
		if !strings.Contains(base, "## Prior session context (summarized)") {
			base += "\n\n## Prior session context (summarized)\n" + strings.TrimSpace(summary)
		}
		out = append(out, adapter.Message{Role: adapter.RoleSystem, Content: base})
	}
	if len(out) == 0 {
		// No system message in the loaded session: inject a synthetic
		// one so the summary is visible to the model.
		out = append(out, adapter.Message{
			Role:    adapter.RoleSystem,
			Content: "## Prior session context (summarized)\n" + strings.TrimSpace(summary),
		})
	}
	return out
}

// startAutoSummarize is invoked by the watermark check when usage
// crosses auto_threshold. Returns the same Cmd /summarize uses, but
// the auto flag changes the post-summary banner to the user-facing
// alert form.
func (m *Model) startAutoSummarize(pct float64) tea.Cmd {
	_ = pct // Progress is shown live; only the final summary receipt persists.
	if m.summarizing {
		return nil
	}
	if !hasSummarizableHistory(m.sess.Messages) {
		return nil
	}
	m.summarizing = true
	m.summarizeStart = time.Now()
	// The live footer already shows summarization progress; keep scrollback for
	// the final success/failure receipt instead of persisting a warning-colored
	// preflight line for routine housekeeping.
	return tea.Batch(m.spinner.Tick, m.summarizeCmd(true))
}
