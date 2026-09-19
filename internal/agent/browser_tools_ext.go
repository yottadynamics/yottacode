package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// BrowserEvalTool evaluates a JavaScript expression in the active page and
// returns the JSON-rendered result — the way to read page state or extract
// structured data the accessibility snapshot doesn't expose (a table's
// cells, a global, computed style, localStorage). Always requires approval,
// and never earns an "always allow" shortcut: page JS can read anything the
// page's origin can, including cookies and storage, and can act on the page
// exactly as a click would.
type BrowserEvalTool struct{ browserToolBase }

func (t *BrowserEvalTool) Name() string { return "browser_eval" }
func (t *BrowserEvalTool) Description() string {
	return "Evaluate a JavaScript expression in the active page's main frame (like typing it in the DevTools console) and return the result as JSON. A returned Promise is awaited. Use it to read page state or extract structured data the accessibility snapshot doesn't show, e.g. `document.title` or `[...document.querySelectorAll('tr')].map(r => r.innerText)`. Expressions are limited to 4000 characters and output is truncated at 20000. Requires approval: page JavaScript can read cookies and storage and act on the page."
}
func (t *BrowserEvalTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expression": map[string]any{"type": "string", "description": "JavaScript expression to evaluate. Not a function body — wrap statements in an IIFE, e.g. (() => { …; return x })()."},
		},
		"required": []string{"expression"},
	}
}
func (t *BrowserEvalTool) RequiresApproval(string) bool { return t.Enabled }

// browserEvalMaxChars caps an expression's length. The approval prompt shows
// the whole expression — a preview that cut it short would let whatever sits
// past the cut run unseen — so instead of truncating the preview, an
// expression too long to show in full is refused. Generous for real
// extraction one-liners, small enough to read before approving.
const browserEvalMaxChars = 4000

// PreviewCall shows the expression in full, verbatim. It must be exactly what
// Execute will run, since it is what the person approving reads.
func (t *BrowserEvalTool) PreviewCall(argsJSON string) string {
	var a struct {
		Expression string `json:"expression"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_eval(%s)", strings.TrimSpace(a.Expression))
}
func (t *BrowserEvalTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_eval: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Expression) == "" {
		return "", fmt.Errorf("browser_eval: expression is required")
	}
	if n := len([]rune(a.Expression)); n > browserEvalMaxChars {
		return "", fmt.Errorf("browser_eval: expression is %d characters, over the %d limit — an approval prompt has to show it in full; extract less per call or simplify it", n, browserEvalMaxChars)
	}
	out, err := t.Session.Eval(ctx, a.Expression)
	if err != nil {
		return "", fmt.Errorf("browser_eval: %w", err)
	}
	return out, nil
}

// BrowserBackTool navigates the active page to the previous entry in its
// history. Approval-gated like the other page-changing tools: it re-renders
// a page (and re-runs its scripts) even though it can only return to a page
// this session already visited.
type BrowserBackTool struct{ browserToolBase }

func (t *BrowserBackTool) Name() string { return "browser_back" }
func (t *BrowserBackTool) Description() string {
	return "Go back to the previous page in the active tab's history, like the browser's Back button. Errors if there is no earlier page."
}
func (t *BrowserBackTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *BrowserBackTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserBackTool) PreviewCall(string) string    { return "browser_back()" }
func (t *BrowserBackTool) Execute(ctx context.Context, _ string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	res, err := t.Session.Back(ctx)
	if err != nil {
		return "", fmt.Errorf("browser_back: %w", err)
	}
	return fmt.Sprintf("went back to %s (title: %q)", res.URL, res.Title), nil
}

// BrowserDialogTool sets how the session answers JavaScript dialogs
// (alert/confirm/prompt/beforeunload) from now on. The default is dismiss;
// every dialog, whichever way it is answered, is recorded and shows up in
// browser_console_logs. Choosing "accept" needs approval — it lets a
// confirm() go through, which may gate a destructive action the triggering
// click's approval never covered — while "dismiss" only reduces capability.
type BrowserDialogTool struct{ browserToolBase }

func (t *BrowserDialogTool) Name() string { return "browser_dialog" }
func (t *BrowserDialogTool) Description() string {
	return "Set how JavaScript dialogs (alert, confirm, prompt, beforeunload) are answered from now on: \"dismiss\" (the default — confirm() returns false) or \"accept\" (confirm() returns true; prompt() returns prompt_text, or its default if omitted). Set it BEFORE the click or navigation that triggers the dialog, since a dialog blocks the page until answered. Every dialog is recorded — type, message, and how it was answered — and appears in browser_console_logs. \"accept\" requires approval."
}
func (t *BrowserDialogTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":      map[string]any{"type": "string", "enum": []string{"accept", "dismiss"}, "description": "How to answer dialogs from now on"},
			"prompt_text": map[string]any{"type": "string", "description": "Answer for prompt() dialogs when accepting. Omit to accept the prompt's own default value."},
		},
		"required": []string{"action"},
	}
}
func (t *BrowserDialogTool) RequiresApproval(argsJSON string) bool {
	if !t.Enabled {
		return false
	}
	var a struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return strings.EqualFold(strings.TrimSpace(a.Action), "accept")
}
func (t *BrowserDialogTool) PreviewCall(argsJSON string) string {
	var a struct {
		Action     string `json:"action"`
		PromptText string `json:"prompt_text"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	// The text a prompt() will receive is part of what is being approved.
	if a.PromptText != "" {
		return fmt.Sprintf("browser_dialog(%s, prompt_text=%q)", a.Action, a.PromptText)
	}
	return fmt.Sprintf("browser_dialog(%s)", a.Action)
}
func (t *BrowserDialogTool) Execute(_ context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Action     string `json:"action"`
		PromptText string `json:"prompt_text"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_dialog: invalid args: %w", err)
	}
	var accept bool
	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "accept":
		accept = true
	case "dismiss":
	default:
		return "", fmt.Errorf("browser_dialog: action must be \"accept\" or \"dismiss\", got %q", a.Action)
	}
	if err := t.Session.SetDialogPolicy(accept, a.PromptText); err != nil {
		return "", fmt.Errorf("browser_dialog: %w", err)
	}
	if accept {
		return "JavaScript dialogs will now be accepted (until browser_dialog is called again)", nil
	}
	return "JavaScript dialogs will now be dismissed", nil
}
