package browser

import (
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// dialogPolicy decides how a session answers JS-initiated dialogs
// (alert/confirm/prompt/beforeunload). One policy is shared by every page
// the session tracks, so a browser_dialog call applies to popups too.
//
// The default is dismiss: the click that triggered a confirm() was
// approval-gated, but the confirm can gate its own separate destructive
// action, and silently accepting it would go beyond what that approval
// covered. Accepting is opt-in per session (browser_dialog), and needs its
// own approval.
type dialogPolicy struct {
	mu         syncutil.Mutex
	accept     bool
	promptText string
}

// set replaces the policy. Safe on a nil receiver (a no-op) so a bare
// session literal in a test never dereferences nil.
func (d *dialogPolicy) set(accept bool, promptText string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.accept, d.promptText = accept, promptText
	d.mu.Unlock()
}

// get returns the current policy; the zero (dismiss) policy on a nil receiver.
func (d *dialogPolicy) get() (accept bool, promptText string) {
	if d == nil {
		return false, ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.accept, d.promptText
}

// handleDialogs answers every JS dialog on pg according to the session's
// dialogPolicy, and records each one — type, message, and what was done —
// in tp's console buffer so browser_console_logs shows it. A dialog blocks
// the page's execution (and every browser_* call that touches it) until
// something answers Page.handleJavaScriptDialog, and there is no human
// here to click it, so every dialog is answered immediately rather than
// left to hang a tool call until its timeout.
func handleDialogs(pg *rod.Page, tp *trackedPage) {
	go pg.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
		accept, promptText := tp.dialogs.get()
		req := proto.PageHandleJavaScriptDialog{Accept: accept}
		if accept && e.Type == proto.PageDialogTypePrompt {
			if promptText == "" {
				promptText = e.DefaultPrompt
			}
			req.PromptText = promptText
		}
		_ = req.Call(pg)
		verb := "dismissed"
		if accept {
			verb = "accepted"
		}
		tp.recordConsole(ConsoleEntry{
			Level: "dialog",
			Text:  fmt.Sprintf("%s %q -> %s", e.Type, e.Message, verb),
			At:    time.Now(),
		})
	})()
}
