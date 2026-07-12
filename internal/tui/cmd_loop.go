package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// cmdLoop arms, inspects, or disarms the recurring /loop command. It is a
// scheduler over the existing single-turn pipeline — it never spawns its
// own goroutine or runs more than one turn at a time. Each iteration is an
// ordinary turn, so its output lands in the normal scrollback + session
// store (no separate job store) and it obeys the standard approval flow.
//
//	/loop 5m <prompt>        every 5m, dispatch <prompt>
//	/loop 5m /git-review-pr  every 5m, run the /git-review-pr slash command
//	/loop <prompt>           self-paced: re-fire when the previous turn ends
//	/loop 3x <prompt>        self-paced, 3 iterations, then stop
//	/loop 30s 3x <prompt>    every 30s, 3 times, then stop
//	/loop stop               disarm (also Esc / Ctrl+C while armed)
//	/loop                    show current status
func cmdLoop(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		if m.loop.active {
			m.appendLine(styleAuto.Render("[loop] " + m.loopStatusLine()))
		} else {
			m.appendLine(styleAuto.Render("[loop] none armed — /loop [interval] [Nx] <prompt|/cmd>"))
		}
		return m, nil
	}

	// Only a bare `/loop stop` (or `off`) disarms — otherwise a prose payload
	// that happens to start with "stop"/"off" (e.g. `/loop stop the deploy`)
	// would be swallowed as the disarm verb.
	if len(args) == 1 && (strings.EqualFold(args[0], "stop") || strings.EqualFold(args[0], "off")) {
		if !m.loop.active {
			m.appendLine(styleAuto.Render("[loop] nothing to stop"))
			return m, nil
		}
		m.disarmLoop("[loop] stopped")
		// Stop means stop everything, including any in-flight turn. The
		// command is PreservesTurn=true, so the dispatcher did not cancel
		// for us — do it explicitly here.
		if m.turnActive && m.turnCancel != nil {
			m.turnCancel()
		}
		return m, nil
	}

	rest := args
	var interval time.Duration
	if d, ok := parseLoopInterval(rest[0]); ok {
		if d < loopMinInterval {
			m.appendLine(styleError.Render(fmt.Sprintf(
				"[loop] interval %s is below the %s floor — pick a larger interval", d, loopMinInterval)))
			return m, nil
		}
		interval, rest = d, rest[1:]
	}
	remaining := -1
	if len(rest) > 0 {
		if n, ok := parseLoopCount(rest[0]); ok {
			remaining, rest = n, rest[1:]
		}
	}
	payload := strings.TrimSpace(strings.Join(rest, " "))
	if payload == "" {
		m.appendLine(styleError.Render(
			"usage: /loop [interval] [Nx] <prompt|/command> — e.g. /loop 5m /git-review-pr"))
		return m, nil
	}
	// Refuse payloads that don't make sense to repeat: another /loop (would
	// clobber its own state), or a session-lifecycle command that ends or
	// resets the very session the loop runs in.
	switch loopPayloadHead(payload) {
	case "/loop":
		m.appendLine(styleError.Render("[loop] payload cannot be another /loop command"))
		return m, nil
	case "/quit", "/clear":
		m.appendLine(styleError.Render("[loop] payload can't be /quit or /clear — a loop must not end or reset its own session"))
		return m, nil
	}
	// A slash payload must resolve to a real command — otherwise an unbounded
	// loop would just print "unknown command" every interval forever. Catch a
	// typo'd command name at arm time instead.
	if strings.HasPrefix(payload, "/") {
		name := strings.TrimPrefix(loopPayloadHead(payload), "/")
		if m.findSlash(name) == nil {
			m.appendLine(styleError.Render(fmt.Sprintf("[loop] unknown command /%s — not arming (see /help)", name)))
			return m, nil
		}
	}

	if m.loop.active {
		m.appendLine(styleAuto.Render("[loop] replacing the previously armed loop"))
	}
	m.loop.gen++ // invalidate any tick scheduled by the prior loop
	m.loop.active = true
	m.loop.payload = payload
	m.loop.isSlash = strings.HasPrefix(payload, "/")
	m.loop.interval = interval
	m.loop.remaining = remaining
	m.loop.total = 0
	if remaining > 0 {
		m.loop.total = remaining
	}
	m.appendLine(styleAuto.Render("[loop] " + m.loopStatusLine()))

	var cmds []tea.Cmd
	if interval > 0 {
		cmds = append(cmds, loopTickCmd(interval, m.loop.gen))
	}
	// Kick off iteration 1 immediately when idle (both modes). When a turn
	// is active, interval loops wait for the first tick and self-paced
	// loops wait for turnEndedMsg.
	if !m.turnActive && !m.summarizing {
		next, cmd := m.fireLoopIteration()
		m = next
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.loop.active && m.loop.interval == 0 && !m.turnActive {
			m.disarmLoop("[loop] stopped — payload started no turn (self-paced needs a turn to re-fire; add an interval)")
		}
	}
	return m, tea.Batch(cmds...)
}

// fireLoopIteration dispatches one loop iteration and advances the
// bounded-count bookkeeping. The caller must ensure no turn is active.
// Slash payloads route through runSlash (which interprets the leading /);
// prose routes through startTurnWithDisplay (which sends it verbatim).
func (m Model) fireLoopIteration() (Model, tea.Cmd) {
	if m.loop.remaining > 0 {
		m.loop.remaining--
		if m.loop.remaining == 0 {
			m.loop.active = false // dispatch the final iteration, then disarm
		}
	}
	// Progress line for bounded loops so a long count shows where it is.
	if m.loop.total > 0 {
		m.appendLine(styleAuto.Render(fmt.Sprintf("[loop] iteration %d/%d", m.loop.total-m.loop.remaining, m.loop.total)))
	}
	payload := m.loop.payload
	if m.loop.isSlash {
		// dispatchSlash, not runSlash: a loop must not re-record the same
		// command into ↑-history on every iteration.
		return m.dispatchSlash(payload)
	}
	next, cmd := m.startTurnWithDisplay(payload, "")
	return next.(Model), cmd
}

// refireSelfPacedLoop advances a self-paced /loop (interval==0) when the
// model returns to idle — after an ordinary turn OR after an auto-summarize.
// Interval loops are timer-driven (loopTickMsg) and are left untouched here.
// Returns the updated model, a cmd to run (nil if none), and whether it
// fired, so callers can early-return the cmd. If the iteration starts no
// turn, the loop disarms rather than spin. Called from both the turnEndedMsg
// and summaryDoneMsg handlers so a self-paced loop survives a compaction
// cycle instead of silently stalling.
func (m Model) refireSelfPacedLoop() (Model, tea.Cmd, bool) {
	if !m.loop.active || m.loop.interval != 0 || m.summarizing {
		return m, nil, false
	}
	next, cmd := m.fireLoopIteration()
	m = next
	if !m.turnActive {
		m.disarmLoop("[loop] stopped — last iteration started no turn (add an interval to keep going)")
	}
	return m, cmd, true
}

// disarmLoop turns the loop off and invalidates any scheduled tick by
// bumping the generation. A no-op (prints nothing) when nothing is armed.
func (m *Model) disarmLoop(notice string) {
	if !m.loop.active {
		return
	}
	m.loop.active = false
	m.loop.gen++
	if notice != "" {
		m.appendLine(styleAuto.Render(notice))
	}
}

// loopStatusLine renders the one-line summary shown when a loop is armed
// or /loop is called with no args.
func (m Model) loopStatusLine() string {
	when := "self-paced"
	if m.loop.interval > 0 {
		when = "every " + m.loop.interval.String()
	}
	count := "unbounded"
	if m.loop.remaining > 0 {
		count = fmt.Sprintf("%d left", m.loop.remaining)
	}
	return fmt.Sprintf("%s · %s · %q  (Esc / Ctrl+C / /loop stop to end)", when, count, m.loop.payload)
}

// parseLoopInterval accepts a Go duration token (30s, 5m, 1h). Returns
// (0,false) when the token isn't a positive duration, so the caller
// treats it as the start of the payload (a self-paced loop).
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

// renderLoopBanner is the one-line indicator above the cmdline while a
// /loop is armed, so a live loop — and how to stop it — stays visible after
// the arm line scrolls away. It is additive: a loop can be armed in normal,
// auto, or plan mode, so the View stacks it below any mode banner rather
// than replacing it. Reuses the auto-banner styles for a consistent look
// and degrades gracefully on narrow terminals. The label is plain text (no
// leading glyph) — the codebase reserves its ▸/◈/⚠ icons for the existing
// mode banners and avoids new marker glyphs, several of which risk
// emoji-presentation / double-width rendering that would skew the width math.
func renderLoopBanner(ls loopState, width int) string {
	if width <= 0 {
		width = 80
	}
	when := "self-paced"
	if ls.interval > 0 {
		when = "every " + ls.interval.String()
	}
	label := styleAutoBannerLabel.Render("loop")
	dot := styleAutoBannerSep.Render(" · ")
	detail := styleAutoBannerActivity.Render(when)
	if ls.remaining > 0 {
		detail += dot + styleAutoBannerActivity.Render(fmt.Sprintf("%d left", ls.remaining))
	}
	hint := dot + styleAutoBannerHint.Render("/loop stop to end")
	core := label + dot + detail
	if ansi.StringWidth(core+hint) <= width {
		return core + hint
	}
	if ansi.StringWidth(core) <= width {
		return core
	}
	return label
}
