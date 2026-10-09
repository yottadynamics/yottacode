package acp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/agentruntime"
	"github.com/yottadynamics/yottacode/internal/checkpoint"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// closeSessionDrainTimeout bounds how long CloseSession/Shutdown wait
// for an in-flight turn to actually finish unwinding after cancellation
// before persisting anyway. Cancellation should be observed almost
// immediately (every blocking point in internal/agent/loop.go selects on
// ctx.Done()), so this is a safety ceiling, not the expected case —
// mirrors internal/tui/run.go's subagentDrainGrace in spirit (bounded,
// not infinite).
const closeSessionDrainTimeout = 5 * time.Second

// acpSession is one live ACP session: the agentruntime.Runtime backing
// it, plus the per-turn cancellation state a concurrent session/cancel
// needs to reach. Mirrors the exact pattern internal/tui/model.go already
// uses for its own single in-flight turn (turnCtx/turnCancel), and the
// one coder/acp-go-sdk's own example/agent/main.go uses
// (map[string]*agentSession{cancel context.CancelFunc}) — see
// roadmap/acp-adapter.md's Concurrency section.
type acpSession struct {
	id  string
	srv *Server
	rt  *agentruntime.Runtime

	// mu guards cancel and turnActive: session/prompt sets cancel for the
	// duration of one turn, session/cancel reads and calls it. A session
	// is only ever SUPPOSED to host one in-flight Prompt call at a time
	// (the ACP client is expected to wait for a PromptResponse before
	// sending another session/prompt for the same session) — but nothing
	// at the transport layer enforces that (coder/acp-go-sdk dispatches
	// every inbound request in its own goroutine), so turnActive is the
	// actual enforcement: claimTurn/releaseTurn below reject a second
	// concurrent prompt for this session instead of letting two
	// goroutines race appends to the same rt.Session.Messages slice.
	mu         syncutil.Mutex
	cancel     context.CancelFunc
	turnActive bool
	closed     bool
	closing    bool
	historyMu  syncutil.Mutex
	// lifecycle serializes a prompt with session replacement and teardown.
	lifecycle syncutil.Mutex
	// turnWG tracks the in-flight prompt() call, if any — CloseSession

	// before exporting/saving so they never read rt.Session.Messages
	// while the turn goroutine is still mutating it.
	turnWG sync.WaitGroup
}

// waitForTurn blocks until any in-flight prompt() call finishes, timeout
// elapses, or ctx is done, whichever comes first — so a caller's own
// deadline (e.g. cmd/yottacode/acp.go's acpShutdownTimeout, threaded
// through Shutdown/CloseSession) bounds the wait too, not just the fixed
// closeSessionDrainTimeout ceiling.
func (s *acpSession) waitForTurn(ctx context.Context, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.turnWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func newACPSession(srv *Server, rt *agentruntime.Runtime) *acpSession {
	// Checkpoint setup is soft, matching the TUI: an unavailable store must
	// not make an ACP session unusable.
	if cp, err := checkpoint.New(""); err == nil {
		rt.Cfg.Checkpoints = cp
	}
	sess := &acpSession{srv: srv, rt: rt, id: rt.Session.ID}
	// Agent.Turn uses this lock for every history mutation, keeping ACP's
	// per-turn persistence and prompt recomposition from racing the loop.
	rt.Cfg.HistoryLock = &sess.historyMu
	return sess
}

// setCancel installs the current turn's cancel func (or clears it with
// nil once the turn ends). Returns the previous value so a caller can
// decide whether a turn was already in flight — not used yet in the
// skeleton, but the shape session/prompt (M6) needs is already right.
func (s *acpSession) setCancel(cancel context.CancelFunc) context.CancelFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.cancel
	s.cancel = cancel
	return prev
}

// claimTurn atomically claims this session's single in-flight-turn
// slot, returning false if a turn is already running. Mirrors
// internal/tui/model.go's own m.turnActive guard for the identical
// single-turn-at-a-time invariant — the TUI enforces it by construction
// (one Bubbletea update loop), ACP has to enforce it explicitly since
// concurrent session/prompt calls for the same session are a
// transport-level possibility even though the spec assumes clients
// don't send them.
func (s *acpSession) claimTurn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnActive || s.closed || s.closing {
		return false
	}
	s.turnActive = true
	// Add under the lifecycle lock so close/load cannot race Wait with Add.
	s.turnWG.Add(1)
	return true
}

// releaseTurn releases the slot claimTurn claimed.
func (s *acpSession) releaseTurn() {
	s.mu.Lock()
	s.turnActive = false
	s.turnWG.Done()
	s.mu.Unlock()
}

// requestCancel calls the in-flight turn's cancel func, if any. No-op
// when the session is idle (nil cancel) — matches the TUI's
// m.turnCancel nil-check before calling it.
func (s *acpSession) requestCancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Cancel looks up the session and cancels its in-flight turn, if any —
// the agent loop already handles context cancellation and emits
// TurnInterrupted, preserving history (see internal/agent/loop.go).
// session/cancel is a notification (no response), so an unknown session
// id is silently ignored rather than erroring: the client may have
// raced a cancel against a session that already closed.
func (s *Server) Cancel(_ context.Context, params coderacp.CancelNotification) error {
	sess, ok := s.session(string(params.SessionId))
	if !ok {
		return nil
	}
	sess.requestCancel()
	return nil
}

// Prompt appends the user's message and hosts one turn of agent.Turn,
// draining its event channel into session/update notifications
// (events.go) and bridging ApprovalNeeded/PathTrustElevationNeeded into
// session/request_permission round trips (permissions.go). Mirrors
// internal/tui/model.go's own turn-hosting pattern (turnCtx/turnCancel,
// buffered events(64)/decisions(1)/errCh(1), a recover()-guarded
// goroutine that closes events) — the one difference is that ACP's
// session/prompt is a request that blocks until the turn completes, so
// the draining loop lives directly in this method rather than being
// driven by an external event loop.
func (s *Server) Prompt(ctx context.Context, params coderacp.PromptRequest) (coderacp.PromptResponse, error) {
	sess, ok := s.session(string(params.SessionId))
	if !ok {
		return coderacp.PromptResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown session id"})
	}
	return sess.prompt(ctx, s.conn, params)
}

func (s *acpSession) prompt(ctx context.Context, conn *coderacp.AgentSideConnection, params coderacp.PromptRequest) (coderacp.PromptResponse, error) {
	if !s.claimTurn() {
		return coderacp.PromptResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "a turn is already in progress for this session"})
	}
	defer s.releaseTurn()
	s.mu.Lock()
	closing := s.closing || s.closed
	s.mu.Unlock()
	if closing {
		return coderacp.PromptResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "session is closing"})
	}

	text, err := promptText(params.Prompt)
	if err != nil {
		return coderacp.PromptResponse{}, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	// A "/<name> [args...]" message matching a registered
	// internal/promptmacros command (the same 9 bucket-A commands
	// internal/tui exposes as slash commands) substitutes the macro's
	// built prompt for the raw text before it ever reaches the agent
	// loop — mirrors internal/tui's dispatcher, which never lets a
	// slash command's literal text become the turn's user message. Text
	// that isn't a recognized macro (including a message that merely
	// starts with "/" some other way) falls through unchanged.
	if m, args, ok := matchMacroCommand(text); ok {
		built, err := m.Build(s.rt.CwdRef.Get(), args)
		if err != nil {
			return coderacp.PromptResponse{}, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		text = built
	}

	// Resolve @file references into the system prompt, never into the
	// persisted user text. Failures are ordinary diagnostic updates.
	refs := filerefs.Load(filerefs.Parse(text), s.rt.Session.Cwd)
	for _, ref := range refs {
		if ref.Error != "" {
			if err := emitUpdate(ctx, conn, s.id, newToolCallTracker(), agent.ErrorEvent{Err: fmt.Errorf("file reference %s: %s", ref.Token, ref.Error)}); err != nil {
				return coderacp.PromptResponse{}, err
			}
		}
	}
	text = filerefs.Rewrite(text, refs)

	// Capture the pre-turn conversation before appending the user message.
	var checkpointID string
	s.historyMu.Lock()
	if cp, ok := s.rt.Cfg.Checkpoints.(*checkpoint.Store); ok && cp != nil {
		id, err := cp.Begin(s.rt.Session.ID, text, len(s.rt.Session.Messages), s.rt.Session.Messages)
		if err == nil {
			checkpointID = id
		} else if e := emitUpdate(ctx, conn, s.id, newToolCallTracker(), agent.ErrorEvent{Err: fmt.Errorf("checkpoint unavailable: %w", err)}); e != nil {
			s.historyMu.Unlock()
			return coderacp.PromptResponse{}, e
		}
	}
	s.historyMu.Unlock()
	// Refresh memory at the boundary so edits made by tools or another
	// process are visible to the next prompt. A failed refresh must not
	// discard the last known-good memory.
	if mem, loadErr := memory.Load(s.rt.Session.Cwd); loadErr != nil {
		if err := emitUpdate(ctx, conn, s.id, newToolCallTracker(), agent.ErrorEvent{Err: fmt.Errorf("memory refresh: %w", loadErr)}); err != nil {
			return coderacp.PromptResponse{}, err
		}
	} else {
		s.rt.Mem = mem
	}

	// Retrieval is best-effort and must not delay ACP prompt startup for the
	// provider's full request deadline (a cold embedding service can be slow).
	// The TUI uses the same semantic path, but its turn UI remains responsive;
	// bound this headless preparation explicitly.
	retrievalCtx, retrievalCancel := context.WithTimeout(ctx, 750*time.Millisecond)
	base := memory.SystemPromptForSemantic(retrievalCtx, s.rt.BaseSystemPrompt, s.rt.Mem, text, s.rt.FileCfg.Retrieval, s.rt.EmbedClient)
	retrievalCancel()
	base = filerefs.Inject(base, refs)
	s.historyMu.Lock()
	if len(s.rt.Session.Messages) > 0 && s.rt.Session.Messages[0].Role == adapter.RoleSystem {
		s.rt.Session.Messages[0].Content = base
	}
	submitted := time.Now()
	s.rt.Session.Messages = append(s.rt.Session.Messages, adapter.Message{Role: adapter.RoleUser, Content: text, Timestamp: &submitted})
	s.historyMu.Unlock()

	turnCtx, cancel := context.WithCancel(ctx)
	turnCtx = agent.WithCheckpoint(turnCtx, s.rt.Session.ID, checkpointID)
	if prev := s.setCancel(cancel); prev != nil {
		// Shouldn't happen — an ACP client is expected to wait for a
		// session/prompt response before sending another for the same
		// session — but don't leak a goroutine if it does.
		prev()
	}
	// Always invoke this turn's own cancel func once it ends, whether or
	// not session/cancel already called it — context.CancelFunc is
	// idempotent, and skipping this (as a bare `defer s.setCancel(nil)`
	// would, discarding the returned previous value) leaks the context
	// per the stdlib's own documented contract for WithCancel.
	defer func() {
		if prev := s.setCancel(nil); prev != nil {
			prev()
		}
	}()

	events := make(chan agent.Event, 64)
	decisions := make(chan agent.Decision, 1)
	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				close(events)
				errCh <- fmt.Errorf("acp: turn panicked: %v", r)
			}
		}()
		err := agent.Turn(turnCtx, s.rt.Cfg, &s.rt.Session.Messages, events, decisions)
		close(events)
		errCh <- err
	}()

	tracker := newToolCallTracker()
	lastMode := currentModeID(s.rt)
	var updateErr error
	for ev := range events {
		switch e := ev.(type) {
		case agent.ApprovalNeeded:
			d := requestToolPermission(turnCtx, conn, s.id, tracker, e)
			select {
			case decisions <- d:
			default: // turn already ended; drop rather than block
			}
		case agent.ContextUsage:
			// Structured usage_update, not chat text: hosts (e.g. Buzz) use it
			// to track context fill and rotate sessions. Failures are
			// non-fatal — usage is advisory, and embeddings may not have a connection.
			if conn != nil && updateErr == nil {
				_ = conn.SessionUpdate(ctx, usageNotification(s.id, e, s.totalCostUSD()))
			}
		case agent.ErrorEvent:
			if note := correctWindow(s.rt, nil, adapter.IsContextOverflow(e.Err)); note != "" && updateErr == nil {
				if err := emitUpdate(ctx, conn, s.id, tracker, agent.ErrorEvent{Err: fmt.Errorf("%s", strings.TrimSpace(note))}); err != nil {
					updateErr = err
				}
			}
			if updateErr == nil {
				if err := emitUpdate(ctx, conn, s.id, tracker, e); err != nil {
					updateErr = err
				}
			}
		default:
			if msg, ok := ev.(agent.AssistantMessage); ok {
				if note := correctWindow(s.rt, msg.Message.Usage, false); note != "" && updateErr == nil {
					if err := emitUpdate(ctx, conn, s.id, tracker, agent.ErrorEvent{Err: fmt.Errorf("%s", strings.TrimSpace(note))}); err != nil {
						updateErr = err
					}
				}
			}
			if updateErr == nil {
				if err := emitUpdate(ctx, conn, s.id, tracker, ev); err != nil {
					// A broken transport must stop the provider promptly, but we
					// still drain events so the turn goroutine can unwind before
					// this method returns.
					updateErr = err
					s.requestCancel()
				}
			}
			// The model can flip Plan mode itself mid-turn by calling
			// enter_plan_mode/exit_plan_mode (see
			// internal/agent/enter_plan_mode_tool.go,
			// internal/agent/exit_plan_mode_tool.go) — there's no
			// dedicated agent.Event for that transition, so detect it
			// the same way the roadmap's mode-mapping table anticipates
			// ("current_mode_update outbound when the agent changes mode
			// itself") by comparing currentModeID() after every
			// ToolResult for those two tool names specifically, rather
			// than after every event (which would be needless work on
			// every token).
			if tr, ok := ev.(agent.ToolResult); ok && (tr.ToolName == "enter_plan_mode" || tr.ToolName == "exit_plan_mode") {
				if mode := currentModeID(s.rt); mode != lastMode {
					lastMode = mode
					_ = conn.SessionUpdate(ctx, coderacp.SessionNotification{
						SessionId: coderacp.SessionId(s.id),
						Update:    coderacp.SessionUpdate{CurrentModeUpdate: &coderacp.SessionCurrentModeUpdate{CurrentModeId: mode}},
					})
				}
			}
		}
	}

	turnErr := <-errCh
	// Take an immutable snapshot while holding historyMu, then perform disk and
	// recall I/O without blocking the agent loop on the history lock.
	s.historyMu.Lock()
	s.rt.Session.SubagentTasks = s.rt.SubagentTasks.Export()
	snapshot := *s.rt.Session
	snapshot.Messages = append([]adapter.Message(nil), s.rt.Session.Messages...)
	s.historyMu.Unlock()
	persistErr := snapshot.Save()
	if persistErr == nil && s.rt.RecallIndex != nil {
		persistErr = s.rt.RecallIndex.IndexSession(&snapshot)
	}
	if persistErr != nil {
		if err := emitUpdate(ctx, conn, s.id, newToolCallTracker(), agent.ErrorEvent{Err: fmt.Errorf("session persistence: %w", persistErr)}); err != nil {
			return coderacp.PromptResponse{}, err
		}
	}
	if turnErr != nil && adapter.IsContextOverflow(turnErr) {
		if note := correctWindow(s.rt, nil, true); note != "" {
			if err := emitUpdate(ctx, conn, s.id, newToolCallTracker(), agent.ErrorEvent{Err: fmt.Errorf("%s", strings.TrimSpace(note))}); err != nil {
				return coderacp.PromptResponse{}, err
			}
		}
	}
	// Cancellation is a normal ACP stop reason. Prefer it over a transport
	// error caused by trying to emit the agent's cancellation event while the
	// same turn is unwinding.
	if errors.Is(turnErr, context.Canceled) {
		return coderacp.PromptResponse{StopReason: coderacp.StopReasonCancelled}, nil
	}
	if updateErr != nil {
		return coderacp.PromptResponse{}, updateErr
	}
	switch {
	case turnErr == nil:
		return coderacp.PromptResponse{StopReason: coderacp.StopReasonEndTurn}, nil
	case errors.Is(turnErr, context.Canceled):
		// Matches how session/cancel is expected to resolve an
		// in-flight prompt: internal/agent/loop.go's own isCancelErr
		// treats context.Canceled as the cancellation signature.
		return coderacp.PromptResponse{StopReason: coderacp.StopReasonCancelled}, nil
	default:
		// A genuine failure — surfaced as a JSON-RPC error so the
		// client sees it as such rather than a silent end_turn.
		return coderacp.PromptResponse{}, coderacp.NewInternalError(map[string]any{"error": turnErr.Error()})
	}
}

// promptText concatenates a prompt's content blocks into plain text —
// yottacode's agent loop works off plain-text message content, not
// content-block structure. Text and ResourceLink are the two block
// kinds ACP requires every agent to support; a resource link renders as
// a markdown-style reference the model can reason about even though
// yottacode doesn't dereference it automatically in v1.
func promptText(blocks []coderacp.ContentBlock) (string, error) {
	var b strings.Builder
	for i, block := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		switch {
		case block.Text != nil:
			b.WriteString(block.Text.Text)
		case block.ResourceLink != nil:
			fmt.Fprintf(&b, "[%s](%s)", block.ResourceLink.Name, block.ResourceLink.Uri)
		default:
			return "", errors.New("acp: unsupported content block kind in prompt (only text and resource_link are supported in v1)")
		}
	}
	return b.String(), nil
}

// CloseSession cancels any in-flight turn, waits (bounded) for it to
// actually finish so persistence never races the turn goroutine's
// writes to rt.Session, exports SubagentTasks, saves gated on
// HasExchange — mirroring internal/tui/run.go's own exit sequence
// exactly (sess.SubagentTasks = subagentTasks.Export(); save only if
// HasExchange()) — and removes the session from the registry.
func (s *Server) CloseSession(ctx context.Context, params coderacp.CloseSessionRequest) (coderacp.CloseSessionResponse, error) {
	id := string(params.SessionId)
	sess, ok := s.session(id)
	if !ok {
		return coderacp.CloseSessionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown session id"})
	}
	sess.mu.Lock()
	if sess.closed || sess.closing {
		sess.mu.Unlock()
		return coderacp.CloseSessionResponse{}, nil
	}
	// Mark closing before waiting for lifecycle. A prompt that is queued but
	// has not claimed lifecycle will reject immediately; an active prompt will
	// be cancelled once it releases lifecycle.
	sess.closing = true
	sess.mu.Unlock()
	sess.lifecycle.Lock()
	defer sess.lifecycle.Unlock()
	sess.requestCancel()
	if !sess.waitForTurn(ctx, closeSessionDrainTimeout) {
		// Never persist mutable history while the turn may still be appending.
		sess.mu.Lock()
		sess.closing = false
		sess.mu.Unlock()
		return coderacp.CloseSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "timed out draining session turn"})
	}
	sess.mu.Lock()
	sess.closed = true
	sess.mu.Unlock()
	sess.historyMu.Lock()
	sess.rt.Session.SubagentTasks = sess.rt.SubagentTasks.Export()
	snapshot := *sess.rt.Session
	snapshot.Messages = append([]adapter.Message(nil), sess.rt.Session.Messages...)
	sess.historyMu.Unlock()
	var saveErr error
	if snapshot.HasExchange() {
		saveErr = snapshot.Save()
	}
	if saveErr != nil {
		// Restore the lifecycle claim if persistence failed; callers can retry
		// closing the still-live session without racing a second teardown.
		sess.mu.Lock()
		sess.closed = false
		sess.closing = false
		sess.mu.Unlock()
		return coderacp.CloseSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "save session: " + saveErr.Error()})
	}

	closeCtx, cancel := context.WithTimeout(ctx, closeSessionDrainTimeout)
	defer cancel()
	sess.rt.Close(closeCtx)

	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return coderacp.CloseSessionResponse{}, nil
}

// Shutdown cancels and persists every still-registered session — called
// once at process exit (stdin EOF / client disconnect) so a client that
// disconnects without individually closing each session doesn't
// silently drop unsaved state. Reuses CloseSession's own logic per
// session rather than duplicating it. Sessions close concurrently, not
// one after another: cmd/yottacode/acp.go bounds ctx with a single
// process-wide acpShutdownTimeout, and closing N sessions sequentially
// (each already able to spend up to closeSessionDrainTimeout draining
// its own turn) could multiply past that shared deadline instead of
// sharing it.
func (s *Server) Shutdown(ctx context.Context) {
	s.mu.RLock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, _ = s.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: coderacp.SessionId(id)})
		}(id)
	}
	wg.Wait()
}

// usageNotification builds the ACP usage_update for one context reading.
// cost is the cumulative session cost in USD; <= 0 omits the field (unknown
// or free), since a fabricated zero would read as a real price.
func usageNotification(sessionID string, e agent.ContextUsage, cost float64) coderacp.SessionNotification {
	u := &coderacp.SessionUsageUpdate{SessionUpdate: "usage_update", Used: e.Tokens, Size: e.Window}
	if cost > 0 {
		u.Cost = &coderacp.Cost{Amount: cost, Currency: "USD"}
	}
	return coderacp.SessionNotification{
		SessionId: coderacp.SessionId(sessionID),
		Update:    coderacp.SessionUpdate{UsageUpdate: u},
	}
}

// totalCostUSD is the session's cumulative price estimate, 0 when unknown.
func (s *acpSession) totalCostUSD() float64 {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	if u := s.rt.Session.TotalUsage; u.CostAvailable {
		return u.CostUSD
	}
	return 0
}
