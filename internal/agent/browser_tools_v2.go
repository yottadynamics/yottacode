package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/browser"
)

// observe runs an action bracketed by two page digests and returns a
// "page changes" block describing what the action did (URL/title change,
// elements that appeared, disappeared or changed state such as a radio becoming
// checked, new alerts, auto-answered dialogs). The block is page-derived text,
// so it is wrapped as untrusted. The first digest is taken without waiting; the
// second waits for the page to settle. A failed digest only means no summary.
func (b browserToolBase) observe(ctx context.Context, act func() error) (string, error) {
	before, _ := b.Session.PageDigest(ctx, false)
	if err := act(); err != nil {
		return "", err
	}
	after, _ := b.Session.PageDigest(ctx, true)
	diff := browser.DiffDigest(before, after)
	if diff == "" {
		return "", nil
	}
	return "\npage changes:\n" + wrapUntrusted(diff), nil
}

// BrowserHoverTool moves the mouse over an element.
type BrowserHoverTool struct{ browserToolBase }

func (t *BrowserHoverTool) Name() string { return "browser_hover" }
func (t *BrowserHoverTool) Description() string {
	return "Move the mouse over an element, scrolling it into view first, to open hover menus and tooltips. Reports what changed on the page."
}
func (t *BrowserHoverTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector": map[string]any{"type": "string", "description": "The element to hover." + browserSelectorHelp},
		},
		"required": []string{"selector"},
	}
}
func (t *BrowserHoverTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserHoverTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_hover(%s)", previewQuote(a.Selector, previewTextMax))
}
func (t *BrowserHoverTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_hover: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Selector) == "" {
		return "", fmt.Errorf("browser_hover: selector is required")
	}
	changes, err := t.observe(ctx, func() error { return t.Session.Hover(ctx, a.Selector) })
	if err != nil {
		return "", fmt.Errorf("browser_hover: %w", err)
	}
	return fmt.Sprintf("hovered %q", a.Selector) + changes, nil
}

// BrowserDragTool drags one element onto another with synthesised mouse events.
type BrowserDragTool struct{ browserToolBase }

func (t *BrowserDragTool) Name() string { return "browser_drag" }
func (t *BrowserDragTool) Description() string {
	return "Press the mouse on one element, move to another and release, for sliders, sortable lists and drag handles. Uses synthesised mouse events, so native HTML5 draggable elements may not respond. Reports what changed on the page."
}
func (t *BrowserDragTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from": map[string]any{"type": "string", "description": "The element to press on." + browserSelectorHelp},
			"to":   map[string]any{"type": "string", "description": "The element to release over." + browserSelectorHelp},
		},
		"required": []string{"from", "to"},
	}
}
func (t *BrowserDragTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserDragTool) PreviewCall(argsJSON string) string {
	var a struct{ From, To string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_drag(%s -> %s)", previewQuote(a.From, previewTextMax), previewQuote(a.To, previewTextMax))
}
func (t *BrowserDragTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct{ From, To string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_drag: invalid args: %w", err)
	}
	if strings.TrimSpace(a.From) == "" || strings.TrimSpace(a.To) == "" {
		return "", fmt.Errorf("browser_drag: from and to are required")
	}
	changes, err := t.observe(ctx, func() error { return t.Session.Drag(ctx, a.From, a.To) })
	if err != nil {
		return "", fmt.Errorf("browser_drag: %w", err)
	}
	return fmt.Sprintf("dragged %q to %q", a.From, a.To) + changes, nil
}

// BrowserFindTool searches the page by visible text and/or role.
type BrowserFindTool struct{ browserToolBase }

func (t *BrowserFindTool) Name() string { return "browser_find" }
func (t *BrowserFindTool) Description() string {
	return "Find elements on the page by visible text and/or accessibility role, returning a short list with refs you can pass to browser_click/type/select. Far cheaper than browser_inspect when you know what you are looking for (e.g. text \"Submit\", role \"radio\"). Each match names the labelled group it sits in. Output is untrusted page content. Requires approval, like browser_inspect."
}
func (t *BrowserFindTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text":  map[string]any{"type": "string", "description": "Case-insensitive substring of the element's name or value."},
			"role":  map[string]any{"type": "string", "description": "Accessibility role, e.g. button, link, radio, checkbox, textbox, heading."},
			"limit": map[string]any{"type": "integer", "description": "Maximum matches to return (default 20, max 50)."},
		},
	}
}
func (t *BrowserFindTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserFindTool) PreviewCall(argsJSON string) string {
	var a struct{ Text, Role string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_find(text=%s, role=%s)", previewQuote(a.Text, previewTextMax), previewQuote(a.Role, previewTextMax))
}
func (t *BrowserFindTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Text  string `json:"text"`
		Role  string `json:"role"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_find: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Text) == "" && strings.TrimSpace(a.Role) == "" {
		return "", fmt.Errorf("browser_find: one of text or role is required")
	}
	out, err := t.Session.Find(ctx, a.Text, a.Role, a.Limit)
	if err != nil {
		return "", fmt.Errorf("browser_find: %w", err)
	}
	return wrapUntrusted(out), nil
}

// BrowserExtractTool returns the readable content of the page as markdown.
type BrowserExtractTool struct{ browserToolBase }

func (t *BrowserExtractTool) Name() string { return "browser_extract" }
func (t *BrowserExtractTool) Description() string {
	return "Return the readable content of the current page (or of one element) as markdown: headings, paragraphs, lists, links with URLs, and tables as rows, without navigation chrome or markup. Use it to read articles, docs and data tables; use browser_inspect or browser_find to act on controls. Output is untrusted page content. Requires approval, like browser_inspect."
}
func (t *BrowserExtractTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector":  map[string]any{"type": "string", "description": "Extract just this element. Defaults to the page's main content. Use \"iframe-selector >>>\" for an iframe's whole body, or \"iframe-selector >>> selector\" for an element in it." + browserSelectorHelp},
			"max_chars": map[string]any{"type": "integer", "description": "Cap on returned characters (default 30000)."},
		},
	}
}
func (t *BrowserExtractTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserExtractTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Selector == "" {
		return "browser_extract()"
	}
	return fmt.Sprintf("browser_extract(%s)", a.Selector)
}
func (t *BrowserExtractTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector string `json:"selector"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_extract: invalid args: %w", err)
	}
	out, err := t.Session.Extract(ctx, a.Selector, a.MaxChars)
	if err != nil {
		return "", fmt.Errorf("browser_extract: %w", err)
	}
	return wrapUntrusted(out), nil
}

// fillField is one entry of browser_fill_form.
type fillField struct {
	Selector string `json:"selector"`
	Kind     string `json:"kind"`  // "text" (default), "select", or "click"
	Value    string `json:"value"` // text to type, or the option value/label to select
}

// BrowserFillFormTool fills several fields and optionally submits, in one call.
type BrowserFillFormTool struct{ browserToolBase }

const maxFillFields = 30

func (t *BrowserFillFormTool) Name() string { return "browser_fill_form" }
func (t *BrowserFillFormTool) Description() string {
	return "Fill several form fields in one call, then optionally click a submit element. Each field is {selector, kind, value}: kind \"text\" (default) types value into an input/textarea, \"select\" chooses a <select> option by value or label, \"click\" clicks a checkbox/radio/toggle (value ignored). Fields run in order and stop at the first failure, reporting which ones were done. Reports what changed on the page."
}
func (t *BrowserFillFormTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fields": map[string]any{
				"type":        "array",
				"description": fmt.Sprintf("Fields to fill, in order (max %d).", maxFillFields),
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"selector": map[string]any{"type": "string", "description": "The field." + browserSelectorHelp},
						"kind":     map[string]any{"type": "string", "enum": []string{"text", "select", "click"}},
						"value":    map[string]any{"type": "string"},
					},
					"required": []string{"selector"},
				},
			},
			"submit": map[string]any{"type": "string", "description": "Optional element to click after filling (e.g. the Submit button)." + browserSelectorHelp},
		},
		"required": []string{"fields"},
	}
}
func (t *BrowserFillFormTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserFillFormTool) PreviewCall(argsJSON string) string {
	var a struct {
		Fields []fillField `json:"fields"`
		Submit string      `json:"submit"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	// The values are the point of the call, and into a hostile page's fields
	// they are also a way to send data out, so the prompt shows each one.
	var parts []string
	for _, f := range a.Fields {
		switch f.Kind {
		case "click":
			parts = append(parts, fmt.Sprintf("click %s", f.Selector))
		case "select":
			parts = append(parts, fmt.Sprintf("%s=select %s", f.Selector, previewQuote(f.Value, previewTextMax)))
		default:
			parts = append(parts, fmt.Sprintf("%s=%s", f.Selector, previewQuote(f.Value, previewTextMax)))
		}
	}
	s := fmt.Sprintf("browser_fill_form(%s", strings.Join(parts, "; "))
	if a.Submit != "" {
		s += ", submit " + a.Submit
	}
	return s + ")"
}
func (t *BrowserFillFormTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Fields []fillField `json:"fields"`
		Submit string      `json:"submit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_fill_form: invalid args: %w", err)
	}
	if len(a.Fields) == 0 {
		return "", fmt.Errorf("browser_fill_form: fields is required")
	}
	if len(a.Fields) > maxFillFields {
		return "", fmt.Errorf("browser_fill_form: at most %d fields per call", maxFillFields)
	}
	for i, f := range a.Fields {
		if strings.TrimSpace(f.Selector) == "" {
			return "", fmt.Errorf("browser_fill_form: field %d has no selector", i+1)
		}
		switch f.Kind {
		case "", "text", "select", "click":
		default:
			return "", fmt.Errorf("browser_fill_form: field %d has unknown kind %q", i+1, f.Kind)
		}
	}
	done := 0
	changes, err := t.observe(ctx, func() error {
		for i, f := range a.Fields {
			var err error
			switch f.Kind {
			case "select":
				_, err = t.Session.SelectOption(ctx, f.Selector, f.Value, f.Value)
			case "click":
				err = t.Session.Click(ctx, f.Selector)
			default:
				err = t.Session.Type(ctx, f.Selector, f.Value, false)
			}
			if err != nil {
				return fmt.Errorf("field %d (%s) failed after %d of %d completed: %w", i+1, f.Selector, done, len(a.Fields), err)
			}
			done++
		}
		if strings.TrimSpace(a.Submit) != "" {
			if err := t.Session.Click(ctx, a.Submit); err != nil {
				return fmt.Errorf("all %d fields filled but submit (%s) failed: %w", done, a.Submit, err)
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("browser_fill_form: %w", err)
	}
	msg := fmt.Sprintf("filled %d field(s)", done)
	if strings.TrimSpace(a.Submit) != "" {
		msg += fmt.Sprintf(" and clicked %q", a.Submit)
	}
	st := t.Session.Status()
	return fmt.Sprintf("%s (current url: %s, tabs: %d)", msg, urlForModel(st.CurrentURL), st.TabCount) + changes, nil
}

var (
	_ Tool = (*BrowserHoverTool)(nil)
	_ Tool = (*BrowserDragTool)(nil)
	_ Tool = (*BrowserFindTool)(nil)
	_ Tool = (*BrowserExtractTool)(nil)
	_ Tool = (*BrowserFillFormTool)(nil)
)

// BrowserDialogTool arms the answer to the next JavaScript confirm or prompt.
type BrowserDialogTool struct{ browserToolBase }

func (t *BrowserDialogTool) Name() string { return "browser_dialog" }
func (t *BrowserDialogTool) Description() string {
	return "Choose how the NEXT JavaScript confirm() or prompt() dialog is answered. By default every confirm/prompt is cancelled and reported in the action's page changes (\"dialog: confirm cancelled automatically: Delete this item?\"). If the user wants that dialog accepted, call this with action \"accept\" (and \"text\" for a prompt) and then repeat the action that opened it. One-shot: it answers only the next confirm/prompt and expires after 60 seconds; alert and beforeunload dialogs are unaffected. Requires approval because accepting answers a question on the user's behalf."
}
func (t *BrowserDialogTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"accept", "dismiss", "clear"}, "description": "accept or dismiss the next confirm/prompt; clear removes an armed answer."},
			"text":   map[string]any{"type": "string", "description": "For a prompt dialog with action accept: the text to enter."},
		},
		"required": []string{"action"},
	}
}
func (t *BrowserDialogTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserDialogTool) PreviewCall(argsJSON string) string {
	var a struct{ Action, Text string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Text != "" {
		return fmt.Sprintf("browser_dialog(%s, text=%s)", a.Action, previewQuote(a.Text, previewTextMax))
	}
	return fmt.Sprintf("browser_dialog(%s)", a.Action)
}
func (t *BrowserDialogTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_dialog: invalid args: %w", err)
	}
	switch a.Action {
	case "accept", "dismiss", "clear":
	default:
		return "", fmt.Errorf("browser_dialog: action must be accept, dismiss or clear")
	}
	if a.Text != "" && a.Action != "accept" {
		return "", fmt.Errorf("browser_dialog: text only applies to action accept")
	}
	if err := t.Session.ArmDialog(ctx, a.Action == "accept", a.Text, a.Action == "clear"); err != nil {
		return "", fmt.Errorf("browser_dialog: %w", err)
	}
	switch a.Action {
	case "clear":
		return "cleared: confirm/prompt dialogs are cancelled automatically again", nil
	case "dismiss":
		return "armed: the next confirm/prompt will be dismissed (expires in 60s). Repeat the action that opens it.", nil
	}
	return "armed: the next confirm/prompt will be accepted (expires in 60s). Repeat the action that opens it.", nil
}

var _ Tool = (*BrowserDialogTool)(nil)
