package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/checkpoint"
)

// checkpointsPickerState backs the /checkpoints overlay + Esc-Esc
// chord. Two screens: a list of past user prompts (cursor=cursor),
// then a four-action menu once the user picks one (cursor=actionIdx).
type checkpointsPickerState struct {
	entries   []checkpoint.ManifestEntry
	cursor    int
	picked    *checkpoint.ManifestEntry
	actionIdx int
}

// checkpointsPickerActions enumerates the action menu shown after the
// user picks a checkpoint. Order is restoration-strength descending —
// the most-common "restore everything" is first.
var checkpointsPickerActions = []struct {
	Label string
	Desc  string
}{
	{"Restore code and conversation", "rewrites tracked files and rewinds history to this prompt"},
	{"Restore conversation only", "rewinds history; files untouched"},
	{"Restore code only", "rewrites tracked files; history unchanged"},
	{"Summarize from here", "compress history up to this prompt; files untouched"},
}

// escChordWindow is how long after a bare Esc a second Esc still counts
// as the /checkpoints chord. Mirrors the snappy 'gg' / 'dd' style chord
// timing in modal editors — long enough that a deliberate tap-tap
// reliably fires, short enough that two unrelated Escs (closing two
// overlays, say) don't accidentally trigger.
const escChordWindow = 500 * time.Millisecond

// cmdCheckpoints is the /checkpoints slash handler. Same entry point
// as the Esc-Esc chord — both call openCheckpointsPicker.
func cmdCheckpoints(m Model, _ []string) (Model, tea.Cmd) {
	m.openCheckpointsPicker()
	return m, nil
}

// openCheckpointsPicker loads the session's manifest and opens the
// overlay. Empty manifest short-circuits with a scrollback line — no
// point showing an empty picker. Failure to load logs a soft error and
// returns; the rest of the TUI keeps working.
func (m *Model) openCheckpointsPicker() {
	store, ok := m.cfg.Checkpoints.(*checkpoint.Store)
	if !ok || store == nil {
		m.appendLine(styleAuto.Render("[checkpoints] checkpoint store unavailable for this session"))
		return
	}
	man, err := store.LoadManifest(m.sess.ID)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[checkpoints] could not read manifest: %v", err)))
		return
	}
	if len(man.Entries) == 0 {
		m.appendLine(styleAuto.Render("[checkpoints] no checkpoints yet — send a prompt and try again"))
		return
	}
	m.checkpointsPicker = &checkpointsPickerState{entries: man.Entries}
	m.checkpointsPickerOpen = true
}

// updateCheckpointsPicker handles keystrokes while the overlay is
// open. On the prompt screen: Up/Down navigate, Enter advances to the
// action menu, Esc closes. On the action menu: Up/Down navigate, Enter
// commits, Esc returns to the prompt list.
func (m Model) updateCheckpointsPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.checkpointsPicker == nil {
		m.checkpointsPickerOpen = false
		return m, nil
	}
	state := m.checkpointsPicker

	if state.picked != nil {
		switch msg.Type {
		case tea.KeyUp:
			if state.actionIdx > 0 {
				state.actionIdx--
			}
			return m, nil
		case tea.KeyDown:
			if state.actionIdx < len(checkpointsPickerActions)-1 {
				state.actionIdx++
			}
			return m, nil
		case tea.KeyEnter:
			picked := *state.picked
			action := state.actionIdx
			m.checkpointsPickerOpen = false
			m.checkpointsPicker = nil
			return m.applyCheckpointAction(picked, action)
		case tea.KeyEsc:
			state.picked = nil
			state.actionIdx = 0
			return m, nil
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyUp:
		if state.cursor > 0 {
			state.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if state.cursor < len(state.entries)-1 {
			state.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		entry := state.entries[state.cursor]
		state.picked = &entry
		state.actionIdx = 0
		return m, nil
	case tea.KeyEsc:
		m.checkpointsPickerOpen = false
		m.checkpointsPicker = nil
		return m, nil
	}
	return m, nil
}

// renderCheckpointsPicker draws the active screen (prompt list or
// action menu). Width is reserved for future right-align columns;
// today the rows use the same renderMenuItem helper as other pickers.
func renderCheckpointsPicker(state *checkpointsPickerState, _ int) string {
	if state.picked != nil {
		preview := truncatePromptPreview(state.picked.PromptPreview, 60)
		header := renderMenuHeader(
			"Restore from \""+preview+"\"",
			"Pick an action. Enter commits; Esc returns.",
		)
		body := header + "\n"
		maxLabel := 0
		for _, a := range checkpointsPickerActions {
			if l := len(a.Label); l > maxLabel {
				maxLabel = l
			}
		}
		for i, a := range checkpointsPickerActions {
			body += renderMenuItem(menuItemOpts{
				Label:      a.Label,
				LabelWidth: maxLabel,
				Desc:       a.Desc,
				Cursor:     i == state.actionIdx,
			}) + "\n"
		}
		body += "\n" + styleAuto.Render("Bash and git mutations are not tracked.")
		return body
	}

	header := renderMenuHeader(
		"Restore from a checkpoint",
		"Newest first. Enter to pick. Esc closes.",
	)
	const maxPreview = 60
	body := header + "\n"
	for i, en := range state.entries {
		body += renderMenuItem(menuItemOpts{
			Label:      truncatePromptPreview(en.PromptPreview, maxPreview),
			LabelWidth: maxPreview,
			Desc:       formatPlanAge(time.Since(en.Created)),
			Cursor:     i == state.cursor,
		}) + "\n"
	}
	body += "\n" + styleAuto.Render("Bash and git mutations are not tracked.")
	return body
}

// applyCheckpointAction runs the chosen restore. Order is critical:
// files first (so a write failure aborts before the conversation is
// also clobbered), session second, in-memory state third. Errors are
// reported in scrollback; partial failures are non-fatal.
func (m Model) applyCheckpointAction(entry checkpoint.ManifestEntry, action int) (Model, tea.Cmd) {
	store, ok := m.cfg.Checkpoints.(*checkpoint.Store)
	if !ok || store == nil {
		m.appendLine(styleError.Render("[checkpoints] checkpoint store unavailable"))
		return m, nil
	}

	switch action {
	case 0, 2: // restore code (and optionally conversation)
		errs, err := store.RestoreCode(m.sess.ID, entry.CheckpointID)
		if err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("[checkpoints] restore failed: %v", err)))
			return m, nil
		}
		for _, e := range errs {
			m.appendLine(styleError.Render(fmt.Sprintf("[checkpoints] %v", e)))
		}
		if action == 0 {
			return m.restoreConversation(entry, store, true)
		}
		m.appendLine(styleAuto.Render(fmt.Sprintf("[checkpoints] restored code from \"%s\"", truncatePromptPreview(entry.PromptPreview, 60))))
		return m, nil
	case 1: // restore conversation only
		return m.restoreConversation(entry, store, true)
	case 3: // summarize from here
		// Truncate the in-memory conversation to the checkpoint slice,
		// then dispatch the existing /summarize handler. Files
		// intentionally untouched.
		nm, _ := m.restoreConversation(entry, store, false)
		return cmdSummarize(nm, nil)
	}
	return m, nil
}

// restoreConversation truncates messages to the checkpoint snapshot
// and rebuilds the transcript. When prefill is true, also auto-fills
// the textarea with the original prompt so the user can edit + resend
// (mirrors Claude /rewind's "prompt reappears in the input field").
func (m Model) restoreConversation(entry checkpoint.ManifestEntry, store *checkpoint.Store, prefill bool) (Model, tea.Cmd) {
	restored, err := store.LoadMessages(m.sess.ID, entry.CheckpointID)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[checkpoints] could not load conversation: %v", err)))
		return m, nil
	}
	m.sess.Messages = restored
	if err := m.sess.Save(); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[checkpoints] save session: %v", err)))
	}
	rebuildTranscript(&m)
	if prefill {
		m.textInput.SetValue(entry.PromptPreview)
		m.textInput.CursorEnd()
	}
	m.appendLine(styleAuto.Render(fmt.Sprintf("[checkpoints] restored to \"%s\"", truncatePromptPreview(entry.PromptPreview, 60))))
	return m, nil
}

// truncatePromptPreview shortens a prompt string for display in the
// picker, preserving the leading slice and replacing the tail with an
// ellipsis when needed. Whitespace and newlines collapse to single
// spaces so multi-line prompts render on one row.
func truncatePromptPreview(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	if max < 4 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
