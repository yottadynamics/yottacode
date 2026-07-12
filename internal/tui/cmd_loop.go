package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		for _, id := range m.activeLoopIDs() {
			m.appendLine(styleAuto.Render("[loop] " + m.loopStatusLine(id)))
		}
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
	m.appendLine(styleAuto.Render("[loop] " + m.loopStatusLine(id)))

	cmds := []tea.Cmd{loopTickCmd(interval, id, ls.gen)}
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
			if m.turnActive && m.turnCancel != nil {
				m.turnCancel()
			}
			return m, nil
		}
		m.appendLine(styleError.Render("[loop] multiple loops active — pass an ID or `all`"))
		return m, nil
	}
	id := args[0]
	if strings.EqualFold(id, "all") {
		m.disarmAllLoops("[loop] stopped all loops")
		if m.turnActive && m.turnCancel != nil {
			m.turnCancel()
		}
		return m, nil
	}
	if _, ok := m.loops[id]; !ok {
		m.appendLine(styleError.Render(fmt.Sprintf("[loop] no active loop %q", id)))
		return m, nil
	}
	m.disarmLoop(id, "[loop] "+id+" stopped")
	if m.turnActive && m.turnCancel != nil {
		m.turnCancel()
	}
	return m, nil
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
	next, cmd := m.startTurnWithDisplay(payload, "")
	return next.(Model), cmd
}

func (m *Model) ensureLoopStore() {
	if m.loops == nil {
		m.loops = map[string]loopState{}
	}
}

func (m Model) activeLoopCount() int { return len(m.activeLoopIDs()) }

func (m Model) activeLoopIDs() []string {
	if len(m.loops) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m.loops))
	seen := map[string]bool{}
	for _, id := range m.loopOrder {
		if ls, ok := m.loops[id]; ok && ls.active {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for id, ls := range m.loops {
		if ls.active && !seen[id] {
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

// disarmLoop turns one loop off and invalidates any scheduled tick by bumping
// its generation before removal. A no-op (prints nothing) when the ID is absent.
func (m *Model) disarmLoop(id, notice string) {
	ls, ok := m.loops[id]
	if !ok || !ls.active {
		return
	}
	ls.active = false
	ls.gen++
	m.loops[id] = ls
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

// loopStatusLine renders one loop summary shown by /loop and arm notices.
func (m Model) loopStatusLine(id string) string {
	ls, ok := m.loops[id]
	if !ok {
		return id + " · inactive"
	}
	when := "every " + ls.interval.String()
	count := "unbounded"
	if ls.remaining > 0 {
		count = fmt.Sprintf("%d left", ls.remaining)
	}
	return fmt.Sprintf("%s · %s · %s · expires %s · %q  (/loop stop %s)", id, when, count, formatLoopRemaining(time.Now(), ls.expiresAt), ls.payload, id)
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
	left := time.Until(expiresAt)
	if !now.IsZero() {
		left = expiresAt.Sub(now)
	}
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
		detail = styleAutoBannerActivity.Render(ls.id) + dot + styleAutoBannerActivity.Render("every "+ls.interval.String())
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
	b.WriteString(styleAssistantHeader.Render("Background work is running"))
	b.WriteString("\nThe following loops will stop when you exit:\n\n")
	for _, id := range m.activeLoopIDs() {
		ls := m.loops[id]
		fmt.Fprintf(&b, "  %s · every %s · %s\n", id, ls.interval, ls.payload)
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

func (m Model) updateLoopExitConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
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
