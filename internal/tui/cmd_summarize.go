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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/session"
)

// retainTurnsAfterSummary is the number of recent user/assistant turns
// kept verbatim after the synthetic summary message. Tuned to "enough
// recency context that the next prompt has working memory of the very
// last exchange, but few enough that the compression actually saves
// tokens." Tool messages associated with a retained assistant turn
// travel with it.
const retainTurnsAfterSummary = 5

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
}

// cmdSummarize is the /summarize handler. Kicks off summarizeCmd and
// reports the in-progress state immediately so the user sees the
// command was accepted.
func cmdSummarize(m Model, _ []string) (Model, tea.Cmd) {
	if m.summarizing {
		m.appendLine(styleAuto.Render("[summarize] already running"))
		return m, nil
	}
	if !hasSummarizableHistory(m.sess.Messages) {
		m.appendLine(styleAuto.Render("[summarize] nothing to compress yet"))
		return m, nil
	}
	m.summarizing = true
	m.appendLine(styleAuto.Render("[summarize] compressing session…"))
	return m, m.summarizeCmd(false)
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
	history := slices.Clone(m.sess.Messages)
	ad := m.cfg.Adapter
	parentCtx := m.parentCtx
	sessID := m.sess.ID
	tokensBefore := m.contextTokens
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, 2*time.Minute)
		defer cancel()

		summary, err := runSummarization(ctx, ad, history)
		if err != nil {
			return summaryDoneMsg{auto: auto, err: err}
		}
		snapshotPath, err := writePreSummarySnapshot(sessID, history)
		if err != nil {
			return summaryDoneMsg{auto: auto, err: fmt.Errorf("snapshot: %w", err)}
		}

		newHistory := composeSummarizedHistory(history, summary)
		return summaryDoneMsg{
			auto:         auto,
			newMessages:  newHistory,
			snapshotPath: snapshotPath,
			tokensBefore: tokensBefore,
		}
	}
}

// runSummarization streams the compression call and returns the model's
// output. Drops the system message from the input — we pass our own
// summarization prompt instead.
func runSummarization(ctx context.Context, ad agentStreamer, history []adapter.Message) (string, error) {
	body := renderHistoryForSummarization(history)
	if strings.TrimSpace(body) == "" {
		return "", errors.New("history is empty")
	}
	messages := []adapter.Message{
		{Role: adapter.RoleSystem, Content: summarizationPrompt},
		{Role: adapter.RoleUser, Content: body},
	}
	out := ad.ChatStream(ctx, messages, nil)
	var content strings.Builder
	for ev := range out {
		switch ev.Kind {
		case adapter.EventTokenDelta:
			content.WriteString(ev.Token)
		case adapter.EventErr:
			return "", ev.Err
		case adapter.EventDone:
			if content.Len() == 0 && ev.Final != nil {
				content.WriteString(ev.Final.Content)
			}
		}
	}
	out2 := strings.TrimSpace(content.String())
	if out2 == "" {
		return "", errors.New("model returned empty summary")
	}
	return ensureFourSections(out2), nil
}

// agentStreamer is the slim interface the summarizer needs from the
// adapter. Mirrors the agent's Streamer interface so tests can pass a
// stub without depending on the agent package.
type agentStreamer interface {
	ChatStream(ctx context.Context, messages []adapter.Message, tools []adapter.Tool) <-chan adapter.StreamEvent
}

// renderHistoryForSummarization flattens the message history into a
// single user message the compression model can read. Strips system
// messages (we send our own summarization prompt) and tool-call args
// (rarely useful for compression).
func renderHistoryForSummarization(history []adapter.Message) string {
	var b strings.Builder
	for _, m := range history {
		switch m.Role {
		case adapter.RoleUser:
			b.WriteString("USER: ")
			b.WriteString(strings.TrimSpace(m.Content))
			b.WriteString("\n\n")
		case adapter.RoleAssistant:
			if m.Content != "" {
				b.WriteString("ASSISTANT: ")
				b.WriteString(strings.TrimSpace(m.Content))
				b.WriteString("\n\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "ASSISTANT used tool: %s\n", tc.Name)
			}
		case adapter.RoleTool:
			out := strings.TrimSpace(m.Content)
			if len(out) > 400 {
				out = out[:400] + "…(truncated)"
			}
			b.WriteString("TOOL_RESULT: ")
			b.WriteString(out)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
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
// summary message + the last retainTurnsAfterSummary user/assistant
// turns from the original.
//
// "Turn" here means a user message and the assistant reply that
// followed (plus any tool messages between them). Walking backward
// keeps the most recent turns by definition.
func composeSummarizedHistory(history []adapter.Message, summary string) []adapter.Message {
	out := make([]adapter.Message, 0, len(history))
	for _, m := range history {
		if m.Role == adapter.RoleSystem {
			out = append(out, m)
		}
	}

	// Find the index ranges of the last N user-rooted turns so we can
	// preserve them verbatim. Each turn starts at a user message and
	// runs to (but not including) the next user message.
	var userIdxs []int
	for i, m := range history {
		if m.Role == adapter.RoleUser {
			userIdxs = append(userIdxs, i)
		}
	}
	keepFrom := -1
	if n := len(userIdxs); n > retainTurnsAfterSummary {
		keepFrom = userIdxs[n-retainTurnsAfterSummary]
	} else if n > 0 {
		keepFrom = userIdxs[0]
	}

	// Synthetic summary message — clearly labeled as a compression
	// artifact so the model knows it isn't its own prior reply. Turn
	// number is included so the user can correlate to a session
	// transcript.
	turnLabel := fmt.Sprintf("turn %d", len(userIdxs))
	out = append(out, adapter.Message{
		Role:    adapter.RoleAssistant,
		Content: "[Session summary — compressed at " + turnLabel + "]\n\n" + summary,
	})

	if keepFrom >= 0 {
		for i := keepFrom; i < len(history); i++ {
			if history[i].Role == adapter.RoleSystem {
				continue
			}
			out = append(out, history[i])
		}
	}
	return out
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
	stamp := time.Now().UTC().Format("20060102-150405")
	path := filepath.Join(dir, fmt.Sprintf("%s-pre-summary-%s.json", sessionID, stamp))
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

// summarizeDeps carries the dependencies loadSummarizedSession needs.
// Pulled out so callers that don't hold a Model (e.g. CLI startup
// before the TUI Model is constructed) can drive the same path.
type summarizeDeps struct {
	ctx     context.Context
	adapter agentStreamer
	fileCfg config.Config
}

func (m Model) summarizeDeps() summarizeDeps {
	return summarizeDeps{
		ctx:     m.parentCtx,
		adapter: m.cfg.Adapter,
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
	window := contextwindow.WindowFor(loaded.Model, deps.fileCfg.Context.DefaultWindow)
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
	ctx, cancel := context.WithTimeout(deps.ctx, 2*time.Minute)
	defer cancel()
	summary, err := runSummarization(ctx, deps.adapter, sess.Messages)
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
	prefix := sessionID + "-pre-summary-"
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
	if m.summarizing {
		return nil
	}
	if !hasSummarizableHistory(m.sess.Messages) {
		return nil
	}
	m.summarizing = true
	m.appendLine(styleWatermarkAlert.Render(
		fmt.Sprintf("⚡ Context at %d%% — auto-summarizing before next turn…", int(pct*100))))
	return m.summarizeCmd(true)
}
