package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDialogToolRoutesAndValidates(t *testing.T) {
	f := &fakeBrowserSession{}
	tool := &BrowserDialogTool{enabledBase(f)}
	for args, want := range map[string]string{
		`{"action":"accept"}`:              "arm_dialog:true::false",
		`{"action":"accept","text":"Bob"}`: "arm_dialog:true:Bob:false",
		`{"action":"dismiss"}`:             "arm_dialog:false::false",
		`{"action":"clear"}`:               "arm_dialog:false::true",
	} {
		f.calls = nil
		out, err := tool.Execute(context.Background(), args)
		if err != nil || strings.Join(f.calls, ",") != want {
			t.Errorf("%s: calls=%v err=%v", args, f.calls, err)
		}
		if !strings.Contains(out, "armed") && !strings.Contains(out, "cleared") {
			t.Errorf("%s: out=%q", args, out)
		}
	}
	for _, bad := range []string{`{}`, `{"action":"maybe"}`, `{"action":"dismiss","text":"x"}`} {
		if _, err := tool.Execute(context.Background(), bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	f2 := &fakeBrowserSession{armErr: errors.New("no browser session is open")}
	if _, err := (&BrowserDialogTool{enabledBase(f2)}).Execute(context.Background(), `{"action":"accept"}`); err == nil || !strings.Contains(err.Error(), "no browser session") {
		t.Errorf("err=%v", err)
	}
}

func TestDialogToolPromptsAndPreviewShowsText(t *testing.T) {
	tool := &BrowserDialogTool{browserToolBase{Session: &fakeBrowserSession{}, Enabled: true}}
	if !tool.RequiresApproval("") {
		t.Error("answering a dialog must require approval")
	}
	if p := tool.PreviewCall(`{"action":"accept","text":"hunter2"}`); !strings.Contains(p, "accept") || !strings.Contains(p, "hunter2") {
		t.Errorf("preview = %q", p)
	}
	off := &BrowserDialogTool{browserToolBase{Session: &fakeBrowserSession{}}}
	if out, _ := off.Execute(context.Background(), `{"action":"accept"}`); out != browserDisabledMessage || off.RequiresApproval("") {
		t.Error("disabled tool must explain itself and not prompt")
	}
}
