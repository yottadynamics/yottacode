package trust

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PromptResult records what the user chose at the trust gate.
type PromptResult int

const (
	PromptYes PromptResult = iota
	PromptNo
)

// Prompt renders the first-launch trust dialog to out and reads a
// single-character answer from in. The wording mirrors Claude
// Code's prompt for users who arrive from there; plain-text only
// per [[feedback_no_emoji_in_ui]].
//
// Treated as Yes:  "y", "Y", "yes", "Yes", "1"
// Treated as No:   everything else (including empty input — the
//                  safe default is to NOT trust)
func Prompt(in io.Reader, out io.Writer, cwd string) (PromptResult, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return PromptNo, err
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, strings.Repeat("─", 72))
	fmt.Fprintln(out, "Accessing workspace:")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s\n", abs)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Quick safety check: is this a project you created or one you trust?")
	fmt.Fprintln(out, "(Your own code, a well-known open-source project, or work from your team.)")
	fmt.Fprintln(out, "If not, take a moment to review what's in this folder first.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "yottacode will be able to read, edit, and execute files here.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  [1] Yes, I trust this folder")
	fmt.Fprintln(out, "  [2] No, exit")
	fmt.Fprintln(out, strings.Repeat("─", 72))
	fmt.Fprint(out, "Choice [1/2]: ")

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return PromptNo, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	switch answer {
	case "y", "yes", "1":
		return PromptYes, nil
	default:
		return PromptNo, nil
	}
}

// Ensure runs the trust gate for cwd against an io.Reader/Writer
// pair. Used by tests with bytes.Buffer streams; production TUI
// path uses EnsureInteractive instead (which drives a Bubbletea
// picker).
//
//   - If IsTrusted(store, cwd, allowPaths) returns true: no-op.
//   - Else, if interactive==false (non-TTY oneshot mode): no-op.
//     Matches Claude's `-p` behavior — trust verification is
//     skipped in non-interactive runs.
//   - Else: render Prompt to in/out. On Yes, add cwd to the
//     store and save. On No, return ErrUserDeclined.
func Ensure(store *Store, storePath, cwd string, allowPaths []string, interactive bool, in io.Reader, out io.Writer) error {
	return ensure(store, storePath, cwd, allowPaths, interactive, func() (PromptResult, error) {
		return Prompt(in, out, cwd)
	}, out)
}

// EnsureInteractive is the production TUI entry point: same
// gating as Ensure, but the prompt step runs a Bubbletea picker
// (Up/Down/Enter) instead of the line-based fallback. Caller must
// have confirmed stdin/stdout are TTYs.
func EnsureInteractive(store *Store, storePath, cwd string, allowPaths []string) error {
	return ensure(store, storePath, cwd, allowPaths, true, func() (PromptResult, error) {
		return PromptInteractive(cwd)
	}, nil)
}

// ensure is the shared core driving both entry points. The
// promptFn closes over whichever rendering strategy the caller
// picked, so neither Ensure nor EnsureInteractive duplicates the
// trust-check / persist / decline logic.
func ensure(
	store *Store,
	storePath, cwd string,
	allowPaths []string,
	interactive bool,
	promptFn func() (PromptResult, error),
	notifyOut io.Writer,
) error {
	if IsTrusted(store, cwd, allowPaths) {
		return nil
	}
	if !interactive {
		return nil
	}
	res, err := promptFn()
	if err != nil {
		return err
	}
	if res == PromptNo {
		return ErrUserDeclined
	}
	if _, err := store.Add(cwd); err != nil {
		return err
	}
	if err := Save(storePath, store); err != nil {
		return err
	}
	if notifyOut != nil {
		fmt.Fprintln(notifyOut, "Trust recorded. Starting session.")
	}
	return nil
}

// ErrUserDeclined is returned by Ensure when the user picks "No"
// at the trust prompt. The caller should exit non-zero without
// printing a redundant "error:" prefix.
var ErrUserDeclined = errUserDeclined{}

type errUserDeclined struct{}

func (errUserDeclined) Error() string { return "trust declined; not opening session" }

// IsInteractiveStream reports whether f is a terminal. Wrapped
// here so the trust package can decide without pulling
// golang.org/x/term into every caller; the same check appears in
// cmd/yottacode/main.go for the update-prompt path.
func IsInteractiveStream(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
