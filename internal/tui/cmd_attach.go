package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/filerefs"
)

// prepareOptions is how the interactive TUI resolves @ references: the
// user typed (or picked) the path, so outside-project paths are allowed
// (the deny-list still applies); images ride along only when the model
// can read them.
func (m *Model) prepareOptions() filerefs.PrepareOptions {
	return filerefs.PrepareOptions{
		Cwd:            m.cwd,
		AllowOutside:   true,
		AllowImages:    m.providerProfile.SupportsImages,
		ImageRejection: "current model does not support image input",
	}
}

// refPolicy records which @ references the user actually typed when the
// submitted text also contains pasted content. Only those may leave the
// working directory: a path that merely appears inside pasted text (a web
// page, a log, an email) is attacker-controllable and stays confined.
type refPolicy struct {
	restrict bool
	typed    map[string]bool
}

// pasteRefPolicy inspects the text exactly as typed (paste placeholders
// still unexpanded). With no pastes there is nothing to restrict.
func (m *Model) pasteRefPolicy(raw string) refPolicy {
	if len(m.pastes) == 0 {
		return refPolicy{}
	}
	typed := map[string]bool{}
	for _, r := range filerefs.Parse(raw) {
		typed[r.Path] = true
	}
	return refPolicy{restrict: true, typed: typed}
}

// apply narrows opts so only typed references may go outside the project.
func (p refPolicy) apply(opts filerefs.PrepareOptions) filerefs.PrepareOptions {
	if p.restrict {
		typed := p.typed
		opts.OutsideAllowed = func(path string) bool { return typed[path] }
	}
	return opts
}

// takeRefPolicy returns (and clears) the policy staged by the submit path
// for the turn about to start.
func (m *Model) takeRefPolicy() refPolicy {
	p := m.stagedRefPolicy
	m.stagedRefPolicy = refPolicy{}
	return p
}

func policyFromMessage(msg agent.UserMessage) refPolicy {
	if !msg.RestrictRefs {
		return refPolicy{}
	}
	typed := make(map[string]bool, len(msg.TypedRefs))
	for _, p := range msg.TypedRefs {
		typed[p] = true
	}
	return refPolicy{restrict: true, typed: typed}
}

// queuedMessage builds the agent message for a steering input typed
// mid-turn, carrying the paste policy so the delivery-time hook applies it.
func (m *Model) queuedMessage(content string, pol refPolicy) agent.UserMessage {
	msg := agent.UserMessage{Content: content, Timestamp: time.Now(), RestrictRefs: pol.restrict}
	for p := range pol.typed {
		msg.TypedRefs = append(msg.TypedRefs, p)
	}
	return msg
}

// noteAttachments prints a muted notice per reference (red for failures,
// which never block the turn) and records the turn's working set for
// /context. Image bytes are not retained in the snapshot.
func (m *Model) noteAttachments(refs []filerefs.Ref) {
	snap := append([]filerefs.Ref(nil), refs...)
	for i := range snap {
		snap[i].Data = nil
	}
	m.activeFileRefs = snap
	for _, r := range refs {
		text, isErr := filerefs.Describe(r)
		if isErr {
			m.appendLine(styleError.Render("· " + text))
		} else {
			m.appendLine(styleAuto.Render("· " + text))
		}
	}
}

// discardQueuedImages drops pasted images when a message is queued
// mid-turn (a queued message is text only) and says so, instead of
// silently losing them.
func (m *Model) discardQueuedImages() {
	if len(m.pastedImages) > 0 {
		m.appendLine(styleError.Render("· pasted images can't ride a message queued mid-turn; send them with your next prompt"))
	}
	m.pastedImages = nil
}

// clearFileRefs forgets the active working set (new or resumed session).
func (m *Model) clearFileRefs() { m.activeFileRefs = nil }

// prepareQueuedMessage returns the hook the agent loop calls when it
// delivers a steering message queued mid-turn. It resolves @ references
// on the loop's goroutine (so slow file reads never block the UI) using
// the options as they stand when the turn starts.
func (m *Model) prepareQueuedMessage() func(agent.UserMessage) agent.PreparedUser {
	base := m.prepareOptions()
	return func(msg agent.UserMessage) agent.PreparedUser {
		p := filerefs.Prepare(msg.Content, policyFromMessage(msg).apply(base))
		out := agent.PreparedUser{Content: p.Content}
		for _, img := range p.Images {
			out.Images = append(out.Images, adapter.ImageBlock{Data: img.Data, MediaType: img.MediaType})
		}
		for _, r := range p.Refs {
			text, isErr := filerefs.Describe(r)
			out.Notices = append(out.Notices, agent.AttachNotice{Text: text, Error: isErr})
		}
		return out
	}
}

// renderAttachNotices appends the attachment outcome lines carried by a
// delivered steering message.
func (m *Model) renderAttachNotices(notices []agent.AttachNotice) {
	for _, n := range notices {
		if n.Error {
			m.appendLine(styleError.Render("· " + n.Text))
		} else {
			m.appendLine(styleAuto.Render("· " + n.Text))
		}
	}
}

// attachLoadedMsg carries the result of a background @-reference load back
// to the Update loop. seq ties it to the submission that started it so a
// cancelled (Esc) load is ignored.
type attachLoadedMsg struct {
	seq   int
	input string
	pol   refPolicy
	prep  filerefs.Prepared
}

// startTurnAsync is the interactive submit path. A message with @
// references loads them on a background command — a slow disk, network
// mount or a large image never freezes typing — then starts the turn when
// the load completes. Messages without references start immediately.
func (m Model) startTurnAsync(input string) (tea.Model, tea.Cmd) {
	if m.cfg.Adapter == nil || len(filerefs.Parse(input)) == 0 {
		return m.startTurn(input)
	}
	pol := m.takeRefPolicy()
	opts := pol.apply(m.prepareOptions())
	m.attaching = true
	m.attachSeq++
	m.attachInput = input
	seq := m.attachSeq
	m.appendLine(styleAuto.Render("· attaching files… (Esc to cancel)"))
	return m, func() tea.Msg {
		return attachLoadedMsg{seq: seq, input: input, pol: pol, prep: filerefs.Prepare(input, opts)}
	}
}

// handleAttachLoaded finishes a background load: start the turn with the
// prepared message, unless the user cancelled it meanwhile.
func (m Model) handleAttachLoaded(msg attachLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.attaching || msg.seq != m.attachSeq {
		return m, nil // cancelled or superseded
	}
	m.attaching = false
	m.attachInput = ""
	if m.turnActive {
		// Something else (a background-agent wake turn) started a turn
		// while we were loading. Deliver as a steering message; the loop
		// re-resolves the references at delivery.
		select {
		case m.userMsgCh <- m.queuedMessage(msg.input, msg.pol):
			m.appendLine(styleAuto.Render(SysMsg(SysQueue, "queue", "next tool round", truncateForRender(msg.input, 80))))
		default:
			m.appendLine(styleAuto.Render(SysMsg(SysWarning, "queue", "already waiting", "resend after the turn")))
		}
		return m, nil
	}
	return m.startTurnPrepared(msg.input, "", time.Now(), &msg.prep)
}

// cancelAttach abandons a background load and puts the text back in the
// input box so nothing the user typed is lost.
func (m *Model) cancelAttach() {
	m.attaching = false
	m.attachSeq++
	m.textInput.SetValue(m.attachInput)
	m.textInput.CursorEnd()
	m.attachInput = ""
	m.appendLine(styleAuto.Render("· attaching cancelled"))
}
