package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const loopDefaultTTL = 5 * 24 * time.Hour

// cmdLoop arms, inspects, or disarms recurring /loop commands. Loops are
// session-local schedulers over the existing single-turn pipeline: they do not
// run in the background after yottacode exits, and they never bypass normal
// tool approval gates.
func cmdLoop(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		if m.activeLoopCount() == 0 {
			m.appendLine(styleAuto.Render("[loop] none armed — /loop <interval> [Nx] <prompt|/cmd>"))
			return m, nil
		}
		// Show the loops as a dismissable panel above the cmdline rather than
		// writing cards into the session — the transcript shouldn't fill with a
		// status readout every time the user checks their loops.
		m.loopListOpen = true
		m.loopListScrollOffset = 0
		return m, nil
	}

	// Only stop/off forms disarm loops. Prose payloads like
	// `/loop 30s stop the deploy` still arm normally because the disarm verb
	// is recognized before interval parsing.
	if strings.EqualFold(args[0], "stop") || strings.EqualFold(args[0], "off") {
		return cmdLoopStop(m, args[1:])
	}
	if strings.EqualFold(args[0], "pause") {
		return cmdLoopPause(m, args[1:], true)
	}
	if strings.EqualFold(args[0], "resume") {
		return cmdLoopPause(m, args[1:], false)
	}

	rest := args
	interval, ok := parseLoopInterval(rest[0])
	if !ok {
		m.appendLine(styleError.Render(
			"[loop] interval required — use /loop 30s <prompt|/command> or /loop 30s 3x <prompt|/command>"))
		return m, nil
	}
	if interval < loopMinInterval {
		m.appendLine(styleError.Render(fmt.Sprintf(
			"[loop] interval %s is below the %s floor — pick a larger interval", interval, loopMinInterval)))
		return m, nil
	}
	rest = rest[1:]

	// Between the interval and the payload: an optional iteration count (3x) and
	// the --budget / --verify flags, in any order.
	remaining := -1
	var budget int64
	verify, haveCount := false, false
parseOpts:
	for len(rest) > 0 {
		tok := rest[0]
		if n, isCount := parseLoopCount(tok); isCount && !haveCount {
			remaining, haveCount = n, true
			rest = rest[1:]
			continue
		}
		switch {
		case tok == "--verify":
			verify = true
			rest = rest[1:]
		case tok == "--budget" || strings.HasPrefix(tok, "--budget="):
			val := strings.TrimPrefix(tok, "--budget=")
			consumed := 1
			if tok == "--budget" {
				if len(rest) < 2 {
					m.appendLine(styleError.Render("[loop] --budget needs a token count — e.g. --budget 200k"))
					return m, nil
				}
				val, consumed = rest[1], 2
			}
			n, ok := parseLoopTokens(val)
			if !ok {
				m.appendLine(styleError.Render(fmt.Sprintf("[loop] invalid --budget %q — use a positive token count like 200k, 1.5m or 50000", val)))
				return m, nil
			}
			budget = n
			rest = rest[consumed:]
		case strings.HasPrefix(tok, "--"):
			m.appendLine(styleError.Render(fmt.Sprintf("[loop] unknown option %s — supported: --budget <tokens>, --verify", tok)))
			return m, nil
		default:
			break parseOpts
		}
	}
	payload := strings.TrimSpace(strings.Join(rest, " "))
	if payload == "" {
		m.appendLine(styleError.Render(
			"usage: /loop <interval> [Nx] [--budget <tokens>] [--verify] <prompt|/command> — e.g. /loop 5m /git-review-pr"))
		return m, nil
	}
	if strings.HasPrefix(payload, "/") {
		// A slash loop's turn is not owned by the loop (no loop_control, no
		// metering), so these options could only ever be silent no-ops.
		switch {
		case verify:
			m.appendLine(styleError.Render("[loop] --verify applies to prose loops (the agent's stop is what gets verified), not slash commands"))
			return m, nil
		case budget > 0:
			m.appendLine(styleError.Render("[loop] --budget applies to prose loops — a slash command's turns aren't metered, so it would never trigger"))
			return m, nil
		}
	}
	if verify && m.subagentTasks == nil {
		m.appendLine(styleError.Render("[loop] --verify needs subagents (the verification agent) — not available in this session"))
		return m, nil
	}
	// Refuse payloads that don't make sense to repeat: another /loop (would
	// mutate the scheduler from inside itself), or a lifecycle command that ends
	// or resets the very session the loop runs in.
	switch loopPayloadHead(payload) {
	case "/loop":
		m.appendLine(styleError.Render("[loop] payload cannot be another /loop command"))
		return m, nil
	case "/quit", "/clear":
		m.appendLine(styleError.Render("[loop] payload can't be /quit or /clear — a loop must not end or reset its own session"))
		return m, nil
	}
	// A slash payload must resolve to a real command — otherwise an unbounded
	// loop would just print "unknown command" every interval forever.
	if strings.HasPrefix(payload, "/") {
		name := strings.TrimPrefix(loopPayloadHead(payload), "/")
		if m.findSlash(name) == nil {
			m.appendLine(styleError.Render(fmt.Sprintf("[loop] unknown command /%s — not arming (see /help)", name)))
			return m, nil
		}
	}

	now := time.Now()
	m.ensureLoopStore()
	id := m.newLoopID(now)
	ls := loopState{
		id:        id,
		active:    true,
		payload:   payload,
		isSlash:   strings.HasPrefix(payload, "/"),
		interval:  interval,
		remaining: remaining,
		armedAt:   now,
		expiresAt: now.Add(loopDefaultTTL),
		budget:    budget,
		verify:    verify,
	}
	if remaining > 0 {
		ls.total = remaining
	}
	m.loops[id] = ls
	m.loopOrder = append(m.loopOrder, id)
	m.appendLine(renderLoopCard(ls, m.width))
	if verify && budget == 0 && remaining < 0 {
		// Nothing but the agent, you, or the 5-day expiry ends this loop, and a
		// verifier that keeps failing keeps it spending.
		m.appendLine(styleAuto.Render("[loop] note: --verify with no --budget or count runs until verified, stopped, or expiry — consider --budget"))
	}

	cmds := []tea.Cmd{loopTickCmd(interval, id)}
	// Kick off iteration 1 immediately when idle. When a turn is active, the
	// loop waits for its first interval tick instead of interrupting it.
	if !m.turnActive && !m.summarizing {
		next, cmd := m.fireLoopIteration(id)
		m = next
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		// Prose payloads must start a turn; if they don't (for example, no
		// provider is configured), stop instead of printing the same error forever
		// on each interval tick. Slash payloads may be informational/status
		// commands, so they are allowed to return idle.
		if ls, ok := m.loops[id]; ok && ls.active && !ls.isSlash && !m.turnActive && !m.summarizing {
			m.disarmLoop(id, "[loop] "+id+" stopped — payload started no turn")
		}
	}
	return m, tea.Batch(cmds...)
}

func cmdLoopStop(m Model, args []string) (Model, tea.Cmd) {
	ids := m.activeLoopIDs()
	if len(ids) == 0 {
		m.appendLine(styleAuto.Render("[loop] nothing to stop"))
		return m, nil
	}
	if len(args) == 0 {
		if len(ids) == 1 {
			m.disarmLoop(ids[0], "[loop] "+ids[0]+" stopped")
			m.cancelTurnIfLoopOwned(ids[0])
			return m, nil
		}
		m.appendLine(styleError.Render("[loop] multiple loops active — pass an ID or `all`"))
		return m, nil
	}
	id := args[0]
	if strings.EqualFold(id, "all") {
		// Every loop is stopped, so if a loop owns the current turn, cancel it.
		owner := m.currentLoopTurnID
		m.disarmAllLoops("[loop] stopped all loops")
		m.cancelTurnIfLoopOwned(owner)
		return m, nil
	}
	if _, ok := m.loops[id]; !ok {
		m.appendLine(styleError.Render(fmt.Sprintf("[loop] no active loop %q", id)))
		return m, nil
	}
	m.disarmLoop(id, "[loop] "+id+" stopped")
	m.cancelTurnIfLoopOwned(id)
	return m, nil
}

// cmdLoopPause pauses or resumes armed loops. A paused loop stays armed — it
// keeps its ID, count, budget meter and expiry clock — but fires nothing until
// resumed. Pausing never cancels an iteration already running (it just prevents
// the next one); resuming fires the next iteration right away when idle instead
// of waiting out a long interval. Same targeting rules as /loop stop: an ID,
// `all`, or no argument when exactly one loop is armed.
func cmdLoopPause(m Model, args []string, pause bool) (Model, tea.Cmd) {
	verb, past := "resume", "resumed"
	if pause {
		verb, past = "pause", "paused"
	}
	ids := m.activeLoopIDs()
	if len(ids) == 0 {
		m.appendLine(styleAuto.Render("[loop] nothing to " + verb))
		return m, nil
	}
	var targets []string
	switch {
	case len(args) == 0 && len(ids) == 1:
		targets = ids
	case len(args) == 0:
		m.appendLine(styleError.Render("[loop] multiple loops active — pass an ID or `all`"))
		return m, nil
	case strings.EqualFold(args[0], "all"):
		targets = ids
	default:
		if ls, ok := m.loops[args[0]]; !ok || !ls.active {
			m.appendLine(styleError.Render(fmt.Sprintf("[loop] no active loop %q", args[0])))
			return m, nil
		}
		targets = []string{args[0]}
	}

	var cmds []tea.Cmd
	for _, id := range targets {
		ls := m.loops[id]
		if ls.paused == pause {
			m.appendLine(styleAuto.Render("[loop] " + id + " is already " + past))
			continue
		}
		ls.paused = pause
		m.loops[id] = ls
		m.appendLine(styleAuto.Render("[loop] " + id + " " + past))
		if pause || m.turnActive || m.summarizing {
			continue
		}
		next, cmd := m.fireLoopIteration(id)
		m = next
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		// Same guard as arming: a prose iteration that starts no turn (no
		// provider) would just repeat the error every interval.
		if cur, ok := m.loops[id]; ok && cur.active && !cur.isSlash && !m.turnActive && !m.summarizing {
			m.disarmLoop(id, "[loop] "+id+" stopped — payload started no turn")
		}
	}
	return m, tea.Batch(cmds...)
}

// parseLoopTokens accepts a positive token count: plain digits, or a k/m suffix
// with an optional fraction (200k, 1.5m, 50000). Returns (0,false) otherwise.
func parseLoopTokens(tok string) (int64, bool) {
	tok = strings.ToLower(strings.TrimSpace(tok))
	mult := float64(1)
	switch {
	case strings.HasSuffix(tok, "k"):
		mult, tok = 1e3, strings.TrimSuffix(tok, "k")
	case strings.HasSuffix(tok, "m"):
		mult, tok = 1e6, strings.TrimSuffix(tok, "m")
	}
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil || f <= 0 || f*mult > 1e12 {
		return 0, false
	}
	n := int64(f*mult + 0.5)
	return n, n > 0
}

// loopSubagentTokens snapshots non-historical subagent spend by task ID. The
// loop budget uses this keyed snapshot so unrelated tasks cannot be charged.
func (m Model) loopSubagentTokens() map[string]int64 {
	out := map[string]int64{}
	if m.subagentTasks == nil {
		return out
	}
	for _, t := range m.subagentTasks.List() {
		if t.Historical {
			continue
		}
		out[t.ID] = int64(t.UsageTokens())
	}
	return out
}

// loopTurnSubagentSpend returns usage added to tasks observed during this turn.
func (m Model) loopTurnSubagentSpend(before map[string]int64) int64 {
	var spent int64
	for id, now := range m.loopSubagentTokens() {
		if prev, ok := before[id]; ok && now > prev {
			spent += now - prev
		} else if !ok {
			spent += now
		}
	}
	return spent
}

// loopTurnSpend combines main-thread usage with only the subagent usage that
// belongs to this turn's task set.
func (m Model) loopTurnSpend(mainBefore int64, subagentBefore map[string]int64) int64 {
	spent := totalTokensFor(m.sess.TotalUsage) - mainBefore
	if spent < 0 {
		spent = 0
	}
	return spent + m.loopTurnSubagentSpend(subagentBefore)
}

// sessionTokens is the session's running token total for diagnostics/tests.
func (m Model) sessionTokens() int64 {
	if m.sess == nil {
		return 0
	}
	n := totalTokensFor(m.sess.TotalUsage)
	if m.subagentTasks == nil {
		return n + totalTokensFor(m.sess.SubagentUsage().Total)
	}
	for _, t := range m.subagentTasks.List() {
		if t.Historical {
			continue
		}
		n += int64(t.UsageTokens())
	}
	return n
}

// loopTokens formats a token count compactly for loop status lines.
func loopTokens(n int64) string {
	return formatTokens(int(n))
}

// cancelTurnIfLoopOwned cancels the in-flight turn only when that turn is the
// given loop's own iteration. Stopping one loop must not kill a different
// loop's — or a user-initiated — turn: with multiple loops active, only one
// can own the current turn (turns never overlap), so an unrelated `/loop stop`
// used to cancel whatever happened to be running. The empty-id case (no loop
// owns the turn — e.g. a user turn) never matches.
func (m *Model) cancelTurnIfLoopOwned(id string) {
	if id != "" && id == m.currentLoopTurnID && m.turnActive && m.turnCancel != nil {
		m.turnCancel()
	}
}

// fireLoopIteration dispatches one loop iteration and advances the bounded
// count bookkeeping. The caller must ensure no turn is active.
func (m Model) fireLoopIteration(id string) (Model, tea.Cmd) {
	ls, ok := m.loops[id]
	if !ok || !ls.active {
		return m, nil
	}
	ls.iterations++
	if ls.remaining > 0 {
		ls.remaining--
		if ls.remaining == 0 {
			ls.active = false // dispatch the final iteration, then disarm
		}
	}
	m.loops[id] = ls
	// Progress line for bounded loops so a long count shows where it is.
	if ls.total > 0 {
		m.appendLine(styleAuto.Render(fmt.Sprintf("[loop] %s iteration %d/%d", id, ls.total-ls.remaining, ls.total)))
	}
	payload := ls.payload
	if !ls.active {
		m.removeLoop(id)
	}
	if ls.isSlash {
		// dispatchSlash, not runSlash: a loop must not re-record the same command
		// into ↑-history on every iteration.
		return m.dispatchSlash(payload)
	}
	// Mark this turn as a loop iteration BEFORE the turn goroutine spawns so the
	// very first streamIteration advertises loop_control and injects the
	// loop-assessment addendum (the shared state is a pointer, so setting it
	// here is visible to the agent goroutine). If the turn never starts (no
	// provider), undo it — turnEndedMsg won't fire to clear it, and the loop is
	// about to be disarmed anyway.
	mainTokensBefore := int64(0)
	if m.sess != nil {
		mainTokensBefore = totalTokensFor(m.sess.TotalUsage)
	}
	turnSubagentTokens := m.loopSubagentTokens()
	if m.cfg.LoopControl != nil {
		m.cfg.LoopControl.SetContext(loopTurnContext(ls))
		m.cfg.LoopControl.SetVerify(ls.verify, time.Now())
		m.cfg.LoopControl.SetTurnActive(true)
	}
	next, cmd := m.startTurnWithDisplay(payload, "")
	nm := next.(Model)
	if nm.turnActive {
		nm.currentLoopTurnID = id
		nm.loopTurnTokens = mainTokensBefore
		nm.loopTurnSubagentTokens = turnSubagentTokens
		nm.loopTurnBudget = ls.budget
		// The loop is already removed from the store for a bounded loop's last
		// iteration, so remember it was a verify-gated final one to report at turn
		// end if it never reached a verified stop.
		nm.loopTurnFinalVerify = !ls.active && ls.verify
	} else if nm.cfg.LoopControl != nil {
		nm.cfg.LoopControl.SetTurnActive(false)
	}
	return nm, cmd
}

// consumeLoopControl runs at turn end. If the just-finished turn was a /loop
// prose iteration whose agent called loop_control{stop}, disarm that loop so it
// stops re-firing. Always clears the per-turn loop-control flag and owner ID so
// the tool is hidden again on the next (non-loop) turn.
func (m *Model) consumeLoopControl() {
	id := m.currentLoopTurnID
	turnTokens := m.loopTurnTokens
	subagentTokens := m.loopTurnSubagentTokens
	turnBudget := m.loopTurnBudget
	finalVerify := m.loopTurnFinalVerify
	m.currentLoopTurnID, m.loopTurnTokens, m.loopTurnSubagentTokens, m.loopTurnBudget, m.loopTurnFinalVerify = "", 0, nil, 0, false
	stop, reason, blocked := m.cfg.LoopControl.ConsumeStopDetail()
	gateNote := m.cfg.LoopControl.ConsumeGateNote()
	m.cfg.LoopControl.SetTurnActive(false)
	if id == "" {
		return
	}
	ls, ok := m.loops[id]
	// Meter the iteration that just ended against the loop's own budget. Only
	// tokens spent during this loop-owned turn count. A final bounded iteration
	// has already been removed from m.loops, so retain its accounting separately
	// and still emit the normal budget notice when it crosses the limit.
	spent := m.loopTurnSpend(turnTokens, subagentTokens)
	if !ok || !ls.active {
		if finalVerify {
			if turnBudget > 0 && spent >= turnBudget {
				m.appendLine(styleAuto.Render(fmt.Sprintf("[loop] %s stopped — token budget reached (%s of %s used)", id, loopTokens(spent), loopTokens(turnBudget))))
				return
			}
			if stop {
				m.appendLine(styleAuto.Render(loopStopNotice(id, reason, blocked)))
				return
			}
			notice := "[loop] " + id + " ended after its final iteration without a verified stop — treat the work as unverified"
			if gateNote != "" {
				notice += " (" + gateNote + ")"
			}
			m.appendLine(styleAuto.Render(notice))
		}
		return
	}
	if spent > 0 {
		ls.spent += spent
	}
	ls.lastNote = gateNote
	m.loops[id] = ls

	if stop {
		m.disarmLoop(id, loopStopNotice(id, reason, blocked))
		return
	}
	if ls.budget > 0 && ls.spent >= ls.budget {
		m.disarmLoop(id, fmt.Sprintf("[loop] %s stopped — token budget reached (%s of %s used)",
			id, loopTokens(ls.spent), loopTokens(ls.budget)))
	}
}

// loopStopNotice is the scrollback line for a loop the agent ended itself.
// blocked marks a --verify loop it stopped without a passing verification.
func loopStopNotice(id, reason string, blocked bool) string {
	notice := "[loop] " + id + " stopped by the agent"
	if blocked {
		notice = "[loop] " + id + " stopped by the agent (UNVERIFIED — blocked)"
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		notice += ": " + reason
	}
	return notice
}

// loopTurnContext is the one-line loop descriptor fed into the loop-assessment
// addendum (LoopIterationAddendum's %s), so the model knows the cadence and
// whether the loop is bounded when deciding whether to keep going. Called with
// ls after fireLoopIteration has decremented a bounded loop's remaining count.
func loopTurnContext(ls loopState) string {
	cadence := "every " + compactDuration(ls.interval)
	var s string
	if ls.total > 0 {
		s = fmt.Sprintf("It runs %s, iteration %d of %d.", cadence, ls.total-ls.remaining, ls.total)
	} else {
		s = fmt.Sprintf("It runs %s and is unbounded (no fixed number of iterations) — it repeats until stopped.", cadence)
	}
	if ls.budget > 0 {
		s += fmt.Sprintf(" It has a token budget: %s of %s used so far; it stops when the budget is reached, so prioritise the remaining work.", loopTokens(ls.spent), loopTokens(ls.budget))
	}
	if ls.verify {
		s += " Stopping is verify-gated: loop_control stop is refused until the `verification` agent has run this iteration and returned VERDICT: PASS. When you believe the goal is met, run it (foreground) with the original task, the files you changed, your approach, and any previous FAIL findings; fix what it reports and re-verify. Use loop_control with blocked: true only for a real external blocker."
		if ls.lastNote != "" {
			s += " Last stop attempt: " + ls.lastNote + "."
		}
	}
	return s
}

func (m *Model) ensureLoopStore() {
	if m.loops == nil {
		m.loops = map[string]loopState{}
	}
}

// activeLoopCount counts armed loops without allocating — View calls it a few
// times per render frame (banner + panel guards), so it must stay cheap on the
// hot path. loopOrder is authoritative (every arm appends, every removeLoop
// filters), so iterating it with the active check needs no scratch map.
func (m Model) activeLoopCount() int {
	n := 0
	for _, id := range m.loopOrder {
		if ls, ok := m.loops[id]; ok && ls.active {
			n++
		}
	}
	return n
}

// activeLoopIDs returns armed loop IDs in stable arm order. loopOrder is
// authoritative (kept in sync with m.loops by arm/removeLoop), so a single pass
// suffices — no scratch `seen` map, no second pass over the map.
func (m Model) activeLoopIDs() []string {
	if len(m.loopOrder) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m.loopOrder))
	for _, id := range m.loopOrder {
		if ls, ok := m.loops[id]; ok && ls.active {
			ids = append(ids, id)
		}
	}
	return ids
}

func (m *Model) newLoopID(now time.Time) string {
	m.ensureLoopStore()
	base := strconv.FormatInt(now.UnixNano(), 36)
	if len(base) > 6 {
		base = base[len(base)-6:]
	}
	for i := 0; ; i++ {
		id := "loop-" + base
		if i > 0 {
			id = fmt.Sprintf("%s-%d", id, i)
		}
		if _, exists := m.loops[id]; !exists {
			return id
		}
	}
}

// disarmLoop turns one loop off by removing it from the store. That removal is
// what invalidates any pending loopTickMsg: the tick handler drops ticks for an
// id that is no longer an active loop. A no-op (prints nothing) when the ID is
// absent or already inactive.
func (m *Model) disarmLoop(id, notice string) {
	if ls, ok := m.loops[id]; !ok || !ls.active {
		return
	}
	m.removeLoop(id)
	if notice != "" {
		m.appendLine(styleAuto.Render(notice))
	}
}

func (m *Model) disarmAllLoops(notice string) {
	ids := m.activeLoopIDs()
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		m.disarmLoop(id, "")
	}
	if notice != "" {
		m.appendLine(styleAuto.Render(notice))
	}
}

func (m *Model) removeLoop(id string) {
	delete(m.loops, id)
	if len(m.loopOrder) > 0 {
		out := m.loopOrder[:0]
		for _, existing := range m.loopOrder {
			if existing != id {
				out = append(out, existing)
			}
		}
		m.loopOrder = out
	}
	if len(m.loops) == 0 {
		m.loopOrder = nil
	}
}

// renderLoopCard renders one /loop as a gutter card — the tool-card-shaped
// block used for scrollback log entries (see renderPlanModeEntryCard). It backs
// both the arm notice and the /loop list. Header carries the loop label + ID,
// the meta line carries cadence / remaining count / expiry, the body carries
// the wrapped payload, and the footer carries the stop hint. Rendered off the
// arm/list paths, not per frame, so the time.Now expiry read is cheap.
func renderLoopCard(ls loopState, width int) string {
	if width <= 0 {
		width = 80
	}
	gutter := styleCardGutter.Render
	dot := styleAutoBannerSep.Render(" · ")

	g := neutralGutter()
	count := "unbounded"
	if ls.remaining > 0 {
		count = fmt.Sprintf("%d left", ls.remaining)
	}
	meta := styleAutoBannerActivity.Render("every "+compactDuration(ls.interval)) +
		dot + styleAutoBannerActivity.Render(count) +
		dot + styleAutoBannerActivity.Render("expires "+formatLoopRemaining(time.Now(), ls.expiresAt))
	for _, bit := range loopStatusBits(ls) {
		meta += dot + styleAutoBannerActivity.Render(bit)
	}

	lines := []string{
		renderCardHeader("Loop("+ls.id+")", g, 0, width),
		gutter("│ ") + meta,
	}
	// The payload can be long (an entire prose prompt); wrap it under the
	// gutter so continuation lines stay inside the card instead of bleeding to
	// column 0 when queuePrintln hard-wraps a single over-wide line.
	wrapW := width - 2 // "│ " gutter is 2 cols
	if wrapW > 96 {
		wrapW = 96
	}
	if wrapW < 8 {
		wrapW = 8
	}
	for _, seg := range strings.Split(ansi.Hardwrap(ls.payload, wrapW, true), "\n") {
		// Hardwrap can break at a space and carry it onto the next row; trim so
		// continuation lines start flush under the gutter.
		lines = append(lines, gutter("│ ")+styleAutoBannerActivity.Render(strings.TrimLeft(seg, " ")))
	}
	lines = append(lines, gutter("└ ")+styleAutoBannerHint.Render("/loop stop "+ls.id))
	return strings.Join(lines, "\n")
}

// renderLoopListPanel is the body of the bare-`/loop` popup: a compact menu
// (one row per loop, house picker style via renderMenuItem) plus a stop/dismiss
// hint. Rendered as a centered popup (popup.go) so checking loops never
// clutters the transcript. Deliberately NOT the multi-line arm card —
// this is a scannable list of what's running, keyed by ID for `/loop stop`.
func (m Model) renderLoopListPanel() string {
	ids := m.activeLoopIDs()
	width := m.popupWidth()
	label := "loops"
	if len(ids) == 1 {
		label = "loop"
	}
	var b strings.Builder
	b.WriteString(renderMenuHeader(fmt.Sprintf("%d active %s", len(ids), label), "", width))
	b.WriteString("\n")

	const labelW = 13
	descBudget := width - (2 + labelW + 1 + 2) // cursor + label + space + check columns
	for _, id := range ids {
		ls := m.loops[id]
		count := "unbounded"
		if ls.remaining > 0 {
			count = fmt.Sprintf("%d left", ls.remaining)
		}
		facts := []string{"every " + compactDuration(ls.interval), count}
		facts = append(facts, loopStatusBits(ls)...)
		facts = append(facts, "expires "+formatLoopRemaining(time.Now(), ls.expiresAt))
		desc := strings.Join(facts, " · ") + "  ·  " + ls.payload
		if descBudget > 8 {
			desc = ansi.Truncate(desc, descBudget, "…")
		}
		b.WriteString(renderMenuItem(menuItemOpts{Label: id, LabelWidth: labelW, Desc: desc}))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleAutoBannerHint.Render("/loop stop|pause|resume <id> (or all) · any key to dismiss"))
	return b.String()
}

// loopStatusBits lists a loop's optional status facts in display order, shared
// by the arm card and the status panel: paused state, iterations fired, tokens
// spent (against the budget when one is set), the verify gate, and why the last
// stop attempt was refused.
func loopStatusBits(ls loopState) []string {
	var bits []string
	if ls.paused {
		bits = append(bits, "paused")
	}
	if ls.iterations > 0 {
		bits = append(bits, fmt.Sprintf("iter %d", ls.iterations))
	}
	switch {
	case ls.budget > 0:
		bits = append(bits, fmt.Sprintf("%s/%s tokens", loopTokens(ls.spent), loopTokens(ls.budget)))
	case ls.spent > 0:
		bits = append(bits, loopTokens(ls.spent)+" tokens")
	}
	if ls.verify {
		bits = append(bits, "verify")
	}
	if ls.lastNote != "" {
		bits = append(bits, ls.lastNote)
	}
	return bits
}

const loopListScrollReserve = 1

// loopListVisibleLines reserves space for the scroll hint when enough active
// loops exist to push the status popup past the terminal height.
func (m Model) loopListVisibleLines() int {
	n := m.height - 2 - loopListScrollReserve
	if n < 1 {
		n = 1
	}
	return n
}

func (m Model) loopListFullFitLines() int {
	n := m.height - 2
	if n < 1 {
		n = 1
	}
	return n
}

func (m Model) loopListMaxScrollOffset() int {
	panel := m.renderLoopListPanel()
	lines := strings.Count(panel, "\n") + 1
	if lines <= m.loopListFullFitLines() {
		return 0
	}
	return lines - m.loopListVisibleLines()
}

// windowedLoopListPanel windows the bare-/loop status panel so many active
// loops remain inspectable without leaking clicks or wheel events through a
// popup taller than the terminal.
func (m Model) windowedLoopListPanel() string {
	panel := m.renderLoopListPanel()
	if panel == "" {
		return panel
	}
	allLines := strings.Split(panel, "\n")
	total := len(allLines)
	if total <= m.loopListFullFitLines() {
		return panel
	}
	visible := m.loopListVisibleLines()
	offset := min(max(m.loopListScrollOffset, 0), total-visible)
	end := min(total, offset+visible)
	shown := strings.Join(allLines[offset:end], "\n")
	hint := fmt.Sprintf("── %d-%d of %d lines · wheel/click ↑↓ · PgUp/PgDn ──", offset+1, end, total)
	return shown + "\n" + styleHint.Render(hint)
}

// compactDuration renders a loop interval without trailing zero units
// (2m0s → 2m, 1h0m0s → 1h, 1h30m0s → 1h30m) so the banner and cards read
// cleanly. Sub-second intervals fall back to Go's own formatting.
func compactDuration(d time.Duration) string {
	if d <= 0 || d%time.Second != 0 {
		return d.String()
	}
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second
	var b strings.Builder
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if s > 0 || b.Len() == 0 {
		fmt.Fprintf(&b, "%ds", s)
	}
	return b.String()
}

// parseLoopInterval accepts a Go duration token (30s, 5m, 1h). Returns
// (0,false) when the token isn't a positive duration, so the caller can reject
// the command with a clear interval-required error.
func parseLoopInterval(tok string) (time.Duration, bool) {
	d, err := time.ParseDuration(tok)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// parseLoopCount accepts a bounded-iteration token "3x" / "10X". Returns
// (0,false) for anything else so it falls through to the payload.
func parseLoopCount(tok string) (int, bool) {
	if len(tok) < 2 {
		return 0, false
	}
	last := tok[len(tok)-1]
	if last != 'x' && last != 'X' {
		return 0, false
	}
	n, err := strconv.Atoi(tok[:len(tok)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// loopPayloadHead returns the first whitespace-delimited token of a loop
// payload — for a slash payload, the command name (e.g. "/quit").
func loopPayloadHead(payload string) string {
	if i := strings.IndexByte(payload, ' '); i >= 0 {
		return payload[:i]
	}
	return payload
}

func formatLoopRemaining(now, expiresAt time.Time) string {
	if expiresAt.IsZero() {
		return "unknown"
	}
	if now.IsZero() {
		now = time.Now()
	}
	left := expiresAt.Sub(now)
	if left <= 0 {
		return "now"
	}
	if left >= 24*time.Hour {
		days := int(left.Hours() / 24)
		if days == 1 {
			return "in 1d"
		}
		return fmt.Sprintf("in %dd", days)
	}
	if left >= time.Hour {
		return "in " + left.Truncate(time.Hour).String()
	}
	return "in " + left.Truncate(time.Second).String()
}

// renderLoopBanner is the one-line indicator above the cmdline while loops are
// armed, so live loops stay visible after the arm line scrolls away.
func renderLoopBanner(loops []loopState, width int) string {
	if width <= 0 {
		width = 80
	}
	if len(loops) == 0 {
		return ""
	}
	label := styleAutoBannerLabel.Render("loop")
	if len(loops) > 1 {
		label = styleAutoBannerLabel.Render("loops")
	}
	dot := styleAutoBannerSep.Render(" · ")
	var detail string
	if len(loops) == 1 {
		ls := loops[0]
		cadence := "every " + compactDuration(ls.interval)
		if ls.paused {
			cadence = "paused"
		}
		detail = styleAutoBannerActivity.Render(ls.id) + dot + styleAutoBannerActivity.Render(cadence)
		hint := dot + styleAutoBannerHint.Render("/loop stop "+ls.id)
		core := label + dot + detail
		if ansi.StringWidth(core+hint) <= width {
			return core + hint
		}
		if ansi.StringWidth(core) <= width {
			return core
		}
		return label + dot + styleAutoBannerActivity.Render(ls.id)
	}
	detail = styleAutoBannerActivity.Render(fmt.Sprintf("%d active", len(loops)))
	if loops[0].id != "" {
		detail += dot + styleAutoBannerActivity.Render("next "+loops[0].id)
	}
	hint := dot + styleAutoBannerHint.Render("/loop for status")
	core := label + dot + detail
	if ansi.StringWidth(core+hint) <= width {
		return core + hint
	}
	if ansi.StringWidth(core) <= width {
		return core
	}
	return label
}

func (m Model) loopBannerStates() []loopState {
	ids := m.activeLoopIDs()
	out := make([]loopState, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.loops[id])
	}
	return out
}

func requestGracefulExit(m Model) (tea.Model, tea.Cmd) {
	if m.activeLoopCount() > 0 {
		m.loopExitConfirmOpen = true
		m.loopExitConfirmCursor = 0
		return m, nil
	}
	return requestWorktreeAwareGracefulExit(m)
}

func renderLoopExitConfirm(m Model, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	var b strings.Builder
	b.WriteString(styleAssistantHeader.Render("Active loops will stop on exit"))
	b.WriteString("\nThese local loops end when you exit — they do not run in the background:\n\n")
	for _, id := range m.activeLoopIDs() {
		ls := m.loops[id]
		fmt.Fprintf(&b, "  %s · every %s · %s\n", id, compactDuration(ls.interval), ls.payload)
	}
	b.WriteString("\n")
	options := []string{"Exit anyway", "Stay"}
	for i, opt := range options {
		row := strings.Count(b.String(), "\n")
		h.row(row, i)
		cursor := "  "
		if i == m.loopExitConfirmCursor {
			cursor = "❯ "
		}
		b.WriteString(cursor + opt + "\n")
	}
	b.WriteString("\nEnter to confirm · Esc to cancel")
	return b.String()
}

func (m Model) updateLoopExitConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEsc:
		m.loopExitConfirmOpen = false
		return m, nil
	case tea.KeyUp, tea.KeyDown:
		if m.loopExitConfirmCursor == 0 {
			m.loopExitConfirmCursor = 1
		} else {
			m.loopExitConfirmCursor = 0
		}
		return m, nil
	case tea.KeyEnter:
		if m.loopExitConfirmCursor == 0 {
			m.loopExitConfirmOpen = false
			m.disarmAllLoops("")
			return maybeStartExitSaveTurn(m)
		}
		m.loopExitConfirmOpen = false
		return m, nil
	}
	return m, nil
}
