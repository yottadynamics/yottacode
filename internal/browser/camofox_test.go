package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// camofoxFake is a scriptable Camofox server. It records every request as
// "METHOD path" plus its decoded JSON body.
type camofoxFake struct {
	srv *httptest.Server

	mu       syncutil.Mutex
	calls    []string
	bodies   map[string][]map[string]any // path -> bodies, in order
	queries  map[string][]string
	auth     []string
	snapshot string
	// snapshotFn, when set, computes the snapshot per call (1-based).
	snapshotFn    func(call int) string
	snapshotCalls int
	notFoundOnce  bool // the next navigate answers 404 (tab GC'd)
	tabsCreated   int
	healthy       atomic.Bool
}

func newCamofoxFake(t *testing.T) *camofoxFake {
	t.Helper()
	f := &camofoxFake{bodies: map[string][]map[string]any{}, queries: map[string][]string{}, snapshot: `[e1] button "Go"`}
	f.healthy.Store(true)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if body != nil {
			f.bodies[r.URL.Path] = append(f.bodies[r.URL.Path], body)
		}
		f.queries[r.URL.Path] = append(f.queries[r.URL.Path], r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			if !f.healthy.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/tabs":
			f.tabsCreated++
			_, _ = w.Write([]byte(`{"tabId":"tab-` + string(rune('0'+f.tabsCreated)) + `"}`))
		case strings.HasSuffix(r.URL.Path, "/navigate"):
			if f.notFoundOnce {
				f.notFoundOnce = false
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"tab not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"url":"` + body["url"].(string) + `","title":"Camofox Page"}`))
		case strings.HasSuffix(r.URL.Path, "/snapshot"):
			f.snapshotCalls++
			snap := f.snapshot
			if f.snapshotFn != nil {
				snap = f.snapshotFn(f.snapshotCalls)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"snapshot": snap, "refsCount": 1})
		case strings.HasSuffix(r.URL.Path, "/screenshot"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG-fake"))
		case strings.HasSuffix(r.URL.Path, "/click"), strings.HasSuffix(r.URL.Path, "/back"):
			_, _ = w.Write([]byte(`{"url":"https://after.example/"}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *camofoxFake) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// count is how many requests matched call exactly ("METHOD /path").
func (f *camofoxFake) count(call string) int {
	n := 0
	for _, c := range f.callList() {
		if c == call {
			n++
		}
	}
	return n
}

// nonHealthCalls is every request except the liveness probe Manager makes
// before each action.
func (f *camofoxFake) nonHealthCalls() int {
	n := 0
	for _, c := range f.callList() {
		if c != "GET /health" {
			n++
		}
	}
	return n
}

func (f *camofoxFake) lastBody(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.bodies[path]
	if len(b) == 0 {
		return nil
	}
	return b[len(b)-1]
}

func camofoxManager(t *testing.T, f *camofoxFake, key string) *Manager {
	t.Helper()
	m, err := NewManagerWithOptions(Options{
		Provider: ProviderCamofox,
		Camofox:  CamofoxOptions{URL: f.srv.URL + "/", APIKey: key},
	})
	if err != nil {
		t.Fatalf("NewManagerWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m
}

func TestCamofox_EndToEndThroughTheManager(t *testing.T) {
	f := newCamofoxFake(t)
	m := camofoxManager(t, f, "sekrit")
	ctx := context.Background()

	if st := m.Status(); !st.Remote || st.Provider != ProviderCamofox || st.Endpoint != f.srv.URL || st.Active {
		t.Errorf("Status before launch = %+v", st)
	}

	res, err := m.Navigate(ctx, "https://example.com/x", "load")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if res.URL != "https://example.com/x" || res.Title != "Camofox Page" {
		t.Errorf("Navigate = %+v", res)
	}
	// Launch opened a tab on the fresh identity and navigation reused it.
	if f.count("GET /health") != 1 || f.count("POST /tabs") != 1 || f.count("POST /tabs/tab-1/navigate") != 1 {
		t.Errorf("calls = %v", f.callList())
	}
	create := f.lastBody("/tabs")
	userID, _ := create["userId"].(string)
	if !strings.HasPrefix(userID, "yottacode_") || create["url"] != "about:blank" || create["listItemId"] == "" {
		t.Errorf("tab create body = %v", create)
	}
	if nav := f.lastBody("/tabs/tab-1/navigate"); nav["userId"] != userID || nav["url"] != "https://example.com/x" {
		t.Errorf("navigate body = %v", nav)
	}

	// Snapshot text passes straight through.
	out, err := m.Inspect(ctx, "", InspectOptions{})
	if err != nil || out != `[e1] button "Go"` {
		t.Fatalf("Inspect = %q, %v", out, err)
	}
	if q := f.queries["/tabs/tab-1/snapshot"]; len(q) != 1 || q[0] != "userId="+userID {
		t.Errorf("snapshot query = %v", q)
	}

	// Refs go to the server bare, with or without the @.
	if err := m.Click(ctx, "@e5"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if c := f.lastBody("/tabs/tab-1/click"); c["ref"] != "e5" || c["userId"] != userID {
		t.Errorf("click body = %v", c)
	}
	if st := m.Status(); st.CurrentURL != "https://after.example/" || st.TabCount != 1 {
		t.Errorf("the click's resulting URL should be tracked: %+v", st)
	}

	if err := m.Type(ctx, "@e2", "hello", true); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if b := f.lastBody("/tabs/tab-1/type"); b["ref"] != "e2" || b["text"] != "hello" {
		t.Errorf("type body = %v", b)
	}
	if b := f.lastBody("/tabs/tab-1/press"); b["key"] != "Enter" {
		t.Errorf("submit should press Enter, got %v", b)
	}
	if err := m.Hotkey(ctx, "Tab"); err != nil {
		t.Fatalf("Hotkey: %v", err)
	}
	if b := f.lastBody("/tabs/tab-1/press"); b["key"] != "Tab" {
		t.Errorf("press body = %v", b)
	}
	if err := m.Scroll(ctx, "", 0, 400); err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if b := f.lastBody("/tabs/tab-1/scroll"); b["direction"] != "down" {
		t.Errorf("scroll body = %v", b)
	}
	if err := m.Scroll(ctx, "", 0, -400); err != nil {
		t.Fatalf("Scroll up: %v", err)
	}
	if b := f.lastBody("/tabs/tab-1/scroll"); b["direction"] != "up" {
		t.Errorf("scroll body = %v", b)
	}
	back, err := m.Back(ctx)
	if err != nil || back.URL != "https://after.example/" {
		t.Errorf("Back = %+v, %v", back, err)
	}
	png, err := m.Screenshot(ctx, "", false)
	if err != nil || !strings.HasPrefix(string(png), "\x89PNG") {
		t.Errorf("Screenshot = %q, %v", png, err)
	}
	tabs, err := m.Tabs(ctx)
	if err != nil || len(tabs) != 1 || tabs[0].ID != "tab-1" || !tabs[0].Active {
		t.Errorf("Tabs = %+v, %v", tabs, err)
	}

	// Every request carried the bearer token.
	f.mu.Lock()
	for i, a := range f.auth {
		if a != "Bearer sekrit" {
			t.Errorf("request %d (%s) Authorization = %q", i, f.calls[i], a)
		}
	}
	f.mu.Unlock()

	// One identity throughout; closing deletes it server-side.
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if f.count("DELETE /sessions/"+userID) != 1 {
		t.Errorf("close should delete the identity once: %v", f.callList())
	}
}

func TestCamofox_RefusesWhatItCannotDo(t *testing.T) {
	f := newCamofoxFake(t)
	m := camofoxManager(t, f, "")
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	before := f.nonHealthCalls()

	unsupported := map[string]error{}
	unsupported["click by CSS selector"] = m.Click(ctx, "#go")
	unsupported["type by CSS selector"] = m.Type(ctx, "#q", "x", false)
	_, err := m.Eval(ctx, "1")
	unsupported["eval"] = err
	_, err = m.ScreenshotAnnotated(ctx, false)
	unsupported["annotated screenshot"] = err
	_, err = m.Screenshot(ctx, "#go", false)
	unsupported["element screenshot"] = err
	_, err = m.Inspect(ctx, "#go", InspectOptions{})
	unsupported["scoped inspect"] = err
	unsupported["scroll element"] = m.Scroll(ctx, "#go", 0, 0)
	unsupported["horizontal scroll"] = m.Scroll(ctx, "", 100, 0)
	unsupported["wait selector"] = m.Wait(ctx, "#go", "", false, time.Second)
	unsupported["wait network idle"] = m.Wait(ctx, "", "", true, time.Second)
	unsupported["upload"] = m.Upload(ctx, "e1", []string{"/tmp/x"})
	_, err = m.Download(ctx, "e1", "", t.TempDir()+"/o")
	unsupported["download"] = err
	for what, err := range unsupported {
		if !errors.Is(err, ErrUnsupportedRemote) {
			t.Errorf("%s: err = %v, want ErrUnsupportedRemote", what, err)
		}
	}
	// Refusals are decided locally: nothing but the liveness probe reached
	// the server.
	if after := f.nonHealthCalls(); after != before {
		t.Errorf("refused actions still made %d server calls: %v", after-before, f.callList())
	}

	if err := m.SwitchTab(ctx, 1); !errors.Is(err, ErrTabNotFound) {
		t.Errorf("SwitchTab(1): %v", err)
	}
	if err := m.SwitchTab(ctx, 0); err != nil {
		t.Errorf("SwitchTab(0): %v", err)
	}
	if err := m.CloseTab(ctx, 0); err == nil || !strings.Contains(err.Error(), "only remaining tab") {
		t.Errorf("CloseTab: %v", err)
	}

	// Console and network say they are unavailable instead of looking empty.
	logs, _ := m.ConsoleLogs(ctx, 0)
	if len(logs) != 1 || !strings.Contains(logs[0].Text, "not available") {
		t.Errorf("ConsoleLogs = %+v", logs)
	}
	reqs, _ := m.NetworkRequests(ctx, 0)
	if len(reqs) != 1 || !strings.Contains(reqs[0].URL, "not available") {
		t.Errorf("NetworkRequests = %+v", reqs)
	}
	// The dialog policy is accepted but has nothing to act on.
	if err := m.SetDialogPolicy(true, ""); err != nil {
		t.Errorf("SetDialogPolicy: %v", err)
	}
}

func TestCamofox_WaitForTextPollsTheSnapshot(t *testing.T) {
	f := newCamofoxFake(t)
	f.snapshotFn = func(call int) string {
		if call >= 3 {
			return "Order confirmed"
		}
		return "Loading…"
	}
	m := camofoxManager(t, f, "")
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Wait(ctx, "", "Order confirmed", false, 10*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if n := f.count("GET /tabs/tab-1/snapshot"); n < 3 {
		t.Errorf("snapshot polled %d times, want >= 3", n)
	}

	f2 := newCamofoxFake(t)
	m2 := camofoxManager(t, f2, "")
	if _, err := m2.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	err := m2.Wait(ctx, "", "never appears", false, 700*time.Millisecond)
	if !errors.Is(err, ErrSelectorNotFound) {
		t.Errorf("a wait that times out: err = %v, want ErrSelectorNotFound", err)
	}
}

func TestCamofox_RecoversFromAGarbageCollectedTab(t *testing.T) {
	f := newCamofoxFake(t)
	m := camofoxManager(t, f, "")
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com/1", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	f.mu.Lock()
	f.notFoundOnce = true
	f.mu.Unlock()
	res, err := m.Navigate(ctx, "https://example.com/2", "load")
	if err != nil {
		t.Fatalf("Navigate after the tab was collected: %v", err)
	}
	if res.URL != "https://example.com/2" {
		t.Errorf("Navigate = %+v", res)
	}
	if f.count("POST /tabs") != 2 {
		t.Errorf("a fresh tab should have been opened on the target: %v", f.callList())
	}
	if body := f.lastBody("/tabs"); body["url"] != "https://example.com/2" {
		t.Errorf("the replacement tab should open on the requested URL: %v", body)
	}
}

func TestCamofox_OversizedSnapshotSpillsAndIsCleanedUp(t *testing.T) {
	f := newCamofoxFake(t)
	f.snapshot = strings.Repeat("line of a very large snapshot\n", 2000)
	m := camofoxManager(t, f, "")
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	out, err := m.Inspect(ctx, "", InspectOptions{})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.Contains(out, "…[truncated]") {
		t.Fatalf("expected a truncated snapshot (len %d)", len(out))
	}
	const marker = "full snapshot saved to "
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("no spill path in:\n…%s", out[len(out)-200:])
	}
	path := strings.Fields(out[i+len(marker):])[0]
	data, err := os.ReadFile(path)
	if err != nil || string(data) != f.snapshot {
		t.Fatalf("spill file wrong: %v (len %d)", err, len(data))
	}
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("spill file survived close: %v", err)
	}
}

func TestCamofox_InteractiveOnlyIsSaidToBeIgnored(t *testing.T) {
	f := newCamofoxFake(t)
	m := camofoxManager(t, f, "")
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	out, err := m.Inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	if err != nil || !strings.Contains(out, "interactive_only is not available") || !strings.Contains(out, `[e1] button "Go"`) {
		t.Errorf("Inspect = %q, %v", out, err)
	}
}

func TestCamofox_LaunchFailures(t *testing.T) {
	ctx := context.Background()

	// No server URL configured.
	m, err := NewManagerWithOptions(Options{Provider: ProviderCamofox})
	if err != nil {
		t.Fatalf("construction must not need the URL: %v", err)
	}
	if _, err := m.Navigate(ctx, "https://example.com", "load"); !errors.Is(err, ErrProviderNotConfigured) {
		t.Errorf("no URL: err = %v, want ErrProviderNotConfigured", err)
	}

	// Server down.
	f := newCamofoxFake(t)
	f.healthy.Store(false)
	m2 := camofoxManager(t, f, "")
	if _, err := m2.Navigate(ctx, "https://example.com", "load"); !errors.Is(err, ErrLaunchFailed) {
		t.Errorf("unhealthy server: err = %v, want ErrLaunchFailed", err)
	}
	if m2.Status().Active {
		t.Error("a failed launch must leave no session behind")
	}

	// Unreachable server.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	m3, _ := NewManagerWithOptions(Options{Provider: ProviderCamofox, Camofox: CamofoxOptions{URL: dead.URL}})
	if _, err := m3.Navigate(ctx, "https://example.com", "load"); !errors.Is(err, ErrLaunchFailed) || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("dead server: %v", err)
	}
}

func TestCamofox_MetadataFloorStillApplies(t *testing.T) {
	f := newCamofoxFake(t)
	m := camofoxManager(t, f, "")
	if _, err := m.Navigate(context.Background(), "http://169.254.169.254/latest", "load"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("err = %v, want ErrBlockedURL", err)
	}
	if n := len(f.callList()); n != 0 {
		t.Errorf("a refused URL must not even reach the server (%d calls)", n)
	}
}

func TestCamofox_DeadServerIsDetectedAndRelaunches(t *testing.T) {
	f := newCamofoxFake(t)
	m := camofoxManager(t, f, "")
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	f.healthy.Store(false)
	if m.Status().Active {
		t.Error("an unhealthy server means the session is not active")
	}
	f.healthy.Store(true)
	if _, err := m.Navigate(ctx, "https://example.com/again", "load"); err != nil {
		t.Fatalf("Navigate after recovery: %v", err)
	}
}

func TestNewManagerWithOptions_CamofoxValidation(t *testing.T) {
	for _, o := range []Options{
		{Provider: ProviderCamofox, Stealth: true},
		{Provider: ProviderCamofox, ProxyURL: "http://p:1"},
	} {
		if _, err := NewManagerWithOptions(o); err == nil || !strings.Contains(err.Error(), "local browser only") {
			t.Errorf("%+v: err = %v", o, err)
		}
	}
	m, err := NewManagerWithOptions(Options{Provider: ProviderCamofox, Camofox: CamofoxOptions{URL: "http://user:pw@cam.example:9377/"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Status().Endpoint; got != "http://cam.example:9377" {
		t.Errorf("Endpoint = %q, want credentials stripped and no trailing slash", got)
	}
	if strings.Contains(m.Status().Endpoint, "pw") {
		t.Error("Status leaks the URL's credentials")
	}
}

func TestCamofoxHTTPError(t *testing.T) {
	f := newCamofoxFake(t)
	c := newCamofoxClient(CamofoxOptions{URL: f.srv.URL})
	err := c.do(context.Background(), http.MethodPost, "/nope", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("the fake answers unknown paths 200: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer bad.Close()
	err = newCamofoxClient(CamofoxOptions{URL: bad.URL}).do(context.Background(), http.MethodGet, "/x", nil, nil, nil, nil)
	var he *camofoxHTTPError
	if !errors.As(err, &he) || he.Status != http.StatusTeapot || len(he.Body) > 210 {
		t.Errorf("err = %v (body len %d)", err, len(he.Body))
	}
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer garbled.Close()
	var out map[string]any
	if err := newCamofoxClient(CamofoxOptions{URL: garbled.URL}).do(context.Background(), http.MethodGet, "/x", nil, nil, &out, nil); err == nil {
		t.Error("an unparseable JSON reply must be an error")
	}
}
