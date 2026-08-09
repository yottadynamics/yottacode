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
		return m, nil
	}

	// Only stop/off forms disarm loops. Prose payloads like
	// `/loop 30s stop the deploy` still arm normally because the disarm verb
	// is recognized before interval parsing.
	if strings.EqualFold(args[0], "stop") || strings.EqualFold(args[0], "off") {
		return cmdLoopStop(m, args[1:])
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

	remaining := -1
	if len(rest) > 0 {
		if n, ok := parseLoopCount(rest[0]); ok {
			remaining, rest = n, rest[1:]
		}
	}
	payload := strings.TrimSpace(strings.Join(rest, " "))
	if payload == "" {
		m.appendLine(styleError.Render(
			"usage: /loop <interval> [Nx] <prompt|/command> — e.g. /loop 5m /git-review-pr"))
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
	}
	if remaining > 0 {
		ls.total = remaining
	}
	m.loops[id] = ls
	m.loopOrder = append(m.loopOrder, id)
	m.appendLine(renderLoopCard(ls, m.width))

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
	if m.cfg.LoopControl != nil {
		m.cfg.LoopControl.SetContext(loopTurnContext(ls))
		m.cfg.LoopControl.SetTurnActive(true)
	}
	next, cmd := m.startTurnWithDisplay(payload, "")
	nm := next.(Model)
	if nm.turnActive {
		nm.currentLoopTurnID = id
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
	m.currentLoopTurnID = ""
	stop, reason := m.cfg.LoopControl.ConsumeStop()
	m.cfg.LoopControl.SetTurnActive(false)
	if !stop || id == "" {
		return
	}
	// The loop may already be gone (user Esc, expiry, bounded final iteration);
	// disarmLoop is a no-op then.
	notice := "[loop] " + id + " stopped by the agent"
	if reason = strings.TrimSpace(reason); reason != "" {
		notice += ": " + reason
	}
	m.disarmLoop(id, notice)
}

// loopTurnContext is the one-line loop descriptor fed into the loop-assessment
// addendum (LoopIterationAddendum's %s), so the model knows the cadence and
// whether the loop is bounded when deciding whether to keep going. Called with
// ls after fireLoopIteration has decremented a bounded loop's remaining count.
func loopTurnContext(ls loopState) string {
	cadence := "every " + compactDuration(ls.interval)
	if ls.total > 0 {
		return fmt.Sprintf("It runs %s, iteration %d of %d.", cadence, ls.total-ls.remaining, ls.total)
	}
	return fmt.Sprintf("It runs %s and is unbounded (no fixed number of iterations) — it repeats until stopped.", cadence)
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

// renderLoopListPanel is the body of the bare-`/loop` overlay: a compact menu
// (one row per loop, house picker style via renderMenuItem) plus a stop/dismiss
// hint. Rendered above the cmdline via renderInlineOverlay so checking loops
// never clutters the transcript. Deliberately NOT the multi-line arm card —
// this is a scannable list of what's running, keyed by ID for `/loop stop`.
func (m Model) renderLoopListPanel() string {
	ids := m.activeLoopIDs()
	width := m.width
	if width <= 0 {
		width = 80
	}
	label := "loops"
	if len(ids) == 1 {
		label = "loop"
	}
	var b strings.Builder
	b.WriteString(renderMenuHeader(fmt.Sprintf("%d active %s", len(ids), label), ""))
	b.WriteString("\n")

	const labelW = 13
	descBudget := width - (2 + labelW + 1 + 2) // cursor + label + space + check columns
	for _, id := range ids {
		ls := m.loops[id]
		count := "unbounded"
		if ls.remaining > 0 {
			count = fmt.Sprintf("%d left", ls.remaining)
		}
		desc := fmt.Sprintf("every %s · %s · expires %s  ·  %s",
			compactDuration(ls.interval), count, formatLoopRemaining(time.Now(), ls.expiresAt), ls.payload)
		if descBudget > 8 {
			desc = ansi.Truncate(desc, descBudget, "…")
		}
		b.WriteString(renderMenuItem(menuItemOpts{Label: id, LabelWidth: labelW, Desc: desc}))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleAutoBannerHint.Render("/loop stop <id> to stop one · /loop stop all · any key to dismiss"))
	return b.String()
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
		detail = styleAutoBannerActivity.Render(ls.id) + dot + styleAutoBannerActivity.Render("every "+compactDuration(ls.interval))
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
	return maybeStartExitSaveTurn(m)
}

func renderLoopExitConfirm(m Model) string {
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
