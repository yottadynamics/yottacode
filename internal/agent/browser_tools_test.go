package agent

import (
	"context"
	"errors"
	"fmt"
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
func (f *fakeBrowserSession) Inspect(ctx context.Context, selector string) (string, error) {
	f.calls = append(f.calls, "inspect:"+selector)
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

var _ browserSession = (*fakeBrowserSession)(nil)

// browserToolCases enumerates all 10 browser_* tools against a shared
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
		{"browser_close", &BrowserCloseTool{browserToolBase: base}, `{}`, false},
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
		{"browser_close", &BrowserCloseTool{browserToolBase: browserToolBase{Session: fake, Enabled: false}}, `{}`},
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
		"browser_wait", "browser_close",
	}
	if len(names) != 10 {
		t.Fatalf("test bug: expected 10 tool names, got %d", len(names))
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

func TestBrowserStatusTool_Formatting(t *testing.T) {
	fake := &fakeBrowserSession{status: browser.Status{BinaryPath: "/usr/bin/google-chrome", Active: true, CurrentURL: "https://example.com", ProfileDir: "/tmp/x"}}
	tool := &BrowserStatusTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"/usr/bin/google-chrome", "true", "https://example.com", "/tmp/x"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}
