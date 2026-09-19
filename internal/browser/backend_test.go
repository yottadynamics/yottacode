package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

func TestParseProxy(t *testing.T) {
	cases := []struct {
		raw            string
		wantServer     string
		wantUser, pass string
	}{
		{"http://proxy.example:8080", "http://proxy.example:8080", "", ""},
		{"https://proxy.example", "https://proxy.example", "", ""},
		{"socks5://127.0.0.1:1080", "socks5://127.0.0.1:1080", "", ""},
		{"http://ada:s3cret@proxy.example:3128", "http://proxy.example:3128", "ada", "s3cret"},
		{"http://ada@proxy.example:3128", "http://proxy.example:3128", "ada", ""},
		{"  http://proxy.example:8080  ", "http://proxy.example:8080", "", ""},
	}
	for _, c := range cases {
		spec, err := parseProxy(c.raw)
		if err != nil || spec == nil {
			t.Errorf("parseProxy(%q) = %v, %v", c.raw, spec, err)
			continue
		}
		if spec.server != c.wantServer || spec.user != c.wantUser || spec.pass != c.pass {
			t.Errorf("parseProxy(%q) = %+v", c.raw, *spec)
		}
		// The credentials must never be part of what is handed to Chrome.
		if strings.Contains(spec.server, "@") {
			t.Errorf("parseProxy(%q).server %q carries credentials", c.raw, spec.server)
		}
	}

	if spec, err := parseProxy("   "); spec != nil || err != nil {
		t.Errorf("an empty proxy means none: %v, %v", spec, err)
	}
}

func TestParseProxy_Rejects(t *testing.T) {
	for raw, want := range map[string]string{
		"ftp://proxy.example":             "not supported",
		"proxy.example:8080":              "not supported", // parses as scheme "proxy.example"
		"http://":                         "missing host",
		"socks5://ada:pw@proxy.example:1": "SOCKS",
		"http://ada:s3cret@bad host:80":   "", // any error is fine, but it must not echo the password
	} {
		_, err := parseProxy(raw)
		if err == nil {
			t.Errorf("parseProxy(%q) should fail", raw)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("parseProxy(%q) error %q should mention %q", raw, err, want)
		}
		if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "pw@") {
			t.Errorf("parseProxy(%q) error leaks the credentials: %v", raw, err)
		}
	}
}

func TestNewManagerWithOptions_Validation(t *testing.T) {
	if _, err := NewManagerWithOptions(Options{Provider: "chromatic"}); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("unknown provider: %v", err)
	}
	if _, err := NewManagerWithOptions(Options{ProxyURL: "ftp://x"}); err == nil {
		t.Error("a bad proxy URL must be rejected at construction")
	}
	for _, o := range []Options{
		{Provider: ProviderBrowserbase, Stealth: true},
		{Provider: ProviderBrowserbase, ProxyURL: "http://p:1"},
	} {
		if _, err := NewManagerWithOptions(o); err == nil || !strings.Contains(err.Error(), "local browser only") {
			t.Errorf("%+v: err = %v, want a local-only complaint", o, err)
		}
	}
	for _, p := range []string{"", ProviderLocal} {
		m, err := NewManagerWithOptions(Options{Provider: p, Stealth: true, ProxyURL: "http://p:1"})
		if err != nil {
			t.Fatalf("provider %q: %v", p, err)
		}
		if m.provider != ProviderLocal || m.newRemote != nil {
			t.Errorf("provider %q: manager = %+v", p, m)
		}
	}
	m, err := NewManagerWithOptions(Options{Provider: ProviderBrowserbase})
	if err != nil {
		t.Fatalf("browserbase without credentials must still construct (credentials are checked at launch): %v", err)
	}
	if m.provider != ProviderBrowserbase || m.newRemote == nil {
		t.Errorf("browserbase manager = %+v", m)
	}
}

func TestResolveIdleTimeout(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		0:                  defaultIdleTimeout,
		-1:                 0, // disabled
		5 * time.Minute:    5 * time.Minute,
		time.Nanosecond:    time.Nanosecond,
		defaultIdleTimeout: defaultIdleTimeout,
	} {
		if got := resolveIdleTimeout(in); got != want {
			t.Errorf("resolveIdleTimeout(%s) = %s, want %s", in, got, want)
		}
	}
	m, _ := NewManagerWithOptions(Options{IdleTimeout: -1})
	if m.idleTimeout != 0 {
		t.Errorf("a negative IdleTimeout must disable reaping, got %s", m.idleTimeout)
	}
}

func TestBuildStealthProfile(t *testing.T) {
	v := &proto.BrowserGetVersionResult{
		Product:   "HeadlessChrome/153.0.7000.12",
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/153.0.0.0 Safari/537.36",
	}
	sp := buildStealthProfile(v)
	if strings.Contains(sp.userAgent, "Headless") || !strings.Contains(sp.userAgent, "Chrome/153.0.0.0") {
		t.Errorf("userAgent = %q", sp.userAgent)
	}
	if sp.meta == nil || len(sp.meta.Brands) != 3 {
		t.Fatalf("meta = %+v", sp.meta)
	}
	for _, b := range append(append([]*proto.EmulationUserAgentBrandVersion{}, sp.meta.Brands...), sp.meta.FullVersionList...) {
		if strings.Contains(b.Brand, "Headless") {
			t.Errorf("brand %q leaks headless mode", b.Brand)
		}
	}
	if sp.meta.Brands[1].Brand != "Google Chrome" || sp.meta.Brands[1].Version != "153" {
		t.Errorf("major-version brand = %+v", sp.meta.Brands[1])
	}
	if sp.meta.FullVersionList[1].Version != "153.0.7000.12" {
		t.Errorf("full-version brand = %+v", sp.meta.FullVersionList[1])
	}
	if sp.meta.Mobile || sp.meta.Bitness != "64" || sp.platform == "" {
		t.Errorf("profile = %+v / meta %+v", sp, sp.meta)
	}
}

func TestGlobMatch(t *testing.T) {
	yes := [][2]string{
		{"*://169.254.169.254/*", "http://169.254.169.254/latest/meta-data"},
		{"*://169.254.169.254:*/*", "https://169.254.169.254:8443/x"},
		{"*", "anything"},
		{"a?c", "abc"},
		{"*x*", "x"},
		{"**", ""},
		{"http://h/*", "http://h/"},
	}
	no := [][2]string{
		{"*://169.254.169.254/*", "http://169.254.169.253/x"},
		{"*://169.254.169.254/*", "http://169.254.169.254"}, // no trailing slash: pattern needs one
		{"a?c", "ac"},
		{"a?c", "abbc"},
		{"http://h/*", "https://h/x"},
		{"", "x"},
	}
	for _, c := range yes {
		if !globMatch(c[0], c[1]) {
			t.Errorf("globMatch(%q, %q) = false, want true", c[0], c[1])
		}
	}
	for _, c := range no {
		if globMatch(c[0], c[1]) {
			t.Errorf("globMatch(%q, %q) = true, want false", c[0], c[1])
		}
	}
	if !globMatch("", "") {
		t.Error("an empty pattern matches the empty string")
	}
}

func TestURLBlocked(t *testing.T) {
	for _, u := range []string{
		"http://169.254.169.254/latest",
		"http://169.254.9.9/anything", // the /16, not just the enumerated hosts
		"http://metadata.google.internal/x",
		"http://[fd00:ec2::254]/x",
	} {
		if !urlBlocked(u) {
			t.Errorf("urlBlocked(%q) = false", u)
		}
	}
	for _, u := range []string{"http://localhost:3000/", "https://example.com/", "http://10.0.0.1/"} {
		if urlBlocked(u) {
			t.Errorf("urlBlocked(%q) = true", u)
		}
	}
}

// bbFake is a scriptable Browserbase API.
type bbFake struct {
	srv *httptest.Server

	mu       syncutil.Mutex
	bodies   []map[string]any
	paths    []string
	uris     []string
	keys     []string
	respond  func(call int, body map[string]any) (status int, resp string)
	releases atomic.Int32
}

func newBBFake(t *testing.T, respond func(call int, body map[string]any) (int, string)) *bbFake {
	t.Helper()
	f := &bbFake{respond: respond}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.uris = append(f.uris, r.RequestURI)
		f.keys = append(f.keys, r.Header.Get("X-BB-API-Key"))
		if strings.HasPrefix(r.URL.Path, "/v1/sessions/") {
			f.releases.Add(1)
			f.bodies = append(f.bodies, body)
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		f.bodies = append(f.bodies, body)
		call := len(f.bodies)
		f.mu.Unlock()
		status, resp := f.respond(call, body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func okBB(int, map[string]any) (int, string) {
	return 200, `{"id":"sess-9","connectUrl":"wss://connect.example/abc"}`
}

func bbClient(f *bbFake, o BrowserbaseOptions) *browserbaseClient {
	o.APIKey, o.ProjectID, o.BaseURL = "k-secret", "proj", f.srv.URL
	return newBrowserbaseClient(o)
}

func TestBrowserbaseCreate_SendsConfiguredFeatures(t *testing.T) {
	f := newBBFake(t, okBB)
	c := bbClient(f, BrowserbaseOptions{AdvancedStealth: true, SessionTimeoutSeconds: 900})
	s, err := c.create(context.Background())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.ID != "sess-9" || s.ConnectURL != "wss://connect.example/abc" || len(s.Degraded) != 0 {
		t.Errorf("session = %+v", s)
	}
	body := f.bodies[0]
	if body["projectId"] != "proj" || body["proxies"] != true || body["keepAlive"] != true || body["timeout"] != float64(900) {
		t.Errorf("body = %v", body)
	}
	if bs, _ := body["browserSettings"].(map[string]any); bs["advancedStealth"] != true {
		t.Errorf("browserSettings = %v", body["browserSettings"])
	}
	if f.paths[0] != "POST /v1/sessions" || f.keys[0] != "k-secret" {
		t.Errorf("request = %s key=%q", f.paths[0], f.keys[0])
	}
}

func TestBrowserbaseCreate_OptOutsAreHonored(t *testing.T) {
	f := newBBFake(t, okBB)
	c := bbClient(f, BrowserbaseOptions{NoProxies: true, NoKeepAlive: true})
	if _, err := c.create(context.Background()); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := f.bodies[0]
	for _, k := range []string{"proxies", "keepAlive", "browserSettings", "timeout"} {
		if _, ok := body[k]; ok {
			t.Errorf("body should not carry %q: %v", k, body)
		}
	}
}

func TestBrowserbaseCreate_DegradesOnPaymentRequired(t *testing.T) {
	// The plan lacks keep-alive AND proxies: both are dropped, in that
	// order, and reported.
	f := newBBFake(t, func(_ int, body map[string]any) (int, string) {
		if body["keepAlive"] == true || body["proxies"] == true {
			return http.StatusPaymentRequired, `{"error":"upgrade"}`
		}
		return okBB(0, nil)
	})
	c := bbClient(f, BrowserbaseOptions{})
	s, err := c.create(context.Background())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(s.Degraded) != 2 || s.Degraded[0] != "keep-alive" || s.Degraded[1] != "proxies" {
		t.Errorf("Degraded = %v, want [keep-alive proxies]", s.Degraded)
	}
	if len(f.bodies) != 3 {
		t.Fatalf("attempts = %d, want 3", len(f.bodies))
	}
	if _, ok := f.bodies[1]["keepAlive"]; ok || f.bodies[1]["proxies"] != true {
		t.Errorf("second attempt should drop only keepAlive: %v", f.bodies[1])
	}
	if _, ok := f.bodies[2]["proxies"]; ok {
		t.Errorf("third attempt should drop proxies too: %v", f.bodies[2])
	}
}

func TestBrowserbaseCreate_PaymentRequiredWithNothingToDropFails(t *testing.T) {
	f := newBBFake(t, func(int, map[string]any) (int, string) { return http.StatusPaymentRequired, `{"error":"no credits"}` })
	c := bbClient(f, BrowserbaseOptions{NoProxies: true, NoKeepAlive: true})
	_, err := c.create(context.Background())
	if !errors.Is(err, ErrLaunchFailed) || !strings.Contains(err.Error(), "402") {
		t.Errorf("err = %v, want ErrLaunchFailed mentioning 402", err)
	}
}

func TestBrowserbaseCreate_Errors(t *testing.T) {
	// HTTP failure: bounded body excerpt, and never the API key.
	f := newBBFake(t, func(int, map[string]any) (int, string) {
		return 500, strings.Repeat("boom ", 100)
	})
	_, err := bbClient(f, BrowserbaseOptions{}).create(context.Background())
	if !errors.Is(err, ErrLaunchFailed) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "k-secret") || len(err.Error()) > 400 {
		t.Errorf("error leaks the key or an unbounded body (len %d): %v", len(err.Error()), err)
	}

	// A 200 with no usable session.
	f2 := newBBFake(t, func(int, map[string]any) (int, string) { return 200, `{"id":"x"}` })
	if _, err := bbClient(f2, BrowserbaseOptions{}).create(context.Background()); !errors.Is(err, ErrLaunchFailed) {
		t.Errorf("a response without connectUrl: %v", err)
	}
	f3 := newBBFake(t, func(int, map[string]any) (int, string) { return 200, `not json` })
	if _, err := bbClient(f3, BrowserbaseOptions{}).create(context.Background()); !errors.Is(err, ErrLaunchFailed) {
		t.Errorf("a non-JSON response: %v", err)
	}

	// Transport failure.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c := newBrowserbaseClient(BrowserbaseOptions{APIKey: "k-secret", ProjectID: "p", BaseURL: dead.URL})
	if _, err := c.create(context.Background()); !errors.Is(err, ErrLaunchFailed) || strings.Contains(err.Error(), "k-secret") {
		t.Errorf("transport failure: %v", err)
	}
}

func TestBrowserbaseCreate_NotConfigured(t *testing.T) {
	for name, o := range map[string]BrowserbaseOptions{
		"no key":     {ProjectID: "p", APIKeyEnv: "BROWSERBASE_API_KEY"},
		"blank key":  {APIKey: "  ", ProjectID: "p"},
		"no project": {APIKey: "k"},
	} {
		_, err := newBrowserbaseClient(o).create(context.Background())
		if !errors.Is(err, ErrProviderNotConfigured) {
			t.Errorf("%s: err = %v, want ErrProviderNotConfigured", name, err)
		}
	}
	_, err := newBrowserbaseClient(BrowserbaseOptions{ProjectID: "p", APIKeyEnv: "MY_BB_KEY"}).create(context.Background())
	if err == nil || !strings.Contains(err.Error(), "$MY_BB_KEY") {
		t.Errorf("the missing-key error should name the env var: %v", err)
	}
}

func TestBrowserbaseRelease(t *testing.T) {
	f := newBBFake(t, okBB)
	c := bbClient(f, BrowserbaseOptions{})
	if err := c.release(context.Background(), "sess/with space"); err != nil {
		t.Fatalf("release: %v", err)
	}
	// The id is path-escaped: a hostile id cannot climb out of /v1/sessions/.
	if got := f.uris[0]; got != "/v1/sessions/sess%2Fwith%20space" {
		t.Errorf("release request URI = %q, want the id escaped into one path segment", got)
	}
	if b := f.bodies[0]; b["status"] != "REQUEST_RELEASE" || b["projectId"] != "proj" {
		t.Errorf("release body = %v", b)
	}
	if err := c.release(context.Background(), ""); err != nil || f.releases.Load() != 1 {
		t.Errorf("releasing an empty id must be a no-op")
	}

	bad := newBBFake(t, func(int, map[string]any) (int, string) { return 200, "" })
	bad.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	if err := bbClient(bad, BrowserbaseOptions{}).release(context.Background(), "s"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a failed release should report the status: %v", err)
	}
}

// fakeRemoteSession is a Manager-level remote session: a fakeSession that
// also counts how it was torn down.
func TestManager_RemoteLaunchSkipsTheLocalMachinery(t *testing.T) {
	fake := &fakeSession{}
	var launches atomic.Int32
	m := &Manager{
		newRemote: func(context.Context) (pageSession, error) {
			launches.Add(1)
			return fake, nil
		},
		provider: ProviderBrowserbase,
		findBinary: func() (string, error) {
			t.Error("a remote provider must not look for a local binary")
			return "", nil
		},
		mkProfileDir: func() (string, error) {
			t.Error("a remote provider must not create a local profile dir")
			return "", nil
		},
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Error("a remote provider must not launch a local process")
			return nil, nil
		},
	}
	ctx := context.Background()

	st := m.Status()
	if !st.Remote || st.Provider != ProviderBrowserbase || st.BinaryPath != "" || st.Active {
		t.Errorf("Status before launch = %+v", st)
	}
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := m.Navigate(ctx, "https://example.com/2", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if launches.Load() != 1 {
		t.Errorf("launches = %d, want the one session reused", launches.Load())
	}
	if st := m.Status(); !st.Active || !st.Remote || st.ProfileDir != "" {
		t.Errorf("Status after launch = %+v", st)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !hasCall(fake, "close") {
		t.Errorf("Close must tear the remote session down: %v", fake.calls)
	}
}

func TestManager_RemoteLaunchErrorPropagates(t *testing.T) {
	m := &Manager{newRemote: func(context.Context) (pageSession, error) {
		return nil, ErrProviderNotConfigured
	}}
	if _, err := m.Navigate(context.Background(), "https://example.com", "load"); !errors.Is(err, ErrProviderNotConfigured) {
		t.Errorf("err = %v", err)
	}
	if m.Status().Active {
		t.Error("a failed launch must leave no session behind")
	}
}

func TestManager_RemoteCrashRelaunches(t *testing.T) {
	first, second := &fakeSession{}, &fakeSession{}
	sessions := []*fakeSession{first, second}
	var n int
	m := &Manager{newRemote: func(context.Context) (pageSession, error) {
		s := sessions[n]
		n++
		return s, nil
	}}
	ctx := context.Background()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	first.mu.Lock()
	first.dead = true
	first.mu.Unlock()
	if _, err := m.Navigate(ctx, "https://example.com", "load"); err != nil {
		t.Fatalf("Navigate after the remote session died: %v", err)
	}
	if n != 2 {
		t.Errorf("launches = %d, want a relaunch", n)
	}
	if !hasCall(first, "forceCleanup") {
		t.Errorf("the dead remote session must be cleaned up (its provider session released): %v", first.calls)
	}
}

func TestRemoteFileErr(t *testing.T) {
	err := remoteFileErr("upload")
	if !errors.Is(err, ErrUnsupportedRemote) || !strings.Contains(err.Error(), "browser_upload") {
		t.Errorf("remoteFileErr = %v", err)
	}
	// A bare remote session refuses file actions without touching CDP.
	s := &session{remote: &remoteHandle{label: "x"}}
	if err := s.setFiles(context.Background(), "#f", []string{"a"}); !errors.Is(err, ErrUnsupportedRemote) {
		t.Errorf("setFiles: %v", err)
	}
	if _, err := s.downloadViaClick(context.Background(), "#d", t.TempDir()); !errors.Is(err, ErrUnsupportedRemote) {
		t.Errorf("downloadViaClick: %v", err)
	}
	if _, err := s.downloadViaURL(context.Background(), "http://x", t.TempDir()); !errors.Is(err, ErrUnsupportedRemote) {
		t.Errorf("downloadViaURL: %v", err)
	}
}

func TestSession_ReleaseRemoteRunsTheHandleOnce(t *testing.T) {
	var calls atomic.Int32
	s := &session{remote: &remoteHandle{label: "x", release: func(context.Context) error {
		calls.Add(1)
		return errors.New("late")
	}}}
	if err := s.releaseRemote(); err == nil {
		t.Error("a release failure must surface")
	}
	if calls.Load() != 1 {
		t.Errorf("release calls = %d", calls.Load())
	}
	if err := (&session{}).releaseRemote(); err != nil {
		t.Errorf("a local session has nothing to release: %v", err)
	}
}

func TestStealthUserAgent(t *testing.T) {
	osPart := "X11; Linux x86_64"
	if runtime.GOOS == "darwin" {
		osPart = "Macintosh; Intel Mac OS X 10_15_7"
	}
	want := "Mozilla/5.0 (" + osPart + ") AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36"
	for _, in := range []string{"153.0.7000.12", " 153.0.7000.12 ", "153"} {
		if got := stealthUserAgent(in); got != want {
			t.Errorf("stealthUserAgent(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "  ", "abc.1.2", ".1.2.3", "Chrome/153"} {
		if got := stealthUserAgent(in); got != "" {
			t.Errorf("stealthUserAgent(%q) = %q, want empty", in, got)
		}
	}
	if strings.Contains(stealthUserAgent("153.0.7000.12"), "Headless") {
		t.Error("the stealth user agent must never say Headless")
	}
}

func TestLaunchUserAgent_ReadsTheBinaryVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()

	google := write("google", `echo "Google Chrome 153.0.7000.12 "`)
	if got := launchUserAgent(ctx, google); got != stealthUserAgent("153.0.7000.12") {
		t.Errorf("Google Chrome banner: %q", got)
	}
	chromium := write("chromium", `echo "Chromium 141.0.6000.1 built on Debian 12"`)
	if got := launchUserAgent(ctx, chromium); !strings.Contains(got, "Chrome/141.0.0.0") {
		t.Errorf("Chromium banner: %q", got)
	}

	// Anything unreadable degrades to "no flag" rather than failing the launch.
	for name, body := range map[string]string{
		"noversion": `echo "hello"`,
		"fails":     `exit 3`,
		"empty":     `true`,
	} {
		if got := launchUserAgent(ctx, write(name, body)); got != "" {
			t.Errorf("%s: launchUserAgent = %q, want empty", name, got)
		}
	}
	if got := launchUserAgent(ctx, filepath.Join(dir, "does-not-exist")); got != "" {
		t.Errorf("missing binary: %q", got)
	}
}

// A base URL that is sent an API key must not put the key on the wire in the
// clear: https, or http only to this machine.
func TestValidateSecretBearingURL(t *testing.T) {
	for _, ok := range []string{
		"", "  ", "https://api.browserbase.com", "https://gw.example:8443/base",
		"http://localhost:8080", "http://127.0.0.1:9999", "http://[::1]:80", "http://app.localhost",
	} {
		if err := validateSecretBearingURL("browserbase base_url", ok); err != nil {
			t.Errorf("validateSecretBearingURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"http://api.browserbase.com", "http://192.168.1.10:8080", "http://10.0.0.5", "ftp://x.example",
		"gw.example", "https://", "http://user:pw@evil.example",
	} {
		err := validateSecretBearingURL("browserbase base_url", bad)
		if err == nil {
			t.Errorf("validateSecretBearingURL(%q) = nil, want an error", bad)
			continue
		}
		if strings.Contains(err.Error(), "pw@") || strings.Contains(err.Error(), "evil.example") {
			t.Errorf("the error echoes the URL (it can carry credentials): %v", err)
		}
	}
	if _, err := NewManagerWithOptions(Options{Provider: ProviderBrowserbase, Browserbase: BrowserbaseOptions{BaseURL: "http://gw.example"}}); err == nil {
		t.Error("a cleartext non-local Browserbase base_url must be rejected at construction")
	}
	if _, err := NewManagerWithOptions(Options{Provider: ProviderBrowserbase, Browserbase: BrowserbaseOptions{BaseURL: "http://127.0.0.1:1234"}}); err != nil {
		t.Errorf("a loopback base_url (a local gateway or test double) must be allowed: %v", err)
	}
}

// If the caller gives up while the dial is in flight, a connection that lands
// afterwards must be closed, not leaked.
func TestConnectBrowser_ContextCancelDoesNotLeakALateConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	br := rod.New().ControlURL("ws://127.0.0.1:1/devtools/browser/none")
	if err := connectBrowser(ctx, br); err == nil {
		t.Fatal("a cancelled context must fail the connect")
	}
}
