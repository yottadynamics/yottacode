package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/browser"
)

// An upload is a read of the file: its bytes go to a web page. Every read tool
// refuses credential files (DefaultDenyReadPaths); browser_upload used to check
// only the WRITE boundary, so ./.env — inside the workspace, so "allowed" — could
// be handed to any page even though read_file refuses it.
func TestBrowserUploadTool_HonorsReadDenyList(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("AWS_SECRET_ACCESS_KEY=CANARY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("harmless"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := NewCwdRef(root)
	newTool := func(fake *fakeBrowserSession) *BrowserUploadTool {
		return &BrowserUploadTool{
			browserToolBase: browserToolBase{Session: fake, Enabled: true},
			Cwd:             cwd,
			WriteOpts:       WritePathOptions{Cwd: cwd},
			DenyReadPaths:   DefaultDenyReadPaths(root),
		}
	}

	// Precondition: this really is a file the read tools refuse.
	if err := ValidateReadPath(filepath.Join(root, ".env"), DefaultDenyReadPaths(root)); err == nil {
		t.Fatal("test setup: .env should be on the read deny list")
	}

	fake := &fakeBrowserSession{}
	_, err := newTool(fake).Execute(context.Background(), `{"selector":"#f","paths":[".env"]}`)
	if err == nil || !strings.Contains(err.Error(), "read deny list") {
		t.Fatalf("uploading .env: err = %v, want a read-deny-list refusal", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("a denied upload must never reach the browser: %v", fake.calls)
	}

	// One denied path poisons the whole batch: nothing is uploaded.
	fake = &fakeBrowserSession{}
	if _, err := newTool(fake).Execute(context.Background(), `{"selector":"#f","paths":["notes.txt",".env"]}`); err == nil {
		t.Fatal("a batch containing .env must be refused")
	}
	if len(fake.calls) != 0 {
		t.Errorf("no file from a refused batch may be uploaded: %v", fake.calls)
	}

	// An ordinary file still uploads.
	fake = &fakeBrowserSession{}
	if _, err := newTool(fake).Execute(context.Background(), `{"selector":"#f","paths":["notes.txt"]}`); err != nil {
		t.Fatalf("uploading an ordinary file: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("expected one upload call, got %v", fake.calls)
	}
}

// The registered tool must carry the deny list (a struct field left unset
// would silently disable the check).
func TestRegisterCoreCwdTools_BrowserUploadGetsReadDenyList(t *testing.T) {
	root := t.TempDir()
	cwd := NewCwdRef(root)
	deny := DefaultDenyReadPaths(root)
	reg := NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{
		WriteOpts: WritePathOptions{Cwd: cwd}, DenyReads: deny,
		EnableBrowser: true, BrowserSession: &fakeBrowserSession{},
	})
	tool, ok := reg.Get("browser_upload")
	if !ok {
		t.Fatal("browser_upload not registered")
	}
	up, ok := tool.(*BrowserUploadTool)
	if !ok {
		t.Fatalf("browser_upload is %T", tool)
	}
	if len(up.DenyReadPaths) == 0 || len(up.DenyReadPaths) != len(deny) {
		t.Errorf("DenyReadPaths = %v, want the session read deny list %v", up.DenyReadPaths, deny)
	}
}

// Consent is only meaningful if the prompt shows what is being approved. These
// three tools used to show only a selector or a destination.
func TestBrowserPreviews_ShowWhatIsBeingApproved(t *testing.T) {
	typ := (&BrowserTypeTool{}).PreviewCall(`{"selector":"#q","text":"correct horse battery staple","submit":true}`)
	for _, want := range []string{"#q", `"correct horse battery staple"`, "submit"} {
		if !strings.Contains(typ, want) {
			t.Errorf("browser_type preview %q missing %q", typ, want)
		}
	}

	up := (&BrowserUploadTool{}).PreviewCall(`{"selector":"#f","paths":["report.pdf","data/q3.csv"]}`)
	for _, want := range []string{"#f", `"report.pdf"`, `"data/q3.csv"`} {
		if !strings.Contains(up, want) {
			t.Errorf("browser_upload preview %q missing %q", up, want)
		}
	}

	dlURL := (&BrowserDownloadTool{}).PreviewCall(`{"url":"https://example.com/export.csv","path":"out/export.csv"}`)
	for _, want := range []string{"https://example.com/export.csv", "out/export.csv"} {
		if !strings.Contains(dlURL, want) {
			t.Errorf("browser_download (url) preview %q missing %q", dlURL, want)
		}
	}
	dlClick := (&BrowserDownloadTool{}).PreviewCall(`{"selector":"#export","path":"out.csv"}`)
	for _, want := range []string{"click #export", "out.csv"} {
		if !strings.Contains(dlClick, want) {
			t.Errorf("browser_download (click) preview %q missing %q", dlClick, want)
		}
	}
}

// A huge argument must not push the rest of the prompt off screen, and a
// truncated tail must never be invisible: the real length is shown.
func TestBrowserPreviews_AreBoundedButHonest(t *testing.T) {
	args, err := json.Marshal(map[string]any{"selector": "#q", "text": strings.Repeat("A", 5000)})
	if err != nil {
		t.Fatal(err)
	}
	got := (&BrowserTypeTool{}).PreviewCall(string(args))
	if len(got) > 400 {
		t.Errorf("preview is %d bytes; a huge payload must be truncated", len(got))
	}
	if !strings.Contains(got, "5000 chars") {
		t.Errorf("preview %q must show the true length of the truncated text", got)
	}

	// Many files: shown up to a cap, with the true count.
	var paths []string
	for i := 0; i < 12; i++ {
		paths = append(paths, "file"+string(rune('a'+i))+".txt")
	}
	upArgs, err := json.Marshal(map[string]any{"selector": "#f", "paths": paths})
	if err != nil {
		t.Fatal(err)
	}
	up := (&BrowserUploadTool{}).PreviewCall(string(upArgs))
	if !strings.Contains(up, "+7 more") {
		t.Errorf("preview %q should say how many files are not shown", up)
	}
}

// Control characters must show up escaped, not silently reflow the prompt (a
// newline could otherwise fake a second, innocent-looking line).
func TestBrowserPreviews_EscapeControlCharacters(t *testing.T) {
	text := "line1" + string(rune(10)) + "rm -rf /" + string(rune(7))
	args, err := json.Marshal(map[string]any{"selector": "#q", "text": text})
	if err != nil {
		t.Fatal(err)
	}
	got := (&BrowserTypeTool{}).PreviewCall(string(args))
	for _, r := range got {
		if r < 32 {
			t.Fatalf("preview contains a raw control character (%d): %q", r, got)
		}
	}
	if !strings.Contains(got, "line1") || !strings.Contains(got, "rm -rf /") {
		t.Errorf("preview %q should still show the text", got)
	}
}

// browser_navigate's error for a refused URL is what the model reads; it must
// come through the tool intact so the model can recover.
func TestBrowserNavigateTool_SurfacesBlockedURL(t *testing.T) {
	fake := &fakeBrowserSession{navigateErr: browser.ErrBlockedURL}
	tool := &BrowserNavigateTool{browserToolBase: browserToolBase{Session: fake, Enabled: true}}
	_, err := tool.Execute(context.Background(), `{"url":"file:///etc/passwd"}`)
	if err == nil || !strings.Contains(err.Error(), "blocked URL") {
		t.Errorf("err = %v, want it to carry the blocked-URL reason", err)
	}
}
