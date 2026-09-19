package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/browser"
)

// fakeBrowserSession implements browserSession without a real Chrome
// process, recording calls and returning whatever the test configured —
// same fake-client-for-tests convention CoreToolDeps.LSPClientFactory
// uses for LSP tools.
type fakeBrowserSession struct {
	status browser.Status

	navigateResult browser.NavigateResult
	navigateErr    error
	screenshotData []byte
	screenshotErr  error
	inspectText    string
	inspectErr     error
	clickErr       error
	typeErr        error
	hotkeyErr      error
	scrollErr      error
	waitErr        error
	closeErr       error
	handoffResult  browser.HandoffResult
	handoffErr     error

	tabsResult     []browser.TabInfo
	tabsErr        error
	switchTabErr   error
	closeTabErr    error
	uploadErr      error
	downloadResult browser.DownloadResult
	downloadErr    error

	consoleLogsResult     []browser.ConsoleEntry
	consoleLogsErr        error
	networkRequestsResult []browser.NetworkEntry
	networkRequestsErr    error

	annotatedShot browser.AnnotatedShot
	annotatedErr  error
	evalResult    string
	evalErr       error
	backResult    browser.NavigateResult
	backErr       error
	dialogAccept  bool
	dialogPrompt  string
	setDialogErr  error

	calls []string
}

func (f *fakeBrowserSession) Status() browser.Status {
	f.calls = append(f.calls, "status")
	return f.status
}
func (f *fakeBrowserSession) Navigate(ctx context.Context, url, waitUntil string) (browser.NavigateResult, error) {
	f.calls = append(f.calls, "navigate:"+url)
	return f.navigateResult, f.navigateErr
}
func (f *fakeBrowserSession) Screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error) {
	f.calls = append(f.calls, "screenshot:"+selector)
	return f.screenshotData, f.screenshotErr
}
func (f *fakeBrowserSession) Inspect(ctx context.Context, selector string, opts browser.InspectOptions) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("inspect:%s:interactive=%t", selector, opts.InteractiveOnly))
	return f.inspectText, f.inspectErr
}
func (f *fakeBrowserSession) Click(ctx context.Context, selector string) error {
	f.calls = append(f.calls, "click:"+selector)
	return f.clickErr
}
func (f *fakeBrowserSession) Type(ctx context.Context, selector, text string, submit bool) error {
	f.calls = append(f.calls, fmt.Sprintf("type:%s:%s:%t", selector, text, submit))
	return f.typeErr
}
func (f *fakeBrowserSession) Hotkey(ctx context.Context, keys string) error {
	f.calls = append(f.calls, "hotkey:"+keys)
	return f.hotkeyErr
}
func (f *fakeBrowserSession) Scroll(ctx context.Context, selector string, deltaX, deltaY float64) error {
	f.calls = append(f.calls, fmt.Sprintf("scroll:%s:%.0f:%.0f", selector, deltaX, deltaY))
	return f.scrollErr
}
func (f *fakeBrowserSession) Wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error {
	f.calls = append(f.calls, fmt.Sprintf("wait:%s:%s:%t:%s", selector, text, networkIdle, timeout))
	return f.waitErr
}
func (f *fakeBrowserSession) Close(ctx context.Context) error {
	f.calls = append(f.calls, "close")
	return f.closeErr
}
func (f *fakeBrowserSession) Tabs(ctx context.Context) ([]browser.TabInfo, error) {
	f.calls = append(f.calls, "tabs")
	return f.tabsResult, f.tabsErr
}

func (f *fakeBrowserSession) Handoff(ctx context.Context) (browser.HandoffResult, error) {
	f.calls = append(f.calls, "handoff")
	return f.handoffResult, f.handoffErr
}
func (f *fakeBrowserSession) SwitchTab(ctx context.Context, index int) error {
	f.calls = append(f.calls, fmt.Sprintf("switch_tab:%d", index))
	return f.switchTabErr
}
func (f *fakeBrowserSession) CloseTab(ctx context.Context, index int) error {
	f.calls = append(f.calls, fmt.Sprintf("close_tab:%d", index))
	return f.closeTabErr
}
func (f *fakeBrowserSession) Upload(ctx context.Context, selector string, paths []string) error {
	f.calls = append(f.calls, fmt.Sprintf("upload:%s:%v", selector, paths))
	return f.uploadErr
}
func (f *fakeBrowserSession) Download(ctx context.Context, selector, url, destPath string) (browser.DownloadResult, error) {
	f.calls = append(f.calls, fmt.Sprintf("download:%s:%s:%s", selector, url, destPath))
	return f.downloadResult, f.downloadErr
}
func (f *fakeBrowserSession) ConsoleLogs(ctx context.Context, limit int) ([]browser.ConsoleEntry, error) {
	f.calls = append(f.calls, fmt.Sprintf("console_logs:%d", limit))
	return f.consoleLogsResult, f.consoleLogsErr
}
func (f *fakeBrowserSession) NetworkRequests(ctx context.Context, limit int) ([]browser.NetworkEntry, error) {
	f.calls = append(f.calls, fmt.Sprintf("network_requests:%d", limit))
	return f.networkRequestsResult, f.networkRequestsErr
}

func (f *fakeBrowserSession) ScreenshotAnnotated(ctx context.Context, fullPage bool) (browser.AnnotatedShot, error) {
	f.calls = append(f.calls, fmt.Sprintf("screenshot_annotated:full=%t", fullPage))
	return f.annotatedShot, f.annotatedErr
}
func (f *fakeBrowserSession) Eval(ctx context.Context, expression string) (string, error) {
	f.calls = append(f.calls, "eval:"+expression)
	return f.evalResult, f.evalErr
}
func (f *fakeBrowserSession) Back(ctx context.Context) (browser.NavigateResult, error) {
	f.calls = append(f.calls, "back")
	return f.backResult, f.backErr
}
func (f *fakeBrowserSession) SetDialogPolicy(accept bool, promptText string) error {
	f.calls = append(f.calls, fmt.Sprintf("dialog:%t:%s", accept, promptText))
	if f.setDialogErr == nil {
		f.dialogAccept, f.dialogPrompt = accept, promptText
	}
	return f.setDialogErr
}
func (f *fakeBrowserSession) DialogPolicy() (bool, string) { return f.dialogAccept, f.dialogPrompt }

var _ browserSession = (*fakeBrowserSession)(nil)

// browserToolCases enumerates every browser_* tool against a shared
// fake session, for the approval-gating and disabled-message tables.
func browserToolCases(fake *fakeBrowserSession) []struct {
	name   string
	tool   Tool
	args   string
	wantOK bool // RequiresApproval(...) when Enabled
} {
	base := browserToolBase{Session: fake, Enabled: true}
	return []struct {
		name   string
		tool   Tool
		args   string
		wantOK bool
	}{
		{"browser_status", &BrowserStatusTool{browserToolBase: base}, `{}`, false},
		{"browser_navigate", &BrowserNavigateTool{browserToolBase: base}, `{"url":"https://example.com"}`, true},
		{"browser_screenshot", &BrowserScreenshotTool{browserToolBase: base}, `{}`, true},
		{"browser_inspect", &BrowserInspectTool{browserToolBase: base}, `{}`, true},
		{"browser_click", &BrowserClickTool{browserToolBase: base}, `{"selector":"#go"}`, true},
		{"browser_type", &BrowserTypeTool{browserToolBase: base}, `{"selector":"#q","text":"hi"}`, true},
		{"browser_hotkey", &BrowserHotkeyTool{browserToolBase: base}, `{"keys":"Enter"}`, true},
		{"browser_scroll", &BrowserScrollTool{browserToolBase: base}, `{"direction":"down"}`, true},
		{"browser_wait", &BrowserWaitTool{browserToolBase: base}, `{}`, false},
		{"browser_handoff", &BrowserHandoffTool{browserToolBase: base}, `{}`, true},
		{"browser_close", &BrowserCloseTool{browserToolBase: base}, `{}`, false},
		{"browser_tabs", &BrowserTabsTool{browserToolBase: base}, `{}`, false},
		{"browser_switch_tab", &BrowserSwitchTabTool{browserToolBase: base}, `{"index":0}`, false},
		{"browser_close_tab", &BrowserCloseTabTool{browserToolBase: base}, `{"index":0}`, false},
		{"browser_upload", &BrowserUploadTool{browserToolBase: base}, `{"selector":"#f","paths":["a.txt"]}`, true},
		{"browser_download", &BrowserDownloadTool{browserToolBase: base}, `{"selector":"#dl","path":"out.bin"}`, true},
		{"browser_console_logs", &BrowserConsoleLogsTool{browserToolBase: base}, `{}`, true},
		{"browser_network_requests", &BrowserNetworkRequestsTool{browserToolBase: base}, `{}`, true},
	}
}

func TestBrowserTools_ApprovalGatingTable(t *testing.T) {
	fake := &fakeBrowserSession{}
	for _, tc := range browserToolCases(fake) {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tool.RequiresApproval(tc.args); got != tc.wantOK {
				t.Errorf("%s.RequiresApproval = %t, want %t", tc.name, got, tc.wantOK)
			}
			if got := tc.tool.Name(); got != tc.name {
				t.Errorf("Name() = %q, want %q", got, tc.name)
			}
		})
	}
}

// TestBrowserTools_DisabledMessage exercises every tool with Enabled=false
// and checks Execute short-circuits to the explanatory message without
// touching the session — same posture as DispatchTool/IntegrateTool.
func TestBrowserTools_DisabledMessage(t *testing.T) {
	fake := &fakeBrowserSession{}
	disabledCases := []struct {
		name string
		tool Tool
		args string
	}{
		{"browser_status", &BrowserStatusTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_navigate", &BrowserNavigateTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"url":"https://example.com"}`},
		{"browser_screenshot", &BrowserScreenshotTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_inspect", &BrowserInspectTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_click", &BrowserClickTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"selector":"#go"}`},
		{"browser_type", &BrowserTypeTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"selector":"#q","text":"hi"}`},
		{"browser_hotkey", &BrowserHotkeyTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"keys":"Enter"}`},
		{"browser_scroll", &BrowserScrollTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"direction":"down"}`},
		{"browser_wait", &BrowserWaitTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_handoff", &BrowserHandoffTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_close", &BrowserCloseTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_tabs", &BrowserTabsTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_switch_tab", &BrowserSwitchTabTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"index":0}`},
		{"browser_close_tab", &BrowserCloseTabTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"index":0}`},
		{"browser_upload", &BrowserUploadTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"selector":"#f","paths":["a.txt"]}`},
		{"browser_download", &BrowserDownloadTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{"selector":"#dl","path":"out.bin"}`},
		{"browser_console_logs", &BrowserConsoleLogsTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
		{"browser_network_requests", &BrowserNetworkRequestsTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
	}
	for _, tc := range disabledCases {
		t.Run(tc.name, func(t *testing.T) {
			fake.calls = nil
			out, err := tc.tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if out != browserDisabledMessage {
				t.Errorf("Execute output = %q, want disabled message", out)
			}
			if len(fake.calls) != 0 {
				t.Errorf("disabled tool touched the session: %v", fake.calls)
			}
		})
	}
}

func TestRegisterCoreCwdTools_BrowserGate(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())

	reg := NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	for _, name := range []string{"browser_status", "browser_navigate", "browser_close"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("%s should not register when EnableBrowser is false", name)
		}
	}

	reg2 := NewRegistry()
	RegisterCoreCwdTools(reg2, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}, EnableBrowser: true, BrowserSession: &fakeBrowserSession{}})
	names := []string{
		"browser_status", "browser_navigate", "browser_screenshot", "browser_inspect",
		"browser_click", "browser_type", "browser_hotkey", "browser_scroll",

		"browser_wait", "browser_handoff", "browser_close", "browser_tabs", "browser_switch_tab",
		"browser_close_tab", "browser_upload", "browser_download",
		"browser_console_logs", "browser_network_requests",
	}
	if len(names) != 18 {
		t.Fatalf("test bug: expected 18 tool names, got %d", len(names))
	}
	for _, name := range names {
		if _, ok := reg2.Get(name); !ok {
			t.Errorf("%s should register when EnableBrowser is true", name)
		}
	}
}

func TestBrowserNavigateTool_RequiresURL(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserNavigateTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("expected error for missing url")
	}
}

func TestBrowserNavigateTool_Success(t *testing.T) {
	fake := &fakeBrowserSession{navigateResult: browser.NavigateResult{URL: "https://example.com/", Title: "Example"}}
	tool := &BrowserNavigateTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{"url":"https://example.com","wait_until":"load"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "https://example.com/") || !strings.Contains(out, "Example") {
		t.Errorf("unexpected output: %q", out)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "navigate:https://example.com" {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

// TestBrowserNavigateTool_ErrorTaxonomy checks that a browser package
// sentinel error survives Execute's wrapping so errors.Is still matches —
// the whole point of the stable error taxonomy in internal/browser/errors.go.
func TestBrowserNavigateTool_ErrorTaxonomy(t *testing.T) {
	fake := &fakeBrowserSession{navigateErr: fmt.Errorf("%w: dial tcp refused", browser.ErrLaunchFailed)}
	tool := &BrowserNavigateTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	_, err := tool.Execute(context.Background(), `{"url":"https://example.com"}`)
	if !errors.Is(err, browser.ErrLaunchFailed) {
		t.Errorf("expected errors.Is(err, ErrLaunchFailed), got %v", err)
	}
}

func TestBrowserClickTool_RequiresSelector(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserClickTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("expected error for missing selector")
	}
}

func TestBrowserClickTool_SelectorNotFoundTaxonomy(t *testing.T) {
	fake := &fakeBrowserSession{clickErr: fmt.Errorf("%w: css=#missing", browser.ErrSelectorNotFound)}
	tool := &BrowserClickTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	_, err := tool.Execute(context.Background(), `{"selector":"#missing"}`)
	if !errors.Is(err, browser.ErrSelectorNotFound) {
		t.Errorf("expected errors.Is(err, ErrSelectorNotFound), got %v", err)
	}
}

func TestBrowserTypeTool_RequiresSelector(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserTypeTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	if _, err := tool.Execute(context.Background(), `{"text":"hi"}`); err == nil {
		t.Fatal("expected error for missing selector")
	}
}

func TestBrowserTypeTool_Submit(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserTypeTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	if _, err := tool.Execute(context.Background(), `{"selector":"#q","text":"hi","submit":true}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "type:#q:hi:true"
	if len(fake.calls) != 2 || fake.calls[0] != want { // calls[1] is the trailing Status() for current-url reporting
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserHotkeyTool_RequiresKeys(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserHotkeyTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("expected error for missing keys")
	}
}

func TestResolveScrollDelta(t *testing.T) {
	cases := []struct {
		direction      string
		dx, dy         float64
		wantX, wantY   float64
		wantErrPresent bool
	}{
		{direction: "down", wantX: 0, wantY: browserScrollDefaultDelta},
		{direction: "up", wantX: 0, wantY: -browserScrollDefaultDelta},
		{direction: "left", wantX: -browserScrollDefaultDelta, wantY: 0},
		{direction: "right", wantX: browserScrollDefaultDelta, wantY: 0},
		{direction: "", wantX: 0, wantY: browserScrollDefaultDelta},
		{dx: 12, dy: 34, wantX: 12, wantY: 34},
		{direction: "sideways", wantErrPresent: true},
	}
	for _, tc := range cases {
		gotX, gotY, err := resolveScrollDelta(tc.direction, tc.dx, tc.dy)
		if tc.wantErrPresent {
			if err == nil {
				t.Errorf("direction=%q: expected error", tc.direction)
			}
			continue
		}
		if err != nil {
			t.Fatalf("direction=%q: unexpected error: %v", tc.direction, err)
		}
		if gotX != tc.wantX || gotY != tc.wantY {
			t.Errorf("direction=%q dx=%v dy=%v: got (%v,%v), want (%v,%v)", tc.direction, tc.dx, tc.dy, gotX, gotY, tc.wantX, tc.wantY)
		}
	}
}

func TestBrowserScrollTool_SelectorScrollsIntoView(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserScrollTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{"selector":"#footer"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "#footer") {
		t.Errorf("unexpected output: %q", out)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "scroll:#footer:0:0" {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserWaitTool_Args(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserWaitTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	if _, err := tool.Execute(context.Background(), `{"selector":"#done","timeout_ms":5000}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.calls) != 1 || !strings.HasPrefix(fake.calls[0], "wait:#done::false:5s") {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserCloseTool_CleanupCallsSessionRegardlessOfEnabled(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserCloseTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}
	if err := tool.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "close" {
		t.Errorf("Cleanup should call Session.Close even when the tool is disabled; calls=%v", fake.calls)
	}
}

func TestBrowserScreenshotTool_MultimodalGating(t *testing.T) {
	fake := &fakeBrowserSession{screenshotData: []byte("fake-png-bytes")}

	withImages := &BrowserScreenshotTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, SupportsImages: true}
	res, err := withImages.ExecuteMultimodal(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("ExecuteMultimodal: %v", err)
	}
	if len(res.Images) != 1 || res.Images[0].MediaType != "image/png" {
		t.Errorf("expected one png image block, got %+v", res.Images)
	}

	withoutImages := &BrowserScreenshotTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, SupportsImages: false}
	res2, err := withoutImages.ExecuteMultimodal(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("ExecuteMultimodal: %v", err)
	}
	if len(res2.Images) != 0 {
		t.Errorf("expected no image blocks when SupportsImages is false, got %+v", res2.Images)
	}
	if !strings.Contains(res2.Content, "does not support image input") {
		t.Errorf("expected degrade message, got %q", res2.Content)
	}
}

func TestBrowserClickTool_ReportsTabCount(t *testing.T) {
	fake := &fakeBrowserSession{status: browser.Status{CurrentURL: "https://example.com/popup", TabCount: 2}}
	tool := &BrowserClickTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{"selector":"#go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "tabs: 2") {
		t.Errorf("expected tab count in output, got %q", out)
	}
}

func TestBrowserTabsTool_Formatting(t *testing.T) {
	fake := &fakeBrowserSession{tabsResult: []browser.TabInfo{
		{Index: 0, Title: "Home", URL: "https://a.example", Active: false},
		{Index: 1, Title: "Popup", URL: "https://b.example", Active: true},
	}}
	tool := &BrowserTabsTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"https://a.example", "https://b.example", "Popup"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestBrowserTabsTool_Empty(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserTabsTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no tabs") {
		t.Errorf("expected an empty-tabs message, got %q", out)
	}
}

func TestBrowserSwitchTabTool_PropagatesArgsAndError(t *testing.T) {
	fake := &fakeBrowserSession{switchTabErr: fmt.Errorf("%w: index 3, have 1 tab(s)", browser.ErrTabNotFound)}
	tool := &BrowserSwitchTabTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	_, err := tool.Execute(context.Background(), `{"index":3}`)
	if !errors.Is(err, browser.ErrTabNotFound) {
		t.Errorf("expected errors.Is(err, ErrTabNotFound), got %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "switch_tab:3" {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserCloseTabTool_Success(t *testing.T) {
	fake := &fakeBrowserSession{status: browser.Status{TabCount: 1, CurrentURL: "https://a.example"}}
	tool := &BrowserCloseTabTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{"index":1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "tabs remaining: 1") || !strings.Contains(out, "https://a.example") {
		t.Errorf("unexpected output: %q", out)
	}
	if len(fake.calls) != 2 || fake.calls[0] != "close_tab:1" {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserCloseTabTool_PropagatesError(t *testing.T) {
	fake := &fakeBrowserSession{closeTabErr: errors.New("cannot close the only remaining tab; use browser_close to end the session instead")}
	tool := &BrowserCloseTabTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	_, err := tool.Execute(context.Background(), `{"index":0}`)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBrowserUploadTool_RequiresSelectorAndPaths(t *testing.T) {
	fake := &fakeBrowserSession{}
	cwd := NewCwdRef(t.TempDir())
	tool := &BrowserUploadTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	if _, err := tool.Execute(context.Background(), `{"paths":["a.txt"]}`); err == nil {
		t.Fatal("expected error for missing selector")
	}
	if _, err := tool.Execute(context.Background(), `{"selector":"#f"}`); err == nil {
		t.Fatal("expected error for missing paths")
	}
}

func TestBrowserUploadTool_PathOutsideWorkspaceDenied(t *testing.T) {
	fake := &fakeBrowserSession{}
	cwd := NewCwdRef(t.TempDir())
	tool := &BrowserUploadTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	_, err := tool.Execute(context.Background(), `{"selector":"#f","paths":["/etc/passwd"]}`)
	if err == nil {
		t.Fatal("expected a path-outside-workspace error")
	}
	if len(fake.calls) != 0 {
		t.Errorf("session should not have been touched for a denied path: %v", fake.calls)
	}
}

func TestBrowserUploadTool_Success(t *testing.T) {
	fake := &fakeBrowserSession{}
	dir := t.TempDir()
	cwd := NewCwdRef(dir)
	tool := &BrowserUploadTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	out, err := tool.Execute(context.Background(), `{"selector":"#f","paths":["a.txt","sub/b.txt"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "2 file(s)") {
		t.Errorf("unexpected output: %q", out)
	}
	wantCall := fmt.Sprintf("upload:#f:[%s %s]", filepath.Join(dir, "a.txt"), filepath.Join(dir, "sub", "b.txt"))
	if len(fake.calls) != 1 || fake.calls[0] != wantCall {
		t.Errorf("unexpected calls: %v, want [%s]", fake.calls, wantCall)
	}
}

func TestBrowserDownloadTool_RequiresExactlyOneOfSelectorOrURL(t *testing.T) {
	fake := &fakeBrowserSession{}
	cwd := NewCwdRef(t.TempDir())
	tool := &BrowserDownloadTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	if _, err := tool.Execute(context.Background(), `{"path":"out.bin"}`); err == nil {
		t.Fatal("expected error when neither selector nor url is set")
	}
	if _, err := tool.Execute(context.Background(), `{"selector":"#dl","url":"https://x","path":"out.bin"}`); err == nil {
		t.Fatal("expected error when both selector and url are set")
	}
}

func TestBrowserDownloadTool_PathOutsideWorkspaceDenied(t *testing.T) {
	fake := &fakeBrowserSession{}
	cwd := NewCwdRef(t.TempDir())
	tool := &BrowserDownloadTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	_, err := tool.Execute(context.Background(), `{"selector":"#dl","path":"/etc/passwd"}`)
	if err == nil {
		t.Fatal("expected a path-outside-workspace error")
	}
	if len(fake.calls) != 0 {
		t.Errorf("session should not have been touched for a denied path: %v", fake.calls)
	}
}

func TestBrowserDownloadTool_Success(t *testing.T) {
	dir := t.TempDir()
	cwd := NewCwdRef(dir)
	wantPath := filepath.Join(dir, "out.bin")
	fake := &fakeBrowserSession{downloadResult: browser.DownloadResult{Path: wantPath, SuggestedFilename: "report.bin", SizeBytes: 42}}
	tool := &BrowserDownloadTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	out, err := tool.Execute(context.Background(), `{"selector":"#dl","path":"out.bin"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"report.bin", wantPath, "42"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	if len(fake.calls) != 1 || fake.calls[0] != fmt.Sprintf("download:#dl::%s", wantPath) {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserDownloadTool_ErrorTaxonomy(t *testing.T) {
	fake := &fakeBrowserSession{downloadErr: fmt.Errorf("%w: no download event", browser.ErrDownloadFailed)}
	cwd := NewCwdRef(t.TempDir())
	tool := &BrowserDownloadTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}, Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}}
	_, err := tool.Execute(context.Background(), `{"url":"https://example.com/x.bin","path":"out.bin"}`)
	if !errors.Is(err, browser.ErrDownloadFailed) {
		t.Errorf("expected errors.Is(err, ErrDownloadFailed), got %v", err)
	}
}

func TestBrowserConsoleLogsTool_DefaultLimit(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserConsoleLogsTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	if _, err := tool.Execute(context.Background(), `{}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != fmt.Sprintf("console_logs:%d", browserLogLimitDefault) {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserConsoleLogsTool_Formatting(t *testing.T) {
	at := time.Date(2026, 9, 18, 12, 3, 4, 123000000, time.UTC)
	fake := &fakeBrowserSession{consoleLogsResult: []browser.ConsoleEntry{
		{Level: "error", Text: "Uncaught TypeError: x is not a function", At: at},
	}}
	tool := &BrowserConsoleLogsTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{"limit":10}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"[error]", "12:03:04.123", "Uncaught TypeError"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	if len(fake.calls) != 1 || fake.calls[0] != "console_logs:10" {
		t.Errorf("unexpected calls: %v", fake.calls)
	}
}

func TestBrowserConsoleLogsTool_Empty(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserConsoleLogsTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no console output buffered") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestBrowserNetworkRequestsTool_Formatting(t *testing.T) {
	fake := &fakeBrowserSession{networkRequestsResult: []browser.NetworkEntry{
		{Method: "GET", URL: "https://api.example.com/user", Status: 200, StatusText: "OK", MIMEType: "application/json"},
		{Method: "POST", URL: "https://api.example.com/x", Failed: true, ErrorText: "net::ERR_CONNECTION_REFUSED"},
	}}
	tool := &BrowserNetworkRequestsTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"GET", "200 OK", "https://api.example.com/user", "FAILED: net::ERR_CONNECTION_REFUSED"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestBrowserNetworkRequestsTool_Empty(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserNetworkRequestsTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no requests buffered") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestBrowserStatusTool_Formatting(t *testing.T) {
	fake := &fakeBrowserSession{status: browser.Status{BinaryPath: "/usr/bin/google-chrome", Active: true, CurrentURL: "https://example.com", ProfileDir: "/tmp/x"}}
	tool := &BrowserStatusTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"/usr/bin/google-chrome", "true", "https://example.com", "/tmp/x", "mode: headless"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestBrowserStatusTool_ReportsHeadedMode(t *testing.T) {
	fake := &fakeBrowserSession{status: browser.Status{Active: true, Headed: true}}
	tool := &BrowserStatusTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "mode: headed") {
		t.Errorf("output %q missing %q", out, "mode: headed")
	}
}

func TestBrowserHandoffTool_Success(t *testing.T) {
	fake := &fakeBrowserSession{handoffResult: browser.HandoffResult{URL: "https://streeteasy.com/for-rent/long-island-city"}}
	tool := &BrowserHandoffTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "handoff" {
		t.Errorf("calls = %v, want [handoff]", fake.calls)
	}
	// The result is what steers the model's next message to the user: it must
	// name the page, say the user does the verification, and forbid the model
	// from attempting the challenge itself.
	for _, want := range []string{
		"https://streeteasy.com/for-rent/long-island-city",
		"user must complete the verification themselves",
		"do not try to solve or click through it yourself",
		"wait for their reply",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestBrowserHandoffTool_AlreadyVisibleAndWarning(t *testing.T) {
	fake := &fakeBrowserSession{handoffResult: browser.HandoffResult{URL: "https://example.com", AlreadyVisible: true, LoadWarning: "navigation timed out"}}
	tool := &BrowserHandoffTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"already visible", "navigation timed out"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestBrowserHandoffTool_NoEarlierPage(t *testing.T) {
	fake := &fakeBrowserSession{}
	tool := &BrowserHandoffTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "blank page") {
		t.Errorf("output %q should say the window is blank", out)
	}
}

func TestBrowserHandoffTool_NoDisplayErrorIsActionable(t *testing.T) {
	fake := &fakeBrowserSession{handoffErr: fmt.Errorf("%w (no DISPLAY): paste what you need", browser.ErrNoDisplay)}
	tool := &BrowserHandoffTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	_, err := tool.Execute(context.Background(), `{}`)
	if !errors.Is(err, browser.ErrNoDisplay) {
		t.Fatalf("err = %v, want it to wrap ErrNoDisplay", err)
	}
	if !strings.Contains(err.Error(), "paste") {
		t.Errorf("error %q should tell the model what to do instead", err)
	}
}

// TestBrowserNavigateTool_DescriptionPointsAtHandoff guards the guidance
// that stops the model telling the user to "look for a browser window": the
// session is headless, and the only route to a human-solvable challenge is
// browser_handoff.
func TestBrowserNavigateTool_DescriptionPointsAtHandoff(t *testing.T) {
	desc := (&BrowserNavigateTool{}).Description()
	for _, want := range []string{"invisible to the user", "browser_handoff"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description %q missing %q", desc, want)
		}
	}
}

func TestBrowserHandoffTool_DescriptionForbidsBypass(t *testing.T) {
	desc := (&BrowserHandoffTool{}).Description()
	if !strings.Contains(desc, "does not solve, click through, or bypass") {
		t.Errorf("description %q must state that the tool does not bypass the challenge", desc)
	}
}

// TestBrowserHandoffTool_PreviewSaysWhatApprovalDoes: the preview is the
// entire approval prompt, so it must make clear that approving opens a
// window for the *user* to complete the check, not that anything is being
// approved or validated on the agent's behalf.
func TestBrowserHandoffTool_PreviewSaysWhatApprovalDoes(t *testing.T) {
	got := (&BrowserHandoffTool{browserToolBase{Enabled: true}}).PreviewCall(`{}`)
	for _, want := range []string{"browser_handoff", "window", "you can complete the verification yourself"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview %q missing %q", got, want)
		}
	}
}
