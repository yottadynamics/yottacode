package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// pagerDoneMsg fires when the suspended pager exits and Bubbletea
// reclaims the terminal. err is the pager process's exit error (nil
// on the normal "user quit" path). Distinct type from editorDoneMsg
// so the update() switch can disambiguate which subprocess returned.
type pagerDoneMsg struct {
	err  error
	path string
}

// cmdSubagents implements the /subagents slash command. Subcommands:
//
//	/subagents                     — picker overlay, tasks view (Enter views, s stops)
//	/subagents types               — picker overlay, types view (t toggles back)
//	/subagents stop <id-prefix>    — cancel a running task from the cmdline
//	/subagents stop batch <id>     — cancel every running worker in one dispatch batch
//
// All viewing happens through the picker overlay (Enter on a row
// opens that task's transcript in $PAGER). Standalone `list` and
// `view` subcommands existed previously and were dropped — the
// picker's Enter shortcut and the bare `/subagents` form cover the
// same needs without splitting the surface or forcing the user to
// type IDs. Stop kept as a cmdline form because a user who already
// knows the ID (from the previous picker visit or the inline card)
// can interrupt a task without re-opening the overlay.
func cmdSubagents(m Model, args []string) (Model, tea.Cmd) {
	if m.subagentTasks == nil {
		m.appendLine(styleError.Render("subagents are not wired in this session (Agent tool unavailable)"))
		return m, nil
	}
	if len(args) == 0 {
		// Bare /subagents: open the interactive picker.
		m.openSubagentsPicker()
		return m, nil
	}
	sub := strings.ToLower(args[0])
	switch sub {
	case "stop":
		if len(args) < 2 {
			m.appendLine(styleError.Render("usage: /subagents stop <id-prefix> | /subagents stop batch <batch-id>"))
			return m, nil
		}
		// `stop batch <id>` kills a whole dispatch batch at once. A batch is up
		// to 8 workers; stopping them one at a time means hunting down each id
		// from the dock while the rest keep spending tokens.
		if strings.ToLower(args[1]) == "batch" {
			if len(args) < 3 {
				m.appendLine(styleError.Render("usage: /subagents stop batch <batch-id> (the id shown on the dock header)"))
				return m, nil
			}
			batchID := args[2]
			n := m.subagentTasks.CancelBatch(batchID)
			if n == 0 {
				m.appendLine(styleError.Render(fmt.Sprintf("no running subagents in batch %q", batchID)))
				return m, nil
			}
			m.appendLine(styleSubagentMeta.Render(fmt.Sprintf("canceled %s in batch %s — wait a moment for the runs to clean up", pluralizeWorkers(n), batchID)))
			return m, nil
		}
		t, ok := m.subagentTasks.FindByPrefix(args[1])
		if !ok {
			m.appendLine(styleError.Render(fmt.Sprintf("no subagent task matches %q", args[1])))
			return m, nil
		}
		if !m.subagentTasks.Cancel(t.ID) {
			m.appendLine(styleSubagentMeta.Render(fmt.Sprintf("task %s already finished or has no cancel hook", t.ID[:8])))
			return m, nil
		}
		m.appendLine(styleSubagentMeta.Render(fmt.Sprintf("canceled subagent task %s — wait a moment for the run to clean up", t.ID[:8])))
	case "types":
		// Open the picker overlay in types mode instead of dumping
		// multi-line content into scrollback. Press `t` inside the
		// picker to toggle back to the tasks view, Esc to close.
		m.openSubagentsPickerInMode(subagentsPickerModeTypes)
	default:
		m.appendLine(styleError.Render(fmt.Sprintf("unknown /subagents subcommand %q (try: types, stop)", sub)))
	}
	return m, nil
}

// openTranscriptInPager suspends the TUI to the system pager so the
// user can scroll/search the transcript without flooding scrollback.
// Resolution order: $YOTTACODE_PAGER > $PAGER > `less -RF` > `more`.
//
// Flag history (so we don't repeat past mistakes):
//   - The very first version used `less -RF` and scrolling worked.
//   - We then added `+F` (follow mode) for running subagents to give
//     live-tail UX; that broke arrow-key scrolling because follow
//     mode swallows scroll input until you Ctrl+C out of it.
//   - We then replaced `+F` with `+G` (open at end, scroll mode);
//     that LOOKED right but combined with `-F` it made less
//     instantly quit on short transcripts because `+G` positions at
//     EOF and `-F` exits when EOF is reached on a one-screen file.
//     Users reported "view doesn't even open."
//   - The current version is back to plain `less -RF`: open at top,
//     scroll keys just work, auto-quit on short transcripts. A custom
//     prompt (`-P`) keeps the available keys visible at the bottom
//     of every page so users don't have to memorize them.
//
// `running` is currently unused but retained on the signature so
// callers don't have to change if we revive auto-follow as a
// configurable behavior later.
func (m Model) openTranscriptInPager(path, idShort string, running bool) (Model, tea.Cmd) {
	_ = running // see comment above — reserved for future use
	if _, err := os.Stat(path); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("cannot open transcript: %v", err)))
		return m, nil
	}
	argv := resolvePagerCommand(path, running)
	if len(argv) == 0 {
		// No pager available — fall back to inline rendering so the
		// user still gets to read the transcript.
		body, err := os.ReadFile(path)
		if err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("cannot read transcript: %v", err)))
			return m, nil
		}
		m.appendLine("")
		m.appendLine(styleSubagentLabel.Render("transcript · task " + idShort + " (no pager available; install `less` for an in-place view)"))
		m.appendLine(string(body))
		return m, nil
	}
	// IMPORTANT: do NOT appendLine before returning the
	// tea.ExecProcess cmd. The outer Model.Update wraps pending
	// println output in tea.Sequence(flush, cmd), and that
	// interaction with tea.ExecProcess made the pager fail to
	// suspend the TUI (the println would land but the exec would
	// silently drop). The key-hint line that used to sit here has
	// been moved into the /subagents picker's description so
	// users see it before pressing Enter, without polluting the
	// pager-launch path.
	cmd := exec.Command(argv[0], argv[1:]...)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return pagerDoneMsg{err: err, path: path}
	})
}

// resolvePagerCommand picks the right pager binary + flags for
// viewing a transcript. Returns an argv slice (first element is the
// program, remaining are args including the path), or nil when no
// pager is available on PATH. Separated from openTranscriptInPager
// so the resolution rules can be unit-tested without touching the
// Bubbletea program state.
//
// Flag policy for the built-in `less` path:
//   - `-R` passes ANSI escapes through (the transcript carries them).
//   - `-F` auto-quits when the entire file fits one screen so short
//     transcripts don't need a manual `q`.
//
// We tried injecting a custom prompt via `-P` to display a key
// hint at the bottom of every screen, but less's `-P` argument
// parsing is finicky (the prompt must be attached: `-Phint`, not
// space-separated `-P hint`, on some versions) and getting it
// wrong makes less exit immediately. Reverted to the bare default;
// docs/subagents.md covers the available pager keys.
//
// The `running` parameter is currently unused; reserved for callers
// that want to opt into follow mode again in the future.
func resolvePagerCommand(path string, running bool) []string {
	_ = running
	envPager := strings.TrimSpace(os.Getenv("YOTTACODE_PAGER"))
	if envPager == "" {
		envPager = strings.TrimSpace(os.Getenv("PAGER"))
	}
	if envPager != "" {
		// Honor user's $PAGER verbatim. Split on whitespace so flags
		// like "less -FRSX" or "bat -p" pass through cleanly. We
		// deliberately do NOT inject our flags here — if the user
		// configured their own pager, respect their preferences.
		fields := strings.Fields(envPager)
		args := append([]string{}, fields[1:]...)
		args = append(args, path)
		return append([]string{fields[0]}, args...)
	}
	if _, err := exec.LookPath("less"); err == nil {
		// Plain `less -R` — no `-F` (quit-if-one-screen) flag.
		//
		// `-F` was convenient for short transcripts but actively
		// broken for just-started subagents: when the transcript
		// contains only the header (no progress events yet), it
		// fits one screen, `-F` triggers an immediate auto-quit,
		// and the user sees a one-frame flash that reads as "the
		// view doesn't open." Reverted so less always waits for
		// the user's `q`. Slight extra friction for tiny files,
		// predictable behavior for every case.
		return []string{"less", "-R", path}
	}
	if _, err := exec.LookPath("more"); err == nil {
		return []string{"more", path}
	}
	return nil
}
