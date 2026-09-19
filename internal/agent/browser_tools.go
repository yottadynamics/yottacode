package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/browser"
)

// browserDisabledMessage is returned by every browser_* tool's Execute when
// the experimental `browser` feature isn't enabled for this session — same
// phrasing convention as DispatchTool/IntegrateTool's disabled message.
const browserDisabledMessage = "error: `browser_*` tools are an experimental feature and are not enabled in this session. Enable it with `--experimental browser`, `YOTTACODE_EXPERIMENTAL=browser`, or `[experimental] browser = true` in config.toml."

// browserSession is the minimal surface the browser_* tools need from a
// live browser manager. *browser.Manager implements it; unit tests inject
// a fake so schema/approval/error-mapping tests never need a real Chrome
// — same fake-client-for-tests convention CoreToolDeps.LSPClientFactory
// uses for LSP tools.
type browserSession interface {
	Status() browser.Status
	Navigate(ctx context.Context, url, waitUntil string) (browser.NavigateResult, error)
	Screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error)
	Inspect(ctx context.Context, selector string) (string, error)
	Click(ctx context.Context, selector string) error
	Type(ctx context.Context, selector, text string, submit bool) error
	Hotkey(ctx context.Context, keys string) error
	Scroll(ctx context.Context, selector string, deltaX, deltaY float64) error
	Wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error
	Close(ctx context.Context) error
	Tabs(ctx context.Context) ([]browser.TabInfo, error)
	SwitchTab(ctx context.Context, index int) error
	CloseTab(ctx context.Context, index int) error
	Upload(ctx context.Context, selector string, paths []string) error
	Download(ctx context.Context, selector, url, destPath string) (browser.DownloadResult, error)
	ConsoleLogs(ctx context.Context, limit int) ([]browser.ConsoleEntry, error)
	NetworkRequests(ctx context.Context, limit int) ([]browser.NetworkEntry, error)
}

// browserToolBase is embedded by every browser_* tool struct. Enabled
// gates the tool behind the `browser` experimental feature (see
// experimental.Browser) — same pattern as DispatchTool.Enabled/
// IntegrateTool.Enabled: the tool is always registered so the model can
// discover it and get an explanatory message, rather than silently
// missing from the schema.
type browserToolBase struct {
	Session browserSession
	Enabled bool
}

// BrowserStatusTool reports local manager state — binary discovery,
// whether a session is active, its current URL, and its profile
// directory. Never launches a browser and never needs approval.
type BrowserStatusTool struct{ browserToolBase }

func (t *BrowserStatusTool) Name() string { return "browser_status" }
func (t *BrowserStatusTool) Description() string {
	return "Report the browser automation manager's state: whether a system Chrome/Chromium binary was found, whether a browser session is active, its current URL, and its isolated profile directory. Never launches a browser."
}
func (t *BrowserStatusTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *BrowserStatusTool) RequiresApproval(string) bool { return false }
func (t *BrowserStatusTool) PreviewCall(string) string    { return "browser_status()" }
func (t *BrowserStatusTool) Execute(context.Context, string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	st := t.Session.Status()
	binary := st.BinaryPath
	if binary == "" {
		binary = "(not found)"
	}
	url := st.CurrentURL
	if url == "" {
		url = "(none)"
	}
	profile := st.ProfileDir
	if profile == "" {
		profile = "(none)"
	}
	return fmt.Sprintf("binary: %s\nactive: %t\nurl: %s\nprofile_dir: %s", binary, st.Active, url, profile), nil
}

// BrowserNavigateTool navigates the single active page to a URL, lazily
// launching the browser on first call.
type BrowserNavigateTool struct{ browserToolBase }

func (t *BrowserNavigateTool) Name() string { return "browser_navigate" }
func (t *BrowserNavigateTool) Description() string {
	return "Navigate the browser's single active page to a URL, launching an isolated headless Chrome/Chromium session on first use. Waits for the given lifecycle event before returning."
}
func (t *BrowserNavigateTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "URL to navigate to"},
			"wait_until": map[string]any{
				"type":        "string",
				"enum":        []string{"load", "domcontentloaded", "networkidle"},
				"description": "Lifecycle event to wait for before returning. Defaults to \"load\".",
			},
		},
		"required": []string{"url"},
	}
}
func (t *BrowserNavigateTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserNavigateTool) PreviewCall(argsJSON string) string {
	var a struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_navigate(%s)", a.URL)
}
func (t *BrowserNavigateTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		URL       string `json:"url"`
		WaitUntil string `json:"wait_until"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_navigate: invalid args: %w", err)
	}
	if strings.TrimSpace(a.URL) == "" {
		return "", fmt.Errorf("browser_navigate: url is required")
	}
	res, err := t.Session.Navigate(ctx, a.URL, a.WaitUntil)
	if err != nil {
		return "", fmt.Errorf("browser_navigate: %w", err)
	}
	return fmt.Sprintf("navigated to %s (title: %q)", res.URL, res.Title), nil
}

// BrowserScreenshotTool captures a PNG of the page (or one element) as an
// image block. Like read_file, it degrades to a text label when the
// active model doesn't accept image input.
type BrowserScreenshotTool struct {
	browserToolBase
	SupportsImages bool
}

func (t *BrowserScreenshotTool) Name() string { return "browser_screenshot" }
func (t *BrowserScreenshotTool) Description() string {
	return "Capture a PNG screenshot of the current page, or of one element if a selector is given. Requires approval: a screenshot can surface on-screen private data even without clicking anything."
}
func (t *BrowserScreenshotTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector":  map[string]any{"type": "string", "description": "CSS selector to screenshot just that element. Omit to screenshot the page."},
			"full_page": map[string]any{"type": "boolean", "description": "Capture the full scrollable page rather than just the viewport. Ignored when selector is set."},
		},
	}
}
func (t *BrowserScreenshotTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserScreenshotTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Selector == "" {
		return "browser_screenshot()"
	}
	return fmt.Sprintf("browser_screenshot(%s)", a.Selector)
}
func (t *BrowserScreenshotTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	res, err := t.ExecuteMultimodal(ctx, argsJSON)
	return res.Content, err
}
func (t *BrowserScreenshotTool) ExecuteMultimodal(ctx context.Context, argsJSON string) (MultimodalResult, error) {
	if !t.Enabled {
		return MultimodalResult{Content: browserDisabledMessage}, nil
	}
	var a struct {
		Selector string `json:"selector"`
		FullPage bool   `json:"full_page"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return MultimodalResult{}, fmt.Errorf("browser_screenshot: invalid args: %w", err)
	}
	data, err := t.Session.Screenshot(ctx, a.Selector, a.FullPage)
	if err != nil {
		return MultimodalResult{}, fmt.Errorf("browser_screenshot: %w", err)
	}
	label := fmt.Sprintf("[screenshot: %d bytes, image/png]", len(data))
	if !t.SupportsImages {
		return MultimodalResult{Content: label + " — current model does not support image input"}, nil
	}
	return MultimodalResult{Content: label, Images: []adapter.ImageBlock{{Data: data, MediaType: "image/png"}}}, nil
}

// BrowserInspectTool returns an accessibility-tree/DOM text snapshot
// (role/name/value) — the token-cheap way to "read" a page without a
// screenshot.
type BrowserInspectTool struct{ browserToolBase }

func (t *BrowserInspectTool) Name() string { return "browser_inspect" }
func (t *BrowserInspectTool) Description() string {
	return "Return an accessibility-tree text snapshot (role, name, value) of the current page, or of one element if a selector is given. Cheaper on tokens than a screenshot. Requires approval: the snapshot can surface on-screen private data even without clicking anything."
}
func (t *BrowserInspectTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector": map[string]any{"type": "string", "description": "CSS selector to scope the snapshot to that element's subtree. Omit for the whole page."},
		},
	}
}
func (t *BrowserInspectTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserInspectTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Selector == "" {
		return "browser_inspect()"
	}
	return fmt.Sprintf("browser_inspect(%s)", a.Selector)
}
func (t *BrowserInspectTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_inspect: invalid args: %w", err)
	}
	out, err := t.Session.Inspect(ctx, a.Selector)
	if err != nil {
		return "", fmt.Errorf("browser_inspect: %w", err)
	}
	return out, nil
}

// BrowserClickTool clicks the first element matching a selector.
type BrowserClickTool struct{ browserToolBase }

func (t *BrowserClickTool) Name() string { return "browser_click" }
func (t *BrowserClickTool) Description() string {
	return "Click the element matching a CSS selector, waiting for it to become interactable first."
}
func (t *BrowserClickTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector": map[string]any{"type": "string", "description": "CSS selector of the element to click"},
		},
		"required": []string{"selector"},
	}
}
func (t *BrowserClickTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserClickTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_click(%s)", a.Selector)
}
func (t *BrowserClickTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_click: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Selector) == "" {
		return "", fmt.Errorf("browser_click: selector is required")
	}
	if err := t.Session.Click(ctx, a.Selector); err != nil {
		return "", fmt.Errorf("browser_click: %w", err)
	}
	// tabs is reported so a click that opened a new tab (target="_blank",
	// window.open) is visible without a separate browser_tabs call — the
	// session auto-follows a newly opened tab, so current url already
	// reflects it too; see browser.Manager/session's newTabDetectWindow.
	st := t.Session.Status()
	return fmt.Sprintf("clicked %q (current url: %s, tabs: %d)", a.Selector, st.CurrentURL, st.TabCount), nil
}

// BrowserTypeTool fills an input/textarea/contenteditable element,
// optionally submitting with Enter afterward.
type BrowserTypeTool struct{ browserToolBase }

func (t *BrowserTypeTool) Name() string { return "browser_type" }
func (t *BrowserTypeTool) Description() string {
	return "Clear and type text into the element matching a CSS selector, waiting for it to become interactable first. Optionally submit with Enter afterward."
}
func (t *BrowserTypeTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector": map[string]any{"type": "string", "description": "CSS selector of the input/textarea/contenteditable element"},
			"text":     map[string]any{"type": "string", "description": "Text to type"},
			"submit":   map[string]any{"type": "boolean", "description": "Press Enter after typing"},
		},
		"required": []string{"selector", "text"},
	}
}
func (t *BrowserTypeTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserTypeTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_type(%s)", a.Selector)
}
func (t *BrowserTypeTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector string `json:"selector"`
		Text     string `json:"text"`
		Submit   bool   `json:"submit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_type: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Selector) == "" {
		return "", fmt.Errorf("browser_type: selector is required")
	}
	if err := t.Session.Type(ctx, a.Selector, a.Text, a.Submit); err != nil {
		return "", fmt.Errorf("browser_type: %w", err)
	}
	// See BrowserClickTool.Execute: a submit that opens a new tab (a form
	// with target="_blank") is auto-followed and visible via tabs/current url.
	st := t.Session.Status()
	return fmt.Sprintf("typed into %q (current url: %s, tabs: %d)", a.Selector, st.CurrentURL, st.TabCount), nil
}

// BrowserHotkeyTool sends a key or key combo to the page (e.g. "Enter",
// "Control+a").
type BrowserHotkeyTool struct{ browserToolBase }

func (t *BrowserHotkeyTool) Name() string { return "browser_hotkey" }
func (t *BrowserHotkeyTool) Description() string {
	return `Send a key or "+"-joined key combo to the page, e.g. "Enter" or "Control+a". The last token is pressed and released; earlier tokens are held as modifiers.`
}
func (t *BrowserHotkeyTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"keys": map[string]any{"type": "string", "description": `Key or combo, e.g. "Enter", "Escape", "Control+a"`},
		},
		"required": []string{"keys"},
	}
}
func (t *BrowserHotkeyTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserHotkeyTool) PreviewCall(argsJSON string) string {
	var a struct {
		Keys string `json:"keys"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_hotkey(%s)", a.Keys)
}
func (t *BrowserHotkeyTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Keys string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_hotkey: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Keys) == "" {
		return "", fmt.Errorf("browser_hotkey: keys is required")
	}
	if err := t.Session.Hotkey(ctx, a.Keys); err != nil {
		return "", fmt.Errorf("browser_hotkey: %w", err)
	}
	st := t.Session.Status()
	return fmt.Sprintf("sent %q (current url: %s)", a.Keys, st.CurrentURL), nil
}

// browserScrollDefaultDelta is the pixel magnitude applied when direction
// is given without an explicit delta_x/delta_y.
const browserScrollDefaultDelta = 400.0

// resolveScrollDelta turns a human direction enum into a signed
// (deltaX, deltaY) pair, unless an explicit non-zero delta was already
// given (which always wins).
func resolveScrollDelta(direction string, deltaX, deltaY float64) (float64, float64, error) {
	if deltaX != 0 || deltaY != 0 {
		return deltaX, deltaY, nil
	}
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "", "down":
		return 0, browserScrollDefaultDelta, nil
	case "up":
		return 0, -browserScrollDefaultDelta, nil
	case "left":
		return -browserScrollDefaultDelta, 0, nil
	case "right":
		return browserScrollDefaultDelta, 0, nil
	default:
		return 0, 0, fmt.Errorf("unrecognized direction %q (use up/down/left/right, or delta_x/delta_y)", direction)
	}
}

// BrowserScrollTool scrolls an element into view, or scrolls the page by a
// direction or explicit pixel delta.
type BrowserScrollTool struct{ browserToolBase }

func (t *BrowserScrollTool) Name() string { return "browser_scroll" }
func (t *BrowserScrollTool) Description() string {
	return "Scroll an element into view (if selector is given), or scroll the page by a direction or explicit pixel delta."
}
func (t *BrowserScrollTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector":  map[string]any{"type": "string", "description": "CSS selector to scroll into view. When set, direction/delta_x/delta_y are ignored."},
			"direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "Scroll direction, applied at a default magnitude. Ignored if delta_x/delta_y is non-zero."},
			"delta_x":   map[string]any{"type": "number", "description": "Explicit horizontal scroll delta in pixels. Overrides direction."},
			"delta_y":   map[string]any{"type": "number", "description": "Explicit vertical scroll delta in pixels. Overrides direction."},
		},
	}
}
func (t *BrowserScrollTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserScrollTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector  string `json:"selector"`
		Direction string `json:"direction"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Selector != "" {
		return fmt.Sprintf("browser_scroll(%s)", a.Selector)
	}
	return fmt.Sprintf("browser_scroll(%s)", a.Direction)
}
func (t *BrowserScrollTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector  string  `json:"selector"`
		Direction string  `json:"direction"`
		DeltaX    float64 `json:"delta_x"`
		DeltaY    float64 `json:"delta_y"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_scroll: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Selector) != "" {
		if err := t.Session.Scroll(ctx, a.Selector, 0, 0); err != nil {
			return "", fmt.Errorf("browser_scroll: %w", err)
		}
		return fmt.Sprintf("scrolled %q into view", a.Selector), nil
	}
	dx, dy, err := resolveScrollDelta(a.Direction, a.DeltaX, a.DeltaY)
	if err != nil {
		return "", fmt.Errorf("browser_scroll: %w", err)
	}
	if err := t.Session.Scroll(ctx, "", dx, dy); err != nil {
		return "", fmt.Errorf("browser_scroll: %w", err)
	}
	return fmt.Sprintf("scrolled by (%.0f, %.0f)", dx, dy), nil
}

// BrowserWaitTool blocks until a selector/text becomes visible or the
// network goes idle. Pure wait: it never launches the browser and never
// mutates state (see browser.Manager.Wait), so it needs no approval.
type BrowserWaitTool struct{ browserToolBase }

func (t *BrowserWaitTool) Name() string { return "browser_wait" }
func (t *BrowserWaitTool) Description() string {
	return "Wait for a CSS selector to become visible, for text to appear on the page, and/or for the network to go idle. No-op if the browser was never launched — there is nothing to wait for yet."
}
func (t *BrowserWaitTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector":     map[string]any{"type": "string", "description": "Wait for this CSS selector to become visible"},
			"text":         map[string]any{"type": "string", "description": "Wait for this text to appear anywhere on the page"},
			"network_idle": map[string]any{"type": "boolean", "description": "Wait for the network to go idle"},
			"timeout_ms":   map[string]any{"type": "integer", "description": "Deadline in milliseconds. Defaults to a built-in timeout."},
		},
	}
}
func (t *BrowserWaitTool) RequiresApproval(string) bool { return false }
func (t *BrowserWaitTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
		Text     string `json:"text"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_wait(%s%s)", a.Selector, a.Text)
}
func (t *BrowserWaitTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector    string `json:"selector"`
		Text        string `json:"text"`
		NetworkIdle bool   `json:"network_idle"`
		TimeoutMS   int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_wait: invalid args: %w", err)
	}
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond
	if err := t.Session.Wait(ctx, a.Selector, a.Text, a.NetworkIdle, timeout); err != nil {
		return "", fmt.Errorf("browser_wait: %w", err)
	}
	return "wait condition satisfied", nil
}

// BrowserCloseTool tears down the browser process and its isolated temp
// profile. Also runs automatically via CleanupTool.Cleanup at session
// exit, so an agent that forgets to call it explicitly still doesn't
// leak a browser process.
type BrowserCloseTool struct{ browserToolBase }

func (t *BrowserCloseTool) Name() string { return "browser_close" }
func (t *BrowserCloseTool) Description() string {
	return "Close the browser session and remove its isolated temp profile. Only reduces capability — safe to call any time, and safe to call more than once."
}
func (t *BrowserCloseTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *BrowserCloseTool) RequiresApproval(string) bool { return false }
func (t *BrowserCloseTool) PreviewCall(string) string    { return "browser_close()" }
func (t *BrowserCloseTool) Execute(ctx context.Context, _ string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	if err := t.Session.Close(ctx); err != nil {
		return "", fmt.Errorf("browser_close: %w", err)
	}
	return "browser session closed", nil
}

// Cleanup satisfies CleanupTool so CleanupRegistryTools tears the browser
// down at session shutdown even if the model never calls browser_close.
// Session.Close is idempotent, so this is safe whether or not the browser
// was ever launched or already closed explicitly.
func (t *BrowserCloseTool) Cleanup(ctx context.Context) error {
	return t.Session.Close(ctx)
}

// BrowserTabsTool lists every tracked page (main page plus any popup or
// target="_blank" tab opened since launch). Read-only, never launches —
// an unlaunched session has no tabs to report.
type BrowserTabsTool struct{ browserToolBase }

func (t *BrowserTabsTool) Name() string { return "browser_tabs" }
func (t *BrowserTabsTool) Description() string {
	return `List every tracked browser tab (main page plus any popup or target="_blank" tab opened since launch), with each one's index, title, URL, and whether it's the active tab. Never launches the browser.`
}
func (t *BrowserTabsTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *BrowserTabsTool) RequiresApproval(string) bool { return false }
func (t *BrowserTabsTool) PreviewCall(string) string    { return "browser_tabs()" }
func (t *BrowserTabsTool) Execute(ctx context.Context, _ string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	tabs, err := t.Session.Tabs(ctx)
	if err != nil {
		return "", fmt.Errorf("browser_tabs: %w", err)
	}
	if len(tabs) == 0 {
		return "no tabs (browser not launched yet)", nil
	}
	var b strings.Builder
	for _, tb := range tabs {
		marker := " "
		if tb.Active {
			marker = "*"
		}
		fmt.Fprintf(&b, "%s [%d] %s — %s\n", marker, tb.Index, tb.Title, tb.URL)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// BrowserSwitchTabTool changes which tracked tab subsequent browser_*
// actions act on. Never launches, and doesn't itself read or change any
// page content — it only changes which page later calls target — so it
// needs no approval, same reasoning as browser_wait.
type BrowserSwitchTabTool struct{ browserToolBase }

func (t *BrowserSwitchTabTool) Name() string { return "browser_switch_tab" }
func (t *BrowserSwitchTabTool) Description() string {
	return "Switch which tracked tab subsequent browser_* actions act on, by the index reported by browser_tabs."
}
func (t *BrowserSwitchTabTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"index": map[string]any{"type": "integer", "description": "Tab index, as reported by browser_tabs"},
		},
		"required": []string{"index"},
	}
}
func (t *BrowserSwitchTabTool) RequiresApproval(string) bool { return false }
func (t *BrowserSwitchTabTool) PreviewCall(argsJSON string) string {
	var a struct {
		Index int `json:"index"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_switch_tab(%d)", a.Index)
}
func (t *BrowserSwitchTabTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_switch_tab: invalid args: %w", err)
	}
	if err := t.Session.SwitchTab(ctx, a.Index); err != nil {
		return "", fmt.Errorf("browser_switch_tab: %w", err)
	}
	st := t.Session.Status()
	return fmt.Sprintf("switched to tab %d (current url: %s)", a.Index, st.CurrentURL), nil
}

// BrowserCloseTabTool closes one tracked tab without tearing down the
// whole session — the narrower counterpart to browser_close. No
// approval, same reasoning as browser_close: it only ever reduces
// capability (an isolated, disposable headless tab going away, nothing
// outside the browser session is touched) and never surfaces anything
// an already-approved call hasn't already shown.
type BrowserCloseTabTool struct{ browserToolBase }

func (t *BrowserCloseTabTool) Name() string { return "browser_close_tab" }
func (t *BrowserCloseTabTool) Description() string {
	return "Close one tracked tab by the index browser_tabs reports, without closing the browser session. Refuses to close the only remaining tab — use browser_close for that."
}
func (t *BrowserCloseTabTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"index": map[string]any{"type": "integer", "description": "Tab index, as reported by browser_tabs"},
		},
		"required": []string{"index"},
	}
}
func (t *BrowserCloseTabTool) RequiresApproval(string) bool { return false }
func (t *BrowserCloseTabTool) PreviewCall(argsJSON string) string {
	var a struct {
		Index int `json:"index"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_close_tab(%d)", a.Index)
}
func (t *BrowserCloseTabTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_close_tab: invalid args: %w", err)
	}
	if err := t.Session.CloseTab(ctx, a.Index); err != nil {
		return "", fmt.Errorf("browser_close_tab: %w", err)
	}
	st := t.Session.Status()
	return fmt.Sprintf("closed tab %d (tabs remaining: %d, current url: %s)", a.Index, st.TabCount, st.CurrentURL), nil
}

// BrowserUploadTool sets a file input element's files from local paths.
// Always requires approval and reuses the write-path trust boundary
// (ValidateWritePath) even though it's technically a read: uploading a
// local file hands its bytes to whatever origin the active page is on,
// which is at least as sensitive as writing it — the same cwd/
// --allow-paths boundary write_file enforces applies here too.
type BrowserUploadTool struct {
	browserToolBase
	Cwd       *CwdRef
	WriteOpts WritePathOptions
}

func (t *BrowserUploadTool) Name() string { return "browser_upload" }
func (t *BrowserUploadTool) Description() string {
	return "Set a <input type=file> element's files from local paths. Every path must be inside the session workspace (same boundary as write_file) — uploading a file hands its bytes to whatever origin the active page is on."
}
func (t *BrowserUploadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector": map[string]any{"type": "string", "description": "CSS selector of the file input element"},
			"paths":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Local file paths to upload"},
		},
		"required": []string{"selector", "paths"},
	}
}
func (t *BrowserUploadTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserUploadTool) PreviewCall(argsJSON string) string {
	var a struct {
		Selector string `json:"selector"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_upload(%s)", a.Selector)
}
func (t *BrowserUploadTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector string   `json:"selector"`
		Paths    []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_upload: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Selector) == "" {
		return "", fmt.Errorf("browser_upload: selector is required")
	}
	if len(a.Paths) == 0 {
		return "", fmt.Errorf("browser_upload: paths is required")
	}
	cwd := t.Cwd.Get()
	resolved := make([]string, len(a.Paths))
	for i, p := range a.Paths {
		rp := resolvePath(cwd, p)
		if err := ValidateWritePath(rp, t.WriteOpts); err != nil {
			return "", fmt.Errorf("browser_upload: %w", err)
		}
		resolved[i] = rp
	}
	if err := t.Session.Upload(ctx, a.Selector, resolved); err != nil {
		return "", fmt.Errorf("browser_upload: %w", err)
	}
	return fmt.Sprintf("uploaded %d file(s) to %q", len(resolved), a.Selector), nil
}

// BrowserDownloadTool triggers a download — by clicking selector, or by
// navigating directly to url (exactly one must be given) — and saves it
// to path, validated the same way write_file validates its destination.
type BrowserDownloadTool struct {
	browserToolBase
	Cwd       *CwdRef
	WriteOpts WritePathOptions
}

func (t *BrowserDownloadTool) Name() string { return "browser_download" }
func (t *BrowserDownloadTool) Description() string {
	return "Trigger a download — by clicking an element (selector), or by navigating directly to a downloadable url — and save it to a local path. Exactly one of selector/url is required. path must be inside the session workspace, same boundary as write_file."
}
func (t *BrowserDownloadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector": map[string]any{"type": "string", "description": "CSS selector of an element (e.g. a download link/button) to click to trigger the download"},
			"url":      map[string]any{"type": "string", "description": "URL to navigate to directly to trigger the download"},
			"path":     map[string]any{"type": "string", "description": "Local destination path for the downloaded file"},
		},
		"required": []string{"path"},
	}
}
func (t *BrowserDownloadTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserDownloadTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("browser_download(%s)", a.Path)
}
func (t *BrowserDownloadTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Selector string `json:"selector"`
		URL      string `json:"url"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_download: invalid args: %w", err)
	}
	hasSelector := strings.TrimSpace(a.Selector) != ""
	hasURL := strings.TrimSpace(a.URL) != ""
	if hasSelector == hasURL {
		return "", fmt.Errorf("browser_download: exactly one of selector or url is required")
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("browser_download: path is required")
	}
	dest := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateWritePath(dest, t.WriteOpts); err != nil {
		return "", fmt.Errorf("browser_download: %w", err)
	}
	res, err := t.Session.Download(ctx, a.Selector, a.URL, dest)
	if err != nil {
		return "", fmt.Errorf("browser_download: %w", err)
	}
	return fmt.Sprintf("downloaded %q to %s (%d bytes)", res.SuggestedFilename, res.Path, res.SizeBytes), nil
}

// browserLogLimitDefault caps how many entries browser_console_logs and
// browser_network_requests return when the caller omits limit — the
// underlying buffer (see browser.maxBufferedEntries) holds more, but a
// smaller default keeps a routine call token-cheap.
const browserLogLimitDefault = 50

// BrowserConsoleLogsTool returns recently buffered console.* calls and
// uncaught exceptions from the active page — captured continuously
// since the page was tracked, not just during this call, since a
// console message can happen at any time (page load, async JS) not only
// while a tool call is in flight. Always requires approval: console
// output can carry private data (a logged token, PII) even though
// nothing was clicked to produce it — same rationale as
// browser_screenshot/browser_inspect.
type BrowserConsoleLogsTool struct{ browserToolBase }

func (t *BrowserConsoleLogsTool) Name() string { return "browser_console_logs" }
func (t *BrowserConsoleLogsTool) Description() string {
	return "Return recently buffered console.* calls and uncaught JS exceptions from the active page, most recent last. Captured continuously since the page was tracked, not just during this call."
}
func (t *BrowserConsoleLogsTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer", "description": "Max entries to return, most recent first. Defaults to 50."},
		},
	}
}
func (t *BrowserConsoleLogsTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserConsoleLogsTool) PreviewCall(string) string    { return "browser_console_logs()" }
func (t *BrowserConsoleLogsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_console_logs: invalid args: %w", err)
	}
	if a.Limit <= 0 {
		a.Limit = browserLogLimitDefault
	}
	entries, err := t.Session.ConsoleLogs(ctx, a.Limit)
	if err != nil {
		return "", fmt.Errorf("browser_console_logs: %w", err)
	}
	if len(entries) == 0 {
		return "no console output buffered", nil
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "[%s] %s %s\n", e.Level, e.At.Format("15:04:05.000"), e.Text)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// BrowserNetworkRequestsTool returns recently buffered requests the
// active page has made — metadata only (method/url/status), never
// response bodies. Always requires approval: request URLs can carry
// private data (a session token in a query param) and reveal what
// backend APIs a page talks to, even though nothing was clicked to
// produce them — same rationale as browser_console_logs.
type BrowserNetworkRequestsTool struct{ browserToolBase }

func (t *BrowserNetworkRequestsTool) Name() string { return "browser_network_requests" }
func (t *BrowserNetworkRequestsTool) Description() string {
	return "Return recently buffered network requests the active page has made — method, URL, status, and failures — most recent last. Metadata only, never response bodies."
}
func (t *BrowserNetworkRequestsTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer", "description": "Max entries to return, most recent first. Defaults to 50."},
		},
	}
}
func (t *BrowserNetworkRequestsTool) RequiresApproval(string) bool { return t.Enabled }
func (t *BrowserNetworkRequestsTool) PreviewCall(string) string    { return "browser_network_requests()" }
func (t *BrowserNetworkRequestsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return browserDisabledMessage, nil
	}
	var a struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("browser_network_requests: invalid args: %w", err)
	}
	if a.Limit <= 0 {
		a.Limit = browserLogLimitDefault
	}
	entries, err := t.Session.NetworkRequests(ctx, a.Limit)
	if err != nil {
		return "", fmt.Errorf("browser_network_requests: %w", err)
	}
	if len(entries) == 0 {
		return "no requests buffered", nil
	}
	var b strings.Builder
	for _, e := range entries {
		status := "PENDING"
		switch {
		case e.Failed:
			status = "FAILED: " + e.ErrorText
		case e.Status != 0:
			status = fmt.Sprintf("%d %s", e.Status, e.StatusText)
		}
		fmt.Fprintf(&b, "%s %s %s (%s)\n", e.Method, status, e.URL, e.MIMEType)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

var _ MultimodalTool = (*BrowserScreenshotTool)(nil)
var _ CleanupTool = (*BrowserCloseTool)(nil)
