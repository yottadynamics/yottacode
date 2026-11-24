package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/browser"
)

func TestClickReportsPageChanges(t *testing.T) {
	f := &fakeBrowserSession{digests: []browser.PageDigest{
		{Valid: true, URL: "u", Elements: []string{`radio "B"`}},
		{Valid: true, URL: "u", Elements: []string{`radio "B" (checked)`}},
	}}
	tool := &BrowserClickTool{enabledBase(f)}
	out, err := tool.Execute(context.Background(), `{"selector":"ref=e1"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"clicked", "page changes:", `changed: radio "B" -> radio "B" (checked)`, "untrusted_web_content"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestClickWithoutChangesIsUnchanged(t *testing.T) {
	f := &fakeBrowserSession{}
	out, err := (&BrowserClickTool{enabledBase(f)}).Execute(context.Background(), `{"selector":"#a"}`)
	if err != nil || strings.Contains(out, "page changes") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got := strings.Join(f.calls, ","); !strings.HasPrefix(got, "click:#a") {
		t.Errorf("plain click must use Click, calls=%s", got)
	}
}

func TestClickOptionsRouteToClickWith(t *testing.T) {
	f := &fakeBrowserSession{}
	tool := &BrowserClickTool{enabledBase(f)}
	if _, err := tool.Execute(context.Background(), `{"selector":"#a","button":"right"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), `{"selector":"#a","double":true}`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.calls, ","), "click_with:#a:right:false") || !strings.Contains(strings.Join(f.calls, ","), "click_with:#a::true") {
		t.Errorf("calls=%v", f.calls)
	}
	if _, err := tool.Execute(context.Background(), `{"selector":"#a","button":"back"}`); err == nil {
		t.Error("unknown button accepted")
	}
}

func TestClickErrorSkipsChanges(t *testing.T) {
	f := &fakeBrowserSession{clickErr: errors.New("boom")}
	if _, err := (&BrowserClickTool{enabledBase(f)}).Execute(context.Background(), `{"selector":"#a"}`); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestInspectModeInteractive(t *testing.T) {
	f := &fakeBrowserSession{inspectText: "button \"x\" [ref=e1]"}
	tool := &BrowserInspectTool{enabledBase(f)}
	if _, err := tool.Execute(context.Background(), `{"mode":"interactive"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "inspect_mode::interactive,inspect:" {
		t.Errorf("calls=%s", got)
	}
	if _, err := tool.Execute(context.Background(), `{"mode":"bogus"}`); err == nil {
		t.Error("bogus mode accepted")
	}
}

func TestScreenshotAnnotateRouting(t *testing.T) {
	f := &fakeBrowserSession{screenshotData: []byte("png")}
	tool := &BrowserScreenshotTool{browserToolBase: enabledBase(f), SupportsImages: true}
	for _, args := range []string{`{"annotate":true}`, `{"annotate":true,"full_page":true}`, `{"annotate":true,"selector":"#x"}`} {
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(f.calls, ","); got != "screenshot_annotated,screenshot:,screenshot:#x" {
		t.Errorf("calls=%s", got)
	}
}

func TestFillFormRunsInOrderAndSubmits(t *testing.T) {
	f := &fakeBrowserSession{}
	tool := &BrowserFillFormTool{enabledBase(f)}
	out, err := tool.Execute(context.Background(), `{"fields":[
		{"selector":"#u","value":"bob"},
		{"selector":"#c","kind":"select","value":"US"},
		{"selector":"#agree","kind":"click"}],"submit":"#go"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "type:#u:bob:false,select:#c:US:US,click:#agree,click:#go"
	if got := strings.Join(f.calls, ","); !strings.HasPrefix(got, want) {
		t.Errorf("calls=%s want prefix %s", got, want)
	}
	if !strings.Contains(out, "filled 3 field(s) and clicked") {
		t.Errorf("out=%q", out)
	}
}

func TestFillFormStopsAtFirstFailure(t *testing.T) {
	f := &fakeBrowserSession{typeErr: errors.New("no such field")}
	_, err := (&BrowserFillFormTool{enabledBase(f)}).Execute(context.Background(), `{"fields":[{"selector":"#a","value":"x"},{"selector":"#b","value":"y"}]}`)
	if err == nil || !strings.Contains(err.Error(), "field 1 (#a) failed after 0 of 2") {
		t.Fatalf("err=%v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("must stop after first failure, calls=%v", f.calls)
	}
}

func TestFillFormValidation(t *testing.T) {
	tool := &BrowserFillFormTool{enabledBase(&fakeBrowserSession{})}
	for name, args := range map[string]string{
		"empty":    `{"fields":[]}`,
		"noselect": `{"fields":[{"value":"x"}]}`,
		"badkind":  `{"fields":[{"selector":"#a","kind":"fly"}]}`,
		"toomany":  `{"fields":[` + strings.Repeat(`{"selector":"#a"},`, maxFillFields) + `{"selector":"#a"}]}`,
	} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFillFormPreviewShowsValues(t *testing.T) {
	p := (&BrowserFillFormTool{}).PreviewCall(`{"fields":[{"selector":"#pw","value":"hunter2"},{"selector":"#c","kind":"select","value":"US"}],"submit":"#go"}`)
	for _, want := range []string{"hunter2", "select", "#go"} {
		if !strings.Contains(p, want) {
			t.Errorf("preview %q lacks %q", p, want)
		}
	}
}

func TestFindAndExtractWrapAndValidate(t *testing.T) {
	f := &fakeBrowserSession{findText: `radio "B" [ref=e3]`, extractText: "# Title\n\nbody"}
	find := &BrowserFindTool{enabledBase(f)}
	out, err := find.Execute(context.Background(), `{"text":"B","role":"radio"}`)
	if err != nil || !strings.Contains(out, "untrusted_web_content") || !strings.Contains(out, "[ref=e3]") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := find.Execute(context.Background(), `{}`); err == nil {
		t.Error("find with neither text nor role accepted")
	}
	ex := &BrowserExtractTool{enabledBase(f)}
	out, err = ex.Execute(context.Background(), `{}`)
	if err != nil || !strings.Contains(out, "untrusted_web_content") || !strings.Contains(out, "# Title") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestHoverAndDragReportChangesAndValidate(t *testing.T) {
	f := &fakeBrowserSession{digests: []browser.PageDigest{{Valid: true}, {Valid: true, Elements: []string{`menuitem "Settings"`}}}}
	out, err := (&BrowserHoverTool{enabledBase(f)}).Execute(context.Background(), `{"selector":"#menu"}`)
	if err != nil || !strings.Contains(out, `added: menuitem "Settings"`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := (&BrowserHoverTool{enabledBase(f)}).Execute(context.Background(), `{}`); err == nil {
		t.Error("hover without selector accepted")
	}
	if _, err := (&BrowserDragTool{enabledBase(f)}).Execute(context.Background(), `{"from":"#a"}`); err == nil {
		t.Error("drag without target accepted")
	}
	if _, err := (&BrowserDragTool{enabledBase(f)}).Execute(context.Background(), `{"from":"#a","to":"#b"}`); err != nil {
		t.Fatal(err)
	}
}

func TestNewToolsDisabledMessage(t *testing.T) {
	base := browserToolBase{Session: &fakeBrowserSession{}}
	for _, tool := range []Tool{&BrowserHoverTool{base}, &BrowserDragTool{base}, &BrowserFindTool{base}, &BrowserExtractTool{base}, &BrowserFillFormTool{base}} {
		out, err := tool.Execute(context.Background(), `{}`)
		if err != nil || out != browserDisabledMessage {
			t.Errorf("%s: %q %v", tool.Name(), out, err)
		}
		if tool.RequiresApproval("") {
			t.Errorf("%s: disabled tool must not prompt", tool.Name())
		}
	}
}

func TestScrollUntilEndRoutesAndReports(t *testing.T) {
	f := &fakeBrowserSession{scrollEndResult: browser.ScrollResult{Steps: 4, StartHeight: 900, EndHeight: 5200, ReachedEnd: true},
		digests: []browser.PageDigest{{Valid: true}, {Valid: true, Elements: []string{`link "Post 40"`}}}}
	out, err := (&BrowserScrollTool{enabledBase(f)}).Execute(context.Background(), `{"until_end":true,"max_steps":5}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "scroll_until_end::5" {
		t.Errorf("calls=%s", got)
	}
	for _, want := range []string{"the page", "4 step(s)", "900 -> 5200", "reached the end", `added: link "Post 40"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	f2 := &fakeBrowserSession{scrollEndResult: browser.ScrollResult{Steps: 10, StartHeight: 1, EndHeight: 2}}
	out, _ = (&BrowserScrollTool{enabledBase(f2)}).Execute(context.Background(), `{"until_end":true,"selector":"#feed"}`)
	if !strings.Contains(out, `"#feed"`) || !strings.Contains(out, "max_steps") || strings.Contains(out, "reached the end") {
		t.Errorf("capped run must say it can continue: %s", out)
	}
	f3 := &fakeBrowserSession{scrollEndResult: browser.ScrollResult{Steps: 3, TimedOut: true}}
	out, _ = (&BrowserScrollTool{enabledBase(f3)}).Execute(context.Background(), `{"until_end":true}`)
	if !strings.Contains(out, "time limit") {
		t.Errorf("timed-out run must say so: %s", out)
	}
	// Without until_end the old behaviour is untouched.
	f4 := &fakeBrowserSession{}
	if _, err := (&BrowserScrollTool{enabledBase(f4)}).Execute(context.Background(), `{"direction":"down"}`); err != nil || strings.Contains(strings.Join(f4.calls, ","), "scroll_until_end") {
		t.Errorf("plain scroll changed: %v %v", err, f4.calls)
	}
	if p := (&BrowserScrollTool{}).PreviewCall(`{"until_end":true}`); !strings.Contains(p, "until_end") {
		t.Errorf("preview=%q", p)
	}
}

func TestScrollUntilEndReportsLoadMoreControl(t *testing.T) {
	f := &fakeBrowserSession{scrollEndResult: browser.ScrollResult{Steps: 2, StartHeight: 1, EndHeight: 1, ReachedEnd: true, LoadMore: `button "Load more" [ref=e12]`}}
	out, err := (&BrowserScrollTool{enabledBase(f)}).Execute(context.Background(), `{"until_end":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `button "Load more" [ref=e12]`) || !strings.Contains(out, "untrusted_web_content") || !strings.Contains(out, "click it") {
		t.Errorf("load more not reported safely:\n%s", out)
	}
	f.scrollEndResult.LoadMore = ""
	out, _ = (&BrowserScrollTool{enabledBase(f)}).Execute(context.Background(), `{"until_end":true}`)
	if strings.Contains(out, "load more") {
		t.Errorf("no control, no note:\n%s", out)
	}
}
